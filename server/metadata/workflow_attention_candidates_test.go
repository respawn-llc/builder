package metadata_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowstore"
)

func TestWorkflowAttentionCandidateRelationIsAuthoritative(t *testing.T) {
	ctx := context.Background()
	metadataStore, workflowStore, binding, now := newWorkflowAttentionCandidateStores(t)
	workflowID := createWorkflowAttentionCandidateWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}

	*now = time.UnixMilli(2_000)
	approvalTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Approval", Body: "Approval candidate."})
	if err != nil {
		t.Fatalf("CreateTask approval: %v", err)
	}
	approvalStarted, err := workflowStore.StartTask(ctx, approvalTask.ID)
	if err != nil {
		t.Fatalf("StartTask approval: %v", err)
	}
	approval, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: approvalStarted.RunID, TransitionID: "done"})
	if err != nil {
		t.Fatalf("CompleteRun approval: %v", err)
	}

	*now = time.UnixMilli(3_000)
	questionTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Question", Body: "Question candidate."})
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
	if err := workflowStore.SetRunWaitingAsk(ctx, questionStarted.RunID, questionClaimed.Generation, "ask-candidate"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}

	*now = time.UnixMilli(4_000)
	interruptedTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Interrupted", Body: "Interrupted candidate."})
	if err != nil {
		t.Fatalf("CreateTask interrupted: %v", err)
	}
	interruptedStarted, err := workflowStore.StartTask(ctx, interruptedTask.ID)
	if err != nil {
		t.Fatalf("StartTask interrupted: %v", err)
	}
	if _, err := workflowStore.ClaimRun(ctx, interruptedStarted.RunID, 0); err != nil {
		t.Fatalf("ClaimRun interrupted: %v", err)
	}
	if err := workflowStore.InterruptRun(ctx, interruptedStarted.RunID, "manual", `{"error":"stopped"}`); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}

	global, err := metadataStore.Queries().ListWorkflowAttentionCandidates(ctx, sqlitegen.ListWorkflowAttentionCandidatesParams{
		CursorActive:           0,
		CursorOccurredAtUnixMs: 0,
		CursorItemID:           "",
		PageLimit:              10,
	})
	if err != nil {
		t.Fatalf("ListWorkflowAttentionCandidates: %v", err)
	}
	if len(global) != 4 {
		t.Fatalf("global candidates = %+v, want four kinds", global)
	}
	wantKinds := []string{"interrupted_run", "question", "approval", "validation_blocker"}
	for index, wantKind := range wantKinds {
		if global[index].Kind != wantKind {
			t.Fatalf("global candidate %d = %+v, want kind %q", index, global[index], wantKind)
		}
	}
	requireNullAttentionCandidateIdentity(t, global[3].TaskID, "validation task")
	requireNullAttentionCandidateIdentity(t, global[3].RunID, "validation run")
	requireNullAttentionCandidateIdentity(t, global[3].TaskTransitionID, "validation transition")
	requireNullAttentionCandidateIdentity(t, global[2].AskID, "approval ask")
	requireNullAttentionCandidateIdentity(t, global[1].TaskTransitionID, "question transition")
	requireNullAttentionCandidateIdentity(t, global[0].TaskTransitionID, "interruption transition")
	requireAttentionCandidateIdentity(t, global[2].RunID, string(approvalStarted.RunID), "approval run")
	requireAttentionCandidateIdentity(t, global[2].TaskTransitionID, string(approval.TransitionID), "approval transition")

	taskCandidates, err := metadataStore.Queries().ListWorkflowTaskAttentionCandidates(ctx, string(questionTask.ID))
	if err != nil {
		t.Fatalf("ListWorkflowTaskAttentionCandidates: %v", err)
	}
	if len(taskCandidates) != 1 || taskCandidates[0].Kind != "question" {
		t.Fatalf("question task candidates = %+v", taskCandidates)
	}
	for _, taskID := range []workflow.TaskID{approvalTask.ID, questionTask.ID, interruptedTask.ID} {
		count, err := metadataStore.Queries().CountWorkflowTaskAttentionCandidates(ctx, string(taskID))
		if err != nil {
			t.Fatalf("CountWorkflowTaskAttentionCandidates %s: %v", taskID, err)
		}
		if count != 1 {
			t.Fatalf("task %s candidate count = %d, want 1", taskID, count)
		}
	}

	tx, err := metadataStore.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	transactionQueries := metadataStore.Queries().WithTx(tx)
	taskApprovalIDs, err := transactionQueries.ListPendingApprovalTransitionIDsByTask(ctx, string(approvalTask.ID))
	if err != nil {
		t.Fatalf("ListPendingApprovalTransitionIDsByTask: %v", err)
	}
	if len(taskApprovalIDs) != 1 || taskApprovalIDs[0] != string(approval.TransitionID) {
		t.Fatalf("task approval ids = %+v", taskApprovalIDs)
	}
	workflowApprovalIDs, err := transactionQueries.ListPendingApprovalTransitionIDsByWorkflow(ctx, string(workflowID))
	if err != nil {
		t.Fatalf("ListPendingApprovalTransitionIDsByWorkflow: %v", err)
	}
	if len(workflowApprovalIDs) != 1 || workflowApprovalIDs[0] != string(approval.TransitionID) {
		t.Fatalf("workflow approval ids = %+v", workflowApprovalIDs)
	}
	taskInterruptedIDs, err := transactionQueries.ListActionableInterruptedRunIDsByTask(ctx, string(interruptedTask.ID))
	if err != nil {
		t.Fatalf("ListActionableInterruptedRunIDsByTask: %v", err)
	}
	if len(taskInterruptedIDs) != 1 || taskInterruptedIDs[0] != string(interruptedStarted.RunID) {
		t.Fatalf("task interrupted ids = %+v", taskInterruptedIDs)
	}
	workflowInterruptedIDs, err := transactionQueries.ListActionableInterruptedRunIDsByWorkflow(ctx, string(workflowID))
	if err != nil {
		t.Fatalf("ListActionableInterruptedRunIDsByWorkflow: %v", err)
	}
	if len(workflowInterruptedIDs) != 1 || workflowInterruptedIDs[0] != string(interruptedStarted.RunID) {
		t.Fatalf("workflow interrupted ids = %+v", workflowInterruptedIDs)
	}

	approvalCandidate, err := transactionQueries.GetWorkflowApprovalAttentionCandidateByTransitionID(ctx, string(approval.TransitionID))
	if err != nil {
		t.Fatalf("GetWorkflowApprovalAttentionCandidateByTransitionID: %v", err)
	}
	requireAttentionCandidateIdentity(t, approvalCandidate.TaskID, string(approvalTask.ID), "approval task")
	requireAttentionCandidateIdentity(t, approvalCandidate.RunID, string(approvalStarted.RunID), "approval lookup run")
	interruptionCandidate, err := transactionQueries.GetWorkflowInterruptedRunAttentionCandidateByRunID(ctx, string(interruptedStarted.RunID))
	if err != nil {
		t.Fatalf("GetWorkflowInterruptedRunAttentionCandidateByRunID: %v", err)
	}
	requireAttentionCandidateIdentity(t, interruptionCandidate.TaskID, string(interruptedTask.ID), "interruption task")
	requireAttentionCandidateIdentity(t, interruptionCandidate.RunID, string(interruptedStarted.RunID), "interruption lookup run")
}

func newWorkflowAttentionCandidateStores(t *testing.T) (*metadata.Store, *workflowstore.Store, metadata.Binding, *time.Time) {
	t.Helper()
	metadataStore := testsetup.OpenStore(t, t.TempDir())
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if err := metadataStore.SetProjectKey(context.Background(), binding.ProjectID, "ATT"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	now := time.UnixMilli(1_000)
	workflowStore, err := workflowstore.New(
		metadataStore,
		workflowstore.WithNow(func() time.Time { return now }),
		workflowstore.WithRoleResolver(testsetup.QuestionsEnabled("coder")),
	)
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	return metadataStore, workflowStore, binding, &now
}

func createWorkflowAttentionCandidateWorkflow(t *testing.T, ctx context.Context, store *workflowstore.Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Attention"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := workflowAttentionCandidateNodeByKind(t, definition, workflow.NodeKindStart)
	done := workflowAttentionCandidateNodeByKind(t, definition, workflow.NodeKindTerminal)
	agentID := workflow.NodeID("node-attention-agent")
	if _, err := store.AddNode(ctx, workflowstore.NodeRecord{
		ID:             agentID,
		WorkflowID:     created.ID,
		Key:            "agent",
		Kind:           workflow.NodeKindAgent,
		DisplayName:    "Agent",
		SubagentRole:   "coder",
		PromptTemplate: "Do work.",
	}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{
		ID:           "transition-attention-start",
		WorkflowID:   created.ID,
		SourceNodeID: workflow.NodeIDOf(start),
		TransitionID: "start",
		DisplayName:  "Start",
	}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{
		ID:                "edge-attention-start",
		WorkflowID:        created.ID,
		TransitionGroupID: "transition-attention-start",
		Key:               "start",
		TargetNodeID:      agentID,
		ContextMode:       workflow.ContextModeNewSession,
		PromptTemplate:    "Do work.",
	}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{
		ID:           "transition-attention-done",
		WorkflowID:   created.ID,
		SourceNodeID: agentID,
		TransitionID: "done",
		DisplayName:  "Done",
	}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{
		ID:                "edge-attention-done",
		WorkflowID:        created.ID,
		TransitionGroupID: "transition-attention-done",
		Key:               "done",
		TargetNodeID:      workflow.NodeIDOf(done),
		ContextMode:       workflow.ContextModeNewSession,
		RequiresApproval:  true,
	}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	return created.ID
}

func workflowAttentionCandidateNodeByKind(t *testing.T, definition workflow.Definition, kind workflow.NodeKind) workflow.Node {
	t.Helper()
	for _, node := range definition.Nodes {
		if node.Kind() == kind {
			return node
		}
	}
	t.Fatalf("workflow node kind %q missing", kind)
	return nil
}

func requireNullAttentionCandidateIdentity(t *testing.T, value sql.NullString, field string) {
	t.Helper()
	if value.Valid {
		t.Fatalf("%s = %+v, want SQL NULL", field, value)
	}
}

func requireAttentionCandidateIdentity(t *testing.T, value sql.NullString, want string, field string) {
	t.Helper()
	if !value.Valid || value.String != want {
		t.Fatalf("%s = %+v, want %q", field, value, want)
	}
}
