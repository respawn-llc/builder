//go:build darwin

package sleepguard

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	caffeinateOwnerHelperEnv = "KENT_CAFFEINATE_OWNER_HELPER"
	caffeinatePIDFileEnv     = "KENT_CAFFEINATE_PID_FILE"
)

func TestCaffeinateExitsWhenOwningProcessExits(t *testing.T) {
	if _, err := exec.LookPath("caffeinate"); err != nil {
		t.Skipf("caffeinate unavailable: %v", err)
	}
	pidFile := t.TempDir() + "/caffeinate.pid"
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	helper := exec.Command(executable, "-test.run=^TestCaffeinateOwnerDeathHelper$")
	helper.Env = append(os.Environ(),
		caffeinateOwnerHelperEnv+"=1",
		caffeinatePIDFileEnv+"="+pidFile,
	)
	if output, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("run caffeinate owner helper: %v\n%s", err, output)
	}

	caffeinatePID := readProcessPID(t, pidFile)
	t.Cleanup(func() {
		_ = syscall.Kill(caffeinatePID, syscall.SIGKILL)
	})
	waitForProcessToExit(t, caffeinatePID, 2*time.Second)
}

func TestCaffeinateOwnerDeathHelper(t *testing.T) {
	if os.Getenv(caffeinateOwnerHelperEnv) != "1" {
		return
	}
	var guard Guard
	if err := guard.Acquire(); err != nil {
		t.Fatalf("acquire guard: %v", err)
	}
	command := ownerDeathCaffeinateCommand(t, &guard)
	pidFile := os.Getenv(caffeinatePIDFileEnv)
	if pidFile == "" {
		t.Fatal("caffeinate PID file is required")
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
		t.Fatalf("write caffeinate PID: %v", err)
	}
}

func ownerDeathCaffeinateCommand(t *testing.T, guard *Guard) *exec.Cmd {
	t.Helper()
	guard.mu.Lock()
	defer guard.mu.Unlock()
	impl, ok := guard.impl.(*platformGuardImpl)
	if !ok || impl.cmd == nil || impl.cmd.Process == nil {
		t.Fatal("expected active caffeinate process")
	}
	return impl.cmd
}

func readProcessPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read process PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse process PID: %v", err)
	}
	return pid
}

func waitForProcessToExit(t *testing.T, pid int, timeout time.Duration) {
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
