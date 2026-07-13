package postprocess

import (
	"context"
	"testing"

	"core/shared/config"
	"core/shared/toolspec"
)

func TestNewRunnerRejectsInvalidModes(t *testing.T) {
	for _, mode := range []config.ShellPostprocessingMode{"", "invalid"} {
		if _, err := NewRunner(Settings{Mode: mode}); err == nil {
			t.Fatalf("expected mode %q to fail runner construction", mode)
		}
	}
}

func TestNewRunnerRejectsPresentBlankHook(t *testing.T) {
	for _, hook := range []string{"", " \t "} {
		if _, err := NewRunner(Settings{
			Mode:     config.ShellPostprocessingModeUser,
			HookPath: &hook,
		}); err == nil {
			t.Fatalf("expected present blank hook %q to fail runner construction", hook)
		}
	}
}

func TestNewRunnerPreservesTypedHookAbsence(t *testing.T) {
	runner, err := NewRunner(Settings{Mode: config.ShellPostprocessingModeUser})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if runner.hookPath != nil {
		t.Fatalf("compiled absent hook = %#v, want nil", runner.hookPath)
	}
}

func TestResolveHookPathRejectsPresentBlankValue(t *testing.T) {
	blank := " \t "
	if _, _, err := resolveHookPath(&blank); err == nil {
		t.Fatal("expected present blank compiled hook to fail resolution")
	}
}

func TestNewRunnerCopiesPresentHook(t *testing.T) {
	originalPath := writeHookScript(t, "#!/bin/sh\nprintf '{\"processed\":true,\"replaced_output\":\"ORIGINAL\"}'\n")
	hookPath := originalPath
	runner, err := NewRunner(Settings{
		Mode:     config.ShellPostprocessingModeUser,
		HookPath: &hookPath,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if runner.hookPath == nil || *runner.hookPath != originalPath {
		t.Fatalf("compiled hook = %#v, want copied path %q", runner.hookPath, originalPath)
	}
	hookPath = originalPath + ".missing"

	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: "printf input",
		Output:      "input",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Output != "ORIGINAL" {
		t.Fatalf("output = %q, want copied hook policy output", result.Output)
	}
}
