package sessioncontract

import "testing"

func TestPromptAnswerShapeValidation(t *testing.T) {
	selected := 1
	zero := 0
	freeform := "answer"
	blank := " "

	for _, answer := range []struct {
		name     string
		selected *int
		freeform *string
	}{
		{name: "selected", selected: &selected},
		{name: "freeform", freeform: &freeform},
		{name: "selected with freeform", selected: &selected, freeform: &freeform},
	} {
		t.Run("Question valid "+answer.name, func(t *testing.T) {
			if err := ValidatePromptQuestionAnswerShape(answer.selected, answer.freeform); err != nil {
				t.Fatalf("ValidatePromptQuestionAnswerShape: %v", err)
			}
		})
	}
	for _, answer := range []struct {
		name     string
		selected *int
		freeform *string
	}{
		{name: "absent"},
		{name: "nonpositive selection", selected: &zero},
		{name: "blank freeform", freeform: &blank},
	} {
		t.Run("Question invalid "+answer.name, func(t *testing.T) {
			if err := ValidatePromptQuestionAnswerShape(answer.selected, answer.freeform); err == nil {
				t.Fatal("invalid Question answer shape validated")
			}
		})
	}

	commentary := "context"
	for _, decision := range []PromptApprovalDecision{
		PromptApprovalDecisionAllowOnce,
		PromptApprovalDecisionAllowSession,
		PromptApprovalDecisionDeny,
	} {
		t.Run("Approval valid "+string(decision), func(t *testing.T) {
			if err := ValidatePromptApprovalAnswerShape(decision, &commentary); err != nil {
				t.Fatalf("ValidatePromptApprovalAnswerShape: %v", err)
			}
		})
	}
	for _, answer := range []struct {
		name       string
		decision   PromptApprovalDecision
		commentary *string
	}{
		{name: "invalid decision", decision: "later"},
		{name: "blank commentary", decision: PromptApprovalDecisionAllowOnce, commentary: &blank},
	} {
		t.Run("Approval invalid "+answer.name, func(t *testing.T) {
			if err := ValidatePromptApprovalAnswerShape(answer.decision, answer.commentary); err == nil {
				t.Fatal("invalid Approval answer shape validated")
			}
		})
	}
}
