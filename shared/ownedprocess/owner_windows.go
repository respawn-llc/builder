//go:build windows

package ownedprocess

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const processTerminateExitCode = 1

type windowsProcessTree struct {
	process windows.Handle
	thread  windows.Handle
	job     windows.Handle
	stdio   *windowsStdio

	handleMu      sync.Mutex
	terminateOnce sync.Once
	terminateErr  error
	closeOnce     sync.Once
	closeErr      error
}

func startProcessTree(cmd *exec.Cmd) (_ processTree, err error) {
	stdio, err := newWindowsStdio(cmd.Stdin, cmd.Stdout, cmd.Stderr)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			stdio.closeParentEnds()
			stdio.closeChildEnds()
		}
	}()

	job, err := createKillOnCloseJob()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = windows.CloseHandle(job)
		}
	}()

	applicationName, err := windows.UTF16PtrFromString(cmd.Path)
	if err != nil {
		return nil, err
	}
	commandLine, err := windows.UTF16PtrFromString(windowsCommandLine(cmd.Args))
	if err != nil {
		return nil, err
	}
	currentDirectory, err := windowsUTF16Optional(cmd.Dir)
	if err != nil {
		return nil, err
	}
	environment, err := windowsEnvironment(cmd.Env)
	if err != nil {
		return nil, err
	}

	handles := stdio.childHandles()
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	if err := attributes.Update(
		windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&handles[0]),
		uintptr(len(handles))*unsafe.Sizeof(handles[0]),
	); err != nil {
		return nil, err
	}
	startup := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:        uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:     windows.STARTF_USESTDHANDLES,
			StdInput:  handles[0],
			StdOutput: handles[1],
			StdErr:    handles[2],
		},
		ProcThreadAttributeList: attributes.List(),
	}
	var information windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	if err := windows.CreateProcess(
		applicationName,
		commandLine,
		nil,
		nil,
		true,
		flags,
		environment,
		currentDirectory,
		&startup.StartupInfo,
		&information,
	); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = windows.TerminateProcess(information.Process, processTerminateExitCode)
			_ = windows.CloseHandle(information.Thread)
			_ = windows.CloseHandle(information.Process)
		}
	}()

	if err := windows.AssignProcessToJobObject(job, information.Process); err != nil {
		return nil, err
	}
	if _, err := windows.ResumeThread(information.Thread); err != nil {
		return nil, err
	}

	stdio.closeChildEnds()
	tree := &windowsProcessTree{
		process: information.Process,
		thread:  information.Thread,
		job:     job,
		stdio:   stdio,
	}
	stdio.startBridges()
	return tree, nil
}

func (tree *windowsProcessTree) Wait() error {
	tree.handleMu.Lock()
	process := tree.process
	tree.handleMu.Unlock()
	if process == 0 {
		return errors.New("wait for closed owned Windows process")
	}
	status, err := windows.WaitForSingleObject(process, windows.INFINITE)
	if err != nil {
		return err
	}
	if status != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("wait for owned Windows process: unexpected status %d", status)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(process, &exitCode); err != nil {
		return err
	}
	if exitCode != 0 {
		return windowsExitError{code: exitCode}
	}
	return nil
}

func (tree *windowsProcessTree) Terminate() error {
	tree.terminateOnce.Do(func() {
		tree.handleMu.Lock()
		job := tree.job
		tree.job = 0
		tree.handleMu.Unlock()
		if job == 0 {
			return
		}
		tree.terminateErr = windows.CloseHandle(job)
	})
	return tree.terminateErr
}

func (tree *windowsProcessTree) Kill() error {
	return tree.Terminate()
}

func (tree *windowsProcessTree) Close() error {
	tree.closeOnce.Do(func() {
		tree.stdio.closeInputWriter()
		bridgeErr := tree.stdio.joinBridges()
		tree.handleMu.Lock()
		thread, process := tree.thread, tree.process
		tree.thread, tree.process = 0, 0
		tree.handleMu.Unlock()
		tree.closeErr = errors.Join(
			bridgeErr,
			closeWindowsHandle(thread),
			closeWindowsHandle(process),
		)
	})
	return tree.closeErr
}

type windowsExitError struct {
	code uint32
}

func (err windowsExitError) Error() string {
	return fmt.Sprintf("owned Windows process exited with code %d", err.code)
}

func (err windowsExitError) ExitCode() int {
	return int(err.code)
}

func createKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

type windowsStdio struct {
	stdinRead   *os.File
	stdinWrite  *os.File
	stdoutRead  *os.File
	stdoutWrite *os.File
	stderrRead  *os.File
	stderrWrite *os.File
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
	bridges     sync.WaitGroup
	bridgeMu    sync.Mutex
	bridgeErr   error
}

func newWindowsStdio(stdin io.Reader, stdout, stderr io.Writer) (*windowsStdio, error) {
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		return nil, err
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		return nil, err
	}
	for _, file := range []*os.File{stdinRead, stdoutWrite, stderrWrite} {
		if err := windows.SetHandleInformation(windows.Handle(file.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			_ = stdinRead.Close()
			_ = stdinWrite.Close()
			_ = stdoutRead.Close()
			_ = stdoutWrite.Close()
			_ = stderrRead.Close()
			_ = stderrWrite.Close()
			return nil, err
		}
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return &windowsStdio{
		stdinRead: stdinRead, stdinWrite: stdinWrite,
		stdoutRead: stdoutRead, stdoutWrite: stdoutWrite,
		stderrRead: stderrRead, stderrWrite: stderrWrite,
		stdin: stdin, stdout: stdout, stderr: stderr,
	}, nil
}

func (stdio *windowsStdio) childHandles() [3]windows.Handle {
	return [3]windows.Handle{
		windows.Handle(stdio.stdinRead.Fd()),
		windows.Handle(stdio.stdoutWrite.Fd()),
		windows.Handle(stdio.stderrWrite.Fd()),
	}
}

func (stdio *windowsStdio) closeChildEnds() {
	_ = stdio.stdinRead.Close()
	_ = stdio.stdoutWrite.Close()
	_ = stdio.stderrWrite.Close()
}

func (stdio *windowsStdio) closeParentEnds() {
	_ = stdio.stdinWrite.Close()
	_ = stdio.stdoutRead.Close()
	_ = stdio.stderrRead.Close()
}

func (stdio *windowsStdio) closeInputWriter() {
	_ = stdio.stdinWrite.Close()
}

func (stdio *windowsStdio) startBridges() {
	stdio.bridges.Add(3)
	go func() {
		defer stdio.bridges.Done()
		stdio.copyInput()
		_ = stdio.stdinWrite.Close()
	}()
	go func() {
		defer stdio.bridges.Done()
		stdio.copyOutput("stdout", stdio.stdout, stdio.stdoutRead)
		_ = stdio.stdoutRead.Close()
	}()
	go func() {
		defer stdio.bridges.Done()
		stdio.copyOutput("stderr", stdio.stderr, stdio.stderrRead)
		_ = stdio.stderrRead.Close()
	}()
}

func (stdio *windowsStdio) copyInput() {
	if err := copyBridge(stdio.stdinWrite, stdio.stdin, true, false); err != nil {
		stdio.recordBridgeError("stdin", err)
	}
}

func (stdio *windowsStdio) copyOutput(name string, destination io.Writer, source io.Reader) {
	if err := copyBridge(destination, source, false, true); err != nil {
		stdio.recordBridgeError(name, err)
	}
}

func (stdio *windowsStdio) joinBridges() error {
	stdio.bridges.Wait()
	stdio.bridgeMu.Lock()
	defer stdio.bridgeMu.Unlock()
	return stdio.bridgeErr
}

func (stdio *windowsStdio) recordBridgeError(name string, err error) {
	stdio.bridgeMu.Lock()
	defer stdio.bridgeMu.Unlock()
	stdio.bridgeErr = errors.Join(stdio.bridgeErr, fmt.Errorf("owned Windows process %s bridge: %w", name, err))
}

func copyBridge(destination io.Writer, source io.Reader, ignoreDestinationClosure, ignoreSourceClosure bool) error {
	buffer := make([]byte, 32*1024)
	for {
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			if writeErr != nil {
				if ignoreDestinationClosure && isExpectedPipeClosure(writeErr) {
					return nil
				}
				return writeErr
			}
			if written != read {
				return io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if ignoreSourceClosure && isExpectedPipeClosure(readErr) {
				return nil
			}
			return readErr
		}
	}
}

func isExpectedPipeClosure(err error) bool {
	return errors.Is(err, os.ErrClosed) ||
		errors.Is(err, windows.ERROR_BROKEN_PIPE) ||
		errors.Is(err, windows.ERROR_NO_DATA) ||
		errors.Is(err, windows.ERROR_OPERATION_ABORTED)
}

func windowsCommandLine(argv []string) string {
	escaped := make([]string, len(argv))
	for index, argument := range argv {
		escaped[index] = syscall.EscapeArg(argument)
	}
	return strings.Join(escaped, " ")
}

func windowsUTF16Optional(value string) (*uint16, error) {
	if value == "" {
		return nil, nil
	}
	return windows.UTF16PtrFromString(value)
}

func windowsEnvironment(env []string) (*uint16, error) {
	if env == nil {
		return nil, nil
	}
	block, err := windowsEnvironmentBlock(env)
	if err != nil {
		return nil, err
	}
	return &block[0], nil
}

func windowsEnvironmentBlock(env []string) ([]uint16, error) {
	if env == nil {
		return nil, nil
	}
	normalized, err := normalizeWindowsEnvironment(env)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return []uint16{0, 0}, nil
	}
	sort.SliceStable(normalized, func(left, right int) bool {
		return compareWindowsEnvironmentNames(normalized[left], normalized[right]) < 0
	})
	block := make([]uint16, 0)
	for _, entry := range normalized {
		if strings.IndexByte(entry, 0) >= 0 {
			return nil, errors.New("owned Windows process environment contains NUL")
		}
		block = append(block, utf16.Encode([]rune(entry))...)
		block = append(block, 0)
	}
	block = append(block, 0)
	return block, nil
}

func normalizeWindowsEnvironment(env []string) ([]string, error) {
	normalized := make([]string, 0, len(env))
	seen := make(map[string]struct{}, len(env))
	var validationErr error
	for index := len(env) - 1; index >= 0; index-- {
		entry := env[index]
		if strings.IndexByte(entry, 0) >= 0 {
			validationErr = errors.New("owned Windows process environment contains NUL")
			continue
		}
		keyEnd := strings.IndexByte(entry, '=')
		if keyEnd == 0 {
			keyEnd = strings.IndexByte(entry[1:], '=') + 1
		}
		if keyEnd < 0 {
			if entry != "" {
				normalized = append(normalized, entry)
			}
			continue
		}
		key := strings.ToLower(entry[:keyEnd])
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, entry)
	}
	for left, right := 0, len(normalized)-1; left < right; left, right = left+1, right-1 {
		normalized[left], normalized[right] = normalized[right], normalized[left]
	}
	if validationErr != nil {
		return nil, validationErr
	}
	return normalized, nil
}

func compareWindowsEnvironmentNames(left, right string) int {
	leftKey := windowsEnvironmentSortKey(left)
	rightKey := windowsEnvironmentSortKey(right)
	switch {
	case leftKey < rightKey:
		return -1
	case leftKey > rightKey:
		return 1
	default:
		return 0
	}
}

func windowsEnvironmentSortKey(entry string) string {
	keyEnd := strings.IndexByte(entry, '=')
	if keyEnd < 0 {
		return ""
	}
	key := []byte(entry[:keyEnd])
	for index, value := range key {
		if value >= 'a' && value <= 'z' {
			key[index] = value - ('a' - 'A')
		}
	}
	return string(key)
}

func closeWindowsHandle(handle windows.Handle) error {
	if handle == 0 {
		return nil
	}
	return windows.CloseHandle(handle)
}
