package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/server/workflowruntime"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestWorkflowAgentPanicsBeforeSecondModelTurnWithoutWorkflowInstructions(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseCommentary),
			Content: textutil.Value("working"),
		},
		ToolCalls: []llm.ToolCall{{
			ID:    "continue",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"true"}`),
		}},
		Usage: llm.Usage{WindowTokens: 200_000},
	}}}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{
			Model: "gpt-5",
			EnabledTools: []toolspec.ID{
				toolspec.ToolExecCommand,
			},
			WorkflowPrompt: &workflowruntime.PromptContract{
				Identity:       "missing-workflow-instructions",
				CompletionMode: workflowruntime.CompletionModeShellCommand,
			},
		},
	)

	defer func() {
		recovered := recover()
		if recovered != "workflow mode skipped prompt" {
			t.Fatalf("panic = %v, want workflow mode skipped prompt", recovered)
		}
	}()

	_ = engine.stepLifecycle.Run(
		context.Background(),
		exclusiveStepOptions{ActiveKind: ActiveKindWorkflowTurn},
		func(stepCtx context.Context, stepID string) error {
			_, err := engine.stepFlow.RunStepLoopWithOptions(stepCtx, stepID, stepLoopOptions{})
			return err
		},
	)
}
