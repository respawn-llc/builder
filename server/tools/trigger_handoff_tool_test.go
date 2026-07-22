package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"core/server/llm"
	"core/shared/toolspec"
)

type controllerStub struct {
	activeCall         llm.ToolCall
	stepID             string
	summarizerPrompt   string
	futureAgentMessage string
	summary            string
	futureAdded        bool
	err                error
}

func (s *controllerStub) TriggerHandoff(_ context.Context, stepID string, activeCall llm.ToolCall, summarizerPrompt string, futureAgentMessage string) (string, bool, error) {
	s.activeCall = activeCall
	s.stepID = stepID
	s.summarizerPrompt = summarizerPrompt
	s.futureAgentMessage = futureAgentMessage
	if s.err != nil {
		return "", false, s.err
	}
	return s.summary, s.futureAdded, nil
}

func callTriggerHandoffTool(t *testing.T, tool *TriggerHandoffTool, id string, input json.RawMessage, stepID string) Result {
	t.Helper()
	result, err := tool.Call(context.Background(), Call{ID: id, Name: toolspec.ToolTriggerHandoff, Input: input, StepID: stepID})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	return result
}

func TestToolCallPassesArgumentsToController(t *testing.T) {
	stub := &controllerStub{summary: "Handoff triggered.", futureAdded: true}
	tool := NewTriggerHandoffTool(func() TriggerHandoffController { return stub })
	input := json.RawMessage(`{"summarizer_prompt":"keep API details","future_agent_message":"resume with tests"}`)

	result := callTriggerHandoffTool(t, tool, "call-1", input, "step-1")
	if result.IsError {
		t.Fatalf("expected success result, got %s", string(result.Output))
	}
	if stub.stepID != "step-1" || stub.summarizerPrompt != "keep API details" || stub.futureAgentMessage != "resume with tests" {
		t.Fatalf("unexpected controller args: %+v", stub)
	}
	if stub.activeCall.ID != "call-1" || stub.activeCall.Name != string(toolspec.ToolTriggerHandoff) {
		t.Fatalf("unexpected active call: %+v", stub.activeCall)
	}
	if string(stub.activeCall.Input) != string(input) {
		t.Fatalf("unexpected active call input: %s", string(stub.activeCall.Input))
	}
	var payload TriggerHandoffResultPayload
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if payload.Summary != "Handoff triggered." || !payload.FutureAgentMessageAdded {
		t.Fatalf("unexpected output payload: %+v", payload)
	}
}

func TestToolCallTreatsArgsAsOptional(t *testing.T) {
	stub := &controllerStub{summary: "Handoff triggered."}
	tool := NewTriggerHandoffTool(func() TriggerHandoffController { return stub })

	result := callTriggerHandoffTool(t, tool, "call-1", json.RawMessage(`{}`), "step-2")
	if result.IsError {
		t.Fatalf("expected success result, got %s", string(result.Output))
	}
	if stub.summarizerPrompt != "" || stub.futureAgentMessage != "" {
		t.Fatalf("expected optional args to remain blank, got %+v", stub)
	}
}

func TestToolCallReturnsControllerErrorsAsToolErrors(t *testing.T) {
	tool := NewTriggerHandoffTool(func() TriggerHandoffController { return &controllerStub{err: errors.New("too early")} })

	result := callTriggerHandoffTool(t, tool, "call-1", json.RawMessage(`{}`), "step-3")
	if !result.IsError {
		t.Fatalf("expected tool error result, got %s", string(result.Output))
	}
	if got := string(result.Output); got == "" || !strings.Contains(got, "trigger_handoff failed: too early") || !strings.Contains(got, "Retry only after the developer compaction reminder is present") {
		t.Fatalf("unexpected error output: %q", got)
	}
}

func TestToolCallReturnsGuidanceForInvalidInput(t *testing.T) {
	tool := NewTriggerHandoffTool(func() TriggerHandoffController { return &controllerStub{} })

	result := callTriggerHandoffTool(t, tool, "call-1", json.RawMessage(`{"summarizer_prompt":123}`), "step-4")
	if !result.IsError {
		t.Fatalf("expected tool error result, got %s", string(result.Output))
	}
	if got := string(result.Output); !strings.Contains(got, "invalid trigger_handoff input") || !strings.Contains(got, "future_agent_message") {
		t.Fatalf("unexpected invalid-input output: %q", got)
	}
}
