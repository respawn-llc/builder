package app

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	modelstub "core/internal/testharness/pty/blackbox"
)

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
