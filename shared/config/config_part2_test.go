package config

import (
	"core/shared/toolspec"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCapabilityOverridesFromFile(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, `model = "gpt-5.6-sol"

[model_capabilities]
supports_reasoning_effort = true
supports_vision_inputs = true

[provider_capabilities]
provider_id = "custom-provider"
supports_responses_api = true
supports_responses_compact = false
supports_prompt_cache_key = true
supports_native_web_search = true
supports_reasoning_encrypted = false
supports_server_side_context_edit = false
is_openai_first_party = false
supports_provider_verbosity = true
`, LoadOptions{})
	if !cfg.Settings.ModelCapabilities.SupportsReasoningEffort || !cfg.Settings.ModelCapabilities.SupportsVisionInputs {
		t.Fatalf("expected model capability overrides from file, got %+v", cfg.Settings.ModelCapabilities)
	}
	if cfg.Settings.ProviderCapabilities.ProviderID != "custom-provider" || !cfg.Settings.ProviderCapabilities.SupportsResponsesAPI || !cfg.Settings.ProviderCapabilities.SupportsPromptCacheKey || !cfg.Settings.ProviderCapabilities.SupportsNativeWebSearch {
		t.Fatalf("expected provider capability overrides from file, got %+v", cfg.Settings.ProviderCapabilities)
	}
	if !cfg.Settings.ProviderCapabilities.SupportsProviderVerbosity {
		t.Fatalf("expected supports_provider_verbosity override from file, got %+v", cfg.Settings.ProviderCapabilities)
	}
	if got := cfg.Source.Sources["model_capabilities.supports_reasoning_effort"]; got != "file" {
		t.Fatalf("expected model_capabilities.supports_reasoning_effort source file, got %q", got)
	}
	if got := cfg.Source.Sources["provider_capabilities.provider_id"]; got != "file" {
		t.Fatalf("expected provider_capabilities.provider_id source file, got %q", got)
	}
	if got := cfg.Source.Sources["provider_capabilities.supports_provider_verbosity"]; got != "file" {
		t.Fatalf("expected provider_capabilities.supports_provider_verbosity source file, got %q", got)
	}
}

func TestLoadCapabilityOverridesFromEnv(t *testing.T) {
	_, workspace := newConfigTestEnv(t)
	t.Setenv("KENT_MODEL_CAPABILITIES_SUPPORTS_REASONING_EFFORT", "true")
	t.Setenv("KENT_MODEL_CAPABILITIES_SUPPORTS_VISION_INPUTS", "true")
	t.Setenv("KENT_PROVIDER_CAPABILITIES_PROVIDER_ID", "custom-provider")
	t.Setenv("KENT_PROVIDER_CAPABILITIES_SUPPORTS_RESPONSES_API", "true")
	t.Setenv("KENT_PROVIDER_CAPABILITIES_SUPPORTS_RESPONSES_COMPACT", "false")
	t.Setenv("KENT_PROVIDER_CAPABILITIES_SUPPORTS_PROMPT_CACHE_KEY", "true")
	t.Setenv("KENT_PROVIDER_CAPABILITIES_SUPPORTS_NATIVE_WEB_SEARCH", "true")
	t.Setenv("KENT_PROVIDER_CAPABILITIES_SUPPORTS_REASONING_ENCRYPTED", "false")
	t.Setenv("KENT_PROVIDER_CAPABILITIES_SUPPORTS_SERVER_SIDE_CONTEXT_EDIT", "false")
	t.Setenv("KENT_PROVIDER_CAPABILITIES_IS_OPENAI_FIRST_PARTY", "false")
	t.Setenv("KENT_PROVIDER_CAPABILITIES_SUPPORTS_PROVIDER_VERBOSITY", "false")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if !cfg.Settings.ModelCapabilities.SupportsReasoningEffort || !cfg.Settings.ModelCapabilities.SupportsVisionInputs {
		t.Fatalf("expected model capability overrides from env, got %+v", cfg.Settings.ModelCapabilities)
	}
	if cfg.Settings.ProviderCapabilities.ProviderID != "custom-provider" || !cfg.Settings.ProviderCapabilities.SupportsResponsesAPI || !cfg.Settings.ProviderCapabilities.SupportsPromptCacheKey || !cfg.Settings.ProviderCapabilities.SupportsNativeWebSearch {
		t.Fatalf("expected provider capability overrides from env, got %+v", cfg.Settings.ProviderCapabilities)
	}
	if cfg.Settings.ProviderCapabilities.SupportsProviderVerbosity {
		t.Fatalf("expected supports_provider_verbosity=false override from env, got %+v", cfg.Settings.ProviderCapabilities)
	}
	if got := cfg.Source.Sources["model_capabilities.supports_reasoning_effort"]; got != "env" {
		t.Fatalf("expected model_capabilities.supports_reasoning_effort source env, got %q", got)
	}
	if got := cfg.Source.Sources["provider_capabilities.provider_id"]; got != "env" {
		t.Fatalf("expected provider_capabilities.provider_id source env, got %q", got)
	}
	if got := cfg.Source.Sources["provider_capabilities.supports_provider_verbosity"]; got != "env" {
		t.Fatalf("expected provider_capabilities.supports_provider_verbosity source env, got %q", got)
	}
}

func TestLoadReviewerCapabilityOverridesFromFileAndEnv(t *testing.T) {
	_, workspace, cfg := loadConfigTestFileApp(t, `model = "gpt-5.6-sol"
model_verbosity = "high"
model_context_window = 372000

[reviewer]
model = "local-reviewer"
model_verbosity = "low"
model_context_window = 64000

[reviewer.model_capabilities]
supports_reasoning_effort = true
supports_vision_inputs = true

[reviewer.provider_capabilities]
provider_id = "local-reviewer"
supports_responses_api = true
supports_prompt_cache_key = true
supports_provider_verbosity = false
`, LoadOptions{})
	if cfg.Settings.Reviewer.ModelVerbosity != ModelVerbosityLow {
		t.Fatalf("expected reviewer.model_verbosity=low, got %q", cfg.Settings.Reviewer.ModelVerbosity)
	}
	if cfg.Settings.Reviewer.ModelContextWindow != 64000 {
		t.Fatalf("expected reviewer.model_context_window=64000, got %d", cfg.Settings.Reviewer.ModelContextWindow)
	}
	if !cfg.Settings.Reviewer.ModelCapabilities.SupportsReasoningEffort || !cfg.Settings.Reviewer.ModelCapabilities.SupportsVisionInputs {
		t.Fatalf("expected reviewer model capability overrides, got %+v", cfg.Settings.Reviewer.ModelCapabilities)
	}
	if cfg.Settings.Reviewer.ProviderCapabilities.ProviderID != "local-reviewer" || !cfg.Settings.Reviewer.ProviderCapabilities.SupportsResponsesAPI || !cfg.Settings.Reviewer.ProviderCapabilities.SupportsPromptCacheKey {
		t.Fatalf("expected reviewer provider capability overrides, got %+v", cfg.Settings.Reviewer.ProviderCapabilities)
	}
	if cfg.Settings.Reviewer.ProviderCapabilities.SupportsProviderVerbosity {
		t.Fatalf("expected reviewer supports_provider_verbosity=false from file, got %+v", cfg.Settings.Reviewer.ProviderCapabilities)
	}
	if got := cfg.Source.Sources["reviewer.model_capabilities.supports_reasoning_effort"]; got != "file" {
		t.Fatalf("expected reviewer model capability source file, got %q", got)
	}
	if got := cfg.Source.Sources["reviewer.provider_capabilities.provider_id"]; got != "file" {
		t.Fatalf("expected reviewer provider capability source file, got %q", got)
	}

	t.Setenv("KENT_REVIEWER_MODEL_VERBOSITY", "medium")
	t.Setenv("KENT_REVIEWER_MODEL_CONTEXT_WINDOW", "48000")
	t.Setenv("KENT_REVIEWER_MODEL_CAPABILITIES_SUPPORTS_REASONING_EFFORT", "false")
	t.Setenv("KENT_REVIEWER_MODEL_CAPABILITIES_SUPPORTS_VISION_INPUTS", "false")
	t.Setenv("KENT_REVIEWER_PROVIDER_CAPABILITIES_PROVIDER_ID", "env-reviewer")
	t.Setenv("KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_RESPONSES_API", "true")
	t.Setenv("KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_PROMPT_CACHE_KEY", "false")
	t.Setenv("KENT_REVIEWER_PROVIDER_CAPABILITIES_SUPPORTS_PROVIDER_VERBOSITY", "true")
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Reviewer.ModelVerbosity != ModelVerbosityMedium {
		t.Fatalf("expected env reviewer.model_verbosity=medium, got %q", cfg.Settings.Reviewer.ModelVerbosity)
	}
	if cfg.Settings.Reviewer.ModelContextWindow != 48000 {
		t.Fatalf("expected env reviewer.model_context_window=48000, got %d", cfg.Settings.Reviewer.ModelContextWindow)
	}
	if cfg.Settings.Reviewer.ModelCapabilities.SupportsReasoningEffort || cfg.Settings.Reviewer.ModelCapabilities.SupportsVisionInputs {
		t.Fatalf("expected env reviewer model capability overrides to disable file values, got %+v", cfg.Settings.Reviewer.ModelCapabilities)
	}
	if cfg.Settings.Reviewer.ProviderCapabilities.ProviderID != "env-reviewer" || !cfg.Settings.Reviewer.ProviderCapabilities.SupportsResponsesAPI || cfg.Settings.Reviewer.ProviderCapabilities.SupportsPromptCacheKey {
		t.Fatalf("expected env reviewer provider capability overrides, got %+v", cfg.Settings.Reviewer.ProviderCapabilities)
	}
	if !cfg.Settings.Reviewer.ProviderCapabilities.SupportsProviderVerbosity {
		t.Fatalf("expected reviewer supports_provider_verbosity=true from env, got %+v", cfg.Settings.Reviewer.ProviderCapabilities)
	}
	if got := cfg.Source.Sources["reviewer.model_context_window"]; got != "env" {
		t.Fatalf("expected reviewer.model_context_window source env, got %q", got)
	}
	if got := cfg.Source.Sources["reviewer.provider_capabilities.provider_id"]; got != "env" {
		t.Fatalf("expected reviewer provider capability source env, got %q", got)
	}
	if got := cfg.Source.Sources["reviewer.provider_capabilities.supports_provider_verbosity"]; got != "env" {
		t.Fatalf("expected reviewer supports_provider_verbosity source env, got %q", got)
	}
}

func TestLoadReviewerCapabilitiesInheritMainWhenUnset(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, `model = "gpt-5.6-sol"
model_verbosity = "high"
model_context_window = 128000
context_compaction_threshold_tokens = 121600

[model_capabilities]
supports_reasoning_effort = true

[provider_capabilities]
provider_id = "main-provider"
supports_responses_api = true
supports_provider_verbosity = true
`, LoadOptions{})
	if cfg.Settings.Reviewer.ModelVerbosity != ModelVerbosityHigh {
		t.Fatalf("expected reviewer.model_verbosity to inherit main, got %q", cfg.Settings.Reviewer.ModelVerbosity)
	}
	if cfg.Settings.Reviewer.ModelContextWindow != 128000 {
		t.Fatalf("expected reviewer.model_context_window to inherit main, got %d", cfg.Settings.Reviewer.ModelContextWindow)
	}
	if !cfg.Settings.Reviewer.ModelCapabilities.SupportsReasoningEffort {
		t.Fatalf("expected reviewer model capabilities to inherit main, got %+v", cfg.Settings.Reviewer.ModelCapabilities)
	}
	if cfg.Settings.Reviewer.ProviderCapabilities.ProviderID != "main-provider" || !cfg.Settings.Reviewer.ProviderCapabilities.SupportsResponsesAPI {
		t.Fatalf("expected reviewer provider capabilities to inherit main, got %+v", cfg.Settings.Reviewer.ProviderCapabilities)
	}
	if !cfg.Settings.Reviewer.ProviderCapabilities.SupportsProviderVerbosity {
		t.Fatalf("expected reviewer supports_provider_verbosity to inherit main, got %+v", cfg.Settings.Reviewer.ProviderCapabilities)
	}
}

func TestLoadReviewerModelContextWindowRejectsNegative(t *testing.T) {
	err := loadConfigTestFileError(t, `[reviewer]
model_context_window = -1
`, LoadOptions{})
	if err == nil {
		t.Fatal("expected negative reviewer.model_context_window to fail")
	}
	if !errors.Is(err, errReviewerContextWindowNegative) {
		t.Fatalf("expected reviewer.model_context_window validation error, got %v", err)
	}
}

func TestLoadReviewerModelContextWindowRejectsBelowMinimum(t *testing.T) {
	err := loadConfigTestFileError(t, `[reviewer]
model_context_window = 39999
`, LoadOptions{})
	if err == nil {
		t.Fatal("expected reviewer.model_context_window below minimum to fail")
	}
	if !errors.Is(err, errModelContextWindowBelowMinimum) {
		t.Fatalf("expected model context window minimum validation detail, got %v", err)
	}
}

func TestSettingsTOMLPreservesReviewerProviderCapabilityFalseOverride(t *testing.T) {
	settings := configRegistry.defaultState().Settings
	settings.ProviderCapabilities = ProviderCapabilitiesOverride{
		ProviderID:             "main-provider",
		SupportsResponsesAPI:   true,
		SupportsPromptCacheKey: true,
	}
	settings.Reviewer.ProviderCapabilities = ProviderCapabilitiesOverride{
		ProviderID:             "reviewer-provider",
		SupportsResponsesAPI:   false,
		SupportsPromptCacheKey: false,
	}

	rendered := settingsTOMLWithRenderingOptions(settings, true, nil, nil)
	if !strings.Contains(rendered, "[reviewer.provider_capabilities]") {
		t.Fatalf("expected reviewer provider capabilities section, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "provider_id = \"reviewer-provider\"") {
		t.Fatalf("expected explicit reviewer provider ID override, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "supports_responses_api = false") {
		t.Fatalf("expected explicit reviewer responses API false override, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "supports_prompt_cache_key = false") {
		t.Fatalf("expected explicit reviewer prompt cache false override, got:\n%s", rendered)
	}
}

func TestValidateSettingsWithSourcesAllowsSubagentReviewerAnthropicOverride(t *testing.T) {
	settings := configRegistry.defaultState().Settings
	settings.Reviewer.Model = settings.Model
	settings.Reviewer.ModelContextWindow = settings.ModelContextWindow
	settings.Reviewer.ProviderOverride = "anthropic"

	sources := configRegistry.defaultSourceMap()
	sources["reviewer.provider_override"] = "subagent"

	err := ValidateSettingsWithSources(settings, sources)
	if err != nil {
		t.Fatalf("validate settings with subagent reviewer anthropic override: %v", err)
	}
}

func TestLoadReviewerProviderCapabilitiesDoNotInheritMainForSeparateEndpoint(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, `model = "gpt-5.6-sol"

[provider_capabilities]
provider_id = "main-provider"
supports_responses_api = true

[reviewer]
model = "local-reviewer"
provider_override = "openai"
openai_base_url = "http://127.0.0.1:11434/v1"
`, LoadOptions{})
	if cfg.Settings.Reviewer.ProviderCapabilities.ProviderID != "" || cfg.Settings.Reviewer.ProviderCapabilities.SupportsResponsesAPI {
		t.Fatalf("expected separate reviewer endpoint not to inherit main provider capabilities, got %+v", cfg.Settings.Reviewer.ProviderCapabilities)
	}
}

func TestLoadReviewerProviderCapabilitiesInheritMainForNoOpOpenAIProviderOverride(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, `model = "gpt-5.6-sol"
openai_base_url = "http://127.0.0.1:8080/v1"

[provider_capabilities]
provider_id = "main-compatible"
supports_responses_api = true
supports_prompt_cache_key = true

[reviewer]
provider_override = "openai"
`, LoadOptions{})
	if cfg.Settings.Reviewer.OpenAIBaseURL != "http://127.0.0.1:8080/v1" {
		t.Fatalf("expected reviewer to inherit main base URL, got %q", cfg.Settings.Reviewer.OpenAIBaseURL)
	}
	if cfg.Settings.Reviewer.ProviderCapabilities.ProviderID != "main-compatible" ||
		!cfg.Settings.Reviewer.ProviderCapabilities.SupportsResponsesAPI ||
		!cfg.Settings.Reviewer.ProviderCapabilities.SupportsPromptCacheKey {
		t.Fatalf("expected no-op reviewer provider override to inherit main provider capabilities, got %+v", cfg.Settings.Reviewer.ProviderCapabilities)
	}
}

func TestLoadReviewerProviderInheritsAnthropicProvider(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, `model = "claude-test"
provider_override = "anthropic"
`, LoadOptions{})
	if cfg.Settings.Reviewer.ProviderOverride != "anthropic" {
		t.Fatalf("expected reviewer provider to inherit anthropic, got %q", cfg.Settings.Reviewer.ProviderOverride)
	}
}

func TestLoadReviewerProviderAllowsExplicitAnthropicProvider(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, `model = "gpt-5.6-sol"

[reviewer]
model = "claude-test"
provider_override = "anthropic"
`, LoadOptions{})
	if cfg.Settings.Reviewer.ProviderOverride != "anthropic" {
		t.Fatalf("expected reviewer provider override anthropic, got %q", cfg.Settings.Reviewer.ProviderOverride)
	}
}

func TestLoadProviderOverrideFromFile(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, "model = \"my-team-alias\"\nprovider_override = \"OpenAI\"\n", LoadOptions{})
	if cfg.Settings.ProviderOverride != "openai" {
		t.Fatalf("expected normalized provider_override from file, got %q", cfg.Settings.ProviderOverride)
	}
	if got := cfg.Source.Sources["provider_override"]; got != "file" {
		t.Fatalf("expected provider_override source file, got %q", got)
	}
}

func TestLoadProviderOverrideRequiresExplicitModelOverride(t *testing.T) {
	err := loadConfigTestFileError(t, "provider_override = \"openai\"\n", LoadOptions{})
	if err == nil {
		t.Fatal("expected provider_override without model override to fail")
	}
	if !errors.Is(err, errProviderOverrideRequiresModel) {
		t.Fatalf("expected provider_override/model override validation error, got %v", err)
	}
}

func TestLoadProviderOverrideRejectsUnsupportedProviderFamily(t *testing.T) {
	err := loadConfigTestFileError(t, "model = \"my-team-alias\"\nprovider_override = \"openrouter\"\n", LoadOptions{})
	if err == nil {
		t.Fatal("expected invalid provider_override to fail")
	}
	if !errors.Is(err, errInvalidProviderOverride) {
		t.Fatalf("expected invalid provider_override validation error, got %v", err)
	}
}

func TestLoadProviderOverrideRejectsOpenAIBaseURLConflict(t *testing.T) {
	err := loadConfigTestFileError(t, "model = \"my-team-alias\"\nprovider_override = \"anthropic\"\nopenai_base_url = \"https://example.openrouter.ai/api/v1\"\n", LoadOptions{})
	if err == nil {
		t.Fatal("expected provider_override/openai_base_url conflict to fail")
	}
	if !errors.Is(err, errOpenAIBaseURLConflict) {
		t.Fatalf("expected provider_override/openai_base_url conflict error, got %v", err)
	}
}

func TestLoadProviderOverrideFromCLIWithExplicitFileModel(t *testing.T) {
	_, workspace, _ := loadConfigTestFileApp(t, "model = \"my-team-alias\"\n", LoadOptions{})
	cfg, err := Load(workspace, LoadOptions{ProviderOverride: "openai"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Settings.ProviderOverride != "openai" {
		t.Fatalf("expected cli provider_override, got %q", cfg.Settings.ProviderOverride)
	}
	if got := cfg.Source.Sources["provider_override"]; got != "cli" {
		t.Fatalf("expected provider_override source cli, got %q", got)
	}
}

func TestLoadCapabilityOverridesRequireProviderID(t *testing.T) {
	_, workspace := newConfigTestEnv(t)
	t.Setenv("KENT_PROVIDER_CAPABILITIES_SUPPORTS_NATIVE_WEB_SEARCH", "true")

	_, err := Load(workspace, LoadOptions{})
	if err == nil {
		t.Fatal("expected validation error when provider capability override is set without provider_id")
	}
	if !errors.Is(err, errProviderCapabilitiesNeedID) {
		t.Fatalf("expected provider_id validation error, got %v", err)
	}
}

func TestLoadRejectsRemovedProviderCapabilitySettings(t *testing.T) {
	removedSetting := "supports_request_" + "input_token_count"
	for _, setting := range []string{
		"provider_capabilities." + removedSetting,
		"reviewer.provider_capabilities." + removedSetting,
	} {
		t.Run(setting, func(t *testing.T) {
			err := loadConfigTestFileError(t, setting+" = true\n", LoadOptions{})
			if err == nil || !strings.Contains(err.Error(), setting) {
				t.Fatalf("removed setting error = %v, want unknown key %q", err, setting)
			}
		})
	}
}

func TestLoadFalseProviderVerbosityCapabilityRequiresProviderID(t *testing.T) {
	_, workspace := newConfigTestEnv(t)
	t.Setenv("KENT_PROVIDER_CAPABILITIES_SUPPORTS_PROVIDER_VERBOSITY", "false")

	_, err := Load(workspace, LoadOptions{})
	if err == nil {
		t.Fatal("expected validation error when false verbosity capability override is set without provider_id")
	}
	if !errors.Is(err, errProviderCapabilitiesNeedID) {
		t.Fatalf("expected provider_id validation error, got %v", err)
	}
}

func TestLoadFalseReviewerProviderVerbosityCapabilityRequiresProviderID(t *testing.T) {
	err := loadConfigTestFileError(t, `[reviewer.provider_capabilities]
supports_provider_verbosity = false
`, LoadOptions{})
	if err == nil {
		t.Fatal("expected validation error when false reviewer verbosity capability override is set without provider_id")
	}
	if !errors.Is(err, errReviewerProviderCapabilitiesNeedID) {
		t.Fatalf("expected reviewer provider_id validation error, got %v", err)
	}
}

func TestLoadProviderVerbosityCapabilityRejectsNonBooleanValues(t *testing.T) {
	if err := loadConfigTestFileError(t, `[provider_capabilities]
provider_id = "custom-provider"
supports_provider_verbosity = "yes"
`, LoadOptions{}); err == nil {
		t.Fatal("expected non-boolean TOML verbosity capability to fail")
	}

	_, workspace := newConfigTestEnv(t)
	t.Setenv("KENT_PROVIDER_CAPABILITIES_PROVIDER_ID", "custom-provider")
	t.Setenv("KENT_PROVIDER_CAPABILITIES_SUPPORTS_PROVIDER_VERBOSITY", "yes")
	if _, err := Load(workspace, LoadOptions{}); err == nil {
		t.Fatal("expected non-boolean environment verbosity capability to fail")
	}
}

func TestLoadPriorityRequestModeFromFile(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, "priority_request_mode = true\n", LoadOptions{})
	if !cfg.Settings.PriorityRequestMode {
		t.Fatal("expected priority_request_mode=true from file")
	}
	if got := cfg.Source.Sources["priority_request_mode"]; got != "file" {
		t.Fatalf("expected priority_request_mode source file, got %q", got)
	}
}

func TestLoadModelVerbosityFromFile(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, "model_verbosity = \"high\"\n", LoadOptions{})
	if cfg.Settings.ModelVerbosity != ModelVerbosityHigh {
		t.Fatalf("expected model_verbosity=high from file, got %q", cfg.Settings.ModelVerbosity)
	}
	if got := cfg.Source.Sources["model_verbosity"]; got != "file" {
		t.Fatalf("expected model_verbosity source file, got %q", got)
	}
}

func TestLoadRejectsInvalidModelVerbosityFromFile(t *testing.T) {
	err := loadConfigTestFileError(t, "model_verbosity = \"verbose\"\n", LoadOptions{})
	if err == nil {
		t.Fatal("expected validation error for invalid model_verbosity")
	}
	if !errors.Is(err, errInvalidModelVerbosity) {
		t.Fatalf("expected model_verbosity validation error, got %v", err)
	}
}

func TestProjectIDForWorkspaceRootCanonicalizesSymlinkedWorkspace(t *testing.T) {
	home := t.TempDir()
	realWorkspace := t.TempDir()
	linkParent := t.TempDir()
	symlinkPath := filepath.Join(linkParent, "workspace-link")
	if err := os.Symlink(realWorkspace, symlinkPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	t.Setenv("HOME", home)

	realCfg, err := Load(realWorkspace, LoadOptions{})
	if err != nil {
		t.Fatalf("load real workspace: %v", err)
	}
	symlinkCfg, err := Load(symlinkPath, LoadOptions{})
	if err != nil {
		t.Fatalf("load symlink workspace: %v", err)
	}
	realProjectID, err := ProjectIDForWorkspaceRoot(realCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("project id for real workspace: %v", err)
	}
	symlinkProjectID, err := ProjectIDForWorkspaceRoot(symlinkCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("project id for symlink workspace: %v", err)
	}
	if symlinkProjectID != realProjectID {
		t.Fatalf("expected symlinked workspace to reuse project id, got %q want %q", symlinkProjectID, realProjectID)
	}
}

func TestLoadReviewerPrecedenceAndValidation(t *testing.T) {
	home, workspace, cfg := loadConfigTestFileApp(t, `[reviewer]
frequency = "all"
model = "gpt-file-reviewer"
thinking_level = "medium"
system_prompt_file = "reviewer-global.md"
provider_override = "openai"
openai_base_url = "http://127.0.0.1:11434/v1"
auth = "none"
timeout_seconds = 45
verbose_output = true
`, LoadOptions{})
	if cfg.Settings.Reviewer.Frequency != "all" {
		t.Fatalf("expected file reviewer.frequency=all, got %q", cfg.Settings.Reviewer.Frequency)
	}
	if got := cfg.Source.Sources["reviewer.frequency"]; got != "file" {
		t.Fatalf("expected reviewer.frequency source file, got %q", got)
	}
	if cfg.Settings.Reviewer.Model != "gpt-file-reviewer" {
		t.Fatalf("expected file reviewer.model, got %q", cfg.Settings.Reviewer.Model)
	}
	if got := cfg.Source.Sources["reviewer.model"]; got != "file" {
		t.Fatalf("expected reviewer.model source file, got %q", got)
	}
	if !cfg.Settings.Reviewer.VerboseOutput {
		t.Fatalf("expected file reviewer.verbose_output=true")
	}
	if want := filepath.Join(home, ConfigDirName, "reviewer-global.md"); cfg.Settings.Reviewer.SystemPromptFile != want {
		t.Fatalf("expected file reviewer.system_prompt_file=%q, got %q", want, cfg.Settings.Reviewer.SystemPromptFile)
	}
	if got := cfg.Source.Sources["reviewer.verbose_output"]; got != "file" {
		t.Fatalf("expected reviewer.verbose_output source file, got %q", got)
	}
	if got := cfg.Source.Sources["reviewer.system_prompt_file"]; got != "file" {
		t.Fatalf("expected reviewer.system_prompt_file source file, got %q", got)
	}
	if cfg.Settings.Reviewer.ProviderOverride != "openai" {
		t.Fatalf("expected file reviewer.provider_override=openai, got %q", cfg.Settings.Reviewer.ProviderOverride)
	}
	if got := cfg.Source.Sources["reviewer.provider_override"]; got != "file" {
		t.Fatalf("expected reviewer.provider_override source file, got %q", got)
	}
	if cfg.Settings.Reviewer.OpenAIBaseURL != "http://127.0.0.1:11434/v1" {
		t.Fatalf("expected file reviewer.openai_base_url, got %q", cfg.Settings.Reviewer.OpenAIBaseURL)
	}
	if got := cfg.Source.Sources["reviewer.openai_base_url"]; got != "file" {
		t.Fatalf("expected reviewer.openai_base_url source file, got %q", got)
	}
	if cfg.Settings.Reviewer.Auth != "none" {
		t.Fatalf("expected file reviewer.auth=none, got %q", cfg.Settings.Reviewer.Auth)
	}
	if got := cfg.Source.Sources["reviewer.auth"]; got != "file" {
		t.Fatalf("expected reviewer.auth source file, got %q", got)
	}

	workspaceConfigPath := filepath.Join(workspace, ConfigDirName, "config.toml")
	if err := os.MkdirAll(filepath.Dir(workspaceConfigPath), 0o755); err != nil {
		t.Fatalf("mkdir workspace config dir: %v", err)
	}
	if err := os.WriteFile(workspaceConfigPath, []byte("[reviewer]\nsystem_prompt_file = \"workspace-reviewer.md\"\n"), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if want := filepath.Join(workspace, ConfigDirName, "workspace-reviewer.md"); cfg.Settings.Reviewer.SystemPromptFile != want {
		t.Fatalf("expected workspace reviewer.system_prompt_file=%q, got %q", want, cfg.Settings.Reviewer.SystemPromptFile)
	}

	t.Setenv("KENT_REVIEWER_FREQUENCY", "off")
	t.Setenv("KENT_REVIEWER_MODEL", "gpt-env-reviewer")
	t.Setenv("KENT_REVIEWER_THINKING_LEVEL", "high")
	t.Setenv("KENT_REVIEWER_PROVIDER_OVERRIDE", "openai")
	t.Setenv("KENT_REVIEWER_OPENAI_BASE_URL", "http://localhost:11434/v1")
	t.Setenv("KENT_REVIEWER_AUTH", "inherit")
	t.Setenv("KENT_REVIEWER_TIMEOUT_SECONDS", "30")
	t.Setenv("KENT_REVIEWER_VERBOSE_OUTPUT", "false")

	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Reviewer.Frequency != "off" {
		t.Fatalf("expected env reviewer.frequency=off, got %q", cfg.Settings.Reviewer.Frequency)
	}
	if got := cfg.Source.Sources["reviewer.frequency"]; got != "env" {
		t.Fatalf("expected reviewer.frequency source env, got %q", got)
	}
	if cfg.Settings.Reviewer.Model != "gpt-env-reviewer" {
		t.Fatalf("expected env reviewer.model, got %q", cfg.Settings.Reviewer.Model)
	}
	if got := cfg.Source.Sources["reviewer.model"]; got != "env" {
		t.Fatalf("expected reviewer.model source env, got %q", got)
	}
	if cfg.Settings.Reviewer.ProviderOverride != "openai" {
		t.Fatalf("expected env reviewer.provider_override=openai, got %q", cfg.Settings.Reviewer.ProviderOverride)
	}
	if got := cfg.Source.Sources["reviewer.provider_override"]; got != "env" {
		t.Fatalf("expected reviewer.provider_override source env, got %q", got)
	}
	if cfg.Settings.Reviewer.OpenAIBaseURL != "http://localhost:11434/v1" {
		t.Fatalf("expected env reviewer.openai_base_url, got %q", cfg.Settings.Reviewer.OpenAIBaseURL)
	}
	if got := cfg.Source.Sources["reviewer.openai_base_url"]; got != "env" {
		t.Fatalf("expected reviewer.openai_base_url source env, got %q", got)
	}
	if cfg.Settings.Reviewer.Auth != "inherit" {
		t.Fatalf("expected env reviewer.auth=inherit, got %q", cfg.Settings.Reviewer.Auth)
	}
	if got := cfg.Source.Sources["reviewer.auth"]; got != "env" {
		t.Fatalf("expected reviewer.auth source env, got %q", got)
	}
	if cfg.Settings.Reviewer.VerboseOutput {
		t.Fatalf("expected env reviewer.verbose_output=false")
	}
	if got := cfg.Source.Sources["reviewer.verbose_output"]; got != "env" {
		t.Fatalf("expected reviewer.verbose_output source env, got %q", got)
	}

	t.Setenv("KENT_REVIEWER_FREQUENCY", "sometimes")
	if _, err := Load(workspace, LoadOptions{}); err == nil {
		t.Fatal("expected invalid reviewer frequency")
	}
	t.Setenv("KENT_REVIEWER_FREQUENCY", "all")
	t.Setenv("KENT_REVIEWER_PROVIDER_OVERRIDE", "bogus")
	t.Setenv("KENT_REVIEWER_OPENAI_BASE_URL", "")
	if _, err := Load(workspace, LoadOptions{}); !errors.Is(err, errInvalidReviewerProvider) {
		t.Fatalf("expected invalid reviewer provider error, got %v", err)
	}
}

func TestLoadWebSearchPrecedenceAndValidation(t *testing.T) {
	_, workspace, cfg := loadConfigTestFileApp(t, "web_search = \"native\"\n", LoadOptions{})
	if cfg.Settings.WebSearch != "native" {
		t.Fatalf("expected file web_search=native, got %q", cfg.Settings.WebSearch)
	}
	if got := cfg.Source.Sources["web_search"]; got != "file" {
		t.Fatalf("expected web_search source file, got %q", got)
	}
	if !cfg.Settings.EnabledTools[toolspec.ToolWebSearch] {
		t.Fatalf("expected web_search tool to remain enabled by default")
	}

	t.Setenv("KENT_WEB_SEARCH", "off")
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.WebSearch != "off" {
		t.Fatalf("expected env web_search=off, got %q", cfg.Settings.WebSearch)
	}
	if got := cfg.Source.Sources["web_search"]; got != "env" {
		t.Fatalf("expected web_search source env, got %q", got)
	}
	if !cfg.Settings.EnabledTools[toolspec.ToolWebSearch] {
		t.Fatalf("expected web_search tool to stay enabled when only web_search mode is off")
	}

	t.Setenv("KENT_WEB_SEARCH", "custom")
	if _, err := Load(workspace, LoadOptions{}); err == nil {
		t.Fatal("expected web_search=custom validation error")
	}
}

func TestLoadWebSearchNativeRespectsExplicitToolToggle(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, "web_search = \"native\"\n[tools]\nweb_search = false\n", LoadOptions{})
	if cfg.Settings.EnabledTools[toolspec.ToolWebSearch] {
		t.Fatalf("expected explicit tools.web_search=false to stay disabled")
	}
	if got := cfg.Source.Sources["tools.web_search"]; got != "file" {
		t.Fatalf("expected tools.web_search source file, got %q", got)
	}
}

func TestLoadTriggerHandoffToolToggleFromFile(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, "[tools]\ntrigger_handoff = true\n", LoadOptions{})
	if !cfg.Settings.EnabledTools[toolspec.ToolTriggerHandoff] {
		t.Fatalf("expected explicit tools.trigger_handoff=true to enable the tool")
	}
	if got := cfg.Source.Sources["tools.trigger_handoff"]; got != "file" {
		t.Fatalf("expected tools.trigger_handoff source file, got %q", got)
	}
}

func TestLoadSkillTogglesFromFile(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, "[skills]\nApiResult = false\n\"Local Helper\" = true\n", LoadOptions{})
	if cfg.Settings.SkillToggles["apiresult"] {
		t.Fatalf("expected apiresult skill to be explicitly disabled, got %+v", cfg.Settings.SkillToggles)
	}
	if !cfg.Settings.SkillToggles["local helper"] {
		t.Fatalf("expected quoted skill key to stay enabled, got %+v", cfg.Settings.SkillToggles)
	}
	if got := cfg.Source.Sources["skills.apiresult"]; got != "file" {
		t.Fatalf("expected skills.apiresult source file, got %q", got)
	}
	if got := cfg.Source.Sources["skills.local helper"]; got != "file" {
		t.Fatalf("expected skills.local helper source file, got %q", got)
	}
}

func TestResolveSkillPolicyEnablesUnconfiguredSkills(t *testing.T) {
	policy := ResolveSkillPolicy(Settings{})
	if !policy.SkillEnabled("any skill") {
		t.Fatal("zero-value settings must enable unconfigured skills")
	}
	defaultPolicy := ResolveSkillPolicy(configRegistry.defaultState().Settings)
	if !defaultPolicy.SkillEnabled("any skill") {
		t.Fatal("registry-default settings must enable unconfigured skills")
	}
	if _, exists := configRegistry.defaultSourceMap()["skills.enabled"]; exists {
		t.Fatal("unconfigured skill named enabled must not have a synthetic default source")
	}
}

func TestLoadSkillNamedEnabledAsOrdinaryToggle(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, "[skills]\nenabled = false\napiresult = true\n", LoadOptions{})
	policy := ResolveSkillPolicy(cfg.Settings)
	if policy.SkillEnabled("enabled") {
		t.Fatal("skills.enabled=false must disable only the skill named enabled")
	}
	if !policy.SkillEnabled("apiresult") {
		t.Fatal("skills.enabled=false must not disable other skills")
	}
	if enabled, exists := cfg.Settings.SkillToggles["enabled"]; !exists || enabled {
		t.Fatalf("enabled must remain an ordinary disabled skill toggle: %+v", cfg.Settings.SkillToggles)
	}
	if got := cfg.Source.Sources["skills.enabled"]; got != "file" {
		t.Fatalf("expected skills.enabled source file, got %q", got)
	}
}

func TestLoadSkillNamedEnabledIsCaseInsensitive(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, "[skills]\n\" EnAbLeD \" = false\n", LoadOptions{})
	if ResolveSkillPolicy(cfg.Settings).SkillEnabled("enabled") {
		t.Fatal("normalized enabled key must disable the skill named enabled")
	}
	if enabled, exists := cfg.Settings.SkillToggles["enabled"]; !exists || enabled {
		t.Fatalf("normalized enabled key must be an ordinary disabled toggle: %+v", cfg.Settings.SkillToggles)
	}
}

func TestLoadRejectsNonBooleanSkillToggle(t *testing.T) {
	if err := loadConfigTestFileError(t, "[skills]\napiresult = \"off\"\n", LoadOptions{}); err == nil {
		t.Fatal("expected invalid skills type error")
	} else {
		var typeErr *SettingsKeyTypeError
		if !errors.As(err, &typeErr) || typeErr.Key != "skills.apiresult" {
			t.Fatalf("expected skills.apiresult type error, got %v", err)
		}
	}
}

func TestLoadRejectsNonBooleanSkillNamedEnabled(t *testing.T) {
	err := loadConfigTestFileError(t, "[skills]\nenabled = \"off\"\n", LoadOptions{})
	if err == nil {
		t.Fatal("expected invalid skills.enabled type error")
	}
	var typeErr *SettingsKeyTypeError
	if !errors.As(err, &typeErr) || typeErr.Key != "skills.enabled" {
		t.Fatalf("expected skills.enabled type error, got %v", err)
	}
}

func TestLoadRejectsDuplicateNormalizedSkillToggleKeys(t *testing.T) {
	if err := loadConfigTestFileError(t, "[skills]\nApiResult = false\napiresult = true\n", LoadOptions{}); err == nil {
		t.Fatal("expected duplicate normalized skills key error")
	} else {
		var dupErr *DuplicateSettingsKeysError
		if !errors.As(err, &dupErr) || dupErr.Scope != "skills" ||
			dupErr.KeyA != "ApiResult" || dupErr.KeyB != "apiresult" || dupErr.Normalized != "apiresult" {
			t.Fatalf("expected duplicate skills key error, got %v", err)
		}
	}
}

func TestLoadRejectsDuplicateNormalizedSkillNamedEnabledKeys(t *testing.T) {
	err := loadConfigTestFileError(t, "[skills]\nEnabled = false\nenabled = true\n", LoadOptions{})
	if err == nil {
		t.Fatal("expected duplicate normalized skill key error")
	}
	var dupErr *DuplicateSettingsKeysError
	if !errors.As(err, &dupErr) || dupErr.Scope != "skills" ||
		dupErr.KeyA != "Enabled" || dupErr.KeyB != "enabled" || dupErr.Normalized != "enabled" {
		t.Fatalf("expected duplicate skills key error, got %v", err)
	}
}

func TestLoadNotificationMethodPrecedenceAndValidation(t *testing.T) {
	workspace := assertConfigPrecedence(t, configPrecedenceCase[string]{
		fileContents: "notification_method = \"bel\"\n",
		sourceKey:    "notification_method",
		fileWant:     "bel",
		envName:      "KENT_NOTIFICATION_METHOD",
		envValue:     "osc9",
		envWant:      "osc9",
		read:         func(settings Settings) string { return settings.NotificationMethod },
	})
	assertConfigEnvRejected(t, workspace, "KENT_NOTIFICATION_METHOD", "bad")
}

func TestLoadToolPreamblesPrecedence(t *testing.T) {
	workspace := assertConfigPrecedence(t, configPrecedenceCase[bool]{
		fileContents: "tool_preambles = false\n",
		sourceKey:    "tool_preambles",
		fileWant:     false,
		envName:      "KENT_TOOL_PREAMBLES",
		envValue:     "true",
		envWant:      true,
		read:         func(settings Settings) bool { return settings.ToolPreambles },
	})
	assertConfigEnvRejected(t, workspace, "KENT_TOOL_PREAMBLES", "broken")
}

func TestLoadAllowsReviewerAuthNoneWithInheritedCompatibleBaseURL(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, `model = "local-model"
provider_override = "openai"
openai_base_url = "http://127.0.0.1:11434/v1"

[reviewer]
provider_override = "openai"
auth = "none"
`, LoadOptions{})
	if cfg.Settings.Reviewer.OpenAIBaseURL != "http://127.0.0.1:11434/v1" {
		t.Fatalf("expected reviewer to inherit compatible base URL, got %q", cfg.Settings.Reviewer.OpenAIBaseURL)
	}
}

func TestLoadRejectsRemovedTUIAlternateScreenSetting(t *testing.T) {
	if err := loadConfigTestFileError(t, "tui_alternate_screen = \"always\"\n", LoadOptions{}); !unknownSettingsKeyReported(err, "tui_alternate_screen") {
		t.Fatalf("expected removed tui_alternate_screen setting error, got %v", err)
	}
}

func TestLoadPrecedenceCLIOverEnvOverFile(t *testing.T) {
	home, workspace := newConfigTestEnv(t)

	configPath := filepath.Join(home, ConfigDirName, "config.toml")
	writeConfigTestFile(t, configPath, `model = "gpt-file"
thinking_level = "low"
theme = "light"

[tools]
shell = true
patch = false
ask_question = true

[timeouts]
model_request_seconds = 45
`)

	t.Setenv("KENT_MODEL", "gpt-env")
	t.Setenv("KENT_THINKING_LEVEL", "medium")
	t.Setenv("KENT_TOOLS", "shell,patch")

	thinkingLevel := "xhigh"
	cfg, err := Load(workspace, LoadOptions{Model: "gpt-cli", ThinkingLevel: &thinkingLevel})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Settings.Model != "gpt-cli" {
		t.Fatalf("expected cli model, got %q", cfg.Settings.Model)
	}
	if cfg.Settings.ThinkingLevel != "xhigh" {
		t.Fatalf("expected cli thinking_level, got %q", cfg.Settings.ThinkingLevel)
	}
	if !cfg.Settings.EnabledTools[toolspec.ToolPatch] {
		t.Fatalf("expected env tool override to enable patch")
	}
	if got := cfg.Source.Sources["model"]; got != "cli" {
		t.Fatalf("expected model source cli, got %q", got)
	}
	if got := cfg.Source.Sources["thinking_level"]; got != "cli" {
		t.Fatalf("expected thinking_level source cli, got %q", got)
	}
}
