package blackbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/pty/blackbox"
	"core/internal/testharness/scriptedllm"
	"core/server/llm"
	"core/shared/runtimeids"
	"core/shared/textutil"
)

func TestScriptedResponsesReconcilesCumulativeFunctionAndCustomDeliveries(t *testing.T) {
	function := llm.ToolCall{ID: "function-1", Name: "exec_command", Input: json.RawMessage(`{"cmd":"true"}`)}
	customText := "patch"
	custom := llm.ToolCall{ID: "custom-1", Name: "patch", Custom: true, CustomInput: &customText, Input: json.RawMessage(`"patch"`)}
	final := withExpected(scriptedllm.FinalAnswer("done"), custom)
	final.Response.Usage.InputTokens, final.Response.Usage.OutputTokens = 9, 4
	stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{
		scriptedllm.ToolBatch("", function),
		withExpected(scriptedllm.ToolBatch("", custom), function),
		final,
	}})
	sessionID := runtimeids.NewSessionID().String()
	provider := providerClient(t, stub)
	first := prepared(messageItem("first"))
	response1 := generate(t, provider, sessionID, first)
	if len(response1.ToolCalls) != 1 || response1.ToolCalls[0].Custom {
		t.Fatalf("function-call response = %+v", response1.ToolCalls)
	}
	functionResult := prepared(toolOutput(function, json.RawMessage(`{"ok":true}`)))
	second := appendItems(first, response1.OutputItems, functionResult)
	response2 := generate(t, provider, sessionID, second)
	if len(response2.ToolCalls) != 1 || !response2.ToolCalls[0].Custom {
		t.Fatalf("custom-call response = %+v", response2.ToolCalls)
	}
	customResult := prepared(toolOutput(custom, json.RawMessage(`{"ok":true}`)))
	third := appendItems(second, response2.OutputItems, customResult)
	response3 := generate(t, provider, sessionID, third)
	if response3.Assistant.Content == nil || *response3.Assistant.Content != "done" ||
		response3.Usage.InputTokens != 9 || response3.Usage.OutputTokens != 4 {
		t.Fatalf("terminal response = %+v", response3)
	}
	if stub.ScriptedRequestCount() != 3 || stub.RemainingScriptedSteps() != 0 {
		t.Fatalf("script observations: requests=%d remaining=%d", stub.ScriptedRequestCount(), stub.RemainingScriptedSteps())
	}
	_, err := provider.Generate(context.Background(), request(sessionID, append(third, functionResult...)))
	if err == nil {
		t.Fatal("duplicate appended delivery succeeded")
	}
	if stub.ScriptedRequestCount() != 3 {
		t.Fatalf("duplicate delivery reached script: requests=%d", stub.ScriptedRequestCount())
	}
}

func TestScriptedResponsesAcceptsHistoricalToolOutputOnFirstRequest(t *testing.T) {
	stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{
		scriptedllm.FinalAnswer("done"),
	}})
	historical := llm.ToolCall{ID: "historical", Name: "exec_command"}
	response := generate(t, providerClient(t, stub), runtimeids.NewSessionID().String(), prepared(
		messageItem("before"),
		toolOutput(historical, json.RawMessage(`{"summary":"already persisted"}`)),
		messageItem("after"),
	))
	if response.Assistant.Content == nil || *response.Assistant.Content != "done" {
		t.Fatalf("response = %+v, want final answer", response)
	}
}

func TestScriptedResponsesRejectsDuplicateHistoricalToolOutputs(t *testing.T) {
	stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{
		scriptedllm.FinalAnswer("unused"),
	}})
	historical := llm.ToolCall{ID: "historical", Name: "exec_command"}
	requireGenerateError(t, providerClient(t, stub), runtimeids.NewSessionID().String(), prepared(
		toolOutput(historical, json.RawMessage(`{"first":true}`)),
		toolOutput(historical, json.RawMessage(`{"second":true}`)),
	))
	if stub.ScriptedRequestCount() != 0 {
		t.Fatalf("duplicate historical IDs reached script: requests=%d", stub.ScriptedRequestCount())
	}
}

func TestScriptedResponsesRejectsLaterReuseOfHistoricalCallID(t *testing.T) {
	historical := llm.ToolCall{ID: "historical", Name: "exec_command"}
	stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{
		scriptedllm.ToolBatch("", historical),
	}})
	requireGenerateError(t, providerClient(t, stub), runtimeids.NewSessionID().String(), prepared(
		toolOutput(historical, json.RawMessage(`{"already":true}`)),
	))
	if stub.ScriptedRequestCount() != 1 || stub.RemainingScriptedSteps() != 0 {
		t.Fatalf(
			"historical reuse observations: requests=%d remaining=%d",
			stub.ScriptedRequestCount(),
			stub.RemainingScriptedSteps(),
		)
	}
}

func TestScriptedResponsesIsolatesMainAndReviewerLineages(t *testing.T) {
	mainCall := llm.ToolCall{ID: "shared", Name: "exec_command", Input: json.RawMessage(`{}`)}
	reviewerCall := llm.ToolCall{ID: "shared", Name: "patch", Custom: true, Input: json.RawMessage(`"x"`), CustomInput: textutil.Value("x")}
	stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{
		scriptedllm.ToolBatch("", mainCall),
		scriptedllm.ToolBatch("", reviewerCall),
		withExpected(scriptedllm.FinalAnswer("main"), mainCall),
		withExpected(scriptedllm.FinalAnswer("review"), reviewerCall),
	}})
	provider := providerClient(t, stub)
	base := runtimeids.NewSessionID().String()
	mainInput := prepared(messageItem("main"))
	reviewerInput := prepared(messageItem("review"))
	mainResponse := generate(t, provider, base, mainInput)
	reviewerResponse := generate(t, provider, base+"/supervisor", reviewerInput)
	generate(t, provider, base, appendItems(mainInput, mainResponse.OutputItems, prepared(toolOutput(mainCall, json.RawMessage(`{}`)))))
	generate(t, provider, base+"/supervisor", appendItems(reviewerInput, reviewerResponse.OutputItems, prepared(toolOutput(reviewerCall, json.RawMessage(`{}`)))))
}

func TestScriptedResponsesRejectsConcurrentSameLineageWithoutCommittingRejectedInput(t *testing.T) {
	release := make(chan struct{})
	call := llm.ToolCall{ID: "call", Name: "exec_command", Input: json.RawMessage(`{}`)}
	firstStep := scriptedllm.ToolBatch("", call)
	firstStep.BeforeResponse = func(context.Context) error {
		<-release
		return nil
	}
	stub := startScriptedStub(t, scriptedllm.Script{
		AllowConcurrent: true,
		Steps: []scriptedllm.Step{
			firstStep,
			withExpected(scriptedllm.FinalAnswer("done"), call),
		},
	})
	provider := providerClient(t, stub)
	sessionID := runtimeids.NewSessionID().String()
	firstInput := prepared(messageItem("first"))
	type result struct {
		response llm.Response
		err      error
	}
	firstResult := make(chan result, 1)
	go func() {
		response, err := provider.(llm.StreamEventsClient).GenerateStreamWithEvents(
			context.Background(),
			request(sessionID, firstInput),
			llm.StreamCallbacks{},
		)
		firstResult <- result{response: response, err: err}
	}()
	if err := stub.WaitUntilScriptedActive(context.Background()); err != nil {
		t.Fatalf("WaitUntilScriptedActive: %v", err)
	}

	if _, err := provider.(llm.StreamEventsClient).GenerateStreamWithEvents(
		context.Background(),
		request(sessionID, prepared(messageItem("rejected concurrent input"))),
		llm.StreamCallbacks{},
	); err == nil {
		t.Fatal("concurrent same-lineage request succeeded")
	}
	if stub.ScriptedRequestCount() != 1 || stub.RemainingScriptedSteps() != 1 {
		t.Fatalf(
			"rejected request changed script admission: requests=%d remaining=%d",
			stub.ScriptedRequestCount(),
			stub.RemainingScriptedSteps(),
		)
	}

	close(release)
	first := <-firstResult
	if first.err != nil {
		t.Fatalf("first request: %v", first.err)
	}
	finalInput := appendItems(
		firstInput,
		first.response.OutputItems,
		prepared(toolOutput(call, json.RawMessage(`{"ok":true}`))),
	)
	final := generate(t, provider, sessionID, finalInput)
	if final.Assistant.Content == nil || *final.Assistant.Content != "done" {
		t.Fatalf("final response = %+v", final)
	}
	if err := stub.Verify(); err != nil {
		t.Fatalf("Verify after rejected concurrent request: %v", err)
	}
}

func TestScriptedResponsesDoesNotCommitGloballyRejectedConcurrentLineage(t *testing.T) {
	release := make(chan struct{})
	firstStep := scriptedllm.FinalAnswer("first")
	firstStep.BeforeResponse = func(context.Context) error {
		<-release
		return nil
	}
	stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{
		firstStep,
		scriptedllm.FinalAnswer("second"),
	}})
	provider := providerClient(t, stub).(llm.StreamEventsClient)
	activeSession := runtimeids.NewSessionID().String()
	rejectedSession := runtimeids.NewSessionID().String()
	firstErr := make(chan error, 1)
	go func() {
		_, err := provider.GenerateStreamWithEvents(
			context.Background(),
			request(activeSession, prepared(messageItem("active"))),
			llm.StreamCallbacks{},
		)
		firstErr <- err
	}()
	if err := stub.WaitUntilScriptedActive(context.Background()); err != nil {
		t.Fatalf("WaitUntilScriptedActive: %v", err)
	}

	if _, err := provider.GenerateStreamWithEvents(
		context.Background(),
		request(rejectedSession, prepared(messageItem("rejected"))),
		llm.StreamCallbacks{},
	); err == nil {
		t.Fatal("globally concurrent request succeeded")
	}
	close(release)
	if err := <-firstErr; err != nil {
		t.Fatalf("active request: %v", err)
	}

	response, err := provider.GenerateStreamWithEvents(
		context.Background(),
		request(rejectedSession, prepared(messageItem("accepted retry"))),
		llm.StreamCallbacks{},
	)
	if err != nil {
		t.Fatalf("retry after non-admission: %v", err)
	}
	if response.Assistant.Content == nil || *response.Assistant.Content != "second" {
		t.Fatalf("retry response = %+v", response)
	}
	if err := stub.Verify(); err != nil {
		t.Fatalf("Verify after non-admitted concurrency: %v", err)
	}
}

func TestScriptedResponsesPreservesConcurrentMainAndSupervisorLineages(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	blockingFinal := func(content string) scriptedllm.Step {
		step := scriptedllm.FinalAnswer(content)
		step.BeforeResponse = func(context.Context) error {
			entered <- struct{}{}
			<-release
			return nil
		}
		return step
	}
	stub := startScriptedStub(t, scriptedllm.Script{
		AllowConcurrent: true,
		Steps: []scriptedllm.Step{
			blockingFinal("main"),
			blockingFinal("supervisor"),
		},
	})
	provider := providerClient(t, stub)
	sessionID := runtimeids.NewSessionID().String()
	errs := make(chan error, 2)
	for _, lineage := range []string{sessionID, sessionID + "/supervisor"} {
		go func() {
			_, err := provider.(llm.StreamEventsClient).GenerateStreamWithEvents(
				context.Background(),
				request(lineage, prepared(messageItem(lineage))),
				llm.StreamCallbacks{},
			)
			errs <- err
		}()
	}
	<-entered
	<-entered
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent independent lineage: %v", err)
		}
	}
	if stub.ScriptedRequestCount() != 2 || stub.RemainingScriptedSteps() != 0 {
		t.Fatalf(
			"concurrent lineage observations: requests=%d remaining=%d",
			stub.ScriptedRequestCount(),
			stub.RemainingScriptedSteps(),
		)
	}
}

func TestScriptedResponsesRejectsInvalidCumulativeInputAndDeliveries(t *testing.T) {
	t.Run("changed prefix", func(t *testing.T) {
		stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{
			scriptedllm.FinalAnswer("one"), scriptedllm.FinalAnswer("two"),
		}})
		provider := providerClient(t, stub)
		id := runtimeids.NewSessionID().String()
		generate(t, provider, id, prepared(messageItem("one")))
		requireGenerateError(t, provider, id, prepared(messageItem("changed")))
	})
	t.Run("shortened prefix", func(t *testing.T) {
		stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{
			scriptedllm.FinalAnswer("one"), scriptedllm.FinalAnswer("two"),
		}})
		provider := providerClient(t, stub)
		id := runtimeids.NewSessionID().String()
		two := prepared(messageItem("one"), messageItem("two"))
		generate(t, provider, id, two)
		requireGenerateError(t, provider, id, two[:1])
	})
	t.Run("unknown call ID", func(t *testing.T) {
		stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{scriptedllm.FinalAnswer("unused")}})
		requireGenerateError(t, providerClient(t, stub), runtimeids.NewSessionID().String(), prepared(toolOutput(
			llm.ToolCall{ID: "unknown", Name: "exec_command"}, json.RawMessage(`{}`),
		)))
	})
	t.Run("wrong output kind", func(t *testing.T) {
		call := llm.ToolCall{ID: "call", Name: "exec_command", Input: json.RawMessage(`{}`)}
		stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{
			scriptedllm.ToolBatch("", call), scriptedllm.FinalAnswer("unused"),
		}})
		provider := providerClient(t, stub)
		id := runtimeids.NewSessionID().String()
		input := prepared(messageItem("one"))
		response := generate(t, provider, id, input)
		wrong := call
		wrong.Custom = true
		requireGenerateError(t, provider, id, appendItems(input, response.OutputItems, prepared(toolOutput(wrong, json.RawMessage(`{}`)))))
	})
}

func TestScriptedResponsesHandlesMultipleResultsStreamingErrorsAndMetadata(t *testing.T) {
	t.Run("multiple results and delayed flush", func(t *testing.T) {
		first := llm.ToolCall{ID: "one", Name: "exec_command", Input: json.RawMessage(`{}`)}
		second := llm.ToolCall{ID: "two", Name: "patch", Custom: true, Input: json.RawMessage(`"x"`), CustomInput: textutil.Value("x")}
		delay := 120 * time.Millisecond
		final := withExpected(scriptedllm.FinalAnswer("done"), first, second)
		final.StreamDeltas, final.StreamDeltaDelay = []llm.AssistantDelta{{Text: "do", Phase: llm.MessagePhaseFinal}, {Text: "ne", Phase: llm.MessagePhaseFinal}}, &delay
		stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{scriptedllm.ToolBatch("", first, second), final}})
		provider := providerClient(t, stub)
		id := runtimeids.NewSessionID().String()
		input := prepared(messageItem("go"))
		calls := generate(t, provider, id, input)
		var deltas []string
		started := time.Now()
		result, err := provider.(llm.StreamEventsClient).GenerateStreamWithEvents(context.Background(), request(id,
			appendItems(input, calls.OutputItems,
				prepared(toolOutput(first, json.RawMessage(`{}`))),
				prepared(toolOutput(second, json.RawMessage(`{}`))),
			)), llm.StreamCallbacks{OnAssistantDelta: func(delta llm.AssistantDelta) { deltas = append(deltas, delta.Text) }})
		if err != nil || result.Assistant.Content == nil || *result.Assistant.Content != "done" {
			t.Fatalf("streamed result = %+v, %v", result, err)
		}
		if len(deltas) != 2 || time.Since(started) < delay {
			t.Fatalf("deltas=%v elapsed=%s, want both flushed around delay %s", deltas, time.Since(started), delay)
		}
	})
	t.Run("scripted error", func(t *testing.T) {
		stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{scriptedllm.RuntimeError(errors.New("declared"))}})
		requireGenerateError(t, providerClient(t, stub), runtimeids.NewSessionID().String(), prepared(messageItem("fail")))
	})
	t.Run("request cancellation", func(t *testing.T) {
		stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{{
			BeforeResponse: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
			Response:       scriptedllm.FinalAnswer("unused").Response,
		}}})
		ctx, cancel := context.WithCancel(context.Background())
		errs := make(chan error, 1)
		go func() {
			_, err := providerClient(t, stub).(llm.StreamEventsClient).GenerateStreamWithEvents(
				ctx, request(runtimeids.NewSessionID().String(), prepared(messageItem("cancel"))), llm.StreamCallbacks{},
			)
			errs <- err
		}()
		if err := stub.WaitUntilScriptedActive(context.Background()); err != nil {
			t.Fatalf("WaitUntilActive: %v", err)
		}
		cancel()
		err := <-errs
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
		waitForNoActiveRequests(t, stub)
		if err := stub.Verify(); !errors.Is(err, context.Canceled) {
			t.Fatalf("Verify cancellation error = %v, want context.Canceled", err)
		}
	})
	t.Run("tokens model and compaction", func(t *testing.T) {
		tokens, window := 23, 123456
		stub := startScriptedStub(t, scriptedllm.Script{InputTokenCount: &tokens, ContextWindowTokens: &window})
		provider := providerClientWithWindow(t, stub, 0)
		count, err := provider.(llm.RequestInputTokenCountClient).CountRequestInputTokens(context.Background(), request("", prepared(messageItem("count"))))
		if err != nil || count != tokens {
			t.Fatalf("input tokens = %d, %v", count, err)
		}
		modelResponse, err := http.Get(stub.URL() + "/models/gpt-5")
		if err != nil {
			t.Fatalf("GET model: %v", err)
		}
		defer modelResponse.Body.Close()
		var model struct {
			ContextWindow int `json:"context_window"`
		}
		if err := json.NewDecoder(modelResponse.Body).Decode(&model); err != nil || model.ContextWindow != window {
			t.Fatalf("model metadata = %+v, %v", model, err)
		}
		response, err := http.Post(stub.URL()+"/responses/compact", "application/json", strings.NewReader(`{"input":[]}`))
		if err != nil {
			t.Fatalf("POST compact: %v", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("compact status = %d", response.StatusCode)
		}
	})
}

func TestScriptedResponsesSetsSSEContentTypeBeforeStreaming(t *testing.T) {
	step := scriptedllm.FinalAnswer("done")
	step.StreamDeltas = []llm.AssistantDelta{{Text: "done", Phase: llm.MessagePhaseFinal}}
	stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{step}})
	request, err := http.NewRequest(
		http.MethodPost,
		stub.URL()+"/responses",
		strings.NewReader(`{"model":"gpt-5","input":[]}`),
	)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	request.Header.Set("session_id", runtimeids.NewSessionID().String())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST responses: %v", err)
	}
	defer response.Body.Close()
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
}

func TestScriptedResponsesVerifyRecordsConsumedRequestFailure(t *testing.T) {
	stub := startScriptedStub(t, scriptedllm.Script{Steps: []scriptedllm.Step{
		scriptedllm.RuntimeError(errors.New("declared")),
	}})
	requireGenerateError(t, providerClient(t, stub), runtimeids.NewSessionID().String(), prepared(messageItem("fail")))
	if err := stub.Verify(); err == nil {
		t.Fatal("Verify accepted a consumed scripted request failure")
	}
}

func TestScriptedResponsesDoesNotRecordExhaustionAsConsumedFailure(t *testing.T) {
	stub := startScriptedStub(t, scriptedllm.Script{})
	requireGenerateError(t, providerClient(t, stub), runtimeids.NewSessionID().String(), prepared(messageItem("exhausted")))
	waitForNoActiveRequests(t, stub)
	if err := stub.Verify(); err != nil {
		t.Fatalf("Verify after non-admitted exhaustion: %v", err)
	}
}

func startScriptedStub(t *testing.T, script scriptedllm.Script) *blackbox.ResponsesStub {
	t.Helper()
	stub, err := blackbox.StartScriptedResponsesStub(script)
	if err != nil {
		t.Fatalf("StartScriptedResponsesStub: %v", err)
	}
	t.Cleanup(func() { _ = stub.Stop() })
	return stub
}

func providerClient(t *testing.T, stub *blackbox.ResponsesStub) llm.Client {
	return providerClientWithWindow(t, stub, 200000)
}

func providerClientWithWindow(t *testing.T, stub *blackbox.ResponsesStub, window int) llm.Client {
	t.Helper()
	client, err := llm.NewProviderClient(llm.ProviderClientOptions{
		Provider: llm.ProviderOpenAI, Model: "gpt-5", OpenAIBaseURL: stub.URL(), ContextWindowTokens: window,
	})
	if err != nil {
		t.Fatalf("NewProviderClient: %v", err)
	}
	return client
}

func request(sessionID string, items []llm.ResponseItem) llm.Request {
	return llm.Request{Model: "gpt-5", SessionID: sessionID, Items: items, ToolChoiceMode: llm.ToolChoiceModeAutomatic}
}

func generate(t *testing.T, client llm.Client, sessionID string, items []llm.ResponseItem) llm.Response {
	t.Helper()
	response, err := client.(llm.StreamEventsClient).GenerateStreamWithEvents(
		context.Background(), request(sessionID, items), llm.StreamCallbacks{},
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return response
}

func requireGenerateError(t *testing.T, client llm.Client, sessionID string, items []llm.ResponseItem) {
	t.Helper()
	if _, err := client.Generate(context.Background(), request(sessionID, items)); err == nil {
		t.Fatal("Generate succeeded")
	}
}

func waitForNoActiveRequests(t *testing.T, stub *blackbox.ResponsesStub) {
	t.Helper()
	for stub.Snapshot().ActiveRequests != 0 {
		select {
		case <-stub.Events():
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for scripted request completion")
		}
	}
}

func messageItem(text string) llm.ResponseItem {
	return llm.ResponseItem{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), Content: &text}
}

func toolOutput(call llm.ToolCall, output json.RawMessage) llm.ResponseItem {
	return llm.ResponseItem{Type: llm.ToolOutputItemType(call.Custom), CallID: &call.ID, Output: output}
}

func prepared(items ...llm.ResponseItem) []llm.ResponseItem {
	return llm.PrepareOpenAIInputItems(items)
}

func appendItems(groups ...[]llm.ResponseItem) []llm.ResponseItem {
	var items []llm.ResponseItem
	for _, group := range groups {
		items = append(items, group...)
	}
	return items
}

func withExpected(step scriptedllm.Step, calls ...llm.ToolCall) scriptedllm.Step {
	for _, call := range calls {
		step.ExpectedToolResults = append(step.ExpectedToolResults, scriptedllm.ExpectedToolResult{CallID: call.ID, Name: call.Name})
	}
	return step
}
