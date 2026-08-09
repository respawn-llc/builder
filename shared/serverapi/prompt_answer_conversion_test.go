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
	tests := []struct {
		name     string
		answer   PromptAnswer
		expected PromptAnswerBatchEntry
	}{
		{
			name: "question",
			answer: QuestionPromptAnswer(PromptQuestionAnswer{
				SelectedOptionNumber: &selected,
				Freeform:             &freeform,
			}),
			expected: PromptAnswerBatchEntry{
				PromptID: "prompt-1",
				QuestionAnswer: &PromptQuestionAnswer{
					SelectedOptionNumber: &selected,
					Freeform:             &freeform,
				},
			},
		},
		{
			name: "approval",
			answer: ApprovalPromptAnswer(PromptApprovalAnswer{
				Decision:   clientui.ApprovalDecisionAllowOnce,
				Commentary: &commentary,
			}),
			expected: PromptAnswerBatchEntry{
				PromptID: "prompt-1",
				ApprovalAnswer: &PromptApprovalAnswer{
					Decision:   clientui.ApprovalDecisionAllowOnce,
					Commentary: &commentary,
				},
			},
		},
		{
			name:     "declined",
			answer:   DeclinedPromptAnswer(),
			expected: PromptAnswerBatchEntry{PromptID: "prompt-1", Declined: &PromptDeclined{}},
		},
		{
			name: "question absent optional text",
			answer: QuestionPromptAnswer(PromptQuestionAnswer{
				SelectedOptionNumber: textutil.Value(1),
			}),
			expected: PromptAnswerBatchEntry{PromptID: "prompt-1", QuestionAnswer: &PromptQuestionAnswer{
				SelectedOptionNumber: textutil.Value(1),
			}},
		},
		{
			name: "approval absent optional text",
			answer: ApprovalPromptAnswer(PromptApprovalAnswer{
				Decision: clientui.ApprovalDecisionDeny,
			}),
			expected: PromptAnswerBatchEntry{PromptID: "prompt-1", ApprovalAnswer: &PromptApprovalAnswer{
				Decision: clientui.ApprovalDecisionDeny,
			}},
		},
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
