use serde::{Deserialize, Serialize};

use crate::clientui::SessionExecutionTarget;
use crate::config::null_to_default;

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorktreeView {
    pub worktree_id: String,
    pub display_name: String,
    pub canonical_root: String,
    pub availability: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub branch_ref: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub branch_name: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub detached: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub locked_reason: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub prunable_reason: String,
    #[serde(default, skip_serializing_if = "is_zero")]
    pub dirty_file_count: i32,
    #[serde(default, skip_serializing_if = "is_false")]
    pub is_main: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub is_current: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub builder_managed: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub created_branch: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub origin_session_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorktreeListRequest {
    pub session_id: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub include_dirty_count: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorktreeListResponse {
    pub target: SessionExecutionTarget,
    #[serde(default, deserialize_with = "null_to_default")]
    pub worktrees: Vec<WorktreeView>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorktreeSwitchRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub worktree_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorktreeSwitchResponse {
    pub target: SessionExecutionTarget,
    pub worktree: WorktreeView,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorktreeCreateTargetResolveRequest {
    pub session_id: String,
    pub target: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorktreeCreateTargetResolveResponse {
    pub resolution: WorktreeCreateTargetResolution,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorktreeCreateTargetResolution {
    pub input: String,
    pub kind: WorktreeCreateTargetResolutionKind,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub resolved_ref: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum WorktreeCreateTargetResolutionKind {
    NewBranch,
    ExistingBranch,
    DetachedRef,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorktreeCreateRequest {
    pub client_request_id: String,
    pub session_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub base_ref: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub create_branch: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub branch_name: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub root_path: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorktreeCreateResponse {
    pub target: SessionExecutionTarget,
    pub worktree: WorktreeView,
    pub created_branch: bool,
    pub setup_scheduled: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorktreeDeleteRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub worktree_id: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub delete_branch: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorktreeDeleteResponse {
    pub target: SessionExecutionTarget,
    pub worktree: WorktreeView,
    #[serde(default, skip_serializing_if = "is_false")]
    pub branch_deleted: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub branch_cleanup_message: String,
}

fn is_false(value: &bool) -> bool {
    !*value
}

fn is_zero(value: &i32) -> bool {
    *value == 0
}
