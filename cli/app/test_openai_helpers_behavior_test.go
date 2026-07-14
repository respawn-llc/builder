package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelstub "core/internal/testharness/pty/blackbox"
)

func TestHandleInputTokenCountHandlesResponsesInputTokensPath(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/responses/input_tokens", nil)

	handled := modelstub.HandleInputTokenCount(recorder, req, 123)
	if !handled {
		t.Fatal("expected request to be handled")
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}

	var payload struct {
		Object      string `json:"object"`
		InputTokens int    `json:"input_tokens"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	if payload.Object != "response.input_tokens" {
		t.Fatalf("object = %q, want response.input_tokens", payload.Object)
	}
	if payload.InputTokens != 123 {
		t.Fatalf("input_tokens = %d, want 123", payload.InputTokens)
	}
}

func TestHandleInputTokenCountIgnoresOtherPaths(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/responses", nil)

	handled := modelstub.HandleInputTokenCount(recorder, req, 123)
	if handled {
		t.Fatal("expected non-input-token path to be ignored")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want default 200", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %q", recorder.Body.String())
	}
}

func TestHandleInputTokenCountSetsAllowHeaderForWrongMethod(t *testing.T) {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/responses/input_tokens", nil)

	handled := modelstub.HandleInputTokenCount(recorder, req, 123)
	if !handled {
		t.Fatal("expected wrong-method request to be handled")
	}
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
	if got := recorder.Header().Get("Allow"); got != http.MethodPost {
		t.Fatalf("Allow header = %q, want %q", got, http.MethodPost)
	}
}

func TestWriteCompletedResponseStreamWritesExpectedEvent(t *testing.T) {
	recorder := httptest.NewRecorder()

	modelstub.WriteCompletedResponseStream(recorder, "hello", 11, 7)

	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}

	lines := strings.Split(recorder.Body.String(), "\n")
	if len(lines) < 3 {
		t.Fatalf("unexpected SSE payload: %q", recorder.Body.String())
	}
	first := strings.TrimPrefix(lines[0], "data: ")
	var payload struct {
		Type     string `json:"type"`
		Response struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
				TotalTokens  int `json:"total_tokens"`
			} `json:"usage"`
			Output []struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Phase   string `json:"phase"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(first), &payload); err != nil {
		t.Fatalf("unmarshal response event: %v", err)
	}
	if payload.Type != "response.completed" {
		t.Fatalf("event type = %q, want response.completed", payload.Type)
	}
	if payload.Response.Usage.InputTokens != 11 || payload.Response.Usage.OutputTokens != 7 || payload.Response.Usage.TotalTokens != 18 {
		t.Fatalf("unexpected usage payload: %+v", payload.Response.Usage)
	}
	if len(payload.Response.Output) != 1 || len(payload.Response.Output[0].Content) != 1 {
		t.Fatalf("unexpected output payload: %+v", payload.Response.Output)
	}
	if payload.Response.Output[0].Content[0].Text != "hello" {
		t.Fatalf("assistant text = %q, want hello", payload.Response.Output[0].Content[0].Text)
	}
	if lines[2] != "data: [DONE]" {
		t.Fatalf("done marker = %q, want data: [DONE]", lines[2])
	}
}
