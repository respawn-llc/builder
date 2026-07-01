package tools

import (
	"context"
	"core/shared/toolspec"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestBrokerFIFOQueue(t *testing.T) {
	b := NewAskQuestionBroker()

	ctx := context.Background()
	type out struct {
		id   string
		resp AskQuestionResponse
		err  error
	}
	ch := make(chan out, 2)

	go func() {
		resp, err := b.Ask(ctx, AskQuestionRequest{ID: "q1", Question: "one?"})
		ch <- out{id: "q1", resp: resp, err: err}
	}()
	for i := 0; i < 100; i++ {
		if len(b.Pending()) == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	go func() {
		resp, err := b.Ask(ctx, AskQuestionRequest{ID: "q2", Question: "two?"})
		ch <- out{id: "q2", resp: resp, err: err}
	}()

	time.Sleep(10 * time.Millisecond)
	pending := b.Pending()
	if len(pending) != 2 {
		t.Fatalf("pending count = %d", len(pending))
	}
	if pending[0].ID != "q1" || pending[1].ID != "q2" {
		t.Fatalf("pending not fifo: %+v", pending)
	}

	if err := b.Submit("q1", AskQuestionResponse{Answer: "a1"}); err != nil {
		t.Fatalf("submit q1: %v", err)
	}
	if err := b.Submit("q2", AskQuestionResponse{Answer: "a2"}); err != nil {
		t.Fatalf("submit q2: %v", err)
	}

	got := map[string]string{}
	for i := 0; i < 2; i++ {
		item := <-ch
		if item.err != nil {
			t.Fatalf("ask result err: %v", item.err)
		}
		got[item.id] = item.resp.Answer
	}

	if got["q1"] != "a1" || got["q2"] != "a2" {
		t.Fatalf("unexpected answers: %+v", got)
	}
}

func TestSubmitApprovalResponse(t *testing.T) {
	b := NewAskQuestionBroker()
	ctx := context.Background()
	type out struct {
		resp AskQuestionResponse
		err  error
	}
	done := make(chan out, 1)

	go func() {
		resp, err := b.Ask(ctx, AskQuestionRequest{ID: "approval", Question: "approve?", Approval: true, ApprovalOptions: []AskQuestionApprovalOption{{Decision: AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"}, {Decision: AskQuestionApprovalDecisionAllowSession, Label: "Allow for this session"}, {Decision: AskQuestionApprovalDecisionDeny, Label: "Deny"}}})
		done <- out{resp: resp, err: err}
	}()

	for i := 0; i < 100; i++ {
		if len(b.Pending()) == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	approval := &AskQuestionApprovalPayload{Decision: AskQuestionApprovalDecisionAllowSession, Commentary: "trusted path"}
	if err := b.Submit("approval", AskQuestionResponse{Approval: approval}); err != nil {
		t.Fatalf("submit approval: %v", err)
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("ask approval: %v", result.err)
		}
		if result.resp.RequestID != "approval" {
			t.Fatalf("request id = %q, want approval", result.resp.RequestID)
		}
		if result.resp.Approval == nil || *result.resp.Approval != *approval {
			t.Fatalf("approval payload = %+v, want %+v", result.resp.Approval, approval)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval response")
	}
}

func TestApprovalAskRequiresApprovalOptions(t *testing.T) {
	b := NewAskQuestionBroker()
	_, err := b.Ask(context.Background(), AskQuestionRequest{ID: "approval", Question: "approve?", Approval: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAskQuestionApprovalRequiresOptions) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApprovalAskIgnoresRecommendedOptionIndex(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		if req.RecommendedOptionIndex != 0 {
			t.Fatalf("expected recommended option index ignored for approval ask, got %+v", req)
		}
		return AskQuestionResponse{RequestID: req.ID, Approval: &AskQuestionApprovalPayload{Decision: AskQuestionApprovalDecisionAllowOnce}}, nil
	})

	resp, err := b.Ask(context.Background(), AskQuestionRequest{
		ID:                     "approval",
		Question:               "approve?",
		Approval:               true,
		RecommendedOptionIndex: 1,
		ApprovalOptions: []AskQuestionApprovalOption{
			{Decision: AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
			{Decision: AskQuestionApprovalDecisionAllowSession, Label: "Allow for this session"},
			{Decision: AskQuestionApprovalDecisionDeny, Label: "Deny"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Approval == nil || resp.Approval.Decision != AskQuestionApprovalDecisionAllowOnce {
		t.Fatalf("unexpected approval response: %+v", resp)
	}
}

func TestApprovalAskRejectsSuggestions(t *testing.T) {
	b := NewAskQuestionBroker()
	_, err := b.Ask(context.Background(), AskQuestionRequest{
		ID:       "approval",
		Question: "approve?",
		Approval: true,
		Suggestions: []string{
			"do not use suggestions here",
		},
		ApprovalOptions: []AskQuestionApprovalOption{
			{Decision: AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
			{Decision: AskQuestionApprovalDecisionAllowSession, Label: "Allow for this session"},
			{Decision: AskQuestionApprovalDecisionDeny, Label: "Deny"},
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAskQuestionApprovalForbidsSuggestions) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSuggestionAskAllowsOmittedRecommendedOptionIndexAtRequestLayer(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		if req.RecommendedOptionIndex != 0 {
			t.Fatalf("did not expect recommended option index, got %+v", req)
		}
		return AskQuestionResponse{RequestID: req.ID, FreeformAnswer: "typed answer"}, nil
	})

	resp, err := b.Ask(context.Background(), AskQuestionRequest{
		ID:          "pick-one",
		Question:    "pick one",
		Suggestions: []string{"alpha", "beta"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.FreeformAnswer != "typed answer" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestSuggestionAskIgnoresOutOfRangeRecommendedOptionIndexAtRequestLayer(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		if req.RecommendedOptionIndex != 0 {
			t.Fatalf("expected out-of-range recommendation to be ignored, got %+v", req)
		}
		return AskQuestionResponse{RequestID: req.ID, FreeformAnswer: "typed answer"}, nil
	})

	resp, err := b.Ask(context.Background(), AskQuestionRequest{
		ID:                     "pick-one",
		Question:               "pick one",
		Suggestions:            []string{"alpha", "beta"},
		RecommendedOptionIndex: 3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.FreeformAnswer != "typed answer" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestSuggestionAskIgnoresRecommendedIndexAfterBlankSuggestionsAreDropped(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		if req.RecommendedOptionIndex != 0 {
			t.Fatalf("expected invalid recommendation to be ignored after normalization, got %+v", req)
		}
		if len(req.Suggestions) != 1 || req.Suggestions[0] != "beta" {
			t.Fatalf("expected suggestions normalized before handler, got %+v", req)
		}
		return AskQuestionResponse{RequestID: req.ID, FreeformAnswer: "typed answer"}, nil
	})

	resp, err := b.Ask(context.Background(), AskQuestionRequest{
		ID:                     "pick-one",
		Question:               "pick one",
		Suggestions:            []string{"", "beta"},
		RecommendedOptionIndex: 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.FreeformAnswer != "typed answer" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestFreeformOnlyAskAllowsOmittedRecommendedOptionIndexAtRequestLayer(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		return AskQuestionResponse{RequestID: req.ID, FreeformAnswer: "typed answer"}, nil
	})

	resp, err := b.Ask(context.Background(), AskQuestionRequest{ID: "freeform", Question: "what else?"})
	if err != nil {
		t.Fatalf("unexpected ask error: %v", err)
	}
	if resp.FreeformAnswer != "typed answer" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestFreeformAskRejectsEmptyResponse(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		return AskQuestionResponse{RequestID: req.ID}, nil
	})

	_, err := b.Ask(context.Background(), AskQuestionRequest{ID: "freeform", Question: "what else?"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAskQuestionNonApprovalRequiresAnswer) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSubmitRejectsPlainStringResponseForApprovalAsk(t *testing.T) {
	b := NewAskQuestionBroker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type out struct {
		resp AskQuestionResponse
		err  error
	}
	done := make(chan out, 1)
	approvalReq := AskQuestionRequest{
		ID:       "approval",
		Question: "approve?",
		Approval: true,
		ApprovalOptions: []AskQuestionApprovalOption{
			{Decision: AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
			{Decision: AskQuestionApprovalDecisionAllowSession, Label: "Allow for this session"},
			{Decision: AskQuestionApprovalDecisionDeny, Label: "Deny"},
		},
	}

	go func() {
		resp, err := b.Ask(ctx, approvalReq)
		done <- out{resp: resp, err: err}
	}()

	for i := 0; i < 100; i++ {
		if len(b.Pending()) == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	if err := b.Submit("approval", AskQuestionResponse{Answer: "allow once"}); err == nil {
		t.Fatal("expected submit error for plain-string approval response")
	} else if !errors.Is(err, ErrAskQuestionApprovalRequiresResponse) {
		t.Fatalf("unexpected submit error: %v", err)
	}

	valid := &AskQuestionApprovalPayload{Decision: AskQuestionApprovalDecisionAllowOnce}
	if err := b.Submit("approval", AskQuestionResponse{Approval: valid}); err != nil {
		t.Fatalf("submit valid approval: %v", err)
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("ask approval: %v", result.err)
		}
		if result.resp.Approval == nil || *result.resp.Approval != *valid {
			t.Fatalf("approval payload = %+v, want %+v", result.resp.Approval, valid)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval response")
	}
}

func TestAskHandlerRejectsPlainStringResponseForApprovalAsk(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(AskQuestionRequest) (AskQuestionResponse, error) {
		return AskQuestionResponse{Answer: "allow once"}, nil
	})

	_, err := b.Ask(context.Background(), AskQuestionRequest{
		ID:       "approval",
		Question: "approve?",
		Approval: true,
		ApprovalOptions: []AskQuestionApprovalOption{
			{Decision: AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
			{Decision: AskQuestionApprovalDecisionAllowSession, Label: "Allow for this session"},
			{Decision: AskQuestionApprovalDecisionDeny, Label: "Deny"},
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAskQuestionApprovalRequiresResponse) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAskHandlerModeDoesNotQueuePendingRequest(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		return AskQuestionResponse{RequestID: req.ID, Answer: "handled"}, nil
	})

	resp, err := b.Ask(context.Background(), AskQuestionRequest{ID: "sync", Question: "one?"})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if resp.Answer != "handled" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if pending := b.Pending(); len(pending) != 0 {
		t.Fatalf("expected no pending requests in handler mode, got %+v", pending)
	}
	if err := b.Submit("sync", AskQuestionResponse{Answer: "late"}); err == nil {
		t.Fatal("expected submit to reject non-queued sync request")
	}
}

func TestSubmitRejectsSecondCompletionForQueuedRequest(t *testing.T) {
	b := NewAskQuestionBroker()
	ctx := context.Background()
	done := make(chan error, 1)

	go func() {
		_, err := b.Ask(ctx, AskQuestionRequest{ID: "q1", Question: "one?"})
		done <- err
	}()

	for i := 0; i < 100; i++ {
		if len(b.Pending()) == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	if err := b.Submit("q1", AskQuestionResponse{Answer: "a1"}); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if err := b.Submit("q1", AskQuestionResponse{Answer: "a2"}); err == nil {
		t.Fatal("expected second submit to fail")
	}
	if err := <-done; err != nil {
		t.Fatalf("ask result err: %v", err)
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
	if pending[0].ID != "call-queued" {
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

	if err := b.Submit("call-queued", AskQuestionResponse{SelectedOptionNumber: 2, FreeformAnswer: "need extra context"}); err != nil {
		t.Fatalf("submit answer: %v", err)
	}
	if err := b.Submit("call-queued", AskQuestionResponse{SelectedOptionNumber: 1}); err == nil {
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
		if output != "User chose option #2. They also said: need extra context" {
			t.Fatalf("unexpected tool output summary: %q", output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued tool answer")
	}

	if pending := b.Pending(); len(pending) != 0 {
		t.Fatalf("expected queue drained after completion, got %+v", pending)
	}
}

func TestAskHandlerModeHonorsCanceledContextBeforeInvocation(t *testing.T) {
	b := NewAskQuestionBroker()
	called := false
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		called = true
		return AskQuestionResponse{RequestID: req.ID, Answer: "handled"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := b.Ask(ctx, AskQuestionRequest{ID: "sync", Question: "one?"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	if called {
		t.Fatal("expected handler not to be called after context cancellation")
	}
}

func TestAskHandlerModePrefersContextCancellationAfterHandlerReturns(t *testing.T) {
	b := NewAskQuestionBroker()
	release := make(chan struct{})
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		<-release
		return AskQuestionResponse{RequestID: req.ID, Answer: "handled"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := b.Ask(ctx, AskQuestionRequest{ID: "sync", Question: "one?"})
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
		_, err := b.Ask(ctx, AskQuestionRequest{ID: "q-cancel", Question: "will cancel?"})
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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pending := b.Pending()
		if len(pending) == want {
			return pending
		}
		time.Sleep(5 * time.Millisecond)
	}
	return b.Pending()
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

func TestToolCallRejectsActionField(t *testing.T) {
	result := callAskQuestionTool(t, NewAskQuestionBroker(), "call-1", `{"question":"pick one","action":{"id":"unsafe"}}`)
	if !result.IsError {
		t.Fatalf("expected error result, got %+v", result)
	}
	var payload map[string]string
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("decode error output: %v", err)
	}
	if payload["error"] != `invalid input: field "action" is not allowed` {
		t.Fatalf("expected action rejection message, got %q", payload["error"])
	}
}

func TestToolCallSerializesSelectedOptionWithFreeformAsPlainText(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		return AskQuestionResponse{RequestID: req.ID, SelectedOptionNumber: 2, FreeformAnswer: "need extra context"}, nil
	})
	result := callAskQuestionTool(t, b, "call-structured", `{
			"question":"Pick one",
			"suggestions":["alpha","beta"],
			"recommended_option_index":1
		}`)
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
	if result.CondensedText != "beta\nUser also said:\nneed extra context" {
		t.Fatalf("unexpected ongoing text: %q", result.CondensedText)
	}
}

func TestToolCallSerializesPureFreeformAsPlainText(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		return AskQuestionResponse{RequestID: req.ID, FreeformAnswer: "need extra context"}, nil
	})
	result := callAskQuestionTool(t, b, "call-freeform", `{
			"question":"What else?",
			"suggestions":["alpha","beta"],
			"recommended_option_index":1
		}`)
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
	if result.CondensedText != "need extra context" {
		t.Fatalf("expected ongoing freeform answer without model prefix, got %q", result.CondensedText)
	}
}

func TestToolCallCondensedTextPreservesLiteralUserAnsweredFreeformPrefix(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		return AskQuestionResponse{RequestID: req.ID, FreeformAnswer: "User answered: keep going"}, nil
	})
	result := callAskQuestionTool(t, b, "call-freeform-literal-prefix", `{"question":"What else?"}`)
	if result.IsError {
		t.Fatalf("expected success result, got %+v", result)
	}
	if result.CondensedText != "User answered: keep going" {
		t.Fatalf("expected ongoing freeform answer to preserve literal prefix, got %q", result.CondensedText)
	}
	var payload string
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("decode tool output: %v", err)
	}
	if payload != "User answered: User answered: keep going" {
		t.Fatalf("expected model-facing payload to keep summary prefix, got %q", payload)
	}
}

func TestToolCallAllowsFreeformOnlyWithoutRecommendedOptionIndex(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		if req.RecommendedOptionIndex != 0 {
			t.Fatalf("did not expect recommended option index for freeform ask, got %+v", req)
		}
		return AskQuestionResponse{RequestID: req.ID, FreeformAnswer: "typed answer"}, nil
	})
	result := callAskQuestionTool(t, b, "call-freeform-only", `{"question":"What else?"}`)
	if result.IsError {
		t.Fatalf("expected success result, got %+v", result)
	}
}

func TestToolCallAllowsSuggestionAskWithoutRecommendedOptionIndex(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		if req.RecommendedOptionIndex != 0 {
			t.Fatalf("did not expect recommended option index, got %+v", req)
		}
		return AskQuestionResponse{RequestID: req.ID, FreeformAnswer: "typed answer"}, nil
	})
	result := callAskQuestionTool(t, b, "call-missing-recommended", `{
			"question":"Pick one",
			"suggestions":["alpha","beta"]
		}`)
	if result.IsError {
		t.Fatalf("expected success result, got %+v", result)
	}
}

func TestToolCallIgnoresOutOfRangeRecommendedOptionIndex(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		if req.RecommendedOptionIndex != 0 {
			t.Fatalf("expected out-of-range recommendation to be ignored, got %+v", req)
		}
		return AskQuestionResponse{RequestID: req.ID, FreeformAnswer: "typed answer"}, nil
	})
	result := callAskQuestionTool(t, b, "call-bad-recommended", `{
			"question":"Pick one",
			"suggestions":["alpha","beta"],
			"recommended_option_index":3
		}`)
	if result.IsError {
		t.Fatalf("expected success result, got %+v", result)
	}
}

func TestToolCallIgnoresRecommendedIndexAfterBlankSuggestionsAreDropped(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		if req.RecommendedOptionIndex != 0 {
			t.Fatalf("expected invalid recommendation to be ignored after normalization, got %+v", req)
		}
		if len(req.Suggestions) != 1 || req.Suggestions[0] != "beta" {
			t.Fatalf("expected normalized suggestions, got %+v", req)
		}
		return AskQuestionResponse{RequestID: req.ID, FreeformAnswer: "typed answer"}, nil
	})
	result := callAskQuestionTool(t, b, "call-bad-normalized-recommended", `{
			"question":"Pick one",
			"suggestions":["", "beta"],
			"recommended_option_index":2
		}`)
	if result.IsError {
		t.Fatalf("expected success result, got %+v", result)
	}
}

func TestToolCallRejectsApprovalField(t *testing.T) {
	result := callAskQuestionTool(t, NewAskQuestionBroker(), "call-approval", `{"question":"Approve?","approval":true}`)
	if !result.IsError {
		t.Fatalf("expected error result, got %+v", result)
	}
	var payload map[string]string
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("decode error output: %v", err)
	}
	if payload["error"] != `invalid input: field "approval" is not allowed` {
		t.Fatalf("unexpected error output: %q", payload["error"])
	}
}

func TestToolCallRejectsApprovalOptionsField(t *testing.T) {
	result := callAskQuestionTool(t, NewAskQuestionBroker(), "call-approval-options", `{
			"question":"Approve?",
			"approval_options":[{"decision":"allow_once","label":"Allow once"}]
		}`)
	if !result.IsError {
		t.Fatalf("expected error result, got %+v", result)
	}
	var payload map[string]string
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("decode error output: %v", err)
	}
	if payload["error"] != `invalid input: field "approval_options" is not allowed` {
		t.Fatalf("unexpected error output: %q", payload["error"])
	}
}

func TestToolCallRejectsApprovalPayloadReturnedByHandler(t *testing.T) {
	b := NewAskQuestionBroker()
	b.SetAskHandler(func(req AskQuestionRequest) (AskQuestionResponse, error) {
		return AskQuestionResponse{RequestID: req.ID, Approval: &AskQuestionApprovalPayload{Decision: AskQuestionApprovalDecisionDeny}}, nil
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
		ID:       "approval",
		Question: "approve?",
		Approval: true,
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

func TestBuildToolOutputSummaryRejectsEmptyNonApprovalResponse(t *testing.T) {
	_, err := buildToolOutputSummary(AskQuestionResponse{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAskQuestionNonApprovalRequiresAnswer) {
		t.Fatalf("unexpected error: %v", err)
	}
}
