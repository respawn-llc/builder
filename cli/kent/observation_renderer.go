package main

import (
	"fmt"
	"io"
	"strings"

	"core/shared/serverapi"
)

func writeObservedOutcome(
	stdout io.Writer,
	outcome serverapi.RuntimeObservationOutcome,
	answerHint string,
) int {
	return writeObservedOutcomeWithDiscriminator(stdout, outcome, answerHint, true)
}

func writeObservedOutcomeBody(
	stdout io.Writer,
	outcome serverapi.RuntimeObservationOutcome,
	answerHint string,
) int {
	return writeObservedOutcomeWithDiscriminator(stdout, outcome, answerHint, false)
}

func writeObservedOutcomeWithDiscriminator(
	stdout io.Writer,
	outcome serverapi.RuntimeObservationOutcome,
	answerHint string,
	includeDiscriminator bool,
) int {
	if includeDiscriminator {
		writeObservationDiscriminator(stdout, outcome)
	}
	switch outcome.Kind {
	case serverapi.RuntimeObservationOutcomeQuestion:
		if outcome.Question == nil {
			return 0
		}
		writeObservedQuestion(stdout, *outcome.Question, answerHint)
		return 0
	case serverapi.RuntimeObservationOutcomeFinalAnswer:
		if outcome.FinalAnswer != nil && outcome.FinalAnswer.Result != nil {
			fmt.Fprintln(stdout, *outcome.FinalAnswer.Result)
		}
		return 0
	case serverapi.RuntimeObservationOutcomeInterrupted:
		if outcome.Interrupted != nil {
			fmt.Fprintln(stdout, outcome.Interrupted.Reason)
			if outcome.Interrupted.Diagnostic != nil {
				fmt.Fprintln(stdout, *outcome.Interrupted.Diagnostic)
			}
		}
		return 130
	case serverapi.RuntimeObservationOutcomeExecutionError:
		if outcome.ExecutionError != nil {
			fmt.Fprintln(stdout, outcome.ExecutionError.Reason)
			if outcome.ExecutionError.Diagnostic != nil {
				fmt.Fprintln(stdout, *outcome.ExecutionError.Diagnostic)
			}
		}
		return 1
	case serverapi.RuntimeObservationOutcomeTaskDone:
		return 0
	default:
		return 0
	}
}

func writeObservationDiscriminator(stdout io.Writer, outcome serverapi.RuntimeObservationOutcome) {
	switch {
	case outcome.ScriptPath != nil:
		fmt.Fprintf(stdout, "Script %s", *outcome.ScriptPath)
	case outcome.SessionID != nil:
		fmt.Fprintf(stdout, "Session %s", *outcome.SessionID)
	default:
		return
	}
	if outcome.NodeKey != nil {
		fmt.Fprintf(stdout, " (Node %s)", *outcome.NodeKey)
	}
	fmt.Fprintln(stdout, ":")
}

func observationQuestionHint(response serverapi.WorkflowTaskObservationResponse, outcome serverapi.RuntimeObservationOutcome, projectRef string) string {
	questionCount := 0
	for _, candidate := range response.Outcomes {
		if candidate.Kind == serverapi.RuntimeObservationOutcomeQuestion {
			questionCount++
		}
	}
	if questionCount == 1 {
		taskShortID, _ := response.Target.TaskShortIDValue()
		selector := "--task " + taskShortID
		if strings.TrimSpace(projectRef) != "" {
			projectID, _ := response.Target.ProjectIDValue()
			if project := strings.TrimSpace(projectID); project != "" {
				selector += " --project " + project
			}
		}
		return observedQuestionAnswerHint(*outcome.Question, selector)
	}
	if outcome.SessionID != nil {
		return observedQuestionAnswerHint(*outcome.Question, "--session "+*outcome.SessionID)
	}
	taskShortID, _ := response.Target.TaskShortIDValue()
	return observedQuestionAnswerHint(*outcome.Question, "--task "+taskShortID)
}

func reducedObservationExitCode(current, next int) int {
	if next == 1 {
		return 1
	}
	if next == 130 && current == 0 {
		return 130
	}
	return current
}
