package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

func TestExecutionPromptStoreResolvePromptBatchValidatesAllMatchingEntriesBeforeMutation(t *testing.T) {
	store, feed := newPromptBatchStore(t)
	stepID := promptBatchStepID(t)
	question := promptBatchQuestion("question", stepID, time.Unix(1, 0))
	approval := promptBatchApproval("approval", stepID, time.Unix(2, 0))
	installPromptBatchEntries(&store, question, approval)
	selected := 1

	_, err := store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{
		promptQuestionAnswer("question", &selected, nil),
		promptApprovalAnswer("approval", tools.AskQuestionApprovalDecisionAllowSession, nil),
	})
	if err == nil {
		t.Fatal("batch with unoffered approval decision unexpectedly succeeded")
	}
	if !store.hasPendingID("question") || !store.hasPendingID("approval") {
		t.Fatal("invalid batch mutated a valid sibling")
	}
	if got := feed.resolvedIDs(); len(got) != 0 {
		t.Fatalf("invalid batch published resolutions: %v", got)
	}

	_, err = store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{
		promptApprovalAnswer("question", tools.AskQuestionApprovalDecisionAllowOnce, nil),
		promptApprovalAnswer("approval", tools.AskQuestionApprovalDecisionAllowOnce, nil),
	})
	if err == nil {
		t.Fatal("batch with wrong prompt kind unexpectedly succeeded")
	}
	if !store.hasPendingID("question") || !store.hasPendingID("approval") {
		t.Fatal("wrong-kind batch mutated a sibling")
	}
}

func TestAuthorityResolvePromptBatchRejectsMissingInternalUnionVariantBeforeStaleEvaluation(t *testing.T) {
	authority := NewAuthority(AuthorityOptions{})
	_, err := authority.ResolvePromptBatch(
		context.Background(),
		runtimeids.NewSessionID(),
		promptBatchStepID(t),
		[]PromptAnswerCommand{{
			ToolCallID: "prompt-1",
		}},
	)
	if err == nil {
		t.Fatal("invalid internal command union was treated as stale")
	}
}

func TestExecutionPromptStoreResolvePromptBatchResolvesMixedEntriesInCanonicalOrder(t *testing.T) {
	stepID := promptBatchStepID(t)
	canonical := []*executionPromptEntry{
		promptBatchQuestion("question-b", stepID, time.Unix(1, 0)),
		promptBatchApproval("approval", stepID, time.Unix(2, 0)),
		promptBatchQuestion("question-a", stepID, time.Unix(2, 0)),
	}
	omitted := promptBatchQuestion("omitted", stepID, time.Unix(3, 0))
	selected := 1
	permutations := [][]PromptAnswerCommand{
		{
			promptDeclined("question-a"),
			promptQuestionAnswer("question-b", &selected, nil),
			promptApprovalAnswer("approval", tools.AskQuestionApprovalDecisionAllowOnce, promptBatchText("approved")),
		},
		{
			promptApprovalAnswer("approval", tools.AskQuestionApprovalDecisionAllowOnce, promptBatchText("approved")),
			promptQuestionAnswer("question-b", &selected, nil),
			promptDeclined("question-a"),
		},
		{
			promptQuestionAnswer("question-b", &selected, nil),
			promptDeclined("question-a"),
			promptApprovalAnswer("approval", tools.AskQuestionApprovalDecisionAllowOnce, promptBatchText("approved")),
		},
	}

	for index, commands := range permutations {
		t.Run(fmt.Sprintf("permutation-%d", index), func(t *testing.T) {
			store, feed := newPromptBatchStore(t)
			entries := clonePromptBatchEntries(canonical...)
			installedOmitted := clonePromptBatchEntries(omitted)[0]
			entries[1].response, entries[1].approval = make(chan executionPromptResult), newApprovalPromptLifecycle()
			installPromptBatchEntries(&store, append(entries, installedOmitted)...)
			accepted := make(chan executionPromptResult, 1)
			go func() { accepted <- store.acceptApproval(entries[1], <-entries[1].response) }()

			results, err := store.ResolvePromptBatch(context.Background(), stepID, commands)
			if err != nil {
				t.Fatalf("ResolvePromptBatch: %v", err)
			}
			requirePromptBatchResultSet(t, results, map[clientui.ToolCallID]PromptAnswerOutcome{
				"question-a": PromptAnswerOutcomeResolved,
				"question-b": PromptAnswerOutcomeResolved,
				"approval":   PromptAnswerOutcomeResolved,
			})
			if got, want := feed.resolvedIDs(), []string{"question-b", "approval", "question-a"}; !equalPromptBatchStrings(got, want) {
				t.Fatalf("resolved order = %v, want %v", got, want)
			}
			if !store.hasPendingID("omitted") {
				t.Fatal("omitted prompt was resolved")
			}
			questionResult := <-entries[0].response
			questionResponse, ok := questionResult.resolution.(tools.AskQuestionAnswer)
			if questionResult.err != nil || !ok ||
				questionResponse.SelectedOptionNumber == nil ||
				*questionResponse.SelectedOptionNumber != selected {
				t.Fatalf("question resolution = %+v, error = %v", questionResult.resolution, questionResult.err)
			}
			approvalResult := <-accepted
			approvalResponse, ok := approvalResult.resolution.(tools.AskQuestionApproval)
			if approvalResult.err != nil || !ok ||
				approvalResponse.Decision != tools.AskQuestionApprovalDecisionAllowOnce {
				t.Fatalf("approval resolution = %+v, error = %v", approvalResult.resolution, approvalResult.err)
			}
			declinedResult := <-entries[2].response
			if !errors.Is(declinedResult.err, context.Canceled) {
				t.Fatalf("declined response error = %v, want context.Canceled", declinedResult.err)
			}
		})
	}
}

func TestExecutionPromptStoreResolvePromptBatchStopsAtCanonicalOperationalFailure(t *testing.T) {
	stepID := promptBatchStepID(t)
	selected := 1
	commands := []PromptAnswerCommand{
		promptQuestionAnswer("third", &selected, nil),
		promptQuestionAnswer("first", &selected, nil),
		promptQuestionAnswer("second", &selected, nil),
	}
	permutations := [][]PromptAnswerCommand{
		commands,
		{commands[1], commands[2], commands[0]},
		{commands[2], commands[0], commands[1]},
	}

	for index, permutation := range permutations {
		t.Run(fmt.Sprintf("permutation-%d", index), func(t *testing.T) {
			feed := &promptBatchRecordingFeed{failAt: "second", failErr: errors.New("publish failed")}
			store := newExecutionPromptStoreForTest(t, feed)
			first := promptBatchQuestion("first", stepID, time.Unix(1, 0))
			second := promptBatchQuestion("second", stepID, time.Unix(2, 0))
			third := promptBatchQuestion("third", stepID, time.Unix(3, 0))
			installPromptBatchEntries(&store, first, second, third)

			if _, err := store.ResolvePromptBatch(context.Background(), stepID, permutation); !errors.Is(err, feed.failErr) {
				t.Fatalf("ResolvePromptBatch error = %v, want publication failure", err)
			}
			if got, want := feed.resolvedIDs(), []string{"first", "second"}; !equalPromptBatchStrings(got, want) {
				t.Fatalf("resolution attempts = %v, want %v", got, want)
			}
			if store.hasPendingID("first") || store.hasPendingID("second") {
				t.Fatal("committed canonical prefix remained pending")
			}
			if !store.hasPendingID("third") {
				t.Fatal("unprocessed canonical suffix was removed")
			}
		})
	}
}

func TestExecutionPromptStoreResolvePromptBatchHonorsCancellationBetweenEntries(t *testing.T) {
	stepID := promptBatchStepID(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	feed := &promptBatchCancelingFeed{cancel: cancel}
	store := newExecutionPromptStoreForTest(t, feed)
	first := promptBatchQuestion("first", stepID, time.Unix(1, 0))
	second := promptBatchQuestion("second", stepID, time.Unix(2, 0))
	installPromptBatchEntries(&store, first, second)
	selected := 1

	_, err := store.ResolvePromptBatch(ctx, stepID, []PromptAnswerCommand{
		promptQuestionAnswer("second", &selected, nil),
		promptQuestionAnswer("first", &selected, nil),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolvePromptBatch error = %v, want cancellation", err)
	}
	if store.hasPendingID("first") {
		t.Fatal("resolved prompt remained pending after cancellation")
	}
	if !store.hasPendingID("second") {
		t.Fatal("unprocessed prompt was removed after cancellation")
	}
}

func TestExecutionPromptStoreResolvePromptBatchHonorsCancellationBeforeMutation(t *testing.T) {
	stepID := promptBatchStepID(t)
	store, feed := newPromptBatchStore(t)
	entry := promptBatchQuestion("question", stepID, time.Unix(1, 0))
	installPromptBatchEntries(&store, entry)
	selected := 1
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.ResolvePromptBatch(ctx, stepID, []PromptAnswerCommand{
		promptQuestionAnswer("question", &selected, nil),
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolvePromptBatch error = %v, want context.Canceled", err)
	}
	if !store.hasPendingID("question") {
		t.Fatal("canceled batch mutated pending prompt")
	}
	if got := feed.resolvedIDs(); len(got) != 0 {
		t.Fatalf("canceled batch published resolutions: %v", got)
	}
}

func TestExecutionPromptStoreResolvePromptBatchDoesNotWaitForPreparedSuccessor(t *testing.T) {
	stepID := promptBatchStepID(t)
	store, _ := newPromptBatchStore(t)
	first := promptBatchQuestion("first", stepID, time.Unix(1, 0))
	first.snapshot.Request.Origin = tools.AskQuestionOriginModelTool
	first.snapshot.Request.RunID = "run-1"
	first.snapshot.Request.QuestionBatch = &tools.AskQuestionBatchMetadata{
		Origin:              tools.AskQuestionOriginModelTool,
		RunID:               "run-1",
		StepID:              stepID.String(),
		ToolCallID:          "first",
		BatchToolCallIDs:    []string{"first", "future"},
		CandidateOrdinal:    0,
		PreparedPromptCount: 2,
	}
	installPromptBatchEntries(&store, first)
	selected := 1

	results, err := store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{
		promptQuestionAnswer("first", &selected, nil),
	})
	if err != nil {
		t.Fatalf("ResolvePromptBatch: %v", err)
	}
	requirePromptBatchResultSet(t, results, map[clientui.ToolCallID]PromptAnswerOutcome{
		"first": PromptAnswerOutcomeResolved,
	})
}

func TestAuthorityResolvePromptBatchDelegatesApprovalToExactAction(t *testing.T) {
	h := newApprovalActionHarness(t, approvalActionHarnessOptions{})
	results, err := h.resolve(context.Background(), approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, nil))
	requireApprovalActionOutcome(t, promptBatchCallResult{results: results, err: err}, PromptAnswerOutcomeResolved)
	requireApproval(t, waitApprovalHandle(t, h) == nil, "delegated Approval action failed")
}

type promptBatchCallResult struct {
	results []PromptAnswerResult
	err     error
}

type promptBatchRecordingFeed struct {
	mu       sync.Mutex
	resolved []string
	failAt   string
	failErr  error
}

func (f *promptBatchRecordingFeed) PromptPendingScope(ExecutionScope, tools.AskQuestionRequest, time.Time) error {
	return nil
}

func (f *promptBatchRecordingFeed) PromptResolvedScope(_ ExecutionScope, toolCallID string) error {
	f.mu.Lock()
	f.resolved = append(f.resolved, toolCallID)
	f.mu.Unlock()
	if toolCallID == f.failAt {
		return f.failErr
	}
	return nil
}

func (f *promptBatchRecordingFeed) resolvedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.resolved...)
}

type promptBatchBlockingFeed struct {
	*promptBatchRecordingFeed
	blockAt string
	blocked chan struct{}
	release chan struct{}
}

func newPromptBatchBlockingFeed(blockAt string) *promptBatchBlockingFeed {
	return &promptBatchBlockingFeed{
		promptBatchRecordingFeed: &promptBatchRecordingFeed{},
		blockAt:                  blockAt,
		blocked:                  make(chan struct{}),
		release:                  make(chan struct{}),
	}
}

func (f *promptBatchBlockingFeed) PromptResolvedScope(scope ExecutionScope, toolCallID string) error {
	if toolCallID == f.blockAt {
		close(f.blocked)
		<-f.release
	}
	return f.promptBatchRecordingFeed.PromptResolvedScope(scope, toolCallID)
}

type promptBatchCancelingFeed struct {
	promptBatchRecordingFeed
	cancel context.CancelCauseFunc
}

func (f *promptBatchCancelingFeed) PromptResolvedScope(scope ExecutionScope, toolCallID string) error {
	err := f.promptBatchRecordingFeed.PromptResolvedScope(scope, toolCallID)
	f.cancel(context.Canceled)
	return err
}

func newPromptBatchStore(t *testing.T) (executionPromptStore, *promptBatchRecordingFeed) {
	t.Helper()
	feed := &promptBatchRecordingFeed{}
	return newExecutionPromptStoreForTest(t, feed), feed
}

func promptBatchQuestion(id string, stepID runtimeids.StepID, createdAt time.Time) *executionPromptEntry {
	request := tools.AskQuestionRequest{
		ToolCallID:  id,
		StepID:      stepID.String(),
		Question:    id,
		Suggestions: []string{"one", "two"},
	}
	return promptBatchEntry(request, createdAt)
}

func promptBatchApproval(id string, stepID runtimeids.StepID, createdAt time.Time) *executionPromptEntry {
	request := tools.AskQuestionRequest{
		ToolCallID: id,
		StepID:     stepID.String(),
		Question:   id,
		Approval:   true,
		ApprovalOptions: []tools.AskQuestionApprovalOption{
			{Decision: tools.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
			{Decision: tools.AskQuestionApprovalDecisionDeny, Label: "Deny"},
		},
	}
	return promptBatchEntry(request, createdAt)
}

func promptBatchEntry(request tools.AskQuestionRequest, createdAt time.Time) *executionPromptEntry {
	publicationDone := make(chan struct{})
	close(publicationDone)
	return &executionPromptEntry{
		snapshot: ExecutionPromptSnapshot{
			Request:   request,
			CreatedAt: createdAt,
		},
		response:        make(chan executionPromptResult, 1),
		publicationDone: publicationDone,
	}
}

func installPromptBatchEntries(store *executionPromptStore, entries ...*executionPromptEntry) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, entry := range entries {
		store.pending[entry.snapshot.Request.ToolCallID] = entry
	}
}

func clonePromptBatchEntries(entries ...*executionPromptEntry) []*executionPromptEntry {
	clones := make([]*executionPromptEntry, 0, len(entries))
	for _, entry := range entries {
		clones = append(clones, promptBatchEntry(entry.snapshot.Request.Clone(), entry.snapshot.CreatedAt))
	}
	return clones
}

func promptQuestionAnswer(toolCallID string, selected *int, freeform *string) PromptAnswerCommand {
	return PromptAnswerCommand{
		ToolCallID: clientui.ToolCallID(toolCallID),
		Payload: PromptQuestionAnswerCommand{
			Answer: tools.AskQuestionAnswer{
				SelectedOptionNumber: selected,
				Freeform:             freeform,
			},
		},
	}
}

func promptApprovalAnswer(toolCallID string, decision tools.AskQuestionApprovalDecision, commentary *string) PromptAnswerCommand {
	return PromptAnswerCommand{
		ToolCallID: clientui.ToolCallID(toolCallID),
		Payload: PromptApprovalAnswerCommand{
			Answer: tools.AskQuestionApproval{
				Decision:   decision,
				Commentary: commentary,
			},
		},
	}
}

func promptDeclined(toolCallID string) PromptAnswerCommand {
	return PromptAnswerCommand{
		ToolCallID: clientui.ToolCallID(toolCallID),
		Payload:    PromptDeclinedCommand{},
	}
}

func requirePromptBatchResultSet(
	t *testing.T,
	results []PromptAnswerResult,
	want map[clientui.ToolCallID]PromptAnswerOutcome,
) {
	t.Helper()
	if len(results) != len(want) {
		t.Fatalf("result count = %d, want %d: %+v", len(results), len(want), results)
	}
	seen := make(map[clientui.ToolCallID]struct{}, len(results))
	for _, result := range results {
		if _, exists := seen[result.ToolCallID]; exists {
			t.Fatalf("duplicate result identity %q", result.ToolCallID)
		}
		seen[result.ToolCallID] = struct{}{}
		if result.Outcome != want[result.ToolCallID] {
			t.Fatalf("result %q outcome = %q, want %q", result.ToolCallID, result.Outcome, want[result.ToolCallID])
		}
	}
}

func promptBatchStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	id, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	return id
}

func promptBatchText(value string) *string {
	return &value
}

func equalPromptBatchStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
