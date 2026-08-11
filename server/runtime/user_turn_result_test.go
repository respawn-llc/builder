package runtime

import (
	"testing"

	"core/server/llm"
	"core/shared/textutil"
)

func TestUserTurnResultKindReservesZeroForUnset(t *testing.T) {
	if (UserTurnResult{}).Kind != UserTurnResultInvalid {
		t.Fatalf("zero user-turn result kind = %d, want invalid", (UserTurnResult{}).Kind)
	}
	result := userTurnResultFromStepLoop(stepLoopResult{})
	if result.Kind != UserTurnResultNoFinal {
		t.Fatalf("empty step-loop result kind = %d, want no final", result.Kind)
	}
	if result.Kind == UserTurnResultInvalid {
		t.Fatal("classified no-final result reused invalid zero kind")
	}
}

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

func TestUserTurnResultFromStepLoopTreatsMissingFinalContentAsNoFinal(t *testing.T) {
	result := userTurnResultFromStepLoop(stepLoopResult{
		FinalAnswer: &llm.Message{
			Role:  llm.RoleAssistant,
			Phase: textutil.Value(llm.MessagePhaseFinal),
		},
	})

	if result.Kind != UserTurnResultNoFinal {
		t.Fatalf("result kind = %d, want no final", result.Kind)
	}
	if result.FinalAnswer != nil {
		t.Fatalf("final answer = %+v, want absent", result.FinalAnswer)
	}
}
