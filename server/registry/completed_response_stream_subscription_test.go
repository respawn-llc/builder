package registry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"core/internal/testharness/scriptedllm"
	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/runtime"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestSessionTranscriptSubscriptionContinuesAfterConcurrentLocalToolsWithoutStreamCollision(t *testing.T) {
	handler := newControlledToolHandler("call-a", "call-b")
	firstStep := scriptedllm.ToolBatch("",
		llm.ToolCall{ID: "call-a", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"a"}`)},
		llm.ToolCall{ID: "call-b", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"cmd":"b"}`)},
	)
	firstStep.StreamDeltas = []llm.AssistantDelta{{Text: "draft", Phase: llm.MessagePhaseCommentary}}
	secondStep := scriptedllm.FinalAnswer("answer")
	secondStep.StreamDeltas = []llm.AssistantDelta{{Text: "answer", Phase: llm.MessagePhaseFinal}}
	secondStep.ExpectedToolResults = []scriptedllm.ExpectedToolResult{
		{CallID: "call-a", Name: string(toolspec.ToolExecCommand)},
		{CallID: "call-b", Name: string(toolspec.ToolExecCommand)},
	}
	fixture := newStreamSubscriptionFixture(
		t,
		scriptedllm.NewClient(scriptedllm.Script{Steps: []scriptedllm.Step{firstStep, secondStep}}),
		newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: handler}),
		[]toolspec.ID{toolspec.ToolExecCommand},
	)
	submitDone := fixture.submitUserMessage("run both")

	for len(fixture.lifecycle.toolStarts) < 2 {
		fixture.next()
	}
	if fixture.lifecycle.toolStarts["call-a"] == nil || fixture.lifecycle.toolStarts["call-b"] == nil {
		t.Fatalf("tool start set = %+v, want call-a and call-b", fixture.lifecycle.toolStarts)
	}
	handler.waitEntered(t, "call-a")
	handler.waitEntered(t, "call-b")

	handler.release("call-b")
	handler.release("call-a")
	for fixture.lifecycle.toolCompletions["call-a"] == nil ||
		fixture.lifecycle.toolCompletions["call-b"] == nil {
		fixture.next()
	}
	for fixture.lifecycle.finalAssistant == nil {
		fixture.next()
	}

	fixture.lifecycle.assertCompleted(t)
	for callID, start := range fixture.lifecycle.toolStarts {
		if start.position <= fixture.lifecycle.abort.position {
			t.Fatalf("%s start position = %d, want after abort position %d", callID, start.position, fixture.lifecycle.abort.position)
		}
	}
	if fixture.lifecycle.toolCompletions["call-a"].position <= fixture.lifecycle.toolStarts["call-a"].position ||
		fixture.lifecycle.toolCompletions["call-b"].position <= fixture.lifecycle.toolCompletions["call-a"].position ||
		fixture.lifecycle.resumedDelta.position <= fixture.lifecycle.toolCompletions["call-b"].position {
		t.Fatalf("tool continuation lifecycle = starts:%+v completions:%+v resumed:%+v", fixture.lifecycle.toolStarts, fixture.lifecycle.toolCompletions, fixture.lifecycle.resumedDelta)
	}
	fixture.awaitSubmission(submitDone)
	fixture.assertSubscriptionOpen()
	_ = fixture.freshCleanHydration()
}

type streamSubscriptionFixture struct {
	t         *testing.T
	registry  *RuntimeRegistry
	engine    *runtime.Engine
	sub       serverapi.TranscriptSubscription
	lifecycle streamLifecycleRecorder
}

func newStreamSubscriptionFixture(t *testing.T, client llm.Client, toolRegistry *tools.Registry, enabledTools []toolspec.ID) *streamSubscriptionFixture {
	t.Helper()
	registry := NewRuntimeRegistry()
	engine := newRegistryRuntime(t, client, toolRegistry, runtime.Config{
		Model:         "gpt-5",
		ThinkingLevel: "medium",
		EnabledTools:  append([]toolspec.ID(nil), enabledTools...),
	}, func(engine *runtime.Engine, evt runtime.Event) {
		registry.PublishAuthorityRuntimeEvent(registryTestResourceRef(engine.SessionID()), evt)
	})
	registerReady(t, registry, engine.SessionID(), engine)
	t.Cleanup(func() { closeRuntime(registry, engine.SessionID(), engine) })
	sub := subscribeTranscriptForTest(t, registry, engine.SessionID())
	t.Cleanup(func() { _ = sub.Close() })
	hydration := nextTranscriptMessage(t, sub)
	if hydration.Sequence != 1 || hydration.Kind() != clientui.TranscriptMessageHydration {
		t.Fatalf("initial message = %+v, want seq=1 hydration", hydration)
	}
	return &streamSubscriptionFixture{
		t:        t,
		registry: registry,
		engine:   engine,
		sub:      sub,
		lifecycle: streamLifecycleRecorder{
			lastSequence:    hydration.Sequence,
			toolStarts:      make(map[string]*eventPosition),
			toolCompletions: make(map[string]*eventPosition),
		},
	}
}

func (f *streamSubscriptionFixture) submitUserMessage(text string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := f.engine.SubmitUserMessage(context.Background(), text)
		done <- err
	}()
	return done
}

func (f *streamSubscriptionFixture) next() {
	f.t.Helper()
	message := nextTranscriptMessage(f.t, f.sub)
	f.lifecycle.record(f.t, message)
}

func (f *streamSubscriptionFixture) awaitSubmission(done <-chan error) {
	f.t.Helper()
	select {
	case err := <-done:
		if err != nil {
			f.t.Fatalf("SubmitUserMessage: %v", err)
		}
	case <-time.After(time.Second):
		f.t.Fatal("timed out waiting for resumed turn completion")
	}
}

func (f *streamSubscriptionFixture) assertSubscriptionOpen() {
	f.t.Helper()
	for {
		message, err := nextTranscriptMessageTimeout(f.sub, 20*time.Millisecond)
		if err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				f.t.Fatalf("original subscription closed after turn: %v", err)
			}
			return
		}
		f.lifecycle.record(f.t, message)
	}
}

func (f *streamSubscriptionFixture) freshCleanHydration() *clientui.TranscriptHydration {
	f.t.Helper()
	fresh := subscribeTranscriptForTest(f.t, f.registry, f.engine.SessionID())
	defer func() { _ = fresh.Close() }()
	message := nextTranscriptMessage(f.t, fresh)
	hydration := message.Payload().(clientui.TranscriptHydration)
	if hydration.ActiveAssistant != nil {
		f.t.Fatalf("fresh hydration active stream = %+v, want none", hydration)
	}
	return &hydration
}

type streamObservation struct {
	position int
	streamID runtimeids.AssistantStreamID
}

type eventPosition struct {
	position int
}

type streamLifecycleRecorder struct {
	lastSequence    uint64
	nextPosition    int
	initialDelta    *streamObservation
	abort           *streamObservation
	resumedDelta    *streamObservation
	finalAssistant  *streamObservation
	promptResolved  *eventPosition
	toolStarts      map[string]*eventPosition
	toolCompletions map[string]*eventPosition
}

func (r *streamLifecycleRecorder) record(t *testing.T, message clientui.TranscriptMessage) {
	t.Helper()
	if message.Sequence <= r.lastSequence {
		t.Fatalf("non-monotonic transcript sequence: previous=%d current=%d message=%+v", r.lastSequence, message.Sequence, message)
	}
	r.lastSequence = message.Sequence
	position := r.nextPosition
	r.nextPosition++
	switch message.Kind() {
	case clientui.TranscriptMessageAssistantDelta:
		payload := message.Payload().(clientui.TranscriptAssistantDelta)
		observation := &streamObservation{position: position, streamID: payload.StreamID}
		switch payload.Phase {
		case transcript.AssistantPhaseCommentary:
			if r.initialDelta == nil {
				r.initialDelta = observation
			}
		case transcript.AssistantPhaseFinal:
			if r.resumedDelta == nil {
				r.resumedDelta = observation
			}
		}
	case clientui.TranscriptMessageAssistantStreamAbort:
		payload := message.Payload().(clientui.TranscriptAssistantStreamAbort)
		if payload.Reason == clientui.AssistantStreamAbortSuperseded {
			r.abort = &streamObservation{position: position, streamID: payload.StreamID}
		}
	case clientui.TranscriptMessagePrompt:
		payload := message.Payload().(clientui.TranscriptPrompt)
		if payload.Status == clientui.TranscriptPromptStatusResolved {
			r.promptResolved = &eventPosition{position: position}
		}
	case clientui.TranscriptMessageToolStart:
		payload := message.Payload().(clientui.TranscriptToolStart)
		r.toolStarts[string(payload.ToolCallID)] = &eventPosition{position: position}
	case clientui.TranscriptMessageCommittedRow:
		payload := message.Payload().(clientui.TranscriptCommittedRow)
		if payload.Tool != nil {
			r.toolCompletions[string(payload.Tool.ToolCallID)] = &eventPosition{position: position}
		}
		if payload.Assistant != nil && payload.Assistant.StreamID != nil {
			r.finalAssistant = &streamObservation{position: position, streamID: *payload.Assistant.StreamID}
		}
	}
}

func (r streamLifecycleRecorder) assertCompleted(t *testing.T) {
	t.Helper()
	if r.initialDelta == nil || r.abort == nil || r.resumedDelta == nil || r.finalAssistant == nil {
		t.Fatalf("incomplete assistant lifecycle: %+v", r)
	}
	if r.initialDelta.streamID.IsZero() ||
		r.resumedDelta.streamID.IsZero() ||
		r.finalAssistant.streamID.IsZero() {
		t.Fatalf("assistant stream identities are not UUID v4 values: initial:%s resumed:%s final:%s", r.initialDelta.streamID, r.resumedDelta.streamID, r.finalAssistant.streamID)
	}
	if r.initialDelta.streamID != r.abort.streamID {
		t.Fatalf("abort stream id = %s, want initial delta stream id %s", r.abort.streamID, r.initialDelta.streamID)
	}
	if r.initialDelta.streamID == r.resumedDelta.streamID || r.resumedDelta.streamID != r.finalAssistant.streamID {
		t.Fatalf("stream identities = initial:%s resumed:%s final:%s", r.initialDelta.streamID, r.resumedDelta.streamID, r.finalAssistant.streamID)
	}
	if r.initialDelta.position >= r.abort.position ||
		r.abort.position >= r.resumedDelta.position ||
		r.resumedDelta.position >= r.finalAssistant.position {
		t.Fatalf("assistant lifecycle order = initial:%d abort:%d resumed:%d final:%d", r.initialDelta.position, r.abort.position, r.resumedDelta.position, r.finalAssistant.position)
	}
}

func hydrationContainsAssistantStream(hydration *clientui.TranscriptHydration, streamID runtimeids.AssistantStreamID) bool {
	for _, row := range hydration.TailSegment.Entries {
		if row.Assistant != nil && row.Assistant.StreamID != nil && *row.Assistant.StreamID == streamID {
			return true
		}
	}
	return false
}

type blockingToolCall struct {
	entered chan struct{}
	release chan struct{}
}

type controlledToolHandler struct {
	fallback *blockingToolCall
	calls    map[string]*blockingToolCall
}

func newBlockingToolHandler() *controlledToolHandler {
	return &controlledToolHandler{fallback: newBlockingToolCall()}
}

func newControlledToolHandler(callIDs ...string) *controlledToolHandler {
	calls := make(map[string]*blockingToolCall, len(callIDs))
	for _, callID := range callIDs {
		calls[callID] = newBlockingToolCall()
	}
	return &controlledToolHandler{calls: calls}
}

func newBlockingToolCall() *blockingToolCall {
	return &blockingToolCall{entered: make(chan struct{}), release: make(chan struct{})}
}

func (h *controlledToolHandler) Call(ctx context.Context, call tools.Call) (tools.Result, error) {
	control := h.calls[call.ID]
	if control == nil {
		control = h.fallback
	}
	if control == nil {
		return tools.Result{}, errors.New("controlled tool call was not configured")
	}
	close(control.entered)
	select {
	case <-ctx.Done():
		return tools.Result{}, ctx.Err()
	case <-control.release:
		return tools.Result{
			CallID: call.ID,
			Name:   call.Name,
			Output: json.RawMessage(`{"output":"done"}`),
		}, nil
	}
}

func (h *controlledToolHandler) waitEntered(t *testing.T, callID string) {
	t.Helper()
	control := h.calls[callID]
	if control == nil {
		t.Fatalf("controlled tool call %q was not configured", callID)
	}
	control.waitEntered(t)
}

func (h *controlledToolHandler) release(callID string) {
	h.calls[callID].releaseCall()
}

func (h *controlledToolHandler) waitFallbackEntered(t *testing.T) {
	t.Helper()
	h.fallback.waitEntered(t)
}

func (h *controlledToolHandler) releaseFallback() {
	h.fallback.releaseCall()
}

func (c *blockingToolCall) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-c.entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tool call to enter")
	}
}

func (c *blockingToolCall) releaseCall() {
	close(c.release)
}

func waitForPendingPrompt(t *testing.T, registry *RuntimeRegistry, sessionID string, toolCallID string) {
	t.Helper()
	var prompts []PendingPromptSnapshot
	if testsetup.Until(time.Now().Add(time.Second), 10*time.Millisecond, func() bool {
		prompts = registry.ListPendingPrompts(sessionID)
		return len(prompts) == 1 && prompts[0].Request.ToolCallID == toolCallID
	}) {
		return
	}
	t.Fatalf("pending prompt %q was not registered: %+v", toolCallID, prompts)
}
