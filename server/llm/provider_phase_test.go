package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"core/shared/llmerrors"
)

func TestProviderPhaseConstruction(t *testing.T) {
	var unavailable *ProviderPhase
	if unavailable.IsAbsent() {
		t.Fatal("unavailable provider phase fact must differ from structural phase absence")
	}
	if unavailable.Value() != nil {
		t.Fatal("unavailable provider phase fact must not expose a value")
	}

	absent := AbsentProviderPhase()
	if !absent.IsAbsent() {
		t.Fatal("absent provider phase must report absence")
	}
	if absent.Value() != nil {
		t.Fatal("absent provider phase must not expose a value")
	}
	if absent.Is(MessagePhaseCommentary) || absent.Is(MessagePhaseFinal) {
		t.Fatal("absent provider phase must not match a present phase")
	}

	tests := []struct {
		name  string
		phase *ProviderPhase
		want  MessagePhase
	}{
		{name: "commentary", phase: CommentaryProviderPhase(), want: MessagePhaseCommentary},
		{name: "final", phase: FinalProviderPhase(), want: MessagePhaseFinal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.phase.IsAbsent() {
				t.Fatal("present provider phase must not report absence")
			}
			got := tt.phase.Value()
			if got == nil || *got != tt.want {
				t.Fatalf("provider phase value = %#v; want %q", got, tt.want)
			}
			if !tt.phase.Is(tt.want) {
				t.Fatalf("provider phase must match %q", tt.want)
			}
		})
	}
}

func TestOpenAIClientProjectsProviderPhaseFromOneAuthoritativeFact(t *testing.T) {
	client := NewOpenAIClient(providerPhaseProjectionTransport{
		response: OpenAIResponse{
			AssistantText: "done",
			ProviderPhase: FinalProviderPhase(),
		},
	})

	resp, err := client.Generate(context.Background(), Request{Model: "gpt-5", ToolChoiceMode: ToolChoiceModeAutomatic})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !resp.ProviderPhase.Is(MessagePhaseFinal) {
		t.Fatalf("provider phase = %#v, want final", resp.ProviderPhase)
	}
	if resp.Assistant.Phase != MessagePhaseFinal {
		t.Fatalf("legacy assistant phase = %q, want %q", resp.Assistant.Phase, MessagePhaseFinal)
	}
}

func TestGenerateDecodesAbsentProviderPhaseStructurally(t *testing.T) {
	tests := []struct {
		name       string
		phaseField string
	}{
		{name: "omitted"},
		{name: "null", phaseField: `,"phase":null`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewOpenAIClient(newProviderPhaseResponseTransport(t, tt.phaseField))
			resp, err := client.Generate(context.Background(), Request{Model: "compatible-model", ToolChoiceMode: ToolChoiceModeAutomatic})
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if !resp.ProviderPhase.IsAbsent() {
				t.Fatalf("provider phase = %#v, want absent", resp.ProviderPhase)
			}
			if resp.Assistant.Phase != "" {
				t.Fatalf("legacy assistant phase = %q, want unchanged absent projection", resp.Assistant.Phase)
			}
		})
	}
}

func TestGenerateRejectsInvalidProviderPhaseContracts(t *testing.T) {
	tests := []struct {
		name       string
		phaseField string
	}{
		{name: "empty string", phaseField: `,"phase":""`},
		{name: "unknown string", phaseField: `,"phase":"analysis"`},
		{name: "number", phaseField: `,"phase":1`},
		{name: "boolean", phaseField: `,"phase":true`},
		{name: "object", phaseField: `,"phase":{"kind":"final_answer"}`},
		{name: "array", phaseField: `,"phase":["final_answer"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewOpenAIClient(newProviderPhaseResponseTransport(t, tt.phaseField))
			_, err := client.Generate(context.Background(), Request{Model: "compatible-model", ToolChoiceMode: ToolChoiceModeAutomatic})
			if err == nil {
				t.Fatal("expected provider contract error")
			}
			var providerErr *llmerrors.ProviderAPIError
			if !errors.As(err, &providerErr) {
				t.Fatalf("error = %T %v, want provider API error", err, err)
			}
			if providerErr.Code != llmerrors.UnifiedErrorCodeProviderContract {
				t.Fatalf("error code = %q, want %q", providerErr.Code, llmerrors.UnifiedErrorCodeProviderContract)
			}
		})
	}
}

func TestGenerateStreamAccumulatesOutputItemProviderPhase(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Working"}]}}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":2,"total_tokens":4},"output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Working"}]}]}}`,
		`[DONE]`,
	)

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{Model: "gpt-5", ToolChoiceMode: ToolChoiceModeAutomatic}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("generate stream: %v", err)
	}
	if !resp.ProviderPhase.Is(MessagePhaseCommentary) {
		t.Fatalf("provider phase = %#v, want commentary", resp.ProviderPhase)
	}
}

func TestGenerateStreamAggregatesCompletedResponseProviderPhase(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":2,"total_tokens":4},"output":[{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done"}]}]}}`,
		`[DONE]`,
	)

	resp, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{Model: "gpt-5", ToolChoiceMode: ToolChoiceModeAutomatic}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("generate stream: %v", err)
	}
	if !resp.ProviderPhase.Is(MessagePhaseFinal) {
		t.Fatalf("provider phase = %#v, want final", resp.ProviderPhase)
	}
}

type providerPhaseProjectionTransport struct {
	response OpenAIResponse
}

func (t providerPhaseProjectionTransport) Generate(context.Context, OpenAIRequest) (OpenAIResponse, error) {
	return t.response, nil
}

func (providerPhaseProjectionTransport) Compact(context.Context, OpenAICompactionRequest) (OpenAICompactionResponse, error) {
	return OpenAICompactionResponse{}, nil
}

func newProviderPhaseResponseTransport(t *testing.T, phaseField string) *HTTPTransport {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"id":"resp_phase",
			"object":"response",
			"output":[{
				"type":"message",
				"id":"msg_phase",
				"role":"assistant"%s,
				"status":"completed",
				"content":[{"type":"output_text","text":"done"}]
			}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`, phaseField)
	}))
	t.Cleanup(server.Close)

	transport := NewHTTPTransport(staticAuthHeader{})
	transport.BaseURL = server.URL
	transport.Client = server.Client()
	transport.ProviderCapabilitiesOverride = &ProviderCapabilities{
		ProviderID:           "openai-compatible",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   false,
	}
	return transport
}
