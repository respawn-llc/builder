package config

import (
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"testing"
)

func TestLoadRejectsUnknownLegacyTimeoutSettingNames(t *testing.T) {
	if err := loadConfigTestFileError(t, `[timeouts]
bash_default_seconds = 42
`, LoadOptions{}); err == nil {
		t.Fatal("expected unknown bash_default_seconds settings key error")
	}
}

func TestLoadShellOutputMaxCharsPrecedenceAndValidation(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, "shell_output_max_chars = 12000\n")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.ShellOutputMaxChars != 12000 {
		t.Fatalf("expected file shell_output_max_chars=12000, got %d", cfg.Settings.ShellOutputMaxChars)
	}
	assertConfigSource(t, cfg, "shell_output_max_chars", "file")

	t.Setenv("KENT_SHELL_OUTPUT_MAX_CHARS", "18000")
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.ShellOutputMaxChars != 18000 {
		t.Fatalf("expected env shell_output_max_chars=18000, got %d", cfg.Settings.ShellOutputMaxChars)
	}
	assertConfigSource(t, cfg, "shell_output_max_chars", "env")

	t.Setenv("KENT_SHELL_OUTPUT_MAX_CHARS", "0")
	if _, err := Load(workspace, LoadOptions{}); err == nil {
		t.Fatal("expected invalid shell_output_max_chars")
	}
}

func TestLoadMinimumExecToBgSecondsPrecedenceAndValidation(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, "minimum_exec_to_bg_seconds = 21\n")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.MinimumExecToBgSeconds != 21 {
		t.Fatalf("expected file minimum_exec_to_bg_seconds=21, got %d", cfg.Settings.MinimumExecToBgSeconds)
	}
	assertConfigSource(t, cfg, "minimum_exec_to_bg_seconds", "file")

	t.Setenv("KENT_MINIMUM_EXEC_TO_BG_SECONDS", "18")
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.MinimumExecToBgSeconds != 18 {
		t.Fatalf("expected env minimum_exec_to_bg_seconds=18, got %d", cfg.Settings.MinimumExecToBgSeconds)
	}
	assertConfigSource(t, cfg, "minimum_exec_to_bg_seconds", "env")

	t.Setenv("KENT_MINIMUM_EXEC_TO_BG_SECONDS", "0")
	if _, err := Load(workspace, LoadOptions{}); err == nil {
		t.Fatal("expected invalid minimum_exec_to_bg_seconds")
	}
}

func TestLoadBGShellsOutputPrecedenceAndValidation(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, "bg_shells_output = \"concise\"\n")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.BGShellsOutput != BGShellsOutputConcise {
		t.Fatalf("expected file bg_shells_output=concise, got %q", cfg.Settings.BGShellsOutput)
	}
	assertConfigSource(t, cfg, "bg_shells_output", "file")

	t.Setenv("KENT_BG_SHELLS_OUTPUT", "verbose")
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.BGShellsOutput != BGShellsOutputVerbose {
		t.Fatalf("expected env bg_shells_output=verbose, got %q", cfg.Settings.BGShellsOutput)
	}
	assertConfigSource(t, cfg, "bg_shells_output", "env")

	t.Setenv("KENT_BG_SHELLS_OUTPUT", "loud")
	if _, err := Load(workspace, LoadOptions{}); err == nil {
		t.Fatal("expected invalid bg_shells_output")
	}
}

func TestLoadShellPostprocessingPrecedenceAndValidation(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, "[shell]\npostprocessing_mode = \"all\"\npostprocess_hook = \"/tmp/file-hook\"\n")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Shell.PostprocessingMode != ShellPostprocessingModeAll {
		t.Fatalf("expected file shell.postprocessing_mode=all, got %q", cfg.Settings.Shell.PostprocessingMode)
	}
	if cfg.Settings.Shell.PostprocessHook != "/tmp/file-hook" {
		t.Fatalf("expected file shell.postprocess_hook, got %q", cfg.Settings.Shell.PostprocessHook)
	}
	assertConfigSource(t, cfg, "shell.postprocessing_mode", "file")
	assertConfigSource(t, cfg, "shell.postprocess_hook", "file")

	t.Setenv("KENT_SHELL_POSTPROCESSING_MODE", "user")
	t.Setenv("KENT_SHELL_POSTPROCESS_HOOK", "/tmp/env-hook")
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Shell.PostprocessingMode != ShellPostprocessingModeUser {
		t.Fatalf("expected env shell.postprocessing_mode=user, got %q", cfg.Settings.Shell.PostprocessingMode)
	}
	if cfg.Settings.Shell.PostprocessHook != "/tmp/env-hook" {
		t.Fatalf("expected env shell.postprocess_hook, got %q", cfg.Settings.Shell.PostprocessHook)
	}
	assertConfigSource(t, cfg, "shell.postprocessing_mode", "env")
	assertConfigSource(t, cfg, "shell.postprocess_hook", "env")

	t.Setenv("KENT_SHELL_POSTPROCESSING_MODE", "broken")
	if _, err := Load(workspace, LoadOptions{}); err == nil {
		t.Fatal("expected invalid shell.postprocessing_mode")
	}
}

func TestLoadAcceptsCustomThinkingLevel(t *testing.T) {
	_, workspace := newConfigTestEnv(t)
	t.Setenv("KENT_THINKING_LEVEL", "ultra")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.ThinkingLevel != "ultra" {
		t.Fatalf("expected custom thinking level preserved, got %q", cfg.Settings.ThinkingLevel)
	}
}

func TestLoadExpandsTildePersistenceRootFromEnv(t *testing.T) {
	home, workspace := newConfigTestEnv(t)
	t.Setenv("KENT_PERSISTENCE_ROOT", "~/.kent-custom")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if got := cfg.PersistenceRoot; got != filepath.Join(home, ".kent-custom") {
		t.Fatalf("expanded persistence root mismatch: %q", got)
	}
}

func TestLoadOpenAIBaseURLPrecedence(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, `openai_base_url = "http://file.local/v1"`)

	t.Setenv("KENT_OPENAI_BASE_URL", "http://env.local/v1")
	cfg, err := Load(workspace, LoadOptions{OpenAIBaseURL: "http://cli.local/v1"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Settings.OpenAIBaseURL != "http://cli.local/v1" {
		t.Fatalf("expected cli openai base url, got %q", cfg.Settings.OpenAIBaseURL)
	}
	if got := cfg.Source.Sources["openai_base_url"]; got != "cli" {
		t.Fatalf("expected openai_base_url source cli, got %q", got)
	}
}

func TestNormalizeSettingsForPersistence_AllowsDisabledThinkingWithReviewerInheritance(t *testing.T) {
	settings := configRegistry.defaultState().Settings
	settings.Model = "gpt-5.6-sol"
	settings.ThinkingLevel = ""
	settings.Reviewer = ReviewerSettings{
		Frequency:      "edits",
		Model:          "",
		ThinkingLevel:  "",
		TimeoutSeconds: defaultReviewerTimeoutSec,
		VerboseOutput:  false,
	}

	normalized, err := NormalizeSettingsForPersistenceWithSources(settings, nil)
	if err != nil {
		t.Fatalf("normalize settings for persistence: %v", err)
	}
	if normalized.Reviewer.Model != "gpt-5.6-sol" {
		t.Fatalf("expected reviewer model to inherit main model, got %q", normalized.Reviewer.Model)
	}
	if normalized.Reviewer.ThinkingLevel != "" {
		t.Fatalf("expected reviewer thinking to stay disabled, got %q", normalized.Reviewer.ThinkingLevel)
	}
}

func TestNormalizeSettingsForPersistence_AllowsProviderOverrideWithExplicitPersistedModel(t *testing.T) {
	settings := configRegistry.defaultState().Settings
	settings.Model = "my-team-alias"
	settings.ProviderOverride = "openai"

	normalized, err := NormalizeSettingsForPersistenceWithSources(settings, nil)
	if err != nil {
		t.Fatalf("normalize settings for persistence: %v", err)
	}
	if normalized.ProviderOverride != "openai" {
		t.Fatalf("expected provider_override preserved, got %q", normalized.ProviderOverride)
	}
}

func TestNormalizeSettingsForPersistenceRejectsModelContextWindowBelowMinimum(t *testing.T) {
	settings := configRegistry.defaultState().Settings
	settings.ModelContextWindow = 39999
	settings.ContextCompactionThresholdTokens = 30000

	if _, err := NormalizeSettingsForPersistenceWithSources(settings, nil); err == nil {
		t.Fatal("expected model_context_window below minimum validation error")
	} else if !errors.Is(err, errModelContextWindowBelowMinimum) {
		t.Fatalf("expected model context window minimum validation detail, got %v", err)
	}
}

func TestNormalizeSettingsForPersistenceWithSourcesRejectsModelContextWindowBelowMinimum(t *testing.T) {
	settings := configRegistry.defaultState().Settings
	settings.ModelContextWindow = 39999
	settings.ContextCompactionThresholdTokens = 30000
	sources := configRegistry.defaultSourceMap()
	sources["model_context_window"] = "file"

	if _, err := NormalizeSettingsForPersistenceWithSources(settings, sources); err == nil {
		t.Fatal("expected model_context_window below minimum validation error")
	} else if !errors.Is(err, errModelContextWindowBelowMinimum) {
		t.Fatalf("expected model context window minimum validation detail, got %v", err)
	}
}

func TestLoadCanonicalTimeoutEnvAndSourceKeys(t *testing.T) {
	_, workspace := newConfigTestEnv(t)
	t.Setenv("KENT_TIMEOUTS_MODEL_REQUEST_SECONDS", "123")
	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Timeouts.ModelRequestSeconds != 123 {
		t.Fatalf("expected canonical env model timeout, got %d", cfg.Settings.Timeouts.ModelRequestSeconds)
	}
	if got := cfg.Source.Sources["timeouts.model_request_seconds"]; got != "env" {
		t.Fatalf("expected timeouts.model_request_seconds source env, got %q", got)
	}
}

func TestLoadStorePrecedence(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, `store = true`)

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if !cfg.Settings.Store {
		t.Fatalf("expected file store=true")
	}
	if got := cfg.Source.Sources["store"]; got != "file" {
		t.Fatalf("expected store source file, got %q", got)
	}

	t.Setenv("KENT_STORE", "false")
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Store {
		t.Fatalf("expected env store=false")
	}
	if got := cfg.Source.Sources["store"]; got != "env" {
		t.Fatalf("expected store source env, got %q", got)
	}
}

func TestLoadIgnoresUnknownEnvVars(t *testing.T) {
	_, workspace := newConfigTestEnv(t)
	t.Setenv("KENT_PROVIDER_CAPABILITY_ID", "custom-provider")
	t.Setenv("KENT_MODEL_SUPPORTS_REASONING_EFFORT", "true")
	t.Setenv("KENT_MODEL_TIMEOUT_SECONDS", "123")
	t.Setenv("KENT_USE_NATIVE_COMPACTION", "true")
	t.Setenv("KENT_REVIEWER_MAX_SUGGESTIONS", "15")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.ModelCapabilities.SupportsReasoningEffort {
		t.Fatal("expected unknown legacy env vars to be ignored")
	}
	if cfg.Settings.Timeouts.ModelRequestSeconds != defaultModelTimeoutSeconds {
		t.Fatalf("expected unknown legacy env vars not to affect model timeout, got %d", cfg.Settings.Timeouts.ModelRequestSeconds)
	}
	if cfg.Settings.CompactionMode != CompactionModeLocal {
		t.Fatalf("expected unknown legacy env vars not to affect compaction mode, got %q", cfg.Settings.CompactionMode)
	}
}

func TestLoadRejectsRemovedReviewerMaxSuggestionsFileKey(t *testing.T) {
	if err := loadConfigTestFileError(t, "[reviewer]\nmax_suggestions = 15\n", LoadOptions{}); err == nil {
		t.Fatal("expected removed reviewer.max_suggestions file key to be rejected")
	}
}

func TestLoadAllowNonCwdEditsPrecedence(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, `allow_non_cwd_edits = true`)

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if !cfg.Settings.AllowNonCwdEdits {
		t.Fatalf("expected file allow_non_cwd_edits=true")
	}
	if got := cfg.Source.Sources["allow_non_cwd_edits"]; got != "file" {
		t.Fatalf("expected allow_non_cwd_edits source file, got %q", got)
	}

	t.Setenv("KENT_ALLOW_NON_CWD_EDITS", "false")
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.AllowNonCwdEdits {
		t.Fatalf("expected env allow_non_cwd_edits=false")
	}
	if got := cfg.Source.Sources["allow_non_cwd_edits"]; got != "env" {
		t.Fatalf("expected allow_non_cwd_edits source env, got %q", got)
	}
}

func TestLoadDebugPrecedenceAndValidation(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, "debug = true\n")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if !cfg.Settings.Debug {
		t.Fatalf("expected file debug=true")
	}
	assertConfigSource(t, cfg, "debug", "file")

	t.Setenv("KENT_DEBUG", "false")
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Debug {
		t.Fatalf("expected env debug=false")
	}
	assertConfigSource(t, cfg, "debug", "env")

	t.Setenv("KENT_DEBUG", "broken")
	if _, err := Load(workspace, LoadOptions{}); err == nil {
		t.Fatal("expected invalid KENT_DEBUG error")
	}
}

func TestLoadServerHostPortPrecedenceAndValidation(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, "server_host = \"127.0.0.2\"\nserver_port = 54321\n")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.ServerHost != "127.0.0.2" || cfg.Settings.ServerPort != 54321 {
		t.Fatalf("unexpected server settings from file: host=%q port=%d", cfg.Settings.ServerHost, cfg.Settings.ServerPort)
	}
	assertConfigSource(t, cfg, "server_host", "file")
	assertConfigSource(t, cfg, "server_port", "file")

	t.Setenv("KENT_SERVER_HOST", "::1")
	t.Setenv("KENT_SERVER_PORT", "65432")
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.ServerHost != "::1" || cfg.Settings.ServerPort != 65432 {
		t.Fatalf("unexpected server settings from env: host=%q port=%d", cfg.Settings.ServerHost, cfg.Settings.ServerPort)
	}
	assertConfigSource(t, cfg, "server_host", "env")
	assertConfigSource(t, cfg, "server_port", "env")
	if got := net.JoinHostPort(cfg.Settings.ServerHost, strconv.Itoa(cfg.Settings.ServerPort)); got != "[::1]:65432" {
		t.Fatalf("ServerListenAddress = %q, want [::1]:65432", got)
	}
	if got := ServerHTTPBaseURL(cfg); got != "http://[::1]:65432" {
		t.Fatalf("ServerHTTPBaseURL = %q, want http://[::1]:65432", got)
	}
	if got := ServerRPCURL(cfg); got != "ws://[::1]:65432/rpc" {
		t.Fatalf("ServerRPCURL = %q, want ws://[::1]:65432/rpc", got)
	}

	t.Setenv("KENT_SERVER_PORT", "broken")
	if _, err := Load(workspace, LoadOptions{}); err == nil {
		t.Fatal("expected invalid KENT_SERVER_PORT error")
	}
}

func TestLoadContextCompactionThresholdPrecedence(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, `context_compaction_threshold_tokens = 123456`)

	t.Setenv("KENT_CONTEXT_COMPACTION_THRESHOLD_TOKENS", "234567")
	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.ContextCompactionThresholdTokens != 234567 {
		t.Fatalf("expected env threshold override, got %d", cfg.Settings.ContextCompactionThresholdTokens)
	}
	assertConfigSource(t, cfg, "context_compaction_threshold_tokens", "env")
}

func TestLoadCompactionModePrecedence(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, "compaction_mode = \"local\"\n")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.CompactionMode != CompactionModeLocal {
		t.Fatalf("expected file override compaction_mode=local, got %q", cfg.Settings.CompactionMode)
	}
	if got := cfg.Source.Sources["compaction_mode"]; got != "file" {
		t.Fatalf("expected compaction_mode source file, got %q", got)
	}

	t.Setenv("KENT_COMPACTION_MODE", "none")
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.CompactionMode != CompactionModeNone {
		t.Fatalf("expected env override compaction_mode=none, got %q", cfg.Settings.CompactionMode)
	}
	if got := cfg.Source.Sources["compaction_mode"]; got != "env" {
		t.Fatalf("expected compaction_mode source env, got %q", got)
	}
}

func TestLoadRejectsRemovedUseNativeCompactionSetting(t *testing.T) {
	if err := loadConfigTestFileError(t, "use_native_compaction = true\n", LoadOptions{}); err == nil {
		t.Fatal("expected unsupported use_native_compaction settings key error")
	}
}

func TestLoadRejectsUnrelatedUnknownSettingKeys(t *testing.T) {
	if err := loadConfigTestFileError(t, "model = \"gpt-5\"\nfoo = 1\n", LoadOptions{}); err == nil {
		t.Fatal("expected unknown settings key error")
	} else if !unknownSettingsKeyReported(err, "foo") {
		t.Fatalf("expected unknown key name in error, got %v", err)
	}
}

func TestLoadRejectsInvalidCompactionMode(t *testing.T) {
	if err := loadConfigTestFileError(t, "compaction_mode = \"remote\"\n", LoadOptions{}); err == nil {
		t.Fatal("expected invalid compaction_mode validation error")
	}
}

func TestLoadModelContextWindowPrecedence(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, "model_context_window = 350000\ncontext_compaction_threshold_tokens = 250000\n")

	t.Setenv("KENT_MODEL_CONTEXT_WINDOW", "420000")
	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.ModelContextWindow != 420000 {
		t.Fatalf("expected env model context window override, got %d", cfg.Settings.ModelContextWindow)
	}
	if got := cfg.Source.Sources["model_context_window"]; got != "env" {
		t.Fatalf("expected model_context_window source env, got %q", got)
	}
}

func TestLoadRejectsModelContextWindowBelowMinimum(t *testing.T) {
	err := loadConfigTestFileError(t, "model_context_window = 39999\ncontext_compaction_threshold_tokens = 30000\n", LoadOptions{})
	if err == nil {
		t.Fatal("expected model_context_window below minimum validation error")
	}
	if !errors.Is(err, errModelContextWindowBelowMinimum) {
		t.Fatalf("expected model context window minimum validation detail, got %v", err)
	}
}

func TestLoadRejectsModelContextWindowZeroWithMinimumError(t *testing.T) {
	err := loadConfigTestFileError(t, "model_context_window = 0\n", LoadOptions{})
	if err == nil {
		t.Fatal("expected model_context_window zero validation error")
	}
	if !errors.Is(err, errModelContextWindowBelowMinimum) {
		t.Fatalf("expected model context window minimum validation detail, got %v", err)
	}
}

func TestLoadAcceptsModelContextWindowMinimum(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, "model_context_window = 40000\ncontext_compaction_threshold_tokens = 25000\npre_submit_compaction_lead_tokens = 1000\n", LoadOptions{})
	if cfg.Settings.ModelContextWindow != 40000 {
		t.Fatalf("expected model_context_window=40000, got %d", cfg.Settings.ModelContextWindow)
	}
}

func TestLoadRejectsCompactionThresholdNotBelowContextWindow(t *testing.T) {
	if err := loadConfigTestFileError(t, "model_context_window = 300000\ncontext_compaction_threshold_tokens = 300000\n", LoadOptions{}); err == nil {
		t.Fatal("expected threshold/window validation error")
	}
}

func TestLoadRejectsCompactionThresholdBelowHalfWindow(t *testing.T) {
	if err := loadConfigTestFileError(t, "model_context_window = 300000\ncontext_compaction_threshold_tokens = 149999\n", LoadOptions{}); err == nil {
		t.Fatal("expected threshold minimum-window-percent validation error")
	} else if !errors.Is(err, errCompactionThresholdBelowMinimum) {
		t.Fatalf("expected threshold minimum-window-percent validation detail, got %v", err)
	}
}

func TestLoadRejectsPreSubmitLeadBandBelowHalfWindow(t *testing.T) {
	if err := loadConfigTestFileError(t, "model_context_window = 300000\ncontext_compaction_threshold_tokens = 200000\npre_submit_compaction_lead_tokens = 100000\n", LoadOptions{}); err == nil {
		t.Fatal("expected pre-submit effective threshold validation error")
	} else if !errors.Is(err, errPreSubmitThresholdBelowMinimum) {
		t.Fatalf("expected pre-submit effective threshold validation detail, got %v", err)
	}
}
