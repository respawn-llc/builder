package workflowview

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestTaskDetailSelectorsConvergeOnCompleteCoreDetail(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	sourceWorkspace, err := metadataStore.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	requireDoneTransitionApproval(t, ctx, metadataStore, workflowID)
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:         binding.ProjectID,
		Title:             "Focused task detail",
		Body:              "Core detail body",
		SourceWorkspaceID: sourceWorkspace.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTaskWithExecutionTarget(ctx, task.ID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   sourceWorkspace.WorkspaceID,
			SourceWorkspaceRoot: sourceWorkspace.CanonicalRoot,
		},
	})
	if err != nil {
		t.Fatalf("StartTaskWithExecutionTarget: %v", err)
	}
	_, err = workflowStore.AddComment(ctx, task.ID, "Task detail comment", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}

	detail, err := NewTaskDetail(metadataStore, NewTaskProjector(), sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{}))
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	byID, err := detail.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	byProjectShortID, err := detail.GetTaskByProjectShortID(ctx, binding.ProjectID, task.ShortID)
	if err != nil {
		t.Fatalf("GetTaskByProjectShortID: %v", err)
	}
	byGlobalShortID, err := detail.GetTaskByShortID(ctx, task.ShortID)
	if err != nil {
		t.Fatalf("GetTaskByShortID: %v", err)
	}
	if !reflect.DeepEqual(byID, byProjectShortID) || !reflect.DeepEqual(byProjectShortID, byGlobalShortID) {
		t.Fatalf("task detail selectors diverged:\nby id: %+v\nby project short id: %+v\nby global short id: %+v", byID, byProjectShortID, byGlobalShortID)
	}
	if byID.CurrentSessionIDs == nil || byID.CurrentScripts == nil ||
		len(byID.CurrentSessionIDs) != 0 || len(byID.CurrentScripts) != 0 {
		t.Fatalf("current execution targets = sessions %v scripts %v, want empty arrays", byID.CurrentSessionIDs, byID.CurrentScripts)
	}
	if byID.SourceWorkspace.WorkspaceID != sourceWorkspace.WorkspaceID {
		t.Fatalf("source workspace = %+v, want %q", byID.SourceWorkspace, sourceWorkspace.WorkspaceID)
	}
	if byID.ExecutionTarget == nil ||
		byID.ExecutionTarget.Mode != "none" {
		t.Fatalf("execution target = %+v, want none", byID.ExecutionTarget)
	}
	if byID.AttentionCount != 1 {
		t.Fatalf("attention count = %d, want 1", byID.AttentionCount)
	}
}

func TestTaskDetailReportsMissingLinkedSourceWorkspace(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	sourceWorkspace, err := metadataStore.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:         binding.ProjectID,
		Title:             "Missing source workspace",
		SourceWorkspaceID: sourceWorkspace.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := os.Remove(sourceWorkspace.CanonicalRoot); err != nil {
		t.Fatalf("remove source workspace: %v", err)
	}

	detail, err := NewTaskDetail(metadataStore, NewTaskProjector(), sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{}))
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	got, err := detail.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.SourceWorkspace.Availability != "missing" {
		t.Fatalf("source workspace availability = %q, want missing", got.SourceWorkspace.Availability)
	}
}

func TestTaskDetailUsesLiveScriptTargetsForInterruptPrecedence(t *testing.T) {
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep executable unavailable: %v", err)
	}
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Live script targets",
		Body:      "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	claimed, err := workflowStore.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	detail, err := NewTaskDetail(metadataStore, NewTaskProjector(), authority)
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	cancellationGrace := 50 * time.Millisecond
	handle, err := authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &sessionruntime.WorkflowExecutionRef{
			TaskID:     task.ID,
			RunID:      started.RunID,
			Generation: claimed.Generation,
		},
		Command: sessionruntime.ScriptCommand{
			Path:              sleepPath,
			Args:              []string{"30"},
			CancellationGrace: &cancellationGrace,
		},
	})
	if err != nil {
		t.Fatalf("StartScriptExecution: %v", err)
	}
	t.Cleanup(func() {
		if handle != nil {
			if err := handle.Stop(context.Background()); err != nil {
				t.Errorf("stop script: %v", err)
			}
		}
	})

	live, err := detail.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask live: %v", err)
	}
	if !live.Actions.CanInterrupt || live.Actions.CanStart || live.Actions.CanResume {
		t.Fatalf("live actions = %+v, want interrupt precedence", live.Actions)
	}
	if live.Status.Kind != serverapi.WorkflowTaskStatusKindRunning ||
		len(live.CurrentSessionIDs) != 0 || len(live.CurrentScripts) != 1 ||
		live.CurrentScripts[0].RunID != string(started.RunID) ||
		live.CurrentScripts[0].Path != sleepPath {
		t.Fatalf("live targets = sessions %v scripts %+v", live.CurrentSessionIDs, live.CurrentScripts)
	}

	if err := handle.Stop(context.Background()); err != nil {
		t.Fatalf("stop script: %v", err)
	}
	handle = nil
	stopped, err := detail.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask stopped: %v", err)
	}
	if stopped.Actions.CanInterrupt || stopped.Actions.CanStart ||
		stopped.Status.Kind == serverapi.WorkflowTaskStatusKindRunning ||
		len(stopped.CurrentScripts) != 0 || stopped.CurrentScripts == nil {
		t.Fatalf("stopped detail = %+v", stopped)
	}
}

func TestTaskDetailDoesNotOfferInterruptWhileScriptCompletionFinalizerIsBlocked(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true executable unavailable: %v", err)
	}
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Finalizing script",
		Body:      "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	claimed, err := workflowStore.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	detail, err := NewTaskDetail(metadataStore, NewTaskProjector(), authority)
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	finalizeStarted := make(chan struct{})
	releaseFinalize := make(chan struct{})
	handle, err := authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &sessionruntime.WorkflowExecutionRef{
			TaskID:     task.ID,
			RunID:      started.RunID,
			Generation: claimed.Generation,
		},
		Command: sessionruntime.ScriptCommand{Path: truePath},
		Finalize: func(ctx context.Context, _ sessionruntime.ExecutionScope, _ sessionruntime.ScriptResult, _ error) error {
			if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{
				RunID:        started.RunID,
				TransitionID: "done",
			}); err != nil {
				return err
			}
			close(finalizeStarted)
			<-releaseFinalize
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StartScriptExecution: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-releaseFinalize:
		default:
			close(releaseFinalize)
		}
		_ = handle.Close(context.Background())
	})
	<-finalizeStarted

	finalizing, err := detail.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask finalizing: %v", err)
	}
	if finalizing.Actions.CanInterrupt ||
		len(finalizing.CurrentSessionIDs) != 0 ||
		len(finalizing.CurrentScripts) != 0 {
		t.Fatalf("finalizing task remains interruptible: %+v", finalizing)
	}

	close(releaseFinalize)
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestCurrentTaskExecutionsRequireExactDurableGeneration(t *testing.T) {
	taskID := "task-1"
	matched := sessionruntime.TaskExecution{
		Ref:    sessionruntime.WorkflowExecutionRef{TaskID: workflow.TaskID(taskID), RunID: "run-matched", Generation: 2},
		Script: &sessionruntime.TaskScriptExecutionTarget{Path: "/bin/true"},
	}
	staleGeneration := sessionruntime.TaskExecution{
		Ref:    sessionruntime.WorkflowExecutionRef{TaskID: workflow.TaskID(taskID), RunID: "run-stale", Generation: 1},
		Script: &sessionruntime.TaskScriptExecutionTarget{Path: "/bin/true"},
	}
	current, err := currentTaskExecutions(taskID, []sqlitegen.ListWorkflowTaskCurrentRunFactsRow{
		{ID: "run-matched", RunGeneration: 2},
		{ID: "run-stale", RunGeneration: 2},
	}, []sessionruntime.TaskExecution{staleGeneration, matched})
	if err != nil {
		t.Fatalf("currentTaskExecutions: %v", err)
	}
	if !reflect.DeepEqual(current, []sessionruntime.TaskExecution{matched}) {
		t.Fatalf("current executions = %+v, want exact generation match %+v", current, matched)
	}
}

func TestTaskDetailStatusCombinesDurableFactsWithExactLiveExecutions(t *testing.T) {
	live := sessionruntime.TaskExecution{
		Ref:    sessionruntime.WorkflowExecutionRef{TaskID: "task-1", RunID: "run-live", Generation: 2},
		Script: &sessionruntime.TaskScriptExecutionTarget{Path: "/bin/true"},
	}
	for _, test := range []struct {
		name       string
		durable    serverapi.WorkflowTaskStatus
		done       bool
		current    []sessionruntime.TaskExecution
		wantStatus serverapi.WorkflowTaskStatus
	}{
		{
			name:    "stale durable running becomes active",
			durable: statusForTest(serverapi.WorkflowTaskStatusKindRunning, []string{"run-stale"}, nil),
			wantStatus: statusForTest(
				serverapi.WorkflowTaskStatusKindActive,
				nil,
				nil,
			),
		},
		{
			name:    "stale durable queued becomes active",
			durable: statusForTest(serverapi.WorkflowTaskStatusKindQueued, []string{"run-queued"}, nil),
			wantStatus: statusForTest(
				serverapi.WorkflowTaskStatusKindActive,
				nil,
				nil,
			),
		},
		{
			name: "stale durable question removes question attention",
			durable: statusForTest(
				serverapi.WorkflowTaskStatusKindWaitingQuestion,
				[]string{"run-question"},
				[]serverapi.WorkflowTaskAttentionKind{
					serverapi.WorkflowTaskAttentionKindInterrupted,
					serverapi.WorkflowTaskAttentionKindQuestion,
				},
			),
			wantStatus: statusForTest(
				serverapi.WorkflowTaskStatusKindActive,
				nil,
				[]serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindInterrupted},
			),
		},
		{
			name: "exact pending question wins",
			durable: statusForTest(
				serverapi.WorkflowTaskStatusKindRunning,
				[]string{"run-live"},
				nil,
			),
			current: []sessionruntime.TaskExecution{func() sessionruntime.TaskExecution {
				value := live
				value.WaitingQuestion = true
				return value
			}()},
			wantStatus: statusForTest(
				serverapi.WorkflowTaskStatusKindWaitingQuestion,
				[]string{"run-live"},
				[]serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindQuestion},
			),
		},
		{
			name: "approval keeps precedence and unions run references",
			durable: statusForTest(
				serverapi.WorkflowTaskStatusKindWaitingApproval,
				[]string{"run-approval"},
				[]serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindApproval},
			),
			current: []sessionruntime.TaskExecution{live},
			wantStatus: statusForTest(
				serverapi.WorkflowTaskStatusKindWaitingApproval,
				[]string{"run-approval", "run-live"},
				[]serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindApproval},
			),
		},
		{
			name: "live execution wins over interrupted primary",
			durable: statusForTest(
				serverapi.WorkflowTaskStatusKindInterrupted,
				[]string{"run-interrupted"},
				[]serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindInterrupted},
			),
			current: []sessionruntime.TaskExecution{live},
			wantStatus: statusForTest(
				serverapi.WorkflowTaskStatusKindRunning,
				[]string{"run-live"},
				[]serverapi.WorkflowTaskAttentionKind{serverapi.WorkflowTaskAttentionKindInterrupted},
			),
		},
		{
			name:       "done keeps durable precedence",
			durable:    statusForTest(serverapi.WorkflowTaskStatusKindDone, nil, nil),
			done:       true,
			current:    []sessionruntime.TaskExecution{live},
			wantStatus: statusForTest(serverapi.WorkflowTaskStatusKindDone, nil, nil),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := taskDetailStatusFact(workflowTaskStatusFact{Status: test.durable, Done: test.done}, test.current)
			if !reflect.DeepEqual(got.Status, test.wantStatus) || got.Done != test.done {
				t.Fatalf("status fact = %+v, want status %+v done=%t", got, test.wantStatus, test.done)
			}
		})
	}
}

func TestCurrentTaskExecutionsRequiresDurableAndLiveQuestionEvidence(t *testing.T) {
	execution := sessionruntime.TaskExecution{
		Ref:             sessionruntime.WorkflowExecutionRef{TaskID: "task-1", RunID: "run-1", Generation: 1},
		Agent:           &sessionruntime.TaskAgentExecutionTarget{SessionID: runtimeids.NewSessionID()},
		WaitingQuestion: true,
	}
	for _, durableWaiting := range []bool{false, true} {
		rows := []sqlitegen.ListWorkflowTaskCurrentRunFactsRow{{
			ID:            "run-1",
			RunGeneration: 1,
			WaitingAskID:  sql.NullString{String: "ask-1", Valid: durableWaiting},
		}}
		current, err := currentTaskExecutions("task-1", rows, []sessionruntime.TaskExecution{execution})
		if err != nil {
			t.Fatalf("currentTaskExecutions durableWaiting=%t: %v", durableWaiting, err)
		}
		if len(current) != 1 || current[0].WaitingQuestion != durableWaiting {
			t.Fatalf("current executions durableWaiting=%t = %+v", durableWaiting, current)
		}
	}
}

func statusForTest(kind serverapi.WorkflowTaskStatusKind, runIDs []string, attention []serverapi.WorkflowTaskAttentionKind) serverapi.WorkflowTaskStatus {
	nativeState, ok := kind.NativeState()
	if !ok {
		panic("test status kind has no native state")
	}
	return serverapi.WorkflowTaskStatus{
		Kind:           kind,
		NativeState:    nativeState,
		RunIDs:         runIDs,
		AttentionTypes: attention,
	}
}
