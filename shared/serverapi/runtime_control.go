package serverapi

import (
	"errors"
	"strings"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
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
	ClientRequestID                 string                       `json:"client_request_id"`
	SessionID                       string                       `json:"session_id"`
	Text                            string                       `json:"text"`
	OperationRef                    clientui.RuntimeOperationRef `json:"operation_ref"`
	PreSubmitCompactionOperationRef clientui.RuntimeOperationRef `json:"pre_submit_compaction_operation_ref,omitempty"`
}

type RuntimeSubmitUserTurnResponse struct {
	Message     string `json:"message"`
	Compacted   bool   `json:"compacted,omitempty"`
	Steered     bool   `json:"steered,omitempty"`
	QueueItemID string `json:"queue_item_id,omitempty"`
}

type RuntimeSubmitUserShellCommandRequest struct {
	ClientRequestID string                       `json:"client_request_id"`
	SessionID       string                       `json:"session_id"`
	Command         string                       `json:"command"`
	OperationRef    clientui.RuntimeOperationRef `json:"operation_ref"`
}

type RuntimeCompactContextRequest struct {
	ClientRequestID string                       `json:"client_request_id"`
	SessionID       string                       `json:"session_id"`
	Args            string                       `json:"args"`
	OperationRef    clientui.RuntimeOperationRef `json:"operation_ref"`
}

type RuntimeCompactContextForPreSubmitRequest struct {
	ClientRequestID string                       `json:"client_request_id"`
	SessionID       string                       `json:"session_id"`
	OperationRef    clientui.RuntimeOperationRef `json:"operation_ref"`
}

type RuntimeHasQueuedUserWorkRequest struct {
	SessionID string `json:"session_id"`
}

type RuntimeHasQueuedUserWorkResponse struct {
	HasQueuedUserWork bool `json:"has_queued_user_work"`
}

type RuntimeSubmitQueuedUserMessagesRequest struct {
	ClientRequestID string                       `json:"client_request_id"`
	SessionID       string                       `json:"session_id"`
	OperationRef    clientui.RuntimeOperationRef `json:"operation_ref"`
}

type RuntimeSubmitQueuedUserMessagesResponse struct {
	Message string `json:"message"`
}

type RuntimeInterruptRequest struct {
	ClientRequestID      string                         `json:"client_request_id"`
	SessionID            string                         `json:"session_id"`
	TargetOperationRef   *clientui.RuntimeOperationRef  `json:"target_operation_ref,omitempty"`
	PendingOperationRefs []clientui.RuntimeOperationRef `json:"pending_operation_refs,omitempty"`
}

type RuntimeInterruptResponse struct {
	Version             clientui.ReadModelVersion                   `json:"version"`
	Activity            clientui.RuntimeActivity                    `json:"activity"`
	InputReconciliation clientui.RuntimeInputReconciliationSnapshot `json:"input_reconciliation"`
}

type RuntimeQueueUserMessageRequest struct {
	ClientRequestID string                       `json:"client_request_id"`
	SessionID       string                       `json:"session_id"`
	OperationRef    clientui.RuntimeOperationRef `json:"operation_ref"`
	Text            string                       `json:"text"`
}

type RuntimeQueueUserMessageResponse struct {
	QueueItemID     string `json:"queue_item_id"`
	Text            string `json:"text"`
	ClientRequestID string `json:"client_request_id"`
}

type RuntimeLiveSteerRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Text            string `json:"text"`
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

type RuntimeGoal struct {
	ID        string    `json:"id"`
	Objective string    `json:"objective"`
	Status    string    `json:"status"`
	Suspended bool      `json:"suspended,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RuntimeGoalShowRequest struct {
	SessionID string `json:"session_id"`
}

type RuntimeGoalShowResponse struct {
	Goal *RuntimeGoal `json:"goal,omitempty"`
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

func validateRuntimeOperationRef(ref clientui.RuntimeOperationRef, kind clientui.RuntimeOperationKind, clientRequestID string) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Kind != kind {
		return errors.New("operation_ref kind does not match request")
	}
	if strings.TrimSpace(ref.ClientRequestID) != strings.TrimSpace(clientRequestID) {
		return errors.New("operation_ref client_request_id must match request client_request_id")
	}
	return nil
}

func isZeroServerAPIRuntimeOperationRef(ref clientui.RuntimeOperationRef) bool {
	return ref.Kind == "" && strings.TrimSpace(ref.ClientRequestID) == "" && strings.TrimSpace(ref.QueueItemID) == ""
}

func validateRuntimeGoalActionRequest(clientRequestID string, sessionID string, actor string) error {
	if err := validateClientRequestID(clientRequestID); err != nil {
		return err
	}
	if err := validateRequiredSessionID(sessionID); err != nil {
		return err
	}
	return validateGoalActor(actor)
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
	if err := validateRuntimeOperationRef(r.OperationRef, clientui.RuntimeOperationKindSubmit, r.ClientRequestID); err != nil {
		return err
	}
	if isZeroServerAPIRuntimeOperationRef(r.PreSubmitCompactionOperationRef) {
		return nil
	}
	return validateRuntimeOperationRef(r.PreSubmitCompactionOperationRef, clientui.RuntimeOperationKindPreSubmitCompact, r.PreSubmitCompactionOperationRef.ClientRequestID)
}
func (r RuntimeSubmitUserShellCommandRequest) Validate() error {
	if err := validateRuntimeControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
	}
	return validateRuntimeOperationRef(r.OperationRef, clientui.RuntimeOperationKindUserShell, r.ClientRequestID)
}
func (r RuntimeCompactContextRequest) Validate() error {
	if err := validateRuntimeControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
	}
	return validateRuntimeOperationRef(r.OperationRef, clientui.RuntimeOperationKindCompact, r.ClientRequestID)
}
func (r RuntimeCompactContextForPreSubmitRequest) Validate() error {
	if err := validateRuntimeControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
	}
	return validateRuntimeOperationRef(r.OperationRef, clientui.RuntimeOperationKindPreSubmitCompact, r.ClientRequestID)
}
func (r RuntimeHasQueuedUserWorkRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}
func (r RuntimeSubmitQueuedUserMessagesRequest) Validate() error {
	if err := validateRuntimeControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
	}
	return validateRuntimeOperationRef(r.OperationRef, clientui.RuntimeOperationKindSubmitQueued, r.ClientRequestID)
}
func (r RuntimeInterruptRequest) Validate() error {
	if err := validateRuntimeControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
	}
	if r.TargetOperationRef != nil {
		if err := r.TargetOperationRef.Validate(); err != nil {
			return err
		}
	}
	for _, ref := range r.PendingOperationRefs {
		if err := ref.Validate(); err != nil {
			return err
		}
	}
	return nil
}
func (r RuntimeQueueUserMessageRequest) Validate() error {
	if err := validateRuntimeControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
	}
	if err := validateRuntimeOperationRef(r.OperationRef, clientui.RuntimeOperationKindQueuedMessage, r.ClientRequestID); err != nil {
		return err
	}
	if strings.TrimSpace(r.OperationRef.QueueItemID) != "" {
		return errors.New("queued-message create operation_ref must not carry queue item id")
	}
	if strings.TrimSpace(r.Text) == "" {
		return errors.New("text is required")
	}
	return nil
}
func (r RuntimeLiveSteerRequest) Validate() error {
	if err := validateRuntimeLiveControlRequest(r.ClientRequestID, r.SessionID); err != nil {
		return err
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
