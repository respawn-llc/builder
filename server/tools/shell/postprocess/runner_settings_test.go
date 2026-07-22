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

func TestNewRunnerWithAbsentHookSurfacesUnavailableWarning(t *testing.T) {
	runner, err := NewRunner(Settings{Mode: config.ShellPostprocessingModeUser})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	result, err := runner.Apply(context.Background(), Request{
		ToolName: toolspec.ToolExecCommand,
		Output:   "input",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Output != "input" || result.Warning == nil {
		t.Fatalf("absent hook result = %+v, want unchanged output with warning", result)
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
