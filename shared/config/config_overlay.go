package config

import (
	"fmt"
	"strconv"
	"strings"

	"core/shared/toolspec"
)

func inheritReviewerDefaultsWithSources(settings *Settings, sources map[string]string) {
	reviewerProviderSelectionExplicit := ReviewerUsesIndependentProviderSelection(*settings)
	if strings.TrimSpace(settings.Reviewer.Model) == "" {
		settings.Reviewer.Model = settings.Model
	}
	if strings.TrimSpace(settings.Reviewer.ThinkingLevel) == "" && !hasConfiguredSource(sources, "reviewer.thinking_level") {
		settings.Reviewer.ThinkingLevel = settings.ThinkingLevel
	}
	if strings.TrimSpace(string(settings.Reviewer.ModelVerbosity)) == "" {
		settings.Reviewer.ModelVerbosity = settings.ModelVerbosity
	}
	reviewerProvider := ResolveReviewerProviderSettings(*settings)
	settings.Reviewer.ProviderOverride = reviewerProvider.ProviderOverride
	settings.Reviewer.OpenAIBaseURL = reviewerProvider.OpenAIBaseURL
	inheritReviewerModelCapabilities(settings, sources)
	inheritReviewerProviderCapabilities(settings, sources, reviewerProviderSelectionExplicit)
	if settings.Reviewer.ModelContextWindow == 0 && !hasConfiguredSource(sources, "reviewer.model_context_window") {
		settings.Reviewer.ModelContextWindow = settings.ModelContextWindow
	}
}

func ReviewerUsesIndependentProviderSelection(settings Settings) bool {
	if strings.TrimSpace(settings.Reviewer.OpenAIBaseURL) != "" {
		return true
	}
	reviewerProvider := strings.ToLower(strings.TrimSpace(settings.Reviewer.ProviderOverride))
	if reviewerProvider == "" {
		return false
	}
	mainProvider := strings.ToLower(strings.TrimSpace(settings.ProviderOverride))
	if mainProvider == "" && reviewerProvider == "openai" {
		return false
	}
	return reviewerProvider != mainProvider
}

func ResolveReviewerProviderSettings(settings Settings) ReviewerProviderSettings {
	provider := strings.TrimSpace(settings.Reviewer.ProviderOverride)
	if provider == "" {
		provider = strings.TrimSpace(settings.ProviderOverride)
	}
	baseURL := strings.TrimSpace(settings.Reviewer.OpenAIBaseURL)
	if baseURL == "" && shouldInheritMainOpenAIBaseURL(provider) {
		baseURL = strings.TrimSpace(settings.OpenAIBaseURL)
	}
	return ReviewerProviderSettings{ProviderOverride: provider, OpenAIBaseURL: baseURL}
}

func shouldInheritMainOpenAIBaseURL(reviewerProvider string) bool {
	switch strings.ToLower(strings.TrimSpace(reviewerProvider)) {
	case "", "openai":
		return true
	default:
		return false
	}
}

func inheritReviewerModelCapabilities(settings *Settings, sources map[string]string) {
	if sources == nil {
		if !settings.Reviewer.ModelCapabilities.SupportsReasoningEffort && !settings.Reviewer.ModelCapabilities.SupportsVisionInputs {
			settings.Reviewer.ModelCapabilities = settings.ModelCapabilities
		}
		return
	}
	if !hasAnyConfiguredSource(sources, modelCapabilityKeys...) && !hasAnyConfiguredSource(sources, reviewerModelCapabilityKeys...) {
		return
	}
	if !hasConfiguredSource(sources, "reviewer.model_capabilities.supports_reasoning_effort") {
		settings.Reviewer.ModelCapabilities.SupportsReasoningEffort = settings.ModelCapabilities.SupportsReasoningEffort
	}
	if !hasConfiguredSource(sources, "reviewer.model_capabilities.supports_vision_inputs") {
		settings.Reviewer.ModelCapabilities.SupportsVisionInputs = settings.ModelCapabilities.SupportsVisionInputs
	}
}

func hasProviderCapabilitiesOverride(override ProviderCapabilitiesOverride) bool {
	return strings.TrimSpace(override.ProviderID) != "" ||
		override.SupportsResponsesAPI ||
		override.SupportsResponsesCompact ||
		override.SupportsRequestInputTokenCount ||
		override.SupportsPromptCacheKey ||
		override.SupportsNativeWebSearch ||
		override.SupportsReasoningEncrypted ||
		override.SupportsServerSideContextEdit ||
		override.SupportsProviderVerbosity ||
		override.IsOpenAIFirstParty
}

func inheritReviewerProviderCapabilities(settings *Settings, sources map[string]string, reviewerProviderSelectionExplicit bool) {
	if sources == nil {
		if !hasProviderCapabilitiesOverride(settings.Reviewer.ProviderCapabilities) && !reviewerProviderSelectionExplicit {
			settings.Reviewer.ProviderCapabilities = settings.ProviderCapabilities
		}
		return
	}
	if !hasAnyConfiguredSource(sources, providerCapabilityKeys...) && !hasAnyConfiguredSource(sources, reviewerProviderCapabilityKeys...) {
		return
	}
	if !hasAnyConfiguredSource(sources, reviewerProviderCapabilityKeys...) {
		if !reviewerProviderSelectionExplicit {
			settings.Reviewer.ProviderCapabilities = settings.ProviderCapabilities
		}
		return
	}
	if reviewerProviderSelectionExplicit {
		return
	}
	if !hasConfiguredSource(sources, "reviewer.provider_capabilities.provider_id") {
		settings.Reviewer.ProviderCapabilities.ProviderID = settings.ProviderCapabilities.ProviderID
	}
	if !hasConfiguredSource(sources, "reviewer.provider_capabilities.supports_responses_api") {
		settings.Reviewer.ProviderCapabilities.SupportsResponsesAPI = settings.ProviderCapabilities.SupportsResponsesAPI
	}
	if !hasConfiguredSource(sources, "reviewer.provider_capabilities.supports_responses_compact") {
		settings.Reviewer.ProviderCapabilities.SupportsResponsesCompact = settings.ProviderCapabilities.SupportsResponsesCompact
	}
	if !hasConfiguredSource(sources, "reviewer.provider_capabilities.supports_request_input_token_count") {
		settings.Reviewer.ProviderCapabilities.SupportsRequestInputTokenCount = settings.ProviderCapabilities.SupportsRequestInputTokenCount
	}
	if !hasConfiguredSource(sources, "reviewer.provider_capabilities.supports_prompt_cache_key") {
		settings.Reviewer.ProviderCapabilities.SupportsPromptCacheKey = settings.ProviderCapabilities.SupportsPromptCacheKey
	}
	if !hasConfiguredSource(sources, "reviewer.provider_capabilities.supports_native_web_search") {
		settings.Reviewer.ProviderCapabilities.SupportsNativeWebSearch = settings.ProviderCapabilities.SupportsNativeWebSearch
	}
	if !hasConfiguredSource(sources, "reviewer.provider_capabilities.supports_reasoning_encrypted") {
		settings.Reviewer.ProviderCapabilities.SupportsReasoningEncrypted = settings.ProviderCapabilities.SupportsReasoningEncrypted
	}
	if !hasConfiguredSource(sources, "reviewer.provider_capabilities.supports_server_side_context_edit") {
		settings.Reviewer.ProviderCapabilities.SupportsServerSideContextEdit = settings.ProviderCapabilities.SupportsServerSideContextEdit
	}
	if !hasConfiguredSource(sources, "reviewer.provider_capabilities.supports_provider_verbosity") {
		settings.Reviewer.ProviderCapabilities.SupportsProviderVerbosity = settings.ProviderCapabilities.SupportsProviderVerbosity
	}
	if !hasConfiguredSource(sources, "reviewer.provider_capabilities.is_openai_first_party") {
		settings.Reviewer.ProviderCapabilities.IsOpenAIFirstParty = settings.ProviderCapabilities.IsOpenAIFirstParty
	}
}

var modelCapabilityKeys = []string{
	"model_capabilities.supports_reasoning_effort",
	"model_capabilities.supports_vision_inputs",
}

var reviewerModelCapabilityKeys = []string{
	"reviewer.model_capabilities.supports_reasoning_effort",
	"reviewer.model_capabilities.supports_vision_inputs",
}

type providerCapabilitySourceKeys struct {
	all    []string
	values []string
}

func newProviderCapabilitySourceKeys(providerID string, values ...string) providerCapabilitySourceKeys {
	all := make([]string, 0, len(values)+1)
	all = append(all, providerID)
	all = append(all, values...)
	return providerCapabilitySourceKeys{
		all:    all,
		values: values,
	}
}

var mainProviderCapabilitySourceKeys = newProviderCapabilitySourceKeys(
	"provider_capabilities.provider_id",
	"provider_capabilities.supports_responses_api",
	"provider_capabilities.supports_responses_compact",
	"provider_capabilities.supports_request_input_token_count",
	"provider_capabilities.supports_prompt_cache_key",
	"provider_capabilities.supports_native_web_search",
	"provider_capabilities.supports_reasoning_encrypted",
	"provider_capabilities.supports_server_side_context_edit",
	"provider_capabilities.supports_provider_verbosity",
	"provider_capabilities.is_openai_first_party",
)

var reviewerProviderCapabilitySourceKeys = newProviderCapabilitySourceKeys(
	"reviewer.provider_capabilities.provider_id",
	"reviewer.provider_capabilities.supports_responses_api",
	"reviewer.provider_capabilities.supports_responses_compact",
	"reviewer.provider_capabilities.supports_request_input_token_count",
	"reviewer.provider_capabilities.supports_prompt_cache_key",
	"reviewer.provider_capabilities.supports_native_web_search",
	"reviewer.provider_capabilities.supports_reasoning_encrypted",
	"reviewer.provider_capabilities.supports_server_side_context_edit",
	"reviewer.provider_capabilities.supports_provider_verbosity",
	"reviewer.provider_capabilities.is_openai_first_party",
)

var providerCapabilityKeys = mainProviderCapabilitySourceKeys.all

var reviewerProviderCapabilityKeys = reviewerProviderCapabilitySourceKeys.all

func hasAnyConfiguredSource(sources map[string]string, keys ...string) bool {
	for _, key := range keys {
		if hasConfiguredSource(sources, key) {
			return true
		}
	}
	return false
}

func NormalizeSettingsForPersistenceWithSources(settings Settings, sources map[string]string) (Settings, error) {
	normalized := settings
	if normalized.EnabledTools == nil {
		normalized.EnabledTools = defaultEnabledToolMap()
	}
	if normalized.SkillToggles == nil {
		normalized.SkillToggles = map[string]bool{}
	}
	effectiveSources := cloneSourceMapOrDefault(sources)
	inheritReviewerDefaultsWithSources(&normalized, effectiveSources)
	if err := configRegistry.validate(settingsState{Settings: normalized}, effectiveSources); err != nil {
		return Settings{}, err
	}
	return normalized, nil
}

func cloneSourceMapOrDefault(sources map[string]string) map[string]string {
	if len(sources) == 0 {
		out := configRegistry.defaultSourceMap()
		out["model"] = "file"
		return out
	}
	out := make(map[string]string, len(sources)+1)
	for key, value := range sources {
		out[key] = value
	}
	if strings.TrimSpace(out["model"]) == "" {
		out["model"] = "file"
	}
	return out
}

func ValidateSettingsWithSources(settings Settings, sources map[string]string) error {
	return configRegistry.validate(settingsState{Settings: settings}, sources)
}

func parseEnabledToolsCSV(raw string) ([]toolspec.ID, error) {
	parts := strings.Split(raw, ",")
	seen := map[toolspec.ID]bool{}
	out := make([]toolspec.ID, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		id, ok := toolspec.ParseConfigID(name)
		if !ok {
			return nil, fmt.Errorf("unknown tool %q", name)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func resetEnabledToolMap(enabled []toolspec.ID) map[toolspec.ID]bool {
	out := make(map[toolspec.ID]bool, len(toolspec.CatalogIDs()))
	for _, id := range toolspec.CatalogIDs() {
		out[id] = false
	}
	for _, id := range enabled {
		out[id] = true
	}
	return out
}

func DisabledSkillToggles(settings Settings) map[string]bool {
	if len(settings.SkillToggles) == 0 {
		return nil
	}
	disabled := make(map[string]bool, len(settings.SkillToggles))
	for name, enabled := range settings.SkillToggles {
		if enabled {
			continue
		}
		normalized := NormalizeSkillName(name)
		if normalized == "" {
			continue
		}
		disabled[normalized] = true
	}
	if len(disabled) == 0 {
		return nil
	}
	return disabled
}

func parsePositiveIntString(raw string, envName string) (*int, error) {
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return nil, fmt.Errorf("invalid %s: %q", envName, raw)
	}
	return &parsed, nil
}
