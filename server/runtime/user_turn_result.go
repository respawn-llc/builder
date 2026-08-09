package runtime

import "core/server/llm"

type UserTurnResultKind uint8

const (
	UserTurnResultNoFinal UserTurnResultKind = iota
	UserTurnResultAssistantFinal
	UserTurnResultSilentFinal
)

type UserTurnResult struct {
	Kind        UserTurnResultKind
	FinalAnswer *llm.Message
}

func userTurnResultFromStepLoop(result stepLoopResult) UserTurnResult {
	if result.SilentFinal {
		return UserTurnResult{Kind: UserTurnResultSilentFinal}
	}
	if result.FinalAnswer != nil {
		if isBlankFinalAnswer(*result.FinalAnswer) {
			return UserTurnResult{Kind: UserTurnResultNoFinal}
		}
		return UserTurnResult{
			Kind:        UserTurnResultAssistantFinal,
			FinalAnswer: result.FinalAnswer,
		}
	}
	return UserTurnResult{Kind: UserTurnResultNoFinal}
}
