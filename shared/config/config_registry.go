package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"core/shared/theme"
	"core/shared/toolspec"
)

type settingsState struct {
	Settings        Settings
	PersistenceRoot string
}

type settingsValidator func(settingsState, map[string]string) error

type defaultConfigLine struct {
	Path      []string
	Value     any
	Commented bool
}

type registrySetting interface {
	applyDefault(*settingsState)
	applyFile(settingsFile, string, *settingsState, map[string]string) error
}

type sourceInitializingSetting interface {
	initSources(map[string]string)
}

type envApplyingSetting interface {
	applyEnv(envLookup, *settingsState, map[string]string) error
}

type cliApplyingSetting interface {
	applyCLI(LoadOptions, *settingsState, map[string]string) error
}

type fileKeyRegisteringSetting interface {
	registerFileKeys(*fileKeyTree)
}

type defaultLineAppendingSetting interface {
	appendDefaultLines(*[]defaultConfigLine, settingsState)
}

type fileKeyTree struct {
	children        map[string]*fileKeyTree
	dynamicChildren func(string) bool
	dynamicTemplate *fileKeyTree
}

type settingsRegistry struct {
	settings   []registrySetting
	validators []settingsValidator
	fileKeys   *fileKeyTree
}

type settingDocOptions struct {
	commented                    bool
	omitInTOML                   bool
	resolveRelativeToSettingsDir bool
	defaultValue                 func(settingsState) any
	allowEmptyString             bool
}

type scalarSetting[T any] struct {
	key                string
	defaultValue       T
	apply              func(*settingsState, T)
	get                func(settingsState) T
	decodeFile         func(settingsFile, []string) (T, bool, error)
	transformFileValue func(T, string) (T, error)
	envName            string
	decodeEnv          func(string, string) (T, error)
	decodeCLI          func(LoadOptions) (T, bool, error)
	doc                settingDocOptions
}

type toolsSetting struct{}
type skillsSetting struct{}
type subagentsSetting struct{}

var configRegistry = newSettingsRegistry()

func newSettingsRegistry() settingsRegistry {
	settings := []registrySetting{
		newStringSetting("model", defaultModel,
			func(state *settingsState, value string) { state.Settings.Model = value },
			func(state settingsState) string { return state.Settings.Model },
			"KENT_MODEL",
			func(opts LoadOptions) (string, bool, error) { return trimmedCLIString(opts.Model) },
			nil,
			settingDocOptions{}),
		newStringSetting("thinking_level", defaultThinkingLevel,
			func(state *settingsState, value string) { state.Settings.ThinkingLevel = value },
			func(state settingsState) string { return state.Settings.ThinkingLevel },
			"KENT_THINKING_LEVEL",
			func(opts LoadOptions) (string, bool, error) { return trimmedCLIString(opts.ThinkingLevel) },
			nil,
			settingDocOptions{allowEmptyString: true}),
		newStringSetting("model_verbosity", defaultModelVerbosity,
			func(state *settingsState, value ModelVerbosity) { state.Settings.ModelVerbosity = value },
			func(state settingsState) ModelVerbosity { return state.Settings.ModelVerbosity },
			"",
			nil,
			normalizeModelVerbosity,
			settingDocOptions{}),
		newStringSetting("system_prompt_file", "",
			func(state *settingsState, value string) { state.Settings.SystemPromptFile = value },
			func(state settingsState) string { return state.Settings.SystemPromptFile },
			"",
			nil,
			nil,
			settingDocOptions{commented: true}),
		newBoolSetting("model_capabilities.supports_reasoning_effort", false,
			func(state *settingsState, value bool) {
				state.Settings.ModelCapabilities.SupportsReasoningEffort = value
			},
			func(state settingsState) bool { return state.Settings.ModelCapabilities.SupportsReasoningEffort },
			"KENT_MODEL_CAPABILITIES_SUPPORTS_REASONING_EFFORT",
			settingDocOptions{commented: true}),
		newBoolSetting("model_capabilities.supports_vision_inputs", false,
			func(state *settingsState, value bool) { state.Settings.ModelCapabilities.SupportsVisionInputs = value },
			func(state settingsState) bool { return state.Settings.ModelCapabilities.SupportsVisionInputs },
			"KENT_MODEL_CAPABILITIES_SUPPORTS_VISION_INPUTS",
			settingDocOptions{commented: true}),
		newStringSetting("theme", defaultTheme,
			func(state *settingsState, value string) { state.Settings.Theme = value },
			func(state settingsState) string { return state.Settings.Theme },
			"KENT_THEME",
			func(opts LoadOptions) (string, bool, error) { return trimmedCLIString(opts.Theme) },
			theme.Normalize,
			settingDocOptions{}),
		newStringSetting("notification_method", "auto",
			func(state *settingsState, value string) { state.Settings.NotificationMethod = value },
			func(state settingsState) string { return state.Settings.NotificationMethod },
			"KENT_NOTIFICATION_METHOD",
			nil,
			nil,
			settingDocOptions{}),
		newBoolSetting("tool_preambles", true,
			func(state *settingsState, value bool) { state.Settings.ToolPreambles = value },
			func(state settingsState) bool { return state.Settings.ToolPreambles },
			"KENT_TOOL_PREAMBLES",
			settingDocOptions{}),
		newBoolSetting("priority_request_mode", false,
			func(state *settingsState, value bool) { state.Settings.PriorityRequestMode = value },
			func(state settingsState) bool { return state.Settings.PriorityRequestMode },
			"",
			settingDocOptions{}),
		newBoolSetting("debug", false,
			func(state *settingsState, value bool) { state.Settings.Debug = value },
			func(state settingsState) bool { return state.Settings.Debug },
			"KENT_DEBUG",
			settingDocOptions{}),
		newStringSetting("server_host", defaultServerHost,
			func(state *settingsState, value string) { state.Settings.ServerHost = value },
			func(state settingsState) string { return state.Settings.ServerHost },
			"KENT_SERVER_HOST",
			nil,
			nil,
			settingDocOptions{}),
		newIntSetting("server_port", defaultServerPort,
			func(state *settingsState, value int) { state.Settings.ServerPort = value },
			func(state settingsState) int { return state.Settings.ServerPort },
			"KENT_SERVER_PORT",
			nil,
			settingDocOptions{}),
		newStringSetting("web_search", "native",
			func(state *settingsState, value string) { state.Settings.WebSearch = value },
			func(state settingsState) string { return state.Settings.WebSearch },
			"KENT_WEB_SEARCH",
			nil,
			nil,
			settingDocOptions{}),
		newStringSetting("provider_override", "",
			func(state *settingsState, value string) { state.Settings.ProviderOverride = value },
			func(state settingsState) string { return state.Settings.ProviderOverride },
			"KENT_PROVIDER_OVERRIDE",
			func(opts LoadOptions) (string, bool, error) { return trimmedCLIString(opts.ProviderOverride) },
			func(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) },
			settingDocOptions{}),
		newStringSetting("openai_base_url", "",
			func(state *settingsState, value string) { state.Settings.OpenAIBaseURL = value },
			func(state settingsState) string { return state.Settings.OpenAIBaseURL },
			"KENT_OPENAI_BASE_URL",
			func(opts LoadOptions) (string, bool, error) { return trimmedCLIString(opts.OpenAIBaseURL) },
			nil,
			settingDocOptions{}),
		newStringSetting("provider_capabilities.provider_id", "",
			func(state *settingsState, value string) { state.Settings.ProviderCapabilities.ProviderID = value },
			func(state settingsState) string { return state.Settings.ProviderCapabilities.ProviderID },
			"KENT_PROVIDER_CAPABILITIES_PROVIDER_ID",
			nil,
			nil,
			settingDocOptions{commented: true}),
		newBoolSetting("provider_capabilities.supports_responses_api", false,
			func(state *settingsState, value bool) {
				state.Settings.ProviderCapabilities.SupportsResponsesAPI = value
			},
			func(state settingsState) bool { return state.Settings.ProviderCapabilities.SupportsResponsesAPI },
			"KENT_PROVIDER_CAPABILITIES_SUPPORTS_RESPONSES_API",
			settingDocOptions{commented: true}),
		newBoolSetting("provider_capabilities.supports_responses_compact", false,
			func(state *settingsState, value bool) {
				state.Settings.ProviderCapabilities.SupportsResponsesCompact = value
			},
			func(state settingsState) bool { return state.Settings.ProviderCapabilities.SupportsResponsesCompact },
			"KENT_PROVIDER_CAPABILITIES_SUPPORTS_RESPONSES_COMPACT",
			settingDocOptions{commented: true}),
		newBoolSetting("provider_capabilities.supports_request_input_token_count", false,
			func(state *settingsState, value bool) {
				state.Settings.ProviderCapabilities.SupportsRequestInputTokenCount = value
			},
			func(state settingsState) bool {
				return state.Settings.ProviderCapabilities.SupportsRequestInputTokenCount
			},
			"KENT_PROVIDER_CAPABILITIES_SUPPORTS_REQUEST_INPUT_TOKEN_COUNT",
			settingDocOptions{commented: true}),
		newBoolSetting("provider_capabilities.supports_prompt_cache_key", false,
			func(state *settingsState, value bool) {
				state.Settings.ProviderCapabilities.SupportsPromptCacheKey = value
			},
			func(state settingsState) bool { return state.Settings.ProviderCapabilities.SupportsPromptCacheKey },
			"KENT_PROVIDER_CAPABILITIES_SUPPORTS_PROMPT_CACHE_KEY",
			settingDocOptions{commented: true}),
		newBoolSetting("provider_capabilities.supports_native_web_search", false,
			func(state *settingsState, value bool) {
				state.Settings.ProviderCapabilities.SupportsNativeWebSearch = value
			},
			func(state settingsState) bool { return state.Settings.ProviderCapabilities.SupportsNativeWebSearch },
			"KENT_PROVIDER_CAPABILITIES_SUPPORTS_NATIVE_WEB_SEARCH",
			settingDocOptions{commented: true}),
		newBoolSetting("provider_capabilities.supports_reasoning_encrypted", false,
			func(state *settingsState, value bool) {
				state.Settings.ProviderCapabilities.SupportsReasoningEncrypted = value
			},
			func(state settingsState) bool { return state.Settings.ProviderCapabilities.SupportsReasoningEncrypted },
			"KENT_PROVIDER_CAPABILITIES_SUPPORTS_REASONING_ENCRYPTED",
			settingDocOptions{commented: true}),
		newBoolSetting("provider_capabilities.supports_server_side_context_edit", false,
			func(state *settingsState, value bool) {
				state.Settings.ProviderCapabilities.SupportsServerSideContextEdit = value
			},
			func(state settingsState) bool {
				return state.Settings.ProviderCapabilities.SupportsServerSideContextEdit
			},
			"KENT_PROVIDER_CAPABILITIES_SUPPORTS_SERVER_SIDE_CONTEXT_EDIT",
			settingDocOptions{commented: true}),
		newBoolSetting("provider_capabilities.is_openai_first_party", false,
			func(state *settingsState, value bool) { state.Settings.ProviderCapabilities.IsOpenAIFirstParty = value },
			func(state settingsState) bool { return state.Settings.ProviderCapabilities.IsOpenAIFirstParty },
			"KENT_PROVIDER_CAPABILITIES_IS_OPENAI_FIRST_PARTY",
			settingDocOptions{commented: true}),
		newBoolSetting("store", false,
			func(state *settingsState, value bool) { state.Settings.Store = value },
			func(state settingsState) bool { return state.Settings.Store },
			"KENT_STORE",
			settingDocOptions{}),
		newBoolSetting("allow_non_cwd_edits", false,
			func(state *settingsState, value bool) { state.Settings.AllowNonCwdEdits = value },
			func(state settingsState) bool { return state.Settings.AllowNonCwdEdits },
			"KENT_ALLOW_NON_CWD_EDITS",
			settingDocOptions{}),
		newIntSetting("model_context_window", defaultModelContextWindow,
			func(state *settingsState, value int) { state.Settings.ModelContextWindow = value },
			func(state settingsState) int { return state.Settings.ModelContextWindow },
			"KENT_MODEL_CONTEXT_WINDOW",
			nil,
			settingDocOptions{}),
		newIntSetting("context_compaction_threshold_tokens", defaultCompactionThreshold,
			func(state *settingsState, value int) { state.Settings.ContextCompactionThresholdTokens = value },
			func(state settingsState) int { return state.Settings.ContextCompactionThresholdTokens },
			"KENT_CONTEXT_COMPACTION_THRESHOLD_TOKENS",
			nil,
			settingDocOptions{}),
		newIntSetting("pre_submit_compaction_lead_tokens", defaultPreSubmitCompactionLeadTokens,
			func(state *settingsState, value int) { state.Settings.PreSubmitCompactionLeadTokens = value },
			func(state settingsState) int { return state.Settings.PreSubmitCompactionLeadTokens },
			"KENT_PRE_SUBMIT_COMPACTION_LEAD_TOKENS",
			nil,
			settingDocOptions{}),
		newIntSetting("minimum_exec_to_bg_seconds", defaultMinimumExecToBgSec,
			func(state *settingsState, value int) { state.Settings.MinimumExecToBgSeconds = value },
			func(state settingsState) int { return state.Settings.MinimumExecToBgSeconds },
			"KENT_MINIMUM_EXEC_TO_BG_SECONDS",
			nil,
			settingDocOptions{}),
		newStringSetting("compaction_mode", CompactionMode(defaultCompactionMode),
			func(state *settingsState, value CompactionMode) { state.Settings.CompactionMode = value },
			func(state settingsState) CompactionMode { return state.Settings.CompactionMode },
			"KENT_COMPACTION_MODE",
			nil,
			normalizeCompactionMode,
			settingDocOptions{}),
		toolsSetting{},
		skillsSetting{},
		newIntSetting("timeouts.model_request_seconds", defaultModelTimeoutSeconds,
			func(state *settingsState, value int) { state.Settings.Timeouts.ModelRequestSeconds = value },
			func(state settingsState) int { return state.Settings.Timeouts.ModelRequestSeconds },
			"KENT_TIMEOUTS_MODEL_REQUEST_SECONDS",
			func(opts LoadOptions) (int, bool, error) {
				if opts.ModelTimeoutSeconds <= 0 {
					return 0, false, nil
				}
				return opts.ModelTimeoutSeconds, true, nil
			},
			settingDocOptions{}),
		newIntSetting("shell_output_max_chars", defaultShellOutputMaxChars,
			func(state *settingsState, value int) { state.Settings.ShellOutputMaxChars = value },
			func(state settingsState) int { return state.Settings.ShellOutputMaxChars },
			"KENT_SHELL_OUTPUT_MAX_CHARS",
			nil,
			settingDocOptions{}),
		newStringSetting("bg_shells_output", BGShellsOutputMode(defaultBGShellsOutput),
			func(state *settingsState, value BGShellsOutputMode) { state.Settings.BGShellsOutput = value },
			func(state settingsState) BGShellsOutputMode { return state.Settings.BGShellsOutput },
			"KENT_BG_SHELLS_OUTPUT",
			nil,
			nil,
			settingDocOptions{}),
		newStringSetting("shell.postprocessing_mode", ShellPostprocessingMode(defaultShellPostprocessingMode),
			func(state *settingsState, value ShellPostprocessingMode) {
				state.Settings.Shell.PostprocessingMode = value
			},
			func(state settingsState) ShellPostprocessingMode { return state.Settings.Shell.PostprocessingMode },
			"KENT_SHELL_POSTPROCESSING_MODE",
			nil,
			normalizeShellPostprocessingMode,
			settingDocOptions{}),
		newStringSetting("shell.postprocess_hook", "",
			func(state *settingsState, value string) { state.Settings.Shell.PostprocessHook = value },
			func(state settingsState) string { return state.Settings.Shell.PostprocessHook },
			"KENT_SHELL_POSTPROCESS_HOOK",
			nil,
			nil,
			settingDocOptions{}),
		newStringSetting("cache_warning_mode", CacheWarningMode(defaultCacheWarningMode),
			func(state *settingsState, value CacheWarningMode) { state.Settings.CacheWarningMode = value },
			func(state settingsState) CacheWarningMode { return state.Settings.CacheWarningMode },
			"KENT_CACHE_WARNING_MODE",
			nil,
			normalizeCacheWarningMode,
			settingDocOptions{}),
		newStringSetting("worktrees.base_dir", "",
			func(state *settingsState, value string) { state.Settings.Worktrees.BaseDir = value },
			func(state settingsState) string { return state.Settings.Worktrees.BaseDir },
			"",
			nil,
			nil,
			settingDocOptions{defaultValue: func(state settingsState) any {
				base := strings.TrimSpace(state.PersistenceRoot)
				if base == "" {
					base = DefaultPersistence
				}
				return filepath.Join(base, "worktrees")
			}}),
		newStringSetting("worktrees.setup_script", "",
			func(state *settingsState, value string) { state.Settings.Worktrees.SetupScript = value },
			func(state settingsState) string { return state.Settings.Worktrees.SetupScript },
			"",
			nil,
			nil,
			settingDocOptions{}),
		newStringSetting("workflow.completion_mode", defaultWorkflowCompletionMode,
			func(state *settingsState, value WorkflowCompletionMode) {
				state.Settings.Workflow.CompletionMode = value
			},
			func(state settingsState) WorkflowCompletionMode { return state.Settings.Workflow.CompletionMode },
			"KENT_WORKFLOW_COMPLETION_MODE",
			nil,
			func(raw string) WorkflowCompletionMode {
				return WorkflowCompletionMode(strings.ToLower(strings.TrimSpace(raw)))
			},
			settingDocOptions{}),
		newIntSetting("workflow.concurrency", defaultWorkflowConcurrency,
			func(state *settingsState, value int) { state.Settings.Workflow.Concurrency = value },
			func(state settingsState) int { return state.Settings.Workflow.Concurrency },
			"KENT_WORKFLOW_CONCURRENCY",
			nil,
			settingDocOptions{}),
		newIntSetting("workflow.max_invalid_completion_attempts", defaultWorkflowInvalidCompletionCap,
			func(state *settingsState, value int) { state.Settings.Workflow.MaxInvalidCompletionAttempts = value },
			func(state settingsState) int { return state.Settings.Workflow.MaxInvalidCompletionAttempts },
			"KENT_WORKFLOW_MAX_INVALID_COMPLETION_ATTEMPTS",
			nil,
			settingDocOptions{}),
		newStringSetting("reviewer.frequency", defaultReviewerFrequency,
			func(state *settingsState, value string) { state.Settings.Reviewer.Frequency = value },
			func(state settingsState) string { return state.Settings.Reviewer.Frequency },
			"KENT_REVIEWER_FREQUENCY",
			nil,
			nil,
			settingDocOptions{}),
		newStringSetting("reviewer.model", "",
			func(state *settingsState, value string) { state.Settings.Reviewer.Model = value },
			func(state settingsState) string { return state.Settings.Reviewer.Model },
			"KENT_REVIEWER_MODEL",
			nil,
			nil,
			settingDocOptions{
				omitInTOML: true,
				defaultValue: func(settingsState) any {
					return "<inherits model when unset>"
				},
			}),
		newStringSetting("reviewer.thinking_level", "",
			func(state *settingsState, value string) { state.Settings.Reviewer.ThinkingLevel = value },
			func(state settingsState) string { return state.Settings.Reviewer.ThinkingLevel },
			"KENT_REVIEWER_THINKING_LEVEL",
			nil,
			nil,
			settingDocOptions{
				omitInTOML:       true,
				allowEmptyString: true,
				defaultValue: func(settingsState) any {
					return "<inherits thinking_level when unset>"
				},
			}),
		newStringSetting("reviewer.model_verbosity", "",
			func(state *settingsState, value ModelVerbosity) { state.Settings.Reviewer.ModelVerbosity = value },
			func(state settingsState) ModelVerbosity { return state.Settings.Reviewer.ModelVerbosity },
			"KENT_REVIEWER_MODEL_VERBOSITY",
			nil,
			normalizeModelVerbosity,
			settingDocOptions{
				omitInTOML: true,
				defaultValue: func(settingsState) any {
					return "<inherits model_verbosity when unset>"
				},
			}),
		newStringSetting("reviewer.provider_override", "",
			func(state *settingsState, value string) { state.Settings.Reviewer.ProviderOverride = value },
			func(state settingsState) string { return state.Settings.Reviewer.ProviderOverride },
			"KENT_REVIEWER_PROVIDER_OVERRIDE",
			nil,
			func(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) },
			settingDocOptions{
				omitInTOML: true,
				defaultValue: func(settingsState) any {
					return "<inherits provider_override when unset>"
				},
			}),
		newStringSetting("reviewer.openai_base_url", "",
			func(state *settingsState, value string) { state.Settings.Reviewer.OpenAIBaseURL = value },
			func(state settingsState) string { return state.Settings.Reviewer.OpenAIBaseURL },
			"KENT_REVIEWER_OPENAI_BASE_URL",
			nil,
			nil,
			settingDocOptions{
				omitInTOML: true,
				defaultValue: func(settingsState) any {
					return "<inherits openai_base_url when unset>"
				},
			}),
		newBoolSetting("reviewer.model_capabilities.supports_reasoning_effort", false,
			func(state *settingsState, value bool) {
				state.Settings.Reviewer.ModelCapabilities.SupportsReasoningEffort = value
			},
			func(state settingsState) bool {
				return state.Settings.Reviewer.ModelCapabilities.SupportsReasoningEffort
			},
			"KENT_REVIEWER_MODEL_CAPABILITIES_SUPPORTS_REASONING_EFFORT",
			settingDocOptions{commented: true}),
		newBoolSetting("reviewer.model_capabilities.supports_vision_inputs", false,
			func(state *settingsState, value bool) {
				state.Settings.Reviewer.ModelCapabilities.SupportsVisionInputs = value
			},
			func(state settingsState) bool { return state.Settings.Reviewer.ModelCapabilities.SupportsVisionInputs },
			"KENT_REVIEWER_MODEL_CAPABILITIES_SUPPORTS_VISION_INPUTS",
			settingDocOptions{commented: true}),
		newStringSetting("reviewer.provider_capabilities.provider_id", "",
			func(state *settingsState, value string) {
				state.Settings.Reviewer.ProviderCapabilities.ProviderID = value
			},
			func(state settingsState) string { return state.Settings.Reviewer.ProviderCapabilities.ProviderID },
			"KENT_REVIEWER_PROVIDER_CAPABILITIES_PROVIDER_ID",
			nil,
			nil,
			settingDocOptions{commented: true}),
		newBoolSetting("reviewer.provider_capabilities.supports_responses_api", false,
			func(state *settingsState, value bool) {
				state.Settings.Reviewer.ProviderCapabilities.SupportsResponsesAPI = value
			},
			func(state settingsState) bool {
				return state.Settings.Reviewer.ProviderCapabilities.SupportsResponsesAPI
			},
			"KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_RESPONSES_API",
			settingDocOptions{commented: true}),
		newBoolSetting("reviewer.provider_capabilities.supports_responses_compact", false,
			func(state *settingsState, value bool) {
				state.Settings.Reviewer.ProviderCapabilities.SupportsResponsesCompact = value
			},
			func(state settingsState) bool {
				return state.Settings.Reviewer.ProviderCapabilities.SupportsResponsesCompact
			},
			"KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_RESPONSES_COMPACT",
			settingDocOptions{commented: true}),
		newBoolSetting("reviewer.provider_capabilities.supports_request_input_token_count", false,
			func(state *settingsState, value bool) {
				state.Settings.Reviewer.ProviderCapabilities.SupportsRequestInputTokenCount = value
			},
			func(state settingsState) bool {
				return state.Settings.Reviewer.ProviderCapabilities.SupportsRequestInputTokenCount
			},
			"KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_REQUEST_INPUT_TOKEN_COUNT",
			settingDocOptions{commented: true}),
		newBoolSetting("reviewer.provider_capabilities.supports_prompt_cache_key", false,
			func(state *settingsState, value bool) {
				state.Settings.Reviewer.ProviderCapabilities.SupportsPromptCacheKey = value
			},
			func(state settingsState) bool {
				return state.Settings.Reviewer.ProviderCapabilities.SupportsPromptCacheKey
			},
			"KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_PROMPT_CACHE_KEY",
			settingDocOptions{commented: true}),
		newBoolSetting("reviewer.provider_capabilities.supports_native_web_search", false,
			func(state *settingsState, value bool) {
				state.Settings.Reviewer.ProviderCapabilities.SupportsNativeWebSearch = value
			},
			func(state settingsState) bool {
				return state.Settings.Reviewer.ProviderCapabilities.SupportsNativeWebSearch
			},
			"KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_NATIVE_WEB_SEARCH",
			settingDocOptions{commented: true}),
		newBoolSetting("reviewer.provider_capabilities.supports_reasoning_encrypted", false,
			func(state *settingsState, value bool) {
				state.Settings.Reviewer.ProviderCapabilities.SupportsReasoningEncrypted = value
			},
			func(state settingsState) bool {
				return state.Settings.Reviewer.ProviderCapabilities.SupportsReasoningEncrypted
			},
			"KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_REASONING_ENCRYPTED",
			settingDocOptions{commented: true}),
		newBoolSetting("reviewer.provider_capabilities.supports_server_side_context_edit", false,
			func(state *settingsState, value bool) {
				state.Settings.Reviewer.ProviderCapabilities.SupportsServerSideContextEdit = value
			},
			func(state settingsState) bool {
				return state.Settings.Reviewer.ProviderCapabilities.SupportsServerSideContextEdit
			},
			"KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_SERVER_SIDE_CONTEXT_EDIT",
			settingDocOptions{commented: true}),
		newBoolSetting("reviewer.provider_capabilities.is_openai_first_party", false,
			func(state *settingsState, value bool) {
				state.Settings.Reviewer.ProviderCapabilities.IsOpenAIFirstParty = value
			},
			func(state settingsState) bool { return state.Settings.Reviewer.ProviderCapabilities.IsOpenAIFirstParty },
			"KENT_REVIEWER_PROVIDER_CAPABILITIES_IS_OPENAI_FIRST_PARTY",
			settingDocOptions{commented: true}),
		newIntSetting("reviewer.model_context_window", 0,
			func(state *settingsState, value int) { state.Settings.Reviewer.ModelContextWindow = value },
			func(state settingsState) int { return state.Settings.Reviewer.ModelContextWindow },
			"KENT_REVIEWER_MODEL_CONTEXT_WINDOW",
			nil,
			settingDocOptions{
				omitInTOML: true,
				defaultValue: func(settingsState) any {
					return "<inherits model_context_window when unset>"
				},
			}),
		newStringSetting("reviewer.auth", "inherit",
			func(state *settingsState, value string) { state.Settings.Reviewer.Auth = value },
			func(state settingsState) string { return state.Settings.Reviewer.Auth },
			"KENT_REVIEWER_AUTH",
			nil,
			normalizeReviewerAuth,
			settingDocOptions{}),
		newStringSetting("reviewer.system_prompt_file", "",
			func(state *settingsState, value string) { state.Settings.Reviewer.SystemPromptFile = value },
			func(state settingsState) string { return state.Settings.Reviewer.SystemPromptFile },
			"",
			nil,
			nil,
			settingDocOptions{resolveRelativeToSettingsDir: true}),
		newIntSetting("reviewer.timeout_seconds", defaultReviewerTimeoutSec,
			func(state *settingsState, value int) { state.Settings.Reviewer.TimeoutSeconds = value },
			func(state settingsState) int { return state.Settings.Reviewer.TimeoutSeconds },
			"KENT_REVIEWER_TIMEOUT_SECONDS",
			nil,
			settingDocOptions{}),
		newBoolSetting("reviewer.verbose_output", false,
			func(state *settingsState, value bool) { state.Settings.Reviewer.VerboseOutput = value },
			func(state settingsState) bool { return state.Settings.Reviewer.VerboseOutput },
			"KENT_REVIEWER_VERBOSE_OUTPUT",
			settingDocOptions{}),
		subagentsSetting{},
		newStringSetting("prevent_sleep", SleepPreventionMode(SleepPreventionModeActive),
			func(state *settingsState, value SleepPreventionMode) { state.Settings.PreventSleep = value },
			func(state settingsState) SleepPreventionMode { return state.Settings.PreventSleep },
			"KENT_PREVENT_SLEEP",
			nil,
			nil,
			settingDocOptions{}),
	}

	registry := settingsRegistry{
		settings: settings,
		validators: []settingsValidator{
			validateModelNotEmpty,
			validateProviderOverrideRequiresModel,
			validateProviderOverrideValue,
			validateOpenAIBaseURL,
			validateProviderCapabilitiesProviderID,
			validateModelVerbosity,
			validateTheme,
			validateNotificationMethod,
			validateServerHost,
			validateServerPort,
			validateWebSearch,
			validateTimeouts,
			validateShellOutputMaxChars,
			validateMinimumExecToBgSeconds,
			validateBGShellsOutput,
			validateShellPostprocessing,
			validateCacheWarningMode,
			validateContextWindow,
			validateCompactionMode,
			validateReviewer,
			validateWorkflowSettings,
			validateSleepPreventionMode,
		},
	}

	registry.fileKeys = newFileKeyTree()
	for _, setting := range registry.settings {
		if fileKeySetting, ok := setting.(fileKeyRegisteringSetting); ok {
			fileKeySetting.registerFileKeys(registry.fileKeys)
		}
	}
	registerSubagentFileKeys(registry.fileKeys, registry.settings)
	return registry
}

func (r settingsRegistry) defaultState() settingsState {
	state := settingsState{}
	for _, setting := range r.settings {
		setting.applyDefault(&state)
	}
	return state
}

func (r settingsRegistry) defaultSourceMap() map[string]string {
	sources := map[string]string{}
	for _, setting := range r.settings {
		if sourceSetting, ok := setting.(sourceInitializingSetting); ok {
			sourceSetting.initSources(sources)
		}
	}
	return sources
}

func (r settingsRegistry) applyFile(raw settingsFile, settingsPath string, state *settingsState, sources map[string]string) error {
	if err := validateSettingsFileKeys(raw, r.fileKeys); err != nil {
		return err
	}
	for _, setting := range r.settings {
		if err := setting.applyFile(raw, settingsPath, state, sources); err != nil {
			return err
		}
	}
	return nil
}

func (r settingsRegistry) applyEnv(lookup envLookup, state *settingsState, sources map[string]string) error {
	for _, setting := range r.settings {
		envSetting, ok := setting.(envApplyingSetting)
		if !ok {
			continue
		}
		if err := envSetting.applyEnv(lookup, state, sources); err != nil {
			return err
		}
	}
	return nil
}

func (r settingsRegistry) applyCLI(opts LoadOptions, state *settingsState, sources map[string]string) error {
	for _, setting := range r.settings {
		cliSetting, ok := setting.(cliApplyingSetting)
		if !ok {
			continue
		}
		if err := cliSetting.applyCLI(opts, state, sources); err != nil {
			return err
		}
	}
	return nil
}

func (r settingsRegistry) validate(state settingsState, sources map[string]string) error {
	for _, validator := range r.validators {
		if err := validator(state, sources); err != nil {
			return err
		}
	}
	return nil
}

func (r settingsRegistry) defaultLines(state settingsState) []defaultConfigLine {
	lines := []defaultConfigLine{}
	for _, setting := range r.settings {
		if lineSetting, ok := setting.(defaultLineAppendingSetting); ok {
			lineSetting.appendDefaultLines(&lines, state)
		}
	}
	return lines
}

func newStringSetting[T ~string](
	key string,
	defaultValue T,
	apply func(*settingsState, T),
	get func(settingsState) T,
	envName string,
	decodeCLI func(LoadOptions) (string, bool, error),
	normalize func(string) T,
	doc settingDocOptions,
) scalarSetting[T] {
	var transformFileValue func(T, string) (T, error)
	if doc.resolveRelativeToSettingsDir {
		transformFileValue = func(value T, settingsPath string) (T, error) {
			resolved, err := resolveFileSettingRelativeToSettingsPath(string(value), settingsPath)
			return T(resolved), err
		}
	}
	return scalarSetting[T]{
		key:                key,
		defaultValue:       defaultValue,
		apply:              apply,
		get:                get,
		transformFileValue: transformFileValue,
		decodeFile: func(raw settingsFile, path []string) (T, bool, error) {
			lookup := lookupFileString
			if doc.allowEmptyString {
				lookup = lookupFileStringAllowEmpty
			}
			value, ok, err := lookup(raw, path)
			if err != nil || !ok {
				return *new(T), ok, err
			}
			if normalize != nil {
				return normalize(value), true, nil
			}
			return T(value), true, nil
		},
		envName: envName,
		decodeEnv: func(raw string, envName string) (T, error) {
			if normalize != nil {
				return normalize(raw), nil
			}
			return T(raw), nil
		},
		decodeCLI: func(opts LoadOptions) (T, bool, error) {
			if decodeCLI == nil {
				return *new(T), false, nil
			}
			value, ok, err := decodeCLI(opts)
			if err != nil || !ok {
				return *new(T), ok, err
			}
			if normalize != nil {
				return normalize(value), true, nil
			}
			return T(value), true, nil
		},
		doc: doc,
	}
}

func newBoolSetting(
	key string,
	defaultValue bool,
	apply func(*settingsState, bool),
	get func(settingsState) bool,
	envName string,
	doc settingDocOptions,
) scalarSetting[bool] {
	return scalarSetting[bool]{
		key:          key,
		defaultValue: defaultValue,
		apply:        apply,
		get:          get,
		decodeFile:   lookupFileBool,
		envName:      envName,
		decodeEnv: func(raw string, envName string) (bool, error) {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return false, fmt.Errorf("invalid %s: %q", envName, raw)
			}
			return parsed, nil
		},
		doc: doc,
	}
}

func newIntSetting(
	key string,
	defaultValue int,
	apply func(*settingsState, int),
	get func(settingsState) int,
	envName string,
	decodeCLI func(LoadOptions) (int, bool, error),
	doc settingDocOptions,
) scalarSetting[int] {
	return scalarSetting[int]{
		key:          key,
		defaultValue: defaultValue,
		apply:        apply,
		get:          get,
		decodeFile:   lookupFileInt,
		envName:      envName,
		decodeEnv: func(raw string, envName string) (int, error) {
			parsed, err := parsePositiveIntString(raw, envName)
			if err != nil {
				return 0, err
			}
			return *parsed, nil
		},
		decodeCLI: decodeCLI,
		doc:       doc,
	}
}

func (s scalarSetting[T]) applyDefault(state *settingsState) {
	s.apply(state, s.defaultValue)
}

func (s scalarSetting[T]) initSources(sources map[string]string) {
	sources[s.key] = "default"
}

func (s scalarSetting[T]) applyFile(raw settingsFile, settingsPath string, state *settingsState, sources map[string]string) error {
	value, ok, err := s.decodeFile(raw, strings.Split(s.key, "."))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if s.transformFileValue != nil {
		value, err = s.transformFileValue(value, settingsPath)
		if err != nil {
			return err
		}
	}
	s.apply(state, value)
	sources[s.key] = "file"
	return nil
}

func resolveFileSettingRelativeToSettingsPath(value string, settingsPath string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	expanded, err := expandTildePath(trimmed)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(expanded) {
		return filepath.Abs(expanded)
	}
	baseDir := strings.TrimSpace(filepath.Dir(settingsPath))
	if baseDir == "" || baseDir == "." {
		return filepath.Abs(expanded)
	}
	return filepath.Abs(filepath.Join(baseDir, expanded))
}

func (s scalarSetting[T]) applyEnv(lookup envLookup, state *settingsState, sources map[string]string) error {
	if s.envName == "" || s.decodeEnv == nil {
		return nil
	}
	value, ok := lookupTrimmedEnv(lookup, s.envName)
	if !ok {
		return nil
	}
	parsed, err := s.decodeEnv(value, s.envName)
	if err != nil {
		return err
	}
	s.apply(state, parsed)
	sources[s.key] = "env"
	return nil
}

func (s scalarSetting[T]) applyCLI(opts LoadOptions, state *settingsState, sources map[string]string) error {
	if s.decodeCLI == nil {
		return nil
	}
	value, ok, err := s.decodeCLI(opts)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	s.apply(state, value)
	sources[s.key] = "cli"
	return nil
}

func (s scalarSetting[T]) registerFileKeys(tree *fileKeyTree) {
	tree.allowPath(strings.Split(s.key, "."))
}

func (s scalarSetting[T]) appendDefaultLines(lines *[]defaultConfigLine, state settingsState) {
	if s.doc.omitInTOML {
		return
	}
	*lines = append(*lines, defaultConfigLine{
		Path:      strings.Split(s.key, "."),
		Value:     s.defaultDocValue(state),
		Commented: s.doc.commented,
	})
}

func (s scalarSetting[T]) defaultDocValue(state settingsState) any {
	if s.doc.defaultValue != nil {
		return s.doc.defaultValue(state)
	}
	return s.get(state)
}

func (toolsSetting) applyDefault(state *settingsState) {
	state.Settings.EnabledTools = defaultEnabledToolMap()
}

func (toolsSetting) initSources(sources map[string]string) {
	for _, id := range toolspec.CatalogIDs() {
		sources[toolSourceKey(id)] = "default"
	}
}

func (toolsSetting) applyFile(raw settingsFile, settingsPath string, state *settingsState, sources map[string]string) error {
	table, ok, err := lookupFileTable(raw, []string{"tools"})
	if err != nil || !ok {
		return err
	}
	for key, rawValue := range table {
		id, valid := toolspec.ParseConfigID(strings.TrimSpace(key))
		if !valid {
			return fmt.Errorf("invalid tools key in %s: %q", settingsPath, key)
		}
		enabled, ok := rawValue.(bool)
		if !ok {
			return &SettingsKeyTypeError{Key: strings.Join(append([]string{"tools"}, key), "."), ExpectedType: "boolean"}
		}
		state.Settings.EnabledTools[id] = enabled
		sources[toolSourceKey(id)] = "file"
	}
	return nil
}

func (toolsSetting) applyEnv(lookup envLookup, state *settingsState, sources map[string]string) error {
	value, ok := lookupTrimmedEnv(lookup, "KENT_TOOLS")
	if !ok {
		return nil
	}
	enabled, err := parseEnabledToolsCSV(value)
	if err != nil {
		return fmt.Errorf("invalid KENT_TOOLS: %w", err)
	}
	state.Settings.EnabledTools = resetEnabledToolMap(enabled)
	for _, id := range toolspec.CatalogIDs() {
		sources[toolSourceKey(id)] = "env"
	}
	return nil
}

func (toolsSetting) applyCLI(opts LoadOptions, state *settingsState, sources map[string]string) error {
	value, ok, err := trimmedCLIString(opts.Tools)
	if err != nil || !ok {
		return err
	}
	enabled, err := parseEnabledToolsCSV(value)
	if err != nil {
		return fmt.Errorf("invalid tools flag: %w", err)
	}
	state.Settings.EnabledTools = resetEnabledToolMap(enabled)
	for _, id := range toolspec.CatalogIDs() {
		sources[toolSourceKey(id)] = "cli"
	}
	return nil
}

func (toolsSetting) registerFileKeys(tree *fileKeyTree) {
	tree.allowDynamicChildren([]string{"tools"}, func(key string) bool {
		_, ok := toolspec.ParseConfigID(strings.TrimSpace(key))
		return ok
	}, nil)
}

func (toolsSetting) appendDefaultLines(lines *[]defaultConfigLine, state settingsState) {
	for _, id := range toolspec.CatalogIDs() {
		*lines = append(*lines, defaultConfigLine{
			Path:  []string{"tools", toolspec.ConfigName(id)},
			Value: state.Settings.EnabledTools[id],
		})
	}
}

func (skillsSetting) applyDefault(state *settingsState) {
	state.Settings.SkillToggles = map[string]bool{}
}

func (skillsSetting) applyFile(raw settingsFile, settingsPath string, state *settingsState, sources map[string]string) error {
	table, ok, err := lookupFileTable(raw, []string{"skills"})
	if err != nil || !ok {
		return err
	}
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seenNormalized := make(map[string]string, len(keys))
	for _, key := range keys {
		rawValue := table[key]
		normalized := NormalizeSkillName(key)
		if normalized == "" {
			return fmt.Errorf("invalid skills key in %s: %q", settingsPath, key)
		}
		if priorKey, exists := seenNormalized[normalized]; exists {
			return &DuplicateSettingsKeysError{Scope: "skills", SettingsPath: settingsPath, KeyA: priorKey, KeyB: key, Normalized: normalized}
		}
		enabled, ok := rawValue.(bool)
		if !ok {
			return &SettingsKeyTypeError{Key: strings.Join(append([]string{"skills"}, key), "."), ExpectedType: "boolean"}
		}
		seenNormalized[normalized] = key
		state.Settings.SkillToggles[normalized] = enabled
		sources[skillSourceKey(normalized)] = "file"
	}
	return nil
}

// NormalizeSkillName returns the canonical key used for skill toggles and
// runtime/catalog disabled-skill matching.
func NormalizeSkillName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

func (skillsSetting) registerFileKeys(tree *fileKeyTree) {
	tree.allowDynamicChildren([]string{"skills"}, func(key string) bool {
		return NormalizeSkillName(key) != ""
	}, nil)
}

func (subagentsSetting) applyDefault(state *settingsState) {
	state.Settings.Subagents = map[string]SubagentRole{}
}

func (subagentsSetting) applyFile(raw settingsFile, settingsPath string, state *settingsState, _ map[string]string) error {
	table, ok, err := lookupFileTable(raw, []string{"subagents"})
	if err != nil || !ok {
		return err
	}
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seen := make(map[string]string, len(keys))
	for _, key := range keys {
		rawValue := table[key]
		normalized := NormalizeSubagentRole(key)
		if normalized == "" {
			return fmt.Errorf("%w in %s: %q", errInvalidSubagentKey, settingsPath, key)
		}
		if priorKey, exists := seen[normalized]; exists {
			return &DuplicateSettingsKeysError{Scope: "subagents", SettingsPath: settingsPath, KeyA: priorKey, KeyB: key, Normalized: normalized}
		}
		roleTable, ok := asSettingsFile(rawValue)
		if !ok {
			return &SettingsKeyTypeError{Key: strings.Join([]string{"subagents", key}, "."), ExpectedType: "table"}
		}
		role, err := parseSubagentRole(roleTable, settingsPath, key)
		if err != nil {
			return err
		}
		seen[normalized] = key
		state.Settings.Subagents[normalized] = role
	}
	return nil
}

func registerSubagentFileKeys(tree *fileKeyTree, settings []registrySetting) {
	if tree == nil {
		return
	}
	template := newFileKeyTree()
	template.allowPath([]string{"description"})
	template.allowPath([]string{"agent_callable"})
	for _, setting := range settings {
		if _, ok := setting.(subagentsSetting); ok {
			continue
		}
		if fileKeySetting, ok := setting.(fileKeyRegisteringSetting); ok {
			fileKeySetting.registerFileKeys(template)
		}
	}
	tree.allowDynamicChildren([]string{"subagents"}, func(key string) bool {
		return IsSubagentRoleNameShape(key)
	}, template)
}

func parseSubagentRole(raw settingsFile, settingsPath string, roleKey string) (SubagentRole, error) {
	if _, exists := raw["subagents"]; exists {
		return SubagentRole{}, fmt.Errorf("subagents.%s cannot define nested subagents", roleKey)
	}
	if err := validateSettingsFileKeys(raw, subagentRoleKeyTree(configRegistry.settings)); err != nil {
		return SubagentRole{}, err
	}
	description, err := parseSubagentDescription(raw)
	if err != nil {
		return SubagentRole{}, fmt.Errorf("%w subagents.%s: %w", errSubagentRole, roleKey, err)
	}
	agentCallable, agentCallableSet, err := parseSubagentAgentCallable(raw)
	if err != nil {
		return SubagentRole{}, fmt.Errorf("%w subagents.%s: %w", errSubagentRole, roleKey, err)
	}
	roleState := configRegistry.defaultState()
	roleSources := configRegistry.defaultSourceMap()
	for _, setting := range configRegistry.settings {
		if _, ok := setting.(subagentsSetting); ok {
			continue
		}
		if err := setting.applyFile(raw, settingsPath, &roleState, roleSources); err != nil {
			return SubagentRole{}, fmt.Errorf("%w subagents.%s: %w", errSubagentRole, roleKey, err)
		}
	}
	explicitSources := map[string]string{}
	for key, source := range roleSources {
		if strings.TrimSpace(source) != "file" {
			continue
		}
		explicitSources[key] = source
	}
	if len(explicitSources) == 0 {
		explicitSources = nil
	}
	if err := validateSubagentRoleState(roleState, explicitSources); err != nil {
		return SubagentRole{}, fmt.Errorf("%w subagents.%s: %w", errSubagentRole, roleKey, err)
	}
	if _, ok := explicitSources["system_prompt_file"]; ok {
		resolved, err := resolveConfigRelativePath(roleState.Settings.SystemPromptFile, settingsPath)
		if err != nil {
			return SubagentRole{}, fmt.Errorf("%w subagents.%s: %w", errSubagentRole, roleKey, err)
		}
		if strings.TrimSpace(resolved) != "" {
			roleState.Settings.SystemPromptFiles = []SystemPromptFile{{Path: resolved, Scope: SystemPromptFileScopeSubagent}}
		}
	}
	roleState.Settings.Subagents = nil
	return SubagentRole{
		Settings:         roleState.Settings,
		Sources:          explicitSources,
		Description:      description,
		AgentCallable:    agentCallable,
		AgentCallableSet: agentCallableSet,
	}, nil
}

func subagentRoleKeyTree(settings []registrySetting) *fileKeyTree {
	tree := newFileKeyTree()
	tree.allowPath([]string{"description"})
	tree.allowPath([]string{"agent_callable"})
	for _, setting := range settings {
		if _, ok := setting.(subagentsSetting); ok {
			continue
		}
		if fileKeySetting, ok := setting.(fileKeyRegisteringSetting); ok {
			fileKeySetting.registerFileKeys(tree)
		}
	}
	return tree
}

func parseSubagentDescription(raw settingsFile) (string, error) {
	value, ok, err := lookupFileValue(raw, []string{"description"})
	if err != nil || !ok {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", &SettingsKeyTypeError{Key: strings.Join([]string{"description"}, "."), ExpectedType: "string"}
	}
	description := strings.Join(strings.Fields(text), " ")
	if len([]rune(description)) > MaxSubagentDescriptionChars {
		return "", fmt.Errorf("%w: must be <= %d characters after whitespace normalization", errSubagentDescriptionTooLong, MaxSubagentDescriptionChars)
	}
	return description, nil
}

func parseSubagentAgentCallable(raw settingsFile) (bool, bool, error) {
	value, ok, err := lookupFileBool(raw, []string{"agent_callable"})
	if err != nil {
		return false, false, err
	}
	if !ok {
		return true, false, nil
	}
	return value, true, nil
}

func newFileKeyTree() *fileKeyTree {
	return &fileKeyTree{children: map[string]*fileKeyTree{}}
}

func (t *fileKeyTree) ensureChild(key string) *fileKeyTree {
	child, ok := t.children[key]
	if !ok {
		child = newFileKeyTree()
		t.children[key] = child
	}
	return child
}

func (t *fileKeyTree) allowPath(path []string) {
	current := t
	for _, part := range path {
		current = current.ensureChild(part)
	}
}

func (t *fileKeyTree) allowDynamicChildren(path []string, allow func(string) bool, template *fileKeyTree) {
	current := t
	for _, part := range path {
		current = current.ensureChild(part)
	}
	current.dynamicChildren = allow
	current.dynamicTemplate = template
}

func validateSettingsFileKeys(raw settingsFile, tree *fileKeyTree) error {
	unknown := []string{}
	var walk func([]string, settingsFile, *fileKeyTree)
	walk = func(prefix []string, table settingsFile, node *fileKeyTree) {
		keys := make([]string, 0, len(table))
		for key := range table {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := table[key]
			path := append(append([]string{}, prefix...), key)
			child, ok := node.children[key]
			if !ok {
				if node.dynamicChildren != nil && node.dynamicChildren(key) {
					if node.dynamicTemplate != nil {
						child = node.dynamicTemplate
					} else {
						child = newFileKeyTree()
					}
				} else {
					unknown = append(unknown, strings.Join(path, "."))
					continue
				}
			}
			nested, isTable := asSettingsFile(value)
			if !isTable {
				continue
			}
			walk(path, nested, child)
		}
	}
	walk(nil, raw, tree)
	if len(unknown) == 0 {
		return nil
	}
	return &UnknownSettingsKeysError{Keys: unknown}
}

func lookupFileValue(raw settingsFile, path []string) (any, bool, error) {
	current := raw
	for index, part := range path {
		value, ok := current[part]
		if !ok {
			return nil, false, nil
		}
		if index == len(path)-1 {
			return value, true, nil
		}
		nested, ok := asSettingsFile(value)
		if !ok {
			return nil, false, &SettingsKeyTypeError{Key: strings.Join(path[:index+1], "."), ExpectedType: "table"}
		}
		current = nested
	}
	return nil, false, nil
}

func lookupFileString(raw settingsFile, path []string) (string, bool, error) {
	value, ok, err := lookupFileValue(raw, path)
	if err != nil || !ok {
		return "", ok, err
	}
	text, ok := value.(string)
	if !ok {
		return "", false, &SettingsKeyTypeError{Key: strings.Join(path, "."), ExpectedType: "string"}
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false, nil
	}
	return trimmed, true, nil
}

func lookupFileStringAllowEmpty(raw settingsFile, path []string) (string, bool, error) {
	value, ok, err := lookupFileValue(raw, path)
	if err != nil || !ok {
		return "", ok, err
	}
	text, ok := value.(string)
	if !ok {
		return "", false, &SettingsKeyTypeError{Key: strings.Join(path, "."), ExpectedType: "string"}
	}
	return strings.TrimSpace(text), true, nil
}

func lookupFileBool(raw settingsFile, path []string) (bool, bool, error) {
	value, ok, err := lookupFileValue(raw, path)
	if err != nil || !ok {
		return false, ok, err
	}
	parsed, ok := value.(bool)
	if !ok {
		return false, false, &SettingsKeyTypeError{Key: strings.Join(path, "."), ExpectedType: "boolean"}
	}
	return parsed, true, nil
}

func lookupFileInt(raw settingsFile, path []string) (int, bool, error) {
	value, ok, err := lookupFileValue(raw, path)
	if err != nil || !ok {
		return 0, ok, err
	}
	parsed, ok := coerceTOMLInt(value)
	if !ok {
		return 0, false, &SettingsKeyTypeError{Key: strings.Join(path, "."), ExpectedType: "integer"}
	}
	return parsed, true, nil
}

func lookupFileTable(raw settingsFile, path []string) (settingsFile, bool, error) {
	value, ok, err := lookupFileValue(raw, path)
	if err != nil || !ok {
		return nil, ok, err
	}
	table, ok := asSettingsFile(value)
	if !ok {
		return nil, false, &SettingsKeyTypeError{Key: strings.Join(path, "."), ExpectedType: "table"}
	}
	return table, true, nil
}

func asSettingsFile(value any) (settingsFile, bool) {
	table, ok := value.(map[string]any)
	if ok {
		return settingsFile(table), true
	}
	tableAlt, ok := value.(map[string]interface{})
	if ok {
		return settingsFile(tableAlt), true
	}
	return nil, false
}

func coerceTOMLInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int8:
		return int(v), true
	case int16:
		return int(v), true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	default:
		return 0, false
	}
}

func toolSourceKey(id toolspec.ID) string {
	return "tools." + toolspec.ConfigName(id)
}

func skillSourceKey(name string) string {
	return "skills." + NormalizeSkillName(name)
}

func defaultEnabledToolMap() map[toolspec.ID]bool {
	enabled := map[toolspec.ID]bool{}
	for _, id := range toolspec.CatalogIDs() {
		enabled[id] = false
	}
	for _, id := range toolspec.DefaultEnabledToolIDs() {
		enabled[id] = true
	}
	return enabled
}

func trimmedCLIString(raw string) (string, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, nil
	}
	return trimmed, true, nil
}

func renderTOMLValue(value any) string {
	switch v := value.(type) {
	case string:
		return strconv.Quote(v)
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case fmt.Stringer:
		return strconv.Quote(v.String())
	case ModelVerbosity:
		return strconv.Quote(string(v))
	case CompactionMode:
		return strconv.Quote(string(v))
	case BGShellsOutputMode:
		return strconv.Quote(string(v))
	case ShellPostprocessingMode:
		return strconv.Quote(string(v))
	case SleepPreventionMode:
		return strconv.Quote(string(v))
	default:
		return strconv.Quote(fmt.Sprintf("%v", v))
	}
}

func filterDefaultLines(lines []defaultConfigLine, section string) []defaultConfigLine {
	filtered := []defaultConfigLine{}
	sectionPath := strings.Split(section, ".")
	for _, line := range lines {
		if section == "" {
			if len(line.Path) == 1 {
				filtered = append(filtered, line)
			}
			continue
		}
		if len(line.Path) > len(sectionPath) && hasPathPrefix(line.Path, sectionPath) {
			filtered = append(filtered, line)
		}
	}
	return filtered
}

func filterExactSectionLines(lines []defaultConfigLine, section string) []defaultConfigLine {
	filtered := []defaultConfigLine{}
	sectionPath := strings.Split(section, ".")
	for _, line := range lines {
		if len(line.Path) == len(sectionPath)+1 && hasPathPrefix(line.Path, sectionPath) {
			filtered = append(filtered, line)
		}
	}
	return filtered
}

func hasPathPrefix(path []string, prefix []string) bool {
	if len(prefix) == 0 || len(path) < len(prefix) {
		return false
	}
	for i, part := range prefix {
		if path[i] != part {
			return false
		}
	}
	return true
}

func writeDefaultLines(builder *strings.Builder, lines []defaultConfigLine) {
	for _, line := range lines {
		assignment := line.Path[len(line.Path)-1] + " = " + renderTOMLValue(line.Value)
		if line.Commented {
			builder.WriteString("# ")
		}
		builder.WriteString(assignment)
		builder.WriteByte('\n')
	}
}
