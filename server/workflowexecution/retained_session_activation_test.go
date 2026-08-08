package workflowexecution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/runtimecontrol"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestActivateOrAttachRetainedSessionResumesAndAttachesRepeatedOwnersToOneRun(t *testing.T) {
	fixture := newRetainedSessionActivationFixture(t)

	first, handled, err := fixture.controller.ActivateOrAttachRetainedSession(
		context.Background(),
		fixture.sessionID,
		"owner-a",
	)
	if err != nil {
		t.Fatalf("ActivateOrAttachRetainedSession owner-a: %v", err)
	}
	if !handled {
		t.Fatal("retained Workflow Session was classified as ordinary")
	}
	second, handled, err := fixture.controller.ActivateOrAttachRetainedSession(
		context.Background(),
		fixture.sessionID,
		"owner-b",
	)
	if err != nil {
		t.Fatalf("ActivateOrAttachRetainedSession owner-b: %v", err)
	}
	if !handled {
		t.Fatal("repeated retained Workflow Session activation was classified as ordinary")
	}
	if first.Resource() != second.Resource() {
		t.Fatalf("repeated owners attached to resources %v and %v", first.Resource(), second.Resource())
	}
	snapshot := fixture.controller.Snapshot()
	if len(snapshot.LiveScopes) != 1 || len(snapshot.ExplicitStarts) != 0 || len(snapshot.Gates) != 0 {
		t.Fatalf("controller snapshot after repeated activation = %+v, want one live scope", snapshot)
	}
	if fixture.runner.starts() != 1 {
		t.Fatalf("Agent starts = %d, want 1", fixture.runner.starts())
	}
	fixture.store.mu.Lock()
	resumeCount := len(fixture.store.resumed)
	fixture.store.mu.Unlock()
	if resumeCount != 1 {
		t.Fatalf("durable Resume count = %d, want 1", resumeCount)
	}
}

func TestActivateOrAttachRetainedSessionJoinsBoardResume(t *testing.T) {
	fixture := newRetainedSessionActivationFixture(t)
	if _, err := fixture.controller.ResumeTask(context.Background(), fixture.reference.TaskID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	attachment, handled, err := fixture.controller.ActivateOrAttachRetainedSession(
		context.Background(),
		fixture.sessionID,
		"owner-board-first",
	)
	if err != nil {
		t.Fatalf("ActivateOrAttachRetainedSession: %v", err)
	}
	if !handled || attachment.Resource().SessionID() != fixture.sessionID {
		t.Fatalf("Board-first activation = handled:%t resource:%v", handled, attachment.Resource())
	}
	if fixture.runner.starts() != 1 {
		t.Fatalf("Agent starts = %d, want 1", fixture.runner.starts())
	}
}

func TestObserveWorkflowTaskExecutionsExcludesAuthorityScopeAbsentFromPinnedRoot(t *testing.T) {
	fixture := newRetainedSessionActivationFixture(t)
	if _, handled, err := fixture.controller.ActivateOrAttachRetainedSession(
		context.Background(),
		fixture.sessionID,
		"owner-observation-root-filter",
	); err != nil || !handled {
		t.Fatalf("ActivateOrAttachRetainedSession = handled:%t error:%v", handled, err)
	}
	publication, ok := fixture.controller.publication.(*currentNodeControllerLifecyclePublication)
	if !ok {
		t.Fatalf("controller publication type = %T", fixture.controller.publication)
	}
	publication.mu.Lock()
	publication.root = map[workflow.TaskID][]workflow.CurrentNodeReference{}
	publication.exact = map[workflow.TaskID][]workflowstore.LifecycleExactExecution{}
	publication.mu.Unlock()

	observation, err := fixture.controller.ObserveWorkflowTaskExecutions(nil)
	if err != nil {
		t.Fatalf("ObserveWorkflowTaskExecutions: %v", err)
	}
	if len(observation.Executions) != 0 || len(observation.Lifecycle) != 0 {
		t.Fatalf("observation without pinned root = %+v, want no exact execution facts", observation)
	}
}

func TestCurrentNodeControllerInterruptSessionExecutionStopsRetainedWorkflowRun(t *testing.T) {
	fixture := newRetainedSessionActivationFixture(t)
	if _, handled, err := fixture.controller.ActivateOrAttachRetainedSession(
		context.Background(),
		fixture.sessionID,
		"owner-session-interrupt",
	); err != nil || !handled {
		t.Fatalf("ActivateOrAttachRetainedSession = handled:%t error:%v", handled, err)
	}

	handled, err := fixture.controller.InterruptSessionExecution(context.Background(), fixture.sessionID)
	if err != nil {
		t.Fatalf("InterruptSessionExecution: %v", err)
	}
	if !handled {
		t.Fatal("workflow Session interrupt was classified as ordinary")
	}
	if hasLiveCurrentNode(fixture.controller.Snapshot(), fixture.reference) {
		t.Fatalf("interrupted retained Session Run remains live: %+v", fixture.controller.Snapshot())
	}
	interruption, interrupted := fixture.store.interruption(fixture.reference)
	if !interrupted || interruption.reason != workflow.CurrentNodeInterruptionReasonUserInterrupt {
		t.Fatalf("durable interruption = %+v, interrupted:%t, want user interruption", interruption, interrupted)
	}
}

func TestRuntimeControlSessionInterruptCancelsPausedWorkflowResultFinalizer(t *testing.T) {
	fixture := newRetainedSessionActivationFixture(t)
	finalizerEntered := make(chan struct{})
	finalizerCanceled := make(chan struct{})
	fixture.runner.run = func(context.Context, sessionruntime.ExecutionScope, sessionruntime.AgentRuntimeBridge) error {
		return nil
	}
	fixture.runner.finalize = func(ctx context.Context, _ sessionruntime.ExecutionScope, _ error) error {
		close(finalizerEntered)
		<-ctx.Done()
		close(finalizerCanceled)
		return context.Cause(ctx)
	}
	if _, handled, err := fixture.controller.ActivateOrAttachRetainedSession(
		context.Background(),
		fixture.sessionID,
		"owner-finalizer-interrupt",
	); err != nil || !handled {
		t.Fatalf("ActivateOrAttachRetainedSession = handled:%t error:%v", handled, err)
	}
	select {
	case <-finalizerEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("retained Workflow execution did not enter result finalization")
	}

	service := runtimecontrol.NewService(fixture.authority).
		WithWorkflowSessionInterruptor(fixture.controller)
	if _, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       fixture.sessionID.String(),
	}); err != nil {
		t.Fatalf("runtime Interrupt during finalization: %v", err)
	}
	select {
	case <-finalizerCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("Session interrupt did not cancel the paused result finalizer")
	}
	interruption, interrupted := fixture.store.interruption(fixture.reference)
	if !interrupted || interruption.reason != workflow.CurrentNodeInterruptionReasonUserInterrupt {
		t.Fatalf("durable interruption = %+v, interrupted:%t, want user interruption", interruption, interrupted)
	}
}

func TestCurrentNodeControllerSessionInterruptRevalidatesCompletedPublication(t *testing.T) {
	fixture := newRetainedSessionActivationFixture(t)
	fixture.runner.finalize = func(
		ctx context.Context,
		scope sessionruntime.ExecutionScope,
		runErr error,
	) error {
		return fixture.controller.FinalizeCurrentNodeResult(ctx, scope.ID(), runErr)
	}
	if _, handled, err := fixture.controller.ActivateOrAttachRetainedSession(
		context.Background(),
		fixture.sessionID,
		"owner-publication-race",
	); err != nil || !handled {
		t.Fatalf("ActivateOrAttachRetainedSession = handled:%t error:%v", handled, err)
	}
	if _, err := fixture.controller.CompleteSessionCurrentNode(
		context.Background(),
		fixture.sessionID,
		"done",
		nil,
		"",
	); err != nil {
		t.Fatalf("CompleteSessionCurrentNode: %v", err)
	}
	fixture.store.mu.Lock()
	fixture.store.publicationStarted = make(chan struct{})
	fixture.store.publicationRelease = make(chan struct{})
	publicationStarted := fixture.store.publicationStarted
	publicationRelease := fixture.store.publicationRelease
	fixture.store.mu.Unlock()
	var releasePublication sync.Once
	t.Cleanup(func() {
		releasePublication.Do(func() { close(publicationRelease) })
	})
	fixture.runner.releaseRun()
	select {
	case <-publicationStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("Agent finalizer did not enter lifecycle publication")
	}

	service := runtimecontrol.NewService(fixture.authority).
		WithWorkflowSessionInterruptor(fixture.controller)
	interruptDone := make(chan error, 1)
	go func() {
		_, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
			ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
			SessionID:       fixture.sessionID.String(),
		})
		interruptDone <- err
	}()
	select {
	case err := <-interruptDone:
		t.Fatalf("Session Interrupt returned before publication resolved: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	releasePublication.Do(func() { close(publicationRelease) })
	select {
	case err := <-interruptDone:
		if err != nil {
			t.Fatalf("Session Interrupt after completed publication: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Session Interrupt did not revalidate the completed publication")
	}
	if calls := fixture.store.completionCount(); calls != 1 {
		t.Fatalf("durable completion count = %d, want 1", calls)
	}
	if interruption, interrupted := fixture.store.interruption(fixture.reference); interrupted {
		t.Fatalf("completed Current Node was also interrupted: %+v", interruption)
	}
}

func TestCurrentNodeControllerTaskInterruptRacesSessionInterruptThroughOneFence(t *testing.T) {
	fixture := newRetainedSessionActivationFixture(t)
	fixture.store.interruptStarted = make(chan struct{})
	fixture.store.interruptRelease = make(chan struct{})
	finalizerEntered := make(chan struct{})
	finalizerRelease := make(chan struct{})
	var releaseFinalizer sync.Once
	var releasePublication sync.Once
	t.Cleanup(func() {
		releaseFinalizer.Do(func() { close(finalizerRelease) })
		releasePublication.Do(func() { close(fixture.store.interruptRelease) })
	})
	fixture.runner.finalize = func(ctx context.Context, _ sessionruntime.ExecutionScope, _ error) error {
		close(finalizerEntered)
		<-finalizerRelease
		return context.Cause(ctx)
	}
	if _, handled, err := fixture.controller.ActivateOrAttachRetainedSession(
		context.Background(),
		fixture.sessionID,
		"owner-interrupt-race",
	); err != nil || !handled {
		t.Fatalf("ActivateOrAttachRetainedSession = handled:%t error:%v", handled, err)
	}

	sessionDone := make(chan error, 1)
	go func() {
		_, err := fixture.controller.InterruptSessionExecution(context.Background(), fixture.sessionID)
		sessionDone <- err
	}()
	select {
	case <-fixture.store.interruptStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("Session Interrupt did not enter durable publication")
	}
	select {
	case <-finalizerEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("Session Interrupt did not retain the exact finalizer during publication")
	}

	taskDone := make(chan error, 1)
	go func() {
		taskDone <- fixture.controller.Interrupt(context.Background(), InterruptSelector{
			TaskID: fixture.reference.TaskID,
		})
	}()
	select {
	case err := <-taskDone:
		if !errors.Is(err, ErrTaskExecutionNotQuiescent) {
			t.Fatalf("racing Task Interrupt error = %v, want shared interruption fence", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("racing Task Interrupt started a second interruption publication")
	}

	releasePublication.Do(func() { close(fixture.store.interruptRelease) })
	releaseFinalizer.Do(func() { close(finalizerRelease) })
	select {
	case err := <-sessionDone:
		if err != nil {
			t.Fatalf("Session Interrupt: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Session Interrupt did not finish")
	}
	if writes := fixture.store.interruptionCount(fixture.reference); writes != 1 {
		t.Fatalf("durable interruption writes = %d, want 1", writes)
	}
}

func TestActivateOrAttachRetainedSessionSerializesWithSimultaneousResume(t *testing.T) {
	fixture := newRetainedSessionActivationFixture(t)
	fixture.store.resumeCommitStarted = make(chan struct{})
	fixture.store.resumeCommitRelease = make(chan struct{})

	activationDone := make(chan error, 1)
	go func() {
		_, _, err := fixture.controller.ActivateOrAttachRetainedSession(
			context.Background(),
			fixture.sessionID,
			"owner-race",
		)
		activationDone <- err
	}()
	select {
	case <-fixture.store.resumeCommitStarted:
	case <-time.After(time.Second):
		t.Fatal("interactive activation did not enter Resume publication")
	}
	resumeDone := make(chan error, 1)
	go func() {
		_, err := fixture.controller.ResumeTask(context.Background(), fixture.reference.TaskID)
		resumeDone <- err
	}()
	close(fixture.store.resumeCommitRelease)
	if err := <-activationDone; err != nil {
		t.Fatalf("interactive activation: %v", err)
	}
	var conflict *TaskResumeConflictError
	if err := <-resumeDone; !errors.As(err, &conflict) {
		t.Fatalf("simultaneous Task Resume error = %v, want already-adopted conflict", err)
	}
	if fixture.runner.starts() != 1 {
		t.Fatalf("Agent starts = %d, want 1", fixture.runner.starts())
	}
	snapshot := fixture.controller.Snapshot()
	if len(snapshot.LiveScopes) != 1 {
		t.Fatalf("controller snapshot after activation/Resume race = %+v, want one live scope", snapshot)
	}
}

func TestTaskDeletionGatePreventsRetainedSessionActivationAfterRevalidation(t *testing.T) {
	fixture := newRetainedSessionActivationFixture(t)
	deleteEntered := make(chan struct{})
	deleteRelease := make(chan struct{})
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- fixture.controller.RunTaskDeletion(
			context.Background(),
			[]workflow.TaskID{fixture.reference.TaskID},
			func(context.Context) error {
				close(deleteEntered)
				<-deleteRelease
				fixture.store.mu.Lock()
				delete(fixture.store.taskBySession, fixture.sessionID)
				delete(fixture.store.currentSessionContexts, fixture.sessionID)
				fixture.store.interrupted = nil
				fixture.store.mu.Unlock()
				return nil
			},
		)
	}()
	select {
	case <-deleteEntered:
	case <-time.After(time.Second):
		t.Fatal("Task deletion did not enter after quiescence revalidation")
	}

	activationDone := make(chan struct {
		handled bool
		err     error
	}, 1)
	go func() {
		_, handled, err := fixture.controller.ActivateOrAttachRetainedSession(
			context.Background(),
			fixture.sessionID,
			"owner-delete-race",
		)
		activationDone <- struct {
			handled bool
			err     error
		}{handled: handled, err: err}
	}()
	select {
	case result := <-activationDone:
		t.Fatalf("retained Session activation escaped the Task deletion gate: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	close(deleteRelease)
	if err := <-deleteDone; err != nil {
		t.Fatalf("RunTaskDeletion: %v", err)
	}
	select {
	case result := <-activationDone:
		if result.err != nil || result.handled {
			t.Fatalf("activation after deleted binding = handled:%t error:%v, want ordinary unbound Session", result.handled, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("retained Session activation did not revalidate after Task deletion")
	}
	if fixture.runner.starts() != 0 {
		t.Fatalf("Agent starts after Task deletion = %d, want 0", fixture.runner.starts())
	}
}

func TestProjectDeletionGatesEveryAffectedTaskAgainstRetainedSessionActivation(t *testing.T) {
	fixture := newRetainedSessionActivationFixture(t)
	siblingTaskID := workflow.TaskID("task-project-delete-sibling")
	deleteEntered := make(chan struct{})
	deleteRelease := make(chan struct{})
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- fixture.controller.RunTaskDeletion(
			context.Background(),
			[]workflow.TaskID{siblingTaskID, fixture.reference.TaskID},
			func(context.Context) error {
				close(deleteEntered)
				<-deleteRelease
				fixture.store.mu.Lock()
				delete(fixture.store.taskBySession, fixture.sessionID)
				delete(fixture.store.currentSessionContexts, fixture.sessionID)
				fixture.store.interrupted = nil
				fixture.store.mu.Unlock()
				return nil
			},
		)
	}()
	select {
	case <-deleteEntered:
	case <-time.After(time.Second):
		t.Fatal("Project deletion did not enter after all-Task quiescence revalidation")
	}

	activationDone := make(chan struct {
		handled bool
		err     error
	}, 1)
	go func() {
		_, handled, err := fixture.controller.ActivateOrAttachRetainedSession(
			context.Background(),
			fixture.sessionID,
			"owner-project-delete-race",
		)
		activationDone <- struct {
			handled bool
			err     error
		}{handled: handled, err: err}
	}()
	select {
	case result := <-activationDone:
		t.Fatalf("retained Session activation escaped the Project deletion Task gates: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	close(deleteRelease)
	if err := <-deleteDone; err != nil {
		t.Fatalf("Project RunTaskDeletion: %v", err)
	}
	select {
	case result := <-activationDone:
		if result.err != nil || result.handled {
			t.Fatalf("activation after Project deletion = handled:%t error:%v, want ordinary unbound Session", result.handled, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("retained Session activation did not revalidate after Project deletion")
	}
	if fixture.runner.starts() != 0 {
		t.Fatalf("Agent starts after Project deletion = %d, want 0", fixture.runner.starts())
	}
}

func TestActivateOrAttachRetainedSessionReturnsOrdinaryOnlyWithoutDirectCurrentNodeBinding(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	taskID := workflow.TaskID("task-retained-no-current-binding")
	store := &currentNodeControllerStore{
		taskBySession: map[runtimeids.SessionID]*workflow.TaskID{sessionID: &taskID},
		currentSessionErrors: map[runtimeids.SessionID]error{
			sessionID: workflowstore.ErrSessionNotCurrentWorkflowNode,
		},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	_, handled, err := controller.ActivateOrAttachRetainedSession(context.Background(), sessionID, "owner")
	if err != nil {
		t.Fatalf("ActivateOrAttachRetainedSession: %v", err)
	}
	if handled {
		t.Fatal("Session without a direct Current-Node binding was classified as Workflow activation")
	}
}

func TestActivateOrAttachRetainedSessionSurfacesContradictoryDurableBinding(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	taskID := workflow.TaskID("task-retained-contradictory")
	contradiction := errors.New("session is bound to multiple current nodes")
	store := &currentNodeControllerStore{
		taskBySession: map[runtimeids.SessionID]*workflow.TaskID{sessionID: &taskID},
		currentSessionErrors: map[runtimeids.SessionID]error{
			sessionID: contradiction,
		},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	_, handled, err := controller.ActivateOrAttachRetainedSession(context.Background(), sessionID, "owner")
	if !handled || !errors.Is(err, contradiction) {
		t.Fatalf("contradictory activation = handled:%t error:%v, want explicit consistency failure", handled, err)
	}
}

type retainedSessionActivationFixture struct {
	controller *CurrentNodeController
	store      *currentNodeControllerStore
	runner     *retainedSessionAgentRunner
	authority  *sessionruntime.Authority
	sessionID  runtimeids.SessionID
	reference  workflow.CurrentNodeReference
}

func newRetainedSessionActivationFixture(t *testing.T) retainedSessionActivationFixture {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore := testsetup.OpenStore(t, cfg.PersistenceRoot)
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	sessionStore, err := session.Create(
		filepath.Join(cfg.PersistenceRoot, "projects", binding.ProjectID, "sessions"),
		"sessions",
		cfg.WorkspaceRoot,
		sessioncontract.SessionCategorySubagent,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(sessionStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-retained-activation", "node-agent")
	taskID := reference.TaskID
	currentNode := workflow.CurrentNode{
		Reference:  reference,
		SessionID:  &sessionID,
		Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
	}
	store := &currentNodeControllerStore{
		interrupted:   []workflow.CurrentNode{currentNode},
		taskBySession: map[runtimeids.SessionID]*workflow.TaskID{sessionID: &taskID},
		currentSessionContexts: map[runtimeids.SessionID]workflowstore.CurrentNodeStartContext{
			sessionID: {
				CurrentNode: currentNode,
				Node: workflowstore.NodeRecord{
					ID:   reference.NodeID,
					Kind: workflow.NodeKindAgent,
				},
			},
		},
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			if controller != nil {
				controller.ExecutionFinalized(scope)
			}
		}),
	})
	settings := cfg.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200_000
	settings.Reviewer.Frequency = "off"
	realWorkspace, err := filepath.EvalSymlinks(cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	workspaceInfo, err := os.Stat(realWorkspace)
	if err != nil {
		t.Fatalf("stat workspace: %v", err)
	}
	workspaceRoot := tools.FilesystemRoot{
		LexicalPath: cfg.WorkspaceRoot,
		RealPath:    realWorkspace,
		Info:        workspaceInfo,
	}
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: settings,
		FilesystemContext: tools.FilesystemContext{Access: tools.FileAccessScope{
			WorkingDirectory:    workspaceRoot,
			ExecutionTargetRoot: workspaceRoot,
			ProjectWorkspace: tools.ProjectWorkspaceScope{
				ProjectID: binding.ProjectID,
				Roots: []tools.ProjectWorkspaceRoot{{
					FilesystemRoot: workspaceRoot,
				}},
			},
		}},
		Client: currentNodeQuestionLLMClient{},
	})
	if err != nil {
		t.Fatalf("NewAgentRuntimePlan: %v", err)
	}
	runner := &retainedSessionAgentRunner{
		authority:  authority,
		descriptor: mustRetainedSessionDescriptor(t, sessionID),
		plan:       plan,
		release:    make(chan struct{}),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		runner.releaseRun()
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	return retainedSessionActivationFixture{
		controller: controller,
		store:      store,
		runner:     runner,
		authority:  authority,
		sessionID:  sessionID,
		reference:  reference,
	}
}

type retainedSessionAgentRunner struct {
	authority   *sessionruntime.Authority
	descriptor  session.SessionDescriptor
	plan        sessionruntime.AgentRuntimePlan
	release     chan struct{}
	run         sessionruntime.AgentRunner
	finalize    func(context.Context, sessionruntime.ExecutionScope, error) error
	releaseOnce sync.Once
	mu          sync.Mutex
	count       int
}

func (r *retainedSessionAgentRunner) StartCurrentNode(
	ctx context.Context,
	_ workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ CurrentNodeAssignmentEnsure,
	lease sessionruntime.WorkflowExecutionLease,
	controller workflowruntime.Controller,
) error {
	r.mu.Lock()
	r.count++
	r.mu.Unlock()
	run := r.run
	if run == nil {
		run = func(ctx context.Context, _ sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			select {
			case <-r.release:
				return nil
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}
	}
	_, err := r.authority.StartAgentExecution(ctx, sessionruntime.AgentExecutionRequest{
		Descriptor:         r.descriptor,
		Runtime:            &r.plan,
		Workflow:           &lease,
		Resource:           sessionruntime.OpenAgentResource{},
		RunningPublication: currentNodeRunningPublicationForControllerTest(controller),
		Runner:             run,
		Finalize:           r.finalize,
	})
	return err
}

func (r *retainedSessionAgentRunner) starts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

func (r *retainedSessionAgentRunner) releaseRun() {
	r.releaseOnce.Do(func() { close(r.release) })
}

func mustRetainedSessionDescriptor(t *testing.T, sessionID runtimeids.SessionID) session.SessionDescriptor {
	t.Helper()
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("NewOpenSessionDescriptor: %v", err)
	}
	return descriptor
}
