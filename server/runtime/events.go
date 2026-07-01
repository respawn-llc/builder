package runtime

import (
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/transcript"
	"time"
)

type EventKind string

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
	EventGoalStatusUpdated          EventKind = "goal_status_updated"
	EventQueuedUserMessageStatus    EventKind = "queued_user_message_status"
)

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
)

type QueuedUserMessageStatusEvent struct {
	SessionID       string
	QueueItemID     string
	ClientRequestID string
	Status          QueuedUserMessageStatus
	FailureReason   QueuedUserMessageFailureReason
	RestoreText     string
}

type Event struct {
	Kind                         EventKind
	StepID                       string
	CommittedTranscriptChanged   bool
	TranscriptRevision           int64
	CommittedEntryCount          int
	CommittedEntryStart          int
	CommittedEntryStartSet       bool
	Error                        string
	AssistantDelta               string
	AssistantDeltaPhase          llm.MessagePhase
	ReasoningDelta               *llm.ReasoningSummaryDelta
	UserMessage                  string
	UserMessageBatch             []string
	UserMessageBatchQueueItemIDs []string
	Message                      llm.Message
	ModelResponse                *ModelResponseTrace
	ToolCall                     *llm.ToolCall
	ToolResult                   *tools.Result
	Reviewer                     *ReviewerStatus
	Compaction                   *CompactionStatus
	CacheWarning                 *transcript.CacheWarning
	CacheWarningVisibility       transcript.EntryVisibility
	LocalEntry                   *ChatEntry
	RunState                     *RunState
	ContextUsage                 *ContextUsage
	Background                   *BackgroundShellEvent
	GoalStatus                   *GoalStatusUpdate
	QueuedUserMessageStatus      *QueuedUserMessageStatusEvent
}

type GoalStatusUpdate struct {
	State   session.GoalState
	Cleared bool
}

type RunState struct {
	Lifecycle  RunLifecycle
	RunID      string
	Status     RunStatus
	StartedAt  time.Time
	FinishedAt time.Time
}

type BackgroundShellEvent struct {
	Type              string
	ID                string
	State             string
	Command           string
	Workdir           string
	LogPath           string
	NoticeText        string
	CompactText       string
	Preview           string
	Removed           int
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
	TrimmedItemsCount int
	Count             int
	Error             string
}
