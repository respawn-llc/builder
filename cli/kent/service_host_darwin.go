//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func serviceHostRun(spec serviceSpec, _ io.Writer, stderr io.Writer) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := runDarwinServiceHost(ctx, spec); err != nil {
		fmt.Fprintf(stderr, "service run: %v\n", err)
		return 1
	}
	return 0
}

func runDarwinServiceHost(ctx context.Context, spec serviceSpec) error {
	root := spec.Config.PersistenceRoot
	if err := os.MkdirAll(darwinServiceRuntimeDir(root), 0o755); err != nil {
		return fmt.Errorf("create Darwin service runtime directory: %w", err)
	}
	lock, err := acquireDarwinServiceLock(ctx, spec.Executable, darwinServiceLockPath(root))
	if err != nil {
		return err
	}
	defer lock.Close()
	if healthStatus, healthPID := probeServiceHealth(ctx, spec); healthStatus == "ok" {
		return fmt.Errorf("refusing to start a Darwin service child because an unrelated server is already healthy on %s (pid %d)", spec.Endpoint, healthPID)
	}

	hostLease, err := darwinSocketPair()
	if err != nil {
		return fmt.Errorf("create Darwin host watchdog lease: %w", err)
	}
	childLease, err := darwinSocketPair()
	if err != nil {
		_ = unix.Close(hostLease[0])
		_ = unix.Close(hostLease[1])
		return fmt.Errorf("create Darwin child watchdog lease: %w", err)
	}
	startGate, err := darwinSocketPair()
	if err != nil {
		for _, fd := range []int{hostLease[0], hostLease[1], childLease[0], childLease[1]} {
			_ = unix.Close(fd)
		}
		return fmt.Errorf("create Darwin child start gate: %w", err)
	}
	hostLeaseFile := darwinSocketFile(hostLease[0], "Darwin host watchdog lease")
	watchdogHostFile := darwinSocketFile(hostLease[1], "Darwin watchdog host lease")
	childLeaseFile := darwinSocketFile(childLease[0], "Darwin child watchdog lease")
	watchdogChildFile := darwinSocketFile(childLease[1], "Darwin watchdog child lease")
	hostGateFile := darwinSocketFile(startGate[0], "Darwin host start gate")
	childGateFile := darwinSocketFile(startGate[1], "Darwin child start gate")
	defer closeDarwinFiles(hostLeaseFile, watchdogHostFile, childLeaseFile, watchdogChildFile, hostGateFile, childGateFile)

	watchdog := exec.Command(spec.Executable, darwinPrivateWatchdogMode, "3", "4")
	watchdog.ExtraFiles = []*os.File{watchdogHostFile, watchdogChildFile}
	if err := watchdog.Start(); err != nil {
		return fmt.Errorf("start Darwin service watchdog: %w", err)
	}
	_ = watchdogHostFile.Close()
	watchdogHostFile = nil
	_ = watchdogChildFile.Close()
	watchdogChildFile = nil

	childLockFD, err := unix.Dup(int(lock.Fd()))
	if err != nil {
		_ = stopAndWaitDarwinProcess(watchdog, launchdServiceShutdownTimeout)
		return fmt.Errorf("duplicate Darwin service lock for child: %w", err)
	}
	unix.CloseOnExec(childLockFD)
	childLockFile := os.NewFile(uintptr(childLockFD), "Darwin child activation lock")
	child := exec.Command(spec.Executable, spec.Arguments...)
	child.ExtraFiles = []*os.File{childLockFile, childLeaseFile, childGateFile}
	child.Env = append(os.Environ(),
		darwinServiceChildMarkerEnv+"=1",
		darwinServiceLockFDEnv+"=3",
		darwinServiceLeaseFDEnv+"=4",
		darwinServiceGateFDEnv+"=5",
	)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		_ = childLockFile.Close()
		_ = stopAndWaitDarwinProcess(watchdog, launchdServiceShutdownTimeout)
		return fmt.Errorf("start Darwin server child: %w", err)
	}
	closeDarwinFiles(childLockFile, childLeaseFile, childGateFile)
	childLeaseFile = nil
	childGateFile = nil

	setup := darwinServiceMessage{
		Kind:         "setup",
		HostPID:      os.Getpid(),
		HostCommand:  darwinServiceHostCommand(spec),
		ChildPID:     child.Process.Pid,
		ChildCommand: serverChildCommand(spec),
	}
	if err := writeDarwinServiceMessage(hostLeaseFile, setup); err != nil {
		return settleDarwinHostFailure(child, watchdog, hostLeaseFile, hostGateFile, fmt.Errorf("configure Darwin watchdog: %w", err))
	}
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, darwinServiceHandshakeTimeout)
	ack, err := readDarwinServiceMessageContext(handshakeCtx, hostLeaseFile)
	cancelHandshake()
	if err != nil {
		return settleDarwinHostFailure(child, watchdog, hostLeaseFile, hostGateFile, fmt.Errorf("arm Darwin watchdog: %w", err))
	}
	if err := validateDarwinArmedAcknowledgement(ack, child.Process.Pid); err != nil || ack.HostPID != os.Getpid() {
		if err == nil {
			err = errors.New("Darwin watchdog acknowledged the wrong host")
		}
		return settleDarwinHostFailure(child, watchdog, hostLeaseFile, hostGateFile, err)
	}
	if err := writeDarwinServiceMessage(hostGateFile, ack); err != nil {
		return settleDarwinHostFailure(child, watchdog, hostLeaseFile, hostGateFile, fmt.Errorf("release Darwin server start gate: %w", err))
	}
	_ = hostGateFile.Close()
	hostGateFile = nil

	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()
	watchdogDone := make(chan error, 1)
	go func() { watchdogDone <- watchdog.Wait() }()
	leaseDone := make(chan error, 1)
	go func() {
		for {
			message, err := readDarwinServiceMessage(hostLeaseFile)
			if err != nil {
				leaseDone <- err
				return
			}
			switch message.Kind {
			case "child-exited":
				continue
			case "settling":
				leaseDone <- nil
				return
			default:
				leaseDone <- errors.New("unexpected Darwin watchdog lease message")
				return
			}
		}
	}()

	select {
	case err := <-childDone:
		_ = writeDarwinServiceMessage(hostLeaseFile, darwinServiceMessage{Kind: "settling"})
		_ = waitForDarwinProcessSettlement(watchdog, watchdogDone, launchdServiceShutdownTimeout)
		if exitCode(err) == int(serviceNoRestartExitStatus) {
			return nil
		}
		if err == nil {
			return errors.New("Darwin server child exited unexpectedly")
		}
		return fmt.Errorf("Darwin server child exited unexpectedly: %w", err)
	case err := <-leaseDone:
		if err == nil {
			return settleDarwinChild(child, childDone, watchdog, watchdogDone, hostLeaseFile, nil)
		}
		return settleDarwinChild(child, childDone, watchdog, watchdogDone, hostLeaseFile, fmt.Errorf("Darwin watchdog lease failed: %w", err))
	case <-ctx.Done():
		return settleDarwinChild(child, childDone, watchdog, watchdogDone, hostLeaseFile, nil)
	}
}

func settleDarwinHostFailure(child, watchdog *exec.Cmd, hostLease, hostGate *os.File, cause error) error {
	closeDarwinFiles(hostGate)
	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()
	watchdogDone := make(chan error, 1)
	go func() { watchdogDone <- watchdog.Wait() }()
	return settleDarwinChild(child, childDone, watchdog, watchdogDone, hostLease, cause)
}

func settleDarwinChild(child *exec.Cmd, childDone <-chan error, watchdog *exec.Cmd, watchdogDone <-chan error, hostLease *os.File, cause error) error {
	_ = writeDarwinServiceMessage(hostLease, darwinServiceMessage{Kind: "settling"})
	childErr := stopDarwinProcessAndAwait(child, childDone, launchdServiceShutdownTimeout)
	watchdogErr := stopDarwinProcessAndAwait(watchdog, watchdogDone, launchdServiceShutdownTimeout)
	return errors.Join(cause, childErr, watchdogErr)
}

func stopAndWaitDarwinProcess(cmd *exec.Cmd, timeout time.Duration) error {
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return nil
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if exitCode(err) >= 0 {
			return nil
		}
		return err
	case <-timer.C:
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		<-done
		return nil
	}
}

func stopDarwinProcessAndAwait(cmd *exec.Cmd, done <-chan error, timeout time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if exitCode(err) >= 0 {
			return nil
		}
		return err
	case <-timer.C:
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		<-done
		return nil
	}
}

func waitForDarwinProcessSettlement(cmd *exec.Cmd, done <-chan error, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if exitCode(err) >= 0 {
			return nil
		}
		return err
	case <-timer.C:
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return errors.New("Darwin service watchdog did not settle")
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func acquireDarwinServiceLock(ctx context.Context, executable, path string) (*os.File, error) {
	pair, err := darwinSocketPair()
	if err != nil {
		return nil, fmt.Errorf("create Darwin service lock handoff: %w", err)
	}
	host := darwinSocketFile(pair[0], "Darwin lock handoff host")
	helperSocket := darwinSocketFile(pair[1], "Darwin lock handoff helper")
	defer closeDarwinFiles(host, helperSocket)
	helper := newDarwinLockHelperCommand(executable, path)
	helper.ExtraFiles = []*os.File{helperSocket}
	if err := helper.Start(); err != nil {
		return nil, fmt.Errorf("start Darwin lock helper: %w", err)
	}
	_ = helperSocket.Close()
	helperSocket = nil
	type received struct {
		file *os.File
		err  error
	}
	result := make(chan received, 1)
	go func() {
		file, err := receiveDarwinLockedFile(host)
		result <- received{file: file, err: err}
	}()
	select {
	case got := <-result:
		waitErr := helper.Wait()
		if got.err != nil || waitErr != nil {
			closeDarwinFiles(got.file)
			return nil, errors.Join(got.err, waitErr)
		}
		if err := validateDarwinLockedFile(got.file, path); err != nil {
			_ = got.file.Close()
			return nil, err
		}
		return got.file, nil
	case <-ctx.Done():
		_ = unix.Shutdown(int(host.Fd()), unix.SHUT_RDWR)
		_ = helper.Process.Kill()
		_ = helper.Wait()
		got := <-result
		closeDarwinFiles(got.file)
		return nil, ctx.Err()
	}
}

var newDarwinLockHelperCommand = func(executable, path string) *exec.Cmd {
	return exec.Command(executable, darwinPrivateLockHelperMode, path, "3")
}

func receiveDarwinLockedFile(socket *os.File) (*os.File, error) {
	payload := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4))
	n, oobn, flags, _, err := unix.Recvmsg(int(socket.Fd()), payload, oob, 0)
	if err != nil {
		return nil, err
	}
	if n != 1 || payload[0] != 1 || flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0 {
		return nil, errors.New("invalid Darwin lock handoff payload")
	}
	messages, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(messages) != 1 {
		return nil, errors.New("invalid Darwin lock handoff ancillary data")
	}
	rights, err := unix.ParseUnixRights(&messages[0])
	if err != nil || len(rights) != 1 {
		for _, fd := range rights {
			_ = unix.Close(fd)
		}
		return nil, errors.New("Darwin lock handoff must contain exactly one descriptor")
	}
	unix.CloseOnExec(rights[0])
	return os.NewFile(uintptr(rights[0]), "Darwin service activation lock"), nil
}

func runDarwinPrivateServiceMode(args []string, _ io.Writer, stderr io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case darwinPrivateLockHelperMode:
		if err := runDarwinLockHelper(args[1:]); err != nil {
			fmt.Fprintln(stderr, err)
			return 1, true
		}
		return 0, true
	case darwinPrivateWatchdogMode:
		if err := runDarwinWatchdog(args[1:]); err != nil {
			fmt.Fprintln(stderr, err)
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func runDarwinLockHelper(args []string) error {
	if len(args) != 2 {
		return errors.New("invalid Darwin lock-helper invocation")
	}
	fd, err := strconv.Atoi(strings.TrimSpace(args[1]))
	if err != nil || fd < darwinInheritedFDBase {
		return errors.New("invalid Darwin lock-helper socket descriptor")
	}
	socket := os.NewFile(uintptr(fd), "Darwin lock-helper socket")
	if socket == nil {
		return errors.New("adopt Darwin lock-helper socket")
	}
	defer socket.Close()
	lock, err := os.OpenFile(args[0], os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open Darwin activation lock: %w", err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("acquire Darwin activation lock: %w", err)
	}
	rights := unix.UnixRights(int(lock.Fd()))
	if err := unix.Sendmsg(int(socket.Fd()), []byte{1}, rights, nil, 0); err != nil {
		return fmt.Errorf("hand off Darwin activation lock: %w", err)
	}
	return nil
}
