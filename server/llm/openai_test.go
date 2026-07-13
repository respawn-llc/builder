package llm

import (
	"context"
	"testing"
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
	return OpenAIResponse{AssistantText: "Hello"}, nil
}

type capturingInputTokenTransport struct {
	streamingOnlyTransport
	request OpenAIRequest
}

func (t *capturingInputTokenTransport) CountRequestInputTokens(_ context.Context, request OpenAIRequest) (int, error) {
	t.request = request
	return 123, nil
}

func TestOpenAIClientCountRequestInputTokensPreservesGenerationToolControls(t *testing.T) {
	transport := &capturingInputTokenTransport{}
	client := NewOpenAIClient(transport)
	request := Request{
		Model:                 "gpt-5",
		ToolChoiceMode:        ToolChoiceModeRequired,
		EnableNativeWebSearch: true,
		Tools:                 []Tool{{Name: "shell"}},
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
	if resp.Assistant.Content != "Hello" {
		t.Fatalf("expected final assistant content, got %q", resp.Assistant.Content)
	}
	if len(deltas) != 2 || deltas[0] != "Hel" || deltas[1] != "lo" {
		t.Fatalf("expected only incremental stream deltas, got %+v", deltas)
	}
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
