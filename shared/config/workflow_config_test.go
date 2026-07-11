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
	if cfg.Settings.Workflow.Subagents {
		t.Fatal("workflow subagents = true, want default false")
	}
}

func TestDefaultSettingsTOMLRendersWorkflowDefaults(t *testing.T) {
	rendered := settingsTOMLWithRenderingOptions(configRegistry.defaultState().Settings, true, nil, nil)
	if !strings.Contains(rendered, "[workflow]") {
		t.Fatalf("default TOML missing workflow section:\n%s", rendered)
	}
	for _, want := range []string{
		"completion_mode = \"auto\"",
		"concurrency = 5",
		"max_invalid_completion_attempts = 5",
		"subagents = false",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("default TOML missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "max_final_answer_violations") {
		t.Fatalf("default TOML should not render removed final-answer cap:\n%s", rendered)
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
subagents = true
`, LoadOptions{})
	if cfg.Settings.Workflow.CompletionMode != WorkflowCompletionModeShellCommand || cfg.Settings.Workflow.Concurrency != 7 || cfg.Settings.Workflow.MaxInvalidCompletionAttempts != 6 || !cfg.Settings.Workflow.Subagents {
		t.Fatalf("workflow settings = %+v", cfg.Settings.Workflow)
	}
	if got := cfg.Source.Sources["workflow.completion_mode"]; got != "file" {
		t.Fatalf("workflow.completion_mode source = %q, want file", got)
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
