package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
)

func TestExecuteToolCallsRejectsMissingProviderCallIDBeforeToolExecution(t *testing.T) {
	probe := &toolExecutionProbe{}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: probe,
		}),
		Config{Model: "gpt-5"},
	)

	_, err := engine.executeToolCalls(context.Background(), "step", []llm.ToolCall{{
		Name: string(toolspec.ToolExecCommand),
	}})
	if !errors.Is(err, ErrMissingProviderToolCallID) {
		t.Fatalf("execute tool calls error = %v, want missing provider call ID", err)
	}
	if probe.called {
		t.Fatal("missing provider call ID reached a local tool handler")
	}
}

func TestExecuteToolCallsRejectsInvalidWebSearchBeforeHandler(t *testing.T) {
	tests := []struct {
		name  string
		input json.RawMessage
	}{
		{name: "whitespace query", input: json.RawMessage(`{"query":"   "}`)},
		{name: "hallucinated query", input: json.RawMessage(`{"query":"web search"}`)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := &webSearchExecutionProbe{}
			var completionMu sync.Mutex
			var completionEvents []Event
			engine := mustNewTestEngine(
				t,
				mustCreateTestSession(t),
				&fakeClient{},
				tools.NewRegistry(tools.HandlerRegistration{
					ID:      toolspec.ToolWebSearch,
					Handler: probe,
				}),
				Config{
					Model: "gpt-5",
					OnEvent: func(event Event) {
						if event.Kind != EventToolCallCompleted || event.ToolResult == nil {
							return
						}
						result := *event.ToolResult
						completionMu.Lock()
						completionEvents = append(completionEvents, Event{
							Kind:                       event.Kind,
							CommittedTranscriptChanged: event.CommittedTranscriptChanged,
							ToolResult:                 &result,
						})
						completionMu.Unlock()
					},
				},
			)

			results, err := engine.executeToolCalls(context.Background(), "step", []llm.ToolCall{{
				ID:    "web-search-call",
				Name:  string(toolspec.ToolWebSearch),
				Input: test.input,
			}})
			if err != nil {
				t.Fatalf("execute invalid web search tool call: %v", err)
			}
			if got := probe.calls.Load(); got != 0 {
				t.Fatalf("invalid web search reached handler %d times", got)
			}
			if len(results) != 1 {
				t.Fatalf("invalid web search results = %+v, want one", results)
			}
			if result := results[0]; result.CallID != "web-search-call" ||
				result.Name != toolspec.ToolWebSearch ||
				!result.IsError {
				t.Fatalf("invalid web search result = %+v", result)
			}

			completionMu.Lock()
			defer completionMu.Unlock()
			if len(completionEvents) != 1 {
				t.Fatalf("persisted invalid web search completions = %+v, want one", completionEvents)
			}
			completion := completionEvents[0]
			if !completion.CommittedTranscriptChanged ||
				completion.ToolResult == nil ||
				completion.ToolResult.CallID != "web-search-call" ||
				completion.ToolResult.Name != toolspec.ToolWebSearch ||
				!completion.ToolResult.IsError {
				t.Fatalf("persisted invalid web search completion = %+v", completion)
			}
		})
	}
}

type toolExecutionProbe struct {
	called bool
}

func (p *toolExecutionProbe) Call(context.Context, tools.Call) (tools.Result, error) {
	p.called = true
	return tools.Result{}, nil
}

type webSearchExecutionProbe struct {
	calls atomic.Int32
}

func (p *webSearchExecutionProbe) Call(context.Context, tools.Call) (tools.Result, error) {
	p.calls.Add(1)
	return tools.Result{}, nil
}
