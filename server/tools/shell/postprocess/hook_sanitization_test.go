package postprocess

import (
	"context"
	"testing"

	"core/shared/config"
	"core/shared/toolspec"
)

func TestRunnerUserHookReceivesSanitizedCurrentAndRawOriginalOutput(t *testing.T) {
	hookPath := writeHookScript(t, `#!/bin/sh
payload=$(cat)
if printf '%s' "$payload" | grep -Fq '"current_output":"color"' && printf '%s' "$payload" | grep -Fq '"original_output":"\u001b[31mcolor\u001b[0m"'; then
  printf '{"processed":true,"replaced_output":"SANITIZED_CURRENT_RAW_ORIGINAL"}'
else
  printf '{"processed":true,"replaced_output":"UNEXPECTED_PAYLOAD"}'
fi
`)
	runner := mustNewRunner(t, Settings{Mode: config.ShellPostprocessingModeUser, HookPath: testStringPointer(hookPath)})
	result, err := runner.Apply(context.Background(), Request{
		ToolName:    toolspec.ToolExecCommand,
		CommandText: "printf color",
		Output:      "\x1b[31mcolor\x1b[0m",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Output != "SANITIZED_CURRENT_RAW_ORIGINAL" {
		t.Fatalf("output = %q, want SANITIZED_CURRENT_RAW_ORIGINAL", result.Output)
	}
}
