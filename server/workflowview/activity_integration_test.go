package workflowview

import (
	"testing"

	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestTaskActivityListMergesDurableTaskEventsAndPaginatesStably(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	comment, err := workflowStore.AddComment(ctx, task.ID, "note", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if err := workflowStore.ReplaceComment(ctx, comment.ID, "edited note"); err != nil {
		t.Fatalf("ReplaceComment: %v", err)
	}
	claimed, err := workflowStore.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := workflowStore.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	if _, err := workflowStore.CancelTask(ctx, task.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE task_comments SET updated_at_unix_ms = 111 WHERE id = ?`, comment.ID); err != nil {
		t.Fatalf("force comment timestamp: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE task_runs SET started_at_unix_ms = 111, interrupted_at_unix_ms = 111, updated_at_unix_ms = 111 WHERE id = ?`, string(started.RunID)); err != nil {
		t.Fatalf("force run timestamp: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE tasks SET canceled_at_unix_ms = 111, updated_at_unix_ms = 111 WHERE id = ?`, string(task.ID)); err != nil {
		t.Fatalf("force task timestamp: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE task_transitions SET created_at_unix_ms = 111, applied_at_unix_ms = 111 WHERE task_id = ?`, string(task.ID)); err != nil {
		t.Fatalf("force transition timestamp: %v", err)
	}

	first, err := view.taskActivity(t).List(ctx, serverapi.WorkflowTaskActivityListRequest{TaskID: string(task.ID), PageSize: 2})
	if err != nil {
		t.Fatalf("ListTaskActivity first: %v", err)
	}
	newComment, err := workflowStore.AddComment(ctx, task.ID, "newer note", "user", "nek")
	if err != nil {
		t.Fatalf("AddComment newer: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE task_comments SET updated_at_unix_ms = 222 WHERE id = ?`, newComment.ID); err != nil {
		t.Fatalf("force newer comment timestamp: %v", err)
	}
	second, err := view.taskActivity(t).List(ctx, serverapi.WorkflowTaskActivityListRequest{TaskID: string(task.ID), PageSize: 10, PageToken: first.NextPageToken})
	if err != nil {
		t.Fatalf("ListTaskActivity second: %v", err)
	}
	seen := map[string]bool{}
	kinds := map[string]bool{}
	for _, item := range append(first.Items, second.Items...) {
		if seen[item.ActivityID] {
			t.Fatalf("duplicate activity item across pages: %s", item.ActivityID)
		}
		if item.ActivityID == "comment:"+newComment.ID {
			t.Fatalf("newer activity inserted between page fetches leaked into older page: %+v", item)
		}
		seen[item.ActivityID] = true
		kinds[item.Type] = true
	}
	for _, kind := range []string{"comment", "transition", "run_started", "run_interrupted", "task_canceled"} {
		if !kinds[kind] {
			t.Fatalf("activity kinds = %+v, missing %s; items=%+v/%+v", kinds, kind, first.Items, second.Items)
		}
	}
	if first.Items[0].OccurredAtUnixMs != 111 || first.Items[1].OccurredAtUnixMs != 111 || first.NextPageToken == "" {
		t.Fatalf("first page = %+v", first)
	}
}

func TestTaskActivityProjectsApprovalSnapshots(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	requireDoneTransitionApproval(t, ctx, store, workflowID)
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	pending, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done", Commentary: "needs approval", Actor: "agent"})
	if err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	resp, err := view.taskActivity(t).List(ctx, serverapi.WorkflowTaskActivityListRequest{TaskID: string(task.ID)})
	if err != nil {
		t.Fatalf("ListTaskActivity: %v", err)
	}
	var transition serverapi.WorkflowTaskTransition
	hasRunCompleted := false
	for _, item := range resp.Items {
		if item.Type == "run_completed" && item.Run != nil && item.Run.ID == string(started.RunID) {
			hasRunCompleted = true
		}
		if item.Type == "transition" && item.Transition != nil && item.Transition.ID == string(pending.TransitionID) {
			transition = *item.Transition
		}
	}
	if !hasRunCompleted {
		t.Fatalf("activity missing run_completed item: %+v", resp.Items)
	}
	if transition.ID == "" || transition.SourceNodeID == "" || transition.SourceNodeDisplayName != "Agent" || transition.TransitionDisplayName != "Done" || transition.WorkflowRevisionSeen == 0 || transition.Actor != "agent" || transition.Commentary != "needs approval" || transition.AppliedAtUnixMs != nil {
		t.Fatalf("transition snapshot = %+v", transition)
	}
	if len(transition.Edges) != 1 || !transition.Edges[0].RequiresApproval || transition.Edges[0].TargetNodeDisplayName == "" || len(transition.Edges[0].OutputRequirements) != 0 || transition.Edges[0].WorkflowRevisionSeen == 0 {
		t.Fatalf("edge snapshot = %+v", transition.Edges)
	}
}
