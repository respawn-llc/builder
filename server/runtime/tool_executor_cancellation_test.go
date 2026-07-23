package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
)

func TestExecuteToolCallsPropagatesContextCancellation(t *testing.T) {
	store := mustCreateTestSession(t)
	started := make(chan struct{})
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID: toolspec.ToolExecCommand,
			Handler: cancellationAwareTool{
				started: started,
			},
		}),
		Config{Model: "gpt-5"},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := engine.executeToolCalls(ctx, "step", []llm.ToolCall{{
			ID:    "canceled-call",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{}`),
		}})
		done <- err
	}()

	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("execute tool calls error = %v, want context cancellation", err)
	}
	if _, ok := engine.transcriptRuntimeState().ToolCompletionSnapshot("canceled-call"); !ok {
		t.Fatal("canceled tool completion was not persisted before returning")
	}
}

type cancellationAwareTool struct {
	started chan struct{}
}

func (t cancellationAwareTool) Call(ctx context.Context, call tools.Call) (tools.Result, error) {
	select {
	case <-t.started:
	default:
		close(t.started)
	}
	<-ctx.Done()
	return tools.Result{
		CallID:  call.ID,
		Name:    call.Name,
		IsError: true,
		Output:  json.RawMessage(`{"error":"canceled"}`),
	}, ctx.Err()
}
