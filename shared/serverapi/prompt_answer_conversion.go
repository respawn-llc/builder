package serverapi

import (
	"errors"

	"core/shared/clientui"
)

type PromptAnswer struct {
	question *PromptQuestionAnswer
	approval *PromptApprovalAnswer
	declined *PromptDeclined
}

func QuestionPromptAnswer(answer PromptQuestionAnswer) PromptAnswer {
	return PromptAnswer{question: &answer}
}

func ApprovalPromptAnswer(answer PromptApprovalAnswer) PromptAnswer {
	return PromptAnswer{approval: &answer}
}

func DeclinedPromptAnswer() PromptAnswer {
	return PromptAnswer{declined: &PromptDeclined{}}
}

func PromptAnswerBatchEntryFrom(
	promptID clientui.PromptID,
	answer PromptAnswer,
) (PromptAnswerBatchEntry, error) {
	entry := PromptAnswerBatchEntry{
		PromptID:       promptID,
		QuestionAnswer: answer.question,
		ApprovalAnswer: answer.approval,
		Declined:       answer.declined,
	}
	if answer.question == nil && answer.approval == nil && answer.declined == nil {
		return PromptAnswerBatchEntry{}, errors.New("prompt answer is required")
	}
	if err := entry.Validate(); err != nil {
		return PromptAnswerBatchEntry{}, err
	}
	return entry, nil
}
