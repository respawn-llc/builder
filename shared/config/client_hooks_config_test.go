package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadInteractiveClientLifecycleHookIsAbsentByDefault(t *testing.T) {
	_, workspace := newConfigTestEnv(t)

	app, client, err := LoadInteractive(workspace, LoadOptions{})
	if err != nil {
		t.Fatalf("load interactive config: %v", err)
	}
	if command := client.Hooks.LifecycleCommand(); command != nil {
		t.Fatalf("lifecycle command = %#v, want absent", command)
	}
	if got := app.Source.Sources["hooks.client.lifecycle"]; got != "default" {
		t.Fatalf("lifecycle source = %q, want default", got)
	}
	rendered := settingsTOMLWithRenderingOptions(app.Settings, true, nil, nil)
	if !strings.Contains(rendered, "[hooks.client]\n# lifecycle = [\"executable\", \"fixed-arg\"]") {
		t.Fatalf("default settings TOML missing client lifecycle example:\n%s", rendered)
	}
}

func TestClientLifecycleHookCannotBeSetByEnvironmentOrRequestOverlay(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, "[hooks.client]\nlifecycle = [\"notify\", \"fixed\"]\n")
	t.Setenv("KENT_HOOKS_CLIENT_LIFECYCLE", "replacement")

	app, client, err := LoadInteractive(workspace, LoadOptions{Model: "cli-model"})
	if err != nil {
		t.Fatalf("load interactive config: %v", err)
	}
	want := []string{"notify", "fixed"}
	if got := client.Hooks.LifecycleCommand(); !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle command = %#v, want global file argv %#v", got, want)
	}
	if app.Settings.Model != "cli-model" {
		t.Fatalf("server CLI overlay was not applied: model = %q", app.Settings.Model)
	}
	overlaid, err := ApplyLoadOptionsToSnapshot(app, LoadOptions{Model: "later-cli-model"})
	if err != nil {
		t.Fatalf("apply request overlay: %v", err)
	}
	if overlaid.Settings.Model != "later-cli-model" {
		t.Fatalf("request overlay model = %q", overlaid.Settings.Model)
	}
	if got := client.Hooks.LifecycleCommand(); !reflect.DeepEqual(got, want) {
		t.Fatalf("server request overlay changed client argv: %#v", got)
	}
}

func TestLoadRejectsClientLifecycleHookFromSubagentRole(t *testing.T) {
	err := loadConfigTestFileError(t, "[subagents.worker.hooks.client]\nlifecycle = [\"notify\"]\n", LoadOptions{})
	if err == nil {
		t.Fatal("subagent lifecycle hook succeeded")
	}
	if !unknownSettingsKeyReported(err, "subagents.worker.hooks") {
		t.Fatalf("load error = %v, want typed unknown subagent hook key", err)
	}
}

func TestLoadInteractiveClientLifecycleHookFromGlobalFilePreservesAndCopiesArgv(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, "[hooks.client]\nlifecycle = [\"notify\", \"  fixed arg  \"]\n")

	app, client, err := LoadInteractive(workspace, LoadOptions{})
	if err != nil {
		t.Fatalf("load interactive config: %v", err)
	}
	want := []string{"notify", "  fixed arg  "}
	command := client.Hooks.LifecycleCommand()
	if !reflect.DeepEqual(command, want) {
		t.Fatalf("lifecycle command = %#v, want %#v", command, want)
	}
	command[0] = "mutated"
	if got := client.Hooks.LifecycleCommand(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mutating returned argv changed settings: %#v", got)
	}
	if got := app.Source.Sources["hooks.client.lifecycle"]; got != "file" {
		t.Fatalf("lifecycle source = %q, want file", got)
	}
}

func TestLoadInteractiveSharedSettingsFileIsGlobalOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		link func(string, string) error
	}{
		{"direct home path", nil},
		{"workspace symlink", os.Symlink},
		{"workspace hard link", os.Link},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, workspace, homeSettingsPath := newConfigTestFile(t)
			writeConfigTestFile(t, homeSettingsPath, "system_prompt_file = \"system.md\"\n\n[hooks.client]\nlifecycle = [\"notify\"]\n")
			workspaceRoot := workspace
			workspaceSettingsPath := filepath.Join(workspaceRoot, ConfigDirName, "config.toml")
			if tc.link == nil {
				workspaceRoot, workspaceSettingsPath = home, homeSettingsPath
			} else {
				ensureConfigTestDir(t, workspaceSettingsPath)
				if err := tc.link(homeSettingsPath, workspaceSettingsPath); err != nil {
					if errors.Is(err, errors.ErrUnsupported) {
						t.Skipf("settings link unsupported: %v", err)
					}
					t.Fatalf("link workspace settings: %v", err)
				}
			}

			app, client, err := LoadInteractive(workspaceRoot, LoadOptions{})
			if err != nil {
				t.Fatalf("load interactive config: %v", err)
			}
			if got := client.Hooks.LifecycleCommand(); !reflect.DeepEqual(got, []string{"notify"}) {
				t.Fatalf("lifecycle command = %#v, want global command", got)
			}
			if got := app.Source.Sources["hooks.client.lifecycle"]; got != "file" {
				t.Fatalf("lifecycle source = %q, want global file", got)
			}
			if got := app.Settings.SystemPromptFiles; !reflect.DeepEqual(got, []SystemPromptFile{{Path: filepath.Join(home, ConfigDirName, "system.md"), Scope: SystemPromptFileScopeHomeConfig}}) {
				t.Fatalf("system prompt files = %#v, want one global prompt", got)
			}
			if app.Source.HomeSettingsPath != homeSettingsPath || app.Source.WorkspaceSettingsPath != workspaceSettingsPath ||
				!app.Source.HomeSettingsFileExists || !app.Source.WorkspaceSettingsFileExists || app.Source.WorkspaceSettingsLayerEnabled ||
				app.Source.SettingsPath != homeSettingsPath {
				t.Fatalf("source report = %+v, want retained paths with global effective settings", app.Source)
			}
		})
	}
}

func TestLoadInteractiveRejectsClientLifecycleHookInWorkspaceConfig(t *testing.T) {
	_, workspace, homeSettingsPath := newConfigTestFile(t)
	writeConfigTestFile(t, homeSettingsPath, "model = \"global-model\"\n")
	workspacePath := workspace + "/" + ConfigDirName + "/config.toml"
	writeConfigTestFile(t, workspacePath, "[hooks.client]\nlifecycle = [\"notify\"]\n")
	homeInfo, err := os.Stat(homeSettingsPath)
	if err != nil {
		t.Fatalf("stat global settings: %v", err)
	}
	workspaceInfo, err := os.Stat(workspacePath)
	if err != nil {
		t.Fatalf("stat workspace settings: %v", err)
	}
	if os.SameFile(homeInfo, workspaceInfo) {
		t.Fatal("global and workspace settings files unexpectedly share one physical file")
	}

	_, _, err = LoadInteractive(workspace, LoadOptions{})
	if err == nil {
		t.Fatal("workspace lifecycle hook succeeded")
	}
	var layerErr *SettingsFileLayerError
	if !errors.As(err, &layerErr) {
		t.Fatalf("load error = %v, want typed file-layer error", err)
	}
	if layerErr.Key != "hooks.client.lifecycle" || layerErr.Layer != "workspace" {
		t.Fatalf("file-layer error = %+v, want lifecycle/workspace", layerErr)
	}
}

func TestLoadInteractiveRejectsInvalidClientLifecycleHookArgv(t *testing.T) {
	tests := map[string]string{
		"empty array":        "[hooks.client]\nlifecycle = []\n",
		"non-string element": "[hooks.client]\nlifecycle = [\"notify\", 7]\n",
		"blank executable":   "[hooks.client]\nlifecycle = [\"  \"]\n",
		"blank argument":     "[hooks.client]\nlifecycle = [\"notify\", \"\\t\"]\n",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			_, workspace, configPath := newConfigTestFile(t)
			writeConfigTestFile(t, configPath, contents)
			if _, _, err := LoadInteractive(workspace, LoadOptions{}); err == nil {
				t.Fatal("invalid lifecycle argv succeeded")
			}
		})
	}
}
