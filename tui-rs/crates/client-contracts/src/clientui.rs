use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::config::null_to_default;

pub mod main_view;

pub use main_view::*;

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
    pub entries: Vec<TranscriptCommittedRow>,
}

pub type TranscriptRowIntegrity = u8;
pub type TranscriptRowKind = String;
pub type TranscriptNoticeReason = String;
pub type TranscriptNoticeSeverity = String;

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct TranscriptCommittedRow {
    #[serde(rename = "Visibility")]
    pub visibility: EntryVisibility,
    #[serde(rename = "Integrity")]
    pub integrity: TranscriptRowIntegrity,
    #[serde(rename = "Kind")]
    pub kind: TranscriptRowKind,
    #[serde(rename = "User")]
    pub user: Option<TranscriptUserRow>,
    #[serde(rename = "Assistant")]
    pub assistant: Option<TranscriptAssistantRow>,
    #[serde(rename = "Tool")]
    pub tool: Option<TranscriptToolRow>,
    #[serde(rename = "Notice")]
    pub notice: Option<TranscriptNoticeRow>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct TranscriptUserRow {
    #[serde(rename = "StepID")]
    pub step_id: Uuid,
    #[serde(rename = "Text")]
    pub text: String,
    #[serde(rename = "CondensedText")]
    pub condensed_text: Option<String>,
    #[serde(rename = "RollbackTargetID")]
    pub rollback_target_id: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct TranscriptAssistantRow {
    #[serde(rename = "StepID")]
    pub step_id: Uuid,
    #[serde(rename = "StreamID")]
    pub stream_id: Option<Uuid>,
    #[serde(rename = "Text")]
    pub text: String,
    #[serde(rename = "CondensedText")]
    pub condensed_text: Option<String>,
    #[serde(rename = "Phase")]
    pub phase: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct TranscriptToolRow {
    #[serde(rename = "StepID")]
    pub step_id: Uuid,
    #[serde(rename = "ToolCallID")]
    pub tool_call_id: String,
    #[serde(rename = "ToolName")]
    pub tool_name: String,
    #[serde(rename = "Text")]
    pub text: String,
    #[serde(rename = "IsError")]
    pub is_error: bool,
    #[serde(rename = "ResultSummary")]
    pub result_summary: Option<String>,
    #[serde(rename = "CondensedText")]
    pub condensed_text: Option<String>,
    #[serde(rename = "Presentation")]
    pub presentation: Option<ToolCallMeta>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct TranscriptNoticeRow {
    #[serde(rename = "StepID")]
    pub step_id: Option<Uuid>,
    #[serde(rename = "Reason")]
    pub reason: TranscriptNoticeReason,
    #[serde(rename = "Severity")]
    pub severity: TranscriptNoticeSeverity,
    #[serde(rename = "MessageType")]
    pub message_type: Option<String>,
    #[serde(rename = "LegacyText")]
    pub legacy_text: Option<String>,
    #[serde(rename = "NoticeID")]
    pub notice_id: Option<String>,
    #[serde(rename = "SourcePath")]
    pub source_path: Option<String>,
    #[serde(rename = "Worktree")]
    pub worktree: Option<TranscriptWorktreeContext>,
    #[serde(rename = "CacheWarning")]
    pub cache_warning: Option<TranscriptCacheWarning>,
    #[serde(rename = "Diagnostic")]
    pub diagnostic: Option<TranscriptDiagnostic>,
    #[serde(rename = "Background")]
    pub background: Option<TranscriptBackgroundNoticeIdentity>,
    #[serde(rename = "CondensedText")]
    pub condensed_text: Option<String>,
    #[serde(rename = "CompactLabel")]
    pub compact_label: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct TranscriptWorktreeContext {
    #[serde(rename = "Branch")]
    pub branch: Option<String>,
    #[serde(rename = "WorktreePath")]
    pub worktree_path: String,
    #[serde(rename = "WorkspaceRoot")]
    pub workspace_root: String,
    #[serde(rename = "EffectiveCwd")]
    pub effective_cwd: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct TranscriptCacheWarning {
    #[serde(rename = "Scope")]
    pub scope: String,
    #[serde(rename = "Reason")]
    pub reason: String,
    #[serde(rename = "LostInputTokens")]
    pub lost_input_tokens: Option<i32>,
    #[serde(rename = "Visibility")]
    pub visibility: EntryVisibility,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct TranscriptDiagnostic {
    #[serde(rename = "Code")]
    pub code: String,
    #[serde(rename = "Detail")]
    pub detail: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct TranscriptBackgroundNoticeIdentity {
    #[serde(rename = "ActivityID")]
    pub activity_id: Uuid,
    #[serde(rename = "ProcessID")]
    pub process_id: String,
    #[serde(rename = "ExitCode")]
    pub exit_code: Option<i32>,
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
    #[serde(rename = "MovedToBackground")]
    pub moved_to_background: bool,
    #[serde(rename = "ShellExitCode")]
    pub shell_exit_code: Option<i32>,
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
pub enum ApprovalDecision {
    #[serde(rename = "allow_once")]
    AllowOnce,
    #[serde(rename = "allow_session")]
    AllowSession,
    #[serde(rename = "deny")]
    Deny,
}
