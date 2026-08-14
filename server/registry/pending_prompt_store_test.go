package registry

import (
	"testing"
	"time"

	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

const pendingPromptSnapshotReadTimeout = time.Second

func TestPendingPromptListReturnsPublishedQuestionsAndApprovalsWhileMutationIsPaused(t *testing.T) {
	store := newPendingPromptStore()
	sessionID := runtimeids.NewSessionID().String()
	resource := registryTestResourceRef(sessionID)
	scopeID := runtimeids.NewExecutionScopeID()
	createdAt := time.Now().UTC()
	question := askquestion.AskQuestionRequest{
		ID:          "ask-1",
		StepID:      registryTestStepID,
		Question:    "Proceed?",
		Suggestions: []string{"Yes", "No"},
	}
	approval := askquestion.AskQuestionRequest{
		ID:       "approval-1",
		StepID:   registryTestStepID,
		Question: "Allow this operation?",
		Approval: true,
		ApprovalOptions: []askquestion.AskQuestionApprovalOption{
			{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
			{Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"},
		},
	}
	if _, admitted := store.Begin(sessionID, resource, scopeID, question, createdAt); !admitted {
		t.Fatal("question was not admitted")
	}
	if _, admitted := store.Begin(sessionID, resource, scopeID, approval, createdAt.Add(time.Second)); !admitted {
		t.Fatal("approval was not admitted")
	}

	store.mu.Lock()
	delete(store.pending[sessionID], approval.ID)
	result := make(chan []PendingPromptSnapshot, 1)
	go func() {
		result <- store.List(sessionID)
	}()

	var got []PendingPromptSnapshot
	select {
	case got = <-result:
		store.mu.Unlock()
	case <-time.After(pendingPromptSnapshotReadTimeout):
		store.mu.Unlock()
		<-result
		t.Fatal("pending Question/Approval list waited for a paused prompt mutation")
	}
	if len(got) != 2 {
		t.Fatalf("pending prompts during mutation = %+v, want prior question and approval", got)
	}
	if got[0].Request.ID != question.ID || got[0].Request.Approval {
		t.Fatalf("first pending prompt = %+v, want question %q", got[0], question.ID)
	}
	if got[1].Request.ID != approval.ID || !got[1].Request.Approval {
		t.Fatalf("second pending prompt = %+v, want approval %q", got[1], approval.ID)
	}
}

func TestPendingPromptListDoesNotAliasPublishedNestedCollections(t *testing.T) {
	store := newPendingPromptStore()
	sessionID := runtimeids.NewSessionID().String()
	resource := registryTestResourceRef(sessionID)
	scopeID := runtimeids.NewExecutionScopeID()
	workflowID := runtimeids.NewWorkflowID()
	currentNodeID := "node-1"
	request := askquestion.AskQuestionRequest{
		ID:          "ask-1",
		StepID:      registryTestStepID,
		Question:    "Proceed?",
		Suggestions: []string{"Yes", "No"},
		QuestionBatch: &askquestion.AskQuestionBatchMetadata{
			BatchPromptIDs: []string{"ask-1", "ask-2"},
		},
		AttentionTarget: &clientui.AttentionNotificationTarget{
			Kind:          clientui.AttentionNotificationTargetWorkflowTask,
			WorkflowID:    &workflowID,
			CurrentNodeID: &currentNodeID,
			Focus: &clientui.AttentionNotificationTaskDetailFocus{
				Kind:   clientui.AttentionNotificationFocusQuestion,
				AskIDs: []string{"ask-1", "ask-2"},
			},
		},
	}
	if _, admitted := store.Begin(sessionID, resource, scopeID, request, time.Now().UTC()); !admitted {
		t.Fatal("question was not admitted")
	}

	first := store.List(sessionID)
	if len(first) != 1 {
		t.Fatalf("first pending prompt list = %+v, want one question", first)
	}
	first[0].Request.Suggestions[0] = "mutated"
	first[0].Request.QuestionBatch.BatchPromptIDs[0] = "mutated"
	first[0].Request.AttentionTarget.Focus.AskIDs[0] = "mutated"
	*first[0].Request.AttentionTarget.CurrentNodeID = "mutated"
	*first[0].Request.AttentionTarget.WorkflowID = runtimeids.NewWorkflowID()
	first[0] = PendingPromptSnapshot{}

	second := store.List(sessionID)
	if len(second) != 1 {
		t.Fatalf("mutating returned list changed published catalog: %+v", second)
	}
	got := second[0].Request
	if len(got.Suggestions) != 2 || got.Suggestions[0] != "Yes" {
		t.Fatalf("published suggestions were aliased: %+v", got.Suggestions)
	}
	if got.QuestionBatch == nil || len(got.QuestionBatch.BatchPromptIDs) != 2 || got.QuestionBatch.BatchPromptIDs[0] != "ask-1" {
		t.Fatalf("published question batch was aliased: %+v", got.QuestionBatch)
	}
	if got.AttentionTarget == nil || got.AttentionTarget.Focus == nil ||
		len(got.AttentionTarget.Focus.AskIDs) != 2 ||
		got.AttentionTarget.Focus.AskIDs[0] != "ask-1" {
		t.Fatalf("published attention focus was aliased: %+v", got.AttentionTarget)
	}
	if got.AttentionTarget.CurrentNodeID == nil || *got.AttentionTarget.CurrentNodeID != "node-1" {
		t.Fatalf("published current Node was aliased: %+v", got.AttentionTarget.CurrentNodeID)
	}
	if got.AttentionTarget.WorkflowID == nil || *got.AttentionTarget.WorkflowID != workflowID {
		t.Fatalf("published Workflow identity was aliased: %+v", got.AttentionTarget.WorkflowID)
	}
}
