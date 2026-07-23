package workflowrunner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

type workflowContinuationTestClient struct{}

func (workflowContinuationTestClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("unexpected model request")
}

type gatedMetadataSessionPersistence struct {
	sessions *sessiontest.Persistence
	metadata *metadata.Store
}

func (p gatedMetadataSessionPersistence) ObservePersistedStore(ctx context.Context, snapshot session.PersistedStoreSnapshot) error {
	if err := p.sessions.ObservePersistedStore(ctx, snapshot); err != nil {
		return err
	}
	return p.metadata.ImportSessionSnapshot(ctx, snapshot)
}

func (p gatedMetadataSessionPersistence) ObserveEventLogReconciliation(ctx context.Context, reconciliation session.PersistedEventLogReconciliation) error {
	if err := p.sessions.ObserveEventLogReconciliation(ctx, reconciliation); err != nil {
		return err
	}
	record, err := p.sessions.ResolvePersistedSession(ctx, reconciliation.SessionID)
	if err != nil {
		return err
	}
	if record.Meta == nil {
		return errors.New("reconciled session metadata is required")
	}
	return p.metadata.ImportSessionSnapshot(ctx, session.PersistedStoreSnapshot{
		SessionDir: record.SessionDir,
		Meta:       *record.Meta,
	})
}

func (p gatedMetadataSessionPersistence) ResolvePersistedSession(ctx context.Context, sessionID string) (session.PersistedSessionRecord, error) {
	return p.metadata.ResolvePersistedSession(ctx, sessionID)
}

type runSessionAttachmentGate struct {
	RuntimeStore
	runID   workflow.RunID
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newRunSessionAttachmentGate(store RuntimeStore, runID workflow.RunID) *runSessionAttachmentGate {
	return &runSessionAttachmentGate{
		RuntimeStore: store,
		runID:        runID,
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
}

func (s *runSessionAttachmentGate) AttachRunSession(ctx context.Context, runID workflow.RunID, generation int64, sessionID string) error {
	if runID == s.runID {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	return s.RuntimeStore.AttachRunSession(ctx, runID, generation, sessionID)
}

func TestSchedulerRejectedReviewContinueSessionAttachesBeforeStartingLaterScriptIntent(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("script fixture uses a POSIX shebang")
	}
	ctx := context.Background()
	fixture := newStarterFixture(
		t,
		config.WorkflowCompletionModeStructuredOutput,
		ScriptedFinalAnswer(`{"commentary":"implemented"}`),
		ScriptedFinalAnswer(`{"transition":"reject","commentary":"changes requested"}`),
		ScriptedFinalAnswer(`{"commentary":"reimplemented"}`),
	)
	reviewWorkflowID := createWorkflowRunnerRejectedReviewWorkflow(t, fixture.store)
	if _, err := fixture.store.LinkWorkflow(ctx, fixture.projectID, reviewWorkflowID, true); err != nil {
		t.Fatalf("link rejected-review workflow: %v", err)
	}
	task := fixture.createStartedTask(t)
	firstScheduler := fixture.scheduler(t)
	if err := firstScheduler.Process(ctx); err != nil {
		t.Fatalf("process implementation run: %v", err)
	}
	fixture.waitForRunCount(t, task.ID, 2)
	fixture.waitForCompletedRunCount(t, task.ID, 1)
	fixture.waitForActiveCountZero(t, firstScheduler)
	if err := firstScheduler.Process(ctx); err != nil {
		t.Fatalf("process review run: %v", err)
	}
	fixture.waitForRunCount(t, task.ID, 3)
	fixture.waitForCompletedRunCount(t, task.ID, 2)
	fixture.waitForActiveCountZero(t, firstScheduler)
	if err := firstScheduler.Close(); err != nil {
		t.Fatalf("close source scheduler: %v", err)
	}
	runs, err := fixture.store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("list implementation, review, and continuation runs: %v", err)
	}
	if len(runs) != 3 ||
		runs[0].CompletedAt == nil ||
		runs[0].SessionID == "" ||
		runs[1].CompletedAt == nil ||
		runs[1].SessionID == "" ||
		runs[2].StartedAt != nil ||
		runs[2].SessionID != "" {
		t.Fatalf("runs after rejected review = %+v", runs)
	}
	continuationInput, err := fixture.store.GetRunStartContext(ctx, runs[2].ID)
	if err != nil {
		t.Fatalf("load rejected-review continuation context: %v", err)
	}
	if continuationInput.ContextMode != workflow.ContextModeContinueSession ||
		continuationInput.SourceRunID != runs[0].ID ||
		continuationInput.SourceSessionID != runs[0].SessionID ||
		continuationInput.SourceNode.Key != "implementation" {
		t.Fatalf(
			"rejected-review continuation context = mode %q source run %q session %q node %q, want implementation run %q session %q",
			continuationInput.ContextMode,
			continuationInput.SourceRunID,
			continuationInput.SourceSessionID,
			continuationInput.SourceNode.Key,
			runs[0].ID,
			runs[0].SessionID,
		)
	}
	sourceSessionID, err := runtimeids.ParseSessionID(runs[0].SessionID)
	if err != nil {
		t.Fatalf("parse source session id: %v", err)
	}
	continuationRunID := runs[2].ID

	if err := fixture.runtimeAuthority.Close(ctx); err != nil {
		t.Fatalf("close default runtime authority: %v", err)
	}
	persistence := gatedMetadataSessionPersistence{
		sessions: sessiontest.NewPersistence(),
		metadata: fixture.metadata,
	}
	persistenceGate := sessiontest.NewPersistenceGate(persistence)
	storeOptions := []session.StoreOption{
		session.WithPersistenceObserver(persistenceGate),
		session.WithPersistedSessionResolver(persistence),
	}
	resourceLifecycle, ok := fixture.runtimes.(sessionruntime.AgentResourceLifecycle)
	if !ok {
		t.Fatal("starter runtime registry does not implement agent resource lifecycle")
	}
	fixture.runtimeAuthority = sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: fixture.cfg.PersistenceRoot,
		StoreOptions:    storeOptions,
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(ref sessionruntime.WorkflowExecutionRef) {
			if scheduler := *fixture.schedulerTarget; scheduler != nil {
				scheduler.RuntimeFinished(ref.RunID, ref.Generation)
			}
		}),
		PromptFeed:        fixture.runtimes,
		ResourceLifecycle: resourceLifecycle,
	})
	t.Cleanup(func() {
		if err := fixture.runtimeAuthority.Close(context.Background()); err != nil {
			t.Errorf("close gated runtime authority: %v", err)
		}
	})
	fixture.rebuildStarter(t)
	fixture.starter.storeOptions = storeOptions
	attachmentGate := newRunSessionAttachmentGate(fixture.store, continuationRunID)
	fixture.starter.store = attachmentGate
	var releaseAttachment sync.Once
	t.Cleanup(func() { releaseAttachment.Do(func() { close(attachmentGate.release) }) })

	const scriptPath = "watcher.sh"
	scriptWorkflowID := createWorkflowRunnerScriptWorkflow(t, fixture.store, scriptPath)
	if _, err := fixture.store.LinkWorkflow(ctx, fixture.projectID, scriptWorkflowID, false); err != nil {
		t.Fatalf("link script workflow: %v", err)
	}
	fixture.worktrees.afterCreate = func(worktreeRoot string) error {
		return os.WriteFile(
			filepath.Join(worktreeRoot, scriptPath),
			[]byte("#!/bin/sh\nprintf '%s\\n' '{\"transition\":\"done\",\"commentary\":\"watcher complete\"}'\n"),
			0o755,
		)
	}
	scriptTask, err := fixture.store.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:  fixture.projectID,
		WorkflowID: &scriptWorkflowID,
		Title:      "Watch pull request",
		Body:       "Run the watcher.",
	})
	if err != nil {
		t.Fatalf("create script task: %v", err)
	}
	if err := fixture.worktrees.RestoreLockedTaskWorktree(ctx, LockedTaskWorktreeRestoreRequest{TaskID: scriptTask.ID}); err != nil {
		t.Fatalf("materialize script task worktree: %v", err)
	}
	scriptExecutionTarget := fixture.worktrees.executionTargetCandidate(scriptTask)
	scriptStart, err := fixture.store.StartTaskWithExecutionTarget(ctx, scriptTask.ID, &scriptExecutionTarget)
	if err != nil {
		t.Fatalf("start script task: %v", err)
	}
	if err := fixture.automaticIntents.RegisterAutomaticStarts([]workflow.RunID{scriptStart.RunID}); err != nil {
		t.Fatalf("register script automatic intent: %v", err)
	}

	planningPersisted, releasePlanningPersistence := persistenceGate.BlockNext()
	t.Cleanup(releasePlanningPersistence)
	scheduler, err := NewSchedulerService(
		fixture.store,
		fixture.starter,
		fixture.starter.mutationPermit,
		SchedulerConfig{Concurrency: 2},
		WithAutomaticIntents(fixture.automaticIntents),
	)
	if err != nil {
		t.Fatalf("create continuation scheduler: %v", err)
	}
	*fixture.schedulerTarget = scheduler
	t.Cleanup(func() { _ = scheduler.Close() })
	processed := make(chan error, 1)
	go func() {
		processed <- scheduler.Process(ctx)
	}()

	select {
	case <-planningPersisted:
	case <-time.After(5 * time.Second):
		t.Fatal("continue_session planning did not reach persistence gate")
	}
	requireSessionStartAdmissionBusy(t, ctx, fixture.runtimeAuthority, sourceSessionID)
	runs, err = fixture.store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("list blocked continuation run: %v", err)
	}
	if len(runs) != 3 ||
		runs[2].ID != continuationRunID ||
		runs[2].StartedAt == nil ||
		runs[2].SessionID != "" {
		t.Fatalf("blocked continuation run = %+v, want started without attached session", runs)
	}
	scriptRuns, err := fixture.store.ListRuns(ctx, scriptTask.ID)
	if err != nil {
		t.Fatalf("list blocked script run: %v", err)
	}
	if len(scriptRuns) != 1 || scriptRuns[0].StartedAt != nil {
		t.Fatalf("later script intent advanced while continuation start was blocked: %+v", scriptRuns)
	}

	releasePlanningPersistence()
	select {
	case <-attachmentGate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("continue_session start did not reach durable Session attachment")
	}
	runs, err = fixture.store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("list continuation blocked at attachment: %v", err)
	}
	if len(runs) != 3 || runs[2].SessionID != "" {
		t.Fatalf("continuation attached before attachment gate release: %+v", runs)
	}
	scriptRuns, err = fixture.store.ListRuns(ctx, scriptTask.ID)
	if err != nil {
		t.Fatalf("list script run blocked by attachment: %v", err)
	}
	if len(scriptRuns) != 1 || scriptRuns[0].StartedAt != nil {
		t.Fatalf("later script intent started before durable Session attachment: %+v", scriptRuns)
	}
	releaseAttachment.Do(func() { close(attachmentGate.release) })
	select {
	case err := <-processed:
		if err != nil {
			t.Fatalf("process continuation and script intents: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler Process did not return after Session attachment release")
	}
	runs, err = fixture.store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("list attached continuation run: %v", err)
	}
	if len(runs) != 3 || runs[2].SessionID != sourceSessionID.String() || runs[2].InterruptedAt != nil {
		t.Fatalf("continuation run after scheduler Process = %+v, want source session attached", runs)
	}
	scriptRuns, err = fixture.store.ListRuns(ctx, scriptTask.ID)
	if err != nil {
		t.Fatalf("list started script run: %v", err)
	}
	if len(scriptRuns) != 1 || scriptRuns[0].StartedAt == nil || scriptRuns[0].InterruptedAt != nil {
		t.Fatalf("later script run after scheduler Process = %+v, want started by same invocation", scriptRuns)
	}
	fixture.waitForAllRunsCompleted(t, task.ID, 3)
	fixture.waitForCompletedRun(t, scriptTask.ID)
	fixture.waitForActiveCountZero(t, scheduler)
}

func createWorkflowRunnerRejectedReviewWorkflow(t *testing.T, store *workflowstore.Store) workflow.WorkflowID {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Rejected Review Workflow"})
	if err != nil {
		t.Fatalf("create rejected-review workflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("get rejected-review workflow definition: %v", err)
	}
	start := starterNodeByKind(t, def, workflow.NodeKindStart)
	done := starterNodeByKind(t, def, workflow.NodeKindTerminal)
	implementationID := workflow.NodeID("node-implementation-" + string(created.ID))
	reviewID := workflow.NodeID("node-review-" + string(created.ID))
	reimplementationID := workflow.NodeID("node-reimplementation-" + string(created.ID))
	for _, node := range []workflowstore.NodeRecord{
		{ID: implementationID, WorkflowID: created.ID, Key: "implementation", Kind: workflow.NodeKindAgent, DisplayName: "Implementation", SubagentRole: "coder", PromptTemplate: "Implement the task."},
		{ID: reviewID, WorkflowID: created.ID, Key: "review", Kind: workflow.NodeKindAgent, DisplayName: "Review", SubagentRole: "reviewer", PromptTemplate: "Review the implementation."},
		{ID: reimplementationID, WorkflowID: created.ID, Key: "reimplementation", Kind: workflow.NodeKindAgent, DisplayName: "Reimplementation", SubagentRole: "coder", PromptTemplate: "Address the rejected review."},
	} {
		if _, err := store.AddNode(ctx, node); err != nil {
			t.Fatalf("add rejected-review node %s: %v", node.Key, err)
		}
	}
	startGroupID := workflow.TransitionGroupID("group-start-" + string(created.ID))
	reviewGroupID := workflow.TransitionGroupID("group-review-" + string(created.ID))
	acceptGroupID := workflow.TransitionGroupID("group-accept-" + string(created.ID))
	rejectGroupID := workflow.TransitionGroupID("group-reject-" + string(created.ID))
	doneGroupID := workflow.TransitionGroupID("group-done-" + string(created.ID))
	for _, group := range []workflowstore.TransitionGroupRecord{
		{ID: startGroupID, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
		{ID: reviewGroupID, WorkflowID: created.ID, SourceNodeID: implementationID, TransitionID: "review", DisplayName: "Review"},
		{ID: acceptGroupID, WorkflowID: created.ID, SourceNodeID: reviewID, TransitionID: "accept", DisplayName: "Accept"},
		{ID: rejectGroupID, WorkflowID: created.ID, SourceNodeID: reviewID, TransitionID: "reject", DisplayName: "Reject"},
		{ID: doneGroupID, WorkflowID: created.ID, SourceNodeID: reimplementationID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("add rejected-review transition %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []workflowstore.EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroupID, Key: "start", TargetNodeID: implementationID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Implement the task."},
		{ID: workflow.EdgeID("edge-review-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: reviewGroupID, Key: "review", TargetNodeID: reviewID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Review the implementation."},
		{ID: workflow.EdgeID("edge-accept-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: acceptGroupID, Key: "accept", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
		{
			ID:                workflow.EdgeID("edge-reject-" + string(created.ID)),
			WorkflowID:        created.ID,
			TransitionGroupID: rejectGroupID,
			Key:               "reject",
			TargetNodeID:      reimplementationID,
			ContextMode:       workflow.ContextModeContinueSession,
			ContextSource: workflow.ContextSource{
				Kind:    workflow.ContextSourceSelectedNode,
				NodeKey: "implementation",
			},
			PromptTemplate: "Address the rejected review.",
		},
		{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("add rejected-review edge %s: %v", edge.Key, err)
		}
	}
	return created.ID
}

func createWorkflowRunnerScriptWorkflow(t *testing.T, store *workflowstore.Store, scriptPath string) workflow.WorkflowID {
	t.Helper()
	ctx := context.Background()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Watcher Workflow"})
	if err != nil {
		t.Fatalf("create script workflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("get script workflow definition: %v", err)
	}
	start := starterNodeByKind(t, def, workflow.NodeKindStart)
	done := starterNodeByKind(t, def, workflow.NodeKindTerminal)
	scriptID := workflow.NodeID("node-script-" + string(created.ID))
	startGroupID := workflow.TransitionGroupID("group-start-" + string(created.ID))
	doneGroupID := workflow.TransitionGroupID("group-done-" + string(created.ID))
	if _, err := store.AddNode(ctx, workflowstore.NodeRecord{
		ID:          scriptID,
		WorkflowID:  created.ID,
		Key:         "watch",
		Kind:        workflow.NodeKindScript,
		DisplayName: "Watch",
		ScriptPath:  scriptPath,
	}); err != nil {
		t.Fatalf("add script node: %v", err)
	}
	for _, group := range []workflowstore.TransitionGroupRecord{
		{ID: startGroupID, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
		{ID: doneGroupID, WorkflowID: created.ID, SourceNodeID: scriptID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := store.AddTransitionGroup(ctx, group); err != nil {
			t.Fatalf("add script transition %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []workflowstore.EdgeRecord{
		{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroupID, Key: "start", TargetNodeID: scriptID, ContextMode: workflow.ContextModeNewSession},
		{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
	} {
		if _, err := store.AddEdge(ctx, edge); err != nil {
			t.Fatalf("add script edge %s: %v", edge.Key, err)
		}
	}
	return created.ID
}

func requireSessionStartAdmissionBusy(
	t *testing.T,
	ctx context.Context,
	authority *sessionruntime.Authority,
	sessionID runtimeids.SessionID,
) {
	t.Helper()
	release, err := authority.TryBlockSessionStarts(
		ctx,
		[]runtimeids.SessionID{sessionID},
		sessionruntime.SessionStartBlockMaintenance,
	)
	if errors.Is(err, sessionruntime.ErrSessionStartAdmissionBusy) {
		return
	}
	if release != nil {
		if closeErr := release.Close(ctx); closeErr != nil {
			t.Fatalf("close unexpected session start block: %v", closeErr)
		}
	}
	t.Fatalf("continue_session planning admission = %v, want ErrSessionStartAdmissionBusy", err)
}

func TestPlanContinueSessionWorkflowSuccessorSerializesReplacementOnCurrentResource(t *testing.T) {
	ctx := context.Background()
	persistenceRoot := t.TempDir()
	workspace := t.TempDir()
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(ctx, workspace)
	if err != nil {
		t.Fatalf("register workspace: %v", err)
	}
	persistence := sessiontest.NewPersistence()
	persistenceGate := sessiontest.NewPersistenceGate(persistence)
	storeOptions := []session.StoreOption{
		session.WithPersistenceObserver(persistenceGate),
		session.WithPersistedSessionResolver(persistence),
	}
	containerDir := filepath.Join(persistenceRoot, "projects", binding.ProjectID, "sessions")
	store, err := session.Create(
		containerDir,
		"sessions",
		workspace,
		sessioncontract.SessionCategoryMain,
		storeOptions...,
	)
	if err != nil {
		t.Fatalf("create workflow session: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	settings := config.Settings{
		Model:              "gpt-5",
		ModelContextWindow: 200_000,
		OpenAIBaseURL:      "http://workflow-planning.example/v1",
		Reviewer:           config.ReviewerSettings{Frequency: "off"},
		Shell: config.ShellSettings{
			PostprocessingMode: config.ShellPostprocessingModeNone,
		},
	}
	runtimePlan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: settings,
		Workdir:  workspace,
		Client:   workflowContinuationTestClient{},
	})
	if err != nil {
		t.Fatalf("create runtime plan: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: persistenceRoot,
		StoreOptions:    storeOptions,
	})
	t.Cleanup(func() {
		if closeErr := authority.Close(context.Background()); closeErr != nil {
			t.Errorf("close authority: %v", closeErr)
		}
	})
	initial, err := authority.OpenRuntime(ctx, sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "initial-resource",
		Runtime:   &runtimePlan,
	})
	if err != nil {
		t.Fatalf("open initial runtime: %v", err)
	}
	starter := &Starter{
		cfg: config.App{
			PersistenceRoot: persistenceRoot,
			WorkspaceRoot:   workspace,
			Settings:        settings,
		},
		metadata:         metadataStore,
		runtimeAuthority: authority,
		storeOptions:     storeOptions,
	}
	input := workflowstore.RunStartContext{
		Run: workflowstore.RunRecord{
			ID: "workflow-run",
		},
		Task:            workflowstore.TaskRecord{ProjectID: binding.ProjectID},
		ContextMode:     workflow.ContextModeContinueSession,
		SourceSessionID: sessionID.String(),
		ExecutionRoot: &workflowstore.ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: workspace,
		},
	}

	planningPersisted, releasePlanningPersistence := persistenceGate.BlockNext()
	t.Cleanup(releasePlanningPersistence)
	planned := make(chan error, 1)
	go func() {
		plan, _, planErr := starter.planSession(ctx, input)
		if planErr == nil && plan.Descriptor.SessionID() != sessionID {
			planErr = errors.New("continue_session planning did not reuse the source session")
		}
		planned <- planErr
	}()
	select {
	case <-planningPersisted:
	case <-time.After(3 * time.Second):
		t.Fatal("workflow planning mutation did not reach persistence gate")
	}
	requireSessionStartAdmissionBusy(t, context.Background(), authority, sessionID)

	replacementRelease := make(chan struct{})
	var releaseReplacement sync.Once
	t.Cleanup(func() { releaseReplacement.Do(func() { close(replacementRelease) }) })
	type replacementResult struct {
		handle sessionruntime.ExecutionHandle
		err    error
	}
	replacementDescriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new open session descriptor: %v", err)
	}
	replaced := make(chan replacementResult, 1)
	go func() {
		handle, startErr := authority.StartAgentExecution(ctx, sessionruntime.AgentExecutionRequest{
			Descriptor: replacementDescriptor,
			Runtime:    &runtimePlan,
			Resource:   sessionruntime.ReplaceAgentResource{},
			Runner: func(runCtx context.Context, _ sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
				select {
				case <-replacementRelease:
					return nil
				case <-runCtx.Done():
					return context.Cause(runCtx)
				}
			},
		})
		replaced <- replacementResult{handle: handle, err: startErr}
	}()
	if err := authority.WithRuntime(ctx, initial.Resource(), func(context.Context, *runtime.Engine) error {
		return nil
	}); err != nil {
		t.Fatalf("current resource was unavailable while workflow planning owned the session: %v", err)
	}

	releasePlanningPersistence()
	select {
	case err := <-planned:
		if err != nil {
			t.Fatalf("plan existing workflow session: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("workflow planning did not complete after persistence release")
	}
	var replacement replacementResult
	select {
	case replacement = <-replaced:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement did not complete after workflow planning")
	}
	if replacement.err != nil {
		t.Fatalf("replace runtime: %v", replacement.err)
	}
	if replacement.handle == nil {
		t.Fatal("replacement returned nil execution handle")
	}
	if replacement.handle.Scope().ResourceGeneration() <= initial.Resource().Generation() {
		t.Fatalf(
			"replacement resource generation = %d, want successor of %d",
			replacement.handle.Scope().ResourceGeneration(),
			initial.Resource().Generation(),
		)
	}
	releaseReplacement.Do(func() { close(replacementRelease) })
	if _, err := replacement.handle.Wait(ctx); err != nil {
		t.Fatalf("wait replacement execution: %v", err)
	}
	if err := replacement.handle.Close(ctx); err != nil {
		t.Fatalf("close replacement execution: %v", err)
	}
}
