package runtime

import "core/server/llm"

type UserTurnResultKind uint8

const (
	UserTurnResultInvalid UserTurnResultKind = iota
	UserTurnResultNoFinal
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
		if result.FinalAnswer.Content == nil || isBlankFinalAnswer(*result.FinalAnswer) {
			return UserTurnResult{Kind: UserTurnResultNoFinal}
		}
		return UserTurnResult{
			Kind:        UserTurnResultAssistantFinal,
			FinalAnswer: result.FinalAnswer,
		}
	}
	return UserTurnResult{Kind: UserTurnResultNoFinal}
}

func WorkflowTurnUserResult(result WorkflowTurnResult) UserTurnResult {
	if result.Assistant.Content == nil || isBlankFinalAnswer(result.Assistant) {
		return UserTurnResult{Kind: UserTurnResultNoFinal}
	}
	message := result.Assistant
	return UserTurnResult{Kind: UserTurnResultAssistantFinal, FinalAnswer: &message}
}
