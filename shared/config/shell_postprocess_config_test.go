package config

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestShellPostprocessHookUsesTypedAbsenceOnLoadAndWire(t *testing.T) {
	_, workspace := newConfigTestEnv(t)
	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Shell.PostprocessHook != nil {
		t.Fatalf("omitted shell.postprocess_hook = %q, want nil", *cfg.Settings.Shell.PostprocessHook)
	}

	encoded, err := json.Marshal(cfg.Settings.Shell)
	if err != nil {
		t.Fatalf("marshal shell settings: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decode shell settings JSON: %v", err)
	}
	if hook := wire["PostprocessHook"]; hook != nil {
		t.Fatalf("PostprocessHook JSON = %#v, want null", hook)
	}
}

func TestLoadShellPostprocessHookPreservesPresentFileAndEnvironmentValues(t *testing.T) {
	_, workspace, configPath := newConfigTestFile(t)
	writeConfigTestFile(t, configPath, "[shell]\npostprocess_hook = \"/tmp/file-hook\"\n")

	cfg := loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Shell.PostprocessHook == nil || *cfg.Settings.Shell.PostprocessHook != "/tmp/file-hook" {
		t.Fatalf("file shell.postprocess_hook = %#v, want /tmp/file-hook", cfg.Settings.Shell.PostprocessHook)
	}

	t.Setenv("KENT_SHELL_POSTPROCESS_HOOK", "/tmp/env-hook")
	cfg = loadConfigTestApp(t, workspace, LoadOptions{})
	if cfg.Settings.Shell.PostprocessHook == nil || *cfg.Settings.Shell.PostprocessHook != "/tmp/env-hook" {
		t.Fatalf("environment shell.postprocess_hook = %#v, want /tmp/env-hook", cfg.Settings.Shell.PostprocessHook)
	}
}

func TestLoadRejectsPresentBlankShellPostprocessHook(t *testing.T) {
	for _, contents := range []string{
		"[shell]\npostprocess_hook = \"\"\n",
		"[shell]\npostprocess_hook = \" \\t \"\n",
	} {
		if err := loadConfigTestFileError(t, contents, LoadOptions{}); err == nil {
			t.Fatalf("expected explicitly blank TOML shell.postprocess_hook to fail: %q", contents)
		}
	}

	for _, value := range []string{"", " \t "} {
		_, workspace := newConfigTestEnv(t)
		t.Setenv("KENT_SHELL_POSTPROCESS_HOOK", value)
		if _, err := Load(workspace, LoadOptions{}); err == nil {
			t.Fatalf("expected explicitly blank KENT_SHELL_POSTPROCESS_HOOK to fail: %q", value)
		}
	}
}

func TestLoadRejectsPresentBlankShellPostprocessingMode(t *testing.T) {
	for _, contents := range []string{
		"[shell]\npostprocessing_mode = \"\"\n",
		"[shell]\npostprocessing_mode = \" \\t \"\n",
	} {
		if err := loadConfigTestFileError(t, contents, LoadOptions{}); err == nil {
			t.Fatalf("expected explicitly blank TOML shell.postprocessing_mode to fail: %q", contents)
		}
	}

	for _, value := range []string{"", " \t "} {
		_, workspace := newConfigTestEnv(t)
		t.Setenv("KENT_SHELL_POSTPROCESSING_MODE", value)
		if _, err := Load(workspace, LoadOptions{}); err == nil {
			t.Fatalf("expected explicitly blank KENT_SHELL_POSTPROCESSING_MODE to fail: %q", value)
		}
	}
}

func TestDefaultSettingsOmitAbsentShellPostprocessHook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfigTestFile(t, path, settingsTOMLWithRenderingOptions(configRegistry.defaultState().Settings, true, nil, nil))
	raw, err := readSettingsFile(path)
	if err != nil {
		t.Fatalf("read rendered default settings: %v", err)
	}
	if value, present, err := lookupFileValue(raw, []string{"shell", "postprocess_hook"}); err != nil {
		t.Fatalf("lookup shell.postprocess_hook: %v", err)
	} else if present {
		t.Fatalf("rendered default shell.postprocess_hook = %#v, want omitted", value)
	}
}
