package main

import (
	"fmt"
	"io"
	"strings"

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
		fmt.Fprintln(w, question.Approval.Question)
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
	if question.Approval != nil {
		return "kent question answer --session " + sessionID + " --option <number>"
	}
	if question.Ask != nil && len(question.Ask.Suggestions) > 0 {
		return "kent question answer --session " + sessionID + " --option <number>"
	}
	return "kent question answer --session " + sessionID + " --commentary \"<answer>\""
}

func writeRunWatchResponse(w io.Writer, response serverapi.RuntimeLiveWatchResponse, continueHint string) int {
	switch response.Outcome.Kind {
	case serverapi.RuntimeLiveWatchQuestion:
		if response.Outcome.Question == nil {
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
