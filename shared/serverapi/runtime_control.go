package serverapi

import (
	"errors"
	"strings"
	"time"

	"core/shared/transcript"
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
	ClientRequestID       string `json:"client_request_id"`
	SessionID             string `json:"session_id"`
	Text                  string `json:"text"`
	PromptHistoryRecorded bool   `json:"prompt_history_recorded,omitempty"`
}

type RuntimeSubmitUserTurnResponse struct {
	Message     string `json:"message"`
	Compacted   bool   `json:"compacted,omitempty"`
	Steered     bool   `json:"steered,omitempty"`
	QueueItemID string `json:"queue_item_id,omitempty"`
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

type RuntimeCompactContextForPreSubmitRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
}

type RuntimeHasQueuedUserWorkRequest struct {
	SessionID string `json:"session_id"`
}

type RuntimeHasQueuedUserWorkResponse struct {
	HasQueuedUserWork bool `json:"has_queued_user_work"`
}

type RuntimeSubmitQueuedUserMessagesRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
}

type RuntimeSubmitQueuedUserMessagesResponse struct {
	Message string `json:"message"`
}

type RuntimeInterruptRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
}

type RuntimeQueueUserMessageRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Text            string `json:"text"`
}

type RuntimeQueueUserMessageResponse struct {
	QueueItemID     string `json:"queue_item_id"`
	Text            string `json:"text"`
	ClientRequestID string `json:"client_request_id"`
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
}

type RuntimeGoalStatusRequest struct {
	ClientRequestID string `json:"client_request_id"`
	SessionID       string `json:"session_id"`
	Actor           string `json:"actor"`
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
	if visibility := transcript.NormalizeEntryVisibility(transcript.EntryVisibility(r.Visibility)); visibility != "" && visibility != transcript.EntryVisibilityAll && visibility != transcript.EntryVisibilityVerbose {
		return errors.New("visibility must be empty/auto, all, or verbose")
	}
	return nil
}
func (r RuntimeShouldCompactBeforeUserMessageRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}
func (r RuntimeSubmitUserTurnRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeSubmitUserShellCommandRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeCompactContextRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeCompactContextForPreSubmitRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeHasQueuedUserWorkRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}
func (r RuntimeSubmitQueuedUserMessagesRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeInterruptRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
}
func (r RuntimeQueueUserMessageRequest) Validate() error {
	return validateRuntimeControlRequest(r.ClientRequestID, r.SessionID)
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
