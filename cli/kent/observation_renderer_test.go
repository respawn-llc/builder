package main

import (
	"bytes"
	"strings"
	"testing"

	"core/shared/serverapi"
)

func TestWriteTaskObservationRendersParallelDiscriminatorsAndSeverity(t *testing.T) {
	sessionID := "9b9447ad-04e7-4c70-b4b0-f0eb1a53b47d"
	nodeAgent := "implement"
	nodeScript := "verify"
	scriptPath := "scripts/verify.sh"
	response := serverapi.WorkflowTaskObservationResponse{
		Target: serverapi.NewRuntimeObservationTaskTarget("task-1", "KNT-1", "project-1"),
		Outcomes: []serverapi.RuntimeObservationOutcome{
			{
				Kind:      serverapi.RuntimeObservationOutcomeInterrupted,
				NodeKey:   &nodeAgent,
				SessionID: &sessionID,
				Interrupted: &serverapi.RuntimeObservationInterrupted{
					Reason: "user_interrupt",
				},
			},
			{
				Kind:       serverapi.RuntimeObservationOutcomeExecutionError,
				NodeKey:    &nodeScript,
				ScriptPath: &scriptPath,
				ExecutionError: &serverapi.RuntimeObservationExecutionError{
					Reason: "script_failed",
				},
			},
		},
	}
	var output bytes.Buffer
	if got := writeTaskObservation(&output, response); got != 1 {
		t.Fatalf("exit code = %d, want failure severity", got)
	}
	for _, value := range []string{sessionID, nodeAgent, scriptPath, nodeScript, "user_interrupt", "script_failed"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("output %q does not contain dynamic value %q", output.String(), value)
		}
	}
}

func TestObservationQuestionHintKeepsTaskQualificationWhenExplicit(t *testing.T) {
	question := serverapi.RuntimeObservationQuestion{
		QuestionID:  "question-1",
		Text:        "Choose",
		Kind:        serverapi.RuntimeObservationQuestionOrdinary,
		Suggestions: []string{"one"},
	}
	outcome := serverapi.RuntimeObservationOutcome{
		Kind:     serverapi.RuntimeObservationOutcomeQuestion,
		Question: &question,
	}
	response := serverapi.WorkflowTaskObservationResponse{
		Target: serverapi.NewRuntimeObservationTaskTarget("task-1", "KNT-1", "project-1"),
		Outcomes: []serverapi.RuntimeObservationOutcome{outcome},
	}
	hint := observationQuestionHint(response, outcome, "project-1")
	for _, value := range []string{"KNT-1", "--project project-1", "--option"} {
		if !strings.Contains(hint, value) {
			t.Fatalf("hint %q does not contain %q", hint, value)
		}
	}
}
