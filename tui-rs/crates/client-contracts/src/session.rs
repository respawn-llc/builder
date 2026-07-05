use serde::{Deserialize, Serialize};

use crate::project::ProjectBinding;

use crate::clientui::{CommittedTranscriptSuffix, RuntimeMainView, TranscriptPage};
use crate::config::{Settings, SourceReport, null_to_default};

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionMainViewRequest {
    #[serde(rename = "SessionID", default)]
    pub session_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionMainViewResponse {
    #[serde(rename = "MainView")]
    pub main_view: RuntimeMainView,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionPlanRequest {
    pub client_request_id: String,
    #[serde(default)]
    pub mode: SessionLaunchMode,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub selected_session_id: String,
    #[serde(skip_serializing_if = "is_false", default)]
    pub force_new_session: bool,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub parent_session_id: String,
    pub overrides: RunPromptOverrides,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum SessionLaunchMode {
    Interactive,
    Headless,
    Unknown(String),
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize, Serialize)]
pub struct RunPromptOverrides {
    #[serde(rename = "AgentRole")]
    pub agent_role: String,
    #[serde(rename = "Model")]
    pub model: String,
    #[serde(rename = "ProviderOverride")]
    pub provider_override: String,
    #[serde(rename = "ThinkingLevel")]
    pub thinking_level: String,
    #[serde(rename = "Theme")]
    pub theme: String,
    #[serde(rename = "ModelTimeoutSeconds")]
    pub model_timeout_seconds: i32,
    #[serde(rename = "Tools")]
    pub tools: String,
    #[serde(rename = "OpenAIBaseURL")]
    pub openai_base_url: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionPlanResponse {
    pub plan: SessionPlan,
    #[serde(default)]
    pub warnings: Vec<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionPlan {
    pub session_id: String,
    pub active_settings: Settings,
    #[serde(default, deserialize_with = "null_to_default")]
    pub enabled_tool_ids: Vec<String>,
    #[serde(default)]
    pub configured_model_name: String,
    #[serde(default)]
    pub session_name: String,
    #[serde(default)]
    pub model_contract_locked: bool,
    #[serde(default)]
    pub workspace_root: String,
    pub source: SourceReport,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionRuntimeActivateRequest {
    pub client_request_id: String,
    pub session_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub owner_id: String,
    pub active_settings: Settings,
    #[serde(default, deserialize_with = "null_to_default")]
    pub enabled_tool_ids: Vec<String>,
    pub source: SourceReport,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionRuntimeActivateResponse {}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionRuntimeReleaseRequest {
    pub client_request_id: String,
    pub session_id: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub only_if_idle: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub drop_owner: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub owner_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionRuntimeReleaseResponse {
    #[serde(default)]
    pub released: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub active: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionRetargetWorkspaceRequest {
    pub client_request_id: String,
    pub session_id: String,
    pub workspace_root: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionRetargetWorkspaceResponse {
    pub binding: ProjectBinding,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionResolveTransitionRequest {
    pub client_request_id: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub session_id: String,
    pub transition: SessionTransition,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionTransition {
    pub action: SessionTransitionAction,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub initial_prompt: String,
    #[serde(skip_serializing_if = "is_false", default)]
    pub initial_prompt_history_recorded: bool,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub initial_input: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub target_session_id: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub fork_rollback_target_id: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub parent_session_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum SessionTransitionAction {
    None,
    NewSession,
    Resume,
    Logout,
    ForkRollback,
    OpenSession,
    Unknown(String),
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionResolveTransitionResponse {
    #[serde(default)]
    pub next_session_id: String,
    #[serde(default)]
    pub initial_prompt: String,
    #[serde(default)]
    pub initial_prompt_history_recorded: bool,
    #[serde(default)]
    pub initial_input: String,
    #[serde(default)]
    pub parent_session_id: String,
    #[serde(default)]
    pub force_new_session: bool,
    #[serde(default)]
    pub should_continue: bool,
    #[serde(default)]
    pub requires_reauth: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionInitialInputRequest {
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub session_id: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub transition_input: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionInitialInputResponse {
    pub input: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionPersistInputDraftRequest {
    pub client_request_id: String,
    pub session_id: String,
    #[serde(skip_serializing_if = "String::is_empty", default)]
    pub input: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionPersistInputDraftResponse {}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionActivitySubscribeRequest {
    #[serde(rename = "SessionID", default)]
    pub session_id: String,
    #[serde(rename = "AfterSequence")]
    pub after_sequence: u64,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionTranscriptPageRequest {
    #[serde(rename = "session_id", default)]
    pub session_id: String,
    #[serde(rename = "cursor", default, skip_serializing_if = "Option::is_none")]
    pub cursor: Option<i64>,
    #[serde(
        rename = "newer_cursor",
        default,
        skip_serializing_if = "Option::is_none"
    )]
    pub newer_cursor: Option<i64>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionTranscriptPageResponse {
    pub transcript: TranscriptPage,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionCommittedTranscriptSuffixRequest {
    #[serde(rename = "session_id", default)]
    pub session_id: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SessionCommittedTranscriptSuffixResponse {
    pub suffix: CommittedTranscriptSuffix,
}

impl Serialize for SessionLaunchMode {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: serde::Serializer,
    {
        serializer.serialize_str(self.as_str())
    }
}

impl<'de> Deserialize<'de> for SessionLaunchMode {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let value = String::deserialize(deserializer)?;
        Ok(Self::from_wire(value))
    }
}

impl Serialize for SessionTransitionAction {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: serde::Serializer,
    {
        serializer.serialize_str(self.as_str())
    }
}

impl<'de> Deserialize<'de> for SessionTransitionAction {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let value = String::deserialize(deserializer)?;
        Ok(Self::from_wire(value))
    }
}

impl SessionLaunchMode {
    fn as_str(&self) -> &str {
        match self {
            Self::Interactive => "interactive",
            Self::Headless => "headless",
            Self::Unknown(value) => value,
        }
    }

    fn from_wire(value: String) -> Self {
        match value.as_str() {
            "interactive" => Self::Interactive,
            "headless" => Self::Headless,
            _ => Self::Unknown(value),
        }
    }
}

impl SessionTransitionAction {
    fn as_str(&self) -> &str {
        match self {
            Self::None => "none",
            Self::NewSession => "new_session",
            Self::Resume => "resume",
            Self::Logout => "logout",
            Self::ForkRollback => "fork_rollback",
            Self::OpenSession => "open_session",
            Self::Unknown(value) => value,
        }
    }

    fn from_wire(value: String) -> Self {
        match value.as_str() {
            "none" => Self::None,
            "new_session" => Self::NewSession,
            "resume" => Self::Resume,
            "logout" => Self::Logout,
            "fork_rollback" => Self::ForkRollback,
            "open_session" => Self::OpenSession,
            _ => Self::Unknown(value),
        }
    }
}

impl Default for SessionLaunchMode {
    fn default() -> Self {
        Self::Unknown(String::new())
    }
}

fn is_false(value: &bool) -> bool {
    !*value
}
