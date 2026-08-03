package serverapi

import (
	"errors"
	"strings"

	"core/shared/clientui"
)

type RuntimeObservationTargetKind string

const (
	RuntimeObservationTargetSession RuntimeObservationTargetKind = "session"
	RuntimeObservationTargetTask    RuntimeObservationTargetKind = "task"
)

type RuntimeObservationTarget struct {
	Kind        RuntimeObservationTargetKind `json:"kind"`
	SessionID   *string                      `json:"session_id"`
	TaskID      *string                      `json:"task_id"`
	TaskShortID *string                      `json:"task_short_id"`
	ProjectID   *string                      `json:"project_id"`
}

func NewRuntimeObservationSessionTarget(sessionID string) RuntimeObservationTarget {
	return RuntimeObservationTarget{
		Kind:      RuntimeObservationTargetSession,
		SessionID: observationStringPointer(sessionID),
	}
}

func NewRuntimeObservationTaskTarget(taskID, taskShortID, projectID string) RuntimeObservationTarget {
	return RuntimeObservationTarget{
		Kind:        RuntimeObservationTargetTask,
		TaskID:      observationStringPointer(taskID),
		TaskShortID: observationStringPointer(taskShortID),
		ProjectID:   observationStringPointer(projectID),
	}
}

func (t RuntimeObservationTarget) SessionIDValue() (string, bool) {
	return observationStringValue(t.SessionID)
}

func (t RuntimeObservationTarget) TaskIDValue() (string, bool) {
	return observationStringValue(t.TaskID)
}

func (t RuntimeObservationTarget) TaskShortIDValue() (string, bool) {
	return observationStringValue(t.TaskShortID)
}

func (t RuntimeObservationTarget) ProjectIDValue() (string, bool) {
	return observationStringValue(t.ProjectID)
}

func (t RuntimeObservationTarget) Validate() error {
	switch t.Kind {
	case RuntimeObservationTargetSession:
		if t.SessionID == nil {
			return errors.New("session_id is required")
		}
		if err := validateRequiredSessionID(*t.SessionID); err != nil {
			return err
		}
		if t.TaskID != nil || t.TaskShortID != nil || t.ProjectID != nil {
			return errors.New("session observation target must not include task identity")
		}
	case RuntimeObservationTargetTask:
		if t.TaskID == nil || t.TaskShortID == nil || t.ProjectID == nil {
			return errors.New("task observation target requires task_id, task_short_id, and project_id")
		}
		if err := validateRequired("task_id", *t.TaskID); err != nil {
			return err
		}
		if err := validateRequired("project_id", *t.ProjectID); err != nil {
			return err
		}
		if strings.TrimSpace(*t.TaskShortID) == "" {
			return errors.New("task_short_id is required")
		}
		if t.SessionID != nil {
			return errors.New("task observation target must not include session identity")
		}
	default:
		return errors.New("observation target kind must be session or task")
	}
	return nil
}

func observationStringPointer(value string) *string {
	return &value
}

func observationStringValue(value *string) (string, bool) {
	if value == nil {
		return "", false
	}
	return *value, true
}

type RuntimeObservationQuestionKind string

const (
	RuntimeObservationQuestionOrdinary      RuntimeObservationQuestionKind = "ordinary"
	RuntimeObservationQuestionAccessRequest RuntimeObservationQuestionKind = "access_request"
)

type RuntimeObservationQuestion struct {
	QuestionID             string                         `json:"question_id"`
	Text                   string                         `json:"text"`
	Kind                   RuntimeObservationQuestionKind `json:"kind"`
	Suggestions            []string                       `json:"suggestions,omitempty"`
	RecommendedOptionIndex *int                           `json:"recommended_option_index,omitempty"`
	AccessOptions          []clientui.ApprovalOption      `json:"access_options,omitempty"`
}

func (q RuntimeObservationQuestion) Validate() error {
	if strings.TrimSpace(q.QuestionID) == "" {
		return errors.New("question_id is required")
	}
	if strings.TrimSpace(q.Text) == "" {
		return errors.New("text is required")
	}
	switch q.Kind {
	case RuntimeObservationQuestionOrdinary:
		if len(q.AccessOptions) > 0 {
			return errors.New("ordinary question must not include access options")
		}
		if q.RecommendedOptionIndex != nil &&
			(*q.RecommendedOptionIndex < 1 || *q.RecommendedOptionIndex > len(q.Suggestions)) {
			return errors.New("recommended_option_index must select an ordinary suggestion")
		}
	case RuntimeObservationQuestionAccessRequest:
		if len(q.AccessOptions) == 0 {
			return errors.New("access request requires access options")
		}
		if len(q.Suggestions) > 0 || q.RecommendedOptionIndex != nil {
			return errors.New("access request must use typed access options")
		}
		for _, option := range q.AccessOptions {
			if strings.TrimSpace(option.Label) == "" {
				return errors.New("access option label is required")
			}
			switch option.Decision {
			case clientui.ApprovalDecisionAllowOnce, clientui.ApprovalDecisionAllowSession, clientui.ApprovalDecisionDeny:
			default:
				return errors.New("access option decision is invalid")
			}
		}
	default:
		return errors.New("question kind must be ordinary or access_request")
	}
	return nil
}

type RuntimeObservationFinalAnswer struct {
	Result         *string  `json:"result,omitempty"`
	SessionName    string   `json:"session_name"`
	Warnings       []string `json:"warnings,omitempty"`
	DurationMillis int64    `json:"duration_ms"`
}

func (a RuntimeObservationFinalAnswer) Validate() error {
	if strings.TrimSpace(a.SessionName) == "" {
		return errors.New("session_name is required")
	}
	if a.DurationMillis < 0 {
		return errors.New("duration_ms must not be negative")
	}
	return nil
}

type RuntimeObservationExecutionError struct {
	Reason     string  `json:"reason"`
	Diagnostic *string `json:"diagnostic,omitempty"`
}

func (e RuntimeObservationExecutionError) Validate() error {
	if strings.TrimSpace(e.Reason) == "" {
		return errors.New("reason is required")
	}
	return nil
}

type RuntimeObservationInterrupted struct {
	Reason     string  `json:"reason"`
	Diagnostic *string `json:"diagnostic,omitempty"`
}

func (i RuntimeObservationInterrupted) Validate() error {
	if strings.TrimSpace(i.Reason) == "" {
		return errors.New("reason is required")
	}
	return nil
}

type RuntimeObservationTaskDone struct{}

type RuntimeObservationOutcomeKind string

const (
	RuntimeObservationOutcomeQuestion       RuntimeObservationOutcomeKind = "question"
	RuntimeObservationOutcomeFinalAnswer    RuntimeObservationOutcomeKind = "final_answer"
	RuntimeObservationOutcomeExecutionError RuntimeObservationOutcomeKind = "execution_error"
	RuntimeObservationOutcomeInterrupted    RuntimeObservationOutcomeKind = "interrupted"
	RuntimeObservationOutcomeTaskDone       RuntimeObservationOutcomeKind = "task_done"
)

type RuntimeObservationOutcome struct {
	Kind       RuntimeObservationOutcomeKind `json:"kind"`
	NodeKey    *string                       `json:"node_key,omitempty"`
	SessionID  *string                       `json:"session_id,omitempty"`
	ScriptPath *string                       `json:"script_path,omitempty"`

	Question       *RuntimeObservationQuestion       `json:"question,omitempty"`
	FinalAnswer    *RuntimeObservationFinalAnswer    `json:"final_answer,omitempty"`
	ExecutionError *RuntimeObservationExecutionError `json:"execution_error,omitempty"`
	Interrupted    *RuntimeObservationInterrupted    `json:"interrupted,omitempty"`
	TaskDone       *RuntimeObservationTaskDone       `json:"task_done,omitempty"`
}

func (o RuntimeObservationOutcome) Validate() error {
	payloads := 0
	if o.Question != nil {
		payloads++
	}
	if o.FinalAnswer != nil {
		payloads++
	}
	if o.ExecutionError != nil {
		payloads++
	}
	if o.Interrupted != nil {
		payloads++
	}
	if o.TaskDone != nil {
		payloads++
	}
	if payloads != 1 {
		return errors.New("observation outcome must contain exactly one payload")
	}
	if o.NodeKey != nil && strings.TrimSpace(*o.NodeKey) == "" {
		return errors.New("node_key must not be blank")
	}
	if o.SessionID != nil {
		if err := validateRequiredSessionID(*o.SessionID); err != nil {
			return err
		}
	}
	if o.ScriptPath != nil && strings.TrimSpace(*o.ScriptPath) == "" {
		return errors.New("script_path must not be blank")
	}
	switch o.Kind {
	case RuntimeObservationOutcomeQuestion:
		if o.Question == nil {
			return errors.New("question outcome requires question payload")
		}
		return o.Question.Validate()
	case RuntimeObservationOutcomeFinalAnswer:
		if o.FinalAnswer == nil {
			return errors.New("final_answer outcome requires final_answer payload")
		}
		return o.FinalAnswer.Validate()
	case RuntimeObservationOutcomeExecutionError:
		if o.ExecutionError == nil {
			return errors.New("execution_error outcome requires execution_error payload")
		}
		return o.ExecutionError.Validate()
	case RuntimeObservationOutcomeInterrupted:
		if o.Interrupted == nil {
			return errors.New("interrupted outcome requires interrupted payload")
		}
		return o.Interrupted.Validate()
	case RuntimeObservationOutcomeTaskDone:
		if o.TaskDone == nil {
			return errors.New("task_done outcome requires task_done payload")
		}
		return nil
	default:
		return errors.New("observation outcome kind is invalid")
	}
}

type RuntimeObservationResponse struct {
	Target   RuntimeObservationTarget    `json:"target"`
	Outcomes []RuntimeObservationOutcome `json:"outcomes"`
}

func (r RuntimeObservationResponse) Validate() error {
	if err := r.Target.Validate(); err != nil {
		return err
	}
	if len(r.Outcomes) == 0 {
		return errors.New("observation outcomes are required")
	}
	for _, outcome := range r.Outcomes {
		if err := outcome.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type RuntimeLiveWatchRequest struct {
	SessionID string `json:"session_id"`
}

func (r RuntimeLiveWatchRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}

type RuntimeLiveWatchResponse = RuntimeObservationResponse

type WorkflowTaskObservationMode string

const (
	WorkflowTaskObservationModeWait  WorkflowTaskObservationMode = "wait"
	WorkflowTaskObservationModeWatch WorkflowTaskObservationMode = "watch"
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
	switch r.Mode {
	case WorkflowTaskObservationModeWait, WorkflowTaskObservationModeWatch:
		return nil
	default:
		return errors.New("workflow task observation mode must be wait or watch")
	}
}

type WorkflowTaskObservationResponse = RuntimeObservationResponse
