package workflowview

import (
	"reflect"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/server/worktree"
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
	comment, err := workflowStore.AddComment(ctx, task.ID, "Task detail comment", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}

	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	detail, err := NewTaskDetail(metadataStore, definitions, NewTaskProjector(), worktree.NewGitInspector(nil))
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
	if len(byID.Placements) == 0 || len(byID.Runs) != 1 || len(byID.Transitions) != 2 {
		t.Fatalf("task execution detail is incomplete: %+v", byID)
	}
	if len(byID.Comments) != 1 || byID.Comments[0].ID != comment.ID {
		t.Fatalf("task comments = %+v, want %q", byID.Comments, comment.ID)
	}
	if byID.SourceWorkspace.WorkspaceID != sourceWorkspace.WorkspaceID {
		t.Fatalf("source workspace = %+v, want %q", byID.SourceWorkspace, sourceWorkspace.WorkspaceID)
	}
	if byID.ExecutionTarget == nil ||
		byID.ExecutionTarget.Mode != "none" ||
		byID.ExecutionTarget.EffectiveRoot == nil ||
		*byID.ExecutionTarget.EffectiveRoot != sourceWorkspace.CanonicalRoot {
		t.Fatalf("execution target = %+v, want none at %q", byID.ExecutionTarget, sourceWorkspace.CanonicalRoot)
	}
	if byID.AttentionCount != 1 {
		t.Fatalf("attention count = %d, want 1", byID.AttentionCount)
	}
}
