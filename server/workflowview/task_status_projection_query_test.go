package workflowview

import (
	"context"
	"encoding/json"
	"testing"

	"core/server/metadata/sqlitegen"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestCanonicalWorkflowTaskStatusProjectionUsesExactLiveAuthority(t *testing.T) {
	ctx, store, workflowStore, binding := newWorkflowViewTestContextStore(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Canonical status",
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

	noAuthority := canonicalWorkflowTaskStatusForTest(t, ctx, store.Queries(), string(task.ID), nil, nil)
	if noAuthority.Kind != string(serverapi.WorkflowTaskStatusKindActive) {
		t.Fatalf("durable running without authority kind = %q, want active", noAuthority.Kind)
	}

	currentRunFacts := []canonicalTaskStatusCurrentRunFact{{
		TaskID: string(task.ID), RunID: string(started.RunID), Generation: claimed.Generation,
	}}
	exactAuthority := []canonicalTaskStatusAuthorityObservation{{
		TaskID: string(task.ID), RunID: string(started.RunID), Generation: claimed.Generation,
	}}
	running := canonicalWorkflowTaskStatusForTest(t, ctx, store.Queries(), string(task.ID), exactAuthority, currentRunFacts)
	if running.Kind != string(serverapi.WorkflowTaskStatusKindRunning) {
		t.Fatalf("durable running with exact authority kind = %q, want running", running.Kind)
	}

	if err := workflowStore.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-1"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	questionDurableFacts := []canonicalTaskStatusCurrentRunFact{{
		TaskID: string(task.ID), RunID: string(started.RunID), Generation: claimed.Generation, WaitingQuestion: true,
	}}
	questionDisagrees := canonicalWorkflowTaskStatusForTest(t, ctx, store.Queries(), string(task.ID), exactAuthority, questionDurableFacts)
	if questionDisagrees.Kind != string(serverapi.WorkflowTaskStatusKindRunning) {
		t.Fatalf("durable/live question disagreement kind = %q, want running", questionDisagrees.Kind)
	}
	exactAuthority[0].WaitingQuestion = true
	waitingQuestion := canonicalWorkflowTaskStatusForTest(t, ctx, store.Queries(), string(task.ID), exactAuthority, questionDurableFacts)
	if waitingQuestion.Kind != string(serverapi.WorkflowTaskStatusKindWaitingQuestion) {
		t.Fatalf("matching durable/live question kind = %q, want waiting_question", waitingQuestion.Kind)
	}
}

type canonicalTaskStatusAuthorityObservation struct {
	TaskID          string `json:"task_id"`
	RunID           string `json:"run_id"`
	Generation      int64  `json:"generation"`
	WaitingQuestion bool   `json:"waiting_question"`
}

type canonicalTaskStatusCurrentRunFact struct {
	TaskID          string `json:"task_id"`
	RunID           string `json:"run_id"`
	Generation      int64  `json:"generation"`
	WaitingQuestion bool   `json:"waiting_question"`
}

func canonicalWorkflowTaskStatusForTest(
	t *testing.T,
	ctx context.Context,
	queries *sqlitegen.Queries,
	taskID string,
	authority []canonicalTaskStatusAuthorityObservation,
	currentRunFacts []canonicalTaskStatusCurrentRunFact,
) sqlitegen.GetCanonicalWorkflowTaskStatusRecordRow {
	t.Helper()
	encodedAuthority, err := json.Marshal(authority)
	if err != nil {
		t.Fatalf("marshal authority observations: %v", err)
	}
	encodedCurrentRunFacts, err := json.Marshal(currentRunFacts)
	if err != nil {
		t.Fatalf("marshal current run facts: %v", err)
	}
	status, err := queries.GetCanonicalWorkflowTaskStatusRecord(ctx, sqlitegen.GetCanonicalWorkflowTaskStatusRecordParams{
		TaskID:                    taskID,
		AuthorityObservationsJson: string(encodedAuthority),
		CurrentRunFactsJson:       string(encodedCurrentRunFacts),
	})
	if err != nil {
		t.Fatalf("GetCanonicalWorkflowTaskStatusRecord: %v", err)
	}
	return status
}
