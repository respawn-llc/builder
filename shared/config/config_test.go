package config

import (
	"core/internal/testharness/testenv"
	"core/shared/toolspec"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	testenv.ClearAtProcessStart(PersistenceRootEnvName)
	os.Exit(m.Run())
}

func TestConfigProcessClearsPersistenceRoot(t *testing.T) {
	testenv.AssertUnsetAtProcessStart(
		t,
		"TestConfigProcessClearsPersistenceRoot",
		PersistenceRootEnvName,
	)
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
	if cfg.Settings.ProviderIdentifier != Command {
		t.Fatalf("default provider identifier mismatch: %q", cfg.Settings.ProviderIdentifier)
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
	if cfg.Settings.Shell.PostprocessHook != nil {
		t.Fatalf("default shell.postprocess_hook mismatch: %#v", cfg.Settings.Shell.PostprocessHook)
	}
	if got := cfg.Settings.Worktrees.BaseDir; got != filepath.Join(cfg.PersistenceRoot, "worktrees") {
		t.Fatalf("default worktrees.base_dir mismatch: %q", got)
	}
	if cfg.Settings.Worktrees.SetupScript != "" {
		t.Fatalf("expected default worktrees.setup_script empty, got %q", cfg.Settings.Worktrees.SetupScript)
	}
	if cfg.Settings.Worktrees.SetupTimeoutSeconds != defaultWorktreeSetupTimeoutSeconds {
		t.Fatalf("default worktrees.setup_timeout_seconds mismatch: %d", cfg.Settings.Worktrees.SetupTimeoutSeconds)
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

func TestFindNearestWorkspaceSettingsRoot(t *testing.T) {
	workspace := t.TempDir()
	settingsDir := filepath.Join(workspace, ConfigDirName)
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "config.toml"), nil, 0o644); err != nil {
		t.Fatalf("WriteFile settings: %v", err)
	}
	nested := filepath.Join(workspace, "pkg", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll nested: %v", err)
	}

	root, err := FindNearestWorkspaceSettingsRoot(nested)
	if err != nil {
		t.Fatalf("FindNearestWorkspaceSettingsRoot: %v", err)
	}
	if root == nil || *root != workspace {
		t.Fatalf("settings root = %v, want %q", root, workspace)
	}
}

func TestFindNearestWorkspaceSettingsRootDoesNotTreatHomeSettingsAsWorkspaceSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsDir := filepath.Join(home, ConfigDirName)
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "config.toml"), nil, 0o644); err != nil {
		t.Fatalf("WriteFile settings: %v", err)
	}
	nested := filepath.Join(home, "projects", "demo")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll nested: %v", err)
	}

	root, err := FindNearestWorkspaceSettingsRoot(nested)
	if err != nil {
		t.Fatalf("FindNearestWorkspaceSettingsRoot: %v", err)
	}
	if root != nil {
		t.Fatalf("settings root = %q, want absent", *root)
	}
}

func TestFindNearestWorkspaceSettingsRootExcludesExplicitPersistenceSettings(t *testing.T) {
	workspace := t.TempDir()
	persistenceRoot := filepath.Join(workspace, ConfigDirName)
	t.Setenv(PersistenceRootEnvName, persistenceRoot)
	if err := os.MkdirAll(persistenceRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll persistence root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(persistenceRoot, "config.toml"), nil, 0o644); err != nil {
		t.Fatalf("WriteFile settings: %v", err)
	}
	nested := filepath.Join(workspace, "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll nested: %v", err)
	}

	root, err := FindNearestWorkspaceSettingsRoot(nested)
	if err != nil {
		t.Fatalf("FindNearestWorkspaceSettingsRoot: %v", err)
	}
	if root != nil {
		t.Fatalf("settings root = %q, want absent", *root)
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

	thinkingLevel := "low"
	cfg, err := Load(workspace, LoadOptions{ThinkingLevel: &thinkingLevel})
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

func TestLoadSubagentRoleFromFile(t *testing.T) {
	home, workspace, configPath := newConfigTestFile(t)
	contents := strings.Join([]string{
		"model = \"gpt-5.6-sol\"",
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

func TestLoadSubagentRoleSkillNamedEnabledToggle(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, strings.Join([]string{
		"[skills]",
		"enabled = false",
		"apiresult = false",
		"",
		"[subagents.inherits]",
		"thinking_level = \"high\"",
		"",
		"[subagents.reenabled.skills]",
		"enabled = true",
		"apiresult = true",
		"",
		"[subagents.disabled.skills]",
		"enabled = false",
	}, "\n"), LoadOptions{})

	inherited := cfg.Settings.Subagents["inherits"]
	if _, exists := inherited.Sources["skills.enabled"]; exists {
		t.Fatalf("omitted role toggle must not have an explicit source: %+v", inherited.Sources)
	}

	reenabled := cfg.Settings.Subagents["reenabled"]
	if !ResolveSkillPolicy(reenabled.Settings).SkillEnabled("enabled") {
		t.Fatal("explicit role skills.enabled=true must enable the skill named enabled")
	}
	if got := reenabled.Sources["skills.enabled"]; got != "file" {
		t.Fatalf("expected role skills.enabled source file, got %q", got)
	}
	if enabled, exists := reenabled.Settings.SkillToggles["enabled"]; !exists || !enabled {
		t.Fatalf("role enabled key must be an ordinary enabled toggle: %+v", reenabled.Settings.SkillToggles)
	}

	disabled := cfg.Settings.Subagents["disabled"]
	if ResolveSkillPolicy(disabled.Settings).SkillEnabled("enabled") {
		t.Fatal("explicit role skills.enabled=false must disable the skill named enabled")
	}
}

func TestLoadSubagentRoleRejectsNonBooleanSkillNamedEnabledWithScopedPath(t *testing.T) {
	err := loadConfigTestFileError(t, "[subagents.worker.skills]\nenabled = \"off\"\n", LoadOptions{})
	if err == nil {
		t.Fatal("expected invalid role skills.enabled type")
	}
	if !errors.Is(err, errSubagentRole) {
		t.Fatalf("expected subagent role wrapper, got %v", err)
	}
	var typeErr *SettingsKeyTypeError
	if !errors.As(err, &typeErr) || typeErr.Key != "subagents.worker.skills.enabled" {
		t.Fatalf("expected scoped subagents.worker.skills.enabled type error, got %v", err)
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

func TestLoadSubagentRoleWorkflowSubagentMetadata(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    bool
		wantSet bool
	}{
		{name: "omitted defaults enabled", body: "[subagents.worker]\nmodel = \"gpt-5.4-mini\"\n", want: true},
		{name: "explicit enabled", body: "[subagents.worker]\nworkflow_subagent = true\nmodel = \"gpt-5.4-mini\"\n", want: true, wantSet: true},
		{name: "explicit disabled", body: "[subagents.worker]\nworkflow_subagent = false\nmodel = \"gpt-5.4-mini\"\n", wantSet: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, cfg := loadConfigTestFileApp(t, tt.body, LoadOptions{})
			lookup := LookupSubagentRole(cfg.Settings, " Worker ")
			if lookup.Status != SubagentRoleLookupPresent {
				t.Fatalf("role lookup = %q, want present", lookup.Status)
			}
			if lookup.Role.WorkflowSubagent != tt.want || lookup.Role.WorkflowSubagentSet != tt.wantSet {
				t.Fatalf("workflow_subagent metadata = (%t, %t), want (%t, %t)", lookup.Role.WorkflowSubagent, lookup.Role.WorkflowSubagentSet, tt.want, tt.wantSet)
			}
			if _, exists := lookup.Role.Sources["workflow_subagent"]; exists {
				t.Fatalf("workflow_subagent should not be runtime source, got %+v", lookup.Role.Sources)
			}
		})
	}
}

type configRejectionExpectation interface {
	assert(t *testing.T, err error)
}

type configErrorChainExpectation []error

func (expected configErrorChainExpectation) assert(t *testing.T, err error) {
	t.Helper()
	for _, target := range expected {
		if !errors.Is(err, target) {
			t.Fatalf("error = %v, want errors.Is target %v", err, target)
		}
	}
}

type unknownConfigKeyExpectation string

func (expected unknownConfigKeyExpectation) assert(t *testing.T, err error) {
	t.Helper()
	if !unknownSettingsKeyReported(err, string(expected)) {
		t.Fatalf("error = %v, want unknown settings key %q", err, expected)
	}
}

type configKeyTypeExpectation string

func (expected configKeyTypeExpectation) assert(t *testing.T, err error) {
	t.Helper()
	var typeErr *SettingsKeyTypeError
	if !errors.As(err, &typeErr) || typeErr.ExpectedType != string(expected) {
		t.Fatalf("error = %v, want settings key type %q", err, expected)
	}
}

func TestLoadSubagentRoleRejections(t *testing.T) {
	tests := []struct {
		name string
		body string
		want configRejectionExpectation
	}{
		{name: "reserved name/default", body: "[subagents.default]\nmodel = \"gpt-5.6-sol\"\n", want: configErrorChainExpectation{errInvalidSubagentKey}},
		{name: "reserved name/none", body: "[subagents.none]\nmodel = \"gpt-5.6-sol\"\n", want: configErrorChainExpectation{errInvalidSubagentKey}},
		{name: "reserved name/self", body: "[subagents.self]\nmodel = \"gpt-5.6-sol\"\n", want: configErrorChainExpectation{errInvalidSubagentKey}},
		{name: "invalid metadata/description type", body: "[subagents.worker]\ndescription = 123\n", want: configKeyTypeExpectation("string")},
		{name: "invalid metadata/agent callable type", body: "[subagents.worker]\nagent_callable = \"no\"\n", want: configKeyTypeExpectation("boolean")},
		{name: "invalid metadata/workflow subagent type", body: "[subagents.worker]\nworkflow_subagent = \"no\"\n", want: configKeyTypeExpectation("boolean")},
		{name: "invalid metadata/description length", body: "[subagents.worker]\ndescription = \"" + strings.Repeat("x", MaxSubagentDescriptionChars+1) + "\"\n", want: configErrorChainExpectation{errSubagentDescriptionTooLong}},
		{name: "nested subagents table", body: strings.Join([]string{
			"model = \"gpt-5.6-sol\"",
			"",
			"[subagents.fast]",
			"thinking_level = \"low\"",
			"",
			"[subagents.fast.subagents.worker]",
			"thinking_level = \"high\"",
		}, "\n"), want: unknownConfigKeyExpectation("subagents.fast.subagents")},
		{name: "unknown key/unknown toggle", body: strings.Join([]string{
			"model = \"gpt-5.6-sol\"",
			"",
			"[subagents.fast]",
			"thinking_level = \"low\"",
			"unknown_toggle = true",
		}, "\n"), want: unknownConfigKeyExpectation("subagents.fast.unknown_toggle")},
		{name: "unknown key/workflow subagent typo", body: strings.Join([]string{
			"model = \"gpt-5.6-sol\"",
			"",
			"[subagents.fast]",
			"thinking_level = \"low\"",
			"workflow_subagent_typo = true",
		}, "\n"), want: unknownConfigKeyExpectation("subagents.fast.workflow_subagent_typo")},
		{name: "invalid values", body: strings.Join([]string{
			"model = \"gpt-5.6-sol\"",
			"",
			"[subagents.fast]",
			"provider_override = \"bogus\"",
		}, "\n"), want: configErrorChainExpectation{errSubagentRole}},
		{name: "model context window below minimum", body: strings.Join([]string{
			"[subagents.fast]",
			"model_context_window = 39999",
			"context_compaction_threshold_tokens = 30000",
		}, "\n"), want: configErrorChainExpectation{errSubagentRole, errModelContextWindowBelowMinimum}},
		{name: "reviewer model context window below minimum", body: strings.Join([]string{
			"[subagents.fast.reviewer]",
			"model_context_window = 39999",
		}, "\n"), want: configErrorChainExpectation{errSubagentRole, errModelContextWindowBelowMinimum}},
		{name: "persistence root", body: strings.Join([]string{
			"model = \"gpt-5.6-sol\"",
			"",
			"[subagents.fast]",
			"persistence_root = \"/tmp/custom\"",
		}, "\n"), want: unknownConfigKeyExpectation("subagents.fast.persistence_root")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loadConfigTestFileError(t, tt.body, LoadOptions{})
			if err == nil {
				t.Fatal("expected subagent role config rejection")
			}
			tt.want.assert(t, err)
		})
	}
}

func TestOverlaySubagentRoleSettingsDoesNotApplyProcessSettings(t *testing.T) {
	base := Settings{
		Worktrees:    WorktreeSettings{BaseDir: "/base", SetupScript: "base.sh", SetupTimeoutSeconds: 10},
		Workflow:     WorkflowSettings{CompletionMode: "structured_output", Concurrency: 2, MaxInvalidCompletionAttempts: 3, Subagents: true},
		PreventSleep: "active",
	}
	role := SubagentRole{
		Settings: Settings{
			Worktrees:    WorktreeSettings{BaseDir: "/role", SetupScript: "role.sh", SetupTimeoutSeconds: 20},
			Workflow:     WorkflowSettings{CompletionMode: "tool", Concurrency: 4, MaxInvalidCompletionAttempts: 5},
			PreventSleep: "never",
		},
		Sources: map[string]string{
			"worktrees.base_dir":                       "file",
			"worktrees.setup_script":                   "file",
			"worktrees.setup_timeout_seconds":          "file",
			"workflow.completion_mode":                 "file",
			"workflow.concurrency":                     "file",
			"workflow.max_invalid_completion_attempts": "file",
			"prevent_sleep":                            "file",
		},
	}

	got := OverlaySubagentRoleSettings(base, role, true)
	if got.Worktrees != base.Worktrees || got.Workflow != base.Workflow || got.PreventSleep != base.PreventSleep {
		t.Fatalf("process settings changed: got worktrees=%+v workflow=%+v prevent_sleep=%q", got.Worktrees, got.Workflow, got.PreventSleep)
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

func TestSkillOnlyRoleAffectsCatalogWithoutChangingCallability(t *testing.T) {
	settings := Settings{
		SkillToggles: map[string]bool{"enabled": true},
		Subagents: map[string]SubagentRole{
			"worker": {
				Settings: Settings{SkillToggles: map[string]bool{"enabled": false}},
				Sources:  map[string]string{"skills.enabled": "file"},
			},
			"blocked": {
				Settings:         Settings{SkillToggles: map[string]bool{"enabled": false}},
				Sources:          map[string]string{"skills.enabled": "file"},
				AgentCallableSet: true,
			},
			"visible": {
				Settings: Settings{
					ThinkingLevel: "high",
					SkillToggles:  map[string]bool{"enabled": true},
				},
				Sources: map[string]string{
					"thinking_level": "file",
					"skills.enabled": "file",
				},
			},
		},
	}

	if lookup := LookupSubagentRole(settings, "worker"); lookup.Status != SubagentRoleLookupPresent {
		t.Fatalf("worker lookup = %q, want present", lookup.Status)
	}
	if !SubagentRoleCallable(LookupSubagentRole(settings, "worker").Role) {
		t.Fatal("skill policy must not block directly callable role")
	}
	if SubagentRoleCallable(LookupSubagentRole(settings, "blocked").Role) {
		t.Fatal("callability metadata must remain authoritative")
	}
	if got := strings.Join(AvailableSubagentRoleNames(settings, false), ","); got != "fast,blocked,visible,worker" {
		t.Fatalf("available roles = %q, want all meaningful roles", got)
	}
	if got := strings.Join(AvailableSubagentRoleNames(settings, true), ","); got != "fast,visible,worker" {
		t.Fatalf("callable roles = %q, want skill-only role and independent visible role", got)
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

func TestLoadWorktreeSetupTimeoutSeconds(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{name: "default", content: "", want: defaultWorktreeSetupTimeoutSeconds},
		{name: "positive", content: "[worktrees]\nsetup_timeout_seconds = 120\n", want: 120},
		{name: "zero", content: "[worktrees]\nsetup_timeout_seconds = 0\n", want: 0},
		{name: "negative", content: "[worktrees]\nsetup_timeout_seconds = -1\n", want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			workspace := t.TempDir()
			if strings.TrimSpace(tt.content) != "" {
				if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(tt.content), 0o644); err != nil {
					t.Fatalf("write config: %v", err)
				}
			}
			cfg, err := Load(workspace, LoadOptions{ConfigRoot: root})
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := cfg.Settings.Worktrees.SetupTimeoutSeconds; got != tt.want {
				t.Fatalf("worktrees.setup_timeout_seconds = %d, want %d", got, tt.want)
			}
		})
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

func TestSettingsTOMLRoundTripsCapabilityOverrides(t *testing.T) {
	settings := configRegistry.defaultState().Settings
	settings.ModelCapabilities.SupportsReasoningEffort = true
	settings.ProviderCapabilities = ProviderCapabilitiesOverride{
		ProviderID:                    "openai-compatible",
		SupportsResponsesAPI:          true,
		SupportsPromptCacheKey:        true,
		SupportsServerSideContextEdit: true,
		SupportsProviderVerbosity:     true,
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
	if err := configRegistry.applyFile(raw, path, settingsFileLayerGlobal, &state, sources); err != nil {
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
	if !state.Settings.ProviderCapabilities.SupportsServerSideContextEdit {
		t.Fatal("expected supports_server_side_context_edit to round-trip")
	}
	if !state.Settings.ProviderCapabilities.SupportsProviderVerbosity {
		t.Fatal("expected supports_provider_verbosity to round-trip")
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

func TestCanonicalPathIdentityUsesPersistenceRootCasePolicy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "KentRoot", "Nested")
	withTrailing := root + string(filepath.Separator)
	first, err := CanonicalPathIdentity(root)
	if err != nil {
		t.Fatalf("CanonicalPathIdentity: %v", err)
	}
	second, err := CanonicalPathIdentity(withTrailing)
	if err != nil {
		t.Fatalf("CanonicalPathIdentity trailing: %v", err)
	}
	if first != second {
		t.Fatalf("path identity should ignore trailing separator: %q != %q", first, second)
	}
	caseVariant, err := CanonicalPathIdentity(strings.ToLower(root))
	if err != nil {
		t.Fatalf("CanonicalPathIdentity case variant: %v", err)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if first != caseVariant {
			t.Fatalf("path identity should fold case on %s: %q != %q", runtime.GOOS, first, caseVariant)
		}
	} else if first == caseVariant && root != strings.ToLower(root) {
		t.Fatalf("path identity should preserve case on %s", runtime.GOOS)
	}
}

func TestCanonicalPathIdentityPreservesSignificantFilenameSpaces(t *testing.T) {
	root := t.TempDir()
	withSpace := filepath.Join(root, "notes ")
	withoutSpace := filepath.Join(root, "notes")

	spaceIdentity, err := CanonicalPathIdentity(withSpace)
	if err != nil {
		t.Fatalf("CanonicalPathIdentity with trailing filename space: %v", err)
	}
	plainIdentity, err := CanonicalPathIdentity(withoutSpace)
	if err != nil {
		t.Fatalf("CanonicalPathIdentity without trailing filename space: %v", err)
	}
	if spaceIdentity == plainIdentity {
		t.Fatalf("path identity collapsed significant filename space: %q", spaceIdentity)
	}
}

func TestCanonicalLexicalPathIdentityDoesNotFollowSymlinkAncestors(t *testing.T) {
	realRoot := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "link-root")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	lexical, err := CanonicalLexicalPathIdentity(filepath.Join(linkRoot, "child.txt"))
	if err != nil {
		t.Fatalf("CanonicalLexicalPathIdentity: %v", err)
	}
	real, err := CanonicalPathIdentity(filepath.Join(linkRoot, "child.txt"))
	if err != nil {
		t.Fatalf("CanonicalPathIdentity: %v", err)
	}
	if lexical == real {
		t.Fatalf("lexical identity followed symlink ancestor: lexical=%q real=%q", lexical, real)
	}
}

func TestResolveExistingAncestorRealPathUsesNearestExistingRealAncestor(t *testing.T) {
	parentReal := t.TempDir()
	linkParent := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(parentReal, linkParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	got, err := ResolveExistingAncestorRealPath(filepath.Join(linkParent, "missing", "child.txt"))
	if err != nil {
		t.Fatalf("ResolveExistingAncestorRealPath: %v", err)
	}
	parentRealCanonical, err := filepath.EvalSymlinks(parentReal)
	if err != nil {
		t.Fatalf("resolve temp real path: %v", err)
	}
	want := filepath.Join(parentRealCanonical, "missing", "child.txt")
	if got != want {
		t.Fatalf("resolved path = %q, want %q", got, want)
	}

	loop := filepath.Join(parentReal, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Skipf("symlink loop unavailable: %v", err)
	}
	if _, err := ResolveExistingAncestorRealPath(loop); err == nil {
		t.Fatal("expected symlink loop error")
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
