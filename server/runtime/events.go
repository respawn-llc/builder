package runtime

import (
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/transcript"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type EventKind string

type AssistantStreamAbortReason string

type BackgroundShellEventType string

const (
	EventConversationUpdated        EventKind = "conversation_updated"
	EventAssistantDelta             EventKind = "assistant_delta"
	EventAssistantDeltaReset        EventKind = "assistant_delta_reset"
	EventStreamingErrorUpdated      EventKind = "streaming_error_updated"
	EventReasoningDelta             EventKind = "reasoning_delta"
	EventReasoningDeltaReset        EventKind = "reasoning_delta_reset"
	EventAssistantMessage           EventKind = "assistant_message"
	EventModelResponse              EventKind = "model_response_received"
	EventUserMessageFlushed         EventKind = "user_message_flushed"
	EventToolCallStarted            EventKind = "tool_call_started"
	EventToolCallCompleted          EventKind = "tool_call_completed"
	EventToolCallAborted            EventKind = "tool_call_aborted"
	EventReviewerStarted            EventKind = "reviewer_started"
	EventReviewerCompleted          EventKind = "reviewer_completed"
	EventInFlightClearFailed        EventKind = "in_flight_clear_failed"
	EventCompactionStarted          EventKind = "context_compaction_started"
	EventCompactionCompleted        EventKind = "context_compaction_completed"
	EventCompactionFailed           EventKind = "context_compaction_failed"
	EventCacheWarning               EventKind = "cache_warning"
	EventLocalEntryAdded            EventKind = "local_entry_added"
	EventRunStateChanged            EventKind = "run_state_changed"
	EventBackgroundUpdated          EventKind = "background_updated"
	EventSleepGuardFailed           EventKind = "sleep_guard_failed"
	EventPromptHistoryPersistFailed EventKind = "prompt_history_persist_failed"
	EventContextFactsPersistFailed  EventKind = "context_facts_persist_failed"
	EventProviderTurnStateInvalid   EventKind = "provider_turn_state_invalid"
	EventGoalStatusUpdated          EventKind = "goal_status_updated"
	EventQueuedUserMessageStatus    EventKind = "queued_user_message_status"
	EventHumanInputInterrupted      EventKind = "human_input_interrupted"
	EventLiveRunFinished            EventKind = "live_run_finished"

	AssistantStreamAbortSuperseded AssistantStreamAbortReason = "superseded"
)

const (
	BackgroundShellEventBackgrounded BackgroundShellEventType = "backgrounded"
	BackgroundShellEventCompleted    BackgroundShellEventType = "completed"
	BackgroundShellEventKilled       BackgroundShellEventType = "killed"
)

func (t BackgroundShellEventType) IsTerminal() bool {
	switch t {
	case BackgroundShellEventCompleted, BackgroundShellEventKilled:
		return true
	default:
		return false
	}
}

type QueuedUserMessageStatus string

const (
	QueuedUserMessageAccepted  QueuedUserMessageStatus = "accepted"
	QueuedUserMessageSubmitted QueuedUserMessageStatus = "submitted"
	QueuedUserMessageFailed    QueuedUserMessageStatus = "failed"
	QueuedUserMessageDiscarded QueuedUserMessageStatus = "discarded"
)

type QueuedUserMessageFailureReason string

const (
	QueuedUserMessageFailureClosing                    QueuedUserMessageFailureReason = "closing"
	QueuedUserMessageFailureTerminalWorkflowCompletion QueuedUserMessageFailureReason = "terminal_workflow_completion"
	QueuedUserMessageFailureRuntimeUnavailable         QueuedUserMessageFailureReason = "runtime_unavailable"
	QueuedUserMessageFailureStopped                    QueuedUserMessageFailureReason = "stopped"
)

type QueuedUserMessageStatusEvent struct {
	SessionID     string
	QueueItemID   string
	Status        QueuedUserMessageStatus
	FailureReason QueuedUserMessageFailureReason
	Text          string
}

type QueuedUserMessageIdentity struct {
	QueueItemID string
}

type InterruptedHumanInput struct {
	QueueItemID string
	Text        string
}

type HumanInputInterruptedEvent struct {
	Items []InterruptedHumanInput
}

type Event struct {
	Kind                         EventKind
	StepID                       *string
	CommittedTranscriptChanged   bool
	TranscriptRevision           int64
	CommittedEntryCount          int
	CommittedEntryStart          int
	CommittedEntryStartSet       bool
	CommittedProvenance          *TranscriptCommittedRowProvenance
	Error                        string
	AssistantDelta               string
	AssistantDeltaPhase          llm.MessagePhase
	AssistantStreamMetadata      *AssistantStreamMetadata
	AssistantTranscriptStreamID  *uuid.UUID
	AssistantStreamAbortReason   string
	ReasoningDelta               *llm.ReasoningSummaryDelta
	ReasoningTraceIdentity       *TranscriptReasoningTraceIdentity
	UserMessage                  string
	UserMessageBatch             []string
	UserMessageBatchQueueItemIDs []string
	UserMessageBatchQueuedItems  []QueuedUserMessageIdentity
	Message                      llm.Message
	ModelResponse                *ModelResponseTrace
	ToolCall                     *llm.ToolCall
	ToolResult                   *tools.Result
	ToolAbortReason              string
	Reviewer                     *ReviewerStatus
	Compaction                   *CompactionStatus
	CacheWarning                 *transcript.CacheWarning
	CacheWarningVisibility       transcript.EntryVisibility
	LocalEntry                   *ChatEntry
	LocalEntryProjected          bool
	RunState                     *RunState
	ContextUsage                 *ContextUsage
	Background                   *BackgroundShellEvent
	GoalStatus                   *GoalStatusUpdate
	QueuedUserMessageStatus      *QueuedUserMessageStatusEvent
	HumanInputInterrupted        *HumanInputInterruptedEvent
	LiveRunResult                *LiveRunResult
}

func exactStepIDPointer(stepID string) *string {
	normalized := strings.TrimSpace(stepID)
	if normalized == "" {
		panic("Step identity must not be empty")
	}
	return &normalized
}

func cloneOptionalStepID(stepID *string) *string {
	if stepID == nil {
		return nil
	}
	return exactStepIDPointer(*stepID)
}

func (event Event) withStepID(stepID *string) Event {
	if event.StepID == nil {
		event.StepID = cloneOptionalStepID(stepID)
	} else {
		event.StepID = cloneOptionalStepID(event.StepID)
	}
	return event
}

func requireStepID(stepID *string, operation string) (string, error) {
	if stepID == nil {
		return "", fmt.Errorf("%s requires Step identity", operation)
	}
	normalized := strings.TrimSpace(*stepID)
	if normalized == "" {
		return "", fmt.Errorf("%s received an empty Step identity", operation)
	}
	return normalized, nil
}

type GoalStatusUpdate struct {
	State   session.GoalState
	Cleared bool
}

type RunState struct {
	Lifecycle  RunLifecycle
	RunID      string
	ActiveKind ActiveKind
	Status     RunStatus
	StartedAt  time.Time
	FinishedAt time.Time
}

type BackgroundShellEvent struct {
	Type              BackgroundShellEventType
	ID                string
	ActivityID        uuid.UUID
	OwnerRunID        string
	OwnerStepID       string
	State             string
	Command           string
	Workdir           string
	LogPath           string
	NoticeText        string
	CompactText       string
	Preview           string
	PreviewRemoved    int
	ExitCode          *int
	UserRequestedKill bool
	NoticeSuppressed  bool
}

type ReviewerStatus struct {
	Outcome               string `json:"outcome,omitempty"`
	SuggestionsCount      int    `json:"suggestions_count,omitempty"`
	CacheHitPercent       int    `json:"cache_hit_percent,omitempty"`
	HasCacheHitPercentage bool   `json:"has_cache_hit_percentage,omitempty"`
	Error                 string `json:"error,omitempty"`
}

type ModelResponseTrace struct {
	AssistantPhase   llm.MessagePhase `json:"assistant_phase,omitempty"`
	AssistantChars   int              `json:"assistant_chars,omitempty"`
	ToolCallsCount   int              `json:"tool_calls_count,omitempty"`
	OutputItemsCount int              `json:"output_items_count,omitempty"`
	OutputItemTypes  []string         `json:"output_item_types,omitempty"`
}

type CompactionStatus struct {
	Mode              string
	Engine            string
	Provider          string
	TrimmedItemsCount *int
	Count             int
	Error             string
}
