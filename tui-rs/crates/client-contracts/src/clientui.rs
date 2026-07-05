use serde::{Deserialize, Serialize};

use crate::config::null_to_default;

pub mod main_view;

pub use main_view::*;

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct Event {
    #[serde(rename = "Sequence")]
    pub sequence: u64,
    #[serde(rename = "Kind")]
    pub kind: EventKind,
    #[serde(rename = "StepID")]
    pub step_id: String,
    #[serde(rename = "RecoveryCause")]
    pub recovery_cause: TranscriptRecoveryCause,
    #[serde(rename = "CommittedTranscriptChanged")]
    pub committed_transcript_changed: bool,
    #[serde(rename = "Error")]
    pub error: String,
    #[serde(rename = "AssistantDelta")]
    pub assistant_delta: String,
    #[serde(rename = "ReasoningDelta")]
    pub reasoning_delta: Option<ReasoningDelta>,
    #[serde(rename = "UserMessage")]
    pub user_message: String,
    #[serde(
        rename = "UserMessageBatch",
        default,
        deserialize_with = "null_to_default"
    )]
    pub user_message_batch: Vec<String>,
    #[serde(
        rename = "UserMessageBatchQueueItemIDs",
        default,
        deserialize_with = "null_to_default"
    )]
    pub user_message_batch_queue_item_ids: Vec<String>,
    #[serde(
        rename = "TranscriptEntries",
        default,
        deserialize_with = "null_to_default"
    )]
    pub transcript_entries: Vec<ChatEntry>,
    #[serde(rename = "Compaction")]
    pub compaction: Option<CompactionStatus>,
    #[serde(rename = "CacheWarning")]
    pub cache_warning: Option<CacheWarning>,
    #[serde(rename = "CacheWarningVisibility")]
    pub cache_warning_visibility: EntryVisibility,
    #[serde(rename = "RunState")]
    pub run_state: Option<RunState>,
    #[serde(rename = "ContextUsage")]
    pub context_usage: Option<RuntimeContextUsage>,
    #[serde(rename = "Background")]
    pub background: Option<BackgroundShellEvent>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub enum EventKind {
    #[serde(rename = "conversation_updated")]
    ConversationUpdated,
    #[serde(rename = "stream_gap")]
    StreamGap,
    #[serde(rename = "assistant_delta")]
    AssistantDelta,
    #[serde(rename = "assistant_delta_reset")]
    AssistantDeltaReset,
    #[serde(rename = "streaming_error_updated")]
    OngoingErrorUpdated,
    #[serde(rename = "reasoning_delta")]
    ReasoningDelta,
    #[serde(rename = "reasoning_delta_reset")]
    ReasoningDeltaReset,
    #[serde(rename = "assistant_message")]
    AssistantMessage,
    #[serde(rename = "model_response_received")]
    ModelResponse,
    #[serde(rename = "user_message_flushed")]
    UserMessageFlushed,
    #[serde(rename = "tool_call_started")]
    ToolCallStarted,
    #[serde(rename = "tool_call_completed")]
    ToolCallCompleted,
    #[serde(rename = "reviewer_started")]
    ReviewerStarted,
    #[serde(rename = "reviewer_completed")]
    ReviewerCompleted,
    #[serde(rename = "in_flight_clear_failed")]
    InFlightClearFailed,
    #[serde(rename = "context_compaction_started")]
    CompactionStarted,
    #[serde(rename = "context_compaction_completed")]
    CompactionCompleted,
    #[serde(rename = "context_compaction_failed")]
    CompactionFailed,
    #[serde(rename = "cache_warning")]
    CacheWarning,
    #[serde(rename = "local_entry_added")]
    LocalEntryAdded,
    #[serde(rename = "run_state_changed")]
    RunStateChanged,
    #[serde(rename = "background_updated")]
    BackgroundUpdated,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub enum TranscriptRecoveryCause {
    #[serde(rename = "")]
    None,
    #[serde(rename = "stream_gap")]
    StreamGap,
    #[serde(rename = "hydrate_retry")]
    HydrateRetry,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ReasoningDelta {
    #[serde(rename = "Key")]
    pub key: String,
    #[serde(rename = "Role")]
    pub role: String,
    #[serde(rename = "Text")]
    pub text: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct CompactionStatus {
    #[serde(rename = "Mode")]
    pub mode: String,
    #[serde(rename = "Count")]
    pub count: i32,
    #[serde(rename = "Error")]
    pub error: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct CacheWarning {
    #[serde(default)]
    pub scope: String,
    pub reason: String,
    #[serde(default)]
    pub cache_key: String,
    #[serde(default)]
    pub lost_input_tokens: i32,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RunState {
    #[serde(rename = "Lifecycle")]
    pub lifecycle: RunLifecycle,
    #[serde(rename = "RunID")]
    pub run_id: String,
    #[serde(rename = "Status")]
    pub status: RunStatus,
    #[serde(rename = "StartedAt")]
    pub started_at: String,
    #[serde(rename = "FinishedAt")]
    pub finished_at: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub enum RunStatus {
    #[serde(rename = "running")]
    Running,
    #[serde(rename = "completed")]
    Completed,
    #[serde(rename = "interrupted")]
    Interrupted,
    #[serde(rename = "failed")]
    Failed,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RunLifecycle {
    #[serde(rename = "Phase")]
    pub phase: RunLifecyclePhase,
    #[serde(rename = "Mode")]
    pub mode: RunMode,
}

impl RunLifecycle {
    pub fn idle() -> Self {
        Self {
            phase: RunLifecyclePhase::Idle,
            mode: RunMode::None,
        }
    }

    pub fn running(mode: RunMode) -> Self {
        Self {
            phase: RunLifecyclePhase::Running,
            mode,
        }
    }

    pub fn finished(mode: RunMode) -> Self {
        Self {
            phase: RunLifecyclePhase::Finished,
            mode,
        }
    }

    pub fn validate(&self) -> Result<(), RunLifecycleError> {
        match self.phase {
            RunLifecyclePhase::Idle if self.mode != RunMode::None => {
                Err(RunLifecycleError::IdleWithRunMode)
            }
            _ => Ok(()),
        }
    }

    pub fn is_running(&self) -> bool {
        self.phase == RunLifecyclePhase::Running
    }

    pub fn is_finished(&self) -> bool {
        self.phase == RunLifecyclePhase::Finished
    }

    pub fn is_goal_loop_running(&self) -> bool {
        self.phase == RunLifecyclePhase::Running && self.mode == RunMode::GoalLoop
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RunLifecycleError {
    IdleWithRunMode,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub enum RunLifecyclePhase {
    #[serde(rename = "idle")]
    Idle,
    #[serde(rename = "running")]
    Running,
    #[serde(rename = "finished")]
    Finished,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub enum RunMode {
    #[serde(rename = "")]
    None,
    #[serde(rename = "turn")]
    Turn,
    #[serde(rename = "goal_loop")]
    GoalLoop,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeContextUsage {
    #[serde(rename = "UsedTokens")]
    pub used_tokens: i32,
    #[serde(rename = "WindowTokens")]
    pub window_tokens: i32,
    #[serde(rename = "CacheHitPercent")]
    pub cache_hit_percent: i32,
    #[serde(rename = "HasCacheHitPercentage")]
    pub has_cache_hit_percentage: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct BackgroundShellEvent {
    #[serde(rename = "Type")]
    pub kind: String,
    #[serde(rename = "ID")]
    pub id: String,
    #[serde(rename = "State")]
    pub state: String,
    #[serde(rename = "Command")]
    pub command: String,
    #[serde(rename = "Workdir")]
    pub workdir: String,
    #[serde(rename = "LogPath")]
    pub log_path: String,
    #[serde(rename = "NoticeText")]
    pub notice_text: String,
    #[serde(rename = "CompactText")]
    pub compact_text: String,
    #[serde(rename = "Preview")]
    pub preview: String,
    #[serde(rename = "Removed")]
    pub removed: i32,
    #[serde(rename = "ExitCode")]
    pub exit_code: Option<i32>,
    #[serde(rename = "UserRequestedKill")]
    pub user_requested_kill: bool,
    #[serde(rename = "NoticeSuppressed")]
    pub notice_suppressed: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ChatEntry {
    #[serde(rename = "Visibility")]
    pub visibility: EntryVisibility,
    #[serde(rename = "RollbackTargetID")]
    pub rollback_target_id: String,
    #[serde(rename = "Role")]
    pub role: String,
    #[serde(rename = "Text")]
    pub text: String,
    #[serde(
        rename = "CondensedText",
        default,
        skip_serializing_if = "String::is_empty"
    )]
    pub ongoing_text: String,
    #[serde(rename = "Phase")]
    pub phase: String,
    #[serde(rename = "MessageType")]
    pub message_type: String,
    #[serde(rename = "SourcePath")]
    pub source_path: String,
    #[serde(rename = "CompactLabel")]
    pub compact_label: String,
    #[serde(rename = "ToolResultSummary")]
    pub tool_result_summary: String,
    #[serde(rename = "ToolCallID")]
    pub tool_call_id: String,
    #[serde(rename = "NoticeID")]
    pub notice_id: String,
    #[serde(rename = "ToolCall")]
    pub tool_call: Option<ToolCallMeta>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize, Serialize)]
#[serde(transparent)]
pub struct TranscriptWindow(String);

impl TranscriptWindow {
    pub fn ongoing_tail() -> Self {
        Self("ongoing_tail".to_owned())
    }

    pub fn from_wire(value: impl Into<String>) -> Self {
        Self(value.into())
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }

    pub fn is_default(&self) -> bool {
        self.0.is_empty()
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct TranscriptPage {
    #[serde(rename = "SessionID")]
    pub session_id: String,
    #[serde(rename = "SessionName")]
    pub session_name: String,
    #[serde(rename = "ConversationFreshness")]
    pub conversation_freshness: ConversationFreshness,
    #[serde(rename = "OlderCursor", default)]
    pub older_cursor: Option<i64>,
    #[serde(rename = "HasMoreAbove", default)]
    pub has_more_above: bool,
    #[serde(rename = "NewerCursor", default)]
    pub newer_cursor: Option<i64>,
    #[serde(rename = "HasMoreBelow", default)]
    pub has_more_below: bool,
    #[serde(rename = "Entries", default, deserialize_with = "null_to_default")]
    pub entries: Vec<ChatEntry>,
}

pub type ConversationFreshness = u8;
pub const CONVERSATION_FRESHNESS_FRESH: ConversationFreshness = 0;
pub const CONVERSATION_FRESHNESS_ESTABLISHED: ConversationFreshness = 1;

pub type EntryVisibility = String;

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ToolCallMeta {
    #[serde(rename = "ToolName")]
    pub tool_name: String,
    #[serde(rename = "Presentation")]
    pub presentation: String,
    #[serde(rename = "RenderBehavior")]
    pub render_behavior: String,
    #[serde(rename = "IsShell")]
    pub is_shell: bool,
    #[serde(rename = "UserInitiated")]
    pub user_initiated: bool,
    #[serde(rename = "Command")]
    pub command: String,
    #[serde(rename = "CompactText")]
    pub compact_text: String,
    #[serde(rename = "InlineMeta")]
    pub inline_meta: String,
    #[serde(rename = "TimeoutLabel")]
    pub timeout_label: String,
    #[serde(rename = "PatchSummary")]
    pub patch_summary: String,
    #[serde(rename = "PatchDetail")]
    pub patch_detail: String,
    #[serde(rename = "PatchRender")]
    pub patch_render: Option<RenderedPatch>,
    #[serde(rename = "RenderHint")]
    pub render_hint: Option<ToolRenderHint>,
    #[serde(rename = "Question")]
    pub question: String,
    #[serde(rename = "Suggestions", default, deserialize_with = "null_to_default")]
    pub suggestions: Vec<String>,
    #[serde(rename = "RecommendedOptionIndex")]
    pub recommended_option_index: i32,
    #[serde(rename = "OmitSuccessfulResult")]
    pub omit_successful_result: bool,
    #[serde(rename = "RawOutputRequested")]
    pub raw_output_requested: bool,
    #[serde(rename = "OutputTruncated")]
    pub output_truncated: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RenderedPatch {
    #[serde(rename = "Files", default, deserialize_with = "null_to_default")]
    pub files: Vec<RenderedFile>,
    #[serde(rename = "SummaryLines", default, deserialize_with = "null_to_default")]
    pub summary_lines: Vec<RenderedLine>,
    #[serde(rename = "DetailLines", default, deserialize_with = "null_to_default")]
    pub detail_lines: Vec<RenderedLine>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RenderedFile {
    #[serde(rename = "AbsPath")]
    pub abs_path: String,
    #[serde(rename = "RelPath")]
    pub rel_path: String,
    #[serde(rename = "Added")]
    pub added: i32,
    #[serde(rename = "Removed")]
    pub removed: i32,
    #[serde(rename = "Diff", default, deserialize_with = "null_to_default")]
    pub diff: Vec<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RenderedLine {
    #[serde(rename = "Kind")]
    pub kind: String,
    #[serde(rename = "Text")]
    pub text: String,
    #[serde(rename = "FileIndex")]
    pub file_index: i32,
    #[serde(rename = "Path")]
    pub path: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ToolRenderHint {
    #[serde(rename = "Kind")]
    pub kind: String,
    #[serde(rename = "Path")]
    pub path: String,
    #[serde(rename = "ResultOnly")]
    pub result_only: bool,
    #[serde(rename = "ShellDialect")]
    pub shell_dialect: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct PendingPromptEvent {
    #[serde(rename = "Sequence")]
    pub sequence: u64,
    #[serde(rename = "Type")]
    pub event_type: PendingPromptEventType,
    #[serde(rename = "PromptID")]
    pub prompt_id: String,
    #[serde(rename = "SessionID")]
    pub session_id: String,
    #[serde(rename = "Question")]
    pub question: String,
    #[serde(rename = "Suggestions", default, deserialize_with = "null_to_default")]
    pub suggestions: Vec<String>,
    #[serde(rename = "RecommendedOptionIndex")]
    pub recommended_option_index: i32,
    #[serde(rename = "Approval")]
    pub approval: bool,
    #[serde(
        rename = "ApprovalOptions",
        default,
        deserialize_with = "null_to_default"
    )]
    pub approval_options: Vec<ApprovalOption>,
    #[serde(rename = "CreatedAt")]
    pub created_at: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub enum PendingPromptEventType {
    #[serde(rename = "pending")]
    Pending,
    #[serde(rename = "resolved")]
    Resolved,
    #[serde(rename = "snapshot_complete")]
    Snapshot,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ApprovalOption {
    #[serde(rename = "Decision")]
    pub decision: ApprovalDecision,
    #[serde(rename = "Label")]
    pub label: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub enum ApprovalDecision {
    #[serde(rename = "allow_once")]
    AllowOnce,
    #[serde(rename = "allow_session")]
    AllowSession,
    #[serde(rename = "deny")]
    Deny,
}
