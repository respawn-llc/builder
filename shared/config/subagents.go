package config

import (
	"sort"
	"strings"

	"core/shared/toolspec"
)

const BuiltInSubagentRoleFast = "fast"
const DefaultSubagentRole = "default"
const MaxSubagentDescriptionChars = 5000

var reservedSubagentRoleNames = map[string]bool{
	DefaultSubagentRole: true,
	"none":              true,
	"self":              true,
}

func NormalizeSubagentRole(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return ""
	}
	if reservedSubagentRoleNames[normalized] {
		return ""
	}
	for _, r := range normalized {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '-', '_':
			continue
		default:
			return ""
		}
	}
	return normalized
}

func NormalizeSubagentSelector(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if reservedSubagentRoleNames[normalized] {
		return ""
	}
	return NormalizeSubagentRole(raw)
}

func IsReservedSubagentRoleName(raw string) bool {
	return reservedSubagentRoleNames[strings.ToLower(strings.TrimSpace(raw))]
}

func IsSubagentRoleNameShape(raw string) bool {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return false
	}
	for _, r := range normalized {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '-', '_':
			continue
		default:
			return false
		}
	}
	return true
}

func SubagentRoleCallable(role SubagentRole) bool {
	return !role.AgentCallableSet || role.AgentCallable
}

type SubagentRoleLookupStatus string

const (
	SubagentRoleLookupInvalid SubagentRoleLookupStatus = "invalid"
	SubagentRoleLookupMissing SubagentRoleLookupStatus = "missing"
	SubagentRoleLookupPresent SubagentRoleLookupStatus = "present"
)

type SubagentRoleLookup struct {
	Role               SubagentRole
	NormalizedSelector *string
	Status             SubagentRoleLookupStatus
}

// LookupSubagentRole resolves a subagent selector to configured role identity.
// It treats built-in roles as present and does not apply presentation filters
// such as meaningful-diff or callability checks.
func LookupSubagentRole(settings Settings, rawSelector string) SubagentRoleLookup {
	normalized := NormalizeSubagentSelector(rawSelector)
	if normalized == "" {
		return SubagentRoleLookup{Status: SubagentRoleLookupInvalid}
	}
	if normalized == BuiltInSubagentRoleFast {
		return SubagentRoleLookup{
			Role:               settings.Subagents[normalized],
			NormalizedSelector: subagentRoleLookupSelector(normalized),
			Status:             SubagentRoleLookupPresent,
		}
	}
	role, ok := settings.Subagents[normalized]
	if !ok {
		return SubagentRoleLookup{
			NormalizedSelector: subagentRoleLookupSelector(normalized),
			Status:             SubagentRoleLookupMissing,
		}
	}
	return SubagentRoleLookup{
		Role:               role,
		NormalizedSelector: subagentRoleLookupSelector(normalized),
		Status:             SubagentRoleLookupPresent,
	}
}

func subagentRoleLookupSelector(selector string) *string {
	return &selector
}

// AvailableSubagentRoleNames returns presentation-ready role names. It filters
// out configured roles that have no runtime diff from the base settings and can
// optionally filter non-callable roles. Use LookupSubagentRole for existence.
func AvailableSubagentRoleNames(settings Settings, agentCallableOnly bool) []string {
	names := []string{}
	if !agentCallableOnly || SubagentRoleCallable(settings.Subagents[BuiltInSubagentRoleFast]) {
		names = append(names, BuiltInSubagentRoleFast)
	}
	for name, role := range settings.Subagents {
		normalized := NormalizeSubagentRole(name)
		if normalized == "" || normalized == BuiltInSubagentRoleFast {
			continue
		}
		if !SubagentRoleHasMeaningfulDiff(settings, role) {
			continue
		}
		if agentCallableOnly && !SubagentRoleCallable(role) {
			continue
		}
		names = append(names, normalized)
	}
	sort.Strings(names)
	for i, name := range names {
		if name == BuiltInSubagentRoleFast {
			copy(names[1:i+1], names[0:i])
			names[0] = BuiltInSubagentRoleFast
			break
		}
	}
	return names
}

func SubagentRoleHasMeaningfulDiff(base Settings, role SubagentRole) bool {
	for key := range role.Sources {
		if subagentSourceDiffers(base, role, key) {
			return true
		}
	}
	return false
}

func subagentSourceDiffers(base Settings, role SubagentRole, key string) bool {
	switch key {
	case "model":
		return strings.TrimSpace(base.Model) != strings.TrimSpace(role.Settings.Model)
	case "thinking_level":
		return strings.TrimSpace(base.ThinkingLevel) != strings.TrimSpace(role.Settings.ThinkingLevel)
	case "model_verbosity":
		return strings.TrimSpace(string(base.ModelVerbosity)) != strings.TrimSpace(string(role.Settings.ModelVerbosity))
	case "model_capabilities.supports_reasoning_effort":
		return base.ModelCapabilities.SupportsReasoningEffort != role.Settings.ModelCapabilities.SupportsReasoningEffort
	case "model_capabilities.supports_vision_inputs":
		return base.ModelCapabilities.SupportsVisionInputs != role.Settings.ModelCapabilities.SupportsVisionInputs
	case "system_prompt_file":
		return strings.TrimSpace(base.SystemPromptFile) != strings.TrimSpace(role.Settings.SystemPromptFile)
	case "priority_request_mode":
		return base.PriorityRequestMode != role.Settings.PriorityRequestMode
	case "provider_override":
		return strings.TrimSpace(base.ProviderOverride) != strings.TrimSpace(role.Settings.ProviderOverride)
	case "openai_base_url":
		return strings.TrimSpace(base.OpenAIBaseURL) != strings.TrimSpace(role.Settings.OpenAIBaseURL)
	case "provider_capabilities.provider_id":
		return strings.TrimSpace(base.ProviderCapabilities.ProviderID) != strings.TrimSpace(role.Settings.ProviderCapabilities.ProviderID)
	case "provider_capabilities.supports_responses_api":
		return base.ProviderCapabilities.SupportsResponsesAPI != role.Settings.ProviderCapabilities.SupportsResponsesAPI
	case "provider_capabilities.supports_responses_compact":
		return base.ProviderCapabilities.SupportsResponsesCompact != role.Settings.ProviderCapabilities.SupportsResponsesCompact
	case "provider_capabilities.supports_request_input_token_count":
		return base.ProviderCapabilities.SupportsRequestInputTokenCount != role.Settings.ProviderCapabilities.SupportsRequestInputTokenCount
	case "provider_capabilities.supports_prompt_cache_key":
		return base.ProviderCapabilities.SupportsPromptCacheKey != role.Settings.ProviderCapabilities.SupportsPromptCacheKey
	case "provider_capabilities.supports_native_web_search":
		return base.ProviderCapabilities.SupportsNativeWebSearch != role.Settings.ProviderCapabilities.SupportsNativeWebSearch
	case "provider_capabilities.supports_reasoning_encrypted":
		return base.ProviderCapabilities.SupportsReasoningEncrypted != role.Settings.ProviderCapabilities.SupportsReasoningEncrypted
	case "provider_capabilities.supports_server_side_context_edit":
		return base.ProviderCapabilities.SupportsServerSideContextEdit != role.Settings.ProviderCapabilities.SupportsServerSideContextEdit
	case "provider_capabilities.supports_provider_verbosity":
		return base.ProviderCapabilities.SupportsProviderVerbosity != role.Settings.ProviderCapabilities.SupportsProviderVerbosity
	case "provider_capabilities.is_openai_first_party":
		return base.ProviderCapabilities.IsOpenAIFirstParty != role.Settings.ProviderCapabilities.IsOpenAIFirstParty
	case "web_search":
		return strings.TrimSpace(base.WebSearch) != strings.TrimSpace(role.Settings.WebSearch)
	case "tool_preambles":
		return base.ToolPreambles != role.Settings.ToolPreambles
	case "model_context_window":
		return base.ModelContextWindow != role.Settings.ModelContextWindow
	case "context_compaction_threshold_tokens":
		return base.ContextCompactionThresholdTokens != role.Settings.ContextCompactionThresholdTokens
	case "pre_submit_compaction_lead_tokens":
		return base.PreSubmitCompactionLeadTokens != role.Settings.PreSubmitCompactionLeadTokens
	case "minimum_exec_to_bg_seconds":
		return base.MinimumExecToBgSeconds != role.Settings.MinimumExecToBgSeconds
	case "shell_output_max_chars":
		return base.ShellOutputMaxChars != role.Settings.ShellOutputMaxChars
	case "bg_shells_output":
		return strings.TrimSpace(string(base.BGShellsOutput)) != strings.TrimSpace(string(role.Settings.BGShellsOutput))
	case "cache_warning_mode":
		return strings.TrimSpace(string(base.CacheWarningMode)) != strings.TrimSpace(string(role.Settings.CacheWarningMode))
	case "compaction_mode":
		return strings.TrimSpace(string(base.CompactionMode)) != strings.TrimSpace(string(role.Settings.CompactionMode))
	case "timeouts.model_request_seconds":
		return base.Timeouts.ModelRequestSeconds != role.Settings.Timeouts.ModelRequestSeconds
	case "shell.postprocessing_mode":
		return strings.TrimSpace(string(base.Shell.PostprocessingMode)) != strings.TrimSpace(string(role.Settings.Shell.PostprocessingMode))
	case "shell.postprocess_hook":
		return strings.TrimSpace(base.Shell.PostprocessHook) != strings.TrimSpace(role.Settings.Shell.PostprocessHook)
	case "reviewer.frequency":
		return strings.TrimSpace(base.Reviewer.Frequency) != strings.TrimSpace(role.Settings.Reviewer.Frequency)
	case "reviewer.model":
		return strings.TrimSpace(base.Reviewer.Model) != strings.TrimSpace(role.Settings.Reviewer.Model)
	case "reviewer.thinking_level":
		return strings.TrimSpace(base.Reviewer.ThinkingLevel) != strings.TrimSpace(role.Settings.Reviewer.ThinkingLevel)
	case "reviewer.model_verbosity":
		return strings.TrimSpace(string(base.Reviewer.ModelVerbosity)) != strings.TrimSpace(string(role.Settings.Reviewer.ModelVerbosity))
	case "reviewer.provider_override":
		return strings.TrimSpace(base.Reviewer.ProviderOverride) != strings.TrimSpace(role.Settings.Reviewer.ProviderOverride)
	case "reviewer.openai_base_url":
		return strings.TrimSpace(base.Reviewer.OpenAIBaseURL) != strings.TrimSpace(role.Settings.Reviewer.OpenAIBaseURL)
	case "reviewer.model_context_window":
		return base.Reviewer.ModelContextWindow != role.Settings.Reviewer.ModelContextWindow
	case "reviewer.auth":
		return strings.TrimSpace(base.Reviewer.Auth) != strings.TrimSpace(role.Settings.Reviewer.Auth)
	case "reviewer.system_prompt_file":
		return strings.TrimSpace(base.Reviewer.SystemPromptFile) != strings.TrimSpace(role.Settings.Reviewer.SystemPromptFile)
	case "reviewer.timeout_seconds":
		return base.Reviewer.TimeoutSeconds != role.Settings.Reviewer.TimeoutSeconds
	case "reviewer.verbose_output":
		return base.Reviewer.VerboseOutput != role.Settings.Reviewer.VerboseOutput
	case "reviewer.model_capabilities.supports_reasoning_effort":
		return base.Reviewer.ModelCapabilities.SupportsReasoningEffort != role.Settings.Reviewer.ModelCapabilities.SupportsReasoningEffort
	case "reviewer.model_capabilities.supports_vision_inputs":
		return base.Reviewer.ModelCapabilities.SupportsVisionInputs != role.Settings.Reviewer.ModelCapabilities.SupportsVisionInputs
	case "reviewer.provider_capabilities.provider_id":
		return strings.TrimSpace(base.Reviewer.ProviderCapabilities.ProviderID) != strings.TrimSpace(role.Settings.Reviewer.ProviderCapabilities.ProviderID)
	case "reviewer.provider_capabilities.supports_responses_api":
		return base.Reviewer.ProviderCapabilities.SupportsResponsesAPI != role.Settings.Reviewer.ProviderCapabilities.SupportsResponsesAPI
	case "reviewer.provider_capabilities.supports_responses_compact":
		return base.Reviewer.ProviderCapabilities.SupportsResponsesCompact != role.Settings.Reviewer.ProviderCapabilities.SupportsResponsesCompact
	case "reviewer.provider_capabilities.supports_request_input_token_count":
		return base.Reviewer.ProviderCapabilities.SupportsRequestInputTokenCount != role.Settings.Reviewer.ProviderCapabilities.SupportsRequestInputTokenCount
	case "reviewer.provider_capabilities.supports_prompt_cache_key":
		return base.Reviewer.ProviderCapabilities.SupportsPromptCacheKey != role.Settings.Reviewer.ProviderCapabilities.SupportsPromptCacheKey
	case "reviewer.provider_capabilities.supports_native_web_search":
		return base.Reviewer.ProviderCapabilities.SupportsNativeWebSearch != role.Settings.Reviewer.ProviderCapabilities.SupportsNativeWebSearch
	case "reviewer.provider_capabilities.supports_reasoning_encrypted":
		return base.Reviewer.ProviderCapabilities.SupportsReasoningEncrypted != role.Settings.Reviewer.ProviderCapabilities.SupportsReasoningEncrypted
	case "reviewer.provider_capabilities.supports_server_side_context_edit":
		return base.Reviewer.ProviderCapabilities.SupportsServerSideContextEdit != role.Settings.Reviewer.ProviderCapabilities.SupportsServerSideContextEdit
	case "reviewer.provider_capabilities.supports_provider_verbosity":
		return base.Reviewer.ProviderCapabilities.SupportsProviderVerbosity != role.Settings.Reviewer.ProviderCapabilities.SupportsProviderVerbosity
	case "reviewer.provider_capabilities.is_openai_first_party":
		return base.Reviewer.ProviderCapabilities.IsOpenAIFirstParty != role.Settings.Reviewer.ProviderCapabilities.IsOpenAIFirstParty
	}
	if strings.HasPrefix(key, "tools.") {
		toolName := strings.TrimPrefix(key, "tools.")
		if id, ok := toolspec.ParseConfigID(toolName); ok {
			return base.EnabledTools[id] != role.Settings.EnabledTools[id]
		}
		return true
	}
	if strings.HasPrefix(key, "skills.") {
		name := strings.TrimPrefix(key, "skills.")
		return base.SkillToggles[name] != role.Settings.SkillToggles[name]
	}
	return true
}
