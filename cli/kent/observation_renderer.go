package main

import (
	"fmt"
	"io"
	"strings"

	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

func writeObservedQuestion(w io.Writer, question serverapi.ObservationQuestion, hint string) {
	if question.Ask != nil {
		fmt.Fprintln(w, question.Ask.Question)
		if len(question.Ask.Suggestions) > 0 {
			fmt.Fprintln(w, questionSuggestionsHeading)
			for i, option := range question.Ask.Suggestions {
				suffix := ""
				if question.Ask.RecommendedOptionIndex != nil && *question.Ask.RecommendedOptionIndex == i+1 {
					suffix = recommendedSuggestionSuffix
				}
				fmt.Fprintf(w, "%d. %s%s\n", i+1, option, suffix)
			}
		}
	} else if question.Approval != nil {
		if len(question.Approval.AccessTargets) > 0 {
			fmt.Fprintln(w, clientui.FormatFileAccessApprovalMarkdown(question.Approval.AccessTargets))
		} else {
			fmt.Fprintln(w, question.Approval.Question)
		}
		fmt.Fprintln(w, questionSuggestionsHeading)
		for i, option := range question.Approval.Options {
			fmt.Fprintf(w, "%d. %s\n", i+1, option.Label)
		}
	}
	if strings.TrimSpace(hint) != "" {
		fmt.Fprintf(w, "\nAnswer with: %s\n", hint)
	}
}

func observationQuestionHint(sessionID string, question serverapi.ObservationQuestion) string {
	args := []string{config.Command, "question", "answer", "--session", sessionID}
	return commandString(append(args, observationQuestionAnswerArgs(question)...))
}

func observationQuestionAnswerArgs(question serverapi.ObservationQuestion) []string {
	if question.Approval != nil || question.Ask != nil && len(question.Ask.Suggestions) > 0 {
		return []string{"--option", "<number>"}
	}
	return []string{"--commentary", "<answer>"}
}

func writeRunWatchResponse(w io.Writer, stderr io.Writer, response serverapi.RuntimeLiveWatchResponse, continueHint string) int {
	if stderr == nil {
		stderr = io.Discard
	}
	switch response.Outcome.Kind {
	case serverapi.RuntimeLiveWatchQuestion:
		if response.Outcome.Question == nil {
			fmt.Fprintf(stderr, "invalid live watch response: %s outcome has no question payload\n", response.Outcome.Kind)
			return 1
		}
		writeObservedQuestion(w, *response.Outcome.Question, observationQuestionHint(response.SessionID, *response.Outcome.Question))
	case serverapi.RuntimeLiveWatchFinalAnswer:
		if response.Outcome.FinalAnswer != nil {
			result := ""
			if response.Outcome.FinalAnswer.Result != nil {
				result = *response.Outcome.FinalAnswer.Result
			}
			emitRunFinalText(w, nil, result, continueHint)
		}
	case serverapi.RuntimeLiveWatchNoFinalResult, serverapi.RuntimeLiveWatchExecutionError, serverapi.RuntimeLiveWatchInterrupted:
		if response.Outcome.Failure == nil {
			fmt.Fprintf(stderr, "invalid live watch response: %s outcome has no failure payload\n", response.Outcome.Kind)
			return 1
		}
		fmt.Fprintln(w, response.Outcome.Failure.Reason)
		if response.Outcome.Failure.Diagnostic != nil {
			fmt.Fprintln(w, *response.Outcome.Failure.Diagnostic)
		}
		if response.Outcome.Kind == serverapi.RuntimeLiveWatchInterrupted {
			return 130
		}
		return 1
	}
	return 0
}
