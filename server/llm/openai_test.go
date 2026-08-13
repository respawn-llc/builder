package llm

import (
	"context"
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
