package workflowview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

func TestAttentionProjectsPendingApprovalAndInterruptedCurrentNode(t *testing.T) {
	approvalFixture := newCurrentNodeViewFixture(t, true)
	approvalStarted := approvalFixture.startTask(t, "Approval task")
	completed, err := approvalFixture.store.CompleteCurrentNode(approvalFixture.ctx, workflowstore.CurrentNodeCompletionRequest{
		Source:       approvalStarted.currentNode,
		TransitionID: "done",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("CompleteCurrentNode did not create a pending Approval")
	}

	interruptedFixture := newCurrentNodeViewFixture(t, false)
	interruptedStarted := interruptedFixture.startTask(t, "Interrupted task")
	interruptedSessionID := interruptedFixture.bindCurrentNodeSession(t, interruptedStarted)
	if err := interruptedFixture.store.InterruptCurrentNode(
		interruptedFixture.ctx,
		interruptedStarted.currentNode,
		workflow.CurrentNodeInterruptionReason("server_restart"),
		workflow.CurrentNodeInterruptionDetail{Code: "restart", Fields: map[string]string{"error": "process stopped"}},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}

	approvalAttention := approvalFixture.attention(t)
	approvals, err := approvalAttention.ListTask(approvalFixture.ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(approvalStarted.task.ID)})
	if err != nil {
		t.Fatalf("Attention.ListTask approval: %v", err)
	}
	if len(approvals.Items) != 1 {
		t.Fatalf("approval attention = %+v, want one approval", approvals.Items)
	}
	approval := approvals.Items[0]
	if approval.Kind != "approval" ||
		approval.ApprovalID == nil ||
		*approval.ApprovalID != completed.PendingApproval.ID.String() ||
		approval.ApprovalSnapshot == nil ||
		approval.CurrentNode != nil {
		t.Fatalf("approval attention item = %+v, want pending Approval identity", approval)
	}
	requireAttentionMessageOmitted(t, approval)

	interruptedAttention := interruptedFixture.attention(t)
	interruptions, err := interruptedAttention.List(interruptedFixture.ctx, serverapi.WorkflowAttentionListRequest{PageSize: 20})
	if err != nil {
		t.Fatalf("Attention.List interrupted: %v", err)
	}
	if len(interruptions.Items) != 1 {
		t.Fatalf("interrupted attention = %+v, want one interrupted Current Node", interruptions.Items)
	}
	interrupted := interruptions.Items[0]
	if interrupted.Kind != "interrupted_current_node" ||
		interrupted.TaskID != string(interruptedStarted.task.ID) ||
		interrupted.CurrentNode == nil ||
		interrupted.CurrentNode.NodeID != string(interruptedFixture.agentNodeID) ||
		interrupted.CurrentNode.SessionID == nil ||
		*interrupted.CurrentNode.SessionID != interruptedSessionID.String() ||
		interrupted.SessionID == nil ||
		*interrupted.SessionID != interruptedSessionID.String() ||
		interrupted.DetailJSON == nil ||
		strings.TrimSpace(*interrupted.DetailJSON) == "" ||
		interrupted.ApprovalID != nil ||
		interrupted.QuestionID != nil {
		t.Fatalf("interrupted attention item = %+v, want Current Node identity", interrupted)
	}
	requireAttentionMessageOmitted(t, interrupted)
	taskInterruptions, err := interruptedAttention.ListTask(interruptedFixture.ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(interruptedStarted.task.ID)})
	if err != nil {
		t.Fatalf("Attention.ListTask interrupted: %v", err)
	}
	if len(taskInterruptions.Items) != 1 || taskInterruptions.Items[0].ID != interrupted.ID {
		t.Fatalf("task interrupted attention = %+v, want exact Current Node attention", taskInterruptions.Items)
	}
}

func requireAttentionMessageOmitted(t *testing.T, item serverapi.WorkflowAttentionItem) {
	t.Helper()
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal attention item: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode attention item fields: %v", err)
	}
	if _, exists := fields["message"]; exists {
		t.Fatalf("attention item serialized fallback message: %s", raw)
	}
}

func TestAttentionPaginatesDurableCurrentStateAndScopesTaskQuery(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, true)
	approvalTask := fixture.startTask(t, "Approval")
	completed, err := fixture.store.CompleteCurrentNode(fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
		Source:       approvalTask.currentNode,
		TransitionID: "done",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("pending Approval is missing")
	}
	firstInterrupted := fixture.startTask(t, "Interrupted first")
	secondInterrupted := fixture.startTask(t, "Interrupted second")
	for _, task := range []startedCurrentNodeViewTask{firstInterrupted, secondInterrupted} {
		if err := fixture.store.InterruptCurrentNode(
			fixture.ctx,
			task.currentNode,
			workflow.CurrentNodeInterruptionReason("server_restart"),
			workflow.CurrentNodeInterruptionDetail{Code: "restart"},
		); err != nil {
			t.Fatalf("InterruptCurrentNode %s: %v", task.task.ID, err)
		}
	}
	fixture.setApprovalCreatedAt(t, completed.PendingApproval.ID.String(), 3_000)
	fixture.setCurrentNodeInterruptedAt(t, firstInterrupted.currentNode, 2_000)
	fixture.setCurrentNodeInterruptedAt(t, secondInterrupted.currentNode, 1_000)
	attention := fixture.attention(t)

	first, err := attention.List(fixture.ctx, serverapi.WorkflowAttentionListRequest{PageSize: 2})
	if err != nil {
		t.Fatalf("Attention.List first: %v", err)
	}
	if len(first.Items) != 2 ||
		first.Items[0].Kind != "approval" ||
		first.Items[1].TaskID != string(firstInterrupted.task.ID) ||
		first.NextPageToken == "" {
		t.Fatalf("first attention page = %+v token %q", first.Items, first.NextPageToken)
	}
	second, err := attention.List(fixture.ctx, serverapi.WorkflowAttentionListRequest{
		PageSize:  2,
		PageToken: first.NextPageToken,
	})
	if err != nil {
		t.Fatalf("Attention.List second: %v", err)
	}
	if len(second.Items) != 1 ||
		second.Items[0].TaskID != string(secondInterrupted.task.ID) ||
		second.NextPageToken != "" {
		t.Fatalf("second attention page = %+v token %q", second.Items, second.NextPageToken)
	}
	scoped, err := attention.ListTask(fixture.ctx, serverapi.WorkflowTaskAttentionListRequest{
		TaskID: string(secondInterrupted.task.ID),
	})
	if err != nil {
		t.Fatalf("Attention.ListTask: %v", err)
	}
	if len(scoped.Items) != 1 || scoped.Items[0].TaskID != string(secondInterrupted.task.ID) {
		t.Fatalf("task-scoped attention = %+v", scoped.Items)
	}
	if _, err := attention.List(fixture.ctx, serverapi.WorkflowAttentionListRequest{
		PageSize:  2,
		PageToken: "invalid",
	}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("invalid attention page token error = %v, want invalid page token", err)
	}
}

func TestAttentionAndDetailProjectLiveQuestionFromExactScope(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Question")
	unrelated := fixture.startTask(t, "Unrelated")
	sessionID := fixture.bindCurrentNodeSession(t, started)
	authority, plan := fixture.newAgentAuthority(t)
	askID := uuid.NewString()
	request := tools.AskQuestionRequest{
		ID:                     askID,
		StepID:                 uuid.NewString(),
		Question:               "Proceed?",
		Suggestions:            []string{"Yes", "No"},
		RecommendedOptionIndex: 1,
	}
	lease, err := authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   fixture.binding.ProjectID,
		WorkflowID:  fixture.workflowID,
		CurrentNode: started.currentNode,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	lease.Release()
	handle, err := authority.StartAgentExecution(fixture.ctx, sessionruntime.AgentExecutionRequest{
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
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, err := authority.ResolvePendingWorkflowPrompt(started.currentNode.TaskID, askID)
		return err == nil
	}, "timed out waiting for live workflow Question")
	taskList, err := NewTaskList(
		fixture.metadata,
		mustDefinitionProjection(t, fixture.store),
		NewTaskProjector(),
		authority,
	)
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
	}
	projectID := fixture.binding.ProjectID
	limit := 20
	listed, err := taskList.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingQuestion},
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		Limit:       &limit,
	})
	if err != nil {
		t.Fatalf("TaskList.List waiting question: %v", err)
	}
	if len(listed.Tasks) != 1 ||
		listed.Tasks[0].TaskID != string(started.task.ID) ||
		listed.Tasks[0].Status.Kind != serverapi.WorkflowTaskStatusKindWaitingQuestion ||
		len(listed.Tasks[0].Status.AttentionTypes) != 1 ||
		listed.Tasks[0].Status.AttentionTypes[0] != serverapi.WorkflowTaskAttentionKindQuestion {
		t.Fatalf("task list waiting-question projection = %+v", listed)
	}
	prompts := currentNodeViewPrompts{bySession: map[string][]PendingPromptSnapshot{
		sessionID.String(): {{
			ID:                     askID,
			CreatedAt:              time.UnixMilli(4_000).UTC(),
			Question:               request.Question,
			Suggestions:            request.Suggestions,
			RecommendedOptionIndex: intPointer(request.RecommendedOptionIndex),
		}},
	}}
	attention, err := NewAttention(
		fixture.metadata,
		mustDefinitionProjection(t, fixture.store),
		authority,
		prompts,
	)
	if err != nil {
		t.Fatalf("NewAttention: %v", err)
	}
	taskAttention, err := attention.ListTask(fixture.ctx, serverapi.WorkflowTaskAttentionListRequest{
		TaskID: string(started.task.ID),
	})
	if err != nil {
		t.Fatalf("Attention.ListTask: %v", err)
	}
	if len(taskAttention.Items) != 1 ||
		taskAttention.Items[0].Kind != "question" ||
		taskAttention.Items[0].QuestionID == nil ||
		*taskAttention.Items[0].QuestionID != askID ||
		taskAttention.Items[0].Message == nil ||
		*taskAttention.Items[0].Message != request.Question ||
		taskAttention.Items[0].SessionID == nil ||
		*taskAttention.Items[0].SessionID != sessionID.String() ||
		taskAttention.Items[0].CurrentNode == nil ||
		taskAttention.Items[0].CurrentNode.NodeID != string(fixture.agentNodeID) {
		t.Fatalf("question attention = %+v", taskAttention.Items)
	}
	unrelatedAttention, err := attention.ListTask(fixture.ctx, serverapi.WorkflowTaskAttentionListRequest{
		TaskID: string(unrelated.task.ID),
	})
	if err != nil {
		t.Fatalf("Attention.ListTask unrelated: %v", err)
	}
	if len(unrelatedAttention.Items) != 0 {
		t.Fatalf("unrelated task attention = %+v, want none", unrelatedAttention.Items)
	}
	detail, err := NewTaskDetail(fixture.metadata, mustDefinitionProjection(t, fixture.store), NewTaskProjector(), authority, fixture.quiescence)
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	projected, err := detail.GetTask(fixture.ctx, string(started.task.ID))
	if err != nil {
		t.Fatalf("TaskDetail.GetTask: %v", err)
	}
	if projected.Status.Kind != serverapi.WorkflowTaskStatusKindWaitingQuestion ||
		projected.AttentionCount != 1 ||
		len(projected.LiveSessionIDs) != 1 ||
		projected.LiveSessionIDs[0] != sessionID.String() {
		t.Fatalf("question task detail = %+v", projected)
	}
	if err := authority.SubmitPromptResponse(sessionID, tools.AskQuestionResponse{
		RequestID: askID,
		Answer:    "Yes",
	}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse: %v", err)
	}
	if _, err := handle.Wait(fixture.ctx); err != nil {
		t.Fatalf("wait Question execution: %v", err)
	}
}
