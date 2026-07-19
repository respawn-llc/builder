package workflowview

import (
	"context"
	"testing"

	"core/server/runtime"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestAttentionListProjectsApprovalQuestionAndInterruptedRun(t *testing.T) {
	ctx, store, workflowStore, binding := newWorkflowViewTestContextStore(t)
	view, err := newWorkflowViewTestFixture(store, workflowStore, staticTranscriptProvider{entries: map[string][]runtime.ChatEntry{
		"session-attention-question": transcriptEntriesWithAsk("ask-attention", "Attention ask?"),
	}}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	requireDoneTransitionApproval(t, ctx, store, workflowID)
	approvalTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Approval", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask approval: %v", err)
	}
	approvalStarted, err := workflowStore.StartTask(ctx, approvalTask.ID)
	if err != nil {
		t.Fatalf("StartTask approval: %v", err)
	}
	pendingApproval, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: approvalStarted.RunID, TransitionID: "done"})
	if err != nil {
		t.Fatalf("CompleteRun approval: %v", err)
	}
	if pendingApproval.State != "pending_approval" {
		t.Fatalf("approval completion = %+v, want pending_approval", pendingApproval)
	}
	questionTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Question", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask question: %v", err)
	}
	questionStarted, err := workflowStore.StartTask(ctx, questionTask.ID)
	if err != nil {
		t.Fatalf("StartTask question: %v", err)
	}
	questionClaimed, err := workflowStore.ClaimRun(ctx, questionStarted.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun question: %v", err)
	}
	sessionID := "session-attention-question"
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO sessions (id, project_id, workspace_id, artifact_relpath, name, first_prompt_preview, input_draft, previous_session_id, parent_agent_session_id, created_at_unix_ms, updated_at_unix_ms, last_sequence, model_request_count, launch_visible, cwd_relpath, continuation_json, locked_json, usage_state_json, metadata_json) VALUES (?, ?, ?, ?, '', '', '', NULL, NULL, 1, 1, 0, 0, 1, '.', '{}', '{}', '{}', '{}')`, sessionID, binding.ProjectID, binding.WorkspaceID, "sessions/"+sessionID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if err := workflowStore.AttachRunSession(ctx, questionStarted.RunID, questionClaimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession question: %v", err)
	}
	if err := workflowStore.SetRunWaitingAsk(ctx, questionStarted.RunID, questionClaimed.Generation, "ask-attention"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	interruptedTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Interrupted", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask interrupted: %v", err)
	}
	interruptedStarted, err := workflowStore.StartTask(ctx, interruptedTask.ID)
	if err != nil {
		t.Fatalf("StartTask interrupted: %v", err)
	}
	interruptedClaimed, err := workflowStore.ClaimRun(ctx, interruptedStarted.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun interrupted: %v", err)
	}
	if err := workflowStore.InterruptRunGeneration(ctx, interruptedStarted.RunID, interruptedClaimed.Generation, "manual", `{"error":"role missing"}`); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}

	resp, err := view.taskAttention(t).List(ctx, serverapi.WorkflowAttentionListRequest{})
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	kinds := map[string]serverapi.WorkflowAttentionItem{}
	for _, item := range resp.Items {
		kinds[item.Kind] = item
	}
	if kinds["approval"].TaskTransitionID != string(pendingApproval.TransitionID) || kinds["question"].AskID != "ask-attention" || kinds["interrupted_run"].TaskID != string(interruptedTask.ID) || kinds["interrupted_run"].RunID != string(interruptedStarted.RunID) || kinds["interrupted_run"].Message != "Run interrupted: manual: role missing" {
		t.Fatalf("attention items = %+v", resp.Items)
	}
	firstPage, err := view.taskAttention(t).List(ctx, serverapi.WorkflowAttentionListRequest{PageSize: 1})
	if err != nil {
		t.Fatalf("ListAttention first page: %v", err)
	}
	if len(firstPage.Items) != 1 || firstPage.NextPageToken == "" {
		t.Fatalf("first attention page = %+v, want one item and next token", firstPage)
	}
	secondPage, err := view.taskAttention(t).List(ctx, serverapi.WorkflowAttentionListRequest{PageSize: 1, PageToken: firstPage.NextPageToken})
	if err != nil {
		t.Fatalf("ListAttention second page: %v", err)
	}
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID == firstPage.Items[0].ID {
		t.Fatalf("second attention page = %+v first=%+v, want distinct next item", secondPage, firstPage)
	}
	taskResp, err := view.taskAttention(t).ListTask(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(questionTask.ID)})
	if err != nil {
		t.Fatalf("ListTaskAttention: %v", err)
	}
	if len(taskResp.Items) != 1 || taskResp.Items[0].Kind != "question" || taskResp.Items[0].TaskID != string(questionTask.ID) {
		t.Fatalf("task attention items = %+v", taskResp.Items)
	}
}

func TestCompletedPlacementQuestionRunIsExcludedFromTaskAndAttentionProjections(t *testing.T) {
	ctx, store, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Historical question", Body: "Body"})
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
	if err := workflowStore.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-historical"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE task_node_placements SET state = 'completed' WHERE id = ?`, string(started.PlacementID)); err != nil {
		t.Fatalf("seed completed-placement historical question run: %v", err)
	}

	detail, err := view.detail(t).GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if detail.Status.Kind == serverapi.WorkflowTaskStatusKindWaitingQuestion || len(detail.Status.RunIDs) != 0 || len(detail.Status.AttentionTypes) != 0 || detail.AttentionCount != 0 {
		t.Fatalf("detail = %+v, want no historical question state or attention", detail)
	}
	global, err := view.taskAttention(t).List(ctx, serverapi.WorkflowAttentionListRequest{})
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(global.Items) != 0 {
		t.Fatalf("global attention = %+v, want no historical question item", global.Items)
	}
	taskAttention, err := view.taskAttention(t).ListTask(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(task.ID)})
	if err != nil {
		t.Fatalf("ListTaskAttention: %v", err)
	}
	if len(taskAttention.Items) != 0 {
		t.Fatalf("task attention = %+v, want no historical question item", taskAttention.Items)
	}
	home, err := store.ListProjectHomeSummaries(ctx, binding.ProjectID, 1, 0)
	if err != nil {
		t.Fatalf("ListProjectHomeSummaries: %v", err)
	}
	if len(home) != 1 || home[0].AttentionCount != 0 {
		t.Fatalf("project home = %+v, want no historical question attention count", home)
	}
}

func TestAttentionListFillsPagePastDroppedCandidates(t *testing.T) {
	ctx, store, workflowStore, binding := newWorkflowViewTestContextStore(t)
	view, err := newWorkflowViewTestFixture(store, workflowStore, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	approvalWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, approvalWorkflowID, true); err != nil {
		t.Fatalf("LinkWorkflow approval: %v", err)
	}
	requireDoneTransitionApproval(t, ctx, store, approvalWorkflowID)
	approvalTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Approval", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask approval: %v", err)
	}
	approvalStarted, err := workflowStore.StartTask(ctx, approvalTask.ID)
	if err != nil {
		t.Fatalf("StartTask approval: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: approvalStarted.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun approval: %v", err)
	}

	// Two extra valid linked workflows produce validation_blocker candidates that
	// get dropped (they validate cleanly). Force them newest so they sort ahead
	// of the approval, spanning the first candidate fetch window.
	for i, title := range []string{"Clean A", "Clean B"} {
		cleanWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
		if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, cleanWorkflowID, false); err != nil {
			t.Fatalf("LinkWorkflow %s: %v", title, err)
		}
		if _, err := store.DB().ExecContext(ctx, `UPDATE workflows SET updated_at_unix_ms = ? WHERE id = ?`, int64(1_000_000_000_000+i), string(cleanWorkflowID)); err != nil {
			t.Fatalf("force clean workflow timestamp: %v", err)
		}
	}

	// With pageSize 1 the dropped candidates fill the first fetch; the page must
	// still surface the real approval item instead of coming back empty.
	page, err := view.taskAttention(t).List(ctx, serverapi.WorkflowAttentionListRequest{PageSize: 1})
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Kind != "approval" {
		t.Fatalf("attention page = %+v, want the approval item past dropped candidates", page.Items)
	}
	if page.NextPageToken == "" {
		t.Fatal("expected a next page token while candidates remain")
	}

}

func TestAttentionListExcludesNonActionableInterruptions(t *testing.T) {
	tests := []struct {
		name      string
		interrupt func(*testing.T, context.Context, *workflowstore.Store, workflowstore.TaskRecord, workflowstore.StartTaskResult, workflowstore.RunnableRunRecord)
	}{
		{
			name: "user initiated",
			interrupt: func(t *testing.T, ctx context.Context, store *workflowstore.Store, task workflowstore.TaskRecord, _ workflowstore.StartTaskResult, _ workflowstore.RunnableRunRecord) {
				if _, err := store.InterruptTaskRuns(ctx, task.ID, "", ""); err != nil {
					t.Fatalf("InterruptTaskRuns: %v", err)
				}
			},
		},
		{
			name: "blank reason",
			interrupt: func(t *testing.T, ctx context.Context, store *workflowstore.Store, _ workflowstore.TaskRecord, started workflowstore.StartTaskResult, claimed workflowstore.RunnableRunRecord) {
				if err := store.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, " \t ", "{}"); err != nil {
					t.Fatalf("InterruptRunGeneration: %v", err)
				}
			},
		},
		{
			name: "runtime canceled",
			interrupt: func(t *testing.T, ctx context.Context, store *workflowstore.Store, _ workflowstore.TaskRecord, started workflowstore.StartTaskResult, claimed workflowstore.RunnableRunRecord) {
				if err := store.InterruptRunGeneration(ctx, started.RunID, claimed.Generation, "workflow_runtime_canceled", "{}"); err != nil {
					t.Fatalf("InterruptRunGeneration: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
			workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
			if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
				t.Fatalf("LinkWorkflow: %v", err)
			}
			task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: tt.name, Body: "Body"})
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
			tt.interrupt(t, ctx, workflowStore, task, started, claimed)

			resp, err := view.taskAttention(t).List(ctx, serverapi.WorkflowAttentionListRequest{})
			if err != nil {
				t.Fatalf("ListAttention: %v", err)
			}
			for _, item := range resp.Items {
				if item.Kind == "interrupted_run" {
					t.Fatalf("non-actionable interruption surfaced as attention: %+v", resp.Items)
				}
			}
		})
	}
}
