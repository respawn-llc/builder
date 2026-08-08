package workflowexecution

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/sessioncontract"
)

type blockingSessionPersistenceObserver struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (o *blockingSessionPersistenceObserver) ObservePersistedStore(
	ctx context.Context,
	_ session.PersistedStoreSnapshot,
) error {
	o.once.Do(func() { close(o.entered) })
	select {
	case <-o.release:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func TestLifecycleCaptureDoesNotWaitForSessionPersistence(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-session-persistence-slow", "node-agent")
	store := &currentNodeControllerStore{
		started: workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
			Created: []workflow.CurrentNode{{
				Reference:  reference,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}},
		}},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &blockingCurrentNodeRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	observer := &blockingSessionPersistenceObserver{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(observer.release) }) }
	t.Cleanup(func() {
		release()
		close(runner.release)
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	sessionRoot := filepath.Join(t.TempDir(), "sessions")
	workspaceRoot := t.TempDir()
	if _, err := controller.StartTask(context.Background(), reference.TaskID, func(context.Context) error {
		sessionStore, err := session.Create(
			sessionRoot,
			"workspace",
			workspaceRoot,
			sessioncontract.SessionCategoryMain,
			session.WithPersistenceObserver(observer),
		)
		if err != nil {
			return err
		}
		return sessionStore.EnsureDurable()
	}); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	awaitControllerBarrier(t, observer.entered, "Session persistence did not reach its observer boundary")
	requireControllerPublicationBoundaryAvailable(t, store)
	requireControllerLifecycleCaptureAvailable(t, controller)
	release()
}

type blockingAssignmentEnsurer struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingAssignmentEnsurer) EnsureCurrentNodeAssignment(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
) (CurrentNodeAssignmentEnsure, error) {
	return s, nil
}

func (s *blockingAssignmentEnsurer) Wait(ctx context.Context) (session.CommitReceipt, error) {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
		return session.CommitReceipt{Committed: true}, nil
	case <-ctx.Done():
		return session.CommitReceipt{}, context.Cause(ctx)
	}
}

func TestLifecycleCaptureDoesNotWaitForAssignmentDelivery(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-assignment-slow", "node-agent")
	store := &currentNodeControllerStore{
		started: workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
			Created: []workflow.CurrentNode{{
				Reference:  reference,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}},
		}},
	}
	ensurer := &blockingAssignmentEnsurer{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller, err := NewCurrentNodeController(
		store,
		&countingCurrentNodeRunner{},
		authority,
		NewMutationPermit(),
		CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentEnsurer: ensurer,
		},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(ensurer.release) }) }
	t.Cleanup(func() {
		release()
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	if _, err := controller.StartTask(context.Background(), reference.TaskID, func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	awaitControllerBarrier(t, ensurer.entered, "assignment delivery did not begin")
	requireControllerPublicationBoundaryAvailable(t, store)
	requireControllerLifecycleCaptureAvailable(t, controller)
	release()
}

func TestLifecycleCaptureDoesNotWaitForScriptProcessStop(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-process-stop-slow", "node-script")
	store := &currentNodeControllerStore{
		started: workflowstore.StartTaskResult{
			Mutation: workflow.CurrentNodeMutationResult{Created: []workflow.CurrentNode{{
				Reference:  reference,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}}},
			CreatedNodeKind: workflow.NodeKindScript,
		},
	}
	stopRoot := t.TempDir()
	runEntered := filepath.Join(stopRoot, "running")
	stopEntered := filepath.Join(stopRoot, "entered")
	stopRelease := filepath.Join(stopRoot, "release")
	grace := 10 * time.Second
	runner := &recordingScriptRunner{
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", `trap 'touch "$STOP_ENTERED"; while [ ! -e "$STOP_RELEASE" ]; do sleep 0.01; done; exit 0' TERM; touch "$RUN_ENTERED"; while :; do sleep 1; done`},
			Env: append(os.Environ(),
				"RUN_ENTERED="+runEntered,
				"STOP_ENTERED="+stopEntered,
				"STOP_RELEASE="+stopRelease,
			),
			CancellationGrace: &grace,
		},
		started: make(chan workflow.CurrentNodeReference, 1),
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner.authority = authority
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			if err := os.WriteFile(stopRelease, []byte("release"), 0o600); err != nil {
				t.Errorf("release stopped process: %v", err)
			}
		})
	}
	t.Cleanup(func() {
		release()
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	if _, err := controller.StartTask(context.Background(), reference.TaskID, func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitForRunningCurrentNode(t, authority, reference)
	testsetup.RequireUntil(t, time.Now().Add(10*time.Second), 10*time.Millisecond, func() bool {
		_, err := os.Stat(runEntered)
		return err == nil
	}, "Script process did not enter its running barrier")
	interrupted := make(chan error, 1)
	go func() {
		interrupted <- controller.Interrupt(context.Background(), InterruptSelector{TaskID: reference.TaskID})
	}()
	testsetup.RequireUntil(t, time.Now().Add(10*time.Second), 10*time.Millisecond, func() bool {
		_, err := os.Stat(stopEntered)
		return err == nil
	}, "Script process did not enter controlled stop barrier")
	select {
	case err := <-interrupted:
		t.Fatalf("Interrupt returned before process-stop barrier release: %v", err)
	default:
	}
	requireControllerPublicationBoundaryAvailable(t, store)
	requireControllerLifecycleCaptureAvailable(t, controller)
	release()
	select {
	case err := <-interrupted:
		if err != nil {
			t.Fatalf("Interrupt: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Interrupt did not finish after process-stop barrier release")
	}
}

func TestLifecycleCaptureDoesNotWaitForResultFinalization(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-result-finalization-slow", "node-script")
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{{
			Reference:  reference,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
		}},
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &runningAndFinalizingScriptRunner{
		authority:           authority,
		shellPath:           shellPath,
		finalizing:          reference,
		finalizerEntered:    make(chan struct{}),
		releaseFinalizer:    make(chan struct{}),
		finalizerCompletion: make(chan error, 1),
		successorStarted:    make(chan struct{}, 1),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(runner.releaseFinalizer) }) }
	t.Cleanup(func() {
		release()
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	if _, err := controller.ResumeTask(context.Background(), reference.TaskID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	awaitControllerBarrier(t, runner.finalizerEntered, "result finalization did not begin")
	requireControllerPublicationBoundaryAvailable(t, store)
	requireControllerLifecycleCaptureAvailable(t, controller)
	release()
	select {
	case err := <-runner.finalizerCompletion:
		if err != nil {
			t.Fatalf("result finalization: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("result finalization did not finish after barrier release")
	}
}

func requireControllerPublicationBoundaryAvailable(
	t *testing.T,
	store *currentNodeControllerStore,
) {
	t.Helper()
	publication, ok := store.lifecyclePublication().(*currentNodeControllerLifecyclePublication)
	if !ok {
		t.Fatalf("lifecycle publication type = %T, want controller test publication", store.lifecyclePublication())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if publication.mu.TryLock() {
			publication.mu.Unlock()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("slow lifecycle work retained publication critical section")
}

func requireControllerLifecycleCaptureAvailable(t *testing.T, controller *CurrentNodeController) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	capture, err := controller.CaptureLifecycle(ctx)
	if err != nil {
		t.Fatalf("CaptureLifecycle while slow work paused: %v", err)
	}
	if err := capture.Close(); err != nil {
		t.Fatalf("close lifecycle capture: %v", err)
	}
}

func awaitControllerBarrier(t *testing.T, entered <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal(failure)
	}
}

var _ session.PersistenceObserver = (*blockingSessionPersistenceObserver)(nil)
var _ CurrentNodeAssignmentEnsurer = (*blockingAssignmentEnsurer)(nil)
var _ CurrentNodeAssignmentEnsure = (*blockingAssignmentEnsurer)(nil)
var _ workflowruntime.Controller = (*CurrentNodeController)(nil)
