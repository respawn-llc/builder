package serverapi

import (
	"errors"
	"sort"
	"strings"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

type RuntimeLiveWatchOutcomeKind string

const (
	RuntimeLiveWatchQuestion       RuntimeLiveWatchOutcomeKind = "question"
	RuntimeLiveWatchFinalAnswer    RuntimeLiveWatchOutcomeKind = "final_answer"
	RuntimeLiveWatchExecutionError RuntimeLiveWatchOutcomeKind = "execution_error"
	RuntimeLiveWatchNoFinalResult  RuntimeLiveWatchOutcomeKind = "no_final_result"
	RuntimeLiveWatchInterrupted    RuntimeLiveWatchOutcomeKind = "interrupted"
)

type RuntimeLiveWatchRequest struct {
	SessionID string `json:"session_id"`
}

func (r RuntimeLiveWatchRequest) Validate() error {
	_, err := PrepareRuntimeLiveWaitRequest(RuntimeLiveWaitRequest{SessionID: r.SessionID})
	return err
}

type ObservationQuestion struct {
	Ask      *clientui.PendingAsk      `json:"ask,omitempty"`
	Approval *clientui.PendingApproval `json:"approval,omitempty"`
}

type PendingPromptObservation struct {
	ID        string
	CreatedAt time.Time
	Question  ObservationQuestion
}

func FirstPendingPromptObservation(
	asks []clientui.PendingAsk,
	approvals []clientui.PendingApproval,
) (PendingPromptObservation, bool) {
	prompts := make([]PendingPromptObservation, 0, len(asks)+len(approvals))
	for index := range asks {
		prompts = append(prompts, PendingPromptObservation{
			ID: string(asks[index].PromptID), CreatedAt: asks[index].CreatedAt,
			Question: ObservationQuestion{Ask: &asks[index]},
		})
	}
	for index := range approvals {
		prompts = append(prompts, PendingPromptObservation{
			ID: string(approvals[index].PromptID), CreatedAt: approvals[index].CreatedAt,
			Question: ObservationQuestion{Approval: &approvals[index]},
		})
	}
	sort.SliceStable(prompts, func(i, j int) bool {
		if !prompts[i].CreatedAt.Equal(prompts[j].CreatedAt) {
			return prompts[i].CreatedAt.Before(prompts[j].CreatedAt)
		}
		return prompts[i].ID < prompts[j].ID
	})
	if len(prompts) == 0 {
		return PendingPromptObservation{}, false
	}
	return prompts[0], true
}

func (q ObservationQuestion) Validate() error {
	if (q.Ask == nil) == (q.Approval == nil) {
		return errors.New("observation question must contain one ask or approval")
	}
	if q.Ask != nil {
		return validateObservationAsk(*q.Ask)
	}
	return validateObservationApproval(*q.Approval)
}

func validateObservationAsk(ask clientui.PendingAsk) error {
	if err := validateObservationPromptIdentity(ask.PromptID, ask.SessionID, ask.StepID); err != nil {
		return err
	}
	if strings.TrimSpace(ask.Question) == "" {
		return errors.New("observation ask question is required")
	}
	if ask.RecommendedOptionIndex != nil &&
		(*ask.RecommendedOptionIndex <= 0 || *ask.RecommendedOptionIndex > len(ask.Suggestions)) {
		return errors.New("observation ask recommendation is invalid")
	}
	return nil
}

func validateObservationApproval(approval clientui.PendingApproval) error {
	if err := validateObservationPromptIdentity(approval.PromptID, approval.SessionID, approval.StepID); err != nil {
		return err
	}
	if strings.TrimSpace(approval.Question) == "" {
		return errors.New("observation approval question is required")
	}
	if len(approval.Options) == 0 {
		return errors.New("observation approval options are required")
	}
	for _, option := range approval.Options {
		if strings.TrimSpace(option.Label) == "" {
			return errors.New("observation approval option label is required")
		}
		switch option.Decision {
		case clientui.ApprovalDecisionAllowOnce, clientui.ApprovalDecisionAllowSession, clientui.ApprovalDecisionDeny:
		default:
			return errors.New("observation approval option decision is invalid")
		}
	}
	return nil
}

type RuntimeLiveWatchFailure struct {
	Reason     string  `json:"reason"`
	Diagnostic *string `json:"diagnostic,omitempty"`
}

func (f RuntimeLiveWatchFailure) Validate() error {
	if strings.TrimSpace(f.Reason) == "" {
		return errors.New("failure reason is required")
	}
	return nil
}

type RuntimeLiveWatchFinal struct {
	Result         *string `json:"result,omitempty"`
	SessionName    string  `json:"session_name"`
	DurationMillis int64   `json:"duration_ms"`
}

type RuntimeLiveWatchOutcome struct {
	Kind        RuntimeLiveWatchOutcomeKind `json:"kind"`
	Question    *ObservationQuestion        `json:"question,omitempty"`
	FinalAnswer *RuntimeLiveWatchFinal      `json:"final_answer,omitempty"`
	Failure     *RuntimeLiveWatchFailure    `json:"failure,omitempty"`
}

type RuntimeLiveWatchResponse struct {
	SessionID string                  `json:"session_id"`
	Outcome   RuntimeLiveWatchOutcome `json:"outcome"`
}

func (r RuntimeLiveWatchResponse) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	payloads := 0
	if r.Outcome.Question != nil {
		payloads++
	}
	if r.Outcome.FinalAnswer != nil {
		payloads++
	}
	if r.Outcome.Failure != nil {
		payloads++
	}
	if payloads != 1 {
		return errors.New("live watch outcome must contain one payload")
	}
	switch r.Outcome.Kind {
	case RuntimeLiveWatchQuestion:
		if r.Outcome.Question == nil {
			return errors.New("question outcome requires question")
		}
		if err := r.Outcome.Question.Validate(); err != nil {
			return err
		}
		if r.Outcome.Question.Ask != nil && r.Outcome.Question.Ask.SessionID.String() != r.SessionID {
			return errors.New("question ask session does not match live watch session")
		}
		if r.Outcome.Question.Approval != nil && r.Outcome.Question.Approval.SessionID.String() != r.SessionID {
			return errors.New("question approval session does not match live watch session")
		}
		return nil
	case RuntimeLiveWatchFinalAnswer:
		if r.Outcome.FinalAnswer == nil {
			return errors.New("final answer outcome requires final answer")
		}
	case RuntimeLiveWatchExecutionError, RuntimeLiveWatchNoFinalResult, RuntimeLiveWatchInterrupted:
		if r.Outcome.Failure == nil {
			return errors.New("failure outcome requires failure")
		}
		return r.Outcome.Failure.Validate()
	default:
		return errors.New("live watch outcome kind is invalid")
	}
	return nil
}

func validateObservationPromptIdentity(promptID clientui.PromptID, sessionID runtimeids.SessionID, stepID runtimeids.StepID) error {
	if err := promptID.Validate(); err != nil {
		return err
	}
	if sessionID.IsZero() {
		return errors.New("observation prompt session id is required")
	}
	if stepID.IsZero() {
		return errors.New("observation prompt step id is required")
	}
	return nil
}

type WorkflowTaskObservationMode string

const (
	WorkflowTaskObservationWait  WorkflowTaskObservationMode = "wait"
	WorkflowTaskObservationWatch WorkflowTaskObservationMode = "watch"
)

type WorkflowTaskObservationRequest struct {
	TaskID    string                      `json:"task_id"`
	ProjectID string                      `json:"project_id"`
	Mode      WorkflowTaskObservationMode `json:"mode"`
}

func (r WorkflowTaskObservationRequest) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	if err := validateRequired("project_id", r.ProjectID); err != nil {
		return err
	}
	if r.Mode != WorkflowTaskObservationWait && r.Mode != WorkflowTaskObservationWatch {
		return errors.New("workflow task observation mode is invalid")
	}
	return nil
}

type WorkflowTaskObservationOutcomeKind string

const (
	WorkflowTaskObservationDone           WorkflowTaskObservationOutcomeKind = "done"
	WorkflowTaskObservationQuestion       WorkflowTaskObservationOutcomeKind = "question"
	WorkflowTaskObservationExecutionError WorkflowTaskObservationOutcomeKind = "execution_error"
	WorkflowTaskObservationInterrupted    WorkflowTaskObservationOutcomeKind = "interrupted"
)

type WorkflowTaskObservationOutcome struct {
	Kind       WorkflowTaskObservationOutcomeKind `json:"kind"`
	SessionID  *string                            `json:"session_id,omitempty"`
	ScriptPath *string                            `json:"script_path,omitempty"`
	NodeKey    *string                            `json:"node_key,omitempty"`
	Question   *ObservationQuestion               `json:"question,omitempty"`
	Failure    *RuntimeLiveWatchFailure           `json:"failure,omitempty"`
}

type WorkflowTaskObservationResponse struct {
	TaskID      string                           `json:"task_id"`
	TaskShortID string                           `json:"task_short_id"`
	Outcomes    []WorkflowTaskObservationOutcome `json:"outcomes"`
}

func (r WorkflowTaskObservationResponse) Validate() error {
	if err := validateRequired("task_id", r.TaskID); err != nil {
		return err
	}
	if strings.TrimSpace(r.TaskShortID) == "" {
		return errors.New("task_short_id is required")
	}
	if len(r.Outcomes) == 0 {
		return errors.New("task observation outcomes are required")
	}
	for _, outcome := range r.Outcomes {
		switch outcome.Kind {
		case WorkflowTaskObservationDone:
			if outcome.Question != nil || outcome.Failure != nil {
				return errors.New("done outcome has an invalid payload")
			}
		case WorkflowTaskObservationQuestion:
			if outcome.Question == nil || outcome.Failure != nil {
				return errors.New("question outcome has an invalid payload")
			}
			if err := outcome.Question.Validate(); err != nil {
				return err
			}
		case WorkflowTaskObservationExecutionError, WorkflowTaskObservationInterrupted:
			if outcome.Failure == nil || outcome.Question != nil {
				return errors.New("failure outcome has an invalid payload")
			}
			if err := outcome.Failure.Validate(); err != nil {
				return err
			}
		default:
			return errors.New("task observation outcome kind is invalid")
		}
	}
	return nil
}
