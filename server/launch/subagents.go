package launch

import (
	"fmt"
	"reflect"
	"strings"

	"core/server/auth"
	"core/server/llm"
	"core/shared/config"
	"core/shared/toolspec"
	"core/shared/valuecopy"
)

const fastRoleSameAsMainWarning = "Warning: user configuration for fast agents is the same as for other agents. Consider asking the user to edit their config to pick a faster, smaller model at the end of your task. More info at " + config.DocsURL

func resolveSubagentSettingsWithValidation(base config.Settings, providerBase config.Settings, baseSources map[string]string, roleName string, authState auth.State, allowModelOverride bool, validate bool) (config.Settings, string, error) {
	lookup := config.LookupSubagentRole(base, roleName)
	switch lookup.Status {
	case config.SubagentRoleLookupInvalid:
		return config.Settings{}, "", fmt.Errorf("invalid subagent role %q", roleName)
	case config.SubagentRoleLookupMissing:
		return config.Settings{}, "", fmt.Errorf("Unrecognized role %q. It may have been removed by the user during the session. Available roles: [%s]", *lookup.NormalizedSelector, strings.Join(config.AvailableSubagentRoleNames(base, false), ", "))
	}
	providerSettings := cloneSettings(providerBase)
	providerSettings.Subagents = nil
	applySubagentProviderOverrides(&providerSettings, lookup.Role)
	providerCaps, err := llm.ProviderCapabilitiesForSettings(authState, providerSettings)
	if err != nil {
		return config.Settings{}, "", err
	}
	resolved, _, warning, err := resolveSubagentSettingsWithProviderID(
		base,
		config.SourceReport{Sources: baseSources},
		*lookup.NormalizedSelector,
		strings.TrimSpace(providerCaps.ProviderID),
		allowModelOverride,
		validate,
	)
	if err != nil {
		return config.Settings{}, "", err
	}
	return resolved, warning, nil
}

func resolveSubagentSettingsWithProviderID(base config.Settings, baseSource config.SourceReport, roleName string, providerID string, allowModelOverride bool, validate bool) (config.Settings, config.SourceReport, string, error) {
	lookup := config.LookupSubagentRole(base, roleName)
	switch lookup.Status {
	case config.SubagentRoleLookupInvalid:
		return config.Settings{}, config.SourceReport{}, "", fmt.Errorf("invalid subagent role %q", roleName)
	case config.SubagentRoleLookupMissing:
		return config.Settings{}, config.SourceReport{}, "", fmt.Errorf("Unrecognized role %q. It may have been removed by the user during the session. Available roles: [%s]", *lookup.NormalizedSelector, strings.Join(config.AvailableSubagentRoleNames(base, false), ", "))
	}
	resolved := cloneSettings(base)
	_ = applyBuiltInRoleHeuristics(&resolved, *lookup.NormalizedSelector, strings.TrimSpace(providerID), allowModelOverride)
	applySubagentRoleOverrides(&resolved, lookup.Role, allowModelOverride)
	effectiveSource := sourceReportWithSubagentRoleSources(baseSource, base, *lookup.NormalizedSelector, allowModelOverride)
	effectiveSources := cloneStringMap(effectiveSource.Sources)
	applyReviewerInheritance(&resolved, effectiveSources)
	effectiveSource.Sources = effectiveSources
	if validate {
		if err := config.ValidateSettingsWithSources(resolved, effectiveSources); err != nil {
			return config.Settings{}, config.SourceReport{}, "", fmt.Errorf("invalid subagent role %q: %w", *lookup.NormalizedSelector, err)
		}
	}
	warning := ""
	if *lookup.NormalizedSelector == config.BuiltInSubagentRoleFast && sameResolvedSubagentSettings(base, resolved) {
		warning = fastRoleSameAsMainWarning
	}
	return resolved, effectiveSource, warning, nil
}

func applyBuiltInRoleHeuristics(settings *config.Settings, roleName string, providerID string, allowModelOverride bool) bool {
	if settings == nil || roleName != config.BuiltInSubagentRoleFast {
		return false
	}
	if providerID != "openai" && providerID != "chatgpt-codex" {
		return false
	}
	settings.PriorityRequestMode = true
	if !allowModelOverride {
		return true
	}
	settings.Model = "gpt-5.6-terra"
	settings.ThinkingLevel = "low"
	llm.ApplyDerivedModelContextBudget(settings, settings.Model, settings.ModelContextWindow, settings.ContextCompactionThresholdTokens)
	settings.PreSubmitCompactionLeadTokens = config.DefaultPreSubmitRunwayTokens
	return true
}

func applySubagentRoleOverrides(settings *config.Settings, role config.SubagentRole, allowModelOverride bool) {
	if settings == nil || len(role.Sources) == 0 {
		return
	}
	originalModel := strings.TrimSpace(settings.Model)
	for key := range role.Sources {
		switch key {
		case "model":
			if allowModelOverride {
				settings.Model = role.Settings.Model
			}
		case "thinking_level":
			settings.ThinkingLevel = role.Settings.ThinkingLevel
		case "model_verbosity":
			settings.ModelVerbosity = role.Settings.ModelVerbosity
		case "system_prompt_file":
			settings.SystemPromptFile = role.Settings.SystemPromptFile
			settings.SystemPromptFiles = append(settings.SystemPromptFiles, role.Settings.SystemPromptFiles...)
		case "model_capabilities.supports_reasoning_effort":
			settings.ModelCapabilities.SupportsReasoningEffort = role.Settings.ModelCapabilities.SupportsReasoningEffort
		case "model_capabilities.supports_vision_inputs":
			settings.ModelCapabilities.SupportsVisionInputs = role.Settings.ModelCapabilities.SupportsVisionInputs
		case "theme":
			settings.Theme = role.Settings.Theme
		case "notification_method":
			settings.NotificationMethod = role.Settings.NotificationMethod
		case "tool_preambles":
			settings.ToolPreambles = role.Settings.ToolPreambles
		case "priority_request_mode":
			settings.PriorityRequestMode = role.Settings.PriorityRequestMode
		case "debug":
			settings.Debug = role.Settings.Debug
		case "server_host":
			settings.ServerHost = role.Settings.ServerHost
		case "server_port":
			settings.ServerPort = role.Settings.ServerPort
		case "web_search":
			settings.WebSearch = role.Settings.WebSearch
		case "provider_override":
			settings.ProviderOverride = role.Settings.ProviderOverride
		case "openai_base_url":
			settings.OpenAIBaseURL = role.Settings.OpenAIBaseURL
		case "provider_capabilities.provider_id":
			settings.ProviderCapabilities.ProviderID = role.Settings.ProviderCapabilities.ProviderID
		case "provider_capabilities.supports_responses_api":
			settings.ProviderCapabilities.SupportsResponsesAPI = role.Settings.ProviderCapabilities.SupportsResponsesAPI
		case "provider_capabilities.supports_responses_compact":
			settings.ProviderCapabilities.SupportsResponsesCompact = role.Settings.ProviderCapabilities.SupportsResponsesCompact
		case "provider_capabilities.supports_request_input_token_count":
			settings.ProviderCapabilities.SupportsRequestInputTokenCount = role.Settings.ProviderCapabilities.SupportsRequestInputTokenCount
		case "provider_capabilities.supports_prompt_cache_key":
			settings.ProviderCapabilities.SupportsPromptCacheKey = role.Settings.ProviderCapabilities.SupportsPromptCacheKey
		case "provider_capabilities.supports_native_web_search":
			settings.ProviderCapabilities.SupportsNativeWebSearch = role.Settings.ProviderCapabilities.SupportsNativeWebSearch
		case "provider_capabilities.supports_reasoning_encrypted":
			settings.ProviderCapabilities.SupportsReasoningEncrypted = role.Settings.ProviderCapabilities.SupportsReasoningEncrypted
		case "provider_capabilities.supports_server_side_context_edit":
			settings.ProviderCapabilities.SupportsServerSideContextEdit = role.Settings.ProviderCapabilities.SupportsServerSideContextEdit
		case "provider_capabilities.supports_provider_verbosity":
			settings.ProviderCapabilities.SupportsProviderVerbosity = role.Settings.ProviderCapabilities.SupportsProviderVerbosity
		case "provider_capabilities.is_openai_first_party":
			settings.ProviderCapabilities.IsOpenAIFirstParty = role.Settings.ProviderCapabilities.IsOpenAIFirstParty
		case "store":
			settings.Store = role.Settings.Store
		case "allow_non_cwd_edits":
			settings.AllowNonCwdEdits = role.Settings.AllowNonCwdEdits
		case "model_context_window":
			settings.ModelContextWindow = role.Settings.ModelContextWindow
		case "context_compaction_threshold_tokens":
			settings.ContextCompactionThresholdTokens = role.Settings.ContextCompactionThresholdTokens
		case "pre_submit_compaction_lead_tokens":
			settings.PreSubmitCompactionLeadTokens = role.Settings.PreSubmitCompactionLeadTokens
		case "minimum_exec_to_bg_seconds":
			settings.MinimumExecToBgSeconds = role.Settings.MinimumExecToBgSeconds
		case "compaction_mode":
			settings.CompactionMode = role.Settings.CompactionMode
		case "timeouts.model_request_seconds":
			settings.Timeouts.ModelRequestSeconds = role.Settings.Timeouts.ModelRequestSeconds
		case "shell_output_max_chars":
			settings.ShellOutputMaxChars = role.Settings.ShellOutputMaxChars
		case "bg_shells_output":
			settings.BGShellsOutput = role.Settings.BGShellsOutput
		case "shell.postprocessing_mode":
			settings.Shell.PostprocessingMode = role.Settings.Shell.PostprocessingMode
		case "shell.postprocess_hook":
			settings.Shell.PostprocessHook = valuecopy.Pointer(role.Settings.Shell.PostprocessHook)
		case "cache_warning_mode":
			settings.CacheWarningMode = role.Settings.CacheWarningMode
		default:
			if applyReviewerRoleOverride(settings, role.Settings, key) {
				continue
			}
		}
	}
	applyDerivedModelContextBudgetOverrides(settings, role.Sources, originalModel, allowModelOverride)
	for _, id := range toolspec.CatalogIDs() {
		key := "tools." + toolspec.ConfigName(id)
		if _, ok := role.Sources[key]; !ok {
			continue
		}
		if settings.EnabledTools == nil {
			settings.EnabledTools = map[toolspec.ID]bool{}
		}
		settings.EnabledTools[id] = role.Settings.EnabledTools[id]
	}
	for key, enabled := range role.Settings.SkillToggles {
		if _, ok := role.Sources["skills."+key]; !ok {
			continue
		}
		if settings.SkillToggles == nil {
			settings.SkillToggles = map[string]bool{}
		}
		settings.SkillToggles[key] = enabled
	}
}

func applySubagentProviderOverrides(settings *config.Settings, role config.SubagentRole) {
	if settings == nil || len(role.Sources) == 0 {
		return
	}
	for key := range role.Sources {
		switch key {
		case "provider_override":
			settings.ProviderOverride = role.Settings.ProviderOverride
		case "openai_base_url":
			settings.OpenAIBaseURL = role.Settings.OpenAIBaseURL
		case "provider_capabilities.provider_id":
			settings.ProviderCapabilities.ProviderID = role.Settings.ProviderCapabilities.ProviderID
		case "provider_capabilities.supports_responses_api":
			settings.ProviderCapabilities.SupportsResponsesAPI = role.Settings.ProviderCapabilities.SupportsResponsesAPI
		case "provider_capabilities.supports_responses_compact":
			settings.ProviderCapabilities.SupportsResponsesCompact = role.Settings.ProviderCapabilities.SupportsResponsesCompact
		case "provider_capabilities.supports_request_input_token_count":
			settings.ProviderCapabilities.SupportsRequestInputTokenCount = role.Settings.ProviderCapabilities.SupportsRequestInputTokenCount
		case "provider_capabilities.supports_prompt_cache_key":
			settings.ProviderCapabilities.SupportsPromptCacheKey = role.Settings.ProviderCapabilities.SupportsPromptCacheKey
		case "provider_capabilities.supports_native_web_search":
			settings.ProviderCapabilities.SupportsNativeWebSearch = role.Settings.ProviderCapabilities.SupportsNativeWebSearch
		case "provider_capabilities.supports_reasoning_encrypted":
			settings.ProviderCapabilities.SupportsReasoningEncrypted = role.Settings.ProviderCapabilities.SupportsReasoningEncrypted
		case "provider_capabilities.supports_server_side_context_edit":
			settings.ProviderCapabilities.SupportsServerSideContextEdit = role.Settings.ProviderCapabilities.SupportsServerSideContextEdit
		case "provider_capabilities.supports_provider_verbosity":
			settings.ProviderCapabilities.SupportsProviderVerbosity = role.Settings.ProviderCapabilities.SupportsProviderVerbosity
		case "provider_capabilities.is_openai_first_party":
			settings.ProviderCapabilities.IsOpenAIFirstParty = role.Settings.ProviderCapabilities.IsOpenAIFirstParty
		}
	}
}

func applyDerivedModelContextBudgetOverrides(settings *config.Settings, explicitSources map[string]string, originalModel string, allowModelOverride bool) {
	if settings == nil || !allowModelOverride {
		return
	}
	if _, ok := explicitSources["model"]; !ok {
		return
	}
	if strings.TrimSpace(settings.Model) == "" || strings.TrimSpace(settings.Model) == originalModel {
		return
	}
	if _, ok := explicitSources["model_context_window"]; !ok {
		if meta, ok := llm.LookupModelMetadata(settings.Model); ok && meta.ContextWindowTokens > 0 {
			settings.ModelContextWindow = meta.ContextWindowTokens
		}
	}
	if _, ok := explicitSources["context_compaction_threshold_tokens"]; !ok && settings.ModelContextWindow > 0 {
		settings.ContextCompactionThresholdTokens = settings.ModelContextWindow * 95 / 100
	}
	if _, ok := explicitSources["pre_submit_compaction_lead_tokens"]; !ok {
		settings.PreSubmitCompactionLeadTokens = config.DefaultPreSubmitRunwayTokens
	}
}

func applyReviewerInheritance(settings *config.Settings, sources map[string]string) {
	if settings == nil {
		return
	}
	if strings.TrimSpace(sources["reviewer.model"]) == "default" {
		settings.Reviewer.Model = settings.Model
	}
	if strings.TrimSpace(sources["reviewer.thinking_level"]) == "default" {
		settings.Reviewer.ThinkingLevel = settings.ThinkingLevel
	}
	if strings.TrimSpace(sources["reviewer.model_verbosity"]) == "default" {
		settings.Reviewer.ModelVerbosity = settings.ModelVerbosity
	}
	reviewerProviderSourceDefault := strings.TrimSpace(sources["reviewer.provider_override"]) == "default"
	reviewerBaseURLSourceDefault := strings.TrimSpace(sources["reviewer.openai_base_url"]) == "default"
	if reviewerProviderSourceDefault || reviewerBaseURLSourceDefault {
		originalProviderOverride := settings.Reviewer.ProviderOverride
		originalOpenAIBaseURL := settings.Reviewer.OpenAIBaseURL
		if reviewerProviderSourceDefault {
			settings.Reviewer.ProviderOverride = ""
		}
		if reviewerBaseURLSourceDefault {
			settings.Reviewer.OpenAIBaseURL = ""
		}
		reviewerProvider := config.ResolveReviewerProviderSettings(*settings)
		if reviewerProviderSourceDefault {
			settings.Reviewer.ProviderOverride = reviewerProvider.ProviderOverride
		} else {
			settings.Reviewer.ProviderOverride = originalProviderOverride
		}
		if reviewerBaseURLSourceDefault {
			settings.Reviewer.OpenAIBaseURL = reviewerProvider.OpenAIBaseURL
		} else {
			settings.Reviewer.OpenAIBaseURL = originalOpenAIBaseURL
		}
	}
	if strings.TrimSpace(sources["reviewer.model_context_window"]) == "default" {
		settings.Reviewer.ModelContextWindow = settings.ModelContextWindow
	}
	if strings.TrimSpace(sources["reviewer.auth"]) == "default" {
		settings.Reviewer.Auth = "inherit"
	}
	applyReviewerModelCapabilityInheritance(settings, sources)
	reviewerProviderSelectionExplicit := reviewerProviderSelectionExplicitForInheritance(settings, sources)
	applyReviewerProviderCapabilityInheritance(settings, sources, reviewerProviderSelectionExplicit)
}

func reviewerProviderSelectionExplicitForInheritance(settings *config.Settings, sources map[string]string) bool {
	if settings == nil {
		return false
	}
	candidate := *settings
	if strings.TrimSpace(sources["reviewer.provider_override"]) == "default" {
		candidate.Reviewer.ProviderOverride = ""
	}
	if strings.TrimSpace(sources["reviewer.openai_base_url"]) == "default" {
		candidate.Reviewer.OpenAIBaseURL = ""
	}
	return config.ReviewerUsesIndependentProviderSelection(candidate)
}

func applyReviewerModelCapabilityInheritance(settings *config.Settings, sources map[string]string) {
	if settings == nil {
		return
	}
	if strings.TrimSpace(sources["reviewer.model_capabilities.supports_reasoning_effort"]) == "default" {
		settings.Reviewer.ModelCapabilities.SupportsReasoningEffort = settings.ModelCapabilities.SupportsReasoningEffort
	}
	if strings.TrimSpace(sources["reviewer.model_capabilities.supports_vision_inputs"]) == "default" {
		settings.Reviewer.ModelCapabilities.SupportsVisionInputs = settings.ModelCapabilities.SupportsVisionInputs
	}
}

func applyReviewerProviderCapabilityInheritance(settings *config.Settings, sources map[string]string, reviewerProviderSelectionExplicit bool) {
	if settings == nil || reviewerProviderSelectionExplicit {
		return
	}
	if strings.TrimSpace(sources["reviewer.provider_capabilities.provider_id"]) == "default" {
		settings.Reviewer.ProviderCapabilities.ProviderID = settings.ProviderCapabilities.ProviderID
	}
	if strings.TrimSpace(sources["reviewer.provider_capabilities.supports_responses_api"]) == "default" {
		settings.Reviewer.ProviderCapabilities.SupportsResponsesAPI = settings.ProviderCapabilities.SupportsResponsesAPI
	}
	if strings.TrimSpace(sources["reviewer.provider_capabilities.supports_responses_compact"]) == "default" {
		settings.Reviewer.ProviderCapabilities.SupportsResponsesCompact = settings.ProviderCapabilities.SupportsResponsesCompact
	}
	if strings.TrimSpace(sources["reviewer.provider_capabilities.supports_request_input_token_count"]) == "default" {
		settings.Reviewer.ProviderCapabilities.SupportsRequestInputTokenCount = settings.ProviderCapabilities.SupportsRequestInputTokenCount
	}
	if strings.TrimSpace(sources["reviewer.provider_capabilities.supports_prompt_cache_key"]) == "default" {
		settings.Reviewer.ProviderCapabilities.SupportsPromptCacheKey = settings.ProviderCapabilities.SupportsPromptCacheKey
	}
	if strings.TrimSpace(sources["reviewer.provider_capabilities.supports_native_web_search"]) == "default" {
		settings.Reviewer.ProviderCapabilities.SupportsNativeWebSearch = settings.ProviderCapabilities.SupportsNativeWebSearch
	}
	if strings.TrimSpace(sources["reviewer.provider_capabilities.supports_reasoning_encrypted"]) == "default" {
		settings.Reviewer.ProviderCapabilities.SupportsReasoningEncrypted = settings.ProviderCapabilities.SupportsReasoningEncrypted
	}
	if strings.TrimSpace(sources["reviewer.provider_capabilities.supports_server_side_context_edit"]) == "default" {
		settings.Reviewer.ProviderCapabilities.SupportsServerSideContextEdit = settings.ProviderCapabilities.SupportsServerSideContextEdit
	}
	if strings.TrimSpace(sources["reviewer.provider_capabilities.supports_provider_verbosity"]) == "default" {
		settings.Reviewer.ProviderCapabilities.SupportsProviderVerbosity = settings.ProviderCapabilities.SupportsProviderVerbosity
	}
	if strings.TrimSpace(sources["reviewer.provider_capabilities.is_openai_first_party"]) == "default" {
		settings.Reviewer.ProviderCapabilities.IsOpenAIFirstParty = settings.ProviderCapabilities.IsOpenAIFirstParty
	}
}

var reviewerRoleOverrideApplicators = map[string]func(*config.Settings, config.Settings){
	"reviewer.frequency": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.Frequency = role.Reviewer.Frequency
	},
	"reviewer.model": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.Model = role.Reviewer.Model
	},
	"reviewer.thinking_level": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ThinkingLevel = role.Reviewer.ThinkingLevel
	},
	"reviewer.model_verbosity": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ModelVerbosity = role.Reviewer.ModelVerbosity
	},
	"reviewer.provider_override": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ProviderOverride = role.Reviewer.ProviderOverride
	},
	"reviewer.openai_base_url": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.OpenAIBaseURL = role.Reviewer.OpenAIBaseURL
	},
	"reviewer.model_context_window": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ModelContextWindow = role.Reviewer.ModelContextWindow
	},
	"reviewer.auth": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.Auth = role.Reviewer.Auth
	},
	"reviewer.model_capabilities.supports_reasoning_effort": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ModelCapabilities.SupportsReasoningEffort = role.Reviewer.ModelCapabilities.SupportsReasoningEffort
	},
	"reviewer.model_capabilities.supports_vision_inputs": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ModelCapabilities.SupportsVisionInputs = role.Reviewer.ModelCapabilities.SupportsVisionInputs
	},
	"reviewer.provider_capabilities.provider_id": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ProviderCapabilities.ProviderID = role.Reviewer.ProviderCapabilities.ProviderID
	},
	"reviewer.provider_capabilities.supports_responses_api": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ProviderCapabilities.SupportsResponsesAPI = role.Reviewer.ProviderCapabilities.SupportsResponsesAPI
	},
	"reviewer.provider_capabilities.supports_responses_compact": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ProviderCapabilities.SupportsResponsesCompact = role.Reviewer.ProviderCapabilities.SupportsResponsesCompact
	},
	"reviewer.provider_capabilities.supports_request_input_token_count": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ProviderCapabilities.SupportsRequestInputTokenCount = role.Reviewer.ProviderCapabilities.SupportsRequestInputTokenCount
	},
	"reviewer.provider_capabilities.supports_prompt_cache_key": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ProviderCapabilities.SupportsPromptCacheKey = role.Reviewer.ProviderCapabilities.SupportsPromptCacheKey
	},
	"reviewer.provider_capabilities.supports_native_web_search": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ProviderCapabilities.SupportsNativeWebSearch = role.Reviewer.ProviderCapabilities.SupportsNativeWebSearch
	},
	"reviewer.provider_capabilities.supports_reasoning_encrypted": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ProviderCapabilities.SupportsReasoningEncrypted = role.Reviewer.ProviderCapabilities.SupportsReasoningEncrypted
	},
	"reviewer.provider_capabilities.supports_server_side_context_edit": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ProviderCapabilities.SupportsServerSideContextEdit = role.Reviewer.ProviderCapabilities.SupportsServerSideContextEdit
	},
	"reviewer.provider_capabilities.supports_provider_verbosity": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ProviderCapabilities.SupportsProviderVerbosity = role.Reviewer.ProviderCapabilities.SupportsProviderVerbosity
	},
	"reviewer.provider_capabilities.is_openai_first_party": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.ProviderCapabilities.IsOpenAIFirstParty = role.Reviewer.ProviderCapabilities.IsOpenAIFirstParty
	},
	"reviewer.system_prompt_file": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.SystemPromptFile = role.Reviewer.SystemPromptFile
	},
	"reviewer.timeout_seconds": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.TimeoutSeconds = role.Reviewer.TimeoutSeconds
	},
	"reviewer.verbose_output": func(settings *config.Settings, role config.Settings) {
		settings.Reviewer.VerboseOutput = role.Reviewer.VerboseOutput
	},
}

func applyReviewerRoleOverride(settings *config.Settings, role config.Settings, key string) bool {
	apply, ok := reviewerRoleOverrideApplicators[key]
	if !ok {
		return false
	}
	apply(settings, role)
	return true
}

func cloneSettings(in config.Settings) config.Settings {
	out := in
	out.Shell.PostprocessHook = valuecopy.Pointer(in.Shell.PostprocessHook)
	out.SystemPromptFiles = append([]config.SystemPromptFile(nil), in.SystemPromptFiles...)
	out.EnabledTools = cloneEnabledToolSet(in.EnabledTools)
	out.SkillToggles = cloneStringBoolMap(in.SkillToggles)
	out.Subagents = cloneSubagentRoles(in.Subagents)
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneStringBoolMap(in map[string]bool) map[string]bool {
	if len(in) == 0 {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneSubagentRoles(in map[string]config.SubagentRole) map[string]config.SubagentRole {
	if len(in) == 0 {
		return map[string]config.SubagentRole{}
	}
	out := make(map[string]config.SubagentRole, len(in))
	for key, role := range in {
		copied := role
		copied.Settings = cloneSettings(role.Settings)
		copied.Sources = cloneStringMap(role.Sources)
		out[key] = copied
	}
	return out
}

func sameResolvedSubagentSettings(base config.Settings, resolved config.Settings) bool {
	left := normalizeComparableSettings(base)
	right := normalizeComparableSettings(resolved)
	return reflect.DeepEqual(left, right)
}

func normalizeComparableSettings(settings config.Settings) config.Settings {
	normalized := cloneSettings(settings)
	normalized.Subagents = nil
	if len(normalized.SkillToggles) == 0 {
		normalized.SkillToggles = nil
	}
	return normalized
}
