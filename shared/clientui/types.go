package clientui

import (
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
	"time"
)

type EventKind string

type TranscriptRecoveryCause string

const (
	EventConversationUpdated        EventKind = "conversation_updated"
	EventStreamGap                  EventKind = "stream_gap"
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
	EventRuntimeActivityChanged     EventKind = "runtime_activity_changed"

	TranscriptRecoveryCauseNone         TranscriptRecoveryCause = ""
	TranscriptRecoveryCauseStreamGap    TranscriptRecoveryCause = "stream_gap"
	TranscriptRecoveryCauseHydrateRetry TranscriptRecoveryCause = "hydrate_retry"
)

type Event struct {
	Sequence                     uint64
	Kind                         EventKind
	StepID                       string
	RecoveryCause                TranscriptRecoveryCause
	CommittedTranscriptChanged   bool
	Error                        string
	AssistantDelta               string
	AssistantDeltaPhase          MessagePhase
	ReasoningDelta               *ReasoningDelta
	UserMessage                  string
	UserMessageBatch             []string
	UserMessageBatchQueueItemIDs []string
	Compaction                   *CompactionStatus
	CacheWarning                 *transcript.CacheWarning
	CacheWarningVisibility       EntryVisibility
	RunState                     *RunState
	ContextUsage                 *RuntimeContextUsage
	Background                   *BackgroundShellEvent
	GoalStatus                   *RuntimeGoalStatusUpdate
	QueuedUserMessageStatus      *QueuedUserMessageStatusEvent
	ReadModelVersion             ReadModelVersion
	RuntimeActivity              *RuntimeActivity
	InputReconciliation          *RuntimeInputReconciliationSnapshot
}

type RuntimeGoalStatusUpdate struct {
	ID        string
	Objective string
	Status    RuntimeGoalStatus
	Cleared   bool
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
	SessionID       string
	QueueItemID     string
	ClientRequestID string
	Status          QueuedUserMessageStatus
	FailureReason   QueuedUserMessageFailureReason
	RestoreText     string
}

type CompactionStatus struct {
	Mode  string
	Count int
	Error string
}

type ReasoningDelta struct {
	Key  string
	Role string
	Text string
}

type RunState struct {
	Lifecycle  RunLifecycle
	RunID      string
	ActiveKind RuntimeActivityActiveKind
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

type ChatEntry struct {
	Visibility         EntryVisibility
	RollbackTargetID   string
	Role               string
	Text               string
	CondensedText      string
	Phase              MessagePhase
	MessageType        MessageType
	SourcePath         string
	CompactLabel       string
	ToolResultSummary  string
	ToolCallID         string
	NoticeID           string
	BackgroundExitCode *int
	ToolCall           *ToolCallMeta
}

const ChatEntryPhaseFinalAnswer = string(MessagePhaseFinal)

type TranscriptPageRequest struct {
	Cursor      *int64
	NewerCursor *int64
}

type TranscriptPage struct {
	SessionID             string
	SessionName           string
	ConversationFreshness ConversationFreshness
	OlderCursor           *int64
	HasMoreAbove          bool
	NewerCursor           *int64
	HasMoreBelow          bool
	Entries               []TranscriptCommittedRow
}

type ToolPresentationKind string
type ToolCallRenderBehavior string
type ToolRenderKind string
type ToolShellDialect string

const (
	ToolPresentationDefault     ToolPresentationKind = "default"
	ToolPresentationShell       ToolPresentationKind = "shell"
	ToolPresentationAskQuestion ToolPresentationKind = "ask_question"

	ToolCallRenderBehaviorDefault     ToolCallRenderBehavior = "default"
	ToolCallRenderBehaviorShell       ToolCallRenderBehavior = "shell"
	ToolCallRenderBehaviorAskQuestion ToolCallRenderBehavior = "ask_question"

	ToolRenderKindShell  ToolRenderKind = "shell"
	ToolRenderKindDiff   ToolRenderKind = "diff"
	ToolRenderKindSource ToolRenderKind = "source"
	ToolRenderKindPlain  ToolRenderKind = "plain"

	ToolShellDialectPosix          ToolShellDialect = "posix"
	ToolShellDialectPowerShell     ToolShellDialect = "powershell"
	ToolShellDialectWindowsCommand ToolShellDialect = "windows_command"
)

type ToolRenderHint struct {
	Kind         ToolRenderKind
	Path         string
	ResultOnly   bool
	ShellDialect ToolShellDialect
}

type ToolCallMeta struct {
	ToolName               string
	Presentation           ToolPresentationKind
	RenderBehavior         ToolCallRenderBehavior
	IsShell                bool
	UserInitiated          bool
	Command                string
	CompactText            string
	InlineMeta             string
	TimeoutLabel           string
	PatchSummary           string
	PatchDetail            string
	PatchRender            *patchformat.RenderedPatch
	RenderHint             *ToolRenderHint
	Question               string
	Suggestions            []string
	RecommendedOptionIndex int
	OmitSuccessfulResult   bool
	RawOutputRequested     bool
	OutputTruncated        bool
	MovedToBackground      bool
	ShellExitCode          *int
}
