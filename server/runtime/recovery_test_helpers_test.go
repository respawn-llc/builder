package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type finishFailureLifecycleSink struct {
	gate    *sessiontest.PersistenceGate
	failure error
	ended   *StepLifecycleSnapshot
}

func (s *finishFailureLifecycleSink) StepBegan(context.Context, StepLifecycleSnapshot) error {
	return nil
}

func (s *finishFailureLifecycleSink) StepEnded(_ context.Context, snapshot StepLifecycleSnapshot) error {
	s.ended = &snapshot
	s.gate.FailNext(s.failure)
	return nil
}

func persistSuccessfulTriggerHandoff(t *testing.T, engine *Engine, callID string) llm.ToolCall {
	t.Helper()
	call := llm.ToolCall{
		ID: callID, Name: string(toolspec.ToolTriggerHandoff),
		Input: mustJSON(map[string]any{"summarizer_prompt": "summarize", "future_agent_message": "continue"}),
	}
	if err := engine.steer("handoff", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal, steeringMessageEventNone, true,
		[]llm.Message{{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseCommentary), Content: textutil.Value("handoff"), ToolCalls: []llm.ToolCall{call}}},
	)); err != nil {
		t.Fatalf("persist trigger-handoff call: %v", err)
	}
	if err := engine.steer("handoff", steerToolCompletionIntent(tools.Result{
		CallID: call.ID, Name: toolspec.ToolTriggerHandoff,
		Output: mustJSON(tools.TriggerHandoffResultPayload{FutureAgentMessageAdded: true}),
	})); err != nil {
		t.Fatalf("persist trigger-handoff completion: %v", err)
	}
	return call
}
