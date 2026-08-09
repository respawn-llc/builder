package serverapi

import (
	"reflect"
	"testing"

	"core/shared/clientui"
	"core/shared/textutil"
)

func TestPromptAnswerBatchEntryFromTypedAnswer(t *testing.T) {
	selected := 2
	freeform := "details"
	commentary := "approved"
	question := PromptQuestionAnswer{SelectedOptionNumber: &selected, Freeform: &freeform}
	approval := PromptApprovalAnswer{Decision: clientui.ApprovalDecisionAllowOnce, Commentary: &commentary}
	optionOnly := PromptQuestionAnswer{SelectedOptionNumber: textutil.Value(1)}
	deny := PromptApprovalAnswer{Decision: clientui.ApprovalDecisionDeny}
	tests := []struct {
		name     string
		answer   PromptAnswer
		expected PromptAnswerBatchEntry
	}{
		{name: "question", answer: QuestionPromptAnswer(question), expected: PromptAnswerBatchEntry{PromptID: "prompt-1", QuestionAnswer: &question}},
		{name: "approval", answer: ApprovalPromptAnswer(approval), expected: PromptAnswerBatchEntry{PromptID: "prompt-1", ApprovalAnswer: &approval}},
		{name: "declined", answer: DeclinedPromptAnswer(), expected: PromptAnswerBatchEntry{PromptID: "prompt-1", Declined: &PromptDeclined{}}},
		{name: "question absent optional text", answer: QuestionPromptAnswer(optionOnly), expected: PromptAnswerBatchEntry{PromptID: "prompt-1", QuestionAnswer: &optionOnly}},
		{name: "approval absent optional text", answer: ApprovalPromptAnswer(deny), expected: PromptAnswerBatchEntry{PromptID: "prompt-1", ApprovalAnswer: &deny}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry, err := PromptAnswerBatchEntryFrom("prompt-1", test.answer)
			if err != nil {
				t.Fatalf("convert answer: %v", err)
			}
			if err := entry.Validate(); err != nil {
				t.Fatalf("validate entry: %v", err)
			}
			if !reflect.DeepEqual(entry, test.expected) {
				t.Fatalf("entry = %+v, want %+v", entry, test.expected)
			}
		})
	}
}

func TestPromptAnswerBatchEntryFromRejectsAbsentOrInvalidTypedAnswer(t *testing.T) {
	if _, err := PromptAnswerBatchEntryFrom("prompt-1", PromptAnswer{}); err == nil {
		t.Fatal("absent answer succeeded")
	}
	if _, err := PromptAnswerBatchEntryFrom("", DeclinedPromptAnswer()); err == nil {
		t.Fatal("blank prompt id succeeded")
	}
}
