//go:build !windows

package pty_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/blackbox"
	"core/internal/testharness/pty/driver"

	"github.com/google/uuid"
)

const (
	smokeBuildWait   = 30 * time.Second
	smokeEventWait   = 15 * time.Second
	smokeCleanupWait = 5 * time.Second
)

func TestProductionKentBinaryPTYSmoke(t *testing.T) {
	buildContext, cancelBuild := context.WithTimeout(context.Background(), smokeBuildWait)
	binary, err := pty.BuildOrUsePrebuiltKent(buildContext, filepath.Join(t.TempDir(), "kent"))
	cancelBuild()
	if err != nil {
		t.Fatalf("build production Kent: %v", err)
	}

	probe := uuid.New().String()
	output := "ok"
	environment, err := blackbox.NewIsolatedEnvironment(binary, []blackbox.RequiredOperation{{
		ID:              uuid.New(),
		Route:           blackbox.RouteResponses,
		Probe:           &probe,
		Outcome:         blackbox.OutcomeStream,
		Output:          &output,
		ResponsePhase:   blackbox.NewResponsePhase(blackbox.ResponsePhaseFinal),
		SessionCacheKey: true,
	}})
	var session *driver.Session
	if environment != nil {
		t.Cleanup(func() {
			cleanupPTYSmoke(t, session, environment)
		})
	}
	if err != nil {
		t.Fatalf("start isolated configured server: %v; %s", err, smokeDiagnostics(session, environment))
	}
	if err := environment.WaitReady(); err != nil {
		t.Fatalf("wait for isolated server readiness: %v; %s", err, smokeDiagnostics(session, environment))
	}
	if err := environment.BindProject(); err != nil {
		t.Fatalf("bind isolated server workspace: %v; %s", err, smokeDiagnostics(session, environment))
	}
	clientEnvironment, err := environment.ClientEnvironment()
	if err != nil {
		t.Fatalf("build isolated client environment: %v; %s", err, smokeDiagnostics(session, environment))
	}
	session, err = driver.StartSession(driver.SessionSpec{
		Path:       binary,
		Args:       []string{"--force-interactive", "--persistence-root", environment.Root},
		Dir:        environment.Workspace,
		Env:        clientEnvironment,
		Dimensions: pty.MustDimensions(40, 120),
	})
	if err != nil {
		t.Fatalf("start production Kent PTY: %v; %s", err, smokeDiagnostics(session, environment))
	}

	processExit, err := runModelBoundarySmoke(session, environment, probe)
	if err != nil {
		t.Fatalf("run typed model-boundary smoke: %v; %s", err, smokeDiagnostics(session, environment))
	}
	select {
	case <-session.Done():
	case <-time.After(smokeEventWait):
		t.Fatalf("client PTY reactor did not stop; %s", smokeDiagnostics(session, environment))
	}

	capture, err := session.Capture()
	if err != nil {
		t.Fatalf("collect production Kent capture: %v; %s", err, smokeDiagnostics(session, environment))
	}
	if len(capture.Raw) == 0 {
		t.Fatal("expected production Kent to emit terminal bytes")
	}
	if _, err := pty.Analyze(capture); err != nil {
		t.Fatalf("analyze production Kent smoke: %v", err)
	}
	if capture.ProcessExit == nil || processExit == nil {
		t.Fatal("expected recorded process exit state")
	}
	if capture.ProcessExit.Code != 0 && !capture.ProcessExit.Signaled {
		t.Fatalf("process exit = %#v, want zero exit or signal", capture.ProcessExit)
	}
	if err := environment.Stub.Verify(); err != nil {
		t.Fatalf("verify model-boundary operation after client exit: %v; %s", err, smokeDiagnostics(session, environment))
	}
}

func TestPromptReadyRequiresCursorVisibilityAfterMostRecentAlternateExit(t *testing.T) {
	t.Parallel()

	if !promptReady([]analyzer.PrivateModeChange{{Mode: 25, Enabled: true}}) {
		t.Fatal("startup cursor-visible transition did not satisfy prompt readiness")
	}
	if promptReady([]analyzer.PrivateModeChange{
		{Mode: 1049, Enabled: true},
		{Mode: 25, Enabled: true},
	}) {
		t.Fatal("cursor-visible transition inside alternate screen satisfied prompt readiness")
	}
	if promptReady([]analyzer.PrivateModeChange{
		{Mode: 25, Enabled: true},
		{Mode: 1049, Enabled: true},
		{Mode: 1049, Enabled: false},
	}) {
		t.Fatal("cursor transition before alternate exit satisfied prompt readiness")
	}
	if !promptReady([]analyzer.PrivateModeChange{
		{Mode: 1049, Enabled: true},
		{Mode: 1049, Enabled: false},
		{Mode: 25, Enabled: true},
	}) {
		t.Fatal("cursor-visible transition after alternate exit did not satisfy prompt readiness")
	}
}

func promptReady(changes []analyzer.PrivateModeChange) bool {
	alternateScreenActive := false
	cursorVisibleAfterBoundary := false
	for _, change := range changes {
		if change.Mode == 1049 {
			alternateScreenActive = change.Enabled
			cursorVisibleAfterBoundary = false
			continue
		}
		if change.Mode == 25 && change.Enabled && !alternateScreenActive {
			cursorVisibleAfterBoundary = true
		}
	}
	return !alternateScreenActive && cursorVisibleAfterBoundary
}

type smokeStage string

const (
	smokeAwaitingPrompt      smokeStage = "prompt readiness"
	smokeAwaitingSubmit      smokeStage = "prompt submission"
	smokeAwaitingModel       smokeStage = "model operation"
	smokeAwaitingProcessExit smokeStage = "process exit"
)

func runModelBoundarySmoke(
	session *driver.Session,
	environment *blackbox.IsolatedEnvironment,
	probe string,
) (*analyzer.ProcessExit, error) {
	submitID := uuid.New()
	terminateID := uuid.New()
	terminateCompleted := false
	stage := smokeAwaitingPrompt
	deadline := time.NewTimer(smokeEventWait)
	defer deadline.Stop()
	for {
		snapshot := environment.Stub.Snapshot()
		if snapshot.Failure != nil {
			return nil, fmt.Errorf("Responses stub failed during %s: %w", stage, snapshot.Failure)
		}
		if stage == smokeAwaitingModel && snapshot.RequiredConsumed() {
			if err := session.Enqueue(driver.SessionCommand{
				ID: terminateID, Kind: driver.SessionCommandTerminateProcess,
			}); err != nil {
				return nil, fmt.Errorf("enqueue client termination: %w", err)
			}
			stage = smokeAwaitingProcessExit
		}
		select {
		case event, ok := <-session.Events():
			if !ok {
				return nil, fmt.Errorf("client PTY closed during %s", stage)
			}
			if event.Kind == driver.SessionEventFailure {
				return nil, fmt.Errorf("client PTY failed during %s: %w", stage, event.Err)
			}
			if event.Kind == driver.SessionEventCommandFailed {
				return nil, fmt.Errorf("PTY command %s failed during %s: %w", event.CommandID, stage, event.Err)
			}
			if event.Kind == driver.SessionEventProcessExit {
				if stage != smokeAwaitingProcessExit || !terminateCompleted {
					return nil, fmt.Errorf("client exited during %s: %#v", stage, event.ProcessExit)
				}
				return event.ProcessExit, nil
			}
			switch stage {
			case smokeAwaitingPrompt:
				if event.Analysis != nil && promptReady(event.Analysis.PrivateModeChanges) {
					if err := session.Enqueue(driver.SessionCommand{
						ID: submitID, Kind: driver.SessionCommandWrite, Bytes: []byte(probe + "\r"),
					}); err != nil {
						return nil, fmt.Errorf("enqueue model-boundary probe: %w", err)
					}
					stage = smokeAwaitingSubmit
				}
			case smokeAwaitingSubmit:
				if event.Kind == driver.SessionEventCommandCompleted && event.CommandID == submitID {
					stage = smokeAwaitingModel
				}
			case smokeAwaitingProcessExit:
				if event.Kind == driver.SessionEventCommandCompleted && event.CommandID == terminateID {
					terminateCompleted = true
				}
			}
		case <-session.Failure():
			if err := session.Error(); err != nil {
				return nil, fmt.Errorf("client PTY failed during %s: %w", stage, err)
			}
			return nil, fmt.Errorf("client PTY reported an unknown failure during %s", stage)
		case <-environment.Stub.Events():
		case <-environment.Server.Failure():
			if err := environment.Server.Error(); err != nil {
				return nil, fmt.Errorf("isolated server failed during %s: %w", stage, err)
			}
			return nil, fmt.Errorf("isolated server reported an unknown failure during %s", stage)
		case <-environment.Server.Done():
			return nil, fmt.Errorf("isolated server exited during %s", stage)
		case <-deadline.C:
			return nil, fmt.Errorf("model-boundary smoke timed out during %s after %s", stage, smokeEventWait)
		}
	}
}

func cleanupPTYSmoke(t *testing.T, session *driver.Session, environment *blackbox.IsolatedEnvironment) {
	t.Helper()
	ownersStopped := true
	if session != nil {
		if err := session.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("close client PTY admission: %v", err)
		}
		killAttempted := false
		select {
		case <-session.Done():
		default:
			killAttempted = true
			if err := session.ForceKill(); err != nil {
				ownersStopped = false
				t.Errorf("force-kill client PTY: %v", err)
			}
			select {
			case <-session.Done():
			case <-time.After(smokeCleanupWait):
				ownersStopped = false
				t.Errorf("client PTY did not stop within %s", smokeCleanupWait)
			}
		}
		capture, captureErr := session.Capture()
		if captureErr != nil || capture.ProcessExit == nil {
			if !killAttempted {
				if err := session.ForceKill(); err != nil {
					t.Errorf("force-kill reactor-failed client PTY: %v", err)
				}
			}
			ownersStopped = false
			t.Errorf("client process exit could not be confirmed; retaining isolated run root: %v", captureErr)
		}
	}
	if environment.Server != nil {
		select {
		case <-environment.Server.Done():
		default:
			if err := environment.Server.ForceKill(); err != nil {
				t.Errorf("force-kill isolated server: %v", err)
			}
			select {
			case <-environment.Server.Done():
			case <-time.After(smokeCleanupWait):
				ownersStopped = false
				t.Errorf("isolated server did not stop within %s", smokeCleanupWait)
			}
		}
	}
	if environment.Stub != nil {
		if err := environment.Stub.Stop(); err != nil {
			t.Errorf("stop Responses stub: %v", err)
		}
		select {
		case <-environment.Stub.Done():
		case <-time.After(smokeCleanupWait):
			ownersStopped = false
			t.Errorf("Responses stub did not stop within %s", smokeCleanupWait)
		}
	}
	if !ownersStopped {
		t.Logf("retaining isolated run root after incomplete owner cleanup: %s", environment.Root)
		return
	}
	if environment.Root != "" {
		if err := os.RemoveAll(environment.Root); err != nil {
			t.Errorf("remove isolated run root %q: %v", environment.Root, err)
		}
	}
}

func smokeDiagnostics(session *driver.Session, environment *blackbox.IsolatedEnvironment) string {
	if environment == nil {
		return "environment=<nil>"
	}
	type captureDiagnostic struct {
		Bytes       int
		Chunks      int
		ProcessExit *analyzer.ProcessExit
	}
	var captureSummary *captureDiagnostic
	var captureErr error
	if session != nil {
		select {
		case <-session.Done():
			capture, err := session.Capture()
			if err != nil {
				captureErr = err
			} else {
				captureSummary = &captureDiagnostic{
					Bytes:       len(capture.Raw),
					Chunks:      len(capture.Chunks),
					ProcessExit: capture.ProcessExit,
				}
			}
		default:
		}
	}
	var stub *blackbox.StubSnapshot
	if environment.Stub != nil {
		snapshot := environment.Stub.Snapshot()
		stub = &snapshot
	}
	var serverErr error
	if environment.Server != nil {
		serverErr = environment.Server.Error()
	}
	return fmt.Sprintf(
		"run_root=%s stub=%#v server_error=%v capture=%#v capture_error=%v",
		environment.Root,
		stub,
		serverErr,
		captureSummary,
		captureErr,
	)
}
