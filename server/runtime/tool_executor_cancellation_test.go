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

type cancelAwareTool struct {
	name    toolspec.ID
	started chan struct{}
}

type executionIdentityCapturingTool struct {
	identity tools.ExecutionIdentity
}

func (t cancelAwareTool) Call(ctx context.Context, c tools.Call) (tools.Result, error) {
	select {
	case <-t.started:
	default:
		close(t.started)
	}
	<-ctx.Done()
	out, _ := json.Marshal(map[string]any{"error": ctx.Err().Error()})
	return tools.Result{CallID: c.ID, Name: c.Name, IsError: true, Output: out, Summary: ctx.Err().Error()}, ctx.Err()
}

func (t *executionIdentityCapturingTool) Call(ctx context.Context, c tools.Call) (tools.Result, error) {
	identity, err := tools.ExecutionIdentityFromContext(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	t.identity = identity
	out, _ := json.Marshal("ok")
	return tools.Result{CallID: c.ID, Name: c.Name, Output: out}, nil
}

func TestExecuteToolCallsProvidesActiveExecutionIdentityToHandler(t *testing.T) {
	store := mustCreateTestSession(t)
	handler := &executionIdentityCapturingTool{}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{
		ID: toolspec.ToolExecCommand, Handler: handler,
	}), Config{Model: "gpt-5"})
	lifecycle := eng.stepLifecycle

	err := lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn, EmitRunState: true}, func(ctx context.Context, stepID string) error {
		snapshot := lifecycle.Snapshot()
		if snapshot == nil {
			t.Fatal("expected active run snapshot")
		}
		results, err := eng.executeToolCalls(ctx, stepID, []llm.ToolCall{{
			ID:    "call-1",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{}`),
		}})
		if err != nil {
			t.Fatalf("execute tool calls: %v", err)
		}
		if len(results) != 1 || results[0].IsError {
			t.Fatalf("results = %+v, want successful call", results)
		}
		if handler.identity.RunID != snapshot.RunID || handler.identity.StepID != stepID {
			t.Fatalf("handler identity = %+v, want run %q step %q", handler.identity, snapshot.RunID, stepID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestExecuteToolCallsPropagatesContextCancellation(t *testing.T) {
	store := mustCreateTestSession(t)
	started := make(chan struct{})
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: cancelAwareTool{name: toolspec.ToolExecCommand, started: started}}), Config{Model: "gpt-5"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := eng.executeToolCalls(ctx, "step-1", []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)}})
		done <- err
	}()

	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("executeToolCalls error=%v, want context.Canceled", err)
	}
	if _, ok := eng.transcriptRuntimeState().ToolCompletionSnapshot("call-1"); !ok {
		t.Fatal("expected canceled tool completion to be persisted before returning")
	}
}
