package tools

import (
	"context"
	"core/shared/clientui"
	"core/shared/textutil"
	"core/shared/toolspec"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

func testApprovalRequest(id string) AskQuestionRequest {
	return AskQuestionRequest{
		ToolCallID: id,
		Question:   "approve?",
		Approval:   true,
		ApprovalOptions: []AskQuestionApprovalOption{
			{Decision: AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
			{Decision: AskQuestionApprovalDecisionAllowSession, Label: "Allow for this session"},
			{Decision: AskQuestionApprovalDecisionDeny, Label: "Deny"},
		},
	}
}

func testApprovalContext(parent context.Context, toolCallID string) context.Context {
	ctx := WithExecutionIdentity(parent, ExecutionIdentity{
		RunID:      "11111111-1111-4111-8111-111111111111",
		StepID:     "22222222-2222-4222-8222-222222222222",
		ToolCallID: clientui.ToolCallID(toolCallID),
	})
	return WithApprovalLifecycle(ctx, NewApprovalLifecycle())
}

func testQuestionAnswer(text string) AskQuestionAnswer {
	return AskQuestionAnswer{Freeform: textutil.Value(text)}
}

func TestAskRunsTypedEffectBarrierAfterValidationBeforeHandlerSelection(t *testing.T) {
	b := NewAskQuestionBroker()
	order := make([]string, 0, 2)
	b.SetAskHandler(func(_ context.Context, _ AskQuestionRequest) (AskQuestionResolution, error) {
		order = append(order, "handler")
		return testQuestionAnswer("handled"), nil
	})
	ctx := WithEffectBarrier(context.Background(), func(reason EffectBarrierReason) error {
		if reason != EffectBarrierQuestion {
			t.Fatalf("barrier reason = %d, want Question", reason)
		}
		order = append(order, "barrier")
		// This would deadlock if Ask held the broker mutex while invoking the barrier.
		b.SetAskHandler(func(_ context.Context, _ AskQuestionRequest) (AskQuestionResolution, error) {
			order = append(order, "replacement")
			return testQuestionAnswer("replaced"), nil
		})
		return nil
	})

	resolution, err := b.Ask(ctx, AskQuestionRequest{ToolCallID: "question", Question: "one?"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	answer, ok := resolution.(AskQuestionAnswer)
	if !ok || answer.Freeform == nil || *answer.Freeform != "replaced" {
		t.Fatalf("resolution = %+v, want replacement handler response", resolution)
	}
	if !slices.Equal(order, []string{"barrier", "replacement"}) {
		t.Fatalf("execution order = %v, want barrier then selected handler", order)
	}
}

func TestAskUsesApprovalBarrierAndBlocksInteractionWhenItFails(t *testing.T) {
	b := NewAskQuestionBroker()
	handlerCalled := false
	b.SetAskHandler(func(_ context.Context, _ AskQuestionRequest) (AskQuestionResolution, error) {
		handlerCalled = true
		return AskQuestionApproval{Decision: AskQuestionApprovalDecisionAllowOnce}, nil
	})
	barrierErr := errors.New("flush failed")
	ctx := WithEffectBarrier(testApprovalContext(context.Background(), "approval"), func(reason EffectBarrierReason) error {
		if reason != EffectBarrierApproval {
			t.Fatalf("barrier reason = %d, want Approval", reason)
		}
		return barrierErr
	})

	if _, err := b.Ask(ctx, testApprovalRequest("approval")); !errors.Is(err, barrierErr) {
		t.Fatalf("Ask error = %v, want barrier error", err)
	}
	if handlerCalled {
		t.Fatal("approval handler ran after barrier failure")
	}
	if pending := b.Pending(); len(pending) != 0 {
		t.Fatalf("approval was queued after barrier failure: %+v", pending)
	}
}

func TestAskRejectsInvalidRequestBeforeEffectBarrier(t *testing.T) {
	calls := 0
	ctx := WithEffectBarrier(context.Background(), func(EffectBarrierReason) error {
		calls++
		return nil
	})

	if _, err := NewAskQuestionBroker().Ask(ctx, AskQuestionRequest{}); err == nil {
		t.Fatal("invalid ask succeeded")
	}
	if calls != 0 {
		t.Fatalf("barrier calls = %d, want zero for invalid request", calls)
	}
}

func TestQueuedToolCallBarrierFailureDoesNotMaterializeRequestAndRunsBatchCleanup(t *testing.T) {
	b := NewAskQuestionBroker()
	barrierErr := errors.New("flush failed")
	executionCtx, cancelExecution := context.WithCancel(context.Background())
	ctx := WithEffectBarrier(executionCtx, func(reason EffectBarrierReason) error {
		if reason != EffectBarrierQuestion {
			t.Fatalf("barrier reason = %d, want Question", reason)
		}
		cancelExecution()
		return barrierErr
	})
	skipped := 0
	result, err := NewAskQuestionTool(b, func() bool { return true }).Call(ctx, Call{
		ID:    "queued-question",
		Name:  toolspec.ToolAskQuestion,
		Input: json.RawMessage(`{"question":"Continue?"}`),
		AskQuestionBatch: &AskQuestionBatchMetadata{
			Origin:              AskQuestionOriginModelTool,
			RunID:               "run-1",
			StepID:              "step-1",
			ToolCallID:          "queued-question",
			BatchToolCallIDs:    []string{"queued-question"},
			CandidateOrdinal:    0,
			PreparedPromptCount: 1,
		},
		OnAskQuestionBatchSkipped: func(AskQuestionBatchMetadata) {
			skipped++
		},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !result.IsError {
		t.Fatalf("result = %+v, want provisional barrier error", result)
	}
	if skipped != 1 {
		t.Fatalf("batch cleanup calls = %d, want one", skipped)
	}
	if pending := b.Pending(); len(pending) != 0 {
		t.Fatalf("barrier-failed queued request materialized: %+v", pending)
	}
}
func TestBrokerFIFOQueue(t *testing.T) {
	b := NewAskQuestionBroker()

	ctx := context.Background()
	type out struct {
		id         string
		resolution AskQuestionResolution
		err        error
	}
	ch := make(chan out, 2)

	go func() {
		resp, err := b.Ask(ctx, AskQuestionRequest{ToolCallID: "q1", Question: "one?"})
		ch <- out{id: "q1", resolution: resp, err: err}
	}()
	for i := 0; i < 100; i++ {
		if len(b.Pending()) == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	go func() {
		resp, err := b.Ask(ctx, AskQuestionRequest{ToolCallID: "q2", Question: "two?"})
		ch <- out{id: "q2", resolution: resp, err: err}
	}()

	time.Sleep(10 * time.Millisecond)
	pending := b.Pending()
	if len(pending) != 2 {
		t.Fatalf("pending count = %d", len(pending))
	}
	if pending[0].ToolCallID != "q1" || pending[1].ToolCallID != "q2" {
		t.Fatalf("pending not fifo: %+v", pending)
	}

	if err := b.Submit("q1", testQuestionAnswer("a1")); err != nil {
		t.Fatalf("submit q1: %v", err)
	}
	if err := b.Submit("q2", testQuestionAnswer("a2")); err != nil {
		t.Fatalf("submit q2: %v", err)
	}

	got := map[string]string{}
	for i := 0; i < 2; i++ {
		item := <-ch
		if item.err != nil {
			t.Fatalf("ask result err: %v", item.err)
		}
		answer, ok := item.resolution.(AskQuestionAnswer)
		if !ok || answer.Freeform == nil {
			t.Fatalf("Question resolution = %+v", item.resolution)
		}
		got[item.id] = *answer.Freeform
	}

	if got["q1"] != "a1" || got["q2"] != "a2" {
		t.Fatalf("unexpected answers: %+v", got)
	}
}

func TestAskQuestionToolSkipsPreparedBatchWhenBrokerReturnsBeforeHandler(t *testing.T) {
	b := NewAskQuestionBroker()
	handlerCalled := false
	b.SetAskHandler(func(context.Context, AskQuestionRequest) (AskQuestionResolution, error) {
		handlerCalled = true
		return AskQuestionAnswer{}, nil
	})
	tool := NewAskQuestionTool(b, func() bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	skipped := make([]AskQuestionBatchMetadata, 0, 1)

	result, err := tool.Call(ctx, Call{
		ID:    "ask-2",
		Name:  toolspec.ToolAskQuestion,
		Input: mustAskQuestionInput(t, "two?"),
		AskQuestionBatch: &AskQuestionBatchMetadata{
			Origin:              AskQuestionOriginModelTool,
			RunID:               "run-1",
			StepID:              "step-1",
			ToolCallID:          "ask-2",
			BatchToolCallIDs:    []string{"ask-1", "ask-2"},
			CandidateOrdinal:    1,
			PreparedPromptCount: 2,
		},
		OnAskQuestionBatchSkipped: func(batch AskQuestionBatchMetadata) {
			skipped = append(skipped, batch)
		},
	})
	if err != nil {
		t.Fatalf("Call returned unexpected handler error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("result = %+v, want error result", result)
	}
	if handlerCalled {
		t.Fatal("ask handler was called after context cancellation")
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped batches = %+v, want one skip", skipped)
	}
	if skipped[0].ToolCallID != "ask-2" || len(skipped[0].BatchToolCallIDs) != 2 || skipped[0].BatchToolCallIDs[0] != "ask-1" || skipped[0].BatchToolCallIDs[1] != "ask-2" {
		t.Fatalf("skipped batch = %+v", skipped[0])
	}
}

func TestAskQuestionToolDeclineKeepsPreparedSuccessorsPending(t *testing.T) {
	broker := NewAskQuestionBroker()
	broker.SetAskHandler(func(context.Context, AskQuestionRequest) (AskQuestionResolution, error) {
		return nil, context.Canceled
	})
	tool := NewAskQuestionTool(broker, func() bool { return true })
	var skipped []AskQuestionBatchMetadata

	result, err := tool.Call(context.Background(), Call{
		ID:    "ask-1",
		Name:  toolspec.ToolAskQuestion,
		Input: mustAskQuestionInput(t, "one?"),
		AskQuestionBatch: &AskQuestionBatchMetadata{
			Origin:              AskQuestionOriginModelTool,
			RunID:               "run-1",
			StepID:              "step-1",
			ToolCallID:          "ask-1",
			BatchToolCallIDs:    []string{"ask-1", "ask-2"},
			CandidateOrdinal:    0,
			PreparedPromptCount: 2,
		},
		OnAskQuestionBatchSkipped: func(batch AskQuestionBatchMetadata) {
			skipped = append(skipped, batch)
		},
	})
	if err != nil {
		t.Fatalf("Call returned unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("result = %+v, want declined error result", result)
	}
	if len(skipped) != 0 {
		t.Fatalf("decline marked prepared successors skipped: %+v", skipped)
	}
}

func TestSubmitApprovalResponse(t *testing.T) {
	b := NewAskQuestionBroker()
	ctx := testApprovalContext(context.Background(), "approval")
	type out struct {
		resolution AskQuestionResolution
		err        error
	}
	done := make(chan out, 1)

	go func() {
		resp, err := b.Ask(ctx, testApprovalRequest("approval"))
		done <- out{resolution: resp, err: err}
	}()

	for i := 0; i < 100; i++ {
		if len(b.Pending()) == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	commentary := "trusted path"
	approval := AskQuestionApproval{Decision: AskQuestionApprovalDecisionAllowSession, Commentary: &commentary}
	if err := b.Submit("approval", approval); err != nil {
		t.Fatalf("submit approval: %v", err)
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("ask approval: %v", result.err)
		}
		if result.resolution != approval {
			t.Fatalf("approval resolution = %+v, want %+v", result.resolution, approval)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval response")
	}
}

func TestValidateAskQuestionResolutionForApprovalPrompt(t *testing.T) {
	req := AskQuestionRequest{
		ToolCallID: "approval",
		Question:   "approve?",
		Approval:   true,
		ApprovalOptions: []AskQuestionApprovalOption{
			{Decision: AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
			{Decision: AskQuestionApprovalDecisionDeny, Label: "Deny"},
		},
	}
	if err := ValidateAskQuestionResolution(req, testQuestionAnswer("allow")); !errors.Is(err, ErrAskQuestionApprovalRequiresResponse) {
		t.Fatalf("ordinary answer to approval prompt error = %v, want approval response required", err)
	}
	if err := ValidateAskQuestionResolution(req, AskQuestionApproval{Decision: AskQuestionApprovalDecisionAllowSession}); err == nil {
		t.Fatal("expected unoffered approval decision to be rejected")
	}
	commentary := "no"
	if err := ValidateAskQuestionResolution(req, AskQuestionApproval{Decision: AskQuestionApprovalDecisionDeny, Commentary: &commentary}); err != nil {
		t.Fatalf("valid approval response rejected: %v", err)
	}
}

func TestValidateAskQuestionResolutionRejectsInvalidSelectedOption(t *testing.T) {
	for _, option := range []int{0, -1, 2} {
		if err := ValidateAskQuestionResolution(
			AskQuestionRequest{ToolCallID: "ask-1", Question: "Proceed?", Suggestions: []string{"yes"}},
			AskQuestionAnswer{SelectedOptionNumber: &option},
		); err == nil {
			t.Fatalf("expected selected option %d to be rejected", option)
		}
	}
}

func TestValidateAskQuestionResolutionRejectsApprovalForOrdinaryQuestion(t *testing.T) {
	err := ValidateAskQuestionResolution(
		AskQuestionRequest{ToolCallID: "ask-1", Question: "Proceed?"},
		AskQuestionApproval{Decision: AskQuestionApprovalDecisionAllowOnce},
	)
	if !errors.Is(err, ErrAskQuestionNonApprovalForbidsApproval) {
		t.Fatalf("approval payload to ordinary prompt error = %v, want forbidden approval payload", err)
	}
}

func TestApprovalAskRequiresApprovalOptions(t *testing.T) {
	b := NewAskQuestionBroker()
	_, err := b.Ask(context.Background(), AskQuestionRequest{ToolCallID: "approval", Question: "approve?", Approval: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAskQuestionApprovalRequiresOptions) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApprovalAskIgnoresRecommendedOptionIndex(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(_ context.Context, req AskQuestionRequest) (AskQuestionResolution, error) {
		if req.RecommendedOptionIndex != 0 {
			t.Fatalf("expected recommended option index ignored for approval ask, got %+v", req)
		}
		return AskQuestionApproval{Decision: AskQuestionApprovalDecisionAllowOnce}, nil
	})

	req := testApprovalRequest("approval")
	req.RecommendedOptionIndex = 1
	resp, err := b.Ask(testApprovalContext(context.Background(), "approval"), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	approval, ok := resp.(AskQuestionApproval)
	if !ok || approval.Decision != AskQuestionApprovalDecisionAllowOnce {
		t.Fatalf("unexpected approval response: %+v", resp)
	}
}

func TestApprovalAskRejectsSuggestions(t *testing.T) {
	b := NewAskQuestionBroker()
	req := testApprovalRequest("approval")
	req.Suggestions = []string{"do not use suggestions here"}
	_, err := b.Ask(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAskQuestionApprovalForbidsSuggestions) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFreeformAskRejectsEmptyResponse(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(_ context.Context, req AskQuestionRequest) (AskQuestionResolution, error) {
		return AskQuestionAnswer{}, nil
	})

	_, err := b.Ask(context.Background(), AskQuestionRequest{ToolCallID: "freeform", Question: "what else?"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAskQuestionNonApprovalRequiresAnswer) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSubmitRejectsPlainStringResponseForApprovalAsk(t *testing.T) {
	b := NewAskQuestionBroker()
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := testApprovalContext(baseCtx, "approval")
	type out struct {
		resolution AskQuestionResolution
		err        error
	}
	done := make(chan out, 1)
	approvalReq := testApprovalRequest("approval")

	go func() {
		resp, err := b.Ask(ctx, approvalReq)
		done <- out{resolution: resp, err: err}
	}()

	for i := 0; i < 100; i++ {
		if len(b.Pending()) == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	if err := b.Submit("approval", testQuestionAnswer("allow once")); err == nil {
		t.Fatal("expected submit error for plain-string approval response")
	} else if !errors.Is(err, ErrAskQuestionApprovalRequiresResponse) {
		t.Fatalf("unexpected submit error: %v", err)
	}

	valid := AskQuestionApproval{Decision: AskQuestionApprovalDecisionAllowOnce}
	if err := b.Submit("approval", valid); err != nil {
		t.Fatalf("submit valid approval: %v", err)
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("ask approval: %v", result.err)
		}
		if result.resolution != valid {
			t.Fatalf("approval resolution = %+v, want %+v", result.resolution, valid)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval response")
	}
}

func TestAskHandlerRejectsPlainStringResponseForApprovalAsk(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(context.Context, AskQuestionRequest) (AskQuestionResolution, error) {
		return testQuestionAnswer("allow once"), nil
	})

	_, err := b.Ask(
		testApprovalContext(context.Background(), "approval"),
		testApprovalRequest("approval"),
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAskQuestionApprovalRequiresResponse) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAskHandlerModeDoesNotQueuePendingRequest(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(_ context.Context, req AskQuestionRequest) (AskQuestionResolution, error) {
		return testQuestionAnswer("handled"), nil
	})

	resp, err := b.Ask(context.Background(), AskQuestionRequest{ToolCallID: "sync", Question: "one?"})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	answer, ok := resp.(AskQuestionAnswer)
	if !ok || answer.Freeform == nil || *answer.Freeform != "handled" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if pending := b.Pending(); len(pending) != 0 {
		t.Fatalf("expected no pending requests in handler mode, got %+v", pending)
	}
	if err := b.Submit("sync", testQuestionAnswer("late")); err == nil {
		t.Fatal("expected submit to reject non-queued sync request")
	}
}

func TestSynchronousInternalApprovalAcceptsConsumerExactlyOnce(t *testing.T) {
	b := NewAskQuestionBroker()
	consumerCalls := 0
	request := testApprovalRequest("approval-sync")
	request.ApprovalConsumer = func(AskQuestionApproval) error {
		consumerCalls++
		return nil
	}
	b.SetLifecycleAskHandler(func(_ context.Context, handled AskQuestionRequest) (AskQuestionResolution, error) {
		resolution := AskQuestionApproval{Decision: AskQuestionApprovalDecisionAllowOnce}
		return resolution, handled.AcceptApproval(resolution)
	})

	if _, err := b.Ask(testApprovalContext(context.Background(), request.ToolCallID), request); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if consumerCalls != 1 {
		t.Fatalf("Approval consumer calls = %d, want 1", consumerCalls)
	}
}

func TestToolCallBlocksUntilQueuedAnswerSubmitted(t *testing.T) {
	b := NewAskQuestionBroker()
	tl := NewAskQuestionTool(b, nil)
	type callResult struct {
		result Result
		err    error
	}
	done := make(chan callResult, 1)

	go func() {
		result, err := tl.Call(context.Background(), Call{
			ID:   "call-queued",
			Name: toolspec.ToolAskQuestion,
			Input: json.RawMessage(`{
				"question":"Pick one",
				"suggestions":["alpha","beta"]
			}`),
		})
		done <- callResult{result: result, err: err}
	}()

	pending := waitForPendingRequests(t, b, 1)
	if len(pending) != 1 {
		t.Fatalf("expected one pending request, got %+v", pending)
	}
	if pending[0].ToolCallID != "call-queued" {
		t.Fatalf("expected pending request id call-queued, got %+v", pending[0])
	}
	if pending[0].Question != "Pick one" {
		t.Fatalf("unexpected pending question: %+v", pending[0])
	}
	if len(pending[0].Suggestions) != 2 || pending[0].Suggestions[0] != "alpha" || pending[0].Suggestions[1] != "beta" {
		t.Fatalf("unexpected pending suggestions: %+v", pending[0])
	}

	select {
	case result := <-done:
		t.Fatalf("tool call returned before answer submission: %+v", result)
	default:
	}

	if err := b.Submit("call-queued", AskQuestionAnswer{
		SelectedOptionNumber: textutil.Value(2),
		Freeform:             textutil.Value("need extra context"),
	}); err != nil {
		t.Fatalf("submit answer: %v", err)
	}
	if err := b.Submit("call-queued", AskQuestionAnswer{SelectedOptionNumber: textutil.Value(1)}); err == nil {
		t.Fatal("expected duplicate submission to fail after queued tool answer")
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("tool call err: %v", result.err)
		}
		if result.result.IsError {
			t.Fatalf("expected success result, got %+v", result.result)
		}
		var output string
		if err := json.Unmarshal(result.result.Output, &output); err != nil {
			t.Fatalf("decode output summary: %v", err)
		}
		if output == "" {
			t.Fatal("expected non-empty tool output summary")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued tool answer")
	}

	if pending := b.Pending(); len(pending) != 0 {
		t.Fatalf("expected queue drained after completion, got %+v", pending)
	}
}

func TestToolCallPassesPreparedBatchMetadataToAskBroker(t *testing.T) {
	b := NewAskQuestionBroker()
	var got AskQuestionRequest
	b.SetAskHandler(func(_ context.Context, req AskQuestionRequest) (AskQuestionResolution, error) {
		got = req
		return testQuestionAnswer("answer"), nil
	})
	tool := NewAskQuestionTool(b, func() bool { return true })
	meta := &AskQuestionBatchMetadata{
		Origin:              AskQuestionOriginModelTool,
		RunID:               "run-1",
		StepID:              "step-1",
		ToolCallID:          "ask-1",
		BatchToolCallIDs:    []string{"ask-1", "ask-2"},
		CandidateOrdinal:    0,
		PreparedPromptCount: 2,
	}
	_, err := tool.Call(context.Background(), Call{
		ID:               "ask-1",
		Name:             toolspec.ToolAskQuestion,
		Input:            json.RawMessage(`{"question":"one?"}`),
		RunID:            "run-1",
		StepID:           "step-1",
		AskQuestionBatch: meta,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got.QuestionBatch == nil || got.QuestionBatch.StepID != "step-1" || got.QuestionBatch.PreparedPromptCount != 2 {
		t.Fatalf("broker metadata = %+v", got)
	}
	if got.Origin != AskQuestionOriginModelTool || got.RunID != "run-1" || got.StepID != "step-1" || got.ToolCallID != "ask-1" {
		t.Fatalf("broker request identity = %+v", got)
	}
}

func TestToolCallReportsPreparedBatchSkippedWhenQuestionsBecomeDisabled(t *testing.T) {
	tool := NewAskQuestionTool(NewAskQuestionBroker(), func() bool { return false })
	meta := &AskQuestionBatchMetadata{
		Origin:              AskQuestionOriginModelTool,
		RunID:               "run-1",
		StepID:              "step-1",
		ToolCallID:          "ask-1",
		BatchToolCallIDs:    []string{"ask-1"},
		CandidateOrdinal:    0,
		PreparedPromptCount: 1,
	}
	var skipped *AskQuestionBatchMetadata
	res, err := tool.Call(context.Background(), Call{
		ID:               "ask-1",
		Name:             toolspec.ToolAskQuestion,
		Input:            json.RawMessage(`{"question":"one?"}`),
		RunID:            "run-1",
		StepID:           "step-1",
		AskQuestionBatch: meta,
		OnAskQuestionBatchSkipped: func(metadata AskQuestionBatchMetadata) {
			skipped = &metadata
		},
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.IsError {
		t.Fatalf("result = %+v, want error result", res)
	}
	if skipped == nil || skipped.StepID != "step-1" || skipped.ToolCallID != "ask-1" {
		t.Fatalf("skipped metadata = %+v", skipped)
	}
}

func TestAskHandlerModePrefersContextCancellationAfterHandlerReturns(t *testing.T) {
	b := NewAskQuestionBroker()
	release := make(chan struct{})
	b.SetAskHandler(func(_ context.Context, req AskQuestionRequest) (AskQuestionResolution, error) {
		<-release
		return testQuestionAnswer("handled"), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := b.Ask(ctx, AskQuestionRequest{ToolCallID: "sync", Question: "one?"})
		done <- err
	}()
	cancel()
	close(release)

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestCanceledAskIsRemovedFromPendingQueue(t *testing.T) {
	b := NewAskQuestionBroker()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := b.Ask(ctx, AskQuestionRequest{ToolCallID: "q-cancel", Question: "will cancel?"})
		done <- err
	}()

	for i := 0; i < 100; i++ {
		if len(b.Pending()) == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context canceled error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for canceled ask")
	}

	if pending := b.Pending(); len(pending) != 0 {
		t.Fatalf("pending queue should be empty after cancellation, got %+v", pending)
	}
}

func waitForPendingRequests(t *testing.T, b *AskQuestionBroker, want int) []AskQuestionRequest {
	t.Helper()
	var pending []AskQuestionRequest
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pending = b.Pending()
		if len(pending) == want {
			return pending
		}
		time.Sleep(5 * time.Millisecond)
	}
	return pending
}

func TestApprovalBrokerAdmission(t *testing.T) {
	broker := NewAskQuestionBroker()
	broker.SetAskHandler(func(context.Context, AskQuestionRequest) (AskQuestionResolution, error) {
		return AskQuestionApproval{Decision: AskQuestionApprovalDecisionAllowOnce}, nil
	})
	for _, id := range []string{"", " call", "call "} {
		t.Run("invalid_"+id, func(t *testing.T) {
			if _, err := broker.Ask(context.Background(), testApprovalRequest(id)); err == nil {
				t.Fatalf("Approval admitted Tool Call ID %q", id)
			}
		})
	}
	ctx := testApprovalContext(context.Background(), "call")
	if _, err := broker.Ask(ctx, testApprovalRequest("call")); err != nil {
		t.Fatalf("first Approval: %v", err)
	}
	if _, err := broker.Ask(ctx, testApprovalRequest("call")); err == nil {
		t.Fatal("second Approval was admitted")
	}
}

func callAskQuestionTool(t *testing.T, b *AskQuestionBroker, id string, input string) Result {
	t.Helper()
	result, err := NewAskQuestionTool(b, nil).Call(context.Background(), Call{
		ID:    id,
		Name:  toolspec.ToolAskQuestion,
		Input: json.RawMessage(input),
	})
	if err != nil {
		t.Fatalf("unexpected call error: %v", err)
	}
	return result
}

func TestToolCallSerializesResponsesAsPlainText(t *testing.T) {
	tests := []struct {
		id, input, condensedText string
		resolution               AskQuestionResolution
	}{
		{
			"call-structured",
			`{"question":"Pick one","suggestions":["alpha","beta"],"recommended_option_index":1}`,
			"beta\nUser also said:\nneed extra context",
			AskQuestionAnswer{
				SelectedOptionNumber: textutil.Value(2),
				Freeform:             textutil.Value("need extra context"),
			},
		},
		{"call-freeform", `{"question":"What else?","suggestions":["alpha","beta"],"recommended_option_index":1}`,
			"need extra context", testQuestionAnswer("need extra context")},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			b := NewAskQuestionBroker()
			b.SetAskHandler(func(_ context.Context, req AskQuestionRequest) (AskQuestionResolution, error) {
				if req.ToolCallID != tt.id {
					t.Fatalf("request id = %q, want %q", req.ToolCallID, tt.id)
				}
				return tt.resolution, nil
			})
			result := callAskQuestionTool(t, b, tt.id, tt.input)
			if result.IsError {
				t.Fatalf("expected success result, got %+v", result)
			}
			var payload string
			if err := json.Unmarshal(result.Output, &payload); err != nil {
				t.Fatalf("decode tool output: %v", err)
			}
			if payload == "" {
				t.Fatal("expected non-empty plain-text summary")
			}
			if result.CondensedText == nil || *result.CondensedText != tt.condensedText {
				t.Fatalf("condensed text = %v, want %q", result.CondensedText, tt.condensedText)
			}
		})
	}
}

func TestToolCallNormalizesRecommendedOptionIndex(t *testing.T) {
	tests := []struct {
		id, input   string
		suggestions []string
	}{
		{"call-freeform-only", `{"question":"What else?"}`, nil},
		{"call-missing-recommended", `{"question":"Pick one","suggestions":["alpha","beta"]}`, []string{"alpha", "beta"}},
		{"call-bad-recommended", `{"question":"Pick one","suggestions":["alpha","beta"],"recommended_option_index":3}`, []string{"alpha", "beta"}},
		{"call-bad-normalized-recommended", `{"question":"Pick one","suggestions":["","beta"],"recommended_option_index":2}`, []string{"beta"}},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			b := NewAskQuestionBroker()
			b.SetAskHandler(func(_ context.Context, req AskQuestionRequest) (AskQuestionResolution, error) {
				if req.ToolCallID != tt.id {
					t.Fatalf("request id = %q, want %q", req.ToolCallID, tt.id)
				}
				if req.RecommendedOptionIndex != 0 {
					t.Fatalf("recommended option index = %d, want 0", req.RecommendedOptionIndex)
				}
				if !slices.Equal(req.Suggestions, tt.suggestions) {
					t.Fatalf("suggestions = %q, want %q", req.Suggestions, tt.suggestions)
				}
				return testQuestionAnswer("typed answer"), nil
			})
			if result := callAskQuestionTool(t, b, tt.id, tt.input); result.IsError {
				t.Fatalf("expected success result, got %+v", result)
			}
		})
	}
}

func TestToolCallRejectsApprovalPayloadReturnedByHandler(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(_ context.Context, req AskQuestionRequest) (AskQuestionResolution, error) {
		return AskQuestionApproval{Decision: AskQuestionApprovalDecisionDeny}, nil
	})
	result := callAskQuestionTool(t, b, "call-approval-payload", `{"question":"What should I do?"}`)
	if !result.IsError {
		t.Fatalf("expected error result, got %+v", result)
	}
	var payload map[string]string
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("decode error output: %v", err)
	}
	if payload["error"] != "non-approval questions must not return approval payloads" {
		t.Fatalf("unexpected error output: %q", payload["error"])
	}
}

func TestInternalRequestIsNotModelFacingJSONShape(t *testing.T) {
	encoded, err := json.Marshal(AskQuestionRequest{
		ToolCallID: "approval",
		Question:   "approve?",
		Approval:   true,
		ApprovalOptions: []AskQuestionApprovalOption{{
			Decision: AskQuestionApprovalDecisionAllowOnce,
			Label:    "Allow once",
		}},
	})
	if err != nil {
		t.Fatalf("marshal internal request: %v", err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("internal request unexpectedly serialized as %s", encoded)
	}

	encoded, err = json.Marshal(AskQuestionToolRequest{
		Question:               "pick one",
		Suggestions:            []string{"alpha", "beta"},
		RecommendedOptionIndex: 2,
	})
	if err != nil {
		t.Fatalf("marshal tool request: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode tool request json: %v", err)
	}
	if _, ok := payload["approval"]; ok {
		t.Fatalf("tool request json must not contain approval field: %s", encoded)
	}
	if _, ok := payload["approval_options"]; ok {
		t.Fatalf("tool request json must not contain approval_options field: %s", encoded)
	}
	if payload["question"] != "pick one" {
		t.Fatalf("unexpected tool request question payload: %+v", payload)
	}
}

func mustAskQuestionInput(t *testing.T, question string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(map[string]string{"question": question})
	if err != nil {
		t.Fatalf("marshal ask question input: %v", err)
	}
	return encoded
}
