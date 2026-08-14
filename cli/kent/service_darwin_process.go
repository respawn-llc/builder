//go:build darwin

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	darwinKernProcArgs2  = 49
	darwinProcessArgsMax = 1024 * 1024
)

type darwinProcessIdentity struct {
	PID     int
	Parent  int
	Command []string
}

var inspectDarwinProcessIdentity = inspectDarwinProcess
var listDarwinChildProcesses = listDarwinDirectChildren

func inspectDarwinProcess(pid int) (darwinProcessIdentity, error) {
	if pid <= 0 {
		return darwinProcessIdentity{}, fmt.Errorf("invalid process id %d", pid)
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return darwinProcessIdentity{}, fmt.Errorf("inspect process %d: %w", pid, err)
	}
	if int(info.Proc.P_pid) != pid {
		return darwinProcessIdentity{}, fmt.Errorf("inspect process %d: kernel returned process %d", pid, info.Proc.P_pid)
	}
	command, err := darwinProcessArguments(pid)
	if err != nil {
		return darwinProcessIdentity{}, err
	}
	return darwinProcessIdentity{PID: pid, Parent: int(info.Eproc.Ppid), Command: command}, nil
}

func listDarwinDirectChildren(parentPID int) ([]darwinProcessIdentity, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("enumerate processes: %w", err)
	}
	children := make([]darwinProcessIdentity, 0)
	for _, process := range processes {
		if int(process.Eproc.Ppid) != parentPID {
			continue
		}
		identity, err := inspectDarwinProcess(int(process.Proc.P_pid))
		if err != nil {
			if errors.Is(err, unix.ESRCH) {
				continue
			}
			return nil, fmt.Errorf("inspect direct child of process %d: %w", parentPID, err)
		}
		children = append(children, identity)
	}
	return children, nil
}

func darwinProcessArguments(pid int) ([]string, error) {
	mib := []int32{unix.CTL_KERN, darwinKernProcArgs2, int32(pid)}
	buffer := make([]byte, darwinProcessArgsMax)
	length := uintptr(len(buffer))
	_, _, errno := unix.Syscall6(
		unix.SYS_SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])),
		uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(unsafe.Pointer(&length)),
		0,
		0,
	)
	if errno != 0 {
		return nil, fmt.Errorf("read process %d arguments: %w", pid, errno)
	}
	if length < 4 || length > uintptr(len(buffer)) {
		return nil, fmt.Errorf("read process %d arguments: invalid payload length %d", pid, length)
	}
	buffer = buffer[:length]
	argc := int(binary.NativeEndian.Uint32(buffer[:4]))
	if argc <= 0 {
		return nil, fmt.Errorf("read process %d arguments: invalid argument count %d", pid, argc)
	}
	cursor := 4
	executableEnd := indexByte(buffer, cursor, 0)
	if executableEnd < 0 {
		return nil, fmt.Errorf("read process %d arguments: missing executable terminator", pid)
	}
	cursor = executableEnd
	for cursor < len(buffer) && buffer[cursor] == 0 {
		cursor++
	}
	args := make([]string, 0, argc)
	for len(args) < argc && cursor < len(buffer) {
		end := indexByte(buffer, cursor, 0)
		if end < 0 {
			return nil, fmt.Errorf("read process %d arguments: unterminated argument %d", pid, len(args))
		}
		args = append(args, string(buffer[cursor:end]))
		cursor = end + 1
	}
	if len(args) != argc {
		return nil, fmt.Errorf("read process %d arguments: found %d arguments, expected %d", pid, len(args), argc)
	}
	return args, nil
}

func indexByte(data []byte, start int, value byte) int {
	for i := start; i < len(data); i++ {
		if data[i] == value {
			return i
		}
	}
	return -1
}

func validateDarwinProcessIdentity(pid int, expected []string) error {
	identity, err := inspectDarwinProcess(pid)
	if err != nil {
		return err
	}
	if !commandArgsEqual(identity.Command, expected) {
		return fmt.Errorf("process %d command is %s, expected %s", pid, commandString(identity.Command), commandString(expected))
	}
	return nil
}

func darwinProcessAlive(pid int) (bool, error) {
	err := unix.Kill(pid, 0)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.ESRCH):
		return false, nil
	default:
		return false, err
	}
}

func validateDarwinLockedFile(file *os.File, expectedPath string) error {
	if file == nil {
		return errors.New("service lock descriptor is missing")
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat service lock descriptor: %w", err)
	}
	pathInfo, err := os.Stat(expectedPath)
	if err != nil {
		return fmt.Errorf("stat service lock path: %w", err)
	}
	if !os.SameFile(fileInfo, pathInfo) {
		return fmt.Errorf("service lock descriptor does not resolve to %s", expectedPath)
	}
	if strings.TrimSpace(file.Name()) == "" {
		return errors.New("service lock descriptor has no path identity")
	}
	probe, err := os.OpenFile(expectedPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open independent service lock probe: %w", err)
	}
	defer probe.Close()
	err = unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	switch {
	case errors.Is(err, unix.EWOULDBLOCK):
		return nil
	case err != nil:
		return fmt.Errorf("verify service lock ownership: %w", err)
	default:
		_ = unix.Flock(int(probe.Fd()), unix.LOCK_UN)
		return errors.New("service lock descriptor does not hold the exclusive activation lock")
	}
}
