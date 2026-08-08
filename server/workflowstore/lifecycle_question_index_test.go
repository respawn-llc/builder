package workflowstore

import (
	"context"
	"os"
	"testing"
	"time"

	"core/server/workflow"
	"core/shared/runtimeids"
)

func TestLifecycleQuestionIndexPagesOrderedImmutableDiskSnapshots(t *testing.T) {
	ctx := context.Background()
	index, err := openLifecycleQuestionIndex(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("openLifecycleQuestionIndex: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeErr := index.close()
			if closeErr != nil {
				t.Errorf("close lifecycle Question index: %v", closeErr)
			}
		}
	})
	taskID, key, entry := lifecycleQuestionIndexFixture(t)
	if err := index.replaceTaskQuestions(ctx, taskID, lifecycleTaskEntry{}, entry); err != nil {
		t.Fatalf("replaceTaskQuestions: %v", err)
	}
	root := lifecycleRoot{taskID: entry}
	prior, err := index.beginRead(ctx)
	if err != nil {
		t.Fatalf("begin prior read: %v", err)
	}
	first, err := lifecycleQuestionPage(ctx, prior, root, LifecycleQuestionCursor{}, 2)
	if err != nil {
		t.Fatalf("lifecycleQuestionPage first: %v", err)
	}
	if len(first) != 2 || first[0].Prompt.ID != "newer-b" || first[1].Prompt.ID != "newer-a" {
		t.Fatalf("first Question page = %+v", first)
	}
	if _, err := lifecycleQuestionPage(ctx, prior, lifecycleRoot{}, LifecycleQuestionCursor{}, 1); err == nil {
		t.Fatal("Question index accepted a missing canonical Exact prompt")
	}
	cursorItemID, err := LifecycleQuestionItemID(first[1].SessionID, first[1].Prompt.ID)
	if err != nil {
		t.Fatalf("LifecycleQuestionItemID: %v", err)
	}
	second, err := lifecycleQuestionPage(ctx, prior, root, LifecycleQuestionCursor{
		OccurredAtUnixMs: first[1].Prompt.CreatedAt.UnixMilli(),
		ItemID:           cursorItemID,
		HasValue:         true,
	}, 2)
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
	if err := index.replaceTaskQuestions(ctx, taskID, entry, nextEntry); err != nil {
		t.Fatalf("replace next Questions: %v", err)
	}
	oldPage, err := lifecycleQuestionPage(ctx, prior, root, LifecycleQuestionCursor{}, 3)
	if err != nil || len(oldPage) != 3 {
		t.Fatalf("prior immutable Question page = %+v, err = %v", oldPage, err)
	}
	if err := prior.close(); err != nil {
		t.Fatalf("close prior read: %v", err)
	}
	next, err := index.beginRead(ctx)
	if err != nil {
		t.Fatalf("begin next read: %v", err)
	}
	nextPage, err := lifecycleQuestionPage(
		ctx,
		next,
		lifecycleRoot{taskID: nextEntry},
		LifecycleQuestionCursor{},
		3,
	)
	if err != nil || len(nextPage) != 2 {
		t.Fatalf("next immutable Question page = %+v, err = %v", nextPage, err)
	}
	if err := next.close(); err != nil {
		t.Fatalf("close next read: %v", err)
	}
	path := index.path
	if err := index.close(); err != nil {
		t.Fatalf("close lifecycle Question index: %v", err)
	}
	closed = true
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lifecycle Question index path remains after close: %v", err)
	}
}

func TestLifecycleQuestionIndexFailureLeavesCanonicalRootUnchanged(t *testing.T) {
	ctx := context.Background()
	index, err := openLifecycleQuestionIndex(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("openLifecycleQuestionIndex: %v", err)
	}
	t.Cleanup(func() { _ = index.close() })
	taskID, key, entry := lifecycleQuestionIndexFixture(t)
	publication := &LifecyclePublication{
		store:         &Store{},
		root:          lifecycleRoot{taskID: entry},
		questionIndex: index,
	}
	exact := entry.exact[key]
	promptID := exact.PendingPrompts[0].ID
	if err := publication.PublishExactPromptResolved(ctx, exact.ScopeID, promptID); err == nil {
		t.Fatal("prompt resolution succeeded with a missing derived index row")
	}
	published := publication.root[taskID].exact[key]
	if len(published.PendingPrompts) != len(exact.PendingPrompts) ||
		published.PendingPrompts[0].ID != promptID {
		t.Fatalf("canonical Exact prompt changed after index failure: %+v", published.PendingPrompts)
	}
}

func lifecycleQuestionIndexFixture(
	t *testing.T,
) (workflow.TaskID, workflow.CurrentNodeReferenceKey, lifecycleTaskEntry) {
	t.Helper()
	taskID := workflow.TaskID("task-questions")
	reference, err := workflow.NewCurrentNodeReference(taskID, "node-agent", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("CurrentNodeReference.Key: %v", err)
	}
	entry := lifecycleTaskEntry{
		runs: map[workflow.CurrentNodeReferenceKey]workflow.CurrentNodeReference{key: reference},
		exact: map[workflow.CurrentNodeReferenceKey]LifecycleExactExecution{
			key: {
				CurrentNode: reference,
				ScopeID:     runtimeids.NewExecutionScopeID(),
				Agent:       &LifecycleAgentExecutionTarget{SessionID: runtimeids.NewSessionID()},
				Phase:       LifecycleExactExecutionRunning,
				PendingPrompts: []LifecyclePendingPrompt{
					{ID: "newer-a", Kind: LifecyclePendingPromptQuestion, CreatedAt: time.UnixMilli(2_000), Question: "newer a"},
					{ID: "newer-b", Kind: LifecyclePendingPromptSessionApproval, CreatedAt: time.UnixMilli(2_000), Question: "newer b"},
					{ID: "older", Kind: LifecyclePendingPromptQuestion, CreatedAt: time.UnixMilli(1_000), Question: "older"},
				},
			},
		},
	}
	return taskID, key, entry
}
