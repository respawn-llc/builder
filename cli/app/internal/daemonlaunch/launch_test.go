package daemonlaunch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	goruntime "runtime"
	"testing"
	"time"
)

func TestLaunchDialsSpawnedPIDAndReturnsOwnedClose(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("shell helper is unix-only")
	}
	target, closeFn, ok, err := Launch[int](context.Background(), Request[int]{
		ExecutablePath: "/bin/sh",
		Args:           []string{"-c", "sleep 30"},
		ConnectTimeout: time.Second,
		PollInterval:   time.Millisecond,
		Dial: func(_ context.Context, childPID int) (int, bool, error) {
			if childPID == 0 {
				t.Fatal("expected child pid")
			}
			return childPID, true, nil
		},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if !ok {
		t.Fatal("expected launched target")
	}
	if target == 0 {
		t.Fatal("expected target pid")
	}
	if closeFn == nil {
		t.Fatal("expected close function")
	}
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestLaunchClosesProcessWhenDialFails(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("shell helper is unix-only")
	}
	wantErr := errors.New("dial failed")
	_, closeFn, ok, err := Launch[int](context.Background(), Request[int]{
		ExecutablePath: "/bin/sh",
		Args:           []string{"-c", "sleep 30"},
		ConnectTimeout: time.Second,
		PollInterval:   time.Millisecond,
		Dial: func(context.Context, int) (int, bool, error) {
			return 0, false, wantErr
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Launch error = %v, want %v", err, wantErr)
	}
	if ok {
		t.Fatal("expected no target")
	}
	if closeFn != nil {
		t.Fatal("expected no close function")
	}
}

func TestNewOwnedProcessCloseFallsBackToKillWhenTerminateFails(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("sleep helper is unix-only")
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Wait()
	}()
	killed := false
	closeFn := NewOwnedProcessClose[int](0, nil, cmd, errCh, Controls{
		Terminate: func(*os.Process) error {
			return errors.New("interrupt unsupported")
		},
		Kill: func(process *os.Process) error {
			killed = true
			if process == nil {
				return nil
			}
			return process.Kill()
		},
	})
	if err := closeFn(); err != nil {
		t.Fatalf("closeFn: %v", err)
	}
	if !killed {
		t.Fatal("expected owned process close to fall back to kill")
	}
}
