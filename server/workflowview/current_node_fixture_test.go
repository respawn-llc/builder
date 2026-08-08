package workflowview

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	"github.com/google/uuid"
)

type currentNodeViewFixture struct {
	ctx         context.Context
	metadata    *metadata.Store
	store       *workflowstore.Store
	binding     metadata.Binding
	cfg         config.App
	workflowID  runtimeids.WorkflowID
	agentNodeID workflow.NodeID
	authority   *sessionruntime.Authority
	quiescence  *currentNodeViewQuiescence
	projection  *TaskStatusProjection
	board       *Board
	detail      *TaskDetail
	tasks       *TaskList
	search      *TaskSearch
	activity    *Activity
}

type currentNodeViewStatusObservationSource struct {
	authority  *sessionruntime.Authority
	quiescence currentNodeViewQuiescenceSource
	store      *workflowstore.Store
	prompts    currentNodeViewPrompts
}

type currentNodeViewQuiescenceSource interface {
	CurrentTaskQuiescence([]workflow.TaskID) (map[workflow.TaskID]bool, error)
}

func (s currentNodeViewStatusObservationSource) ObserveWorkflowTaskExecutions(taskIDs []workflow.TaskID) (workflowexecution.WorkflowTaskExecutionObservation, error) {
	executions, err := s.authority.CurrentWorkflowTaskExecutionSnapshots()
	if err != nil {
		return workflowexecution.WorkflowTaskExecutionObservation{}, err
	}
	if len(taskIDs) == 0 {
		selected := make(map[workflow.TaskID]struct{}, len(executions))
		for taskID := range executions {
			selected[taskID] = struct{}{}
		}
		if quiescence, ok := s.quiescence.(*currentNodeViewQuiescence); ok {
			for taskID := range quiescence.blocked {
				selected[taskID] = struct{}{}
			}
		}
		taskIDs = make([]workflow.TaskID, 0, len(selected))
		for taskID := range selected {
			taskIDs = append(taskIDs, taskID)
		}
	}
	quiescence, err := s.quiescence.CurrentTaskQuiescence(taskIDs)
	if err != nil {
		return workflowexecution.WorkflowTaskExecutionObservation{}, err
	}
	lifecycle := make(map[workflow.TaskID]workflowexecution.WorkflowTaskLifecycleSnapshot, len(executions))
	for taskID, snapshot := range executions {
		currentNodes, err := s.store.ListCurrentNodes(context.Background(), taskID)
		if err != nil {
			return workflowexecution.WorkflowTaskExecutionObservation{}, err
		}
		if len(currentNodes) == 0 {
			continue
		}
		lifecycleSnapshot := workflowexecution.WorkflowTaskLifecycleSnapshot{
			CurrentNodes: currentNodes,
		}
		currentKeys := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(currentNodes))
		for _, currentNode := range currentNodes {
			key, err := currentNode.Reference.Key()
			if err != nil {
				return workflowexecution.WorkflowTaskExecutionObservation{}, err
			}
			currentKeys[key] = struct{}{}
		}
		queued := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(snapshot.Executions))
		for _, execution := range snapshot.Executions {
			key, err := execution.Ref.CurrentNode.Key()
			if err != nil {
				return workflowexecution.WorkflowTaskExecutionObservation{}, err
			}
			if _, current := currentKeys[key]; !current {
				continue
			}
			if _, exists := queued[key]; !exists {
				queued[key] = struct{}{}
				lifecycleSnapshot.QueuedCurrentNodes = append(
					lifecycleSnapshot.QueuedCurrentNodes,
					execution.Ref.CurrentNode,
				)
			}
			if !execution.Queued {
				exact := workflowstore.LifecycleExactExecution{
					ProjectID:   execution.Ref.ProjectID,
					WorkflowID:  execution.Ref.WorkflowID,
					CurrentNode: execution.Ref.CurrentNode,
					ScopeID:     execution.ScopeID,
					Phase:       workflowstore.LifecycleExactExecutionRunning,
				}
				if execution.Agent != nil {
					exact.Agent = &workflowstore.LifecycleAgentExecutionTarget{
						SessionID: execution.Agent.SessionID,
					}
					for _, promptReference := range execution.PendingPrompts {
						for _, prompt := range s.prompts.bySession[execution.Agent.SessionID.String()] {
							if prompt.ID != promptReference.ID {
								continue
							}
							kind := workflowstore.LifecyclePendingPromptQuestion
							if promptReference.Kind == sessionruntime.PendingPromptKindSessionApproval {
								kind = workflowstore.LifecyclePendingPromptSessionApproval
							}
							exact.PendingPrompts = append(exact.PendingPrompts, workflowstore.LifecyclePendingPromptReference{
								ID:        prompt.ID,
								Kind:      kind,
								CreatedAt: prompt.CreatedAt,
							})
						}
					}
				}
				if execution.Script != nil {
					exact.Script = &workflowstore.LifecycleScriptExecutionTarget{Path: execution.Script.Path}
				}
				lifecycleSnapshot.ExactExecutions = append(
					lifecycleSnapshot.ExactExecutions,
					exact,
				)
			}
		}
		lifecycle[taskID] = lifecycleSnapshot
	}
	return workflowexecution.WorkflowTaskExecutionObservation{
		Executions: executions,
		Quiescence: quiescence,
		Lifecycle:  lifecycle,
	}, nil
}

func lifecycleApprovalDecisionsForTest(
	decisions []clientui.ApprovalDecision,
) []workflowstore.LifecycleApprovalDecision {
	out := make([]workflowstore.LifecycleApprovalDecision, len(decisions))
	for index, decision := range decisions {
		out[index] = workflowstore.LifecycleApprovalDecision(decision)
	}
	return out
}

func (s currentNodeViewStatusObservationSource) CaptureWorkflowTaskExecutions(
	ctx context.Context,
	taskIDs []workflow.TaskID,
	operation func(workflowexecution.WorkflowTaskExecutionObservation, *sqlitegen.Queries) error,
) error {
	observation, err := s.ObserveWorkflowTaskExecutions(taskIDs)
	if err != nil {
		return err
	}
	return captureWorkflowViewTestObservation(ctx, s.store, observation, operation)
}

func (s currentNodeViewStatusObservationSource) CaptureWorkflowTaskLifecycleQuery(
	ctx context.Context,
	operation func(string, *sqlitegen.Queries) error,
) error {
	observation, err := s.ObserveWorkflowTaskExecutions(nil)
	if err != nil {
		return err
	}
	token, release, err := sqlitegen.RegisterLifecycleTaskStateResolver(func(taskID string) (sqlitegen.LifecycleTaskQueryState, error) {
		return testLifecycleTaskState(observation, workflow.TaskID(taskID))
	})
	if err != nil {
		return err
	}
	defer release()
	return captureWorkflowViewTestObservation(
		ctx,
		s.store,
		observation,
		func(_ workflowexecution.WorkflowTaskExecutionObservation, queries *sqlitegen.Queries) error {
			return operation(token, queries)
		},
	)
}

func (s currentNodeViewStatusObservationSource) CaptureWorkflowTaskBoundedLifecycleRead(
	ctx context.Context,
	operation func(string, *sqlitegen.Queries, workflowexecution.WorkflowTaskLifecycleReader) error,
) error {
	observation, err := s.ObserveWorkflowTaskExecutions(nil)
	if err != nil {
		return err
	}
	return captureWorkflowViewTestBoundedLifecycle(
		ctx,
		s.store,
		observation,
		currentNodeViewPendingQuestionsForTest(observation, s.prompts),
		nil,
		operation,
	)
}

func currentNodeViewPendingQuestionsForTest(
	observation workflowexecution.WorkflowTaskExecutionObservation,
	prompts currentNodeViewPrompts,
) []workflowstore.LifecyclePendingQuestion {
	out := make([]workflowstore.LifecyclePendingQuestion, 0)
	for taskID, lifecycle := range observation.Lifecycle {
		for _, exact := range lifecycle.ExactExecutions {
			if exact.Agent == nil {
				continue
			}
			for _, reference := range exact.PendingPrompts {
				for _, prompt := range prompts.bySession[exact.Agent.SessionID.String()] {
					if prompt.ID != reference.ID {
						continue
					}
					out = append(out, workflowstore.LifecyclePendingQuestion{
						TaskID:      taskID,
						CurrentNode: exact.CurrentNode,
						SessionID:   exact.Agent.SessionID,
						Prompt: workflowstore.LifecyclePendingPrompt{
							ID:                     prompt.ID,
							Kind:                   reference.Kind,
							CreatedAt:              prompt.CreatedAt,
							Question:               prompt.Question,
							Suggestions:            append([]string(nil), prompt.Suggestions...),
							RecommendedOptionIndex: prompt.RecommendedOptionIndex,
							ApprovalDecisions: lifecycleApprovalDecisionsForTest(
								prompt.ApprovalDecisions,
							),
						},
					})
				}
			}
		}
	}
	return out
}

func captureWorkflowViewTestObservation(
	ctx context.Context,
	store *workflowstore.Store,
	observation workflowexecution.WorkflowTaskExecutionObservation,
	operation func(workflowexecution.WorkflowTaskExecutionObservation, *sqlitegen.Queries) error,
) (err error) {
	if store == nil {
		return errors.New("workflow test Store is required")
	}
	publication, err := workflowstore.NewLifecyclePublication(store)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := publication.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	capture, err := publication.Capture(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := capture.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return capture.WithQueries(func(queries *sqlitegen.Queries) error {
		return operation(observation, queries)
	})
}

type startedCurrentNodeViewTask struct {
	task        workflowstore.TaskRecord
	currentNode workflow.CurrentNodeReference
}

type currentNodeViewQuestion struct {
	authority *sessionruntime.Authority
	sessionID runtimeids.SessionID
	request   tools.AskQuestionRequest
	handle    sessionruntime.ExecutionHandle
}

func newCurrentNodeViewFixture(t *testing.T, requiresApproval bool) currentNodeViewFixture {
	t.Helper()
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	t.Setenv(config.PersistenceRootEnvName, filepath.Join(home, "kent-root"))
	cfg, err := config.Load(workspaceRoot, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore := testsetup.OpenStore(t, cfg.PersistenceRoot)
	binding, err := metadataStore.RegisterWorkspaceBinding(t.Context(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if err := metadataStore.SetProjectKey(t.Context(), binding.ProjectID, "WOR"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	store, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(testsetup.QuestionsEnabled("coder")))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	workflowID := currentNodeViewWorkflow(t, store, requiresApproval)
	if _, err := store.LinkWorkflow(t.Context(), binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	definitions, err := NewDefinitionProjection(store)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	definition, _, err := store.GetDefinition(t.Context(), workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agentNodeID := currentNodeViewNodeID(t, definition, "agent")
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
	})
	quiescence := &currentNodeViewQuiescence{blocked: map[workflow.TaskID]bool{}}
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close fixture authority: %v", err)
		}
	})
	projector := NewTaskProjector()
	projection, err := NewTaskStatusProjection(
		metadataStore,
		store,
		projector,
		currentNodeViewStatusObservationSource{
			authority:  authority,
			quiescence: quiescence,
			store:      store,
		},
	)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}
	dependencies, err := NewTaskDependencies(metadataStore, projection)
	if err != nil {
		t.Fatalf("NewTaskDependencies: %v", err)
	}
	board, err := NewBoard(metadataStore, definitions, testsetup.QuestionsEnabled("coder"), projection)
	if err != nil {
		t.Fatalf("NewBoard: %v", err)
	}
	detail, err := NewTaskDetail(metadataStore, projection, dependencies)
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	tasks, err := NewTaskList(metadataStore, definitions, projection)
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, projection)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	activity, err := NewActivity(metadataStore, projector)
	if err != nil {
		t.Fatalf("NewActivity: %v", err)
	}
	return currentNodeViewFixture{
		ctx:         t.Context(),
		metadata:    metadataStore,
		store:       store,
		binding:     binding,
		cfg:         cfg,
		workflowID:  workflowID,
		agentNodeID: agentNodeID,
		authority:   authority,
		quiescence:  quiescence,
		projection:  projection,
		board:       board,
		detail:      detail,
		tasks:       tasks,
		search:      search,
		activity:    activity,
	}
}

type currentNodeViewQuiescence struct {
	blocked map[workflow.TaskID]bool
}

func (q *currentNodeViewQuiescence) CurrentTaskQuiescence(taskIDs []workflow.TaskID) (map[workflow.TaskID]bool, error) {
	result := make(map[workflow.TaskID]bool, len(taskIDs))
	for _, taskID := range taskIDs {
		result[taskID] = !q.blocked[taskID]
	}
	return result, nil
}

func (f currentNodeViewFixture) startTask(t *testing.T, title string) startedCurrentNodeViewTask {
	t.Helper()
	workflowID := f.workflowID
	task, err := f.store.CreateTask(f.ctx, workflowstore.CreateTaskRequest{
		ProjectID:         f.binding.ProjectID,
		WorkflowID:        &workflowID,
		Title:             title,
		SourceWorkspaceID: f.binding.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return f.startExistingTask(t, task)
}

func (f currentNodeViewFixture) startExistingTask(t *testing.T, task workflowstore.TaskRecord) startedCurrentNodeViewTask {
	t.Helper()
	publication, err := workflowstore.NewLifecyclePublication(f.store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	started, err := publication.PublishTaskStart(
		f.ctx,
		task.ID,
		testsetup.PreparedPublicationStage(workflowstore.NewTaskStartLifecycleDelta),
	)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("StartTask mutation = %+v, want one Current Node", started.Mutation)
	}
	return startedCurrentNodeViewTask{task: task, currentNode: started.Mutation.Created[0].Reference}
}

func (f currentNodeViewFixture) createBacklogTask(t *testing.T, title string) workflowstore.TaskRecord {
	t.Helper()
	workflowID := f.workflowID
	task, err := f.store.CreateTask(f.ctx, workflowstore.CreateTaskRequest{
		ProjectID:         f.binding.ProjectID,
		WorkflowID:        &workflowID,
		Title:             title,
		SourceWorkspaceID: f.binding.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateTask backlog: %v", err)
	}
	return task
}

func (f currentNodeViewFixture) bindCurrentNodeSession(t *testing.T, started startedCurrentNodeViewTask) runtimeids.SessionID {
	t.Helper()
	sessionID := f.newCurrentNodeViewSession(t)
	if _, err := f.store.BindSessionToCurrentNode(f.ctx, workflowstore.CurrentNodeSessionBindingRequest{
		Association: workflowstore.TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  started.currentNode,
			AssociatedAt: time.Now().UTC(),
		},
	}); err != nil {
		t.Fatalf("BindSessionToCurrentNode: %v", err)
	}
	return sessionID
}

func (f currentNodeViewFixture) newCurrentNodeViewSession(t *testing.T) runtimeids.SessionID {
	t.Helper()
	sessionRoot := filepath.Join(f.cfg.PersistenceRoot, "projects", f.binding.ProjectID, "sessions")
	sessionStore, err := session.Create(
		sessionRoot,
		filepath.Base(f.cfg.WorkspaceRoot),
		f.cfg.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		f.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sessionStore.EnsureDurable(); err != nil {
		t.Fatalf("session.EnsureDurable: %v", err)
	}
	if err := sessionStore.SetName("Current Node session"); err != nil {
		t.Fatalf("session.SetName: %v", err)
	}
	if _, err := f.metadata.ResolvePersistedSession(f.ctx, sessionStore.Meta().SessionID); err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(sessionStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return sessionID
}

func (f currentNodeViewFixture) setTaskUpdatedAt(t *testing.T, taskID workflow.TaskID, unixMs int64) {
	t.Helper()
	if _, err := f.metadata.DB().ExecContext(
		f.ctx,
		`UPDATE tasks SET updated_at_unix_ms = ? WHERE id = ?`,
		unixMs,
		string(taskID),
	); err != nil {
		t.Fatalf("set task updated at: %v", err)
	}
}

func (f currentNodeViewFixture) setSessionCreatedAt(t *testing.T, sessionID runtimeids.SessionID, unixMs int64) {
	t.Helper()
	if _, err := f.metadata.DB().ExecContext(
		f.ctx,
		`UPDATE sessions SET created_at_unix_ms = ? WHERE id = ?`,
		unixMs,
		sessionID.String(),
	); err != nil {
		t.Fatalf("set session created at: %v", err)
	}
}

func (f currentNodeViewFixture) setCommentUpdatedAt(t *testing.T, commentID string, unixMs int64) {
	t.Helper()
	if _, err := f.metadata.DB().ExecContext(
		f.ctx,
		`UPDATE task_comments SET updated_at_unix_ms = ? WHERE id = ?`,
		unixMs,
		commentID,
	); err != nil {
		t.Fatalf("set comment updated at: %v", err)
	}
}

func (f currentNodeViewFixture) setApprovalCreatedAt(t *testing.T, approvalID string, unixMs int64) {
	t.Helper()
	if _, err := f.metadata.DB().ExecContext(
		f.ctx,
		`UPDATE task_pending_approvals SET created_at_unix_ms = ? WHERE id = ?`,
		unixMs,
		approvalID,
	); err != nil {
		t.Fatalf("set Approval created at: %v", err)
	}
}

func (f currentNodeViewFixture) setCurrentNodeInterruptedAt(
	t *testing.T,
	reference workflow.CurrentNodeReference,
	unixMs int64,
) {
	t.Helper()
	branchKey, branchScoped := reference.TransitionBranchKey()
	var err error
	if branchScoped {
		_, err = f.metadata.DB().ExecContext(
			f.ctx,
			`UPDATE task_current_nodes
SET interrupted_at_unix_ms = ?
WHERE task_id = ? AND node_id = ? AND transition_branch_key = ?`,
			unixMs,
			string(reference.TaskID),
			string(reference.NodeID),
			string(branchKey),
		)
	} else {
		_, err = f.metadata.DB().ExecContext(
			f.ctx,
			`UPDATE task_current_nodes
SET interrupted_at_unix_ms = ?
WHERE task_id = ? AND node_id = ? AND transition_branch_key IS NULL`,
			unixMs,
			string(reference.TaskID),
			string(reference.NodeID),
		)
	}
	if err != nil {
		t.Fatalf("set Current Node interrupted at: %v", err)
	}
}

func (f currentNodeViewFixture) newAgentAuthority(t *testing.T) (*sessionruntime.Authority, sessionruntime.AgentRuntimePlan) {
	t.Helper()
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: f.cfg.PersistenceRoot,
		StoreOptions:    f.metadata.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close Agent authority: %v", err)
		}
	})
	return authority, f.newAgentRuntimePlan(t)
}

func (f currentNodeViewFixture) startCurrentNodeQuestion(t *testing.T, started startedCurrentNodeViewTask) currentNodeViewQuestion {
	t.Helper()
	authority, plan := f.newAgentAuthority(t)
	return f.startCurrentNodeQuestionOnAuthority(t, started, authority, plan)
}

func (f currentNodeViewFixture) startCurrentNodeQuestionOnAuthority(
	t *testing.T,
	started startedCurrentNodeViewTask,
	authority *sessionruntime.Authority,
	plan sessionruntime.AgentRuntimePlan,
) currentNodeViewQuestion {
	t.Helper()
	sessionID := f.bindCurrentNodeSession(t, started)
	request := tools.AskQuestionRequest{
		ID:                     uuid.NewString(),
		StepID:                 uuid.NewString(),
		Question:               "Proceed?",
		Suggestions:            []string{"Yes", "No"},
		RecommendedOptionIndex: 1,
	}
	lease, err := authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   f.binding.ProjectID,
		WorkflowID:  f.workflowID,
		CurrentNode: started.currentNode,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	lease.Release()
	handle, err := authority.StartAgentExecution(f.ctx, sessionruntime.AgentExecutionRequest{
		Descriptor: mustOpenCurrentNodeViewSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Workflow:   &lease,
		Resource:   sessionruntime.OpenAgentResource{},
		Runner: func(ctx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			_, awaitErr := authority.AwaitPromptResponse(ctx, scope.ID(), request)
			return awaitErr
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	if err := handle.PublishRunning(f.ctx, workflowViewRunningPublicationStub{}); err != nil {
		t.Fatalf("PublishRunning: %v", err)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		snapshots, snapshotErr := authority.CurrentWorkflowTaskExecutionSnapshots()
		if snapshotErr != nil {
			return false
		}
		executions := snapshots[started.task.ID].Executions
		return len(executions) == 1 && executions[0].HasPendingPromptKind(sessionruntime.PendingPromptKindQuestion)
	}, "timed out waiting for live workflow Question")
	return currentNodeViewQuestion{
		authority: authority,
		sessionID: sessionID,
		request:   request,
		handle:    handle,
	}
}

type workflowViewRunningPublicationStub struct{}

func (workflowViewRunningPublicationStub) PublishWorkflowRunning(
	_ context.Context,
	_ sessionruntime.TaskExecution,
	activation sessionruntime.WorkflowRunningActivation,
) error {
	return activation.Activate()
}

func (q currentNodeViewQuestion) resolve(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := q.authority.SubmitPromptResponse(q.sessionID, tools.AskQuestionResponse{
		RequestID: q.request.ID,
		Answer:    "Yes",
	}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse: %v", err)
	}
	if _, err := q.handle.Wait(ctx); err != nil {
		t.Fatalf("wait Question execution: %v", err)
	}
}

func (f currentNodeViewFixture) newAgentRuntimePlan(t *testing.T) sessionruntime.AgentRuntimePlan {
	t.Helper()
	settings := f.cfg.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200_000
	settings.Reviewer.Frequency = "off"
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: settings,
		FilesystemContext: func() tools.FilesystemContext {
			context, err := runtimewire.NewFilesystemContext(f.cfg.WorkspaceRoot, f.cfg.WorkspaceRoot, metadata.ProjectWorkspaceBoundary{ProjectID: "test"})
			if err != nil {
				t.Fatalf("NewFilesystemContext: %v", err)
			}
			return context
		}(),
		Client: currentNodeViewLLMClient{},
	})
	if err != nil {
		t.Fatalf("NewAgentRuntimePlan: %v", err)
	}
	return plan
}

func (f currentNodeViewFixture) attention(t *testing.T) *Attention {
	t.Helper()
	attention, err := NewAttention(f.metadata, f.projection)
	if err != nil {
		t.Fatalf("NewAttention: %v", err)
	}
	return attention
}

func currentNodeViewWorkflow(t *testing.T, store *workflowstore.Store, requiresApproval bool) runtimeids.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(t.Context(), workflowstore.CreateWorkflowRequest{Name: "Current Node workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	definition, _, err := store.GetDefinition(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetDefinition initial: %v", err)
	}
	startNodeID := currentNodeViewNodeIDByKind(t, definition, workflow.NodeKindStart)
	terminalNodeID := currentNodeViewNodeIDByKind(t, definition, workflow.NodeKindTerminal)
	if _, err := store.AddNode(t.Context(), workflowstore.NodeRecord{
		WorkflowID:   created.ID,
		Key:          "agent",
		Kind:         workflow.NodeKindAgent,
		DisplayName:  "Agent",
		SubagentRole: "coder",
	}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	definition, _, err = store.GetDefinition(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetDefinition after node: %v", err)
	}
	agentNodeID := currentNodeViewNodeID(t, definition, "agent")
	if _, err := store.AddTransitionGroup(t.Context(), workflowstore.TransitionGroupRecord{
		WorkflowID:   created.ID,
		SourceNodeID: startNodeID,
		TransitionID: "start",
		DisplayName:  "Start",
	}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	definition, _, err = store.GetDefinition(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetDefinition after start group: %v", err)
	}
	startGroupID := currentNodeViewTransitionGroupID(t, definition, startNodeID, "start")
	if _, err := store.AddEdge(t.Context(), workflowstore.EdgeRecord{
		WorkflowID:        created.ID,
		TransitionGroupID: startGroupID,
		Key:               "start",
		TargetNodeID:      agentNodeID,
		AssigneeSelection: workflow.AssigneeSelectionConfigured,
		ThinkingSelection: workflow.ThinkingSelectionConfigured,
		ContextMode:       workflow.ContextModeNewSession,
		PromptTemplate:    "Do work.",
	}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(t.Context(), workflowstore.TransitionGroupRecord{
		WorkflowID:   created.ID,
		SourceNodeID: agentNodeID,
		TransitionID: "done",
		DisplayName:  "Done",
	}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	definition, _, err = store.GetDefinition(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetDefinition after done group: %v", err)
	}
	doneGroupID := currentNodeViewTransitionGroupID(t, definition, agentNodeID, "done")
	if _, err := store.AddEdge(t.Context(), workflowstore.EdgeRecord{
		WorkflowID:        created.ID,
		TransitionGroupID: doneGroupID,
		Key:               "done",
		TargetNodeID:      terminalNodeID,
		AssigneeSelection: workflow.AssigneeSelectionConfigured,
		ThinkingSelection: workflow.ThinkingSelectionConfigured,
		ContextMode:       workflow.ContextModeNewSession,
		RequiresApproval:  requiresApproval,
	}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	return created.ID
}

func currentNodeViewNodeID(t *testing.T, definition workflow.Definition, key string) workflow.NodeID {
	t.Helper()
	for _, node := range definition.Nodes {
		if workflow.NodeKey(node) == workflow.ModelKey(key) {
			return workflow.NodeIDOf(node)
		}
	}
	t.Fatalf("workflow node key %q missing", key)
	return ""
}

func currentNodeViewNodeIDByKind(t *testing.T, definition workflow.Definition, kind workflow.NodeKind) workflow.NodeID {
	t.Helper()
	for _, node := range definition.Nodes {
		if node.Kind() == kind {
			return workflow.NodeIDOf(node)
		}
	}
	t.Fatalf("workflow node kind %q missing", kind)
	return ""
}

func currentNodeViewTransitionGroupID(t *testing.T, definition workflow.Definition, sourceNodeID workflow.NodeID, transitionID string) workflow.TransitionGroupID {
	t.Helper()
	for _, group := range definition.TransitionGroups {
		if group.SourceNodeID == sourceNodeID && group.TransitionID == workflow.TransitionID(transitionID) {
			return group.ID
		}
	}
	t.Fatalf("workflow transition %q from node %q missing", transitionID, sourceNodeID)
	return ""
}

func workflowViewBoardColumn(t *testing.T, board serverapi.WorkflowBoard, nodeID workflow.NodeID) serverapi.WorkflowBoardColumn {
	t.Helper()
	for _, column := range board.Columns {
		if column.Node.NodeID == string(nodeID) {
			return column
		}
	}
	t.Fatalf("board column for node %q missing", nodeID)
	return serverapi.WorkflowBoardColumn{}
}

func mustDefinitionProjection(t *testing.T, store *workflowstore.Store) *DefinitionProjection {
	t.Helper()
	projection, err := NewDefinitionProjection(store)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	return projection
}

func stringPointer(value string) *string {
	return &value
}

func intPointer(value int) *int {
	return &value
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalStatusKinds(left, right []serverapi.WorkflowTaskStatusKind) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func mustOpenCurrentNodeViewSessionDescriptor(t *testing.T, sessionID runtimeids.SessionID) session.SessionDescriptor {
	t.Helper()
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("NewOpenSessionDescriptor: %v", err)
	}
	return descriptor
}

type currentNodeViewLLMClient struct{}

func (currentNodeViewLLMClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (currentNodeViewLLMClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

type currentNodeViewPrompts struct {
	bySession map[string][]PendingPromptSnapshot
}

func (p currentNodeViewPrompts) ListPendingPrompts(sessionID string) ([]PendingPromptSnapshot, error) {
	return append([]PendingPromptSnapshot(nil), p.bySession[sessionID]...), nil
}

type emptyCurrentNodeViewPrompts struct{}

func (emptyCurrentNodeViewPrompts) ListPendingPrompts(string) ([]PendingPromptSnapshot, error) {
	return []PendingPromptSnapshot{}, nil
}
