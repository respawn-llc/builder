package blackbox

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/driver"
)

func TestRunnerExecutesDeclaredGoModelBoundaryScenario(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "kent")
	build := exec.CommandContext(context.Background(), "./scripts/build.sh", "server", "--output", binary)
	build.Dir = filepath.Join("..", "..", "..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Logf("build output:\n%s", output)
		t.Fatalf("build compiled Kent client: %v", err)
	}
	// The build output is a newly materialized macOS executable. Execute its
	// lightweight version path before timing the harness's fixed 500 ms server
	// readiness contract so first-exec loader work is not attributed to server
	// readiness.
	if output, err := exec.CommandContext(context.Background(), binary, "--version").CombinedOutput(); err != nil {
		t.Logf("version output:\n%s", output)
		t.Fatalf("preflight compiled Kent client: %v", err)
	}
	scenario, err := LoadScenario("testdata/go-model-boundary.json")
	if err != nil {
		t.Fatalf("LoadScenario: %v", err)
	}
	result := (Runner{}).Run(RunRequest{
		Scenario:     scenario,
		Profile:      GoProfile,
		ClientBinary: binary,
		ServerBinary: binary,
	})
	if result.Err != nil {
		t.Fatalf("Run: %v; run_root=%s; artifacts=%s", result.Err, result.RunRoot, result.ArtifactDir)
	}
	if !result.Observation.Model.RequiredConsumed() {
		t.Fatalf("declared Responses proof was not consumed: %#v", result.Observation.Model)
	}
	if !result.Observation.ClientExited {
		t.Fatal("client did not exit after declared termination action")
	}
}

func TestCleanupForceKillsTERMAndHUPIgnoringClientAtGraceDeadline(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "ansi-writer")
	if err := driver.BuildPackage(context.Background(), "core/internal/testharness/pty/testdata/cmd/ansi-writer", binary); err != nil {
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
		_ = session.Close()
		_ = session.ForceKill()
	})
	waitForVisibleCursor(t, session)

	started := time.Now()
	incomplete := cleanup(session, nil, false, started.Add(fixedWait))
	elapsed := time.Since(started)
	if incomplete != nil {
		t.Fatalf("cleanup incomplete: %v", incomplete)
	}
	if elapsed < fixedWait/2 {
		t.Fatalf("cleanup completed before the TERM grace elapsed: %s", elapsed)
	}
	if elapsed > fixedWait+100*time.Millisecond {
		t.Fatalf("cleanup exceeded its total deadline: %s", elapsed)
	}
	select {
	case <-session.Done():
	default:
		t.Fatal("TERM-ignoring client remains live after cleanup")
	}
}

func waitForVisibleCursor(t *testing.T, session *driver.Session) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case event, ok := <-session.Events():
			if !ok {
				t.Fatal("client exited before cursor-visible readiness")
			}
			if event.Analysis == nil {
				continue
			}
			for _, change := range event.Analysis.PrivateModeChanges {
				if change.Mode == 25 && change.Enabled {
					return
				}
			}
		case <-deadline.C:
			t.Fatal("client did not emit cursor-visible readiness")
		}
	}
}
