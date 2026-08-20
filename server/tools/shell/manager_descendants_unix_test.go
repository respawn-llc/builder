//go:build darwin || linux

package shell

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"core/server/internal/testprocess"
)

const (
	descendantHelperMarker  = "KENT_SHELL_DESCENDANT_HELPER"
	descendantChildMarker   = "KENT_SHELL_DESCENDANT_CHILD"
	descendantPIDFileEnv    = "KENT_SHELL_DESCENDANT_PID_FILE"
	descendantIgnoreTermEnv = "KENT_SHELL_DESCENDANT_IGNORE_TERM"
	completedHelperMarker   = "KENT_SHELL_COMPLETED_HELPER"
	completedGroupMarker    = "KENT_SHELL_COMPLETED_GROUP_HELPER"
)

func TestManagerKillTerminatesDescendantsInIndependentProcessGroups(t *testing.T) {
	manager := newShellTestManager(t, 50*time.Millisecond)
	pidFile := t.TempDir() + "/descendant.pid"
	t.Setenv(descendantHelperMarker, "1")
	t.Setenv(descendantPIDFileEnv, pidFile)

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{executable, "-test.run=^TestManagedProcessDescendantHelper$"},
		DisplayCommand: "managed descendant test helper",
		Workdir:        t.TempDir(),
		YieldTime:      50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start managed helper: %v", err)
	}
	if !result.MovedToBackground || !result.Running {
		t.Fatalf("managed helper did not move to background: %+v", result)
	}

	descendantPID := waitForDescendantPID(t, pidFile)
	exited := false
	t.Cleanup(func() {
		if exited {
			return
		}
		_ = syscall.Kill(descendantPID, syscall.SIGKILL)
	})

	if err := manager.Kill(result.SessionID); err != nil {
		t.Fatalf("kill managed helper: %v", err)
	}
	testprocess.WaitForExit(t, descendantPID, 2*time.Second)
	exited = true
}

func TestManagerCloseForceKillsIndependentDescendantsAfterGracePeriod(t *testing.T) {
	manager, err := NewManager(
		WithMinimumExecToBgTime(50*time.Millisecond),
		WithCloseTimeouts(50*time.Millisecond, 500*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	pidFile := t.TempDir() + "/descendant.pid"
	t.Setenv(descendantHelperMarker, "1")
	t.Setenv(descendantPIDFileEnv, pidFile)
	t.Setenv(descendantIgnoreTermEnv, "1")

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{executable, "-test.run=^TestManagedProcessDescendantHelper$"},
		DisplayCommand: "managed descendant test helper",
		Workdir:        t.TempDir(),
		YieldTime:      50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start managed helper: %v", err)
	}
	if !result.MovedToBackground || !result.Running {
		t.Fatalf("managed helper did not move to background: %+v", result)
	}

	descendantPID := waitForDescendantPID(t, pidFile)
	exited := false
	t.Cleanup(func() {
		if exited {
			return
		}
		_ = syscall.Kill(descendantPID, syscall.SIGKILL)
	})

	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	testprocess.WaitForExit(t, descendantPID, 2*time.Second)
	exited = true
}

func TestManagerKillRejectsCompletedRetainedProcess(t *testing.T) {
	manager := newShellTestManager(t, 50*time.Millisecond)
	t.Setenv(completedHelperMarker, "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{executable, "-test.run=^TestManagedProcessDescendantHelper$"},
		DisplayCommand: "completed managed helper",
		Workdir:        t.TempDir(),
		YieldTime:      50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start completed helper: %v", err)
	}
	if !result.MovedToBackground || !result.Running {
		t.Fatalf("completed helper did not move to background: %+v", result)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, snapshotErr := manager.Snapshot(result.SessionID)
		if snapshotErr != nil {
			t.Fatalf("snapshot completed helper: %v", snapshotErr)
		}
		if !snapshot.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for completed helper")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := manager.Kill(result.SessionID); err == nil {
		t.Fatal("kill accepted a completed retained process")
	}
}

func TestManagerCloseTerminatesInheritedProcessGroupAfterRootExit(t *testing.T) {
	manager := newShellTestManager(t, 50*time.Millisecond)
	pidFile := t.TempDir() + "/descendant.pid"
	t.Setenv(completedGroupMarker, "1")
	t.Setenv(descendantPIDFileEnv, pidFile)

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	result, err := manager.Start(context.Background(), ExecRequest{
		Command:        []string{executable, "-test.run=^TestManagedProcessDescendantHelper$"},
		DisplayCommand: "completed managed process group helper",
		Workdir:        t.TempDir(),
		YieldTime:      50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start managed helper: %v", err)
	}
	if !result.MovedToBackground || !result.Running {
		t.Fatalf("managed helper did not move to background: %+v", result)
	}

	descendantPID := waitForDescendantPIDWithin(t, pidFile, 10*time.Second)
	exited := false
	t.Cleanup(func() {
		if exited {
			return
		}
		_ = syscall.Kill(descendantPID, syscall.SIGKILL)
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, snapshotErr := manager.Snapshot(result.SessionID)
		if snapshotErr != nil {
			t.Fatalf("snapshot managed helper: %v", snapshotErr)
		}
		if !snapshot.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for managed helper root to exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(descendantPID, 0); err != nil {
		t.Fatalf("inherited-group descendant exited before manager close: %v", err)
	}

	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	testprocess.WaitForExit(t, descendantPID, 2*time.Second)
	exited = true
}

func TestManagedProcessDescendantHelper(t *testing.T) {
	if os.Getenv(descendantChildMarker) == "1" {
		if os.Getenv(descendantIgnoreTermEnv) == "1" {
			signal.Ignore(syscall.SIGTERM, os.Interrupt)
		}
		if os.Getenv(completedGroupMarker) == "1" {
			signal.Ignore(syscall.SIGHUP, syscall.SIGTERM, os.Interrupt)
			publishDescendantPID(t, os.Getenv(descendantPIDFileEnv), os.Getpid())
			time.Sleep(30 * time.Second)
			return
		}
		pidFile := os.Getenv(descendantPIDFileEnv)
		if pidFile == "" {
			t.Fatal("descendant PID file is required")
		}
		publishDescendantPID(t, pidFile, os.Getpid())
		select {}
	}
	if os.Getenv(completedHelperMarker) == "1" {
		time.Sleep(200 * time.Millisecond)
		return
	}
	if os.Getenv(completedGroupMarker) == "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatalf("resolve test executable: %v", err)
		}
		child := exec.CommandContext(context.Background(), executable, "-test.run=^TestManagedProcessDescendantHelper$")
		child.Env = append(os.Environ(), descendantChildMarker+"=1")
		if err := child.Start(); err != nil {
			t.Fatalf("start inherited-group descendant: %v", err)
		}
		waitForDescendantPIDWithin(t, os.Getenv(descendantPIDFileEnv), 10*time.Second)
		time.Sleep(100 * time.Millisecond)
		return
	}
	if os.Getenv(descendantHelperMarker) != "1" {
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	child := exec.CommandContext(context.Background(), executable, "-test.run=^TestManagedProcessDescendantHelper$")
	child.Env = append(os.Environ(), descendantChildMarker+"=1")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatalf("start descendant: %v", err)
	}
	_ = child.Wait()
	select {}
}

func publishDescendantPID(t *testing.T, path string, pid int) {
	t.Helper()
	if path == "" {
		t.Fatal("descendant PID file is required")
	}
	pendingPIDFile := path + ".pending"
	if err := os.WriteFile(pendingPIDFile, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		t.Fatalf("write descendant PID: %v", err)
	}
	if err := os.Rename(pendingPIDFile, path); err != nil {
		t.Fatalf("publish descendant PID: %v", err)
	}
}

func waitForDescendantPID(t *testing.T, path string) int {
	return waitForDescendantPIDWithin(t, path, 2*time.Second)
}

func waitForDescendantPIDWithin(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if parseErr != nil {
				t.Fatalf("parse descendant PID: %v", parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read descendant PID: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for descendant PID")
	return 0
}
