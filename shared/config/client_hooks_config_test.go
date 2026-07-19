package config

import (
	"errors"
	"reflect"
	"slices"
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
	lines := strings.Split(rendered, "\n")
	if !slices.Contains(lines, "[hooks.client]") ||
		!slices.Contains(lines, "# lifecycle = [\"executable\", \"fixed-arg\"]") {
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

func TestLoadInteractiveRejectsClientLifecycleHookInWorkspaceConfig(t *testing.T) {
	_, workspace := newConfigTestEnv(t)
	workspacePath := workspace + "/" + ConfigDirName + "/config.toml"
	writeConfigTestFile(t, workspacePath, "[hooks.client]\nlifecycle = [\"notify\"]\n")

	_, _, err := LoadInteractive(workspace, LoadOptions{})
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
