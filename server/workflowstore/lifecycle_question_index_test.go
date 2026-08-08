package workflowstore

import (
	"testing"
	"time"

	"core/server/workflow"
	"core/shared/runtimeids"
)

func TestLifecycleQuestionIndexPreservesOrderAndImmutablePriorView(t *testing.T) {
	taskID := workflow.TaskID("task-questions")
	reference, err := workflow.NewCurrentNodeReference(taskID, "node-agent", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("CurrentNodeReference.Key: %v", err)
	}
	sessionID := runtimeids.NewSessionID()
	scopeID := runtimeids.NewExecutionScopeID()
	entry := lifecycleTaskEntry{
		runs: map[workflow.CurrentNodeReferenceKey]workflow.CurrentNodeReference{key: reference},
		exact: map[workflow.CurrentNodeReferenceKey]LifecycleExactExecution{
			key: {
				CurrentNode: reference,
				ScopeID:     scopeID,
				Agent:       &LifecycleAgentExecutionTarget{SessionID: sessionID},
				Phase:       LifecycleExactExecutionRunning,
				PendingPrompts: []LifecyclePendingPrompt{
					{ID: "newer-a", Kind: LifecyclePendingPromptQuestion, CreatedAt: time.UnixMilli(2_000), Question: "newer a"},
					{ID: "newer-b", Kind: LifecyclePendingPromptSessionApproval, CreatedAt: time.UnixMilli(2_000), Question: "newer b"},
					{ID: "older", Kind: LifecyclePendingPromptQuestion, CreatedAt: time.UnixMilli(1_000), Question: "older"},
				},
			},
		},
	}
	index, err := updateLifecycleQuestionIndex(nil, taskID, lifecycleTaskEntry{}, entry)
	if err != nil {
		t.Fatalf("updateLifecycleQuestionIndex: %v", err)
	}
	root := lifecycleRoot{taskID: entry}
	first, err := lifecycleQuestionPage(index, root, LifecycleQuestionCursor{}, 2)
	if err != nil {
		t.Fatalf("lifecycleQuestionPage first: %v", err)
	}
	if len(first) != 2 || first[0].Prompt.ID != "newer-b" || first[1].Prompt.ID != "newer-a" {
		t.Fatalf("first Question page = %+v", first)
	}
	cursorItemID, err := LifecycleQuestionItemID(first[1].SessionID, first[1].Prompt.ID)
	if err != nil {
		t.Fatalf("LifecycleQuestionItemID: %v", err)
	}
	cursor := LifecycleQuestionCursor{
		OccurredAtUnixMs: first[1].Prompt.CreatedAt.UnixMilli(),
		ItemID:           cursorItemID,
		HasValue:         true,
	}
	second, err := lifecycleQuestionPage(index, root, cursor, 2)
	if err != nil {
		t.Fatalf("lifecycleQuestionPage second: %v", err)
	}
	if len(second) != 1 || second[0].Prompt.ID != "older" {
		t.Fatalf("second Question page = %+v", second)
	}

	nextEntry := cloneLifecycleTaskEntry(entry)
	nextExact := cloneLifecycleExactExecution(nextEntry.exact[key])
	nextExact.PendingPrompts = nextExact.PendingPrompts[:2]
	nextEntry.exact[key] = nextExact
	nextIndex, err := updateLifecycleQuestionIndex(index, taskID, entry, nextEntry)
	if err != nil {
		t.Fatalf("remove indexed Question: %v", err)
	}
	oldPage, err := lifecycleQuestionPage(index, root, LifecycleQuestionCursor{}, 3)
	if err != nil || len(oldPage) != 3 {
		t.Fatalf("prior immutable Question page = %+v, err = %v", oldPage, err)
	}
	nextPage, err := lifecycleQuestionPage(nextIndex, lifecycleRoot{taskID: nextEntry}, LifecycleQuestionCursor{}, 3)
	if err != nil || len(nextPage) != 2 {
		t.Fatalf("next immutable Question page = %+v, err = %v", nextPage, err)
	}
}

func TestLifecycleQuestionIndexFailsWhenExactPromptCorrespondenceBreaks(t *testing.T) {
	taskID := workflow.TaskID("task-question-corrupt")
	reference, err := workflow.NewCurrentNodeReference(taskID, "node-agent", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	key, _ := reference.Key()
	entry := lifecycleTaskEntry{
		exact: map[workflow.CurrentNodeReferenceKey]LifecycleExactExecution{
			key: {
				CurrentNode: reference,
				ScopeID:     runtimeids.NewExecutionScopeID(),
				Agent:       &LifecycleAgentExecutionTarget{SessionID: runtimeids.NewSessionID()},
				Phase:       LifecycleExactExecutionRunning,
				PendingPrompts: []LifecyclePendingPrompt{{
					ID:        "question",
					Kind:      LifecyclePendingPromptQuestion,
					CreatedAt: time.UnixMilli(1_000),
					Question:  "question",
				}},
			},
		},
	}
	index, err := updateLifecycleQuestionIndex(nil, taskID, lifecycleTaskEntry{}, entry)
	if err != nil {
		t.Fatalf("updateLifecycleQuestionIndex: %v", err)
	}
	if _, err := lifecycleQuestionPage(index, lifecycleRoot{}, LifecycleQuestionCursor{}, 1); err == nil {
		t.Fatal("Question index accepted a missing canonical Exact prompt")
	}
}
