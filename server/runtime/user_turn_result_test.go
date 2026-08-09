package runtime

import (
	"testing"

	"core/server/llm"
	"core/shared/textutil"
)

func TestUserTurnResultFromStepLoopTreatsBlankFinalAsNoFinal(t *testing.T) {
	result := userTurnResultFromStepLoop(stepLoopResult{
		FinalAnswer: &llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value(""),
		},
	})

	if result.Kind != UserTurnResultNoFinal {
		t.Fatalf("result kind = %d, want no final", result.Kind)
	}
	if result.FinalAnswer != nil {
		t.Fatalf("final answer = %+v, want absent", result.FinalAnswer)
	}
}
