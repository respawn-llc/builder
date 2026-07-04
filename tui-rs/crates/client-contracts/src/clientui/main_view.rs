use serde::{Deserialize, Serialize};

use crate::config::null_to_default;

use super::{ChatEntry, ConversationFreshness, RunLifecycle, RunStatus, RuntimeContextUsage};

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeMainView {
    #[serde(rename = "Status")]
    pub status: RuntimeStatus,
    #[serde(rename = "Session")]
    pub session: RuntimeSessionView,
    #[serde(rename = "ActiveRun")]
    pub active_run: Option<RunView>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeStatus {
    #[serde(rename = "ReviewerFrequency")]
    pub reviewer_frequency: String,
    #[serde(rename = "ReviewerEnabled")]
    pub reviewer_enabled: bool,
    #[serde(rename = "AutoCompactionEnabled")]
    pub auto_compaction_enabled: bool,
    #[serde(rename = "QuestionsEnabled")]
    pub questions_enabled: bool,
    #[serde(rename = "FastModeAvailable")]
    pub fast_mode_available: bool,
    #[serde(rename = "FastModeEnabled")]
    pub fast_mode_enabled: bool,
    #[serde(rename = "ConversationFreshness")]
    pub conversation_freshness: ConversationFreshness,
    #[serde(rename = "ParentSessionID")]
    pub parent_session_id: String,
    #[serde(rename = "LastCommittedAssistantFinalAnswer")]
    pub last_committed_assistant_final_answer: String,
    #[serde(rename = "ThinkingLevel")]
    pub thinking_level: String,
    #[serde(rename = "CompactionMode")]
    pub compaction_mode: String,
    #[serde(rename = "ContextUsage")]
    pub context_usage: RuntimeContextUsage,
    #[serde(rename = "CompactionCount")]
    pub compaction_count: i32,
    #[serde(rename = "Goal")]
    pub goal: Option<RuntimeGoal>,
    #[serde(rename = "WorkflowActive", default)]
    pub workflow_active: bool,
    #[serde(rename = "WorkflowSession", default)]
    pub workflow_session: Option<WorkflowSessionStatus>,
    #[serde(rename = "Update")]
    pub update: UpdateStatus,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorkflowSessionStatus {
    #[serde(rename = "RunID")]
    pub run_id: String,
    #[serde(rename = "TaskID")]
    pub task_id: String,
    #[serde(rename = "WorkflowID")]
    pub workflow_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeGoal {
    #[serde(rename = "ID")]
    pub id: String,
    #[serde(rename = "Objective")]
    pub objective: String,
    #[serde(rename = "Status")]
    pub status: RuntimeGoalStatus,
    #[serde(rename = "Suspended")]
    pub suspended: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub enum RuntimeGoalStatus {
    #[serde(rename = "active")]
    Active,
    #[serde(rename = "paused")]
    Paused,
    #[serde(rename = "complete")]
    Complete,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct UpdateStatus {
    #[serde(rename = "Checked")]
    pub checked: bool,
    #[serde(rename = "Available")]
    pub available: bool,
    #[serde(rename = "CurrentVersion")]
    pub current_version: String,
    #[serde(rename = "LatestVersion")]
    pub latest_version: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RuntimeSessionView {
    #[serde(rename = "SessionID")]
    pub session_id: String,
    #[serde(rename = "SessionName")]
    pub session_name: String,
    #[serde(rename = "ConversationFreshness")]
    pub conversation_freshness: ConversationFreshness,
    #[serde(rename = "ExecutionTarget")]
    pub execution_target: SessionExecutionTarget,
    #[serde(rename = "Transcript")]
    pub transcript: TranscriptMetadata,
    #[serde(rename = "Chat")]
    pub chat: ChatSnapshot,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionExecutionTarget {
    #[serde(rename = "WorkspaceID")]
    pub workspace_id: String,
    #[serde(rename = "WorkspaceName")]
    pub workspace_name: String,
    #[serde(rename = "WorkspaceRoot")]
    pub workspace_root: String,
    #[serde(rename = "WorkspaceAvailability")]
    pub workspace_availability: String,
    #[serde(rename = "WorktreeID")]
    pub worktree_id: String,
    #[serde(rename = "WorktreeName")]
    pub worktree_name: String,
    #[serde(rename = "WorktreeRoot")]
    pub worktree_root: String,
    #[serde(rename = "WorktreeAvailability")]
    pub worktree_availability: String,
    #[serde(rename = "CwdRelpath")]
    pub cwd_relpath: String,
    #[serde(rename = "EffectiveWorkdir")]
    pub effective_workdir: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct TranscriptMetadata {
    #[serde(rename = "Revision")]
    pub revision: i64,
    #[serde(rename = "CommittedEntryCount")]
    pub committed_entry_count: i32,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ChatSnapshot {
    #[serde(rename = "Entries", default, deserialize_with = "null_to_default")]
    pub entries: Vec<ChatEntry>,
    #[serde(rename = "Streaming")]
    pub ongoing: String,
    #[serde(rename = "StreamingError")]
    pub ongoing_error: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RunView {
    #[serde(rename = "RunID")]
    pub run_id: String,
    #[serde(rename = "SessionID")]
    pub session_id: String,
    #[serde(rename = "StepID")]
    pub step_id: String,
    #[serde(rename = "Status")]
    pub status: RunStatus,
    #[serde(rename = "Lifecycle")]
    pub lifecycle: RunLifecycle,
    #[serde(rename = "StartedAt")]
    pub started_at: String,
    #[serde(rename = "FinishedAt")]
    pub finished_at: String,
}
