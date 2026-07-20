package workflowview

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
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
	handles := make([]sessionruntime.ExecutionHandle, 0, 2)
	for _, runID := range []workflow.RunID{"run-b", "run-a"} {
		handle, err := authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
			Workflow: &sessionruntime.WorkflowExecutionRef{
				TaskID:     task.ID,
				RunID:      runID,
				Generation: 1,
			},
			Command: sessionruntime.ScriptCommand{
				Path:              sleepPath,
				Args:              []string{"30"},
				CancellationGrace: &cancellationGrace,
			},
		})
		if err != nil {
			t.Fatalf("StartScriptExecution %s: %v", runID, err)
		}
		handles = append(handles, handle)
	}
	t.Cleanup(func() {
		for _, handle := range handles {
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
	if len(live.CurrentSessionIDs) != 0 || len(live.CurrentScripts) != 2 ||
		live.CurrentScripts[0].RunID != "run-a" ||
		live.CurrentScripts[1].RunID != "run-b" ||
		live.CurrentScripts[0].Path != sleepPath ||
		live.CurrentScripts[1].Path != sleepPath {
		t.Fatalf("live targets = sessions %v scripts %+v", live.CurrentSessionIDs, live.CurrentScripts)
	}

	for _, handle := range handles {
		if err := handle.Stop(context.Background()); err != nil {
			t.Fatalf("stop script: %v", err)
		}
	}
	handles = nil
	stopped, err := detail.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask stopped: %v", err)
	}
	if stopped.Actions.CanInterrupt || !stopped.Actions.CanStart ||
		len(stopped.CurrentScripts) != 0 || stopped.CurrentScripts == nil {
		t.Fatalf("stopped detail = %+v", stopped)
	}
}
