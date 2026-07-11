use serde::{Deserialize, Serialize};

use crate::config::null_to_default;

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct BackgroundProcess {
    #[serde(rename = "ID")]
    pub id: String,
    #[serde(rename = "OwnerSessionID")]
    pub owner_session_id: String,
    #[serde(rename = "OwnerRunID")]
    pub owner_run_id: String,
    #[serde(rename = "OwnerStepID")]
    pub owner_step_id: String,
    #[serde(rename = "State")]
    pub state: String,
    #[serde(rename = "Command")]
    pub command: String,
    #[serde(rename = "Workdir")]
    pub workdir: String,
    #[serde(rename = "StartedAt")]
    pub started_at: String,
    #[serde(rename = "FinishedAt")]
    pub finished_at: String,
    #[serde(rename = "ExitCode")]
    pub exit_code: Option<i32>,
    #[serde(rename = "LogPath")]
    pub log_path: String,
    #[serde(rename = "RecentOutput")]
    pub recent_output: String,
    #[serde(rename = "OutputAvailable")]
    pub output_available: bool,
    #[serde(rename = "OutputRetainedFromBytes")]
    pub output_retained_from_bytes: i64,
    #[serde(rename = "OutputRetainedToBytes")]
    pub output_retained_to_bytes: i64,
    #[serde(rename = "Running")]
    pub running: bool,
    #[serde(rename = "StdinOpen")]
    pub stdin_open: bool,
    #[serde(rename = "Backgrounded")]
    pub backgrounded: bool,
    #[serde(rename = "KillRequested")]
    pub kill_requested: bool,
    #[serde(rename = "LastUpdatedAt")]
    pub last_updated_at: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProcessListRequest {
    #[serde(rename = "OwnerSessionID")]
    pub owner_session_id: String,
    #[serde(rename = "OwnerRunID")]
    pub owner_run_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProcessListResponse {
    #[serde(rename = "Processes", default, deserialize_with = "null_to_default")]
    pub processes: Vec<BackgroundProcess>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProcessKillRequest {
    #[serde(rename = "ClientRequestID")]
    pub client_request_id: String,
    #[serde(rename = "ProcessID")]
    pub process_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProcessKillResponse {}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProcessInlineOutputRequest {
    #[serde(rename = "ProcessID")]
    pub process_id: String,
    #[serde(rename = "MaxChars")]
    pub max_chars: i32,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProcessInlineOutputResponse {
    #[serde(rename = "Output")]
    pub output: String,
    #[serde(rename = "LogPath")]
    pub log_path: String,
}
