package runtime

import (
	"context"
	"errors"
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

type toolExecutionProbe struct {
	called bool
}

func (p *toolExecutionProbe) Call(context.Context, tools.Call) (tools.Result, error) {
	p.called = true
	return tools.Result{}, nil
}
