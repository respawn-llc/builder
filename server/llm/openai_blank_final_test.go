package llm

import (
	"context"
	"encoding/json"
	"testing"

	"core/shared/textutil"

	"github.com/openai/openai-go/v3/responses"
)

func TestOpenAIBlankFinalResponsePresence(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		contentField string
		wantContent  *string
		wantPhase    MessagePhase
	}{
		{name: "empty", content: `{"type":"output_text","text":""}`, wantContent: textutil.Value(""), wantPhase: MessagePhaseFinal},
		{name: "whitespace", content: `{"type":"output_text","text":" \n\t"}`, wantContent: textutil.Value(" \n\t"), wantPhase: MessagePhaseFinal},
		{name: "empty array", contentField: `"content":[],`, wantContent: textutil.Value(""), wantPhase: MessagePhaseFinal},
		{name: "null", contentField: `"content":null,`, wantContent: nil, wantPhase: MessagePhaseFinal},
		{name: "omitted", wantContent: nil, wantPhase: MessagePhaseFinal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := test.contentField
			if content == "" && test.content != "" {
				content = `"content":[` + test.content + `],`
			}
			raw := `[{` + `"type":"message","role":"assistant",` + content + `"phase":"final_answer"` + `}]`
			var output []responses.ResponseOutputItemUnion
			if err := json.Unmarshal([]byte(raw), &output); err != nil {
				t.Fatalf("unmarshal output: %v", err)
			}

			items, assistantText, phase, _, _, _, _, err := parseOutputItems(output)
			if err != nil {
				t.Fatalf("parse output: %v", err)
			}
			if !equalOptionalString(assistantText, test.wantContent) {
				t.Fatalf("assistant text = %#v, want %#v", assistantText, test.wantContent)
			}
			if phase != test.wantPhase {
				t.Fatalf("assistant phase = %q, want %q", phase, test.wantPhase)
			}
			if len(items) != 1 {
				t.Fatalf("canonical items = %d, want 1", len(items))
			}
			if !equalOptionalString(items[0].Content, test.wantContent) {
				t.Fatalf("canonical content = %#v, want %#v", items[0].Content, test.wantContent)
			}
		})
	}
}

func TestOpenAIBlankFinalClientPresence(t *testing.T) {
	for _, test := range []struct {
		name          string
		content       *string
		providerPhase *ProviderPhase
		wantContent   *string
	}{
		{name: "present empty final", content: textutil.Value(""), providerPhase: FinalProviderPhase(), wantContent: textutil.Value("")},
		{name: "present whitespace final", content: textutil.Value(" \n\t"), providerPhase: FinalProviderPhase(), wantContent: textutil.Value(" \n\t")},
		{name: "empty commentary", content: textutil.Value(""), providerPhase: CommentaryProviderPhase(), wantContent: nil},
		{name: "whitespace commentary", content: textutil.Value(" \n\t"), providerPhase: CommentaryProviderPhase(), wantContent: nil},
		{name: "phase omitted", content: textutil.Value(""), providerPhase: AbsentProviderPhase(), wantContent: nil},
		{name: "omitted final", content: nil, providerPhase: FinalProviderPhase()},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := NewOpenAIClient(providerPhaseProjectionTransport{
				response: OpenAIResponse{
					AssistantText: test.content,
					ProviderPhase: test.providerPhase,
				},
			})
			response, err := client.Generate(context.Background(), Request{
				Model:          "gpt-5",
				ToolChoiceMode: ToolChoiceModeAutomatic,
			})
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if !equalOptionalString(response.Assistant.Content, test.wantContent) {
				t.Fatalf("assistant content = %#v, want %#v", response.Assistant.Content, test.wantContent)
			}
			if expectedPhase := test.providerPhase.Value(); expectedPhase == nil {
				if response.Assistant.Phase != nil {
					t.Fatalf("assistant phase = %#v, want absent", response.Assistant.Phase)
				}
			} else if response.Assistant.Phase == nil || *response.Assistant.Phase != *expectedPhase {
				t.Fatalf("assistant phase = %#v, want %q", response.Assistant.Phase, *expectedPhase)
			}
		})
	}
}

func TestOpenAIBlankFinalStreamingAfterCommentary(t *testing.T) {
	tests := []struct {
		name          string
		finalItem     string
		finalResponse string
		wantContent   *string
	}{
		{
			name:          "empty",
			finalItem:     `"content":[{"type":"output_text","text":""}],`,
			finalResponse: `"content":[{"type":"output_text","text":""}],`,
			wantContent:   textutil.Value(""),
		},
		{
			name:          "whitespace",
			finalItem:     `"content":[{"type":"output_text","text":" \n\t"}],`,
			finalResponse: `"content":[{"type":"output_text","text":" \n\t"}],`,
			wantContent:   textutil.Value(" \n\t"),
		},
		{
			name:        "omitted",
			wantContent: nil,
		},
		{
			name:          "null",
			finalItem:     `"content":null,`,
			finalResponse: `"content":null,`,
			wantContent:   nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := newOpenAIStreamTestTransport(t,
				`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_commentary","type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"working"}]}}`,
				`{"type":"response.output_text.delta","output_index":0,"delta":"working"}`,
				`{"type":"response.output_item.done","output_index":1,"item":{"id":"msg_final","type":"message","role":"assistant","phase":"final_answer",`+test.finalItem+`"status":"completed"}}`,
				`{"type":"response.completed","response":{"output":[{"id":"msg_commentary","type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"working"}]},{"id":"msg_final","type":"message","role":"assistant","phase":"final_answer",`+test.finalResponse+`"status":"completed"}]}}`,
				`[DONE]`,
			)
			response, err := NewOpenAIClient(transport).GenerateStreamWithEvents(context.Background(), Request{
				Model:          "gpt-5",
				ToolChoiceMode: ToolChoiceModeAutomatic,
			}, StreamCallbacks{})
			if err != nil {
				t.Fatalf("generate stream: %v", err)
			}
			if !equalOptionalString(response.Assistant.Content, test.wantContent) {
				t.Fatalf("assistant content = %#v, want %#v", response.Assistant.Content, test.wantContent)
			}
			if response.Assistant.Phase == nil || *response.Assistant.Phase != MessagePhaseFinal {
				t.Fatalf("assistant phase = %#v, want final", response.Assistant.Phase)
			}
		})
	}
}

func TestOpenAIBlankFinalStreamingWithPendingUnmaterializedOutput(t *testing.T) {
	tests := []struct {
		name          string
		finalItem     string
		finalResponse string
		wantContent   *string
	}{
		{
			name:          "empty",
			finalItem:     `"content":[],`,
			finalResponse: `"content":[],`,
			wantContent:   textutil.Value(""),
		},
		{
			name: "omitted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []string{
				`{"type":"response.output_text.delta","output_index":0,"delta":"working"}`,
				`{"type":"response.output_item.done","output_index":1,"item":{"id":"msg_final","type":"message","role":"assistant","phase":"final_answer",` + test.finalItem + `"status":"completed"}}`,
				`{"type":"response.completed","response":{"output":[{"id":"msg_final","type":"message","role":"assistant","phase":"final_answer",` + test.finalResponse + `"status":"completed"}]}}`,
				`[DONE]`,
			}
			transport := newOpenAIStreamTestTransport(t, events...)
			rawResponse, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{
				Model:          "gpt-5",
				ToolChoiceMode: ToolChoiceModeAutomatic,
			}, StreamCallbacks{})
			if err != nil {
				t.Fatalf("generate raw stream: %v", err)
			}
			if !equalOptionalString(rawResponse.AssistantText, test.wantContent) {
				t.Fatalf("raw assistant text = %#v, want %#v", rawResponse.AssistantText, test.wantContent)
			}
			if !rawResponse.ProviderPhase.Is(MessagePhaseFinal) {
				t.Fatalf("raw provider phase = %v, want final", rawResponse.ProviderPhase.Value())
			}
			response, err := NewOpenAIClient(transport).GenerateStreamWithEvents(context.Background(), Request{
				Model:          "gpt-5",
				ToolChoiceMode: ToolChoiceModeAutomatic,
			}, StreamCallbacks{})
			if err != nil {
				t.Fatalf("generate stream: %v", err)
			}
			if !equalOptionalString(response.Assistant.Content, test.wantContent) {
				t.Fatalf("assistant content = %#v, want %#v", response.Assistant.Content, test.wantContent)
			}
			if response.Assistant.Phase == nil || *response.Assistant.Phase != MessagePhaseFinal {
				t.Fatalf("assistant phase = %#v, want final", response.Assistant.Phase)
			}
		})
	}
}

func TestOpenAIBlankFinalRejectsMalformedContentShape(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_invalid","type":"message","role":"assistant","phase":"final_answer","content":{}}}`,
		`[DONE]`,
	)
	if _, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeAutomatic,
	}, StreamCallbacks{}); err == nil {
		t.Fatal("expected malformed content shape to fail")
	}
}

func TestOpenAIBlankFinalStreamingPresence(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[]}}`,
		`{"type":"response.output_text.done","item_id":"msg_1","output_index":0,"content_index":0,"text":" \n\t"}`,
		`{"type":"response.completed","response":{"output":[{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":" \n\t"}]}]}}`,
		`[DONE]`,
	)

	response, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{
		ToolChoiceMode: ToolChoiceModeAutomatic,
		Model:          "gpt-5",
	}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("generate stream: %v", err)
	}
	if optionalStringValue(response.AssistantText) != " \n\t" || response.AssistantText == nil {
		t.Fatalf("assistant text = %q, want exact whitespace content", optionalStringValue(response.AssistantText))
	}
	if len(response.OutputItems) != 1 || response.OutputItems[0].Content == nil || *response.OutputItems[0].Content != " \n\t" {
		t.Fatalf("output items = %+v, want present whitespace content", response.OutputItems)
	}

	client := NewOpenAIClient(transport)
	clientResponse, err := client.GenerateStreamWithEvents(context.Background(), Request{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeAutomatic,
	}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("client generate stream: %v", err)
	}
	if clientResponse.Assistant.Content == nil || *clientResponse.Assistant.Content != " \n\t" {
		t.Fatalf("client assistant content = %#v, want exact whitespace content", clientResponse.Assistant.Content)
	}
	if clientResponse.Assistant.Phase == nil || *clientResponse.Assistant.Phase != MessagePhaseFinal {
		t.Fatalf("client assistant phase = %#v, want final", clientResponse.Assistant.Phase)
	}

	omittedTransport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_omitted","type":"message","role":"assistant","phase":"final_answer"}}`,
		`{"type":"response.completed","response":{"output":[{"id":"msg_omitted","type":"message","role":"assistant","phase":"final_answer"}]}}`,
		`[DONE]`,
	)
	omittedResponse, err := NewOpenAIClient(omittedTransport).GenerateStreamWithEvents(context.Background(), Request{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeAutomatic,
	}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("omitted client generate stream: %v", err)
	}
	if omittedResponse.Assistant.Content != nil {
		t.Fatalf("omitted client assistant content = %#v, want absent", omittedResponse.Assistant.Content)
	}
	if omittedResponse.Assistant.Phase == nil || *omittedResponse.Assistant.Phase != MessagePhaseFinal {
		t.Fatalf("omitted client assistant phase = %#v, want final", omittedResponse.Assistant.Phase)
	}
}

func TestOpenAIBlankFinalInputPreparation(t *testing.T) {
	final := textutil.Value(MessagePhaseFinal)
	tests := []struct {
		name string
		item ResponseItem
		want bool
	}{
		{
			name: "assistant final empty",
			item: ResponseItem{Type: ResponseItemTypeMessage, Role: textutil.Value(RoleAssistant), Phase: final, Content: textutil.Value("")},
			want: true,
		},
		{
			name: "assistant final whitespace",
			item: ResponseItem{Type: ResponseItemTypeMessage, Role: textutil.Value(RoleAssistant), Phase: final, Content: textutil.Value(" \n\t")},
			want: true,
		},
		{
			name: "assistant commentary empty",
			item: ResponseItem{Type: ResponseItemTypeMessage, Role: textutil.Value(RoleAssistant), Phase: textutil.Value(MessagePhaseCommentary), Content: textutil.Value("")},
		},
		{
			name: "user empty",
			item: ResponseItem{Type: ResponseItemTypeMessage, Role: textutil.Value(RoleUser), Content: textutil.Value("")},
		},
		{
			name: "developer empty",
			item: ResponseItem{Type: ResponseItemTypeMessage, Role: textutil.Value(RoleDeveloper), Content: textutil.Value("")},
		},
		{
			name: "structured reviewer empty",
			item: ResponseItem{
				Type:        ResponseItemTypeMessage,
				Role:        textutil.Value(RoleAssistant),
				Phase:       final,
				MessageType: textutil.Value(MessageTypeReviewerFeedback),
				Content:     textutil.Value(""),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared := PrepareOpenAIInputItems([]ResponseItem{test.item})
			if len(prepared) != 1 {
				t.Fatalf("prepared items = %d, want 1", len(prepared))
			}
			_, err := buildResponsesInput(prepared)
			if test.want {
				if err != nil {
					t.Fatalf("build input: %v", err)
				}
				jsonItems := mustMarshalItems(t, mustBuildResponsesInput(t, prepared))
				content := jsonItems[0]["content"].([]any)
				text := content[0].(map[string]any)["text"]
				if text != valueOrEmpty(test.item.Content) {
					t.Fatalf("prepared text = %#v, want %#v", text, valueOrEmpty(test.item.Content))
				}
				return
			}
			if err == nil {
				t.Fatal("expected blank message to remain unprepared")
			}
		})
	}
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
