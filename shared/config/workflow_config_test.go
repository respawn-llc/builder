package config

import (
	"errors"
	"strings"
	"testing"
)

func TestLoadWorkflowConfigDefaults(t *testing.T) {
	_, workspace := newConfigTestEnv(t)

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Workflow.CompletionMode != WorkflowCompletionModeAuto {
		t.Fatalf("completion mode = %q, want auto", cfg.Settings.Workflow.CompletionMode)
	}
	if cfg.Settings.Workflow.Concurrency != 5 {
		t.Fatalf("concurrency = %d, want 5", cfg.Settings.Workflow.Concurrency)
	}
	if cfg.Settings.Workflow.MaxInvalidCompletionAttempts != 5 {
		t.Fatalf("max invalid completion attempts = %d, want 5", cfg.Settings.Workflow.MaxInvalidCompletionAttempts)
	}
	if !cfg.Settings.Workflow.UseRequiredToolCalls {
		t.Fatal("workflow required tool calls = false, want default true")
	}
	if cfg.Settings.Workflow.Subagents {
		t.Fatal("workflow subagents = true, want default false")
	}
}

func TestResolveWorkflowPreCompactionTokensDefaultsToSeventyPercentOfThreshold(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, `model_context_window = 100000
context_compaction_threshold_tokens = 90001
`, LoadOptions{})

	if cfg.Settings.Workflow.PreCompactionTokens != nil {
		t.Fatalf("pre-compaction tokens = %v, want authored absence", cfg.Settings.Workflow.PreCompactionTokens)
	}
	if got := ResolveWorkflowPreCompactionTokens(cfg.Settings); got != 63000 {
		t.Fatalf("resolved pre-compaction tokens = %d, want floor(90001*70%%) = 63000", got)
	}
}

func TestLoadWorkflowPreCompactionTokensAcceptsPositiveValueIncludingThreshold(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, `model_context_window = 100000
context_compaction_threshold_tokens = 90000

[workflow]
pre_compaction_tokens = 90000
`, LoadOptions{})

	if cfg.Settings.Workflow.PreCompactionTokens == nil || *cfg.Settings.Workflow.PreCompactionTokens != 90000 {
		t.Fatalf("pre-compaction tokens = %v, want 90000", cfg.Settings.Workflow.PreCompactionTokens)
	}
	if got := cfg.Source.Sources["workflow.pre_compaction_tokens"]; got != "file" {
		t.Fatalf("pre-compaction tokens source = %q, want file", got)
	}
	if got := ResolveWorkflowPreCompactionTokens(cfg.Settings); got != 90000 {
		t.Fatalf("resolved pre-compaction tokens = %d, want 90000", got)
	}
}

func TestLoadWorkflowPreCompactionTokensRejectsNonPositiveAndAboveThreshold(t *testing.T) {
	for name, payload := range map[string]string{
		"zero": `[workflow]
pre_compaction_tokens = 0
`,
		"negative": `[workflow]
pre_compaction_tokens = -1
`,
		"above threshold": `context_compaction_threshold_tokens = 90000
model_context_window = 100000

[workflow]
pre_compaction_tokens = 90001
`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := loadConfigTestFileError(t, payload, LoadOptions{}); err == nil {
				t.Fatal("expected invalid workflow pre-compaction threshold error")
			}
		})
	}
}

func TestWorkflowPreCompactionTokensIsRootOnlyAndHasNoEnvironmentSource(t *testing.T) {
	_, workspace := newConfigTestEnv(t)
	t.Setenv("KENT_WORKFLOW_PRE_COMPACTION_TOKENS", "12345")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Workflow.PreCompactionTokens != nil {
		t.Fatalf("environment configured pre-compaction tokens = %v, want authored absence", cfg.Settings.Workflow.PreCompactionTokens)
	}
	if got := cfg.Source.Sources["workflow.pre_compaction_tokens"]; got != "default" {
		t.Fatalf("pre-compaction tokens source = %q, want default", got)
	}

	err := loadConfigTestFileError(t, `[subagents.fast.workflow]
pre_compaction_tokens = 12345
`, LoadOptions{})
	if err == nil {
		t.Fatal("expected root-only workflow pre-compaction setting to be rejected in a role")
	}
	if !unknownSettingsKeyReported(err, "subagents.fast.workflow.pre_compaction_tokens") {
		t.Fatalf("Load error = %v, want root-only pre-compaction setting rejection", err)
	}
}

func TestDefaultSettingsTOMLRendersWorkflowDefaults(t *testing.T) {
	lines := configRegistry.defaultLines(configRegistry.defaultState())
	values := make(map[string]any, len(lines))
	for _, line := range lines {
		values[strings.Join(line.Path, ".")] = line.Value
	}
	for key, want := range map[string]any{
		"workflow.completion_mode":                 WorkflowCompletionModeAuto,
		"workflow.concurrency":                     5,
		"workflow.max_invalid_completion_attempts": 5,
		"workflow.use_required_tool_calls":         true,
		"workflow.subagents":                       false,
	} {
		if got, ok := values[key]; !ok || got != want {
			t.Fatalf("default registry value %q = %#v, want %#v", key, got, want)
		}
	}
	var defaultValue any
	for _, setting := range configRegistry.settings {
		keyed, ok := setting.(keyedRegistrySetting)
		if !ok || keyed.registryKey() != "workflow.pre_compaction_tokens" {
			continue
		}
		root, ok := setting.(rootOnlySetting[*int])
		if !ok {
			t.Fatalf("pre-compaction registry setting = %T, want root-only optional integer", setting)
		}
		defaultValue = root.defaultDocValue(configRegistry.defaultState())
		break
	}
	if defaultValue != 247380 {
		t.Fatalf("default registry pre-compaction threshold = %#v, want 247380", defaultValue)
	}
	if _, ok := values["workflow.max_final_answer_violations"]; ok {
		t.Fatal("default registry should not contain removed final-answer cap")
	}
}

func TestLoadWorkflowSubagentsIsTOMLOnly(t *testing.T) {
	_, workspace := newConfigTestEnv(t)
	t.Setenv("KENT_WORKFLOW_SUBAGENTS", "true")

	defaults := loadConfigTestApp(t, workspace, LoadOptions{})
	if defaults.Settings.Workflow.Subagents {
		t.Fatal("environment should not enable workflow subagents")
	}
	if got := defaults.Source.Sources["workflow.subagents"]; got != "default" {
		t.Fatalf("workflow.subagents source = %q, want default", got)
	}

	_, _, configured := loadConfigTestFileApp(t, "[workflow]\nsubagents = true\n", LoadOptions{})
	if !configured.Settings.Workflow.Subagents {
		t.Fatal("workflow subagents = false, want TOML true")
	}
	if got := configured.Source.Sources["workflow.subagents"]; got != "file" {
		t.Fatalf("workflow.subagents source = %q, want file", got)
	}
}

func TestLoadWorkflowConfigFromFile(t *testing.T) {
	_, _, cfg := loadConfigTestFileApp(t, `[workflow]
completion_mode = "shell_command"
concurrency = 7
max_invalid_completion_attempts = 6
use_required_tool_calls = false
subagents = true
`, LoadOptions{})
	if cfg.Settings.Workflow.CompletionMode != WorkflowCompletionModeShellCommand ||
		cfg.Settings.Workflow.Concurrency != 7 ||
		cfg.Settings.Workflow.MaxInvalidCompletionAttempts != 6 ||
		cfg.Settings.Workflow.UseRequiredToolCalls ||
		!cfg.Settings.Workflow.Subagents {
		t.Fatalf("workflow settings = %+v", cfg.Settings.Workflow)
	}
	if got := cfg.Source.Sources["workflow.completion_mode"]; got != "file" {
		t.Fatalf("workflow.completion_mode source = %q, want file", got)
	}
	if got := cfg.Source.Sources["workflow.use_required_tool_calls"]; got != "file" {
		t.Fatalf("workflow.use_required_tool_calls source = %q, want file", got)
	}
}

func TestLoadWorkflowConfigValidation(t *testing.T) {
	for name, payload := range map[string]string{
		"completion_mode":                 "[workflow]\ncompletion_mode = \"invalid\"\n",
		"concurrency":                     "[workflow]\nconcurrency = 0\n",
		"max_invalid_completion_attempts": "[workflow]\nmax_invalid_completion_attempts = 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := loadConfigTestFileError(t, payload, LoadOptions{}); !errors.Is(err, errInvalidWorkflowSettings) {
				t.Fatalf("Load error = %v, want workflow validation error", err)
			}
		})
	}
}

func TestLoadWorkflowConfigRejectsRemovedFinalAnswerCap(t *testing.T) {
	err := loadConfigTestFileError(t, "[workflow]\nmax_final_answer_violations = 3\n", LoadOptions{})
	if err == nil {
		t.Fatal("expected removed final-answer cap to be rejected")
	}
	if !unknownSettingsKeyReported(err, "workflow.max_final_answer_violations") {
		t.Fatalf("Load error = %v, want unknown workflow.max_final_answer_violations", err)
	}
}

func TestLoadSubagentRoleWorkflowConfigValidation(t *testing.T) {
	if err := loadConfigTestFileError(t, "[subagents.fast.workflow]\nconcurrency = 0\n", LoadOptions{}); !errors.Is(err, errWorkflowConcurrency) {
		t.Fatalf("Load error = %v, want subagent workflow validation error", err)
	}
}

func TestLoadSubagentRoleRejectsWorkflowSubagentsOverride(t *testing.T) {
	err := loadConfigTestFileError(t, "[subagents.fast.workflow]\nsubagents = true\n", LoadOptions{})
	if err == nil {
		t.Fatal("expected root-only workflow subagents setting to be rejected in a role")
	}
	if !unknownSettingsKeyReported(err, "subagents.fast.workflow.subagents") {
		t.Fatalf("Load error = %v, want unknown subagents.fast.workflow.subagents", err)
	}
}
