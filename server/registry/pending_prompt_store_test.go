package registry

import (
	"testing"
	"time"

	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

func TestPendingPromptPublishedCatalogLifecycle(t *testing.T) {
	store := newPendingPromptStore()
	sessionID := runtimeids.NewSessionID().String()
	resource := registryTestResourceRef(sessionID)
	scopeID := runtimeids.NewExecutionScopeID()
	workflowID := runtimeids.NewWorkflowID()
	nodeID := "node-1"
	question := askquestion.AskQuestionRequest{
		ID: "ask-1", StepID: registryTestStepID, Question: "Proceed?", Suggestions: []string{"Yes", "No"},
		AttentionTarget: &clientui.AttentionNotificationTarget{
			Kind: clientui.AttentionNotificationTargetWorkflowTask, WorkflowID: &workflowID, CurrentNodeID: &nodeID,
			Focus: &clientui.AttentionNotificationTaskDetailFocus{Kind: clientui.AttentionNotificationFocusQuestion, AskIDs: []string{"ask-1"}},
		},
	}
	approval := askquestion.AskQuestionRequest{ID: "approval-1", StepID: registryTestStepID, Question: "Allow?", Approval: true}
	createdAt := time.Now().UTC()
	_, questionOK := store.Begin(sessionID, resource, scopeID, question, createdAt)
	_, approvalOK := store.Begin(sessionID, resource, scopeID, approval, createdAt.Add(time.Second))
	if !questionOK || !approvalOK {
		t.Fatal("prompts were not admitted")
	}

	first := store.List(sessionID)
	if len(first) != 2 || first[0].Request.ID != question.ID || first[1].Request.ID != approval.ID {
		t.Fatalf("published prompts = %+v, want ordered Question and Approval", first)
	}

	store.mu.Lock()
	saved := store.pending[sessionID][approval.ID]
	delete(store.pending[sessionID], approval.ID)
	read := make(chan []PendingPromptSnapshot, 1)
	go func() { read <- store.List(sessionID) }()
	select {
	case got := <-read:
		store.pending[sessionID][approval.ID] = saved
		store.mu.Unlock()
		if len(got) != 2 {
			t.Fatalf("prompt read during mutation = %+v, want prior publication", got)
		}
	case <-time.After(time.Second):
		store.pending[sessionID][approval.ID] = saved
		store.mu.Unlock()
		t.Fatal("prompt read waited for mutation")
	}

	if _, ok := store.Complete(sessionID, resource, scopeID, approval.ID); !ok {
		t.Fatal("approval was not completed")
	}
	if got := store.List(sessionID); len(got) != 1 || got[0].Request.ID != question.ID {
		t.Fatalf("published prompts after Complete = %+v, want Question", got)
	}
	var closed []PendingPromptSnapshot
	store.CloseSession(sessionID, func(prompt PendingPromptSnapshot) { closed = append(closed, prompt) })
	if got := store.List(sessionID); got != nil || len(closed) != 1 || closed[0].Request.ID != question.ID {
		t.Fatalf("Session close publication = %+v resolved=%+v", got, closed)
	}
}

func TestPendingPromptListDoesNotAliasPublishedNestedCollections(t *testing.T) {
	store := newPendingPromptStore()
	sessionID := runtimeids.NewSessionID().String()
	workflowID := runtimeids.NewWorkflowID()
	nodeID := "node-1"
	branchKey := "branch-1"
	request := askquestion.AskQuestionRequest{
		ID:          "ask-1",
		StepID:      registryTestStepID,
		Question:    "Proceed?",
		Suggestions: []string{"Yes", "No"},
		ApprovalOptions: []askquestion.AskQuestionApprovalOption{{
			Decision: askquestion.AskQuestionApprovalDecisionAllowOnce,
			Label:    "Allow",
		}},
		QuestionBatch: &askquestion.AskQuestionBatchMetadata{
			PromptID:       "ask-1",
			BatchPromptIDs: []string{"ask-1", "ask-2"},
		},
		AttentionTarget: &clientui.AttentionNotificationTarget{
			Kind:                 clientui.AttentionNotificationTargetWorkflowTask,
			ProjectID:            "project-1",
			WorkflowID:           &workflowID,
			CurrentNodeID:        &nodeID,
			CurrentNodeBranchKey: &branchKey,
			Focus: &clientui.AttentionNotificationTaskDetailFocus{
				Kind:   clientui.AttentionNotificationFocusQuestion,
				AskIDs: []string{"ask-1"},
			},
		},
	}
	if _, ok := store.Begin(
		sessionID,
		registryTestResourceRef(sessionID),
		runtimeids.NewExecutionScopeID(),
		request,
		time.Now().UTC(),
	); !ok {
		t.Fatal("prompt was not admitted")
	}

	tests := []struct {
		name   string
		mutate func([]PendingPromptSnapshot)
		assert func(*testing.T, []PendingPromptSnapshot)
	}{
		{
			name:   "catalog_slice",
			mutate: func(items []PendingPromptSnapshot) { items[0] = PendingPromptSnapshot{} },
			assert: func(t *testing.T, items []PendingPromptSnapshot) {
				if len(items) != 1 || items[0].Request.ID != request.ID {
					t.Fatalf("published catalog was aliased: %+v", items)
				}
			},
		},
		{
			name:   "suggestions",
			mutate: func(items []PendingPromptSnapshot) { items[0].Request.Suggestions[0] = "mutated" },
			assert: func(t *testing.T, items []PendingPromptSnapshot) {
				if items[0].Request.Suggestions[0] != "Yes" {
					t.Fatalf("published suggestions were aliased: %+v", items[0].Request.Suggestions)
				}
			},
		},
		{
			name: "approval_options",
			mutate: func(items []PendingPromptSnapshot) {
				items[0].Request.ApprovalOptions[0].Label = "mutated"
			},
			assert: func(t *testing.T, items []PendingPromptSnapshot) {
				if items[0].Request.ApprovalOptions[0].Label != "Allow" {
					t.Fatalf("published approval options were aliased: %+v", items[0].Request.ApprovalOptions)
				}
			},
		},
		{
			name: "question_batch_pointer_and_prompt_ids",
			mutate: func(items []PendingPromptSnapshot) {
				items[0].Request.QuestionBatch.PromptID = "mutated"
				items[0].Request.QuestionBatch.BatchPromptIDs[0] = "mutated"
			},
			assert: func(t *testing.T, items []PendingPromptSnapshot) {
				batch := items[0].Request.QuestionBatch
				if batch == nil || batch.PromptID != "ask-1" || batch.BatchPromptIDs[0] != "ask-1" {
					t.Fatalf("published Question batch was aliased: %+v", batch)
				}
			},
		},
		{
			name: "attention_target",
			mutate: func(items []PendingPromptSnapshot) {
				items[0].Request.AttentionTarget.ProjectID = "mutated"
			},
			assert: func(t *testing.T, items []PendingPromptSnapshot) {
				if items[0].Request.AttentionTarget == nil ||
					items[0].Request.AttentionTarget.ProjectID != "project-1" {
					t.Fatalf("published attention target was aliased: %+v", items[0].Request.AttentionTarget)
				}
			},
		},
		{
			name: "attention_target_workflow_id",
			mutate: func(items []PendingPromptSnapshot) {
				*items[0].Request.AttentionTarget.WorkflowID = runtimeids.NewWorkflowID()
			},
			assert: func(t *testing.T, items []PendingPromptSnapshot) {
				target := items[0].Request.AttentionTarget
				if target == nil || target.WorkflowID == nil || *target.WorkflowID != workflowID {
					t.Fatalf("published Workflow ID was aliased: %+v", target)
				}
			},
		},
		{
			name: "attention_target_current_node_id",
			mutate: func(items []PendingPromptSnapshot) {
				*items[0].Request.AttentionTarget.CurrentNodeID = "mutated"
			},
			assert: func(t *testing.T, items []PendingPromptSnapshot) {
				target := items[0].Request.AttentionTarget
				if target == nil || target.CurrentNodeID == nil || *target.CurrentNodeID != nodeID {
					t.Fatalf("published Current Node ID was aliased: %+v", target)
				}
			},
		},
		{
			name: "attention_target_current_node_branch_key",
			mutate: func(items []PendingPromptSnapshot) {
				*items[0].Request.AttentionTarget.CurrentNodeBranchKey = "mutated"
			},
			assert: func(t *testing.T, items []PendingPromptSnapshot) {
				target := items[0].Request.AttentionTarget
				if target == nil || target.CurrentNodeBranchKey == nil ||
					*target.CurrentNodeBranchKey != branchKey {
					t.Fatalf("published Current Node branch key was aliased: %+v", target)
				}
			},
		},
		{
			name: "attention_target_focus",
			mutate: func(items []PendingPromptSnapshot) {
				items[0].Request.AttentionTarget.Focus.Kind = clientui.AttentionNotificationFocusApproval
			},
			assert: func(t *testing.T, items []PendingPromptSnapshot) {
				target := items[0].Request.AttentionTarget
				if target == nil || target.Focus == nil ||
					target.Focus.Kind != clientui.AttentionNotificationFocusQuestion {
					t.Fatalf("published attention focus was aliased: %+v", target)
				}
			},
		},
		{
			name: "attention_target_focus_ask_ids",
			mutate: func(items []PendingPromptSnapshot) {
				items[0].Request.AttentionTarget.Focus.AskIDs[0] = "mutated"
			},
			assert: func(t *testing.T, items []PendingPromptSnapshot) {
				target := items[0].Request.AttentionTarget
				if target == nil || target.Focus == nil || target.Focus.AskIDs[0] != "ask-1" {
					t.Fatalf("published attention focus Ask IDs were aliased: %+v", target)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			items := store.List(sessionID)
			test.mutate(items)
			test.assert(t, store.List(sessionID))
		})
	}
}
