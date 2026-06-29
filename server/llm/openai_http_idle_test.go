package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type pacedStreamEvent struct {
	delay time.Duration
	data  string
}

func newPacedStreamTransport(t *testing.T, events ...pacedStreamEvent) *HTTPTransport {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("response writer is not a flusher")
			return
		}
		flusher.Flush()
		for _, event := range events {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(event.delay):
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", event.data); err != nil {
				return
			}
			flusher.Flush()
		}
	}))
	t.Cleanup(server.Close)

	transport := NewHTTPTransport(staticAuthHeader{})
	transport.BaseURL = server.URL
	transport.Client = server.Client()
	return transport
}

func newPacedWatchdogClient(t *testing.T, idle time.Duration, events ...pacedStreamEvent) *idleWatchdogClient {
	t.Helper()
	return newIdleWatchdogClient(NewOpenAIClient(newPacedStreamTransport(t, events...)), idle)
}

func completedStreamEvent(delay time.Duration) pacedStreamEvent {
	return pacedStreamEvent{delay: delay, data: `{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done"}]}]}}`}
}

func TestGenerateStream_HealthyLongStreamSurvivesTotalWallClockBeyondIdle(t *testing.T) {
	idle := 500 * time.Millisecond
	gap := 80 * time.Millisecond
	client := newPacedWatchdogClient(t, idle,
		pacedStreamEvent{delay: gap, data: `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[]}}`},
		pacedStreamEvent{delay: gap, data: `{"type":"response.output_text.delta","delta":"Do"}`},
		pacedStreamEvent{delay: gap, data: `{"type":"response.output_text.delta","delta":"ne"}`},
		pacedStreamEvent{delay: gap, data: `{"type":"response.output_text.delta","delta":"!"}`},
		pacedStreamEvent{delay: gap, data: `{"type":"response.output_text.delta","delta":"!"}`},
		pacedStreamEvent{delay: gap, data: `{"type":"response.output_text.delta","delta":"!"}`},
		pacedStreamEvent{delay: gap, data: `{"type":"response.output_text.delta","delta":"!"}`},
		completedStreamEvent(gap),
		pacedStreamEvent{delay: 0, data: `[DONE]`},
	)

	resp, err := client.GenerateStreamWithEvents(context.Background(), Request{Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("healthy long stream failed: %v", err)
	}
	if resp.Assistant.Content != "Done" {
		t.Fatalf("assistant text = %q, want Done", resp.Assistant.Content)
	}
}

func TestGenerateStream_StalledStreamReturnsStallSentinel(t *testing.T) {
	client := newPacedWatchdogClient(t, 120*time.Millisecond,
		pacedStreamEvent{delay: 20 * time.Millisecond, data: `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[]}}`},
		completedStreamEvent(5*time.Second),
		pacedStreamEvent{delay: 0, data: `[DONE]`},
	)

	ctx := context.Background()
	_, err := client.GenerateStreamWithEvents(ctx, Request{Model: "gpt-5"}, StreamCallbacks{})
	if err == nil {
		t.Fatal("expected stall error")
	}
	if !errors.Is(err, ErrModelStreamStalled) {
		t.Fatalf("err = %v, want ErrModelStreamStalled", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("stall error must not wrap context.Canceled: %v", err)
	}
	if IsNonRetriableModelError(err) {
		t.Fatalf("stall error should be retriable")
	}
	if ctx.Err() != nil {
		t.Fatalf("parent context must remain live after a stall, got %v", ctx.Err())
	}
}

func TestGenerateStream_ParentCancelIsDistinguishableFromStall(t *testing.T) {
	client := newPacedWatchdogClient(t, 5*time.Second,
		pacedStreamEvent{delay: 20 * time.Millisecond, data: `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[]}}`},
		completedStreamEvent(5*time.Second),
		pacedStreamEvent{delay: 0, data: `[DONE]`},
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()

	_, err := client.GenerateStreamWithEvents(ctx, Request{Model: "gpt-5"}, StreamCallbacks{})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if errors.Is(err, ErrModelStreamStalled) {
		t.Fatalf("parent cancel must not classify as stall: %v", err)
	}
}

func TestGenerateStream_StallAfterCompletedSalvagesResponse(t *testing.T) {
	client := newPacedWatchdogClient(t, 120*time.Millisecond,
		pacedStreamEvent{delay: 20 * time.Millisecond, data: `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[]}}`},
		pacedStreamEvent{delay: 20 * time.Millisecond, data: `{"type":"response.output_text.delta","delta":"Done"}`},
		completedStreamEvent(20*time.Millisecond),
		pacedStreamEvent{delay: 5 * time.Second, data: `[DONE]`},
	)

	resp, err := client.GenerateStreamWithEvents(context.Background(), Request{Model: "gpt-5"}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("a fully-received response must not be discarded as a stall: %v", err)
	}
	if resp.Assistant.Content != "Done" {
		t.Fatalf("assistant text = %q, want Done", resp.Assistant.Content)
	}
}

func TestGenerateStream_TransportEmitsActivityHeartbeatPerEvent(t *testing.T) {
	transport := newOpenAIStreamTestTransport(t,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","content":[]}}`,
		`{"type":"response.output_text.delta","delta":"Done"}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done"}]}]}}`,
		`[DONE]`,
	)

	var beats atomic.Int32
	if _, err := transport.GenerateStreamWithEvents(context.Background(), OpenAIRequest{Model: "gpt-5"}, StreamCallbacks{
		OnStreamActivity: func() { beats.Add(1) },
	}); err != nil {
		t.Fatalf("GenerateStreamWithEvents failed: %v", err)
	}
	if got := beats.Load(); got != 3 {
		t.Fatalf("activity beats = %d, want one per streamed event (3)", got)
	}
}
