package serverapi

import (
	"testing"

	"core/shared/clientui"
	"core/shared/textutil"
)

func TestPromptAnswerBatchEntryFromTypedAnswer(t *testing.T) {
	selected := 2
	freeform := "details"
	commentary := "approved"
	tests := []struct {
		name   string
		answer PromptAnswer
		assert func(*testing.T, PromptAnswerBatchEntry)
	}{
		{
			name: "question",
			answer: QuestionPromptAnswer(PromptQuestionAnswer{
				SelectedOptionNumber: &selected,
				Freeform:             &freeform,
			}),
			assert: func(t *testing.T, entry PromptAnswerBatchEntry) {
				t.Helper()
				if entry.QuestionAnswer == nil ||
					entry.QuestionAnswer.SelectedOptionNumber == nil ||
					*entry.QuestionAnswer.SelectedOptionNumber != selected ||
					entry.QuestionAnswer.Freeform == nil ||
					*entry.QuestionAnswer.Freeform != freeform {
					t.Fatalf("question entry = %+v", entry)
				}
			},
		},
		{
			name: "approval",
			answer: ApprovalPromptAnswer(PromptApprovalAnswer{
				Decision:   clientui.ApprovalDecisionAllowOnce,
				Commentary: &commentary,
			}),
			assert: func(t *testing.T, entry PromptAnswerBatchEntry) {
				t.Helper()
				if entry.ApprovalAnswer == nil ||
					entry.ApprovalAnswer.Decision != clientui.ApprovalDecisionAllowOnce ||
					entry.ApprovalAnswer.Commentary == nil ||
					*entry.ApprovalAnswer.Commentary != commentary {
					t.Fatalf("approval entry = %+v", entry)
				}
			},
		},
		{
			name:   "declined",
			answer: DeclinedPromptAnswer(),
			assert: func(t *testing.T, entry PromptAnswerBatchEntry) {
				t.Helper()
				if entry.Declined == nil {
					t.Fatalf("declined entry = %+v", entry)
				}
			},
		},
		{
			name: "question absent optional text",
			answer: QuestionPromptAnswer(PromptQuestionAnswer{
				SelectedOptionNumber: textutil.Value(1),
			}),
			assert: func(t *testing.T, entry PromptAnswerBatchEntry) {
				t.Helper()
				if entry.QuestionAnswer == nil || entry.QuestionAnswer.Freeform != nil {
					t.Fatalf("question entry = %+v", entry)
				}
			},
		},
		{
			name: "approval absent optional text",
			answer: ApprovalPromptAnswer(PromptApprovalAnswer{
				Decision: clientui.ApprovalDecisionDeny,
			}),
			assert: func(t *testing.T, entry PromptAnswerBatchEntry) {
				t.Helper()
				if entry.ApprovalAnswer == nil || entry.ApprovalAnswer.Commentary != nil {
					t.Fatalf("approval entry = %+v", entry)
				}
			},
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
			test.assert(t, entry)
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
