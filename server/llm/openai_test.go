package llm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"

	"core/shared/textutil"
)

type streamingOnlyTransport struct{}

func (streamingOnlyTransport) Generate(context.Context, OpenAIRequest) (OpenAIResponse, error) {
	return OpenAIResponse{}, nil
}

func (streamingOnlyTransport) Compact(context.Context, OpenAICompactionRequest) (OpenAICompactionResponse, error) {
	return OpenAICompactionResponse{}, nil
}

func (streamingOnlyTransport) GenerateStream(_ context.Context, _ OpenAIRequest, onDelta func(text string)) (OpenAIResponse, error) {
	if onDelta != nil {
		onDelta("Hel")
		onDelta("lo")
	}
	return OpenAIResponse{AssistantText: textutil.Value("Hello"), ProviderPhase: AbsentProviderPhase()}, nil
}

type capturingInputTokenTransport struct {
	streamingOnlyTransport
	request OpenAIRequest
}

type capturingCompactionTransport struct {
	streamingOnlyTransport
	request OpenAICompactionRequest
}

func (t *capturingCompactionTransport) Compact(_ context.Context, request OpenAICompactionRequest) (OpenAICompactionResponse, error) {
	t.request = request
	return OpenAICompactionResponse{}, nil
}

func (t *capturingInputTokenTransport) CountRequestInputTokens(_ context.Context, request OpenAIRequest) (int, error) {
	t.request = request
	return 123, nil
}

func TestOpenAIClientCountRequestInputTokensPreservesGenerationToolControls(t *testing.T) {
	transport := &capturingInputTokenTransport{}
	client := NewOpenAIClient(transport)
	dispatch, err := NewCodexDispatchContext(CodexDispatchFacts{
		SessionID: "different-session",
		RunID:     "run-1",
	})
	if err != nil {
		t.Fatalf("dispatch context: %v", err)
	}
	request := Request{
		Model:                 "gpt-5",
		ToolChoiceMode:        ToolChoiceModeRequired,
		EnableNativeWebSearch: true,
		SessionID:             textutil.Value("session-1"),
		CodexDispatch:         dispatch,
		Tools:                 []Tool{{Name: "shell", Schema: mustTestFunctionSchema(t, struct{}{})}},
	}
	count, err := client.CountRequestInputTokens(context.Background(), request)
	if err != nil {
		t.Fatalf("CountRequestInputTokens: %v", err)
	}
	if count != 123 {
		t.Fatalf("count = %d, want 123", count)
	}
	if transport.request.ToolChoiceMode != ToolChoiceModeRequired || !transport.request.EnableNativeWebSearch {
		t.Fatalf("captured tool controls = mode:%q web_search:%t", transport.request.ToolChoiceMode, transport.request.EnableNativeWebSearch)
	}
	if len(transport.request.Tools) != 1 || transport.request.Tools[0].Name != "shell" {
		t.Fatalf("captured tools = %+v", transport.request.Tools)
	}
	if transport.request.SessionID != nil || transport.request.CodexDispatch != nil {
		t.Fatalf("token-count support request carries dispatch identity: %+v", transport.request)
	}
}

func TestRequestAsOpenAIClonesPreparedSchemaCarriers(t *testing.T) {
	request := Request{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeAutomatic,
		Tools: []Tool{{
			Name:   "shell",
			Schema: mustTestFunctionSchema(t, struct{}{}),
		}},
		StructuredOutput: &StructuredOutput{
			Name:   "reviewer_suggestions",
			Schema: mustTestStructuredSchema(t, testReviewerStructuredOutput{}),
		},
	}
	projected := RequestAsOpenAI(request)
	request.Tools[0].Name = "mutated"
	request.StructuredOutput.Name = "mutated"
	if len(projected.Tools) != 1 ||
		projected.Tools[0].Name != "shell" ||
		!projected.Tools[0].Schema.Prepared() {
		t.Fatalf("projected tools changed with source mutation: %+v", projected.Tools)
	}
	if projected.StructuredOutput == nil || projected.StructuredOutput.Name != "reviewer_suggestions" {
		t.Fatalf("projected structured output changed with source mutation: %+v", projected.StructuredOutput)
	}
	if !projected.StructuredOutput.Schema.Prepared() {
		t.Fatal("projected structured output lost its prepared schema")
	}
}

func TestCodexDispatchContextProjectsApprovedTurnEnvelope(t *testing.T) {
	context, err := NewCodexDispatchContext(CodexDispatchFacts{
		SessionID:            "session-1",
		RunID:                "run-1",
		CompactionGeneration: 3,
		RequestKind:          CodexRequestKindTurn.Optional(),
	})
	if err != nil {
		t.Fatalf("NewCodexDispatchContext: %v", err)
	}

	metadata, err := context.TurnMetadataJSON()
	if err != nil {
		t.Fatalf("TurnMetadataJSON: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(metadata), &envelope); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	want := map[string]any{
		"session_id":   "session-1",
		"thread_id":    "session-1",
		"turn_id":      "run-1",
		"window_id":    "session-1:3",
		"request_kind": "turn",
	}
	if len(envelope) != len(want) {
		t.Fatalf("metadata = %#v, want %#v", envelope, want)
	}
	for key, wantValue := range want {
		if got := envelope[key]; got != wantValue {
			t.Fatalf("metadata[%q] = %#v, want %#v", key, got, wantValue)
		}
	}
}

func TestCodexDispatchContextOmitsAbsentRequestKind(t *testing.T) {
	context, err := NewCodexDispatchContext(CodexDispatchFacts{
		SessionID:            "session-1",
		RunID:                "run-1",
		CompactionGeneration: 0,
	})
	if err != nil {
		t.Fatalf("NewCodexDispatchContext: %v", err)
	}
	metadata, err := context.TurnMetadataJSON()
	if err != nil {
		t.Fatalf("TurnMetadataJSON: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(metadata), &envelope); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if _, exists := envelope["request_kind"]; exists {
		t.Fatalf("metadata unexpectedly contains request_kind: %#v", envelope)
	}
}

func TestCodexDispatchContextRejectsInvalidFacts(t *testing.T) {
	tests := []struct {
		name  string
		facts CodexDispatchFacts
	}{
		{name: "blank session", facts: CodexDispatchFacts{RunID: "run-1"}},
		{name: "blank run", facts: CodexDispatchFacts{SessionID: "session-1"}},
		{name: "negative generation", facts: CodexDispatchFacts{SessionID: "session-1", RunID: "run-1", CompactionGeneration: -1}},
		{name: "unknown kind", facts: CodexDispatchFacts{SessionID: "session-1", RunID: "run-1", RequestKind: CodexRequestKind("review").Optional()}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewCodexDispatchContext(test.facts); err == nil {
				t.Fatal("expected invalid facts to fail")
			}
		})
	}
}
func TestCodexDispatchSessionConsistencyAtProviderNeutralBoundaries(t *testing.T) {
	dispatch, err := NewCodexDispatchContext(CodexDispatchFacts{SessionID: "session-1", RunID: "run-1"})
	if err != nil {
		t.Fatalf("NewCodexDispatchContext: %v", err)
	}
	request := Request{
		Model: "gpt-5", ToolChoiceMode: ToolChoiceModeAutomatic,
		SessionID: textutil.Value("session-2"), CodexDispatch: dispatch,
	}
	if err := request.Validate(); err == nil {
		t.Fatal("generation request/session mismatch unexpectedly validated")
	}
	client := NewOpenAIClient(streamingOnlyTransport{})
	if _, err := client.Compact(context.Background(), CompactionRequest{
		Model: "gpt-5", SessionID: textutil.Value("session-2"), CodexDispatch: dispatch,
	}); err == nil {
		t.Fatal("compaction request/session mismatch unexpectedly projected")
	}
}
func TestOpenAIClientCompactProjectsEffectiveFastMode(t *testing.T) {
	transport := &capturingCompactionTransport{}
	if _, err := NewOpenAIClient(transport).Compact(context.Background(), CompactionRequest{
		Model: "gpt-5.6-sol", SessionID: textutil.Value("session-1"), FastMode: true,
	}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !transport.request.FastMode {
		t.Fatal("provider compaction request lost effective Fast Mode")
	}
}

func TestCodexDispatchDoesNotAffectRequestJSONOrPreciseTokenFingerprint(t *testing.T) {
	base := Request{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeAutomatic,
		SessionID:      textutil.Value("session-1"),
	}
	context, err := NewCodexDispatchContext(CodexDispatchFacts{
		SessionID:            *base.SessionID,
		RunID:                "run-1",
		CompactionGeneration: 2,
		RequestKind:          CodexRequestKindTurn.Optional(),
	})
	if err != nil {
		t.Fatalf("NewCodexDispatchContext: %v", err)
	}
	withContext := base
	withContext.CodexDispatch = context

	baseJSON, baseFingerprint := requestJSONAndFingerprint(t, base)
	contextJSON, contextFingerprint := requestJSONAndFingerprint(t, withContext)
	if string(contextJSON) != string(baseJSON) || contextFingerprint != baseFingerprint {
		t.Fatalf("Codex context changed request JSON/fingerprint:\nbase=%s\ncontext=%s", baseJSON, contextJSON)
	}

	context.observeTurnStateCandidate("opaque-state", codexTurnStateSourceHTTPHeader)
	stateJSON, stateFingerprint := requestJSONAndFingerprint(t, withContext)
	if string(stateJSON) != string(baseJSON) || stateFingerprint != baseFingerprint {
		t.Fatalf("retry-local state changed request JSON/fingerprint:\nbase=%s\nstate=%s", baseJSON, stateJSON)
	}
}

func requestJSONAndFingerprint(t *testing.T, request Request) ([]byte, [sha256.Size]byte) {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return payload, sha256.Sum256(payload)
}

func TestOpenAIClientGenerateStreamDoesNotReplayFinalTextAsDelta(t *testing.T) {
	client := NewOpenAIClient(streamingOnlyTransport{})
	req := Request{Model: "gpt-5", ToolChoiceMode: ToolChoiceModeAutomatic}

	var deltas []string
	resp, err := client.GenerateStream(context.Background(), req, func(text string) {
		deltas = append(deltas, text)
	})
	if err != nil {
		t.Fatalf("generate stream failed: %v", err)
	}
	if messageContent(resp.Assistant) != "Hello" {
		t.Fatalf("expected final assistant content, got %q", messageContent(resp.Assistant))
	}
	if len(deltas) != 2 || deltas[0] != "Hel" || deltas[1] != "lo" {
		t.Fatalf("expected only incremental stream deltas, got %+v", deltas)
	}
}

func TestOpenAIClientGenerateStreamPreservesFinalTextThatExtendsStreamWithWhitespace(t *testing.T) {
	transport := trailingWhitespaceStreamingTransport{}
	client := NewOpenAIClient(transport)

	var deltas []string
	resp, err := client.GenerateStream(
		context.Background(),
		Request{Model: "gpt-5", ToolChoiceMode: ToolChoiceModeAutomatic},
		func(text string) {
			deltas = append(deltas, text)
		},
	)
	if err != nil {
		t.Fatalf("generate stream: %v", err)
	}
	if len(deltas) != 1 || deltas[0] != "done\n\n" {
		t.Fatalf("stream deltas = %#v", deltas)
	}
	if resp.Assistant.Content == nil || *resp.Assistant.Content != "done\n\n" {
		t.Fatalf("final assistant content = %#v, want exact streamed text", resp.Assistant.Content)
	}
}

type trailingWhitespaceStreamingTransport struct {
	streamingOnlyTransport
}

func (trailingWhitespaceStreamingTransport) GenerateStream(
	_ context.Context,
	_ OpenAIRequest,
	onDelta func(text string),
) (OpenAIResponse, error) {
	if onDelta != nil {
		onDelta("done\n\n")
	}
	return OpenAIResponse{
		AssistantText: textutil.Value("done\n\n"),
		ProviderPhase: AbsentProviderPhase(),
	}, nil
}

func TestOpenAIClientLegacyStreamTransportEmitsUnknownDeltaPhase(t *testing.T) {
	client := NewOpenAIClient(streamingOnlyTransport{})
	req := Request{Model: "gpt-5", ToolChoiceMode: ToolChoiceModeAutomatic}

	var deltas []AssistantDelta
	_, err := client.GenerateStreamWithEvents(context.Background(), req, StreamCallbacks{
		OnAssistantDelta: func(delta AssistantDelta) {
			deltas = append(deltas, delta)
		},
	})
	if err != nil {
		t.Fatalf("generate stream failed: %v", err)
	}
	if len(deltas) != 2 {
		t.Fatalf("expected two deltas, got %+v", deltas)
	}
	for _, delta := range deltas {
		if delta.Phase != "" {
			t.Fatalf("expected unknown phase for legacy text-only stream delta, got %+v", deltas)
		}
	}
}
