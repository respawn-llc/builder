package config

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMaxSubagentDepthDefaultsAndRenders(t *testing.T) {
	_, workspace := newConfigTestEnv(t)
	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.MaxSubagentDepth != 2 {
		t.Fatalf("max_subagent_depth = %d, want 2", cfg.Settings.MaxSubagentDepth)
	}
	if got := cfg.Source.Sources["max_subagent_depth"]; got != "default" {
		t.Fatalf("max_subagent_depth source = %q, want default", got)
	}
	rendered := settingsTOMLWithRenderingOptions(configRegistry.defaultState().Settings, true, nil, nil)
	if !strings.Contains(rendered, "max_subagent_depth = 2") {
		t.Fatalf("default settings TOML missing max_subagent_depth:\n%s", rendered)
	}
}

func TestLoadMaxSubagentDepthAcceptsClosedRange(t *testing.T) {
	for _, value := range []int{0, 1, 2, 30} {
		t.Run(strconv.Itoa(value), func(t *testing.T) {
			_, _, cfg := loadConfigTestFileApp(t, "max_subagent_depth = "+strconv.Itoa(value)+"\n", LoadOptions{})
			if cfg.Settings.MaxSubagentDepth != value {
				t.Fatalf("max_subagent_depth = %d, want %d", cfg.Settings.MaxSubagentDepth, value)
			}
		})
	}
}

func TestLoadMaxSubagentDepthRejectsOutsideClosedRange(t *testing.T) {
	for _, value := range []int{-1, 31} {
		err := loadConfigTestFileError(t, "max_subagent_depth = "+strconv.Itoa(value)+"\n", LoadOptions{})
		if err == nil {
			t.Fatalf("max_subagent_depth=%d succeeded", value)
		}
	}
}

func TestLoadMaxSubagentDepthUsesGlobalThenWorkspacePrecedence(t *testing.T) {
	home, workspace, globalPath := newConfigTestFile(t)
	writeConfigTestFile(t, globalPath, "max_subagent_depth = 1\n")
	workspacePath := filepath.Join(workspace, ConfigDirName, "config.toml")
	writeConfigTestFile(t, workspacePath, "max_subagent_depth = 3\n")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.MaxSubagentDepth != 3 {
		t.Fatalf("max_subagent_depth = %d, want workspace value 3 (home %s)", cfg.Settings.MaxSubagentDepth, home)
	}
	if got := cfg.Source.Sources["max_subagent_depth"]; got != "file" {
		t.Fatalf("max_subagent_depth source = %q, want file", got)
	}
}

func TestLoadMaxSubagentDepthIsTOMLOnly(t *testing.T) {
	_, workspace := newConfigTestEnv(t)
	t.Setenv("KENT_MAX_SUBAGENT_DEPTH", "9")
	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.MaxSubagentDepth != 2 {
		t.Fatalf("environment changed max_subagent_depth to %d", cfg.Settings.MaxSubagentDepth)
	}
}

func TestLoadSubagentRoleRejectsMaxSubagentDepth(t *testing.T) {
	err := loadConfigTestFileError(t, "[subagents.worker]\nmax_subagent_depth = 1\n", LoadOptions{})
	if err == nil {
		t.Fatal("subagent role max_subagent_depth succeeded")
	}
	if !unknownSettingsKeyReported(err, "subagents.worker.max_subagent_depth") {
		t.Fatalf("Load error = %v, want unknown subagents.worker.max_subagent_depth", err)
	}
}
