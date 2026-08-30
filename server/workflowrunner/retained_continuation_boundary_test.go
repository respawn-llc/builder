package workflowrunner

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"core/server/launch"
	"core/server/llm"
	"core/server/runprompt"
	agentruntime "core/server/runtime"
	"core/server/runtimecontrol"
	"core/server/session"
	"core/server/sessionlaunch"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

type retainedAssignmentOrderClient struct {
	base              *compactingScriptedClient
	fixture           *currentNodeRunnerFixture
	sessionID         runtimeids.SessionID
	expectedReference workflow.CurrentNodeReference
	observed          atomic.Bool
}

func acquireWorkflowMetadataWriteLock(t *testing.T, persistenceRoot string) func() {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(persistenceRoot, "db", "main.sqlite3"))
	if err != nil {
		t.Fatalf("sql.Open metadata: %v", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatalf("metadata DB connection: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Close()
		_ = db.Close()
		t.Fatalf("acquire metadata write lock: %v", err)
	}
	var once atomic.Bool
	return func() {
		if once.CompareAndSwap(false, true) {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			_ = conn.Close()
			_ = db.Close()
		}
	}
}

func (c *retainedAssignmentOrderClient) Generate(
	ctx context.Context,
	request llm.Request,
	callbacks llm.StreamCallbacks,
) (llm.Response, error) {
	matched, err := c.fixture.workflowAssignmentMatchesForTest(c.sessionID, c.expectedReference)
	if err != nil {
		return llm.Response{}, err
	}
	if !matched {
		return llm.Response{}, errors.New("provider called before durable Workflow assignment")
	}
	c.observed.Store(true)
	return c.base.Generate(ctx, request, callbacks)
}

func (c *retainedAssignmentOrderClient) Compact(ctx context.Context, request llm.CompactionRequest) (llm.CompactionResponse, error) {
	return c.base.Compact(ctx, request)
}

func (c *retainedAssignmentOrderClient) ProviderCapabilities(ctx context.Context) (llm.ProviderCapabilities, error) {
	return c.base.ProviderCapabilities(ctx)
}

func (c *retainedAssignmentOrderClient) Requests() []llm.Request {
	return c.base.Requests()
}

func (f *currentNodeRunnerFixture) workflowAssignmentMatchesForTest(
	sessionID runtimeids.SessionID,
	expected workflow.CurrentNodeReference,
) (bool, error) {
	record, err := f.metadata.ResolvePersistedSession(context.Background(), sessionID.String())
	if err != nil || record.Meta == nil {
		return false, err
	}
	store, err := session.Open(record.SessionDir, f.metadata.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		return false, err
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		return false, err
	}
	window, err := eventLog.ReadRecentRecords(128)
	if err != nil {
		return false, err
	}
	for {
		for _, event := range window.Records {
			payload, payloadErr := event.Payload()
			if payloadErr != nil {
				return false, payloadErr
			}
			message, ok := payload.(session.MessageRecord)
			if !ok ||
				message.MessageType == nil ||
				*message.MessageType != session.MessageTypeWorkflowMode ||
				message.SourcePath == nil {
				continue
			}
			identity := workflowruntime.CurrentNodePromptIdentity(expected)
			if *message.SourcePath == identity {
				return true, nil
			}
		}
		if window.ReachedStart {
			return false, nil
		}
		seen := 0
		window, err = eventLog.ReadSegmentBackward(window.StartOffset, func(session.EventRecord) bool {
			seen++
			return seen == 128
		})
		if err != nil {
			return false, err
		}
	}
}

func prepareCompactedRetainedWorkflowSession(
	t *testing.T,
	f *currentNodeRunnerFixture,
	client currentNodeRunnerClient,
) (runtimeids.SessionID, workflow.CurrentNodeReference, sessionruntime.RuntimeAttachment) {
	t.Helper()
	workflowID := createCurrentNodeAgentWorkflow(t, f.store)
	task := f.createTask(t, workflowID)
	if err := f.store.LockTaskExecutionTarget(context.Background(), task.ID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   f.workspaceID,
			SourceWorkspaceRoot: f.workspace,
		},
	}); err != nil {
		t.Fatalf("lock Task execution target: %v", err)
	}
	started, err := f.store.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("start Task: %v", err)
	}
	reference := started.Mutation.Created[0].Reference
	sessionStore, err := session.Create(
		filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"),
		"sessions",
		f.workspace,
		sessioncontract.SessionCategoryMain,
		f.starter.storeOptions...,
	)
	if err != nil {
		t.Fatalf("create retained Session: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(sessionStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse retained Session ID: %v", err)
	}
	if _, err := f.store.BindSessionToCurrentNode(context.Background(), workflowstore.CurrentNodeSessionBindingRequest{
		Association: workflowstore.TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  reference,
			AssociatedAt: time.Now().UTC(),
		},
	}); err != nil {
		t.Fatalf("bind retained Session: %v", err)
	}
	if err := f.store.InterruptCurrentNode(
		context.Background(),
		reference,
		workflow.CurrentNodeInterruptionReason("test_retained_continuation"),
		workflow.CurrentNodeInterruptionDetail{Code: "test_retained_continuation"},
	); err != nil {
		t.Fatalf("interrupt retained Current Node: %v", err)
	}
	input, err := f.store.ResolveCurrentNodeStartContext(context.Background(), reference)
	if err != nil || input.ExecutionRoot == nil {
		t.Fatalf("resolve retained Current Node start context: %v", err)
	}
	planner := launch.Planner{
		Config:                   f.cfg,
		ContainerDir:             filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"),
		StoreOptions:             f.starter.storeOptions,
		PersistedSessions:        f.metadata,
		ExecutionTargets:         f.metadata,
		ProjectWorkspaceBoundary: f.metadata,
	}
	plan, err := planner.PlanSession(context.Background(), launch.SessionRequest{
		Mode:   launch.ModeHeadless,
		Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID),
	})
	if err != nil {
		t.Fatalf("plan retained Session: %v", err)
	}
	prepared := preparedCurrentNodeAgentSession{
		root:   *input.ExecutionRoot,
		plan:   plan,
		client: client,
		mode:   workflowruntime.CompletionModeTool,
	}
	runtimePlan, err := f.starter.buildCurrentNodeAgentRuntimePlan(input, prepared, nil)
	if err != nil {
		t.Fatalf("build retained runtime plan: %v", err)
	}
	attachment, err := f.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "retained-continuation-boundary-test",
		Runtime:   &runtimePlan,
	})
	if err != nil {
		t.Fatalf("open retained runtime: %v", err)
	}
	var binding *agentruntime.CurrentNodeExecutionBinding
	if err := f.authority.WithCurrentRuntime(context.Background(), sessionID, func(_ context.Context, engine *agentruntime.Engine) error {
		var bindErr error
		binding, bindErr = engine.BindCurrentNodeExecution(&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID:        runtimeids.NewExecutionScopeID(),
			CompletionMode: workflowruntime.CompletionModeUnstructuredOutput,
			Instructions: workflowruntime.TaskInstructions{
				WorkflowID:  input.Workflow.ID,
				CurrentNode: reference,
			},
		})
		return bindErr
	}); err != nil {
		t.Fatalf("bind retained Current Node execution: %v", err)
	}
	t.Cleanup(func() {
		if binding != nil {
			_ = binding.Close()
		}
		if _, releaseErr := attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose); releaseErr != nil &&
			!errors.Is(releaseErr, serverapi.ErrRuntimeUnavailable) {
			t.Errorf("release retained runtime: %v", releaseErr)
		}
	})
	if err := f.authority.WithCurrentRuntime(context.Background(), sessionID, func(ctx context.Context, engine *agentruntime.Engine) error {
		return engine.CompactContext(ctx, "")
	}); err != nil {
		t.Fatalf("compact retained Session before first activation: %v", err)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("close dormant Workflow execution binding: %v", err)
	}
	return sessionID, reference, attachment
}

func TestRetainedCompactedNeverActivatedWorkflowAssignmentPrecedesTUISteerProvider(t *testing.T) {
	base := NewCompactingScriptedClient(
		llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
		[]llm.CompactionResponse{workflowPostCompletionCompactionResponse("pre-activation")},
		ScriptedToolBatch("complete", llm.ToolCall{
			ID:    "complete",
			Name:  "complete_node",
			Input: []byte(`{"transition":"done"}`),
		}),
	)
	f := newCurrentNodeRunnerFixtureWithClient(t, base)
	order := &retainedAssignmentOrderClient{base: base, fixture: f}
	f.client = order
	sessionID, reference, _ := prepareCompactedRetainedWorkflowSession(t, f, order)
	order.sessionID = sessionID
	order.expectedReference = reference

	service := runtimecontrol.NewService(f.authority).
		WithWorkflowSessionReactivator(f.controller).
		WithPromptHistoryStore(f.metadata).
		WithPersistedSessionResolver(f.metadata)
	response, err := service.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{
		SessionID: sessionID.String(),
		Input:     runtimeinput.Text("continue"),
	})
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if !order.observed.Load() {
		t.Fatalf("provider was not called after exact durable assignment: err=%v requests=%d", err, len(order.Requests()))
	}
	if response.ResultKind != clientui.UserTurnResultKindNoFinal {
		t.Fatalf("SubmitUserTurn response = %+v, want no-final result", response)
	}
}

func TestRetainedCompactedNeverActivatedWorkflowAssignmentPrecedesHeadlessContinueProvider(t *testing.T) {
	base := NewCompactingScriptedClient(
		llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true},
		[]llm.CompactionResponse{workflowPostCompletionCompactionResponse("pre-activation")},
		ScriptedToolBatch("complete", llm.ToolCall{
			ID:    "complete",
			Name:  "complete_node",
			Input: []byte(`{"transition":"done"}`),
		}),
	)
	f := newCurrentNodeRunnerFixtureWithClient(t, base)
	order := &retainedAssignmentOrderClient{base: base, fixture: f}
	f.client = order
	sessionID, reference, _ := prepareCompactedRetainedWorkflowSession(t, f, order)
	order.sessionID = sessionID
	order.expectedReference = reference

	client := runprompt.NewInProcessRunPromptClient(runprompt.HeadlessBootstrap{
		SessionLaunch: sessionlaunch.NewService(launch.Planner{
			Config:                   f.cfg,
			ContainerDir:             filepath.Join(f.cfg.PersistenceRoot, "projects", f.projectID, "sessions"),
			StoreOptions:             f.starter.storeOptions,
			PersistedSessions:        f.metadata,
			ExecutionTargets:         f.metadata,
			ProjectWorkspaceBoundary: f.metadata,
		}),
		PromptHistory:              f.metadata,
		RuntimeAuthority:           f.authority,
		WorkflowSessionReactivator: f.controller,
	})
	response, err := client.RunPrompt(context.Background(), serverapi.RunPromptRequest{
		Intent: serverapi.OpenExistingSessionLaunchIntent(sessionID),
		Prompt: "continue",
	}, nil)
	if err != nil {
		t.Fatalf("RunPrompt: %v", err)
	}
	if !order.observed.Load() {
		t.Fatalf("provider was not called after exact durable assignment: err=%v requests=%d", err, len(order.Requests()))
	}
	if response.SessionID != sessionID.String() {
		t.Fatalf("RunPrompt response Session = %q, want %q", response.SessionID, sessionID)
	}
}

func TestRetainedSiblingResumeWaitsForFileBackedMetadataLock(t *testing.T) {
	sourceStarted := make(chan struct{})
	sourceRelease := make(chan struct{})
	f := newCurrentNodeRunnerFixture(
		t,
		ScriptedRuntimeStep{
			BeforeResponse: func(ctx context.Context) error {
				close(sourceStarted)
				select {
				case <-sourceRelease:
					return nil
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			},
			Response: ScriptedFinalAnswer(`{"transition":"split","commentary":"source"}`).Response,
		},
	)
	workflowID, branchNodeIDs := createCurrentNodeFanoutContinuationWorkflow(t, f.store, false)
	f.starter.store = currentNodeStartContextStore{
		RuntimeStore: f.store,
		transform: func(input workflowstore.CurrentNodeStartContext) workflowstore.CurrentNodeStartContext {
			if input.IsFanoutBranch {
				root := *input.ExecutionRoot
				root.SourceWorkspaceID = "workspace-missing"
				input.ExecutionRoot = &root
			}
			return input
		},
	}
	task := f.createTask(t, workflowID)
	f.startTask(t, task)
	select {
	case <-sourceStarted:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("fan-out source did not start")
	}
	close(sourceRelease)
	branches := f.waitForCurrentNode(t, task.ID, func(nodes []workflow.CurrentNode) bool {
		if len(nodes) != len(branchNodeIDs) {
			return false
		}
		for _, node := range nodes {
			if node.SessionID == nil ||
				node.Scheduling == nil ||
				node.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
				return false
			}
		}
		return true
	})
	selected := branches[0]
	selectedSession := *selected.SessionID
	continuation, err := workflowexecution.NewWorkflowSessionContinuation("continue", nil)
	if err != nil {
		t.Fatalf("new Workflow continuation: %v", err)
	}
	lockReady := make(chan func(), 1)
	done := make(chan error, 1)
	go func() {
		_, err := f.controller.ReactivateWorkflowSessionWithAcceptance(
			context.Background(),
			selectedSession,
			func(commit agentruntime.CommandAcceptance) (bool, error) {
				committed, err := commit(func() (bool, error) {
					return true, nil
				})
				if !committed || err != nil {
					return committed, err
				}
				lockReady <- acquireWorkflowMetadataWriteLock(t, f.cfg.PersistenceRoot)
				return true, nil
			},
			context.Background(),
			continuation,
		)
		done <- err
	}()
	select {
	case release := <-lockReady:
		t.Cleanup(release)
		select {
		case err := <-done:
			t.Fatalf("sibling Resume returned while metadata was locked: %v", err)
		case <-time.After(150 * time.Millisecond):
		}
		release()
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("selected Resume did not acquire the metadata lock")
	}
	select {
	case <-done:
	case <-time.After(currentNodeRunnerWait):
		t.Fatal("sibling Resume did not continue after metadata lock release")
	}
}
