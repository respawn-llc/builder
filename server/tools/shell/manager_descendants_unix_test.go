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
)

const (
	descendantHelperMarker  = "KENT_SHELL_DESCENDANT_HELPER"
	descendantChildMarker   = "KENT_SHELL_DESCENDANT_CHILD"
	descendantPIDFileEnv    = "KENT_SHELL_DESCENDANT_PID_FILE"
	descendantIgnoreTermEnv = "KENT_SHELL_DESCENDANT_IGNORE_TERM"
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
	t.Cleanup(func() {
		_ = syscall.Kill(descendantPID, syscall.SIGKILL)
	})

	if err := manager.Kill(result.SessionID); err != nil {
		t.Fatalf("kill managed helper: %v", err)
	}
	waitForProcessExit(t, descendantPID, 2*time.Second)
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
	t.Cleanup(func() {
		_ = syscall.Kill(descendantPID, syscall.SIGKILL)
	})

	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	waitForProcessExit(t, descendantPID, 2*time.Second)
}

func TestManagedProcessDescendantHelper(t *testing.T) {
	if os.Getenv(descendantChildMarker) == "1" {
		if os.Getenv(descendantIgnoreTermEnv) == "1" {
			signal.Ignore(syscall.SIGTERM, os.Interrupt)
		}
		pidFile := os.Getenv(descendantPIDFileEnv)
		if pidFile == "" {
			t.Fatal("descendant PID file is required")
		}
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatalf("write descendant PID: %v", err)
		}
		select {}
	}
	if os.Getenv(descendantHelperMarker) != "1" {
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	child := exec.Command(executable, "-test.run=^TestManagedProcessDescendantHelper$")
	child.Env = append(os.Environ(), descendantChildMarker+"=1")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatalf("start descendant: %v", err)
	}
	_ = child.Wait()
	select {}
}

func waitForDescendantPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
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

func waitForProcessExit(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("probe process %d: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d remained alive after %v", pid, timeout)
}
