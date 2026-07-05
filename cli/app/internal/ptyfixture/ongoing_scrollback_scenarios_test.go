package ptyfixture

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/pty"
)

func TestOngoingNativeScrollbackPTYScenarios(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	bin := filepath.Join(t.TempDir(), "kent-pty-fixture")
	if err := pty.BuildPackage(ctx, "core/cli/app/internal/ptyfixture/cmd/kent-pty-fixture", bin); err != nil {
		t.Fatalf("build fixture: %v", err)
	}

	for _, tc := range []struct {
		name                  string
		script                map[string]any
		inputs                []pty.InputEvent
		resizes               []pty.DriverResizeEvent
		expectedAppends       []string
		allowDuplicateAppends bool
		allowsAltScroll       bool
		allowsFullScreen      bool
	}{
		{
			name: "visibility_o_real_app_path",
			script: map[string]any{
				"final": "VISIBILITY_O_MODEL",
			},
			expectedAppends: []string{"❯ visibility_o_real_app_path", "❮ VISIBILITY_O_MODEL"},
		},
		{
			name: "visibility_oc_tool_real_app_path",
			script: map[string]any{
				"steps": []map[string]any{
					{
						"commentary": "calling shell tool",
						"tool_calls": []map[string]any{
							{"id": "call_1", "name": "exec_command", "input": map[string]any{"cmd": "printf 'VISIBILITY_OC_TOOL\\n'"}},
						},
					},
					{
						"expected_tool_results": []map[string]any{{"CallID": "call_1", "Name": "exec_command"}},
						"final":                 "tool path complete",
					},
				},
			},
			expectedAppends: []string{"❮ tool path complete"},
		},
		{
			name: "visibility_d_detail_only_real_app_path",
			script: map[string]any{
				"final": "detail-only fixture completed",
			},
			expectedAppends: []string{"❯ visibility_d_detail_only_real_app_path", "❮ detail-only fixture completed"},
		},
		{
			name: "visibility_x_hidden_real_app_path",
			script: map[string]any{
				"final": "hidden fixture completed",
			},
			expectedAppends: []string{"❯ visibility_x_hidden_real_app_path", "❮ hidden fixture completed"},
		},
		{
			name: "markdown_streaming_promotion_and_final_tail",
			script: map[string]any{
				"prompt":        "stream markdown",
				"stream_deltas": []string{"Stable paragraph.\n\n", "volatile tail"},
				"final":         "Stable paragraph.\n\nvolatile tail",
			},
			expectedAppends: []string{rightPad("Stable paragraph.", 80)},
		},
		{
			name: "long_final_answer_with_resize",
			script: map[string]any{
				"prompt": "long final",
				"final":  "line 01\nline 02\nline 03\nline 04\nline 05\nline 06\nline 07\nline 08\nline 09\nline 10\nline 11\nline 12\nline 13\nline 14\nline 15\nline 16\nline 17\nline 18\nline 19\nline 20\nline 21\nline 22\nline 23\nline 24\nline 25",
			},
			expectedAppends:       []string{"❮ line 01"},
			allowDuplicateAppends: true,
			resizes: []pty.DriverResizeEvent{{
				After:      500 * time.Millisecond,
				Dimensions: pty.MustDimensions(18, 72),
			}},
		},
		{
			name: "parallel_tools_order_and_long_output",
			script: map[string]any{
				"prompt": "run tools",
				"steps": []map[string]any{
					{
						"commentary": "I'll run checks.",
						"tool_calls": []map[string]any{
							{"id": "call_1", "name": "exec_command", "input": map[string]any{"cmd": "printf 'TOOL_ONE_OK\\n'"}},
							{"id": "call_2", "name": "exec_command", "input": map[string]any{"cmd": "for i in $(seq 1 12); do printf 'TOOL_TWO_%02d\\n' \"$i\"; done"}},
						},
					},
					{
						"expected_tool_results": []map[string]any{
							{"CallID": "call_1", "Name": "exec_command"},
							{"CallID": "call_2", "Name": "exec_command"},
						},
						"final": "tools complete",
					},
				},
			},
			expectedAppends: []string{"❮ tools complete"},
		},
		{
			name: "detail_roundtrip_during_stream",
			script: map[string]any{
				"prompt":        "detail roundtrip",
				"stream_deltas": []string{"roundtrip commentary\n\n"},
				"final":         "roundtrip commentary\n\nroundtrip complete",
			},
			expectedAppends: []string{rightPad("roundtrip complete", 80)},
			inputs: []pty.InputEvent{
				{After: 600 * time.Millisecond, Bytes: []byte("\x1b[Z")},
				{After: 1100 * time.Millisecond, Bytes: []byte("\x1b[Z")},
			},
			allowsAltScroll:  true,
			allowsFullScreen: true,
		},
		{
			name: "warning_notice_shape",
			script: map[string]any{
				"prompt":        "notice",
				"stream_deltas": []string{"working\n\n"},
				"final":         "working\n\ndone",
			},
			expectedAppends: []string{rightPad("done", 80)},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			capture, observationsPath := runPTYFixtureScenario(t, ctx, bin, tc.name, tc.script, tc.inputs, tc.resizes)
			analysis, err := pty.Analyze(capture)
			if err != nil {
				t.Fatalf("analyze capture: %v", err)
			}
			window, err := scenarioOperationWindow(analysis)
			if err != nil {
				t.Fatalf("resolve scenario operation window: %v", err)
			}
			appends, err := scenarioAppendRowsWithBoundaryChecks(analysis, window, tc.expectedAppends, tc.allowDuplicateAppends)
			if err != nil {
				t.Fatalf("append boundary windows: %v", err)
			}
			if !tc.allowsAltScroll {
				if err := pty.NoAlternateScroll1007(analysis, window); err != nil {
					t.Fatalf("forbidden alternate-scroll mode: %v", err)
				}
			}
			if !tc.allowsFullScreen {
				if err := pty.NoFullScreenReEmission(analysis, window); err != nil {
					t.Fatalf("full-screen re-emission: %v", err)
				}
			}
			for _, content := range tc.expectedAppends {
				var err error
				if tc.allowDuplicateAppends {
					err = contentAppendedAtLeastOnce(appends, content)
				} else {
					err = pty.ContentAppendedExactlyOnce(appends, content)
				}
				if err != nil {
					t.Fatalf("append cardinality: %v", err)
				}
			}
			if analysis.Screen.IsBlank() {
				t.Fatal("ongoing TUI screen is blank after scenario")
			}
			var obs fixtureObservation
			data, err := os.ReadFile(observationsPath)
			if err != nil {
				t.Fatalf("read observations: %v", err)
			}
			if err := json.Unmarshal(data, &obs); err != nil {
				t.Fatalf("decode observations: %v", err)
			}
			if obs.ModelRequestCount == 0 || !obs.FinalResponseConsumed {
				t.Fatalf("script did not complete through fixture: %+v", obs)
			}
		})
	}
}

func runPTYFixtureScenario(t *testing.T, ctx context.Context, bin string, name string, script map[string]any, inputs []pty.InputEvent, resizes []pty.DriverResizeEvent) (pty.Capture, string) {
	t.Helper()
	root := t.TempDir()
	scriptPath := filepath.Join(root, "script.json")
	data, err := json.Marshal(script)
	if err != nil {
		t.Fatalf("marshal script: %v", err)
	}
	if err := os.WriteFile(scriptPath, data, 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	observationsPath := filepath.Join(root, "observations.json")
	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path: bin,
		Args: []string{
			"--workspace", filepath.Join(root, "workspace"),
			"--persistence-root", filepath.Join(root, "persistence"),
			"--script", scriptPath,
			"--observations", observationsPath,
		},
		Dimensions: pty.MustDimensions(24, 80),
		PhaseInputs: []pty.PhaseInputEvent{
			{Phase: pty.PhaseScenarioStart, Bytes: []byte(name + "\r")},
		},
		Inputs:  append(append([]pty.InputEvent(nil), inputs...), pty.InputEvent{After: 4 * time.Second, Bytes: []byte{0x03, 0x03}}),
		Resizes: resizes,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run fixture: %v raw=%q", err, string(capture.Raw))
	}
	return capture, observationsPath
}

func scenarioOperationWindow(analysis pty.Analysis) (pty.OperationWindow, error) {
	var start *int
	for _, event := range analysis.PhaseEvents {
		switch event.Phase {
		case pty.PhaseScenarioStart:
			value := firstOperationAtOrAfterByte(analysis.Operations, event.ByteRange.End)
			start = &value
		}
	}
	if start == nil {
		return pty.OperationWindow{}, fmt.Errorf("scenario start marker missing")
	}
	return pty.OperationWindow{Start: *start, End: len(analysis.Operations)}, nil
}

func scenarioAppendRowsWithBoundaryChecks(analysis pty.Analysis, window pty.OperationWindow, expected []string, allowDuplicates bool) ([]pty.AppendOperation, error) {
	if len(expected) == 0 {
		return nil, fmt.Errorf("scenario requires exact appended content expectations")
	}
	var lastTexts []string
	for boundary := analysis.Dimensions.Rows - 1; boundary > 0; boundary-- {
		appends := pty.CoalesceAppendRows(pty.ClassifyAppends(analysis, window, boundary))
		lastTexts = appendTexts(appends)
		matched := true
		for _, content := range expected {
			var err error
			if allowDuplicates {
				err = contentAppendedAtLeastOnce(appends, content)
			} else {
				err = pty.ContentAppendedExactlyOnce(appends, content)
			}
			if err != nil {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		appendAnalysis := analysis
		appendAnalysis.Operations = make([]pty.Operation, 0, len(appends))
		for _, appendOperation := range appends {
			appendAnalysis.Operations = append(appendAnalysis.Operations, appendOperation.Operation)
		}
		if err := pty.NoWritesAbove(appendAnalysis, pty.OperationWindow{Start: 0, End: len(appendAnalysis.Operations)}, boundary); err != nil {
			return nil, err
		}
		return appends, nil
	}
	return nil, fmt.Errorf("no non-zero append boundary classified expected content exactly once; candidates=%q", lastTexts)
}

func contentAppendedAtLeastOnce(appends []pty.AppendOperation, content string) error {
	for _, appendOperation := range appends {
		if appendOperation.Operation.Write != nil && appendOperation.Operation.Write.Text == content {
			return nil
		}
	}
	return fmt.Errorf("content append count for %q = 0, want at least 1", content)
}

func appendTexts(appends []pty.AppendOperation) []string {
	out := make([]string, 0, len(appends))
	for _, appendOperation := range appends {
		if appendOperation.Operation.Write != nil {
			out = append(out, appendOperation.Operation.Write.Text)
		}
	}
	if len(out) > 40 {
		return out[len(out)-40:]
	}
	return out
}

func rightPad(value string, width int) string {
	if len(value) >= width {
		return value
	}
	return value + strings.Repeat(" ", width-len(value))
}

func firstOperationAtOrAfterByte(operations []pty.Operation, offset int64) int {
	for index, operation := range operations {
		if operation.ByteRange.Start >= offset {
			return index
		}
	}
	return len(operations)
}
