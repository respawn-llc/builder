package serverapi

import (
	"reflect"
	"testing"

	"core/shared/clientui"
	"core/shared/textutil"
)

func TestPromptAnswerBatchEntryFromTypedAnswer(t *testing.T) {
	selected, freeform, commentary := 2, "details", "approved"
	question, approval := PromptQuestionAnswer{SelectedOptionNumber: &selected, Freeform: &freeform},
		PromptApprovalAnswer{Decision: clientui.ApprovalDecisionAllowOnce, Commentary: &commentary}
	optionOnly := PromptQuestionAnswer{SelectedOptionNumber: textutil.Value(1)}
	deny := PromptApprovalAnswer{Decision: clientui.ApprovalDecisionDeny}
	tests := []struct {
		name     string
		answer   PromptAnswer
		expected PromptAnswerBatchEntry
	}{
		{name: "question", answer: QuestionPromptAnswer(question), expected: PromptAnswerBatchEntry{ToolCallID: "prompt-1", QuestionAnswer: &question}},
		{name: "approval", answer: ApprovalPromptAnswer(approval), expected: PromptAnswerBatchEntry{ToolCallID: "prompt-1", ApprovalAnswer: &approval}},
		{name: "declined", answer: DeclinedPromptAnswer(), expected: PromptAnswerBatchEntry{ToolCallID: "prompt-1", Declined: &PromptDeclined{}}},
		{name: "question absent optional text", answer: QuestionPromptAnswer(optionOnly), expected: PromptAnswerBatchEntry{ToolCallID: "prompt-1", QuestionAnswer: &optionOnly}},
		{name: "approval absent optional text", answer: ApprovalPromptAnswer(deny), expected: PromptAnswerBatchEntry{ToolCallID: "prompt-1", ApprovalAnswer: &deny}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry, err := PromptAnswerBatchEntryFrom("prompt-1", test.answer)
			if err != nil {
				t.Fatalf("convert answer: %v", err)
			}
			if !reflect.DeepEqual(entry, test.expected) {
				t.Fatalf("entry = %+v, want %+v", entry, test.expected)
			}
		})
	}
}
func TestPromptAnswerBatchEntryFromRejectsAbsentOrInvalidTypedAnswer(t *testing.T) {
	_, absentErr := PromptAnswerBatchEntryFrom("prompt-1", PromptAnswer{})
	_, blankIDErr := PromptAnswerBatchEntryFrom("", DeclinedPromptAnswer())
	if absentErr == nil || blankIDErr == nil {
		t.Fatalf("invalid conversion errors = absent %v, blank ID %v", absentErr, blankIDErr)
	}
}
