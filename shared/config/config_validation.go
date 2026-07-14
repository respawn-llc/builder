package config

import (
	"core/shared/theme"
	"errors"
	"fmt"
	"strings"
)

func validateSubagentRoleState(state settingsState, sources map[string]string) error {
	if len(sources) == 0 {
		return nil
	}
	candidate := state
	inheritReviewerDefaultsWithSources(&candidate.Settings, sources)

	checks := []struct {
		enabled bool
		check   settingsValidator
	}{
		{enabled: hasExplicitSource(sources, "model"), check: validateModelNotEmpty},
		{enabled: hasExplicitSource(sources, "provider_override"), check: validateProviderOverrideValue},
		{enabled: hasExplicitSource(sources, "provider_override", "openai_base_url"), check: validateOpenAIBaseURL},
		{enabled: hasExplicitPrefix(sources, "provider_capabilities."), check: validateProviderCapabilitiesProviderID},
		{enabled: hasExplicitSource(sources, "model_verbosity"), check: validateModelVerbosity},
		{enabled: hasExplicitSource(sources, "theme"), check: validateTheme},
		{enabled: hasExplicitSource(sources, "notification_method"), check: validateNotificationMethod},
		{enabled: hasExplicitSource(sources, "server_host"), check: validateServerHost},
		{enabled: hasExplicitSource(sources, "server_port"), check: validateServerPort},
		{enabled: hasExplicitSource(sources, "web_search"), check: validateWebSearch},
		{enabled: hasExplicitSource(sources, "timeouts.model_request_seconds"), check: validateTimeouts},
		{enabled: hasExplicitSource(sources, "shell_output_max_chars"), check: validateShellOutputMaxChars},
		{enabled: hasExplicitSource(sources, "minimum_exec_to_bg_seconds"), check: validateMinimumExecToBgSeconds},
		{enabled: hasExplicitSource(sources, "bg_shells_output"), check: validateBGShellsOutput},
		{enabled: hasExplicitSource(sources, "shell.postprocessing_mode", "shell.postprocess_hook"), check: validateShellPostprocessing},
		{enabled: hasExplicitSource(sources, "cache_warning_mode"), check: validateCacheWarningMode},
		{enabled: hasExplicitSource(sources, "compaction_mode"), check: validateCompactionMode},
		{enabled: hasExplicitPrefix(sources, "workflow."), check: validateWorkflowSettings},
		{enabled: hasExplicitPrefix(sources, "reviewer."), check: validateReviewer},
		{enabled: hasExplicitSource(sources, "prevent_sleep"), check: validateSleepPreventionMode},
	}
	for _, check := range checks {
		if !check.enabled {
			continue
		}
		if err := check.check(candidate, sources); err != nil {
			return err
		}
	}
	if err := validateSubagentRoleContext(candidate, sources); err != nil {
		return err
	}
	return nil
}

func validateSubagentRoleContext(state settingsState, sources map[string]string) error {
	hasWindow := hasExplicitSource(sources, "model_context_window")
	hasThreshold := hasExplicitSource(sources, "context_compaction_threshold_tokens")
	hasLead := hasExplicitSource(sources, "pre_submit_compaction_lead_tokens")
	if !hasWindow && !hasThreshold && !hasLead {
		return nil
	}
	if hasWindow {
		if err := validateModelContextWindowMinimum("model_context_window", state.Settings.ModelContextWindow); err != nil {
			return err
		}
	}
	if hasThreshold && state.Settings.ContextCompactionThresholdTokens <= 0 {
		return fmt.Errorf("context_compaction_threshold_tokens must be > 0")
	}
	if hasLead && state.Settings.PreSubmitCompactionLeadTokens <= 0 {
		return fmt.Errorf("pre_submit_compaction_lead_tokens must be > 0")
	}
	if !hasWindow || !hasThreshold {
		return nil
	}
	if state.Settings.ContextCompactionThresholdTokens >= state.Settings.ModelContextWindow {
		return fmt.Errorf("context_compaction_threshold_tokens must be < model_context_window")
	}
	minimumThreshold := MinimumThresholdTokens(state.Settings.ModelContextWindow)
	if state.Settings.ContextCompactionThresholdTokens < minimumThreshold {
		return fmt.Errorf(
			"%w: context_compaction_threshold_tokens must be >= %d (%d%% of model_context_window=%d)",
			errCompactionThresholdBelowMinimum,
			minimumThreshold,
			MinimumWindowPercent,
			state.Settings.ModelContextWindow,
		)
	}
	if !hasLead {
		return nil
	}
	effectivePreSubmitThreshold := EffectivePreSubmitThresholdTokens(
		state.Settings.ContextCompactionThresholdTokens,
		state.Settings.PreSubmitCompactionLeadTokens,
	)
	if effectivePreSubmitThreshold < minimumThreshold {
		return fmt.Errorf(
			"%w: pre_submit_compaction_lead_tokens makes the effective pre-submit threshold %d, below %d (%d%% of model_context_window=%d)",
			errPreSubmitThresholdBelowMinimum,
			effectivePreSubmitThreshold,
			minimumThreshold,
			MinimumWindowPercent,
			state.Settings.ModelContextWindow,
		)
	}
	return nil
}

func hasExplicitSource(sources map[string]string, keys ...string) bool {
	for _, key := range keys {
		if strings.TrimSpace(sources[key]) == "file" {
			return true
		}
	}
	return false
}

func hasExplicitPrefix(sources map[string]string, prefix string) bool {
	for key, source := range sources {
		if strings.TrimSpace(source) != "file" {
			continue
		}
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func validateModelNotEmpty(state settingsState, _ map[string]string) error {
	if strings.TrimSpace(state.Settings.Model) == "" {
		return errors.New("settings model must not be empty")
	}
	return nil
}

func validateProviderOverrideRequiresModel(state settingsState, sources map[string]string) error {
	if strings.TrimSpace(state.Settings.ProviderOverride) != "" && strings.TrimSpace(sources["model"]) == "default" {
		return fmt.Errorf("%w; set model alongside provider_override", errProviderOverrideRequiresModel)
	}
	return nil
}

func validateProviderOverrideValue(state settingsState, _ map[string]string) error {
	switch strings.ToLower(strings.TrimSpace(state.Settings.ProviderOverride)) {
	case "", "openai", "anthropic":
		return nil
	default:
		return fmt.Errorf("%w %q (expected openai|anthropic)", errInvalidProviderOverride, state.Settings.ProviderOverride)
	}
}

func validateProviderIdentifier(state settingsState, _ map[string]string) error {
	identifier := state.Settings.ProviderIdentifier
	if identifier == "" {
		return fmt.Errorf("%w: value must not be empty", errInvalidProviderIdentifier)
	}
	for i := 0; i < len(identifier); i++ {
		if !isHTTPProductTokenByte(identifier[i]) {
			return fmt.Errorf("%w %q: expected an HTTP product token", errInvalidProviderIdentifier, identifier)
		}
	}
	return nil
}

func isHTTPProductTokenByte(value byte) bool {
	switch {
	case value >= '0' && value <= '9':
		return true
	case value >= 'A' && value <= 'Z':
		return true
	case value >= 'a' && value <= 'z':
		return true
	}
	switch value {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

func validateOpenAIBaseURL(state settingsState, _ map[string]string) error {
	provider := strings.ToLower(strings.TrimSpace(state.Settings.ProviderOverride))
	if strings.TrimSpace(state.Settings.OpenAIBaseURL) != "" && provider != "" && provider != "openai" {
		return fmt.Errorf("%w: provider_override %q; openai_base_url requires provider_override=openai or unset", errOpenAIBaseURLConflict, state.Settings.ProviderOverride)
	}
	return nil
}

func validateProviderCapabilitiesProviderID(state settingsState, sources map[string]string) error {
	capabilities := state.Settings.ProviderCapabilities
	if strings.TrimSpace(capabilities.ProviderID) != "" {
		return nil
	}
	if hasAnyConfiguredSource(sources, mainProviderCapabilitySourceKeys.values...) || hasProviderCapabilitiesOverride(capabilities) {
		return errProviderCapabilitiesNeedID
	}
	return nil
}

func validateModelVerbosity(state settingsState, _ map[string]string) error {
	switch strings.ToLower(strings.TrimSpace(string(state.Settings.ModelVerbosity))) {
	case "", "low", "medium", "high":
		return nil
	default:
		return fmt.Errorf("%w %q (expected low|medium|high)", errInvalidModelVerbosity, state.Settings.ModelVerbosity)
	}
}

func validateTheme(state settingsState, _ map[string]string) error {
	switch theme.Normalize(state.Settings.Theme) {
	case theme.Auto, theme.Light, theme.Dark:
		return nil
	default:
		return fmt.Errorf("invalid theme %q (expected auto|light|dark)", state.Settings.Theme)
	}
}

func validateNotificationMethod(state settingsState, _ map[string]string) error {
	switch strings.ToLower(strings.TrimSpace(state.Settings.NotificationMethod)) {
	case "auto", "osc9", "bel":
		return nil
	default:
		return fmt.Errorf("invalid notification_method %q (expected auto|osc9|bel)", state.Settings.NotificationMethod)
	}
}

func validateServerHost(state settingsState, _ map[string]string) error {
	if strings.TrimSpace(state.Settings.ServerHost) == "" {
		return fmt.Errorf("server_host must not be empty")
	}
	return nil
}

func validateServerPort(state settingsState, _ map[string]string) error {
	if state.Settings.ServerPort <= 0 || state.Settings.ServerPort > 65535 {
		return fmt.Errorf("server_port must be between 1 and 65535")
	}
	return nil
}

func validateWebSearch(state settingsState, _ map[string]string) error {
	switch strings.ToLower(strings.TrimSpace(state.Settings.WebSearch)) {
	case "off", "native":
		return nil
	case "custom":
		return fmt.Errorf("web_search=custom is not implemented yet")
	default:
		return fmt.Errorf("invalid web_search %q (expected off|native|custom)", state.Settings.WebSearch)
	}
}

func validateTimeouts(state settingsState, _ map[string]string) error {
	if state.Settings.Timeouts.ModelRequestSeconds <= 0 {
		return fmt.Errorf("timeouts.model_request_seconds must be > 0")
	}
	return nil
}

func validateShellOutputMaxChars(state settingsState, _ map[string]string) error {
	if state.Settings.ShellOutputMaxChars <= 0 {
		return fmt.Errorf("shell_output_max_chars must be > 0")
	}
	return nil
}

func validateMinimumExecToBgSeconds(state settingsState, _ map[string]string) error {
	if state.Settings.MinimumExecToBgSeconds <= 0 {
		return fmt.Errorf("minimum_exec_to_bg_seconds must be > 0")
	}
	return nil
}

func validateBGShellsOutput(state settingsState, _ map[string]string) error {
	switch strings.ToLower(strings.TrimSpace(string(state.Settings.BGShellsOutput))) {
	case "default", "verbose", "concise":
		return nil
	default:
		return fmt.Errorf("invalid bg_shells_output %q (expected default|verbose|concise)", state.Settings.BGShellsOutput)
	}
}

func validateShellPostprocessing(state settingsState, _ map[string]string) error {
	switch normalizeShellPostprocessingMode(string(state.Settings.Shell.PostprocessingMode)) {
	case ShellPostprocessingModeNone, ShellPostprocessingModeBuiltin, ShellPostprocessingModeUser, ShellPostprocessingModeAll:
	default:
		return fmt.Errorf("invalid shell.postprocessing_mode %q (expected none|builtin|user|all)", state.Settings.Shell.PostprocessingMode)
	}
	if state.Settings.Shell.PostprocessHook != nil && strings.TrimSpace(*state.Settings.Shell.PostprocessHook) == "" {
		return fmt.Errorf("shell.postprocess_hook cannot be empty; remove the setting to leave it unset")
	}
	return nil
}

func validateCacheWarningMode(state settingsState, _ map[string]string) error {
	switch strings.ToLower(strings.TrimSpace(string(state.Settings.CacheWarningMode))) {
	case "off", "default", "verbose":
		return nil
	default:
		return fmt.Errorf("%w %q (expected off|default|verbose)", errInvalidCacheWarningMode, state.Settings.CacheWarningMode)
	}
}

func validateWorkflowSettings(state settingsState, _ map[string]string) error {
	if state.Settings.Workflow.CompletionMode == "" &&
		state.Settings.Workflow.Concurrency == 0 &&
		state.Settings.Workflow.MaxInvalidCompletionAttempts == 0 {
		return nil
	}
	switch state.Settings.Workflow.CompletionMode {
	case WorkflowCompletionModeAuto, WorkflowCompletionModeStructuredOutput, WorkflowCompletionModeTool, WorkflowCompletionModeShellCommand, WorkflowCompletionModeUnstructured:
	default:
		return fmt.Errorf("%w: invalid workflow.completion_mode %q (expected auto|structured_output|tool|shell_command|unstructured_output)", errInvalidWorkflowSettings, state.Settings.Workflow.CompletionMode)
	}
	if state.Settings.Workflow.Concurrency <= 0 {
		return fmt.Errorf("%w: %w must be > 0", errInvalidWorkflowSettings, errWorkflowConcurrency)
	}
	if state.Settings.Workflow.MaxInvalidCompletionAttempts <= 0 {
		return fmt.Errorf("%w: workflow.max_invalid_completion_attempts must be > 0", errInvalidWorkflowSettings)
	}
	return nil
}

func validateContextWindow(state settingsState, _ map[string]string) error {
	if state.Settings.ContextCompactionThresholdTokens <= 0 {
		return fmt.Errorf("context_compaction_threshold_tokens must be > 0")
	}
	if err := validateModelContextWindowMinimum("model_context_window", state.Settings.ModelContextWindow); err != nil {
		return err
	}
	if state.Settings.ContextCompactionThresholdTokens >= state.Settings.ModelContextWindow {
		return fmt.Errorf("context_compaction_threshold_tokens must be < model_context_window")
	}
	if state.Settings.PreSubmitCompactionLeadTokens <= 0 {
		return fmt.Errorf("pre_submit_compaction_lead_tokens must be > 0")
	}
	minimumThreshold := MinimumThresholdTokens(state.Settings.ModelContextWindow)
	if state.Settings.ContextCompactionThresholdTokens < minimumThreshold {
		return fmt.Errorf(
			"%w: context_compaction_threshold_tokens must be >= %d (%d%% of model_context_window=%d)",
			errCompactionThresholdBelowMinimum,
			minimumThreshold,
			MinimumWindowPercent,
			state.Settings.ModelContextWindow,
		)
	}
	effectivePreSubmitThreshold := EffectivePreSubmitThresholdTokens(
		state.Settings.ContextCompactionThresholdTokens,
		state.Settings.PreSubmitCompactionLeadTokens,
	)
	if effectivePreSubmitThreshold < minimumThreshold {
		return fmt.Errorf(
			"%w: pre_submit_compaction_lead_tokens makes the effective pre-submit threshold %d, below %d (%d%% of model_context_window=%d)",
			errPreSubmitThresholdBelowMinimum,
			effectivePreSubmitThreshold,
			minimumThreshold,
			MinimumWindowPercent,
			state.Settings.ModelContextWindow,
		)
	}
	return nil
}

func validateCompactionMode(state settingsState, _ map[string]string) error {
	switch strings.ToLower(strings.TrimSpace(string(state.Settings.CompactionMode))) {
	case "native", "local", "none":
		return nil
	default:
		return fmt.Errorf("invalid compaction_mode %q (expected native|local|none)", state.Settings.CompactionMode)
	}
}

func validateReviewer(state settingsState, sources map[string]string) error {
	reviewer := state.Settings.Reviewer
	switch strings.ToLower(strings.TrimSpace(reviewer.Frequency)) {
	case "off", "all", "edits":
	default:
		return fmt.Errorf("invalid reviewer.frequency %q (expected off|all|edits)", reviewer.Frequency)
	}
	if strings.TrimSpace(reviewer.Model) == "" {
		return fmt.Errorf("reviewer.model must not be empty")
	}
	switch strings.ToLower(strings.TrimSpace(string(reviewer.ModelVerbosity))) {
	case "", "low", "medium", "high":
	default:
		return fmt.Errorf("invalid reviewer.model_verbosity %q (expected low|medium|high)", reviewer.ModelVerbosity)
	}
	provider := strings.ToLower(strings.TrimSpace(reviewer.ProviderOverride))
	switch provider {
	case "", "openai", "anthropic":
	default:
		return fmt.Errorf("%w %q (expected openai|anthropic)", errInvalidReviewerProvider, reviewer.ProviderOverride)
	}
	if strings.TrimSpace(reviewer.OpenAIBaseURL) != "" && provider != "" && provider != "openai" {
		return fmt.Errorf("reviewer.provider_override %q conflicts with reviewer.openai_base_url; reviewer.openai_base_url requires reviewer.provider_override=openai or unset", reviewer.ProviderOverride)
	}
	if err := validateReviewerProviderCapabilities(reviewer.ProviderCapabilities, sources); err != nil {
		return err
	}
	if reviewer.ModelContextWindow < 0 {
		return errReviewerContextWindowNegative
	}
	if err := validateModelContextWindowMinimum("reviewer.model_context_window", reviewer.ModelContextWindow); err != nil {
		return err
	}
	switch normalizeReviewerAuth(reviewer.Auth) {
	case "inherit":
	case "none":
	default:
		return fmt.Errorf("invalid reviewer.auth %q (expected inherit|none)", reviewer.Auth)
	}
	if reviewer.TimeoutSeconds <= 0 {
		return fmt.Errorf("reviewer.timeout_seconds must be > 0")
	}
	return nil
}

func validateModelContextWindowMinimum(field string, window int) error {
	if window >= minimumModelContextWindow {
		return nil
	}
	return fmt.Errorf(
		"%w: %s must be >= %d",
		errModelContextWindowBelowMinimum,
		field,
		minimumModelContextWindow,
	)
}

func validateReviewerProviderCapabilities(capabilities ProviderCapabilitiesOverride, sources map[string]string) error {
	if strings.TrimSpace(capabilities.ProviderID) != "" {
		return nil
	}
	if hasAnyConfiguredSource(sources, reviewerProviderCapabilitySourceKeys.values...) || hasProviderCapabilitiesOverride(capabilities) {
		return errReviewerProviderCapabilitiesNeedID
	}
	return nil
}

func hasConfiguredSource(sources map[string]string, key string) bool {
	switch strings.TrimSpace(sources[key]) {
	case "file", "env", "cli", "subagent":
		return true
	default:
		return false
	}
}

func validateSleepPreventionMode(state settingsState, _ map[string]string) error {
	switch state.Settings.PreventSleep {
	case SleepPreventionModeAlways, SleepPreventionModeActive, SleepPreventionModeNever:
		return nil
	default:
		return fmt.Errorf("invalid prevent_sleep %q (expected always|active|never)", state.Settings.PreventSleep)
	}
}

func normalizeCompactionMode(raw string) CompactionMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "native":
		return CompactionModeNative
	case "local":
		return CompactionModeLocal
	case "none":
		return CompactionModeNone
	default:
		return CompactionMode(strings.TrimSpace(raw))
	}
}

func normalizeCacheWarningMode(raw string) CacheWarningMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "off":
		return CacheWarningModeOff
	case "default":
		return CacheWarningModeDefault
	case "verbose":
		return CacheWarningModeVerbose
	default:
		return CacheWarningMode(strings.TrimSpace(raw))
	}
}

func normalizeShellPostprocessingMode(raw string) ShellPostprocessingMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "none":
		return ShellPostprocessingModeNone
	case "builtin":
		return ShellPostprocessingModeBuiltin
	case "user":
		return ShellPostprocessingModeUser
	case "all":
		return ShellPostprocessingModeAll
	default:
		return ShellPostprocessingMode(strings.TrimSpace(raw))
	}
}

func normalizeModelVerbosity(raw string) ModelVerbosity {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "low":
		return ModelVerbosityLow
	case "medium":
		return ModelVerbosityMedium
	case "high":
		return ModelVerbosityHigh
	default:
		return ModelVerbosity(strings.TrimSpace(raw))
	}
}

func normalizeReviewerAuth(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "inherit":
		return "inherit"
	case "none":
		return "none"
	default:
		return strings.TrimSpace(raw)
	}
}
