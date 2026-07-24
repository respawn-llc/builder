package workflowexecution

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/session"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	"github.com/google/uuid"
)

func TestCurrentNodeControllerRegistersGateBeforeRunnerAndReleasesLeaseAfterLiveSwap(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-gate", "node-agent")
	outputPath := t.TempDir() + "/started"
	store := &currentNodeControllerStore{}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &controlledScriptRunner{
		authority:   authority,
		command:     sessionruntime.ScriptCommand{Path: shellPath, Args: []string{"-c", `printf started > "$1"`, "sh", outputPath}},
		entered:     make(chan struct{}),
		startRunner: make(chan struct{}),
		registered:  make(chan struct{}),
		returnStart: make(chan struct{}),
		handles:     make(chan sessionruntime.ExecutionHandle, 1),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	started := make(chan error, 1)
	go func() {
		started <- controller.Start(context.Background(), reference)
	}()
	<-runner.entered

	snapshot := controller.Snapshot()
	if len(snapshot.Gates) != 1 || !snapshot.Gates[0].CurrentNode.Equal(reference) {
		t.Fatalf("gate snapshot = %+v, want admitted gate for %v", snapshot.Gates, reference)
	}
	if store.admitCount() != 1 {
		t.Fatalf("admitted current nodes = %d, want 1", store.admitCount())
	}
	close(runner.startRunner)
	<-runner.registered
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("script started before gate-to-live swap: stat error = %v", err)
	}
	if _, live := authority.ExecutionByScope(snapshot.Gates[0].ScopeID); !live {
		t.Fatal("registered script scope is not live while runner is still preparing")
	}

	close(runner.returnStart)
	if err := <-started; err != nil {
		t.Fatalf("start current node: %v", err)
	}
	handle := <-runner.handles
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait script: %v", err)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("script did not start after controller released lease: %v", err)
	}
	snapshot = controller.Snapshot()
	if len(snapshot.Gates) != 0 || len(snapshot.LiveScopes) != 0 {
		t.Fatalf("post-retirement snapshot = %+v, want no gate or live scope", snapshot)
	}
}

func TestCurrentNodeControllerRunnerFailureInterruptsAdmittedCurrentNode(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-failure", "node-agent")
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, failingCurrentNodeRunner{cause: errors.New("provider unavailable")}, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	err := controller.Start(context.Background(), reference)
	if err == nil || err.Error() != "provider unavailable" {
		t.Fatalf("start error = %v, want runner failure", err)
	}
	interruption, ok := store.interruption(reference)
	if !ok {
		t.Fatal("runner failure did not interrupt the admitted current node")
	}
	if interruption.reason != "workflow_runtime_start_failed" {
		t.Fatalf("interruption reason = %q, want workflow_runtime_start_failed", interruption.reason)
	}
}

func TestCurrentNodeControllerHoldsSuccessorUntilSourceScopeRetires(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-successor", "node-source")
	successor := currentNodeReferenceForControllerTest(t, "task-successor", "node-successor")
	store := &currentNodeControllerStore{
		completion: workflowstore.CurrentNodeCompletionResult{
			AutomaticIntents: []workflow.CurrentNodeReference{successor},
		},
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 4),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := controller.Start(context.Background(), source); err != nil {
		t.Fatalf("start source: %v", err)
	}
	if got := <-runner.started; !got.Equal(source) {
		t.Fatalf("first started current node = %v, want source %v", got, source)
	}
	sourceScope := singleLiveScope(t, controller, source)
	if _, err := controller.CompleteCurrentNode(context.Background(), workflowruntime.CompletionRequest{
		ScopeID:      sourceScope,
		TransitionID: "next",
	}); err != nil {
		t.Fatalf("complete source: %v", err)
	}
	snapshot := controller.Snapshot()
	if len(snapshot.HeldIntents) != 1 || !snapshot.HeldIntents[0].CurrentNode.Equal(successor) {
		t.Fatalf("held intents = %+v, want successor held by source retirement", snapshot.HeldIntents)
	}
	select {
	case started := <-runner.started:
		t.Fatalf("successor %v started before source retirement", started)
	case <-time.After(50 * time.Millisecond):
	}

	sourceHandle, ok := authority.ExecutionByScope(sourceScope)
	if !ok {
		t.Fatal("source scope disappeared before it was stopped")
	}
	if err := sourceHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop source: %v", err)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(successor) {
			t.Fatalf("started successor = %v, want %v", started, successor)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("successor did not start after source retirement")
	}
}

func TestCurrentNodeControllerRecoveryOnlyMarksAdmittedCurrentNodesInterrupted(t *testing.T) {
	store := &currentNodeControllerStore{recovered: 3}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &countingCurrentNodeRunner{}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	recovered, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered != 3 {
		t.Fatalf("recovered markers = %d, want 3", recovered)
	}
	if runner.starts() != 0 {
		t.Fatalf("recovery started %d current nodes, want no automatic start", runner.starts())
	}
}

func TestCurrentNodeControllerAnswersOnlyDurablyBoundExactPromptScope(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	reference := currentNodeReferenceForControllerTest(t, "task-question", "node-question")
	request := askquestion.AskQuestionRequest{
		ID:       uuid.NewString(),
		StepID:   uuid.NewString(),
		Question: "Proceed?",
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ID)

	if err := fixture.controller.AnswerWorkflowQuestion(context.Background(), reference.TaskID, "different-ask-id", askquestion.AskQuestionResponse{RequestID: "different-ask-id", Answer: "yes"}, nil); !errors.Is(err, serverapi.ErrPromptNotFound) {
		t.Fatalf("unknown prompt answer error = %v, want prompt not found", err)
	}
	if err := fixture.controller.AnswerWorkflowQuestion(context.Background(), reference.TaskID, request.ID, askquestion.AskQuestionResponse{RequestID: request.ID, Answer: "yes"}, nil); err != nil {
		t.Fatalf("AnswerWorkflowQuestion: %v", err)
	}
	select {
	case result := <-pending.result:
		if result.err != nil || result.response.RequestID != request.ID || result.response.Answer != "yes" {
			t.Fatalf("prompt result = %+v, want exact successful answer", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for prompt response")
	}
	if _, err := pending.handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait prompt execution: %v", err)
	}
	if calls := fixture.store.bindingCalls(); len(calls) != 1 || calls[0].sessionID != pending.sessionID || !calls[0].reference.Equal(reference) {
		t.Fatalf("binding validation calls = %+v, want exact session/current-node binding", calls)
	}
}

func TestCurrentNodeControllerRejectsOwnershipMismatchWithoutPromptDelivery(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	reference := currentNodeReferenceForControllerTest(t, "task-question-mismatch", "node-question")
	request := askquestion.AskQuestionRequest{
		ID:       uuid.NewString(),
		StepID:   uuid.NewString(),
		Question: "Proceed?",
	}
	pending := fixture.startPendingPrompt(t, reference, request)
	fixture.waitForPendingPrompt(t, reference.TaskID, request.ID)
	fixture.store.setBindingError(workflowstore.ErrSessionNotCurrentWorkflowNode)

	err := fixture.controller.AnswerWorkflowQuestion(context.Background(), reference.TaskID, request.ID, askquestion.AskQuestionResponse{RequestID: request.ID, Answer: "yes"}, nil)
	if !errors.Is(err, serverapi.ErrPromptNotFound) {
		t.Fatalf("ownership mismatch answer error = %v, want prompt not found", err)
	}
	select {
	case result := <-pending.result:
		t.Fatalf("ownership mismatch delivered response %+v", result)
	default:
	}
	if err := pending.handle.Stop(context.Background()); err != nil {
		t.Fatalf("stop undelivered prompt execution: %v", err)
	}
	select {
	case result := <-pending.result:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("undelivered prompt result error = %v, want cancellation", result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for canceled undelivered prompt")
	}
}

func TestCurrentNodeControllerRejectsAmbiguousPromptScope(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	taskID := workflow.TaskID("task-question-ambiguous")
	request := askquestion.AskQuestionRequest{
		ID:       uuid.NewString(),
		StepID:   uuid.NewString(),
		Question: "Proceed?",
	}
	first := fixture.startPendingPrompt(t, currentNodeReferenceForControllerTest(t, string(taskID), "node-question-a"), request)
	fixture.waitForPendingPrompt(t, taskID, request.ID)
	second := fixture.startPendingPrompt(t, currentNodeReferenceForControllerTest(t, string(taskID), "node-question-b"), request)
	fixture.waitForAmbiguousPendingPrompt(t, taskID, request.ID)

	err := fixture.controller.AnswerWorkflowQuestion(context.Background(), taskID, request.ID, askquestion.AskQuestionResponse{RequestID: request.ID, Answer: "yes"}, nil)
	if !errors.Is(err, sessionruntime.ErrWorkflowPromptAmbiguous) {
		t.Fatalf("ambiguous prompt answer error = %v, want prompt ambiguity", err)
	}
	if calls := fixture.store.bindingCalls(); len(calls) != 0 {
		t.Fatalf("ambiguous prompt checked durable bindings = %+v, want none", calls)
	}
	for _, pending := range []currentNodePendingPrompt{first, second} {
		select {
		case result := <-pending.result:
			t.Fatalf("ambiguous prompt delivered response %+v", result)
		default:
		}
		if err := pending.handle.Stop(context.Background()); err != nil {
			t.Fatalf("stop ambiguous prompt execution: %v", err)
		}
		select {
		case result := <-pending.result:
			if !errors.Is(result.err, context.Canceled) {
				t.Fatalf("ambiguous prompt result error = %v, want cancellation", result.err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for canceled ambiguous prompt")
		}
	}
}

func TestCurrentNodeControllerProtocolViolationCapStopsAndInterruptsLiveScope(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-protocol", "node-agent")
	store := &currentNodeControllerStore{}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 1),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := controller.Start(context.Background(), reference); err != nil {
		t.Fatalf("start current node: %v", err)
	}
	<-runner.started
	scopeID := singleLiveScope(t, controller, reference)
	result, err := controller.RecordProtocolViolation(context.Background(), workflowruntime.ViolationRequest{
		ScopeID:  scopeID,
		Kind:     workflowruntime.ViolationKindInvalidCompletion,
		MaxCount: 1,
		Detail:   "invalid completion",
	})
	if err != nil {
		t.Fatalf("record protocol violation: %v", err)
	}
	if !result.Interrupted || result.Count != 1 {
		t.Fatalf("violation result = %+v, want count 1 and interrupted", result)
	}
	interruption, ok := store.interruption(reference)
	if !ok || interruption.reason != reasonProtocolViolationCap {
		t.Fatalf("protocol interruption = %+v, want reason %q", interruption, reasonProtocolViolationCap)
	}
}

func newCurrentNodeControllerForTest(
	t *testing.T,
	store *currentNodeControllerStore,
	runner CurrentNodeRunner,
	authority *sessionruntime.Authority,
	concurrency int,
) *CurrentNodeController {
	t.Helper()
	controller, err := NewCurrentNodeController(store, runner, authority, NewMutationPermit(), CurrentNodeControllerConfig{
		AutomaticConcurrency: concurrency,
	})
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	return controller
}

func currentNodeReferenceForControllerTest(t *testing.T, taskID string, nodeID string) workflow.CurrentNodeReference {
	t.Helper()
	reference, err := workflow.NewCurrentNodeReference(workflow.TaskID(taskID), workflow.NodeID(nodeID), nil)
	if err != nil {
		t.Fatalf("new current node reference: %v", err)
	}
	return reference
}

func singleLiveScope(t *testing.T, controller *CurrentNodeController, reference workflow.CurrentNodeReference) runtimeids.ExecutionScopeID {
	t.Helper()
	snapshot := controller.Snapshot()
	for _, scope := range snapshot.LiveScopes {
		if scope.CurrentNode.Equal(reference) {
			return scope.ScopeID
		}
	}
	t.Fatalf("snapshot %+v has no live scope for %v", snapshot, reference)
	return runtimeids.ExecutionScopeID{}
}

type currentNodeControllerStore struct {
	mu            sync.Mutex
	admitted      []workflow.CurrentNodeReference
	interruptions map[workflow.CurrentNodeReferenceKey]currentNodeInterruptionRecord
	recovered     int64
	completion    workflowstore.CurrentNodeCompletionResult
	bindingErr    error
	bindings      []currentNodeSessionBindingCall
}

type currentNodeInterruptionRecord struct {
	reason workflow.CurrentNodeInterruptionReason
	detail workflow.CurrentNodeInterruptionDetail
}

type currentNodeSessionBindingCall struct {
	sessionID runtimeids.SessionID
	reference workflow.CurrentNodeReference
}

func (s *currentNodeControllerStore) AdmitCurrentNode(_ context.Context, reference workflow.CurrentNodeReference) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admitted = append(s.admitted, reference)
	return nil
}

func (s *currentNodeControllerStore) ResumeCurrentNode(context.Context, workflow.CurrentNodeReference) error {
	return nil
}

func (s *currentNodeControllerStore) InterruptAdmittedCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, reason workflow.CurrentNodeInterruptionReason, detail workflow.CurrentNodeInterruptionDetail) error {
	key, err := reference.Key()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interruptions == nil {
		s.interruptions = make(map[workflow.CurrentNodeReferenceKey]currentNodeInterruptionRecord)
	}
	s.interruptions[key] = currentNodeInterruptionRecord{reason: reason, detail: detail}
	return nil
}

func (s *currentNodeControllerStore) InterruptCurrentNode(ctx context.Context, reference workflow.CurrentNodeReference, reason workflow.CurrentNodeInterruptionReason, detail workflow.CurrentNodeInterruptionDetail) error {
	return s.InterruptAdmittedCurrentNode(ctx, reference, reason, detail)
}

func (s *currentNodeControllerStore) RecoverAdmittedCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) (int64, error) {
	return s.recovered, nil
}

func (*currentNodeControllerStore) ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error) {
	return workflow.CurrentNode{}, sql.ErrNoRows
}

func (s *currentNodeControllerStore) CompleteCurrentNode(context.Context, workflowstore.CurrentNodeCompletionRequest) (workflowstore.CurrentNodeCompletionResult, error) {
	return s.completion, nil
}

func (s *currentNodeControllerStore) ValidateCurrentNodeSessionBinding(_ context.Context, sessionID runtimeids.SessionID, reference workflow.CurrentNodeReference) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings = append(s.bindings, currentNodeSessionBindingCall{sessionID: sessionID, reference: reference})
	return s.bindingErr
}

func (s *currentNodeControllerStore) admitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.admitted)
}

func (s *currentNodeControllerStore) interruption(reference workflow.CurrentNodeReference) (currentNodeInterruptionRecord, bool) {
	key, err := reference.Key()
	if err != nil {
		return currentNodeInterruptionRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.interruptions[key]
	return value, ok
}

func (s *currentNodeControllerStore) setBindingError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindingErr = err
}

func (s *currentNodeControllerStore) bindingCalls() []currentNodeSessionBindingCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]currentNodeSessionBindingCall(nil), s.bindings...)
}

type currentNodeQuestionFixture struct {
	cfg        config.App
	metadata   interface{ AuthoritativeSessionStoreOptions() []session.StoreOption }
	authority  *sessionruntime.Authority
	controller *CurrentNodeController
	store      *currentNodeControllerStore
	sessionDir string
}

type currentNodePendingPrompt struct {
	handle    sessionruntime.ExecutionHandle
	sessionID runtimeids.SessionID
	result    <-chan currentNodePromptResult
}

type currentNodePromptResult struct {
	response askquestion.AskQuestionResponse
	err      error
}

type currentNodeQuestionLLMClient struct{}

func (currentNodeQuestionLLMClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("question fixture model must not generate")
}

func newCurrentNodeQuestionFixture(t *testing.T) currentNodeQuestionFixture {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	appCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore := testsetup.OpenStore(t, appCfg.PersistenceRoot)
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), appCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: appCfg.PersistenceRoot,
		StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
	})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	return currentNodeQuestionFixture{
		cfg:        appCfg,
		metadata:   metadataStore,
		authority:  authority,
		controller: controller,
		store:      store,
		sessionDir: filepath.Join(appCfg.PersistenceRoot, "projects", binding.ProjectID, "sessions"),
	}
}

func (f currentNodeQuestionFixture) startPendingPrompt(t *testing.T, reference workflow.CurrentNodeReference, request askquestion.AskQuestionRequest) currentNodePendingPrompt {
	t.Helper()
	store, err := session.Create(
		f.sessionDir,
		filepath.Base(f.sessionDir),
		f.cfg.WorkspaceRoot,
		sessioncontract.SessionCategorySubagent,
		f.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("NewOpenSessionDescriptor: %v", err)
	}
	settings := f.cfg.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200_000
	settings.Reviewer.Frequency = "off"
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: settings,
		Workdir:  f.cfg.WorkspaceRoot,
		Client:   currentNodeQuestionLLMClient{},
	})
	if err != nil {
		t.Fatalf("NewAgentRuntimePlan: %v", err)
	}
	lease, err := f.authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{CurrentNode: reference})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	lease.Release()
	result := make(chan currentNodePromptResult, 1)
	handle, err := f.authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor,
		Runtime:    &plan,
		Workflow:   &lease,
		Resource:   sessionruntime.OpenAgentResource{},
		Runner: func(ctx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			response, askErr := f.authority.AwaitPromptResponse(ctx, scope.ID(), request)
			result <- currentNodePromptResult{response: response, err: askErr}
			return askErr
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("current node key: %v", err)
	}
	f.controller.mu.Lock()
	f.controller.live[lease.ScopeID()] = currentNodeLiveScope{reference: reference, lease: lease}
	f.controller.liveByNode[key] = lease.ScopeID()
	f.controller.mu.Unlock()
	return currentNodePendingPrompt{handle: handle, sessionID: sessionID, result: result}
}

func (f currentNodeQuestionFixture) waitForPendingPrompt(t *testing.T, taskID workflow.TaskID, askID string) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, err := f.authority.ResolvePendingWorkflowPrompt(taskID, askID)
		return err == nil || errors.Is(err, sessionruntime.ErrWorkflowPromptAmbiguous)
	}, "timed out waiting for workflow prompt %q on task %q", askID, taskID)
}

func (f currentNodeQuestionFixture) waitForAmbiguousPendingPrompt(t *testing.T, taskID workflow.TaskID, askID string) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, err := f.authority.ResolvePendingWorkflowPrompt(taskID, askID)
		return errors.Is(err, sessionruntime.ErrWorkflowPromptAmbiguous)
	}, "timed out waiting for ambiguous workflow prompt %q on task %q", askID, taskID)
}

type controlledScriptRunner struct {
	authority   *sessionruntime.Authority
	command     sessionruntime.ScriptCommand
	entered     chan struct{}
	startRunner chan struct{}
	registered  chan struct{}
	returnStart chan struct{}
	handles     chan sessionruntime.ExecutionHandle
}

func (r *controlledScriptRunner) StartCurrentNode(_ context.Context, _ workflow.CurrentNodeReference, lease sessionruntime.WorkflowExecutionLease, _ workflowruntime.Controller) error {
	close(r.entered)
	<-r.startRunner
	handle, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command:  r.command,
	})
	if err != nil {
		return err
	}
	r.handles <- handle
	close(r.registered)
	<-r.returnStart
	return nil
}

type failingCurrentNodeRunner struct {
	cause error
}

func (r failingCurrentNodeRunner) StartCurrentNode(context.Context, workflow.CurrentNodeReference, sessionruntime.WorkflowExecutionLease, workflowruntime.Controller) error {
	return r.cause
}

type countingCurrentNodeRunner struct {
	mu    sync.Mutex
	count int
}

func (r *countingCurrentNodeRunner) StartCurrentNode(context.Context, workflow.CurrentNodeReference, sessionruntime.WorkflowExecutionLease, workflowruntime.Controller) error {
	r.mu.Lock()
	r.count++
	r.mu.Unlock()
	return nil
}

func (r *countingCurrentNodeRunner) starts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

type recordingScriptRunner struct {
	authority *sessionruntime.Authority
	command   sessionruntime.ScriptCommand
	started   chan workflow.CurrentNodeReference
}

func (r *recordingScriptRunner) StartCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, lease sessionruntime.WorkflowExecutionLease, _ workflowruntime.Controller) error {
	_, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command:  r.command,
	})
	if err == nil {
		r.started <- reference
	}
	return err
}
