//go:build !windows

package blackbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/driver"

	"github.com/google/uuid"
)

func TestTerminateProcessCommandForceKillsTermIgnoringClient(t *testing.T) {
	session := startTermIgnoringSession(t)
	if err := session.Enqueue(driver.SessionCommand{ID: uuid.New(), Kind: driver.SessionCommandTerminateProcess}); err != nil {
		t.Fatalf("enqueue terminate: %v", err)
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("terminate command did not end TERM-ignoring client")
	}
	capture, err := session.Capture()
	if err != nil {
		t.Fatalf("capture terminated client: %v", err)
	}
	if capture.ProcessExit == nil {
		t.Fatal("terminate command did not record process exit")
	}
}

func startTermIgnoringSession(t *testing.T) *driver.Session {
	t.Helper()
	binary, err := pty.BuildOrUsePrebuiltPackage(
		context.Background(),
		pty.AnsiWriterBinaryEnvName,
		"core/internal/testharness/pty/testdata/cmd/ansi-writer",
		filepath.Join(t.TempDir(), "ansi-writer"),
	)
	if err != nil {
		t.Fatalf("build PTY helper: %v", err)
	}
	session, err := driver.StartSession(driver.SessionSpec{
		Path:       binary,
		Args:       []string{"ignore-term"},
		Env:        []string{"TERM=xterm-256color", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"},
		Dimensions: pty.MustDimensions(2, 8),
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-session.Done():
			if capture, err := session.Capture(); err == nil && capture.ProcessExit != nil {
				break
			}
			if err := session.ForceKill(); err != nil {
				t.Errorf("force-kill reactor-failed PTY session: %v", err)
			}
			t.Errorf("PTY reactor stopped without a recorded process exit")
		default:
			if err := session.ForceKill(); err != nil {
				t.Errorf("force-kill PTY session: %v", err)
			}
			select {
			case <-session.Done():
				if capture, err := session.Capture(); err != nil || capture.ProcessExit == nil {
					t.Errorf("PTY session stopped without a recorded process exit: %v", err)
				}
			case <-time.After(time.Second):
				t.Errorf("PTY session did not stop after force-kill")
			}
		}
		if err := session.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("close PTY session: %v", err)
		}
	})
	return session
}
