package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestExecCommandGuardRetainsStructuredOutputPath(t *testing.T) {
	manager := newBackgroundTestManager(t)
	execTool := NewExecCommandTool(t.TempDir(), 16_000, 20, manager, "")
	const output = "123456789012345678901234567890123456789012345678"
	result := callExecCommand(t, execTool, "oversized-output", map[string]any{
		"cmd": "printf '" + output + "'; exit 7", "shell": "/bin/sh", "login": false,
		"raw": true, "yield_time_ms": 1_000, "max_output_tokens": 11,
	})
	if !result.IsError || result.OutputPath == nil {
		t.Fatalf("guarded result = %#v, want error and structured output path", result)
	}
	want := fmt.Sprintf(oversizedOutputMessageTemplate, *result.OutputPath)
	wantBody, _ := json.Marshal(map[string]string{"error": want})
	if string(result.Output) != string(wantBody) {
		t.Fatalf("failure = %s, want %s", string(result.Output), string(wantBody))
	}
	if log, err := os.ReadFile(*result.OutputPath); err != nil || string(log) != output {
		t.Fatalf("retained output = %q, error=%v, want %q", string(log), err, output)
	}
	if delta := result.PresentationDelta; delta == nil || !delta.RawOutputRequested || delta.ShellExitCode == nil || *delta.ShellExitCode != 7 {
		t.Fatalf("presentation facts = %#v, want raw output and exit code 7", delta)
	}
}
