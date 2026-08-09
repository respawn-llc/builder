package main

import (
	"context"
	"core/cli/app"
	"core/shared/client"
	"core/shared/serverapi"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type observationJSONTarget interface{ observationTarget() }
type observationJSONRunTarget struct {
	SessionID string `json:"session_id"`
}
type observationJSONTaskTarget struct {
	TaskID string `json:"task_id"`
}
type observationJSONAnswerTarget = observationJSONRunTarget
type observationJSONError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type observationJSONOutcome interface{ observationOutcome() }
type observationJSONKind struct {
	Kind string `json:"kind"`
}

func (observationJSONKind) observationOutcome() {}

type observationJSONQuestion struct {
	observationJSONKind
	QuestionID             string                      `json:"question_id"`
	Text                   string                      `json:"text"`
	Suggestions            []string                    `json:"suggestions"`
	RecommendedOptionIndex *int                        `json:"recommended_option_index,omitempty"`
	AnswerTarget           observationJSONAnswerTarget `json:"answer_target"`
	NodeKey                *string                     `json:"node_key,omitempty"`
}
type observationJSONFinalAnswer struct {
	observationJSONKind
	Result   *string  `json:"result,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}
type observationJSONExecutionError struct {
	observationJSONKind
	Reason     string  `json:"reason"`
	Diagnostic *string `json:"diagnostic,omitempty"`
}
type observationJSONInterrupted struct {
	observationJSONKind
	Reason     string  `json:"reason"`
	Diagnostic *string `json:"diagnostic,omitempty"`
}
type observationJSONTaskDone struct{ observationJSONKind }
type observationJSONEnvelope struct {
	Status   string                   `json:"status"`
	Target   observationJSONTarget    `json:"target,omitempty"`
	Outcomes []observationJSONOutcome `json:"outcomes,omitempty"`
	Error    *observationJSONError    `json:"error,omitempty"`
	Warnings []string                 `json:"warnings,omitempty"`
}

const observationOperationRunWait = "run_wait"
const observationOperationRunWatch = "run_watch"
const observationOperationTaskWait = "task_wait"
const observationOperationTaskWatch = "task_watch"

func (observationJSONRunTarget) observationTarget()  {}
func (observationJSONTaskTarget) observationTarget() {}
func writeObservationJSON(w io.Writer, envelope observationJSONEnvelope) error {
	if w == nil {
		return errors.New("JSON output writer is required")
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(envelope)
}
func emitObservationJSON(w io.Writer, envelope observationJSONEnvelope, exitCode int) int {
	if err := writeObservationJSON(w, envelope); err != nil {
		return 1
	}
	return exitCode
}
func writeObservationUsage(w io.Writer, message string) int {
	return emitObservationJSON(w, observationJSONEnvelope{
		Status: "error", Error: &observationJSONError{Code: "usage", Message: message},
	}, 2)
}
func observationTargetSession(id string) observationJSONTarget {
	return observationJSONRunTarget{SessionID: id}
}
func observationTargetTask(id string) observationJSONTarget {
	return observationJSONTaskTarget{TaskID: id}
}
func projectObservationQuestion(question serverapi.ObservationQuestion, answerSessionID string, nodeKey *string) observationJSONOutcome {
	var id, text string
	var suggestions []string
	var recommendation *int
	switch {
	case question.Ask != nil:
		id, text, suggestions, recommendation = question.Ask.AskID, question.Ask.Question, append([]string(nil), question.Ask.Suggestions...), question.Ask.RecommendedOptionIndex
	case question.Approval != nil:
		id, text = question.Approval.ApprovalID, question.Approval.Question
		suggestions = make([]string, 0, len(question.Approval.Options))
		for _, option := range question.Approval.Options {
			suggestions = append(suggestions, option.Label)
		}
	default:
		panic("question outcome has no question payload")
	}
	return observationJSONQuestion{
		observationJSONKind: observationJSONKind{Kind: "question"}, QuestionID: id, Text: text, Suggestions: suggestions,
		RecommendedOptionIndex: recommendation,
		AnswerTarget:           observationJSONAnswerTarget{SessionID: answerSessionID}, NodeKey: nodeKey,
	}
}
func projectRunWatchJSON(targetSessionID string, response serverapi.RuntimeLiveWatchResponse) (observationJSONEnvelope, int) {
	target := observationTargetSession(targetSessionID)
	switch response.Outcome.Kind {
	case serverapi.RuntimeLiveWatchQuestion:
		outcome := projectObservationQuestion(*response.Outcome.Question, targetSessionID, nil)
		return observationJSONEnvelope{Status: "success", Target: target, Outcomes: []observationJSONOutcome{outcome}}, 0
	case serverapi.RuntimeLiveWatchFinalAnswer:
		return observationJSONEnvelope{Status: "success", Target: target, Outcomes: []observationJSONOutcome{
			observationJSONFinalAnswer{observationJSONKind: observationJSONKind{Kind: "final_answer"}, Result: response.Outcome.FinalAnswer.Result},
		}}, 0
	case serverapi.RuntimeLiveWatchNoFinalResult:
		return observationJSONEnvelope{Status: "success", Target: target, Outcomes: []observationJSONOutcome{
			observationJSONFinalAnswer{observationJSONKind: observationJSONKind{Kind: "final_answer"}},
		}}, 0
	case serverapi.RuntimeLiveWatchExecutionError, serverapi.RuntimeLiveWatchInterrupted:
		interrupted := response.Outcome.Kind == serverapi.RuntimeLiveWatchInterrupted
		outcome := projectFailure(interrupted, response.Outcome.Failure.Reason, response.Outcome.Failure.Diagnostic)
		if interrupted {
			return observationJSONEnvelope{Status: "interrupted", Target: target, Outcomes: []observationJSONOutcome{outcome}}, 130
		}
		return observationJSONEnvelope{Status: "error", Target: target, Outcomes: []observationJSONOutcome{outcome}}, 1
	default:
		panic(fmt.Sprintf("unknown live watch outcome %q", response.Outcome.Kind))
	}
}
func projectRunWaitJSON(targetSessionID string, result app.RunPromptResult, err error, caller context.Context) (observationJSONEnvelope, int) {
	if err != nil {
		if errors.Is(err, serverapi.ErrRuntimeNoFinalAnswer) {
			return observationJSONEnvelope{
				Status: "success", Target: observationTargetSession(targetSessionID),
				Outcomes: []observationJSONOutcome{observationJSONFinalAnswer{
					observationJSONKind: observationJSONKind{Kind: "final_answer"}, Warnings: append([]string(nil), result.Warnings...),
				}},
			}, 0
		}
		return projectObservationError(observationOperationRunWait, targetSessionID, caller, err)
	}
	return observationJSONEnvelope{
		Status: "success", Target: observationTargetSession(targetSessionID),
		Outcomes: []observationJSONOutcome{observationJSONFinalAnswer{observationJSONKind: observationJSONKind{Kind: "final_answer"}, Result: jsonStringPointer(result.Result), Warnings: append([]string(nil), result.Warnings...)}},
	}, 0
}
func emitRunWaitJSON(w io.Writer, target string, result app.RunPromptResult, err error, caller context.Context) int {
	envelope, code := projectRunWaitJSON(target, result, err, caller)
	envelope.Warnings = append(envelope.Warnings, result.CleanupWarnings...)
	return emitObservationJSON(w, envelope, code)
}
func projectTaskObservationJSON(targetTaskID string, response serverapi.WorkflowTaskObservationResponse) (observationJSONEnvelope, int) {
	outcomes := make([]observationJSONOutcome, 0, len(response.Outcomes))
	status, exitCode := "success", 0
	for _, observed := range response.Outcomes {
		switch observed.Kind {
		case serverapi.WorkflowTaskObservationDone:
			outcomes = append(outcomes, observationJSONTaskDone{observationJSONKind: observationJSONKind{Kind: "task_done"}})
		case serverapi.WorkflowTaskObservationQuestion:
			sessionID := ""
			if observed.Question.Ask != nil {
				sessionID = observed.Question.Ask.SessionID
			} else if observed.Question.Approval != nil {
				sessionID = observed.Question.Approval.SessionID
			}
			outcome := projectObservationQuestion(*observed.Question, sessionID, observed.NodeKey)
			outcomes = append(outcomes, outcome)
		case serverapi.WorkflowTaskObservationExecutionError, serverapi.WorkflowTaskObservationInterrupted:
			interrupted := observed.Kind == serverapi.WorkflowTaskObservationInterrupted
			outcomes = append(outcomes, projectFailure(interrupted, observed.Failure.Reason, observed.Failure.Diagnostic))
			if !interrupted {
				status, exitCode = "error", 1
			} else if exitCode == 0 {
				status, exitCode = "interrupted", 130
			}
		default:
			panic(fmt.Sprintf("unknown task observation outcome %q", observed.Kind))
		}
	}
	return observationJSONEnvelope{Status: status, Target: observationTargetTask(targetTaskID), Outcomes: outcomes}, exitCode
}
func projectFailure(interrupted bool, reason string, diagnostic *string) observationJSONOutcome {
	if interrupted {
		return observationJSONInterrupted{observationJSONKind: observationJSONKind{Kind: "interrupted"}, Reason: reason, Diagnostic: diagnostic}
	}
	return observationJSONExecutionError{observationJSONKind: observationJSONKind{Kind: "execution_error"}, Reason: reason, Diagnostic: diagnostic}
}
func projectObservationError(operation string, targetID string, caller context.Context, err error) (observationJSONEnvelope, int) {
	var target observationJSONTarget
	if strings.TrimSpace(targetID) != "" {
		if operation == observationOperationTaskWait || operation == observationOperationTaskWatch {
			target = observationTargetTask(targetID)
		} else {
			target = observationTargetSession(targetID)
		}
	}
	code := "runtime"
	var invalidResponse *client.InvalidResponseError
	switch {
	case errors.As(err, &invalidResponse):
		code = "invalid_response"
	case errors.Is(err, serverapi.ErrStreamUnavailable), errors.Is(err, serverapi.ErrStreamFailed),
		errors.Is(err, serverapi.ErrStreamGap), errors.Is(err, serverapi.ErrRuntimeUnavailable),
		errors.Is(err, serverapi.ErrProjectUnavailable):
		code = "unavailable"
	case caller != nil && caller.Err() != nil && errors.Is(caller.Err(), context.Canceled):
		code = "interrupted"
	case operation == observationOperationRunWait && caller != nil && caller.Err() == nil && errors.Is(err, context.Canceled):
		return observationJSONEnvelope{
			Status: "interrupted", Target: target,
			Outcomes: []observationJSONOutcome{observationJSONInterrupted{
				observationJSONKind: observationJSONKind{Kind: "interrupted"}, Reason: runErrorMessage(err),
			}},
		}, 130
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	case errors.Is(err, serverapi.ErrWorkflowTaskNotFound), errors.Is(err, serverapi.ErrProjectNotFound), errors.Is(err, sql.ErrNoRows):
		code = "target_not_found"
	case errors.Is(err, serverapi.ErrRuntimeNoActiveRun):
		code = "no_active_execution"
	}
	exitCode := 1
	if code == "interrupted" {
		exitCode = 130
	}
	return observationJSONEnvelope{
		Status: "error", Target: target,
		Error: &observationJSONError{Code: code, Message: runErrorMessage(err)},
	}, exitCode
}
func emitObservationError(w io.Writer, operation string, target string, caller context.Context, err error, warnings []string) int {
	envelope, code := projectObservationError(operation, target, caller, err)
	envelope.Warnings = append(envelope.Warnings, warnings...)
	return emitObservationJSON(w, envelope, code)
}
func jsonStringPointer(value string) *string { return &value }
