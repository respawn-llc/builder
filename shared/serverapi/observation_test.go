package serverapi

import (
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

func TestWorkflowTaskObservationResponseValidatesTypedOutcomes(t *testing.T) {
	sessionID := observationSessionID(t, "9b9447ad-04e7-4c70-b4b0-f0eb1a53b47d")
	rawSessionID := sessionID.String()
	question := ObservationQuestion{Ask: &clientui.PendingAsk{
		PromptID: "ask-1", SessionID: sessionID, StepID: observationStepID(t),
		Question: "Continue?",
	}}
	response := WorkflowTaskObservationResponse{
		TaskID: "task-1", TaskShortID: "KNT-42",
		Outcomes: []WorkflowTaskObservationOutcome{{
			Kind:      WorkflowTaskObservationQuestion,
			SessionID: &rawSessionID,
			Question:  &question,
		}},
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("valid observation response rejected: %v", err)
	}
}

func TestWorkflowTaskObservationResponseRejectsInvalidOutcomePayloads(t *testing.T) {
	cases := []WorkflowTaskObservationResponse{
		{TaskID: "task-1", TaskShortID: "KNT-42"},
		{
			TaskID: "task-1", TaskShortID: "KNT-42",
			Outcomes: []WorkflowTaskObservationOutcome{{
				Kind:     WorkflowTaskObservationQuestion,
				Question: &ObservationQuestion{},
			}},
		},
		{
			TaskID: "task-1", TaskShortID: "KNT-42",
			Outcomes: []WorkflowTaskObservationOutcome{{
				Kind: WorkflowTaskObservationQuestion,
				Question: &ObservationQuestion{Ask: &clientui.PendingAsk{
					PromptID: "ask-1", SessionID: observationSessionID(t, "session-1"),
				}},
			}},
		},
		{
			TaskID: "task-1", TaskShortID: "KNT-42",
			Outcomes: []WorkflowTaskObservationOutcome{{
				Kind: WorkflowTaskObservationQuestion,
				Question: &ObservationQuestion{Approval: &clientui.PendingApproval{
					PromptID: "approval-1", SessionID: observationSessionID(t, "session-1"),
					StepID: observationStepID(t), Question: "Allow?",
				}},
			}},
		},
		{
			TaskID: "task-1", TaskShortID: "KNT-42",
			Outcomes: []WorkflowTaskObservationOutcome{{
				Kind:    WorkflowTaskObservationDone,
				Failure: &RuntimeLiveWatchFailure{Reason: "invalid"},
			}},
		},
	}
	for index, response := range cases {
		if err := response.Validate(); err == nil {
			t.Fatalf("case %d unexpectedly validated: %+v", index, response)
		}
	}
}

func TestRuntimeLiveWatchResponseRejectsQuestionSessionMismatch(t *testing.T) {
	for _, question := range []ObservationQuestion{
		{Ask: &clientui.PendingAsk{
			PromptID: "ask-1", SessionID: observationSessionID(t, "session-b"),
			StepID: observationStepID(t), Question: "Continue?",
		}},
		{Approval: &clientui.PendingApproval{
			PromptID: "approval-1", SessionID: observationSessionID(t, "session-b"),
			StepID: observationStepID(t), Question: "Allow?",
			Options: []clientui.ApprovalOption{{Decision: clientui.ApprovalDecisionAllowOnce}},
		}},
	} {
		response := RuntimeLiveWatchResponse{
			SessionID: "session-a",
			Outcome: RuntimeLiveWatchOutcome{
				Kind:     RuntimeLiveWatchQuestion,
				Question: &question,
			},
		}
		if err := response.Validate(); err == nil {
			t.Fatalf("question session mismatch unexpectedly validated: %+v", response)
		}
	}
}

func TestObservationQuestionRejectsDuplicateApprovalDecisions(t *testing.T) {
	question := ObservationQuestion{Approval: &clientui.PendingApproval{
		PromptID:  "approval-1",
		SessionID: observationSessionID(t, "session-1"),
		StepID:    observationStepID(t),
		Question:  "Allow?",
		Options: []clientui.ApprovalOption{
			{Decision: clientui.ApprovalDecisionAllowOnce},
			{Decision: clientui.ApprovalDecisionAllowOnce},
		},
	}}
	if err := question.Validate(); err == nil {
		t.Fatal("duplicate approval decisions unexpectedly validated")
	}
}

func observationSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}

func observationStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	id, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParseStepID: %v", err)
	}
	return id
}
