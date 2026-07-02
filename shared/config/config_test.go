package config

import (
	"core/shared/toolspec"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestPreparePersistenceRootRefusesProcessStartRootUnderGoTest(t *testing.T) {
	originalHome := processStartHome
	originalAccountHome := processStartAccountHome
	processStartHome = filepath.Join(string(filepath.Separator), "kent-test-home")
	processStartAccountHome = ""
	t.Cleanup(func() {
		processStartHome = originalHome
		processStartAccountHome = originalAccountHome
	})

	_, err := preparePersistenceRoot(filepath.Join(processStartHome, ConfigDirName))
	if err == nil {
		t.Fatal("expected process-start persistence root to be refused under go test")
	}
	if !errors.Is(err, errProtectedPersistenceRoot) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPreparePersistenceRootAllowsIsolatedTempHomeUnderGoTest(t *testing.T) {
	originalHome := processStartHome
	originalAccountHome := processStartAccountHome
	processStartHome = t.TempDir()
	processStartAccountHome = filepath.Join(string(filepath.Separator), "app-real-home")
	t.Cleanup(func() {
		processStartHome = originalHome
		processStartAccountHome = originalAccountHome
	})

	if _, err := preparePersistenceRoot(filepath.Join(processStartHome, ConfigDirName)); err != nil {
		t.Fatalf("prepare temp persistence root: %v", err)
	}
}

func TestLoadUsesDefaultsWithoutCreatingConfigOnFirstUse(t *testing.T) {
	home, workspace := newConfigTestEnv(t)
	cfg := loadConfigTestApp(t, workspace, LoadOptions{})

	settingsPath := filepath.Join(home, ConfigDirName, "config.toml")
	if _, err := os.Stat(settingsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected config file to stay absent, got err=%v", err)
	}
	rgConfigPath := filepath.Join(home, ConfigDirName, managedRGConfigName)
	rgConfigBytes, err := os.ReadFile(rgConfigPath)
	if err != nil {
		t.Fatalf("read managed rg config: %v", err)
	}
	if string(rgConfigBytes) != managedRGConfigContents {
		t.Fatalf("managed rg config contents mismatch: %q", string(rgConfigBytes))
	}
	if cfg.Source.CreatedDefaultConfig {
		t.Fatalf("expected CreatedDefaultConfig=false")
	}
	if cfg.Source.SettingsFileExists {
		t.Fatalf("expected SettingsFileExists=false")
	}
	if cfg.Settings.Model != defaultModel {
		t.Fatalf("default model mismatch: %q", cfg.Settings.Model)
	}
	if cfg.Settings.WebSearch != "native" {
		t.Fatalf("default web_search mismatch: %q", cfg.Settings.WebSearch)
	}
	if cfg.Settings.ModelVerbosity != defaultModelVerbosity {
		t.Fatalf("default model_verbosity mismatch: %q", cfg.Settings.ModelVerbosity)
	}
	if cfg.Settings.NotificationMethod != "auto" {
		t.Fatalf("default notification_method mismatch: %q", cfg.Settings.NotificationMethod)
	}
	if !cfg.Settings.ToolPreambles {
		t.Fatalf("expected default tool_preambles=true")
	}
	if cfg.Settings.PriorityRequestMode {
		t.Fatalf("expected default priority_request_mode=false")
	}
	if cfg.Settings.Debug {
		t.Fatalf("expected default debug=false")
	}
	if got := cfg.PersistenceRoot; got != filepath.Join(home, ConfigDirName) {
		t.Fatalf("default persistence root mismatch: %q", got)
	}
	if _, err := os.Stat(filepath.Join(cfg.PersistenceRoot, "sessions")); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected legacy sessions root to stay absent, got %v", err)
	}
	if !cfg.Settings.EnabledTools[toolspec.ToolExecCommand] || !cfg.Settings.EnabledTools[toolspec.ToolViewImage] || !cfg.Settings.EnabledTools[toolspec.ToolPatch] || !cfg.Settings.EnabledTools[toolspec.ToolAskQuestion] {
		t.Fatalf("expected all default tools enabled: %+v", cfg.Settings.EnabledTools)
	}
	if !cfg.Settings.EnabledTools[toolspec.ToolTriggerHandoff] {
		t.Fatalf("expected %s enabled in static defaults", toolspec.ToolTriggerHandoff)
	}
	if got := cfg.Source.Sources["tools.trigger_handoff"]; got != "default" {
		t.Fatalf("expected untouched %s source to remain default, got %q", toolspec.ToolTriggerHandoff, got)
	}
	if !cfg.Settings.EnabledTools[toolspec.ToolWebSearch] {
		t.Fatalf("expected web_search tool enabled by default: %+v", cfg.Settings.EnabledTools)
	}
	if cfg.Settings.ContextCompactionThresholdTokens != defaultCompactionThreshold {
		t.Fatalf("default compaction threshold mismatch: %d", cfg.Settings.ContextCompactionThresholdTokens)
	}
	if cfg.Settings.PreSubmitCompactionLeadTokens != 35_000 {
		t.Fatalf("default pre-submit runway mismatch: %d", cfg.Settings.PreSubmitCompactionLeadTokens)
	}
	if cfg.Settings.MinimumExecToBgSeconds != defaultMinimumExecToBgSec {
		t.Fatalf("default minimum_exec_to_bg_seconds mismatch: %d", cfg.Settings.MinimumExecToBgSeconds)
	}
	if cfg.Settings.ModelContextWindow != defaultModelContextWindow {
		t.Fatalf("default model context window mismatch: %d", cfg.Settings.ModelContextWindow)
	}
	if cfg.Settings.Store {
		t.Fatalf("expected default store=false")
	}
	if cfg.Settings.AllowNonCwdEdits {
		t.Fatalf("expected default allow_non_cwd_edits=false")
	}
	if cfg.Settings.CompactionMode != CompactionModeLocal {
		t.Fatalf("expected default compaction_mode=local, got %q", cfg.Settings.CompactionMode)
	}
	if cfg.Settings.ShellOutputMaxChars != 16000 {
		t.Fatalf("default shell_output_max_chars mismatch: %d", cfg.Settings.ShellOutputMaxChars)
	}
	if cfg.Settings.BGShellsOutput != BGShellsOutputDefault {
		t.Fatalf("default bg_shells_output mismatch: %q", cfg.Settings.BGShellsOutput)
	}
	if cfg.Settings.Shell.PostprocessingMode != ShellPostprocessingModeBuiltin {
		t.Fatalf("default shell.postprocessing_mode mismatch: %q", cfg.Settings.Shell.PostprocessingMode)
	}
	if cfg.Settings.Shell.PostprocessHook != "" {
		t.Fatalf("default shell.postprocess_hook mismatch: %q", cfg.Settings.Shell.PostprocessHook)
	}
	if got := cfg.Settings.Worktrees.BaseDir; got != filepath.Join(cfg.PersistenceRoot, "worktrees") {
		t.Fatalf("default worktrees.base_dir mismatch: %q", got)
	}
	if cfg.Settings.Worktrees.SetupScript != "" {
		t.Fatalf("expected default worktrees.setup_script empty, got %q", cfg.Settings.Worktrees.SetupScript)
	}
	if cfg.Settings.Reviewer.Frequency != defaultReviewerFrequency {
		t.Fatalf("expected default reviewer.frequency=%s, got %q", defaultReviewerFrequency, cfg.Settings.Reviewer.Frequency)
	}
	if cfg.Settings.Reviewer.Model != cfg.Settings.Model {
		t.Fatalf("default reviewer model mismatch: %q", cfg.Settings.Reviewer.Model)
	}
	if cfg.Settings.Reviewer.ThinkingLevel != cfg.Settings.ThinkingLevel {
		t.Fatalf("default reviewer thinking_level mismatch: %q", cfg.Settings.Reviewer.ThinkingLevel)
	}
	if cfg.Settings.Reviewer.TimeoutSeconds != defaultReviewerTimeoutSec {
		t.Fatalf("default reviewer timeout mismatch: %d", cfg.Settings.Reviewer.TimeoutSeconds)
	}
	if cfg.Settings.Reviewer.VerboseOutput {
		t.Fatalf("expected default reviewer verbose_output=false")
	}
}

func TestLoadUsesExplicitConfigRootWithoutHomeMutation(t *testing.T) {
	configRoot := t.TempDir()
	workspace := t.TempDir()

	cfg, err := Load(workspace, LoadOptions{ConfigRoot: configRoot})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Source.HomeSettingsPath != filepath.Join(configRoot, "config.toml") {
		t.Fatalf("home settings path = %q, want explicit config root", cfg.Source.HomeSettingsPath)
	}
	if cfg.PersistenceRoot != configRoot {
		t.Fatalf("persistence root = %q, want explicit config root", cfg.PersistenceRoot)
	}
	if _, err := os.Stat(filepath.Join(configRoot, managedRGConfigName)); err != nil {
		t.Fatalf("expected managed rg config in explicit config root: %v", err)
	}
}

func TestLoadRejectsPersistenceRootInConfigFile(t *testing.T) {
	configRoot := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(configRoot, "config.toml"), []byte("persistence_root = \"/tmp/custom\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(workspace, LoadOptions{ConfigRoot: configRoot})
	if !errors.Is(err, errPersistenceRootInConfigFile) {
		t.Fatalf("expected persistence_root migration error, got: %v", err)
	}
}

func TestLoadUsesPersistenceRootEnvForConfigAndData(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	t.Setenv(PersistenceRootEnvName, root)

	cfg, err := Load(workspace, LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Source.HomeSettingsPath != filepath.Join(root, "config.toml") {
		t.Fatalf("home settings path = %q, want env root config.toml", cfg.Source.HomeSettingsPath)
	}
	if cfg.PersistenceRoot != root {
		t.Fatalf("persistence root = %q, want env root %q", cfg.PersistenceRoot, root)
	}
	if got := cfg.Source.Sources["persistence_root"]; got != "env" {
		t.Fatalf("persistence_root source = %q, want env", got)
	}
}

func TestLoadFlagOverridesPersistenceRootEnv(t *testing.T) {
	flagRoot := t.TempDir()
	envRoot := t.TempDir()
	workspace := t.TempDir()
	t.Setenv(PersistenceRootEnvName, envRoot)

	cfg, err := Load(workspace, LoadOptions{ConfigRoot: flagRoot})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PersistenceRoot != flagRoot {
		t.Fatalf("persistence root = %q, want flag root %q", cfg.PersistenceRoot, flagRoot)
	}
	if got := cfg.Source.Sources["persistence_root"]; got != "flag" {
		t.Fatalf("persistence_root source = %q, want flag", got)
	}
}

func TestWriteManagedRGConfigFileForSettingsPathRejectsEmptyPath(t *testing.T) {
	if _, err := writeManagedRGConfigFileForSettingsPath(" \t "); err == nil {
		t.Fatal("expected empty settings path error")
	}
}

func TestLoadHonorsHOMEEnvironmentForDefaultConfigRoot(t *testing.T) {
	home, workspace := newConfigTestEnv(t)

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.PersistenceRoot != filepath.Join(home, ConfigDirName) {
		t.Fatalf("persistence root = %q, want HOME-scoped root", cfg.PersistenceRoot)
	}
	if cfg.Source.HomeSettingsPath != filepath.Join(home, ConfigDirName, "config.toml") {
		t.Fatalf("home settings path = %q, want HOME-scoped config", cfg.Source.HomeSettingsPath)
	}
}

func TestLoadTrimsWorkspaceRootBeforeResolving(t *testing.T) {
	_, workspace := newConfigTestEnv(t)

	cfg, err := Load("  "+workspace+"  ", LoadOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.WorkspaceRoot != workspace {
		t.Fatalf("workspace root = %q, want %q", cfg.WorkspaceRoot, workspace)
	}
}

func TestLoadAppliesWorkspaceConfigBeforeEnvBeforeCLI(t *testing.T) {
	home, workspace := newConfigTestEnv(t)
	t.Setenv("KENT_MODEL", "env-model")
	if err := os.MkdirAll(filepath.Join(home, ConfigDirName), 0o755); err != nil {
		t.Fatalf("create home config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ConfigDirName, "config.toml"), []byte("model = \"home-model\"\n"), 0o644); err != nil {
		t.Fatalf("write home config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ConfigDirName), 0o755); err != nil {
		t.Fatalf("create workspace config dir: %v", err)
	}
	workspaceConfigPath := filepath.Join(workspace, ConfigDirName, "config.toml")
	if err := os.WriteFile(workspaceConfigPath, []byte("model = \"workspace-model\"\nthinking_level = \"high\"\n"), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}

	cfg, err := Load(workspace, LoadOptions{ThinkingLevel: "low"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Settings.Model != "env-model" {
		t.Fatalf("model = %q, want env-model", cfg.Settings.Model)
	}
	if cfg.Settings.ThinkingLevel != "low" {
		t.Fatalf("thinking level = %q, want cli override", cfg.Settings.ThinkingLevel)
	}
	if cfg.Source.SettingsPath != workspaceConfigPath || !cfg.Source.WorkspaceSettingsFileExists {
		t.Fatalf("unexpected workspace source report: %+v", cfg.Source)
	}
	if cfg.Source.Sources["model"] != "env" || cfg.Source.Sources["thinking_level"] != "cli" {
		t.Fatalf("unexpected sources: %+v", cfg.Source.Sources)
	}
}

func TestLoadGlobalSkipsWorkspaceConfigLayer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENT_MODEL", "env-model")
	if err := os.MkdirAll(filepath.Join(home, ConfigDirName), 0o755); err != nil {
		t.Fatalf("create home config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ConfigDirName, "config.toml"), []byte("model = \"home-model\"\n"), 0o644); err != nil {
		t.Fatalf("write home config: %v", err)
	}

	cfg, err := LoadGlobal(LoadOptions{})
	if err != nil {
		t.Fatalf("load global: %v", err)
	}
	if cfg.WorkspaceRoot != "" {
		t.Fatalf("workspace root = %q, want empty", cfg.WorkspaceRoot)
	}
	if cfg.Settings.Model != "env-model" {
		t.Fatalf("model = %q, want env-model", cfg.Settings.Model)
	}
	if cfg.Source.WorkspaceSettingsLayerEnabled || cfg.Source.WorkspaceSettingsPath != "" {
		t.Fatalf("unexpected workspace source report: %+v", cfg.Source)
	}
}

func TestLoadGlobalRejectsModelContextWindowBelowMinimum(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ConfigDirName), 0o755); err != nil {
		t.Fatalf("create home config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ConfigDirName, "config.toml"), []byte("model_context_window = 39999\ncontext_compaction_threshold_tokens = 30000\n"), 0o644); err != nil {
		t.Fatalf("write home config: %v", err)
	}

	if _, err := LoadGlobal(LoadOptions{}); err == nil {
		t.Fatal("expected model_context_window below minimum validation error")
	} else if !errors.Is(err, errModelContextWindowBelowMinimum) {
		t.Fatalf("expected model context window minimum validation detail, got %v", err)
	}
}

func TestEnsureManagedRGConfigFilePreservesExistingContents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := ResolveManagedRGConfigPath()
	if err != nil {
		t.Fatalf("resolve managed rg config path: %v", err)
	}
	if err := ensureSettingsDir(path); err != nil {
		t.Fatalf("ensure settings dir: %v", err)
	}
	const existing = "--max-columns=80\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("write existing managed rg config: %v", err)
	}

	createdPath, created, err := EnsureManagedRGConfigFile()
	if err != nil {
		t.Fatalf("ensure managed rg config file: %v", err)
	}
	if created {
		t.Fatal("expected existing managed rg config not to be replaced")
	}
	if createdPath != path {
		t.Fatalf("managed rg config path = %q, want %q", createdPath, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read managed rg config: %v", err)
	}
	if string(data) != existing {
		t.Fatalf("managed rg config contents = %q, want %q", string(data), existing)
	}
}

func TestResolveManagedRGConfigPathHonorsPersistenceRootEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	t.Setenv(PersistenceRootEnvName, root)

	path, err := ResolveManagedRGConfigPath()
	if err != nil {
		t.Fatalf("resolve managed rg config path: %v", err)
	}
	want := filepath.Join(root, managedRGConfigName)
	if path != want {
		t.Fatalf("managed rg config path = %q, want %q (under selected root, not home)", path, want)
	}
}

func TestResolveManagedRGConfigPathDefaultsToHomeWithoutEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(PersistenceRootEnvName, "")

	path, err := ResolveManagedRGConfigPath()
	if err != nil {
		t.Fatalf("resolve managed rg config path: %v", err)
	}
	want := filepath.Join(home, ConfigDirName, managedRGConfigName)
	if path != want {
		t.Fatalf("managed rg config path = %q, want %q", path, want)
	}
}

func TestNormalizePersistenceRootExpandsTildeAndAbsolutizes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := NormalizePersistenceRoot("~/nested/root")
	if err != nil {
		t.Fatalf("normalize persistence root: %v", err)
	}
	want := filepath.Join(home, "nested", "root")
	if got != want {
		t.Fatalf("normalized tilde root = %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("expected absolute path, got %q", got)
	}
}

func TestLoadSubagentRoleFromFile(t *testing.T) {
	home, workspace, configPath := newConfigTestFile(t)
	contents := strings.Join([]string{
		"model = \"gpt-5.5\"",
		"",
		"[subagents.fast]",
		"model = \"gpt-5.4-mini\"",
		"thinking_level = \"low\"",
		"",
		"[subagents.fast.reviewer]",
		"system_prompt_file = \"fast-reviewer.md\"",
		"",
		"[subagents.fast.tools]",
		"patch = false",
	}, "\n")
	writeConfigTestFile(t, configPath, contents)

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	role, ok := cfg.Settings.Subagents[BuiltInSubagentRoleFast]
	if !ok {
		t.Fatalf("expected fast subagent role, got %+v", cfg.Settings.Subagents)
	}
	if role.Settings.Model != "gpt-5.4-mini" {
		t.Fatalf("role model = %q, want gpt-5.4-mini", role.Settings.Model)
	}
	if role.Settings.ThinkingLevel != "low" {
		t.Fatalf("role thinking = %q, want low", role.Settings.ThinkingLevel)
	}
	if role.Settings.EnabledTools[toolspec.ToolPatch] {
		t.Fatalf("expected fast role patch tool disabled, got %+v", role.Settings.EnabledTools)
	}
	if want := filepath.Join(home, ConfigDirName, "fast-reviewer.md"); role.Settings.Reviewer.SystemPromptFile != want {
		t.Fatalf("role reviewer system prompt file = %q, want %q", role.Settings.Reviewer.SystemPromptFile, want)
	}
	if role.Sources["model"] != "file" || role.Sources["thinking_level"] != "file" || role.Sources["tools.patch"] != "file" || role.Sources["reviewer.system_prompt_file"] != "file" {
		t.Fatalf("unexpected role sources: %+v", role.Sources)
	}
	if _, exists := role.Sources["reviewer.model"]; exists {
		t.Fatalf("did not expect inherited reviewer model to be marked explicit, got %+v", role.Sources)
	}
}

func TestLoadSubagentRoleMetadataFromFile(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	contents := strings.Join([]string{
		"[subagents.research]",
		"description = \"  Deep    repo\\nresearch  \"",
		"agent_callable = false",
		"thinking_level = \"high\"",
	}, "\n")
	writeConfigTestFile(t, configPath, contents)

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	role := cfg.Settings.Subagents["research"]
	if role.Description != "Deep repo research" {
		t.Fatalf("description = %q, want normalized description", role.Description)
	}
	if role.AgentCallable || !role.AgentCallableSet {
		t.Fatalf("agent callable metadata = (%t, %t), want false set", role.AgentCallable, role.AgentCallableSet)
	}
	if _, exists := role.Sources["description"]; exists {
		t.Fatalf("description should not be runtime source, got %+v", role.Sources)
	}
	if _, exists := role.Sources["agent_callable"]; exists {
		t.Fatalf("agent_callable should not be runtime source, got %+v", role.Sources)
	}
}

func TestLoadSubagentRoleRejectsReservedNames(t *testing.T) {
	for _, reserved := range []string{"default", "none", "self"} {
		t.Run(reserved, func(t *testing.T) {
			err := loadConfigTestFileError(t, "[subagents."+reserved+"]\nmodel = \"gpt-5.5\"\n", LoadOptions{})
			if err == nil {
				t.Fatal("expected reserved role to fail")
			}
			if !errors.Is(err, errInvalidSubagentKey) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadSubagentRoleRejectsInvalidMetadata(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		match func(error) bool
	}{
		{name: "description type", body: "[subagents.worker]\ndescription = 123\n", match: func(err error) bool {
			var typeErr *SettingsKeyTypeError
			return errors.As(err, &typeErr) && typeErr.ExpectedType == "string"
		}},
		{name: "agent callable type", body: "[subagents.worker]\nagent_callable = \"no\"\n", match: func(err error) bool {
			var typeErr *SettingsKeyTypeError
			return errors.As(err, &typeErr) && typeErr.ExpectedType == "boolean"
		}},
		{name: "description length", body: "[subagents.worker]\ndescription = \"" + strings.Repeat("x", MaxSubagentDescriptionChars+1) + "\"\n", match: func(err error) bool {
			return errors.Is(err, errSubagentDescriptionTooLong)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loadConfigTestFileError(t, tt.body, LoadOptions{})
			if err == nil {
				t.Fatal("expected metadata error")
			}
			if !tt.match(err) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSubagentRoleHasMeaningfulDiffComparesProviderReviewerAndTimeoutValues(t *testing.T) {
	base := Settings{
		Timeouts: Timeouts{ModelRequestSeconds: 100},
		ProviderCapabilities: ProviderCapabilitiesOverride{
			ProviderID:           "openai",
			SupportsResponsesAPI: true,
		},
		Reviewer: ReviewerSettings{
			Model:          "gpt-5.5",
			TimeoutSeconds: 60,
		},
	}

	same := SubagentRole{
		Settings: base,
		Sources: map[string]string{
			"timeouts.model_request_seconds":                        "file",
			"provider_capabilities.supports_responses_api":          "file",
			"reviewer.model":                                        "file",
			"reviewer.provider_capabilities.supports_responses_api": "file",
		},
	}
	if SubagentRoleHasMeaningfulDiff(base, same) {
		t.Fatal("expected equal provider/reviewer/timeout values to be no-op")
	}

	changedTimeout := same
	changedTimeout.Settings = base
	changedTimeout.Settings.Timeouts.ModelRequestSeconds = 200
	if !SubagentRoleHasMeaningfulDiff(base, changedTimeout) {
		t.Fatal("expected timeout change to be meaningful")
	}

	changedProvider := same
	changedProvider.Settings = base
	changedProvider.Settings.ProviderCapabilities.SupportsResponsesAPI = false
	if !SubagentRoleHasMeaningfulDiff(base, changedProvider) {
		t.Fatal("expected provider capability change to be meaningful")
	}

	changedReviewer := same
	changedReviewer.Settings = base
	changedReviewer.Settings.Reviewer.Model = "gpt-5.4-mini"
	if !SubagentRoleHasMeaningfulDiff(base, changedReviewer) {
		t.Fatal("expected reviewer change to be meaningful")
	}
}

func TestLookupSubagentRoleUsesConfiguredIdentity(t *testing.T) {
	settings := Settings{
		ThinkingLevel: "medium",
		Subagents: map[string]SubagentRole{
			"planner": {
				Settings: Settings{ThinkingLevel: "medium"},
				Sources:  map[string]string{"thinking_level": "file"},
			},
			"blocked": {
				AgentCallable:    false,
				AgentCallableSet: true,
				Sources:          map[string]string{"agent_callable": "file"},
			},
		},
	}

	tests := []struct {
		name           string
		rawSelector    string
		wantNormalized string
		wantSelector   bool
		wantStatus     SubagentRoleLookupStatus
	}{
		{name: "configured no-op role", rawSelector: " Planner ", wantNormalized: "planner", wantSelector: true, wantStatus: SubagentRoleLookupPresent},
		{name: "configured non-callable role", rawSelector: "blocked", wantNormalized: "blocked", wantSelector: true, wantStatus: SubagentRoleLookupPresent},
		{name: "built-in fast", rawSelector: BuiltInSubagentRoleFast, wantNormalized: BuiltInSubagentRoleFast, wantSelector: true, wantStatus: SubagentRoleLookupPresent},
		{name: "missing valid role", rawSelector: "missing", wantNormalized: "missing", wantSelector: true, wantStatus: SubagentRoleLookupMissing},
		{name: "reserved default", rawSelector: "default", wantStatus: SubagentRoleLookupInvalid},
		{name: "reserved none", rawSelector: "none", wantStatus: SubagentRoleLookupInvalid},
		{name: "reserved self", rawSelector: "self", wantStatus: SubagentRoleLookupInvalid},
		{name: "empty after trim", rawSelector: " \t ", wantStatus: SubagentRoleLookupInvalid},
		{name: "internal space", rawSelector: "plan ner", wantStatus: SubagentRoleLookupInvalid},
		{name: "slash", rawSelector: "plan/ner", wantStatus: SubagentRoleLookupInvalid},
		{name: "punctuation", rawSelector: "planner!", wantStatus: SubagentRoleLookupInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := LookupSubagentRole(settings, tt.rawSelector)
			if lookup.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", lookup.Status, tt.wantStatus)
			}
			if got := lookup.NormalizedSelector != nil; got != tt.wantSelector {
				t.Fatalf("normalized selector presence = %v, want %v", got, tt.wantSelector)
			}
			if tt.wantSelector && *lookup.NormalizedSelector != tt.wantNormalized {
				t.Fatalf("normalized selector = %q, want %q", *lookup.NormalizedSelector, tt.wantNormalized)
			}
		})
	}
}

func TestAvailableSubagentRoleNamesRemainsPresentationOnly(t *testing.T) {
	settings := Settings{
		ThinkingLevel: "medium",
		Subagents: map[string]SubagentRole{
			"planner": {
				Settings: Settings{ThinkingLevel: "medium"},
				Sources:  map[string]string{"thinking_level": "file"},
			},
			"callable": {
				Settings: Settings{Model: "gpt-5.4-mini"},
				Sources:  map[string]string{"model": "file"},
			},
			"blocked": {
				Settings:         Settings{Model: "gpt-5.4-mini"},
				Sources:          map[string]string{"model": "file"},
				AgentCallable:    false,
				AgentCallableSet: true,
			},
		},
	}

	all := strings.Join(AvailableSubagentRoleNames(settings, false), ",")
	if all != "fast,blocked,callable" {
		t.Fatalf("available presentation roles = %q, want no no-op role and all meaningful roles", all)
	}
	callable := strings.Join(AvailableSubagentRoleNames(settings, true), ",")
	if callable != "fast,callable" {
		t.Fatalf("callable presentation roles = %q, want no no-op or non-callable roles", callable)
	}
}

func TestAppendSystemPromptFileFromConfigResolvesConfigRelativePath(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), ConfigDirName, "config.toml")
	state := configRegistry.defaultState()

	if err := appendSystemPromptFileFromConfig(
		settingsFile{"system_prompt_file": "prompts/SYSTEM.md"},
		configPath,
		SystemPromptFileScopeWorkspaceConfig,
		&state,
	); err != nil {
		t.Fatalf("append system prompt file: %v", err)
	}

	want := filepath.Join(filepath.Dir(configPath), "prompts", "SYSTEM.md")
	if got := state.Settings.SystemPromptFiles; len(got) != 1 || got[0].Path != want || got[0].Scope != SystemPromptFileScopeWorkspaceConfig {
		t.Fatalf("system prompt files = %+v, want %q %s", got, want, SystemPromptFileScopeWorkspaceConfig)
	}
}

func TestParseSubagentRoleSystemPromptFileResolvesConfigRelativePath(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), ConfigDirName, "config.toml")

	role, err := parseSubagentRole(settingsFile{"system_prompt_file": "fast-system.md"}, configPath, "fast")
	if err != nil {
		t.Fatalf("parse subagent role: %v", err)
	}

	want := filepath.Join(filepath.Dir(configPath), "fast-system.md")
	if got := role.Settings.SystemPromptFiles; len(got) != 1 || got[0].Path != want || got[0].Scope != SystemPromptFileScopeSubagent {
		t.Fatalf("subagent system prompt files = %+v, want %q %s", got, want, SystemPromptFileScopeSubagent)
	}
	if role.Sources["system_prompt_file"] != "file" {
		t.Fatalf("system_prompt_file source = %q, want file", role.Sources["system_prompt_file"])
	}
}

func TestLoadSubagentRoleRejectsNestedSubagentsTable(t *testing.T) {
	contents := strings.Join([]string{
		"model = \"gpt-5.5\"",
		"",
		"[subagents.fast]",
		"thinking_level = \"low\"",
		"",
		"[subagents.fast.subagents.worker]",
		"thinking_level = \"high\"",
	}, "\n")

	err := loadConfigTestFileError(t, contents, LoadOptions{})
	if err == nil {
		t.Fatal("expected nested subagents table to fail")
	}
	if !unknownSettingsKeyReported(err, "subagents.fast.subagents") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSubagentRoleRejectsUnknownKeys(t *testing.T) {
	contents := strings.Join([]string{
		"model = \"gpt-5.5\"",
		"",
		"[subagents.fast]",
		"thinking_level = \"low\"",
		"unknown_toggle = true",
	}, "\n")

	err := loadConfigTestFileError(t, contents, LoadOptions{})
	if err == nil {
		t.Fatal("expected unknown subagent key to fail")
	}
	if !unknownSettingsKeyReported(err, "subagents.fast.unknown_toggle") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadResolvesWorktreeBaseDirRelativeToPersistenceRoot(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	configText := strings.Join([]string{
		"[worktrees]",
		"base_dir = \"managed/worktrees\"",
		"setup_script = \"scripts/setup-worktree.sh\"",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(configText), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(workspace, LoadOptions{ConfigRoot: root})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if got, want := cfg.PersistenceRoot, root; got != want {
		t.Fatalf("persistence root = %q, want %q", got, want)
	}
	if got, want := cfg.Settings.Worktrees.BaseDir, filepath.Join(cfg.PersistenceRoot, "managed", "worktrees"); got != want {
		t.Fatalf("worktrees.base_dir = %q, want %q", got, want)
	}
	if got := cfg.Settings.Worktrees.SetupScript; got != "scripts/setup-worktree.sh" {
		t.Fatalf("worktrees.setup_script = %q, want scripts/setup-worktree.sh", got)
	}
}

func TestLoadDerivesDefaultWorktreeBaseDirFromPersistenceRoot(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()

	cfg, err := Load(workspace, LoadOptions{ConfigRoot: root})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if got, want := cfg.PersistenceRoot, root; got != want {
		t.Fatalf("persistence root = %q, want %q", got, want)
	}
	if got, want := cfg.Settings.Worktrees.BaseDir, filepath.Join(cfg.PersistenceRoot, "worktrees"); got != want {
		t.Fatalf("worktrees.base_dir = %q, want %q", got, want)
	}
}

func TestLoadCreatesWorktreeBaseDir(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	configText := strings.Join([]string{
		"[worktrees]",
		"base_dir = \"managed/worktrees\"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(configText), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(workspace, LoadOptions{ConfigRoot: root})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	info, err := os.Stat(cfg.Settings.Worktrees.BaseDir)
	if err != nil {
		t.Fatalf("stat worktree base dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected worktree base dir, got mode %v", info.Mode())
	}
}

func TestLoadSubagentRoleRejectsInvalidValues(t *testing.T) {
	contents := strings.Join([]string{
		"model = \"gpt-5.5\"",
		"",
		"[subagents.fast]",
		"provider_override = \"bogus\"",
	}, "\n")

	err := loadConfigTestFileError(t, contents, LoadOptions{})
	if err == nil {
		t.Fatal("expected invalid subagent role values to fail")
	}
	if !errors.Is(err, errSubagentRole) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSubagentRoleRejectsModelContextWindowBelowMinimum(t *testing.T) {
	err := loadConfigTestFileError(t, strings.Join([]string{
		"[subagents.fast]",
		"model_context_window = 39999",
		"context_compaction_threshold_tokens = 30000",
	}, "\n"), LoadOptions{})
	if err == nil {
		t.Fatal("expected subagent model_context_window below minimum validation error")
	}
	if !errors.Is(err, errSubagentRole) {
		t.Fatalf("expected subagent role validation error, got %v", err)
	}
	if !errors.Is(err, errModelContextWindowBelowMinimum) {
		t.Fatalf("expected model context window minimum validation detail, got %v", err)
	}
}

func TestLoadSubagentRoleRejectsModelContextWindowZeroWithMinimumError(t *testing.T) {
	err := loadConfigTestFileError(t, strings.Join([]string{
		"[subagents.fast]",
		"model_context_window = 0",
	}, "\n"), LoadOptions{})
	if err == nil {
		t.Fatal("expected subagent model_context_window zero validation error")
	}
	if !errors.Is(err, errSubagentRole) {
		t.Fatalf("expected subagent role validation error, got %v", err)
	}
	if !errors.Is(err, errModelContextWindowBelowMinimum) {
		t.Fatalf("expected model context window minimum validation detail, got %v", err)
	}
}

func TestLoadSubagentRoleRejectsReviewerModelContextWindowBelowMinimum(t *testing.T) {
	err := loadConfigTestFileError(t, strings.Join([]string{
		"[subagents.fast.reviewer]",
		"model_context_window = 39999",
	}, "\n"), LoadOptions{})
	if err == nil {
		t.Fatal("expected subagent reviewer.model_context_window below minimum validation error")
	}
	if !errors.Is(err, errSubagentRole) {
		t.Fatalf("expected subagent role validation error, got %v", err)
	}
	if !errors.Is(err, errModelContextWindowBelowMinimum) {
		t.Fatalf("expected model context window minimum validation detail, got %v", err)
	}
}

func TestLoadSubagentRoleRejectsReviewerModelContextWindowExplicitZero(t *testing.T) {
	err := loadConfigTestFileError(t, strings.Join([]string{
		"[subagents.fast.reviewer]",
		"model_context_window = 0",
	}, "\n"), LoadOptions{})
	if err == nil {
		t.Fatal("expected subagent reviewer.model_context_window=0 validation error")
	}
	if !errors.Is(err, errSubagentRole) {
		t.Fatalf("expected subagent role validation error, got %v", err)
	}
	if !errors.Is(err, errModelContextWindowBelowMinimum) {
		t.Fatalf("expected model context window minimum validation detail, got %v", err)
	}
}

func TestLoadSubagentRoleAllowsReviewerAuthNoneToInheritParentBaseURL(t *testing.T) {
	contents := strings.Join([]string{
		"model = \"gpt-5.5\"",
		"openai_base_url = \"http://127.0.0.1:8080/v1\"",
		"",
		"[subagents.fast.reviewer]",
		"auth = \"none\"",
	}, "\n")

	_, _, cfg := loadConfigTestFileApp(t, contents, LoadOptions{})
	role := cfg.Settings.Subagents[BuiltInSubagentRoleFast]
	if role.Settings.Reviewer.Auth != "none" {
		t.Fatalf("expected subagent reviewer.auth=none, got %q", role.Settings.Reviewer.Auth)
	}
}

func TestLoadSubagentRoleAllowsReviewerAuthNoneWithExplicitFirstPartyBaseURL(t *testing.T) {
	contents := strings.Join([]string{
		"model = \"gpt-5.5\"",
		"",
		"[subagents.fast.reviewer]",
		"auth = \"none\"",
		"openai_base_url = \"https://api.openai.com/v1\"",
	}, "\n")

	_, _, cfg := loadConfigTestFileApp(t, contents, LoadOptions{})
	role := cfg.Settings.Subagents[BuiltInSubagentRoleFast]
	if role.Settings.Reviewer.Auth != "none" {
		t.Fatalf("expected subagent reviewer.auth=none, got %q", role.Settings.Reviewer.Auth)
	}
}

func TestLoadSubagentRoleRejectsPersistenceRoot(t *testing.T) {
	contents := strings.Join([]string{
		"model = \"gpt-5.5\"",
		"",
		"[subagents.fast]",
		"persistence_root = \"/tmp/custom\"",
	}, "\n")

	err := loadConfigTestFileError(t, contents, LoadOptions{})
	if err == nil {
		t.Fatal("expected persistence_root in subagent role to fail")
	}
	if !unknownSettingsKeyReported(err, "subagents.fast.persistence_root") {
		t.Fatalf("expected unknown persistence_root subagent key, got: %v", err)
	}
}

func TestSettingsTOMLRoundTripsCapabilityOverrides(t *testing.T) {
	settings := configRegistry.defaultState().Settings
	settings.ModelCapabilities.SupportsReasoningEffort = true
	settings.ProviderCapabilities = ProviderCapabilitiesOverride{
		ProviderID:                     "openai-compatible",
		SupportsResponsesAPI:           true,
		SupportsRequestInputTokenCount: true,
		SupportsPromptCacheKey:         true,
		SupportsServerSideContextEdit:  true,
	}
	toml := settingsTOMLWithRenderingOptions(settings, true, nil, nil)

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	raw, err := readSettingsFile(path)
	if err != nil {
		t.Fatalf("read settings file: %v", err)
	}
	state := configRegistry.defaultState()
	sources := configRegistry.defaultSourceMap()
	if err := configRegistry.applyFile(raw, path, &state, sources); err != nil {
		t.Fatalf("apply file: %v", err)
	}
	if !state.Settings.ModelCapabilities.SupportsReasoningEffort {
		t.Fatal("expected model capability override to round-trip")
	}
	if state.Settings.ProviderCapabilities.ProviderID != "openai-compatible" {
		t.Fatalf("expected provider_id to round-trip, got %q", state.Settings.ProviderCapabilities.ProviderID)
	}
	if !state.Settings.ProviderCapabilities.SupportsResponsesAPI {
		t.Fatal("expected supports_responses_api to round-trip")
	}
	if !state.Settings.ProviderCapabilities.SupportsRequestInputTokenCount {
		t.Fatal("expected supports_request_input_token_count to round-trip")
	}
	if !state.Settings.ProviderCapabilities.SupportsServerSideContextEdit {
		t.Fatal("expected supports_server_side_context_edit to round-trip")
	}
}

func TestWriteSettingsFileForOnboardingDoesNotOverwriteExistingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, ConfigDirName, "config.toml")
	writeConfigTestFile(t, configPath, "model = \"existing\"\n")
	_, err := WriteSettingsFileForOnboarding(configRegistry.defaultState().Settings)
	if !errors.Is(err, errSettingsFileAlreadyExists) {
		t.Fatalf("expected existing settings file error, got %v", err)
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read settings file: %v", err)
	}
	if string(contents) != "model = \"existing\"\n" {
		t.Fatalf("expected existing settings file contents to remain unchanged, got %q", string(contents))
	}
}

func TestValidateThemeAllowsAutoAndEmpty(t *testing.T) {
	for _, value := range []string{"", "auto", "light", "dark"} {
		if err := validateTheme(settingsState{Settings: Settings{Theme: value}}, nil); err != nil {
			t.Fatalf("validate theme %q: %v", value, err)
		}
	}
}

func TestLoadReviewerDefaultsInheritMainSettingsWhenUnset(t *testing.T) {
	home, workspace := newConfigTestEnv(t)

	configPath := filepath.Join(home, ConfigDirName, "config.toml")
	writeConfigTestFile(t, configPath, "model = \"gpt-main-file\"\nthinking_level = \"xhigh\"\nprovider_override = \"openai\"\nopenai_base_url = \"http://127.0.0.1:8080/v1\"\n[reviewer]\nfrequency = \"all\"\n")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Reviewer.Model != "gpt-main-file" {
		t.Fatalf("expected reviewer.model to inherit file main model, got %q", cfg.Settings.Reviewer.Model)
	}
	if cfg.Settings.Reviewer.ThinkingLevel != "xhigh" {
		t.Fatalf("expected reviewer.thinking_level to inherit file main thinking level, got %q", cfg.Settings.Reviewer.ThinkingLevel)
	}
	if cfg.Settings.Reviewer.ProviderOverride != "openai" {
		t.Fatalf("expected reviewer.provider_override to inherit file main provider override, got %q", cfg.Settings.Reviewer.ProviderOverride)
	}
	if cfg.Settings.Reviewer.OpenAIBaseURL != "http://127.0.0.1:8080/v1" {
		t.Fatalf("expected reviewer.openai_base_url to inherit file main base URL, got %q", cfg.Settings.Reviewer.OpenAIBaseURL)
	}

	t.Setenv("KENT_MODEL", "gpt-main-env")
	t.Setenv("KENT_THINKING_LEVEL", "medium")
	t.Setenv("KENT_OPENAI_BASE_URL", "http://localhost:9090/v1")
	t.Setenv("KENT_REVIEWER_MODEL", "")
	t.Setenv("KENT_REVIEWER_THINKING_LEVEL", "")
	t.Setenv("KENT_REVIEWER_PROVIDER_OVERRIDE", "")
	t.Setenv("KENT_REVIEWER_OPENAI_BASE_URL", "")
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Reviewer.Model != "gpt-main-env" {
		t.Fatalf("expected reviewer.model to inherit env main model, got %q", cfg.Settings.Reviewer.Model)
	}
	if cfg.Settings.Reviewer.ThinkingLevel != "medium" {
		t.Fatalf("expected reviewer.thinking_level to inherit env main thinking level, got %q", cfg.Settings.Reviewer.ThinkingLevel)
	}
	if cfg.Settings.Reviewer.OpenAIBaseURL != "http://localhost:9090/v1" {
		t.Fatalf("expected reviewer.openai_base_url to inherit env main base URL, got %q", cfg.Settings.Reviewer.OpenAIBaseURL)
	}
}

func TestLoadReviewerOpenAIProviderOverrideInheritsMainOpenAIBaseURL(t *testing.T) {
	home, workspace := newConfigTestEnv(t)

	configPath := filepath.Join(home, ConfigDirName, "config.toml")
	writeConfigTestFile(t, configPath, "openai_base_url = \"http://127.0.0.1:8080/v1\"\n[reviewer]\nprovider_override = \"openai\"\nmodel = \"local-reviewer\"\n")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Reviewer.ProviderOverride != "openai" {
		t.Fatalf("expected explicit reviewer.provider_override, got %q", cfg.Settings.Reviewer.ProviderOverride)
	}
	if cfg.Settings.Reviewer.OpenAIBaseURL != "http://127.0.0.1:8080/v1" {
		t.Fatalf("expected reviewer.openai_base_url to inherit main OpenAI base URL, got %q", cfg.Settings.Reviewer.OpenAIBaseURL)
	}
}

func TestPersistenceRootHashIsStableUniqueAndScopesSocket(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "kent-root-example")
	hash := PersistenceRootHash(root)
	if hash == "" {
		t.Fatal("expected non-empty hash for a non-empty root")
	}
	if PersistenceRootHash(root) != hash {
		t.Fatal("hash must be deterministic for the same root")
	}
	if PersistenceRootHash(root+string(filepath.Separator)) != hash {
		t.Fatal("hash must be stable across trailing-separator cleaning")
	}
	if PersistenceRootHash(filepath.Join(string(filepath.Separator), "tmp", "kent-root-other")) == hash {
		t.Fatal("different roots must hash differently")
	}
	if PersistenceRootHash("") != "" {
		t.Fatal("empty root must hash to empty")
	}
	// On platforms with a local RPC socket (unix), the socket directory is
	// scoped by the same hash so client and server agree on the instance.
	socketPath, ok, err := ServerLocalRPCSocketPath(App{PersistenceRoot: root})
	if err != nil {
		t.Fatalf("ServerLocalRPCSocketPath: %v", err)
	}
	if ok && !strings.Contains(socketPath, hash) {
		t.Fatalf("local socket path %q must be scoped by the root hash %q", socketPath, hash)
	}
}

func TestPersistenceRootHashMatchesDesktopGoldenValue(t *testing.T) {
	// Locks the wire contract shared with the Rust desktop client
	// (apps/desktop/src-tauri/src/lib.rs persistence_root_hash), which asserts the
	// same constant for the same already-canonical root. "/tmp/kent-root" is
	// already lowercase and clean, so the value holds on every platform.
	if got, want := PersistenceRootHash("/tmp/kent-root"), "eb013faf79dfc249"; got != want {
		t.Fatalf("PersistenceRootHash(/tmp/kent-root) = %q, want %q (desktop client must agree)", got, want)
	}
}

func TestPersistenceRootHashFoldsCaseOnCaseInsensitivePlatforms(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "Kent-Root-Case")
	upper := PersistenceRootHash(root)
	lower := PersistenceRootHash(strings.ToLower(root))
	switch runtime.GOOS {
	case "darwin", "windows":
		if upper != lower {
			t.Fatalf("case-insensitive platform must hash %q and %q identically", root, strings.ToLower(root))
		}
	default:
		if upper == lower {
			t.Fatalf("case-sensitive platform must hash %q and %q differently", root, strings.ToLower(root))
		}
	}
}

func TestExplicitPersistenceRootID(t *testing.T) {
	home := t.TempDir()
	isoRoot := filepath.Join(string(filepath.Separator), "tmp", "iso-root-id-explicit")

	t.Run("default source returns empty", func(t *testing.T) {
		t.Setenv("HOME", home)
		cfg := App{PersistenceRoot: isoRoot, Source: SourceReport{Sources: map[string]string{"persistence_root": "default"}}}
		if got := ExplicitPersistenceRootID(cfg); got != "" {
			t.Fatalf("default-source id = %q, want empty", got)
		}
	})
	t.Run("explicit default root returns empty", func(t *testing.T) {
		t.Setenv("HOME", home)
		cfg := App{PersistenceRoot: filepath.Join(home, ConfigDirName), Source: SourceReport{Sources: map[string]string{"persistence_root": "flag"}}}
		if got := ExplicitPersistenceRootID(cfg); got != "" {
			t.Fatalf("explicit-default id = %q, want empty", got)
		}
	})
	t.Run("explicit isolated root returns hash", func(t *testing.T) {
		t.Setenv("HOME", home)
		cfg := App{PersistenceRoot: isoRoot, Source: SourceReport{Sources: map[string]string{"persistence_root": "env"}}}
		if got, want := ExplicitPersistenceRootID(cfg), PersistenceRootHash(isoRoot); got != want {
			t.Fatalf("explicit-iso id = %q, want %q", got, want)
		}
	})
	t.Run("default comparison error pins explicit root", func(t *testing.T) {
		// HOME unset makes IsDefaultPersistenceRoot fail to resolve the default
		// root; the explicit root must stay pinned rather than disabling the check.
		t.Setenv("HOME", "")
		cfg := App{PersistenceRoot: isoRoot, Source: SourceReport{Sources: map[string]string{"persistence_root": "flag"}}}
		if got, want := ExplicitPersistenceRootID(cfg), PersistenceRootHash(isoRoot); got != want {
			t.Fatalf("error-case id = %q, want %q (must pin on default-resolution failure)", got, want)
		}
	})
}

func TestIsDefaultPersistenceRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	defaultRoot := filepath.Join(home, ConfigDirName)

	cases := []struct {
		name string
		root string
		want bool
	}{
		{name: "empty", root: "", want: true},
		{name: "absolute default", root: defaultRoot, want: true},
		{name: "trailing separator default", root: defaultRoot + string(filepath.Separator), want: true},
		{name: "tilde default", root: DefaultPersistence, want: true},
		{name: "non-default", root: filepath.Join(string(filepath.Separator), "tmp", "iso-root"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IsDefaultPersistenceRoot(tc.root)
			if err != nil {
				t.Fatalf("IsDefaultPersistenceRoot(%q): %v", tc.root, err)
			}
			if got != tc.want {
				t.Fatalf("IsDefaultPersistenceRoot(%q) = %v, want %v", tc.root, got, tc.want)
			}
		})
	}
}
