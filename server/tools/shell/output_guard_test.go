package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"core/server/tools"
	"core/shared/toolspec"
)

func assertOversizedOutputFailure(t *testing.T, result tools.Result, logPath string) {
	t.Helper()
	want := fmt.Sprintf(oversizedOutputMessageTemplate, logPath)
	wantBody, _ := json.Marshal(map[string]string{"error": want})
	if !result.IsError || string(result.Output) != string(wantBody) {
		t.Fatalf("failure = %s, want %s", string(result.Output), string(wantBody))
	}
}

func TestExecCommandGuardRetainsCompleteOutputAtStructuredPath(t *testing.T) {
	manager := newBackgroundTestManager(t)
	start, err := manager.Start(t.Context(), ExecRequest{
		Command:        []string{"/bin/sh", "-c", "printf 123456789012345678901234567890123456789012345678; exit 7"},
		DisplayCommand: "guarded",
		Workdir:        t.TempDir(),
		MaxOutputChars: 16_000,
		Raw:            true,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	exit := 7
	result, ok := newOversizedOutputGuard(20).FailedResult(
		tools.Call{ID: "guarded", Name: toolspec.ToolExecCommand},
		func() *int { value := 11; return &value }(),
		formatExecResponse(start),
		start.OutputPath,
		shellResultPresentationDelta(true, false, false, &exit),
	)
	if !ok {
		t.Fatal("guard did not trigger")
	}
	assertOversizedOutputFailure(t, result, start.OutputPath)
	if log, err := os.ReadFile(start.OutputPath); err != nil || string(log) != "123456789012345678901234567890123456789012345678" {
		t.Fatalf("retained output = %q, error=%v", string(log), err)
	}
}

func TestExecCommandGuardBoundaryCases(t *testing.T) {
	tests := []struct {
		name, output string
		cap          *int
		wantError    bool
		wantOutput   string
	}{
		{"output at half", strings.Repeat("1", 80), func() *int { v := 21; return &v }(), false, strings.Repeat("1", 80)},
		{"cap equal to half", strings.Repeat("1", 100), func() *int { v := 20; return &v }(), false, ""},
		{"cap below half", strings.Repeat("1", 100), func() *int { v := 19; return &v }(), false, ""},
		{"cap omitted", strings.Repeat("1", 84), nil, false, strings.Repeat("1", 84)},
		{"escaped output", strings.Repeat(`"\`, 40), func() *int { v := 21; return &v }(), false, strings.Repeat(`"\`, 40)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newBackgroundTestManager(t)
			input := map[string]any{"cmd": "printf '%s' '" + test.output + "'", "shell": "/bin/sh", "login": false, "yield_time_ms": 1_000}
			if test.cap != nil {
				input["max_output_tokens"] = *test.cap
			}
			result := callExecCommand(t, NewExecCommandTool(t.TempDir(), 16_000, 40, manager, ""), test.name, input)
			if result.IsError != test.wantError {
				t.Fatalf("is_error = %t, want %t", result.IsError, test.wantError)
			}
			if test.wantOutput != "" && decodeStringToolOutput(t, result) != test.wantOutput {
				t.Fatalf("output = %q, want %q", decodeStringToolOutput(t, result), test.wantOutput)
			}
		})
	}
}

func TestWriteStdinGuardPreservesStatesAndIndependentPolls(t *testing.T) {
	const plain = "12345678901234567890123456789012345678901234567890123456789012345678901234567890"
	tests := []struct {
		name, command, output, chars string
		running, later               bool
	}{
		{"running", "read line; printf '" + plain + "'; sleep 0.8", plain, "go\n", true, true},
		{"completed", "sleep 0.1; printf '" + plain + "'", plain, "", false, false},
		{"escaped", "read line; printf '%s' '" + strings.Repeat(`"\`, 20) + "'; sleep 0.8", strings.Repeat(`"\`, 20), "go\n", true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newShellTestManager(t, 50*time.Millisecond)
			backgrounded := make(chan Snapshot, 1)
			manager.SetEventHandler(func(event Event) bool {
				if event.Type == EventBackgrounded {
					backgrounded <- event.Snapshot
				}
				return true
			})
			start := callExecCommand(t, NewExecCommandTool(t.TempDir(), 16_000, 40, manager, ""), "start", map[string]any{
				"cmd": test.command, "shell": "/bin/sh", "login": false, "tty": true, "yield_time_ms": 50,
			})
			if start.IsError {
				t.Fatalf("start = %s", string(start.Output))
			}
			snapshot := <-backgrounded
			sessionID, err := strconv.Atoi(snapshot.ID)
			if err != nil {
				t.Fatalf("session ID: %v", err)
			}
			input := map[string]any{"session_id": sessionID, "yield_time_ms": 15_000, "max_output_tokens": 21}
			if test.chars != "" {
				input["chars"], input["yield_time_ms"] = test.chars, 50
			}
			result := callWriteStdin(t, NewWriteStdinTool(16_000, 40, manager), "poll", input)
			assertOversizedOutputFailure(t, result, snapshot.LogPath)
			if test.running {
				current, err := manager.Snapshot(snapshot.ID)
				if err != nil || !current.Running {
					t.Fatalf("guarded snapshot = %+v, error=%v", current, err)
				}
			}
			if test.later && callWriteStdin(t, NewWriteStdinTool(16_000, 40, manager), "later", map[string]any{
				"session_id": sessionID, "yield_time_ms": 15_000, "max_output_tokens": 19,
			}).IsError {
				t.Fatal("later independent poll returned an error")
			}
			if log, err := os.ReadFile(snapshot.LogPath); err != nil || string(log) != test.output {
				t.Fatalf("retained output = %q, error=%v", string(log), err)
			}
		})
	}
}
