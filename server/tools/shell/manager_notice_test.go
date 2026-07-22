package shell

import (
	"testing"

	"core/server/tools/shell/postprocess"
)

func TestProjectExecResultClassifiesTerminalVisibleOutput(t *testing.T) {
	zero := 0
	nonZero := 7
	warning, err := postprocess.NewWarning("recoverable warning")
	if err != nil {
		t.Fatalf("NewWarning: %v", err)
	}

	tests := []struct {
		name         string
		result       ExecResult
		wantKind     execPresentationKind
		wantExitCode int
		wantOutput   bool
		wantWarning  bool
	}{
		{
			name:         "zero exit whitespace command output",
			result:       ExecResult{ExitCode: &zero, Output: " \t\n"},
			wantKind:     execPresentationForegroundCompleted,
			wantExitCode: 0,
		},
		{
			name:         "non-zero exit whitespace command output",
			result:       ExecResult{ExitCode: &nonZero, Output: "\n"},
			wantKind:     execPresentationForegroundCompleted,
			wantExitCode: 7,
		},
		{
			name:         "warning-only success",
			result:       ExecResult{ExitCode: &zero, Warning: warning},
			wantKind:     execPresentationForegroundCompleted,
			wantExitCode: 0,
			wantOutput:   true,
			wantWarning:  true,
		},
		{
			name:         "postprocessor replacement with empty output",
			result:       ExecResult{ExitCode: &zero, Output: ""},
			wantKind:     execPresentationForegroundCompleted,
			wantExitCode: 0,
		},
		{
			name:         "non-empty command output",
			result:       ExecResult{ExitCode: &zero, Output: "visible"},
			wantKind:     execPresentationForegroundCompleted,
			wantExitCode: 0,
			wantOutput:   true,
		},
		{
			name:         "completed background",
			result:       ExecResult{ExitCode: &zero, Backgrounded: true},
			wantKind:     execPresentationBackgroundCompleted,
			wantExitCode: 0,
		},
		{
			name:         "completed background non-zero output",
			result:       ExecResult{ExitCode: &nonZero, Backgrounded: true, Output: "visible"},
			wantKind:     execPresentationBackgroundCompleted,
			wantExitCode: 7,
			wantOutput:   true,
		},
		{
			name:     "background transition",
			result:   ExecResult{Running: true, Backgrounded: true, MovedToBackground: true, SessionID: "1000"},
			wantKind: execPresentationRunning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			presentation := projectExecResult(tt.result)
			if presentation.kind != tt.wantKind {
				t.Fatalf("presentation kind = %d, want %d", presentation.kind, tt.wantKind)
			}
			if tt.wantKind != execPresentationRunning && presentation.exitCode != tt.wantExitCode {
				t.Fatalf("exit code = %d, want %d", presentation.exitCode, tt.wantExitCode)
			}
			if got := presentation.output.HasVisibleContent(); got != tt.wantOutput {
				t.Fatalf("visible output = %t, want %t", got, tt.wantOutput)
			}
			if got := presentation.output.Warning() != nil; got != tt.wantWarning {
				t.Fatalf("warning presence = %t, want %t", got, tt.wantWarning)
			}
		})
	}
}
