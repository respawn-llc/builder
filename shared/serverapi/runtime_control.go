package serverapi

import (
	"errors"
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
)

type RuntimeSetSessionNameRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Name            string `json:"name"`
}

type RuntimeSetThinkingLevelRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Level           string `json:"level"`
}

type RuntimeSetFastModeEnabledRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Enabled         bool   `json:"enabled"`
}

type RuntimeSetFastModeEnabledResponse struct {
	Changed bool `json:"changed"`
}

type RuntimeSetReviewerEnabledRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Enabled         bool   `json:"enabled"`
}

type RuntimeSetReviewerEnabledResponse struct {
	Changed bool   `json:"changed"`
	Mode    string `json:"mode"`
}

type RuntimeSetAutoCompactionEnabledRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Enabled         bool   `json:"enabled"`
}

type RuntimeSetAutoCompactionEnabledResponse struct {
	Changed bool `json:"changed"`
	Enabled bool `json:"enabled"`
}

type RuntimeSetQuestionsEnabledRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Enabled         bool   `json:"enabled"`
}

type RuntimeSetQuestionsEnabledResponse struct {
	Changed bool `json:"changed"`
	Enabled bool `json:"enabled"`
}

type RuntimeAppendCommittedEntryRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Role            string `json:"role"`
	Text            string `json:"text"`
	Visibility      string `json:"visibility,omitempty"`
	NoticeID        string `json:"notice_id,omitempty"`
}

type RuntimeShouldCompactBeforeUserMessageRequest struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

type RuntimeShouldCompactBeforeUserMessageResponse struct {
	ShouldCompact bool `json:"should_compact"`
}

type RuntimeSubmitUserTurnRequest struct {
	ClientRequestID string               `json:"client_request_id"`
	SessionID       string               `json:"session_id"`
	Input           RuntimeUserTurnInput `json:"input"`
}

type RuntimeUserTurnInputKind = runtimeinput.Kind

const (
	RuntimeUserTurnInputKindText          = runtimeinput.KindText
	RuntimeUserTurnInputKindPromptCommand = runtimeinput.KindPromptCommand
)

type RuntimePromptCommandInput = runtimeinput.PromptCommand
type RuntimeUserTurnInput = runtimeinput.Input

type RuntimeSubmitUserTurnResponse struct {
	Message     *string                     `json:"message,omitempty"`
	ResultKind  clientui.UserTurnResultKind `json:"result_kind"`
	Compacted   bool                        `json:"compacted,omitempty"`
	Steered     bool                        `json:"steered,omitempty"`
	QueueItemID string                      `json:"queue_item_id,omitempty"`
}

func (r RuntimeSubmitUserTurnResponse) Validate() error {
	switch r.ResultKind {
	case clientui.UserTurnResultKindQueued:
		if !r.Steered {
			return errors.New("queued result must be steered")
		}
		if strings.TrimSpace(r.QueueItemID) == "" {
			return errors.New("queued result requires queue_item_id")
		}
		if r.Message != nil {
			return errors.New("queued result must not include message")
		}
	case clientui.UserTurnResultKindNoFinal:
		if r.Steered || strings.TrimSpace(r.QueueItemID) != "" || r.Message != nil {
			return errors.New("no_final result must not include message or queue state")
		}
	case clientui.UserTurnResultKindAssistantFinal:
		if r.Steered || strings.TrimSpace(r.QueueItemID) != "" {
			return errors.New("assistant_final result must not include queue state")
		}
		if r.Message == nil || strings.TrimSpace(*r.Message) == "" {
			return errors.New("assistant_final result requires a present nonblank message")
		}
	case clientui.UserTurnResultKindSilentFinal:
		if r.Steered || strings.TrimSpace(r.QueueItemID) != "" {
			return errors.New("silent_final result must not include queue state")
		}
		if r.Message == nil || *r.Message != "" {
			return errors.New("silent_final result requires a present empty message")
		}
	default:
		return errors.New("result_kind must be queued, no_final, assistant_final, or silent_final")
	}
	return nil
}

type RuntimeSubmitUserShellCommandRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Command         string `json:"command"`
}

type RuntimeCompactContextRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Args            string `json:"args"`
}

type RuntimeInterruptRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
}

type RuntimeInterruptResponse struct {
	Version  clientui.ReadModelVersion `json:"version"`
	Activity clientui.RuntimeActivity  `json:"activity"`
}

type RuntimeLiveSteerRequest struct {
	ClientRequestID string  `json:"client_request_id"`
	SessionID       string  `json:"session_id"`
	CallerSessionID *string `json:"caller_session_id,omitempty"`
	Text            string  `json:"text"`
}

type RuntimeLiveSteerResponse struct {
	QueueItemID     string `json:"queue_item_id"`
	Text            string `json:"text"`
	ClientRequestID string `json:"client_request_id"`
}

type RuntimeLiveStopRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
}

type RuntimeLiveStopStatus string

const (
	RuntimeLiveStopStatusStopped RuntimeLiveStopStatus = "stopped"
	RuntimeLiveStopStatusIdle    RuntimeLiveStopStatus = "idle"
)

type RuntimeLiveStopResponse struct {
	Status RuntimeLiveStopStatus `json:"status"`
}

type RuntimeLiveWaitRequest struct {
	SessionID string `json:"session_id"`
}

type RuntimeLiveResultKind string

const (
	RuntimeLiveResultKindAssistantFinalAnswer RuntimeLiveResultKind = "assistant_final_answer"
	RuntimeLiveResultKindNoFinalAnswer        RuntimeLiveResultKind = "no_final_answer"
)

type RuntimeLiveWaitResponse struct {
	SessionID      string                `json:"session_id"`
	SessionName    string                `json:"session_name"`
	Result         *string               `json:"result"`
	DurationMillis int64                 `json:"duration_ms"`
	LiveRunGroupID string                `json:"live_run_group_id"`
	TerminalRunID  string                `json:"terminal_run_id"`
	TerminalStepID string                `json:"terminal_step_id"`
	TerminalStatus string                `json:"terminal_status"`
	ResultKind     RuntimeLiveResultKind `json:"result_kind"`
	NoAnswerReason *string               `json:"no_answer_reason"`
}

type RuntimeDiscardQueuedUserMessageRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	QueueItemID     string `json:"queue_item_id"`
}

type RuntimeDiscardQueuedUserMessageResponse struct {
	Discarded bool `json:"discarded"`
}

type RuntimeRecordPromptHistoryRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Text            string `json:"text"`
}

type RuntimeGoal = clientui.Goal

type RuntimeGoalShowRequest struct {
	SessionID string `json:"session_id"`
}

type RuntimeGoalShowResponse = clientui.GoalEnvelope
type RuntimeGoalMutationResponse struct {
	Goal         *clientui.Goal            `json:"goal,omitempty"`
	Pending      *clientui.GoalPreview     `json:"pending,omitempty"`
	Availability clientui.GoalAvailability `json:"availability"`
}

func (r RuntimeGoalMutationResponse) Validate() error {
	if err := r.Availability.Validate(); err != nil {
		return err
	}
	if r.Goal != nil && r.Pending != nil {
		return errors.New("goal mutation response cannot contain Goal and pending preview")
	}
	if r.Goal != nil {
		return r.Goal.Validate()
	}
	if r.Pending != nil {
		return r.Pending.Validate()
	}
	return nil
}

type RuntimeGoalSetRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Objective       string `json:"objective"`
	Actor           string `json:"actor"`
	RunID           string `json:"run_id,omitempty"`
	StepID          string `json:"step_id,omitempty"`
}

type RuntimeGoalStatusRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Actor           string `json:"actor"`
	RunID           string `json:"run_id,omitempty"`
	StepID          string `json:"step_id,omitempty"`
}

type RuntimeGoalClearRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Actor           string `json:"actor"`
}

func validateClientRequestID(clientRequestID string) error {
	if strings.TrimSpace(clientRequestID) == "" {
		return errors.New("client_request_id is required")
	}
	return nil
}

func validateUUIDV4Field(name string, value string) error {
	return runtimeids.ValidateUUIDv4(value, name)
}

func validateRuntimeLiveControlRequest(clientRequestID string, sessionID string) error {
	if err := validateUUIDV4Field("client_request_id", clientRequestID); err != nil {
		return err
	}
	return validateUUIDV4Field("session_id", sessionID)
}

func validateGoalActor(actor string) error {
	switch strings.TrimSpace(actor) {
	case "user", "agent", "system":
		return nil
	default:
		return errors.New("actor must be user, agent, or system")
	}
}

func validateRuntimeControlRequest(clientRequestID string, sessionID string) error {
	if err := validateClientRequestID(clientRequestID); err != nil {
		return err
	}
	return validateRequiredSessionID(sessionID)
}

func (r RuntimeSetSessionNameRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeSetThinkingLevelRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeSetFastModeEnabledRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeSetReviewerEnabledRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeSetAutoCompactionEnabledRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeSetQuestionsEnabledRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeAppendCommittedEntryRequest) Validate() error {
	if err := validateRuntimeControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(r.Visibility)) {
	case "", "auto", "o", "oc", "d", "x":
	default:
		return errors.New("visibility must be empty/auto, O, OC, D, or X")
	}
	return nil
}
func (r RuntimeShouldCompactBeforeUserMessageRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}
func (r RuntimeSubmitUserTurnRequest) Validate() error {
	if err := validateRuntimeControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
	}
	return r.Input.Validate()
}
func (r RuntimeSubmitUserShellCommandRequest) Validate() error {
	if err := validateRuntimeControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.Command) == "" {
		return errors.New("shell command is required")
	}
	return nil
}
func (r RuntimeCompactContextRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeInterruptRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeLiveSteerRequest) Validate() error {
	if err := validateRuntimeLiveControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
	}
	if r.CallerSessionID != nil {
		callerSessionID, err := runtimeids.ParseSessionID(*r.CallerSessionID)
		if err != nil {
			return fmt.Errorf("caller_session_id: %w", err)
		}
		if !callerSessionID.IsCanonicalUUIDv4() {
			return errors.New("caller_session_id: canonical UUIDv4 required")
		}
	}
	if strings.TrimSpace(r.Text) == "" {
		return errors.New("text is required")
	}
	return nil
}
func (r RuntimeLiveSteerResponse) Validate() error {
	if err := validateUUIDV4Field("client_request_id", r.ClientRequestID); err != nil {
		return err
	}
	if err := validateUUIDV4Field("queue_item_id", r.QueueItemID); err != nil {
		return err
	}
	if strings.TrimSpace(r.Text) == "" {
		return errors.New("text is required")
	}
	return nil
}
func (r RuntimeLiveStopRequest) Validate() error {
	return validateRuntimeLiveControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeLiveStopResponse) Validate() error {
	switch r.Status {
	case RuntimeLiveStopStatusStopped, RuntimeLiveStopStatusIdle:
		return nil
	default:
		return errors.New("status must be stopped or idle")
	}
}
func (r RuntimeLiveWaitRequest) Validate() error {
	return validateUUIDV4Field("session_id", r.SessionID)
}
func (r RuntimeLiveWaitResponse) Validate() error {
	if err := validateUUIDV4Field("session_id", r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.SessionName) == "" {
		return errors.New("session_name is required")
	}
	for name, value := range map[string]string{
		"live_run_group_id": r.LiveRunGroupID,
		"terminal_run_id":   r.TerminalRunID,
		"terminal_step_id":  r.TerminalStepID,
	} {
		if err := validateUUIDV4Field(name, value); err != nil {
			return err
		}
	}
	if strings.TrimSpace(r.TerminalStatus) == "" {
		return errors.New("terminal_status is required")
	}
	if r.DurationMillis < 0 {
		return errors.New("duration_ms must not be negative")
	}
	switch r.ResultKind {
	case RuntimeLiveResultKindAssistantFinalAnswer:
		if r.Result == nil || strings.TrimSpace(*r.Result) == "" {
			return errors.New("result is required")
		}
	case RuntimeLiveResultKindNoFinalAnswer:
		if r.NoAnswerReason == nil || strings.TrimSpace(*r.NoAnswerReason) == "" {
			return errors.New("no_answer_reason is required")
		}
	default:
		return errors.New("result_kind must be assistant_final_answer or no_final_answer")
	}
	return nil
}
func (r RuntimeDiscardQueuedUserMessageRequest) Validate() error {
	if err := validateRuntimeControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.QueueItemID) == "" {
		return errors.New("queue_item_id is required")
	}
	return nil
}
func (r RuntimeRecordPromptHistoryRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeGoalShowRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}
func (r RuntimeGoalSetRequest) Validate() error {
	if err := validateRuntimeControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
	}
	if strings.TrimSpace(r.Objective) == "" {
		return errors.New("objective is required")
	}
	return validateGoalActor(r.Actor)
}
func (r RuntimeGoalStatusRequest) Validate() error {
	if err := validateRuntimeControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
	}
	return validateGoalActor(r.Actor)
}
func (r RuntimeGoalClearRequest) Validate() error {
	if err := validateRuntimeControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
	}
	return validateGoalActor(r.Actor)
}
