use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct Settings {
    #[serde(rename = "Model")]
    pub model: String,
    #[serde(rename = "ThinkingLevel")]
    pub thinking_level: String,
    #[serde(rename = "ModelVerbosity")]
    pub model_verbosity: String,
    #[serde(rename = "SystemPromptFile")]
    pub system_prompt_file: String,
    #[serde(
        rename = "SystemPromptFiles",
        default,
        deserialize_with = "null_to_default"
    )]
    pub system_prompt_files: Vec<SystemPromptFile>,
    #[serde(rename = "ModelCapabilities")]
    pub model_capabilities: ModelCapabilitiesOverride,
    #[serde(rename = "Theme")]
    pub theme: String,
    #[serde(rename = "NotificationMethod")]
    pub notification_method: String,
    #[serde(rename = "ToolPreambles")]
    pub tool_preambles: bool,
    #[serde(rename = "PriorityRequestMode")]
    pub priority_request_mode: bool,
    #[serde(rename = "Debug")]
    pub debug: bool,
    #[serde(rename = "ServerHost")]
    pub server_host: String,
    #[serde(rename = "ServerPort")]
    pub server_port: i32,
    #[serde(rename = "WebSearch")]
    pub web_search: String,
    #[serde(rename = "ProviderOverride")]
    pub provider_override: String,
    #[serde(rename = "OpenAIBaseURL")]
    pub openai_base_url: String,
    #[serde(rename = "ProviderCapabilities")]
    pub provider_capabilities: ProviderCapabilitiesOverride,
    #[serde(rename = "Store")]
    pub store: bool,
    #[serde(rename = "AllowNonCwdEdits")]
    pub allow_non_cwd_edits: bool,
    #[serde(rename = "ModelContextWindow")]
    pub model_context_window: i32,
    #[serde(rename = "ContextCompactionThresholdTokens")]
    pub context_compaction_threshold_tokens: i32,
    #[serde(rename = "PreSubmitCompactionLeadTokens")]
    pub pre_submit_compaction_lead_tokens: i32,
    #[serde(rename = "MinimumExecToBgSeconds")]
    pub minimum_exec_to_bg_seconds: i32,
    #[serde(rename = "CompactionMode")]
    pub compaction_mode: String,
    #[serde(rename = "EnabledTools", default, deserialize_with = "null_to_default")]
    pub enabled_tools: BTreeMap<String, bool>,
    #[serde(rename = "SkillToggles", default, deserialize_with = "null_to_default")]
    pub skill_toggles: BTreeMap<String, bool>,
    #[serde(rename = "Timeouts")]
    pub timeouts: Timeouts,
    #[serde(rename = "ShellOutputMaxChars")]
    pub shell_output_max_chars: i32,
    #[serde(rename = "BGShellsOutput")]
    pub bg_shells_output: String,
    #[serde(rename = "Shell")]
    pub shell: ShellSettings,
    #[serde(rename = "CacheWarningMode")]
    pub cache_warning_mode: String,
    #[serde(rename = "Worktrees")]
    pub worktrees: WorktreeSettings,
    #[serde(rename = "Workflow")]
    pub workflow: WorkflowSettings,
    #[serde(rename = "Reviewer")]
    pub reviewer: ReviewerSettings,
    #[serde(rename = "Subagents", default, deserialize_with = "null_to_default")]
    pub subagents: BTreeMap<String, SubagentRole>,
    #[serde(rename = "PreventSleep")]
    pub prevent_sleep: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SystemPromptFile {
    #[serde(rename = "Path")]
    pub path: String,
    #[serde(rename = "Scope")]
    pub scope: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ModelCapabilitiesOverride {
    #[serde(rename = "SupportsReasoningEffort")]
    pub supports_reasoning_effort: bool,
    #[serde(rename = "SupportsVisionInputs")]
    pub supports_vision_inputs: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ProviderCapabilitiesOverride {
    #[serde(rename = "ProviderID")]
    pub provider_id: String,
    #[serde(rename = "SupportsResponsesAPI")]
    pub supports_responses_api: bool,
    #[serde(rename = "SupportsResponsesCompact")]
    pub supports_responses_compact: bool,
    #[serde(rename = "SupportsRequestInputTokenCount")]
    pub supports_request_input_token_count: bool,
    #[serde(rename = "SupportsPromptCacheKey")]
    pub supports_prompt_cache_key: bool,
    #[serde(rename = "SupportsNativeWebSearch")]
    pub supports_native_web_search: bool,
    #[serde(rename = "SupportsReasoningEncrypted")]
    pub supports_reasoning_encrypted: bool,
    #[serde(rename = "SupportsServerSideContextEdit")]
    pub supports_server_side_context_edit: bool,
    #[serde(rename = "IsOpenAIFirstParty")]
    pub is_openai_first_party: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct Timeouts {
    #[serde(rename = "ModelRequestSeconds")]
    pub model_request_seconds: i32,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ShellSettings {
    #[serde(rename = "PostprocessingMode")]
    pub postprocessing_mode: String,
    #[serde(rename = "PostprocessHook")]
    pub postprocess_hook: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorktreeSettings {
    #[serde(rename = "BaseDir")]
    pub base_dir: String,
    #[serde(rename = "SetupScript")]
    pub setup_script: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct WorkflowSettings {
    #[serde(rename = "CompletionMode")]
    pub completion_mode: String,
    #[serde(rename = "Concurrency")]
    pub concurrency: i32,
    #[serde(
        rename = "MaxFinalAnswerViolations",
        default,
        skip_serializing_if = "is_zero"
    )]
    pub max_final_answer_violations: i32,
    #[serde(rename = "MaxInvalidCompletionAttempts")]
    pub max_invalid_completion_attempts: i32,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct ReviewerSettings {
    #[serde(rename = "Frequency")]
    pub frequency: String,
    #[serde(rename = "Model")]
    pub model: String,
    #[serde(rename = "ThinkingLevel")]
    pub thinking_level: String,
    #[serde(rename = "ModelVerbosity")]
    pub model_verbosity: String,
    #[serde(rename = "ProviderOverride")]
    pub provider_override: String,
    #[serde(rename = "OpenAIBaseURL")]
    pub openai_base_url: String,
    #[serde(rename = "ModelCapabilities")]
    pub model_capabilities: ModelCapabilitiesOverride,
    #[serde(rename = "ProviderCapabilities")]
    pub provider_capabilities: ProviderCapabilitiesOverride,
    #[serde(rename = "ModelContextWindow")]
    pub model_context_window: i32,
    #[serde(rename = "Auth")]
    pub auth: String,
    #[serde(rename = "SystemPromptFile")]
    pub system_prompt_file: String,
    #[serde(rename = "TimeoutSeconds")]
    pub timeout_seconds: i32,
    #[serde(rename = "VerboseOutput")]
    pub verbose_output: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SubagentRole {
    #[serde(rename = "Settings")]
    pub settings: Box<Settings>,
    #[serde(rename = "Sources", default)]
    pub sources: BTreeMap<String, String>,
    #[serde(rename = "Description")]
    pub description: String,
    #[serde(rename = "AgentCallable")]
    pub agent_callable: bool,
    #[serde(rename = "AgentCallableSet")]
    pub agent_callable_set: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct SourceReport {
    #[serde(rename = "SettingsPath")]
    pub settings_path: String,
    #[serde(rename = "SettingsFileExists")]
    pub settings_file_exists: bool,
    #[serde(rename = "CreatedDefaultConfig")]
    pub created_default_config: bool,
    #[serde(rename = "HomeSettingsPath")]
    pub home_settings_path: String,
    #[serde(rename = "HomeSettingsFileExists")]
    pub home_settings_file_exists: bool,
    #[serde(rename = "WorkspaceSettingsPath")]
    pub workspace_settings_path: String,
    #[serde(rename = "WorkspaceSettingsFileExists")]
    pub workspace_settings_file_exists: bool,
    #[serde(rename = "WorkspaceSettingsLayerEnabled")]
    pub workspace_settings_layer_enabled: bool,
    #[serde(rename = "Sources", default, deserialize_with = "null_to_default")]
    pub sources: BTreeMap<String, String>,
}

pub(crate) fn null_to_default<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de> + Default,
{
    Ok(Option::<T>::deserialize(deserializer)?.unwrap_or_default())
}

fn is_zero(value: &i32) -> bool {
    *value == 0
}
