//go:build darwin

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"core/shared/config"
	"golang.org/x/sys/unix"
)

func TestDarwinServiceCommandsAndLaunchdPolicy(t *testing.T) {
	spec := darwinTestServiceSpec(t)
	if got, want := serverChildCommand(spec), []string{spec.Executable, "serve", "--persistence-root", spec.Config.PersistenceRoot}; !slices.Equal(got, want) {
		t.Fatalf("server child command = %#v, want %#v", got, want)
	}
	if got, want := darwinServiceHostCommand(spec), []string{spec.Executable, "service", "run", "--persistence-root", spec.Config.PersistenceRoot}; !slices.Equal(got, want) {
		t.Fatalf("Darwin host command = %#v, want %#v", got, want)
	}
	plist := renderLaunchdPlist(spec)
	if got := parseLaunchdProgramArguments([]byte(plist)); !slices.Equal(got, darwinServiceHostCommand(spec)) {
		t.Fatalf("launchd command = %#v, want %#v", got, darwinServiceHostCommand(spec))
	}
	for _, want := range []string{"<key>KeepAlive</key>", "<key>SuccessfulExit</key>", "<false/>"} {
		if !strings.Contains(plist, want) {
			t.Fatalf("launchd plist missing %q:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "<key>KeepAlive</key>\n\t<true/>") {
		t.Fatalf("launchd plist retains unconditional KeepAlive:\n%s", plist)
	}
}

func TestDarwinServiceStatusKeepsRegisteredCommandForRootMatchingWithNoChild(t *testing.T) {
	requested := darwinTestServiceSpec(t)
	installed := requested
	installed.Config.PersistenceRoot = filepath.Join(t.TempDir(), "installed-root")
	plistPath := filepath.Join(t.TempDir(), "kent.plist")
	if err := os.WriteFile(plistPath, []byte(renderLaunchdPlist(installed)), 0o600); err != nil {
		t.Fatal(err)
	}
	command := readLaunchdRegisteredCommand(plistPath)
	status := serviceStatus{
		Installed:         true,
		Loaded:            true,
		Running:           true,
		Command:           serverChildCommand(requested),
		registeredCommand: command,
	}
	if err := rootMismatchError(status, requested); err == nil {
		t.Fatal("NoChild status lost the installed launchd host persistence root")
	}
}

func TestDarwinLaunchdInspectionFailureIsNotAbsence(t *testing.T) {
	previous := runServiceCommand
	runServiceCommand = func(context.Context, string, ...string) (serviceCommandResult, error) {
		result := serviceCommandResult{Code: 1, Stderr: "launchctl unavailable"}
		return result, serviceCommandError{Name: "launchctl", Result: result}
	}
	t.Cleanup(func() { runServiceCommand = previous })
	if _, err := inspectLaunchdService(context.Background()); err == nil {
		t.Fatal("launchd inspection failure was treated as an unloaded service")
	}
}

func TestDarwinFreshActivationLockPreflightCreatesRuntimeDirectory(t *testing.T) {
	spec := darwinTestServiceSpec(t)
	if _, err := darwinServiceLockAvailable(spec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(darwinServiceRuntimeDir(spec.Config.PersistenceRoot)); err != nil {
		t.Fatalf("runtime directory: %v", err)
	}
}

func TestDarwinStatusTwoCompletesActivationWithoutReplacement(t *testing.T) {
	decision := settleManagedChild(
		managedChildSettlementTarget{
			terminate: func() serviceTerminationObservation { return observedStatus(serviceNoRestartExitStatus) },
			release:   func() {},
			retain:    func() { t.Fatal("status 2 retained activation ownership") },
		},
		observedStatus(serviceNoRestartExitStatus),
	)
	if decision.Replace || !decision.Complete {
		t.Fatalf("status 2 decision = %#v", decision)
	}
}

func TestDarwinTopologyRejectsMultipleMatchingDirectChildren(t *testing.T) {
	spec := darwinTestServiceSpec(t)
	previous := listDarwinChildProcesses
	listDarwinChildProcesses = func(parentPID int) ([]darwinProcessIdentity, error) {
		command := serverChildCommand(spec)
		return []darwinProcessIdentity{
			{PID: 101, Parent: parentPID, Command: command},
			{PID: 102, Parent: parentPID, Command: command},
		}, nil
	}
	t.Cleanup(func() { listDarwinChildProcesses = previous })
	_, err := resolveDarwinHostChild(spec, darwinLaunchdHost{Loaded: true, PID: 100})
	if err == nil {
		t.Fatal("multiple matching direct children were accepted")
	}
}

func TestDarwinReadinessRequiresOwnedChildHealthPID(t *testing.T) {
	topology := darwinServiceTopology{
		Host:  darwinLaunchdHost{Loaded: true, PID: 100},
		Child: &darwinOwnedChild{PID: 101},
	}
	if err := validateDarwinServiceReadiness(topology, "ok", 202); err == nil {
		t.Fatal("readiness accepted health from an unrelated process")
	}
	if err := validateDarwinServiceReadiness(topology, "ok", 101); err != nil {
		t.Fatalf("matching owned child was not ready: %v", err)
	}
	if err := validateDarwinServiceReadiness(darwinServiceTopology{Host: topology.Host}, "ok", 101); err == nil {
		t.Fatal("NoChild topology was considered ready")
	}
}

func TestDarwinServiceLockHandoffIsCancellableAndExclusive(t *testing.T) {
	root := t.TempDir()
	lockPath := darwinServiceLockPath(root)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := newDarwinLockHelperCommand
	newDarwinLockHelperCommand = func(_ string, path string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestDarwinServiceHelperProcess")
		cmd.Env = append(os.Environ(), "GO_WANT_DARWIN_SERVICE_HELPER=lock", "DARWIN_SERVICE_LOCK_PATH="+path)
		return cmd
	}
	t.Cleanup(func() { newDarwinLockHelperCommand = previous })
	lock, err := acquireDarwinServiceLock(context.Background(), os.Args[0], lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = acquireDarwinServiceLock(ctx, os.Args[0], lockPath)
	if err == nil {
		t.Fatal("second Darwin activation lock acquisition unexpectedly succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("cancelled lock acquisition took %s", time.Since(start))
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := acquireDarwinServiceLock(context.Background(), os.Args[0], lockPath)
	if err != nil {
		t.Fatalf("replacement acquisition: %v", err)
	}
	_ = replacement.Close()
}

func TestDarwinServiceChildWaitsForDualMatchingAcknowledgementAndCancelsOnLeaseLoss(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(darwinServiceRuntimeDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	lockPath := darwinServiceLockPath(root)
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	leasePair, err := darwinSocketPair()
	if err != nil {
		t.Fatal(err)
	}
	gatePair, err := darwinSocketPair()
	if err != nil {
		t.Fatal(err)
	}
	testLease := darwinSocketFile(leasePair[0], "test watchdog lease")
	childLease := darwinSocketFile(leasePair[1], "child watchdog lease")
	testGate := darwinSocketFile(gatePair[0], "test start gate")
	childGate := darwinSocketFile(gatePair[1], "child start gate")
	defer closeDarwinFiles(testLease, childLease, testGate, childGate)
	lockChildFD, err := unix.Dup(int(lock.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	childLock := os.NewFile(uintptr(lockChildFD), "child lock")
	child := exec.Command(os.Args[0], "-test.run=TestDarwinServiceHelperProcess")
	child.Env = append(darwinTestHelperEnvironment("service-child"),
		"DARWIN_SERVICE_TEST_ROOT="+root,
		darwinServiceChildMarkerEnv+"=1",
		darwinServiceLockFDEnv+"=3",
		darwinServiceLeaseFDEnv+"=4",
		darwinServiceGateFDEnv+"=5",
	)
	child.ExtraFiles = []*os.File{childLock, childLease, childGate}
	var childOutput bytes.Buffer
	child.Stdout = &childOutput
	child.Stderr = &childOutput
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	closeDarwinFiles(childLock, childLease, childGate)
	childLease = nil
	childGate = nil
	_ = unix.Close(leasePair[1])
	_ = unix.Close(gatePair[1])
	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()
	childSettled := false
	t.Cleanup(func() {
		if childSettled || child.Process == nil {
			return
		}
		_ = child.Process.Kill()
		select {
		case <-childDone:
		case <-time.After(time.Second):
		}
	})
	ack := darwinServiceMessage{Kind: "armed", HostPID: os.Getpid(), ChildPID: child.Process.Pid}
	if err := writeDarwinServiceMessage(testLease, ack); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-childDone:
		t.Fatalf("service child passed start gate early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := writeDarwinServiceMessage(testGate, ack); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-childDone:
		t.Fatalf("service child failed after start gate: %v; output: %s", err, childOutput.String())
	case <-time.After(100 * time.Millisecond):
	}
	if err := unix.Shutdown(int(testLease.Fd()), unix.SHUT_RDWR); err != nil {
		t.Fatal(err)
	}
	if err := testLease.Close(); err != nil {
		t.Fatal(err)
	}
	testLease = nil
	select {
	case err := <-childDone:
		childSettled = true
		if err != nil {
			t.Fatalf("service child lease-loss settlement: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service child did not cancel after watchdog lease loss")
	}
}

func TestDarwinServiceChildFailsClosedWhenWatchdogDiesBeforeStartGate(t *testing.T) {
	root, child, childDone, lease, gate := startDarwinTestServiceChild(t)
	_ = root
	ack := darwinServiceMessage{Kind: "armed", HostPID: os.Getpid(), ChildPID: child.Process.Pid}
	if err := writeDarwinServiceMessage(lease, ack); err != nil {
		t.Fatal(err)
	}
	if err := unix.Shutdown(int(lease.Fd()), unix.SHUT_RDWR); err != nil {
		t.Fatal(err)
	}
	_ = lease.Close()
	_ = unix.Shutdown(int(gate.Fd()), unix.SHUT_RDWR)
	_ = gate.Close()
	select {
	case err := <-childDone:
		if err == nil {
			t.Fatal("child reported success after watchdog loss before start gate")
		}
	case <-time.After(3 * time.Second):
		_ = child.Process.Kill()
		t.Fatal("child remained blocked after watchdog loss before start gate")
	}
}

func TestDarwinWatchdogStopsChildWhenHostExits(t *testing.T) {
	controlPair, err := darwinSocketPair()
	if err != nil {
		t.Fatal(err)
	}
	hostPair, err := darwinSocketPair()
	if err != nil {
		t.Fatal(err)
	}
	childPair, err := darwinSocketPair()
	if err != nil {
		t.Fatal(err)
	}
	testHost := darwinSocketFile(hostPair[0], "test host lease")
	watchdogHost := darwinSocketFile(hostPair[1], "watchdog host lease")
	testChild := darwinSocketFile(childPair[0], "test child lease")
	watchdogChild := darwinSocketFile(childPair[1], "watchdog child lease")
	testControl := darwinSocketFile(controlPair[0], "test host control")
	hostControl := darwinSocketFile(controlPair[1], "host test control")
	defer closeDarwinFiles(testControl, hostControl, testHost, watchdogHost, testChild, watchdogChild)
	var watchdogOutput bytes.Buffer
	host := exec.Command(os.Args[0], "-test.run=TestDarwinServiceHelperProcess")
	host.Env = append(os.Environ(), "GO_WANT_DARWIN_SERVICE_HELPER=watchdog-host")
	host.ExtraFiles = []*os.File{hostControl, watchdogHost, watchdogChild}
	host.Stdout = &watchdogOutput
	host.Stderr = &watchdogOutput
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if host.Process != nil {
			_ = host.Process.Kill()
		}
	}()
	_ = watchdogHost.Close()
	watchdogHost = nil
	_ = watchdogChild.Close()
	watchdogChild = nil
	_ = hostControl.Close()
	hostControl = nil
	childCommand := []string{os.Args[0], "-test.run=TestDarwinServiceHelperProcess"}
	if err := writeDarwinServiceMessage(testControl, darwinServiceMessage{Kind: "start-child", ChildCommand: childCommand}); err != nil {
		t.Fatal(err)
	}
	started, err := readDarwinServiceMessage(testControl)
	if err != nil || started.Kind != "child-started" {
		t.Fatalf("child start = %#v, %v; host output: %s", started, err, watchdogOutput.String())
	}
	setup := darwinServiceMessage{
		Kind:         "setup",
		HostPID:      host.Process.Pid,
		HostCommand:  []string{os.Args[0], "-test.run=TestDarwinServiceHelperProcess"},
		ChildPID:     started.ChildPID,
		ChildCommand: childCommand,
	}
	if err := writeDarwinServiceMessage(testHost, setup); err != nil {
		t.Fatal(err)
	}
	if ack, err := readDarwinServiceMessage(testHost); err != nil || ack.Kind != "armed" {
		_ = host.Wait()
		t.Fatalf("host acknowledgement = %#v, %v; watchdog output: %s", ack, err, watchdogOutput.String())
	}
	if ack, err := readDarwinServiceMessage(testChild); err != nil || ack.Kind != "armed" {
		t.Fatalf("child acknowledgement = %#v, %v", ack, err)
	}
	if err := host.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = host.Wait()
	if alive, err := waitDarwinTestPIDExit(started.ChildPID, 3*time.Second); err != nil || alive {
		t.Fatalf("child alive after watchdog settlement = %t, %v", alive, err)
	}
}

func TestDarwinServiceHelperProcess(t *testing.T) {
	mode := os.Getenv("GO_WANT_DARWIN_SERVICE_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "lock":
		if err := runDarwinLockHelper([]string{os.Getenv("DARWIN_SERVICE_LOCK_PATH"), "3"}); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	case "watchdog-host":
		control := os.NewFile(3, "watchdog host test control")
		hostLease := os.NewFile(4, "watchdog test host lease")
		childLease := os.NewFile(5, "watchdog test child lease")
		command, err := readDarwinServiceMessage(control)
		if err != nil || command.Kind != "start-child" {
			os.Exit(1)
		}
		child := exec.Command(command.ChildCommand[0], command.ChildCommand[1:]...)
		child.Env = darwinTestHelperEnvironment("child")
		if err := child.Start(); err != nil {
			os.Exit(1)
		}
		if err := writeDarwinServiceMessage(control, darwinServiceMessage{Kind: "child-started", ChildPID: child.Process.Pid}); err != nil {
			os.Exit(1)
		}
		watchdog := exec.Command(os.Args[0], "-test.run=TestDarwinServiceHelperProcess")
		watchdog.Env = darwinTestHelperEnvironment("watchdog")
		watchdog.ExtraFiles = []*os.File{hostLease, childLease}
		if err := watchdog.Start(); err != nil {
			os.Exit(1)
		}
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		<-signals
		os.Exit(0)
	case "watchdog":
		if err := runDarwinWatchdog([]string{"3", "4"}); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case "service-child":
		containment, err := prepareServiceChildInvocation(os.Getenv("DARWIN_SERVICE_TEST_ROOT"))
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		ctx := containment.Context(context.Background())
		<-ctx.Done()
		containment.Close()
		os.Exit(0)
	case "child":
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		<-signals
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func darwinTestHelperEnvironment(mode string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GO_WANT_DARWIN_SERVICE_HELPER=") {
			env = append(env, entry)
		}
	}
	return append(env, "GO_WANT_DARWIN_SERVICE_HELPER="+mode)
}

func waitDarwinTestPIDExit(pid int, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive, err := darwinProcessAlive(pid)
		if err != nil || !alive {
			return alive, err
		}
		time.Sleep(25 * time.Millisecond)
	}
	alive, err := darwinProcessAlive(pid)
	return alive, err
}

func darwinTestServiceSpec(t *testing.T) serviceSpec {
	t.Helper()
	root := t.TempDir()
	return serviceSpec{
		Config: config.App{
			PersistenceRoot: root,
		},
		Executable: os.Args[0],
		Arguments:  []string{"serve", "--persistence-root", root},
		Endpoint:   "http://127.0.0.1:1",
	}
}

func startDarwinTestServiceChild(t *testing.T) (string, *exec.Cmd, <-chan error, *os.File, *os.File) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(darwinServiceRuntimeDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	lockPath := darwinServiceLockPath(root)
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	leasePair, err := darwinSocketPair()
	if err != nil {
		t.Fatal(err)
	}
	gatePair, err := darwinSocketPair()
	if err != nil {
		t.Fatal(err)
	}
	testLease := darwinSocketFile(leasePair[0], "test watchdog lease")
	childLease := darwinSocketFile(leasePair[1], "child watchdog lease")
	testGate := darwinSocketFile(gatePair[0], "test start gate")
	childGate := darwinSocketFile(gatePair[1], "child start gate")
	lockChildFD, err := unix.Dup(int(lock.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	childLock := os.NewFile(uintptr(lockChildFD), "child lock")
	child := exec.Command(os.Args[0], "-test.run=TestDarwinServiceHelperProcess")
	child.Env = append(darwinTestHelperEnvironment("service-child"),
		"DARWIN_SERVICE_TEST_ROOT="+root,
		darwinServiceChildMarkerEnv+"=1",
		darwinServiceLockFDEnv+"=3",
		darwinServiceLeaseFDEnv+"=4",
		darwinServiceGateFDEnv+"=5",
	)
	child.ExtraFiles = []*os.File{childLock, childLease, childGate}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	closeDarwinFiles(childLock, childLease, childGate)
	_ = unix.Close(leasePair[1])
	_ = unix.Close(gatePair[1])
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		closeDarwinFiles(lock, testLease, testGate)
	})
	return root, child, done, testLease, testGate
}
