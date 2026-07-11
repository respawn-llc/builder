//go:build !windows

package blackbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
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
	sessionOwner := session
	var environment *IsolatedEnvironment
	incomplete := (cleanupSupervisor{session: &sessionOwner, environment: &environment}).stopOwners(session, nil, started.Add(fixedWait))
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

func TestFailureArtifactEvidenceTracksCaptureAvailabilityExplicitly(t *testing.T) {
	dimensions := Dimensions{Rows: 2, Cols: 8}
	empty, err := failureArtifactEvidence(nil, nil, dimensions, nil)
	if err != nil {
		t.Fatalf("failureArtifactEvidence without capture: %v", err)
	}
	if empty.capture.Dimensions.Rows != dimensions.Rows || empty.capture.Dimensions.Cols != dimensions.Cols {
		t.Fatalf("synthetic capture dimensions = %+v, want %+v", empty.capture.Dimensions, dimensions)
	}

	capture, err := analyzer.NewCapture(analyzer.MustDimensions(3, 9), []analyzer.Chunk{
		analyzer.NewChunk(0, 0, []byte("captured")),
	})
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	evidence, err := failureArtifactEvidence(&capture, nil, dimensions, nil)
	if err != nil {
		t.Fatalf("failureArtifactEvidence with capture: %v", err)
	}
	if string(evidence.capture.Raw) != "captured" {
		t.Fatalf("failure evidence lost collected capture: %#v", evidence.capture)
	}
}

func TestCleanupRemovesOnlyCompletedSuccessfulRunRootWithinDeadline(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "evidence.bin"), []byte("bounded"), 0o600); err != nil {
		t.Fatalf("write root content: %v", err)
	}
	artifacts, err := newArtifactStore(t.TempDir()).beginRun()
	if err != nil {
		t.Fatalf("begin artifact run: %v", err)
	}
	var session *driver.Session
	environment := &IsolatedEnvironment{Root: root}
	result := RunResult{}
	(cleanupSupervisor{artifacts: artifacts, session: &session, environment: &environment}).finish(&result, Dimensions{Rows: 2, Cols: 8})
	if result.Err != nil {
		t.Fatalf("cleanup result: %v", result.Err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("successful root cleanup stat error = %v, want not exist", err)
	}
}

func TestCleanupRetainsRunRootWhenItsDeadlineHasElapsed(t *testing.T) {
	root := t.TempDir()
	artifacts, err := newArtifactStore(t.TempDir()).beginRun()
	if err != nil {
		t.Fatalf("begin artifact run: %v", err)
	}
	var session *driver.Session
	environment := &IsolatedEnvironment{Root: root}
	result := RunResult{}
	(cleanupSupervisor{artifacts: artifacts, session: &session, environment: &environment}).finishUntil(&result, Dimensions{Rows: 2, Cols: 8}, time.Now().Add(-time.Millisecond))
	if result.Err == nil {
		t.Fatal("cleanup succeeded after its deadline elapsed")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("expired cleanup removed retained root: %v", err)
	}
}

func TestCleanupReportsNonReturningModelStubHandlerWithoutReplacingPrimaryFailure(t *testing.T) {
	stub, err := StartResponsesStub(nil)
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	_, releaseHandler, admitted := stub.beginHandler(context.Background())
	if !admitted {
		t.Fatal("non-returning model handler was not admitted")
	}

	var session *driver.Session
	environment := &IsolatedEnvironment{Stub: stub}
	result := RunResult{Err: errors.New("primary scenario failure")}
	(cleanupSupervisor{session: &session, environment: &environment}).finishUntil(
		&result,
		Dimensions{Rows: 2, Cols: 8},
		time.Now().Add(20*time.Millisecond),
	)
	if result.Err == nil || result.Err.Error() != "primary scenario failure" {
		t.Fatalf("cleanup replaced primary failure: %v", result.Err)
	}
	if result.Cleanup == nil || !slices.Contains(result.Cleanup.Owners, "model_stub") {
		t.Fatalf("cleanup did not report live model stub owner: %#v", result.Cleanup)
	}

	releaseHandler()
	select {
	case <-stub.Done():
	case <-time.After(time.Second):
		t.Fatal("model stub did not complete after non-returning handler released")
	}
}

func TestCleanupReportsStalledArtifactPublisherWithoutReplacingPrimaryFailure(t *testing.T) {
	release := make(chan struct{})
	publisher := startArtifactPublisher(func() artifactPublicationOutcome {
		<-release
		return artifactPublicationOutcome{}
	})
	artifacts := &artifactRun{publisher: publisher}
	var session *driver.Session
	var environment *IsolatedEnvironment
	result := RunResult{Err: errors.New("primary scenario failure")}
	(cleanupSupervisor{artifacts: artifacts, session: &session, environment: &environment}).finishUntil(
		&result,
		Dimensions{Rows: 2, Cols: 8},
		time.Now().Add(20*time.Millisecond),
	)
	if result.Err == nil || result.Err.Error() != "primary scenario failure" {
		t.Fatalf("cleanup replaced primary failure: %v", result.Err)
	}
	if result.Cleanup == nil || !slices.Contains(result.Cleanup.Owners, "artifact_publisher") {
		t.Fatalf("cleanup did not report artifact publisher: %#v", result.Cleanup)
	}

	close(release)
	select {
	case <-publisher.Done():
	case <-time.After(time.Second):
		t.Fatal("artifact publisher did not complete after release")
	}
}

func waitForVisibleCursor(t *testing.T, session *driver.Session) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
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
