package workflowview

import (
	"context"
	"strings"
	"testing"
	"time"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestBoardDoneCardsIgnoreDeepHistoricalRunPayloads(t *testing.T) {
	const (
		historicalRunCount = 512
		historicalPayload  = 16 * 1024
	)

	ctx, metadataStore, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Done task",
		Body:      "Small card body",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{
		RunID:        started.RunID,
		TransitionID: "done",
	}); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	noLabelFilter := serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}
	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		ProjectID:   binding.ProjectID,
		LabelFilter: noLabelFilter,
	})
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	doneColumn := workflowViewColumnByKind(t, board, workflow.NodeKindTerminal)

	payload := `{"payload":"` + strings.Repeat("x", historicalPayload) + `"}`
	if _, err := metadataStore.DB().ExecContext(ctx, `
WITH RECURSIVE history(run_number) AS (
    SELECT 1
    UNION ALL
    SELECT run_number + 1
    FROM history
    WHERE run_number < ?
)
INSERT INTO task_runs (
    id,
    placement_id,
    run_generation,
    workflow_revision_seen,
    created_at_unix_ms,
    updated_at_unix_ms,
    started_at_unix_ms,
    completed_at_unix_ms,
    interruption_detail_json,
    invalid_completion_count,
    run_start_snapshot_json,
    metadata_json
)
SELECT
    printf('historical-run-%04d', run_number),
    ?,
    0,
    1,
    run_number,
    run_number,
    run_number,
    run_number,
    '{}',
    0,
    ?,
    ?
FROM history`, historicalRunCount, string(started.PlacementID), payload, payload); err != nil {
		t.Fatalf("insert historical runs: %v", err)
	}

	requestCtx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	page, err := view.board(t).ListNodeCards(requestCtx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:   binding.ProjectID,
		WorkflowID:  string(workflowID),
		NodeID:      doneColumn.Node.NodeID,
		LabelFilter: noLabelFilter,
	})
	if err != nil {
		t.Fatalf("ListNodeCards with irrelevant historical runs: %v", err)
	}
	if len(page.Cards) != 1 || page.Cards[0].TaskID != string(task.ID) {
		t.Fatalf("done page cards = %+v, want task %s", page.Cards, task.ID)
	}
}
