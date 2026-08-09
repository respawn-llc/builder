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
)

type observationJSONTarget struct {
	SessionID *string `json:"session_id,omitempty"`
	TaskID    *string `json:"task_id,omitempty"`
}

type observationJSONOutcome struct {
	Kind                   string                 `json:"kind"`
	QuestionID             *string                `json:"question_id,omitempty"`
	Text                   *string                `json:"text,omitempty"`
	Suggestions            *[]string              `json:"suggestions,omitempty"`
	RecommendedOptionIndex *int                   `json:"recommended_option_index,omitempty"`
	AnswerTarget           *observationJSONTarget `json:"answer_target,omitempty"`
	NodeKey                *string                `json:"node_key,omitempty"`
	Result                 *string                `json:"result,omitempty"`
	SessionName            *string                `json:"session_name,omitempty"`
	DurationMS             *int64                 `json:"duration_ms,omitempty"`
	Warnings               []string               `json:"warnings,omitempty"`
	Reason                 *string                `json:"reason,omitempty"`
	Diagnostic             *string                `json:"diagnostic,omitempty"`
	SessionID              *string                `json:"session_id,omitempty"`
	ScriptPath             *string                `json:"script_path,omitempty"`
}
type observationJSONEnvelope struct {
	Status   string                   `json:"status"`
	Target   *observationJSONTarget   `json:"target,omitempty"`
	Outcomes []observationJSONOutcome `json:"outcomes,omitempty"`
	Error    *runJSONError            `json:"error,omitempty"`
	Warnings []string                 `json:"warnings,omitempty"`
}

type observationOperation uint8

const observationOperationRunWait observationOperation = iota
const observationOperationObservation observationOperation = observationOperationRunWait + 1

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
func emitObservationJSONWithCleanup(w io.Writer, envelope observationJSONEnvelope, exitCode int, warnings []string, closeFn func() error) int {
	envelope.Warnings = append(envelope.Warnings, warnings...)
	if closeFn != nil {
		if err := closeFn(); err != nil {
			envelope.Warnings = append(envelope.Warnings, err.Error())
		}
	}
	return emitObservationJSON(w, envelope, exitCode)
}
func writeObservationUsage(w io.Writer, message string) int {
	return emitObservationJSON(w, observationJSONEnvelope{Status: "error", Error: &runJSONError{Code: "usage", Message: message}}, 2)
}
func finalOutcome(result, sessionName *string, duration *int64, warnings []string) observationJSONOutcome {
	return observationJSONOutcome{Kind: "final_answer", Result: result, SessionName: sessionName, DurationMS: duration, Warnings: warnings}
}
func projectedObservation(status string, target *observationJSONTarget, outcome observationJSONOutcome, code int) (observationJSONEnvelope, int, error) {
	return observationJSONEnvelope{Status: status, Target: target, Outcomes: []observationJSONOutcome{outcome}}, code, nil
}
func projectObservationQuestion(question serverapi.ObservationQuestion, answerSessionID string, nodeKey *string) (observationJSONOutcome, error) {
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
		return observationJSONOutcome{}, errors.New("question outcome has no question payload")
	}
	return observationJSONOutcome{
		Kind: "question", QuestionID: &id, Text: &text, Suggestions: &suggestions,
		RecommendedOptionIndex: recommendation,
		AnswerTarget:           &observationJSONTarget{SessionID: jsonStringPointer(answerSessionID)}, NodeKey: nodeKey,
	}, nil
}
func projectRunWatchJSON(targetSessionID string, response serverapi.RuntimeLiveWatchResponse) (observationJSONEnvelope, int, error) {
	target := &observationJSONTarget{SessionID: jsonStringPointer(targetSessionID)}
	switch response.Outcome.Kind {
	case serverapi.RuntimeLiveWatchQuestion:
		if response.Outcome.Question == nil {
			return observationJSONEnvelope{}, 1, errors.New("question outcome has no question payload")
		}
		outcome, err := projectObservationQuestion(*response.Outcome.Question, targetSessionID, nil)
		if err != nil {
			return observationJSONEnvelope{}, 1, err
		}
		return projectedObservation("success", target, outcome, 0)
	case serverapi.RuntimeLiveWatchFinalAnswer:
		if response.Outcome.FinalAnswer == nil {
			return observationJSONEnvelope{}, 1, errors.New("final answer outcome has no final payload")
		}
		return projectedObservation("success", target, finalOutcome(response.Outcome.FinalAnswer.Result, jsonStringPointer(response.Outcome.FinalAnswer.SessionName), jsonInt64Pointer(response.Outcome.FinalAnswer.DurationMillis), nil), 0)
	case serverapi.RuntimeLiveWatchNoFinalResult:
		return projectedObservation("success", target, finalOutcome(nil, nil, nil, nil), 0)
	case serverapi.RuntimeLiveWatchExecutionError, serverapi.RuntimeLiveWatchInterrupted:
		if response.Outcome.Failure == nil {
			return observationJSONEnvelope{}, 1, errors.New("failure outcome has no failure payload")
		}
		interrupted := response.Outcome.Kind == serverapi.RuntimeLiveWatchInterrupted
		outcome := projectFailure(interrupted, response.Outcome.Failure.Reason, response.Outcome.Failure.Diagnostic, nil, nil, nil)
		if interrupted {
			return projectedObservation("interrupted", target, outcome, 130)
		}
		return projectedObservation("error", target, outcome, 1)
	default:
		return observationJSONEnvelope{}, 1, fmt.Errorf("unknown live watch outcome %q", response.Outcome.Kind)
	}
}
func projectRunWaitJSON(targetSessionID string, result app.RunPromptResult, err error, caller context.Context) (observationJSONEnvelope, int) {
	if err != nil {
		if errors.Is(err, serverapi.ErrRuntimeNoFinalAnswer) {
			return observationJSONEnvelope{
				Status: "success", Target: &observationJSONTarget{SessionID: jsonStringPointer(targetSessionID)},
				Outcomes: []observationJSONOutcome{finalOutcome(nil, nil, nil, append([]string(nil), result.Warnings...))},
			}, 0
		}
		return projectObservationError(observationOperationRunWait, &observationJSONTarget{SessionID: jsonStringPointer(targetSessionID)}, caller, err)
	}
	return observationJSONEnvelope{
		Status: "success", Target: &observationJSONTarget{SessionID: jsonStringPointer(targetSessionID)},
		Outcomes: []observationJSONOutcome{finalOutcome(
			jsonStringPointer(result.Result), jsonStringPointer(result.SessionName),
			jsonInt64Pointer(result.Duration.Milliseconds()), append([]string(nil), result.Warnings...),
		)},
	}, 0
}
func emitRunWaitJSON(w io.Writer, target string, result app.RunPromptResult, err error, caller context.Context, closeFn func() error) int {
	envelope, code := projectRunWaitJSON(target, result, err, caller)
	return emitObservationJSONWithCleanup(w, envelope, code, nil, closeFn)
}
func projectTaskObservationJSON(targetTaskID string, response serverapi.WorkflowTaskObservationResponse) (observationJSONEnvelope, int, error) {
	outcomes := make([]observationJSONOutcome, 0, len(response.Outcomes))
	status, exitCode := "success", 0
	for _, observed := range response.Outcomes {
		switch observed.Kind {
		case serverapi.WorkflowTaskObservationDone:
			outcomes = append(outcomes, observationJSONOutcome{Kind: "task_done"})
		case serverapi.WorkflowTaskObservationQuestion:
			if observed.Question == nil {
				return observationJSONEnvelope{}, 1, errors.New("question outcome has no question payload")
			}
			var outcome observationJSONOutcome
			var err error
			switch {
			case observed.Question.Ask != nil:
				outcome, err = projectObservationQuestion(*observed.Question, observed.Question.Ask.SessionID, observed.NodeKey)
			case observed.Question.Approval != nil:
				outcome, err = projectObservationQuestion(*observed.Question, observed.Question.Approval.SessionID, observed.NodeKey)
			default:
				err = errors.New("question outcome has no question payload")
			}
			if err != nil {
				return observationJSONEnvelope{}, 1, err
			}
			outcomes = append(outcomes, outcome)
		case serverapi.WorkflowTaskObservationExecutionError, serverapi.WorkflowTaskObservationInterrupted:
			if observed.Failure == nil {
				return observationJSONEnvelope{}, 1, errors.New("failure outcome has no failure payload")
			}
			interrupted := observed.Kind == serverapi.WorkflowTaskObservationInterrupted
			outcomes = append(outcomes, projectFailure(interrupted, observed.Failure.Reason, observed.Failure.Diagnostic, observed.SessionID, observed.ScriptPath, observed.NodeKey))
			if !interrupted {
				status, exitCode = "error", 1
			} else if exitCode == 0 {
				status, exitCode = "interrupted", 130
			}
		default:
			return observationJSONEnvelope{}, 1, fmt.Errorf("unknown task observation outcome %q", observed.Kind)
		}
	}
	return observationJSONEnvelope{Status: status, Target: &observationJSONTarget{TaskID: jsonStringPointer(targetTaskID)}, Outcomes: outcomes}, exitCode, nil
}
func projectFailure(interrupted bool, reason string, diagnostic, sessionID, scriptPath, nodeKey *string) observationJSONOutcome {
	if interrupted {
		return observationJSONOutcome{Kind: "interrupted", Reason: &reason, Diagnostic: diagnostic, SessionID: sessionID, ScriptPath: scriptPath, NodeKey: nodeKey}
	}
	return observationJSONOutcome{Kind: "execution_error", Reason: &reason, Diagnostic: diagnostic, SessionID: sessionID, ScriptPath: scriptPath, NodeKey: nodeKey}
}
func projectObservationError(operation observationOperation, target *observationJSONTarget, caller context.Context, err error) (observationJSONEnvelope, int) {
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
			Outcomes: []observationJSONOutcome{{Kind: "interrupted", Reason: jsonStringPointer(runErrorMessage(err))}},
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
		Error: &runJSONError{Code: code, Message: runErrorMessage(err)},
	}, exitCode
}
func emitObservationError(w io.Writer, operation observationOperation, target *observationJSONTarget, caller context.Context, err error, warnings []string, closeFn func() error) int {
	envelope, code := projectObservationError(operation, target, caller, err)
	return emitObservationJSONWithCleanup(w, envelope, code, warnings, closeFn)
}
func jsonStringPointer(value string) *string { return &value }
func jsonInt64Pointer(value int64) *int64    { return &value }
