use serde::{Deserialize, Serialize};

use crate::clientui::{Event, PendingPromptEvent};

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct HandshakeRequest {
    #[serde(default)]
    pub protocol_version: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct HandshakeResponse {
    pub identity: ServerIdentity,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ServerIdentity {
    pub protocol_version: String,
    pub server_id: String,
    pub pid: i32,
    pub capabilities: CapabilityFlags,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub persistence_root_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct CapabilityFlags {
    pub jsonrpc_websocket: bool,
    pub auth_bootstrap: bool,
    pub project_attach: bool,
    pub session_attach: bool,
    pub health_endpoint: bool,
    pub readiness_endpoint: bool,
    pub run_prompt: bool,
    pub session_plan: bool,
    pub session_lifecycle: bool,
    pub session_transcript_paging: bool,
    pub session_runtime: bool,
    pub runtime_control: bool,
    pub prompt_control: bool,
    pub prompt_activity: bool,
    pub session_activity: bool,
    pub process_output: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct AttachProjectRequest {
    #[serde(default)]
    pub project_id: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub workspace_id: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub workspace_root: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct AttachSessionRequest {
    #[serde(default)]
    pub session_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct AttachResponse {
    pub kind: String,
    #[serde(default)]
    pub project_id: String,
    #[serde(default)]
    pub workspace_id: String,
    #[serde(default)]
    pub workspace_root: String,
    #[serde(default)]
    pub session_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SubscribeResponse {
    pub stream: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionActivityEventParams {
    pub event: Event,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct PromptActivityEventParams {
    pub event: PendingPromptEvent,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct StreamCompleteParams {
    #[serde(default)]
    pub code: i32,
    #[serde(default)]
    pub message: String,
}
