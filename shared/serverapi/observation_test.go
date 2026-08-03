package serverapi

import (
	"strings"
	"testing"

	"core/shared/clientui"
)

func TestRuntimeObservationTargetValidatesSessionAndTaskUnions(t *testing.T) {
	const (
		sessionID = "9b9447ad-04e7-4c70-b4b0-f0eb1a53b47d"
		taskID    = "8b0364cc-5c6c-412e-a4e8-31380661d1e1"
		projectID = "540a27aa-1e97-4696-8483-6d528ff8bbdd"
	)

	tests := []struct {
		name   string
		target RuntimeObservationTarget
		valid  bool
	}{
		{
			name:   "session",
			target: NewRuntimeObservationSessionTarget(sessionID),
			valid:  true,
		},
		{
			name:   "task",
			target: NewRuntimeObservationTaskTarget(taskID, "KNT-42", projectID),
			valid:  true,
		},
		{
			name:   "missing session identity",
			target: RuntimeObservationTarget{Kind: RuntimeObservationTargetSession},
		},
		{
			name: "blank session identity",
			target: RuntimeObservationTarget{
				Kind:      RuntimeObservationTargetSession,
				SessionID: observationStringPtr(" "),
			},
		},
		{
			name: "blank task identity",
			target: RuntimeObservationTarget{
				Kind:        RuntimeObservationTargetTask,
				TaskID:      observationStringPtr(taskID),
				TaskShortID: observationStringPtr(" "),
				ProjectID:   observationStringPtr(projectID),
			},
		},
		{
			name:   "task with session identity",
			target: RuntimeObservationTarget{Kind: RuntimeObservationTargetTask, SessionID: observationStringPtr(sessionID)},
		},
		{
			name:   "unknown kind",
			target: RuntimeObservationTarget{Kind: RuntimeObservationTargetKind("other"), SessionID: observationStringPtr(sessionID)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.target.Validate()
			if tt.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !tt.valid && err == nil {
				t.Fatal("Validate() accepted invalid target")
			}
		})
	}
}

func TestRuntimeObservationOutcomeRequiresExactlyOneTypedPayload(t *testing.T) {
	const sessionID = "9b9447ad-04e7-4c70-b4b0-f0eb1a53b47d"
	target := NewRuntimeObservationSessionTarget(sessionID)
	question := RuntimeObservationQuestion{
		QuestionID: "question-1",
		Text:       "Which environment?",
		Kind:       RuntimeObservationQuestionAccessRequest,
		AccessOptions: []clientui.ApprovalOption{
			{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Permit this operation"},
			{Decision: clientui.ApprovalDecisionDeny, Label: "No, keep it contained"},
		},
	}

	valid := RuntimeObservationResponse{
		Target: target,
		Outcomes: []RuntimeObservationOutcome{{
			Kind:      RuntimeObservationOutcomeQuestion,
			SessionID: observationStringPtr(sessionID),
			Question:  &question,
		}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := valid.Outcomes[0].Question.AccessOptions[0].Label; got != "Permit this operation" {
		t.Fatalf("access option label = %q", got)
	}

	cases := []RuntimeObservationOutcome{
		{},
		{
			Kind:     RuntimeObservationOutcomeQuestion,
			Question: &question,
			FinalAnswer: &RuntimeObservationFinalAnswer{
				Result: observationStringPtr("also present"),
			},
		},
		{
			Kind:     RuntimeObservationOutcomeQuestion,
			Question: &RuntimeObservationQuestion{QuestionID: "ordinary", Text: "answer"},
		},
	}
	for _, outcome := range cases {
		if err := (RuntimeObservationResponse{
			Target:   target,
			Outcomes: []RuntimeObservationOutcome{outcome},
		}).Validate(); err == nil {
			t.Fatalf("Validate() accepted invalid outcome: %+v", outcome)
		}
	}
}

func observationStringPtr(value string) *string {
	return &value
}

func TestRuntimeObservationQuestionRejectsInferredAccessLabels(t *testing.T) {
	question := RuntimeObservationQuestion{
		QuestionID: "question-1",
		Text:       "Access?",
		Kind:       RuntimeObservationQuestionAccessRequest,
		AccessOptions: []clientui.ApprovalOption{
			{Decision: clientui.ApprovalDecisionAllowOnce, Label: "custom allow"},
		},
	}
	if err := question.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if strings.Contains(question.AccessOptions[0].Label, "Allow once") {
		t.Fatal("test fixture accidentally uses a canonical inferred label")
	}
}
