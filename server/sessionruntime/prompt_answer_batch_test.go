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
			PromptID: "prompt-1",
		}},
	)
	if err == nil {
		t.Fatal("invalid internal command union was treated as stale")
	}
}

func TestExecutionPromptStoreResolvePromptBatchPreservesOptionalTextInTypedDelivery(t *testing.T) {
	store, _ := newPromptBatchStore(t)
	stepID := promptBatchStepID(t)
	question := promptBatchQuestion("question", stepID, time.Unix(1, 0))
	approval := promptBatchApproval("approval", stepID, time.Unix(2, 0))
	installPromptBatchEntries(&store, question, approval)
	selected := 1

	_, err := store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{
		promptQuestionAnswer("question", &selected, nil),
		promptApprovalAnswer("approval", tools.AskQuestionApprovalDecisionAllowOnce, nil),
	})
	if err != nil {
		t.Fatalf("ResolvePromptBatch: %v", err)
	}
	questionResolution, ok := (<-question.response).resolution.(tools.AskQuestionAnswer)
	if !ok {
		t.Fatal("Question delivery did not retain its typed resolution")
	}
	if questionResolution.Freeform != nil {
		t.Fatal("absent Question freeform became present during runtime delivery")
	}
	approvalResolution, ok := (<-approval.response).resolution.(tools.AskQuestionApproval)
	if !ok {
		t.Fatal("Approval delivery did not retain its typed resolution")
	}
	if approvalResolution.Commentary != nil {
		t.Fatal("absent Approval commentary became present during runtime delivery")
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
			installPromptBatchEntries(&store, append(entries, installedOmitted)...)

			results, err := store.ResolvePromptBatch(context.Background(), stepID, commands)
			if err != nil {
				t.Fatalf("ResolvePromptBatch: %v", err)
			}
			requirePromptBatchResultSet(t, results, map[clientui.PromptID]PromptAnswerOutcome{
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
			approvalResult := <-entries[1].response
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

func TestExecutionPromptStoreResolvePromptBatchTreatsStaleEntriesAsSkipped(t *testing.T) {
	store, _ := newPromptBatchStore(t)
	stepID := promptBatchStepID(t)
	otherStepID := promptBatchOtherStepID(t)
	pending := promptBatchQuestion("pending", stepID, time.Unix(1, 0))
	wrongStep := promptBatchQuestion("wrong-step", otherStepID, time.Unix(2, 0))
	installPromptBatchEntries(&store, pending, wrongStep)
	selected := 1

	results, err := store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{
		promptQuestionAnswer("missing", &selected, nil),
		promptQuestionAnswer("wrong-step", &selected, nil),
		promptQuestionAnswer("pending", &selected, nil),
	})
	if err != nil {
		t.Fatalf("ResolvePromptBatch: %v", err)
	}
	requirePromptBatchResultSet(t, results, map[clientui.PromptID]PromptAnswerOutcome{
		"missing":    PromptAnswerOutcomeSkipped,
		"wrong-step": PromptAnswerOutcomeSkipped,
		"pending":    PromptAnswerOutcomeResolved,
	})
	if !store.hasPendingID("wrong-step") {
		t.Fatal("prompt owned by another Step was mutated")
	}

	allStale, err := store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{
		promptQuestionAnswer("missing", &selected, nil),
		promptQuestionAnswer("pending", &selected, nil),
	})
	if err != nil {
		t.Fatalf("all-stale ResolvePromptBatch: %v", err)
	}
	requirePromptBatchResultSet(t, allStale, map[clientui.PromptID]PromptAnswerOutcome{
		"missing": PromptAnswerOutcomeSkipped,
		"pending": PromptAnswerOutcomeSkipped,
	})
}

func TestExecutionPromptStoreResolvePromptBatchFirstResolverWins(t *testing.T) {
	stepID := promptBatchStepID(t)
	feed := newPromptBatchBlockingFeed("first")
	store := newExecutionPromptStoreForTest(t, feed)
	first := promptBatchQuestion("first", stepID, time.Unix(1, 0))
	second := promptBatchQuestion("second", stepID, time.Unix(2, 0))
	installPromptBatchEntries(&store, first, second)
	selected := 1
	done := make(chan promptBatchCallResult, 1)
	go func() {
		results, err := store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{
			promptQuestionAnswer("second", &selected, nil),
			promptQuestionAnswer("first", &selected, nil),
		})
		done <- promptBatchCallResult{results: results, err: err}
	}()
	<-feed.blocked

	if err := store.Submit("second", testLegacyQuestionResolution("external"), nil); err != nil {
		t.Fatalf("external Submit: %v", err)
	}
	close(feed.release)
	call := <-done
	if call.err != nil {
		t.Fatalf("ResolvePromptBatch: %v", call.err)
	}
	requirePromptBatchResultSet(t, call.results, map[clientui.PromptID]PromptAnswerOutcome{
		"first":  PromptAnswerOutcomeResolved,
		"second": PromptAnswerOutcomeSkipped,
	})
	externalResult := <-second.response
	if externalResult.err != nil {
		t.Fatalf("external winner error = %v", externalResult.err)
	}
	requireLegacyQuestionAnswer(t, externalResult.resolution, "external")
}

func TestExecutionPromptStoreResolvePromptBatchSkipsReplacedExactEntry(t *testing.T) {
	stepID := promptBatchStepID(t)
	feed := newPromptBatchBlockingFeed("first")
	store := newExecutionPromptStoreForTest(t, feed)
	first := promptBatchQuestion("first", stepID, time.Unix(1, 0))
	second := promptBatchQuestion("second", stepID, time.Unix(2, 0))
	installPromptBatchEntries(&store, first, second)
	selected := 1
	done := make(chan promptBatchCallResult, 1)
	go func() {
		results, err := store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{
			promptQuestionAnswer("first", &selected, nil),
			promptQuestionAnswer("second", &selected, nil),
		})
		done <- promptBatchCallResult{results: results, err: err}
	}()
	<-feed.blocked

	replacement := promptBatchQuestion("second", stepID, time.Unix(3, 0))
	store.mu.Lock()
	store.pending["second"] = replacement
	store.mu.Unlock()
	close(feed.release)
	call := <-done
	if call.err != nil {
		t.Fatalf("ResolvePromptBatch: %v", call.err)
	}
	requirePromptBatchResultSet(t, call.results, map[clientui.PromptID]PromptAnswerOutcome{
		"first":  PromptAnswerOutcomeResolved,
		"second": PromptAnswerOutcomeSkipped,
	})
	if !store.hasPendingID("second") {
		t.Fatal("replacement prompt was removed by stale prepared entry")
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
		PromptID:            "first",
		BatchPromptIDs:      []string{"first", "future"},
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
	requirePromptBatchResultSet(t, results, map[clientui.PromptID]PromptAnswerOutcome{
		"first": PromptAnswerOutcomeResolved,
	})
}

func TestExecutionPromptStoreResolvePromptBatchDeclinedApprovalHasNoDecisionPayload(t *testing.T) {
	stepID := promptBatchStepID(t)
	store, feed := newPromptBatchStore(t)
	approval := promptBatchApproval("approval", stepID, time.Unix(1, 0))
	installPromptBatchEntries(&store, approval)

	results, err := store.ResolvePromptBatch(context.Background(), stepID, []PromptAnswerCommand{
		promptDeclined("approval"),
	})
	if err != nil {
		t.Fatalf("ResolvePromptBatch: %v", err)
	}
	requirePromptBatchResultSet(t, results, map[clientui.PromptID]PromptAnswerOutcome{
		"approval": PromptAnswerOutcomeResolved,
	})
	if got, want := feed.resolvedIDs(), []string{"approval"}; !equalPromptBatchStrings(got, want) {
		t.Fatalf("resolved approvals = %v, want %v", got, want)
	}
	result := <-approval.response
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("declined Approval error = %v, want context.Canceled", result.err)
	}
	if result.resolution != nil {
		t.Fatalf("declined Approval delivered resolution: %+v", result.resolution)
	}
}

func TestAuthorityResolvePromptBatchWithoutActiveExecutionReturnsAllSkipped(t *testing.T) {
	authority := NewAuthority(AuthorityOptions{})
	selected := 1
	results, err := authority.ResolvePromptBatch(
		context.Background(),
		runtimeids.NewSessionID(),
		promptBatchStepID(t),
		[]PromptAnswerCommand{
			promptQuestionAnswer("question", &selected, nil),
			promptDeclined("approval"),
		},
	)
	if err != nil {
		t.Fatalf("ResolvePromptBatch: %v", err)
	}
	requirePromptBatchResultSet(t, results, map[clientui.PromptID]PromptAnswerOutcome{
		"question": PromptAnswerOutcomeSkipped,
		"approval": PromptAnswerOutcomeSkipped,
	})
}

func TestAuthorityResolvePromptBatchDoesNotRedirectIntoReplacementExecution(t *testing.T) {
	authority := NewAuthority(AuthorityOptions{})
	sessionID := runtimeids.NewSessionID()
	resourceRef, err := runtimeids.NewSessionResourceRef(sessionID, 1)
	if err != nil {
		t.Fatalf("NewSessionResourceRef: %v", err)
	}
	stepID := promptBatchStepID(t)
	feed := newPromptBatchBlockingFeed("first")
	firstScope := newAgentExecutionScope(runtimeids.NewExecutionScopeID(), 1, resourceRef, nil)
	firstExecution := &execution{
		authority: authority,
		scope:     firstScope,
		prompts:   newExecutionPromptStore(authority, firstScope, feed),
	}
	first := promptBatchQuestion("first", stepID, time.Unix(1, 0))
	second := promptBatchQuestion("second", stepID, time.Unix(2, 0))
	installPromptBatchEntries(&firstExecution.prompts, first, second)
	resource := &agentResource{
		authority: authority,
		ref:       resourceRef,
		changed:   make(chan struct{}),
		current:   firstExecution,
	}
	authority.mu.Lock()
	authority.resources[sessionID] = resource
	authority.byScope[firstScope.ID()] = firstExecution
	authority.mu.Unlock()

	selected := 1
	done := make(chan promptBatchCallResult, 1)
	go func() {
		results, resolveErr := authority.ResolvePromptBatch(
			context.Background(),
			sessionID,
			stepID,
			[]PromptAnswerCommand{
				promptQuestionAnswer("second", &selected, nil),
				promptQuestionAnswer("first", &selected, nil),
			},
		)
		done <- promptBatchCallResult{results: results, err: resolveErr}
	}()
	<-feed.blocked

	secondResourceRef, err := runtimeids.NewSessionResourceRef(sessionID, 2)
	if err != nil {
		t.Fatalf("replacement NewSessionResourceRef: %v", err)
	}
	secondScope := newAgentExecutionScope(runtimeids.NewExecutionScopeID(), 2, secondResourceRef, nil)
	replacementExecution := &execution{
		authority: authority,
		scope:     secondScope,
		prompts:   newExecutionPromptStore(authority, secondScope, nil),
	}
	replacementPrompt := promptBatchQuestion("second", stepID, time.Unix(3, 0))
	installPromptBatchEntries(&replacementExecution.prompts, replacementPrompt)
	resource.mu.Lock()
	resource.ref = secondResourceRef
	resource.current = replacementExecution
	resource.mu.Unlock()
	authority.mu.Lock()
	authority.byScope[secondScope.ID()] = replacementExecution
	authority.mu.Unlock()

	close(feed.release)
	call := <-done
	if call.err != nil {
		t.Fatalf("ResolvePromptBatch: %v", call.err)
	}
	requirePromptBatchResultSet(t, call.results, map[clientui.PromptID]PromptAnswerOutcome{
		"first":  PromptAnswerOutcomeResolved,
		"second": PromptAnswerOutcomeResolved,
	})
	if !replacementExecution.prompts.hasPendingID("second") {
		t.Fatal("batch redirected the remaining answer into replacement execution")
	}
	if firstExecution.prompts.hasPendingID("second") {
		t.Fatal("captured exact execution did not receive remaining answer")
	}
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

func (f *promptBatchRecordingFeed) PromptResolvedScope(_ ExecutionScope, promptID string) error {
	f.mu.Lock()
	f.resolved = append(f.resolved, promptID)
	f.mu.Unlock()
	if promptID == f.failAt {
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

func (f *promptBatchBlockingFeed) PromptResolvedScope(scope ExecutionScope, promptID string) error {
	if promptID == f.blockAt {
		close(f.blocked)
		<-f.release
	}
	return f.promptBatchRecordingFeed.PromptResolvedScope(scope, promptID)
}

type promptBatchCancelingFeed struct {
	promptBatchRecordingFeed
	cancel context.CancelCauseFunc
}

func (f *promptBatchCancelingFeed) PromptResolvedScope(scope ExecutionScope, promptID string) error {
	err := f.promptBatchRecordingFeed.PromptResolvedScope(scope, promptID)
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
		ID:          id,
		StepID:      stepID.String(),
		Question:    id,
		Suggestions: []string{"one", "two"},
	}
	return promptBatchEntry(request, createdAt)
}

func promptBatchApproval(id string, stepID runtimeids.StepID, createdAt time.Time) *executionPromptEntry {
	request := tools.AskQuestionRequest{
		ID:       id,
		StepID:   stepID.String(),
		Question: id,
		Approval: true,
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
		store.pending[entry.snapshot.Request.ID] = entry
	}
}

func clonePromptBatchEntries(entries ...*executionPromptEntry) []*executionPromptEntry {
	clones := make([]*executionPromptEntry, 0, len(entries))
	for _, entry := range entries {
		clones = append(clones, promptBatchEntry(cloneExecutionPromptRequest(entry.snapshot.Request), entry.snapshot.CreatedAt))
	}
	return clones
}

func promptQuestionAnswer(promptID string, selected *int, freeform *string) PromptAnswerCommand {
	return PromptAnswerCommand{
		PromptID: clientui.PromptID(promptID),
		Payload: PromptQuestionAnswerCommand{
			Answer: tools.AskQuestionAnswer{
				SelectedOptionNumber: selected,
				Freeform:             freeform,
			},
		},
	}
}

func promptApprovalAnswer(promptID string, decision tools.AskQuestionApprovalDecision, commentary *string) PromptAnswerCommand {
	return PromptAnswerCommand{
		PromptID: clientui.PromptID(promptID),
		Payload: PromptApprovalAnswerCommand{
			Answer: tools.AskQuestionApproval{
				Decision:   decision,
				Commentary: commentary,
			},
		},
	}
}

func promptDeclined(promptID string) PromptAnswerCommand {
	return PromptAnswerCommand{
		PromptID: clientui.PromptID(promptID),
		Payload:  PromptDeclinedCommand{},
	}
}

func requirePromptBatchResultSet(
	t *testing.T,
	results []PromptAnswerResult,
	want map[clientui.PromptID]PromptAnswerOutcome,
) {
	t.Helper()
	if len(results) != len(want) {
		t.Fatalf("result count = %d, want %d: %+v", len(results), len(want), results)
	}
	seen := make(map[clientui.PromptID]struct{}, len(results))
	for _, result := range results {
		if _, exists := seen[result.PromptID]; exists {
			t.Fatalf("duplicate result identity %q", result.PromptID)
		}
		seen[result.PromptID] = struct{}{}
		if result.Outcome != want[result.PromptID] {
			t.Fatalf("result %q outcome = %q, want %q", result.PromptID, result.Outcome, want[result.PromptID])
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

func promptBatchOtherStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	id, err := runtimeids.ParseStepID("33333333-3333-4333-8333-333333333333")
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
