package serverapi

import (
	"testing"

	"core/shared/clientui"
)

func TestWorkflowTaskObservationResponseValidatesTypedOutcomes(t *testing.T) {
	sessionID := "9b9447ad-04e7-4c70-b4b0-f0eb1a53b47d"
	question := ObservationQuestion{Ask: &clientui.PendingAsk{
		AskID: "ask-1", SessionID: sessionID, Question: "Continue?",
	}}
	response := WorkflowTaskObservationResponse{
		TaskID: "task-1", TaskShortID: "KNT-42",
		Outcomes: []WorkflowTaskObservationOutcome{{
			Kind:      WorkflowTaskObservationQuestion,
			SessionID: &sessionID,
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
					AskID: "ask-1", SessionID: "session-1",
				}},
			}},
		},
		{
			TaskID: "task-1", TaskShortID: "KNT-42",
			Outcomes: []WorkflowTaskObservationOutcome{{
				Kind: WorkflowTaskObservationQuestion,
				Question: &ObservationQuestion{Approval: &clientui.PendingApproval{
					SessionID: "session-1", Question: "Allow?",
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
