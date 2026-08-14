package main

import (
	"context"
	"core/cli/app"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/textutil"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	Result      *string  `json:"result,omitempty"`
	SessionName *string  `json:"session_name,omitempty"`
	DurationMS  *int64   `json:"duration_ms,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}
type observationJSONExecutionError struct {
	observationJSONKind
	Reason     string  `json:"reason"`
	Diagnostic *string `json:"diagnostic,omitempty"`
	SessionID  *string `json:"session_id,omitempty"`
	ScriptPath *string `json:"script_path,omitempty"`
	NodeKey    *string `json:"node_key,omitempty"`
}
type observationJSONInterrupted struct {
	observationJSONKind
	Reason     string  `json:"reason"`
	Diagnostic *string `json:"diagnostic,omitempty"`
	SessionID  *string `json:"session_id,omitempty"`
	ScriptPath *string `json:"script_path,omitempty"`
	NodeKey    *string `json:"node_key,omitempty"`
}
type observationJSONTaskDone struct{ observationJSONKind }
type observationJSONEnvelope struct {
	Status   string                   `json:"status"`
	Target   observationJSONTarget    `json:"target,omitempty"`
	Outcomes []observationJSONOutcome `json:"outcomes,omitempty"`
	Error    *observationJSONError    `json:"error,omitempty"`
	Warnings []string                 `json:"warnings,omitempty"`
}

type observationOperation uint8

const (
	observationOperationRunWait observationOperation = iota
	observationOperationRunWatch
	observationOperationTaskWait
	observationOperationTaskWatch
)

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
func projectObservationQuestion(question serverapi.ObservationQuestion, answerSessionID string, nodeKey *string) (observationJSONOutcome, error) {
	var id, text string
	suggestions := make([]string, 0)
	var recommendation *int
	switch {
	case question.Ask != nil:
		id, text, suggestions, recommendation = string(question.Ask.PromptID), question.Ask.Question, append(suggestions, question.Ask.Suggestions...), question.Ask.RecommendedOptionIndex
	case question.Approval != nil:
		id, text = string(question.Approval.PromptID), question.Approval.Question
		suggestions = make([]string, 0, len(question.Approval.Options))
		for _, option := range question.Approval.Options {
			suggestions = append(suggestions, clientui.ApprovalDecisionLabel(option.Decision))
		}
	default:
		return nil, errors.New("question outcome has no question payload")
	}
	return observationJSONQuestion{
		observationJSONKind: observationJSONKind{Kind: "question"}, QuestionID: id, Text: text, Suggestions: suggestions,
		RecommendedOptionIndex: recommendation,
		AnswerTarget:           observationJSONAnswerTarget{SessionID: answerSessionID}, NodeKey: nodeKey,
	}, nil
}
func projectRunWatchJSON(targetSessionID string, response serverapi.RuntimeLiveWatchResponse) (observationJSONEnvelope, int, error) {
	target := observationTargetSession(targetSessionID)
	switch response.Outcome.Kind {
	case serverapi.RuntimeLiveWatchQuestion:
		if response.Outcome.Question == nil {
			return observationJSONEnvelope{}, 1, errors.New("question outcome has no question payload")
		}
		outcome, err := projectObservationQuestion(*response.Outcome.Question, targetSessionID, nil)
		if err != nil {
			return observationJSONEnvelope{}, 1, err
		}
		return observationJSONEnvelope{Status: "success", Target: target, Outcomes: []observationJSONOutcome{outcome}}, 0, nil
	case serverapi.RuntimeLiveWatchFinalAnswer:
		if response.Outcome.FinalAnswer == nil {
			return observationJSONEnvelope{}, 1, errors.New("final answer outcome has no final payload")
		}
		return observationJSONEnvelope{Status: "success", Target: target, Outcomes: []observationJSONOutcome{
			observationJSONFinalAnswer{
				observationJSONKind: observationJSONKind{Kind: "final_answer"},
				Result:              response.Outcome.FinalAnswer.Result,
				SessionName:         textutil.Value(response.Outcome.FinalAnswer.SessionName),
				DurationMS:          textutil.Value(response.Outcome.FinalAnswer.DurationMillis),
			},
		}}, 0, nil
	case serverapi.RuntimeLiveWatchNoFinalResult:
		return observationJSONEnvelope{Status: "success", Target: target, Outcomes: []observationJSONOutcome{
			observationJSONFinalAnswer{observationJSONKind: observationJSONKind{Kind: "final_answer"}},
		}}, 0, nil
	case serverapi.RuntimeLiveWatchExecutionError, serverapi.RuntimeLiveWatchInterrupted:
		if response.Outcome.Failure == nil {
			return observationJSONEnvelope{}, 1, errors.New("failure outcome has no failure payload")
		}
		interrupted := response.Outcome.Kind == serverapi.RuntimeLiveWatchInterrupted
		outcome := projectFailure(interrupted, response.Outcome.Failure.Reason, response.Outcome.Failure.Diagnostic, nil, nil, nil)
		if interrupted {
			return observationJSONEnvelope{Status: "interrupted", Target: target, Outcomes: []observationJSONOutcome{outcome}}, 130, nil
		}
		return observationJSONEnvelope{Status: "error", Target: target, Outcomes: []observationJSONOutcome{outcome}}, 1, nil
	default:
		return observationJSONEnvelope{}, 1, fmt.Errorf("unknown live watch outcome %q", response.Outcome.Kind)
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
		return projectObservationError(observationOperationRunWait, observationTargetSession(targetSessionID), caller, err)
	}
	return observationJSONEnvelope{
		Status: "success", Target: observationTargetSession(targetSessionID),
		Outcomes: []observationJSONOutcome{observationJSONFinalAnswer{
			observationJSONKind: observationJSONKind{Kind: "final_answer"},
			Result:              textutil.Value(result.Result), SessionName: textutil.Value(result.SessionName),
			DurationMS: textutil.Value(result.Duration.Milliseconds()), Warnings: append([]string(nil), result.Warnings...),
		}},
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
			outcomes = append(outcomes, observationJSONTaskDone{observationJSONKind: observationJSONKind{Kind: "task_done"}})
		case serverapi.WorkflowTaskObservationQuestion:
			if observed.Question == nil {
				return observationJSONEnvelope{}, 1, errors.New("question outcome has no question payload")
			}
			var outcome observationJSONOutcome
			var err error
			switch {
			case observed.Question.Ask != nil:
				outcome, err = projectObservationQuestion(*observed.Question, observed.Question.Ask.SessionID.String(), observed.NodeKey)
			case observed.Question.Approval != nil:
				outcome, err = projectObservationQuestion(*observed.Question, observed.Question.Approval.SessionID.String(), observed.NodeKey)
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
	return observationJSONEnvelope{Status: status, Target: observationTargetTask(targetTaskID), Outcomes: outcomes}, exitCode, nil
}
func projectFailure(interrupted bool, reason string, diagnostic, sessionID, scriptPath, nodeKey *string) observationJSONOutcome {
	if interrupted {
		return observationJSONInterrupted{observationJSONKind: observationJSONKind{Kind: "interrupted"}, Reason: reason, Diagnostic: diagnostic, SessionID: sessionID, ScriptPath: scriptPath, NodeKey: nodeKey}
	}
	return observationJSONExecutionError{observationJSONKind: observationJSONKind{Kind: "execution_error"}, Reason: reason, Diagnostic: diagnostic, SessionID: sessionID, ScriptPath: scriptPath, NodeKey: nodeKey}
}
func projectObservationError(operation observationOperation, target observationJSONTarget, caller context.Context, err error) (observationJSONEnvelope, int) {
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
func emitObservationError(w io.Writer, operation observationOperation, target observationJSONTarget, caller context.Context, err error, warnings []string, closeFn func() error) int {
	envelope, code := projectObservationError(operation, target, caller, err)
	return emitObservationJSONWithCleanup(w, envelope, code, warnings, closeFn)
}
