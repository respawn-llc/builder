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

	"core/cli/tui/transcriptrender"
	"core/internal/testharness/pty"
)

type styledAppendExpectation struct {
	Text       string
	Foreground string
	Faint      bool
}

type styledRowExpectation []styledAppendExpectation

const defaultTerminalForeground = "#c0c0c0"
const markdownTerminalForeground = "#d0d0d0"

func TestOngoingNativeScrollbackPTYScenarios(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	bin := filepath.Join(t.TempDir(), "kent-pty-fixture")
	if err := pty.BuildPackage(ctx, "core/cli/app/internal/ptyfixture/cmd/kent-pty-fixture", bin); err != nil {
		t.Fatalf("build fixture: %v", err)
	}

	for _, tc := range []struct {
		name                      string
		script                    map[string]any
		inputs                    []pty.InputEvent
		resizes                   []pty.DriverResizeEvent
		expectedAppends           []string
		expectedAnyAppends        []string
		forbiddenAnyAppends       []string
		expectedFaintDividerCount int
		expectedStyledAppends     []styledAppendExpectation
		expectedStyledWrites      []styledAppendExpectation
		forbiddenStyledWrites     []styledAppendExpectation
		expectedStyledRows        []styledRowExpectation
		allowDuplicateAppends     bool
		allowsAltScroll           bool
		allowsFullScreen          bool
		interruptAfter            *time.Duration
	}{
		{
			name: "visibility_o_real_app_path",
			script: map[string]any{
				"seed_transcript": []map[string]any{
					{"kind": "message", "role": "user", "text": "PTY_SEED_O_USER"},
				},
				"final": "VISIBILITY_O_MODEL",
			},
			expectedAppends:    []string{"❯ visibility_o_real_app_path", "❮ VISIBILITY_O_MODEL"},
			expectedAnyAppends: []string{"❯ PTY_SEED_O_USER"},
		},
		{
			name: "visibility_oc_tool_real_app_path",
			script: map[string]any{
				"seed_transcript": []map[string]any{
					{"kind": "local_entry", "visibility": "OC", "role": "system", "text": "PTY_SEED_OC_FULL_DETAIL_TEXT", "condensed_text": "PTY_SEED_OC_COMPACT"},
				},
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
			expectedAppends:     []string{"❮ tool path complete"},
			expectedAnyAppends:  []string{"ℹ PTY_SEED_OC_COMPACT"},
			forbiddenAnyAppends: []string{"ℹ PTY_SEED_OC_FULL_DETAIL_TEXT"},
		},
		{
			name: "visibility_d_detail_only_real_app_path",
			script: map[string]any{
				"seed_transcript": []map[string]any{
					{"kind": "message", "role": "developer", "message_type": "environment", "text": "PTY_SEED_D_DETAIL_ONLY", "condensed_text": "PTY_SEED_D_COMPACT"},
				},
				"final": "detail-only fixture completed",
			},
			expectedAppends:     []string{"❯ visibility_d_detail_only_real_app_path", "❮ detail-only fixture completed"},
			forbiddenAnyAppends: []string{"ℹ PTY_SEED_D_DETAIL_ONLY", "ℹ PTY_SEED_D_COMPACT"},
		},
		{
			name: "visibility_x_hidden_real_app_path",
			script: map[string]any{
				"seed_transcript": []map[string]any{
					{"kind": "local_entry", "visibility": "X", "role": "system", "text": "PTY_SEED_X_HIDDEN", "condensed_text": "PTY_SEED_X_COMPACT"},
				},
				"final": "hidden fixture completed",
			},
			expectedAppends:     []string{"❯ visibility_x_hidden_real_app_path", "❮ hidden fixture completed"},
			forbiddenAnyAppends: []string{"ℹ PTY_SEED_X_HIDDEN", "ℹ PTY_SEED_X_COMPACT"},
		},
		{
			name: "seeded_tool_message_style_matrix_real_app_path",
			script: map[string]any{
				"seed_transcript": seededStyleMatrixTranscript(),
				"final":           "style matrix complete",
			},
			expectedAppends: []string{"❮ style matrix complete"},
			expectedAnyAppends: []string{
				"❯ PTY_STYLE_USER",
				"❮ PTY_STYLE_ASSISTANT",
				"⚠ PTY_STYLE_WARNING",
				"! PTY_STYLE_ERROR",
				"ℹ PTY_STYLE_NOTICE",
				"ℹ PTY_STYLE_TOGGLE_FAST_ON",
				"ℹ PTY_STYLE_TOGGLE_FAST_OFF",
				"ℹ PTY_STYLE_TOGGLE_SUPERVISOR_ON",
				"ℹ PTY_STYLE_TOGGLE_SUPERVISOR_OFF",
				"$ printf 'PTY_TOOL_SHELL",
				"⇄ ./pty_patch.txt -1 +1",
				"⇄ ./pty_edit.txt -1 +1",
				"• path: image.png",
				"@ web search: \"PTY_TOOL_WEB\"",
				"• {\"input\":\"PTY_TOOL_CUSTOM\"}",
				"• commentary: PTY_TOOL_COMPLETE",
				"? PTY_TOOL_QUESTION",
				"• Model requested compaction.",
				"❯ seeded_tool_message_style_matrix_real_app_path",
			},
			expectedFaintDividerCount: 3,
			expectedStyledAppends: []styledAppendExpectation{
				{Text: "❯ PTY_STYLE_USER", Foreground: colorForStyle(transcriptrender.StyleRoleUser)},
				{Text: "❮ PTY_STYLE_ASSISTANT", Foreground: colorForStyle(transcriptrender.StyleRoleAssistant)},
				{Text: "⚠ PTY_STYLE_WARNING", Foreground: colorForStyle(transcriptrender.StyleRoleWarning)},
				{Text: "! PTY_STYLE_ERROR", Foreground: colorForStyle(transcriptrender.StyleRoleError)},
				{Text: "ℹ PTY_STYLE_NOTICE", Foreground: colorForStyle(transcriptrender.StyleRoleNotice)},
				{Text: "ℹ PTY_STYLE_TOGGLE_FAST_ON", Foreground: colorForStyle(transcriptrender.StyleRoleNotice)},
				{Text: "ℹ PTY_STYLE_TOGGLE_FAST_OFF", Foreground: colorForStyle(transcriptrender.StyleRoleNotice)},
				{Text: "ℹ PTY_STYLE_TOGGLE_SUPERVISOR_ON", Foreground: colorForStyle(transcriptrender.StyleRoleNotice)},
				{Text: "ℹ PTY_STYLE_TOGGLE_SUPERVISOR_OFF", Foreground: colorForStyle(transcriptrender.StyleRoleNotice)},
				{Text: "-1", Foreground: colorForStyle(transcriptrender.StyleRoleToolError)},
				{Text: "+1", Foreground: colorForStyle(transcriptrender.StyleRoleToolSuccess)},
				{Text: "❯ seeded_tool_message_style_matrix_real_app_path", Foreground: colorForStyle(transcriptrender.StyleRoleUser)},
			},
			expectedStyledRows: []styledRowExpectation{
				{
					{Text: "$", Foreground: colorForStyle(transcriptrender.StyleRoleToolSuccess)},
					{Text: "printf", Foreground: colorForStyle(transcriptrender.StyleRoleToolShellSecondary), Faint: true},
					{Text: "PTY_TOOL_SHELL", Foreground: colorForStyle(transcriptrender.StyleRoleToolShell), Faint: true},
				},
				{
					{Text: "⇄", Foreground: colorForStyle(transcriptrender.StyleRoleToolSuccess)},
					{Text: " ./pty_patch.txt ", Foreground: colorForStyle(transcriptrender.StyleRoleToolPatch)},
					{Text: "-1", Foreground: colorForStyle(transcriptrender.StyleRoleToolError)},
					{Text: "+1", Foreground: colorForStyle(transcriptrender.StyleRoleToolSuccess)},
				},
				{
					{Text: "⇄", Foreground: colorForStyle(transcriptrender.StyleRoleToolSuccess)},
					{Text: " ./pty_edit.txt ", Foreground: colorForStyle(transcriptrender.StyleRoleToolPatch)},
					{Text: "-1", Foreground: colorForStyle(transcriptrender.StyleRoleToolError)},
					{Text: "+1", Foreground: colorForStyle(transcriptrender.StyleRoleToolSuccess)},
				},
				{
					{Text: "•", Foreground: colorForStyle(transcriptrender.StyleRoleToolSuccess)},
					{Text: " path: image.png", Foreground: colorForStyle(transcriptrender.StyleRoleTool), Faint: true},
				},
				{
					{Text: "@", Foreground: colorForStyle(transcriptrender.StyleRoleToolSuccess)},
					{Text: " web search: \"PTY_TOOL_WEB\"", Foreground: colorForStyle(transcriptrender.StyleRoleToolWebSearch), Faint: true},
				},
				{
					{Text: "•", Foreground: colorForStyle(transcriptrender.StyleRoleToolSuccess)},
					{Text: " {\"input\":\"PTY_TOOL_CUSTOM\"}", Foreground: colorForStyle(transcriptrender.StyleRoleTool), Faint: true},
				},
				{
					{Text: "•", Foreground: colorForStyle(transcriptrender.StyleRoleToolSuccess)},
					{Text: " commentary: PTY_TOOL_COMPLETE", Foreground: colorForStyle(transcriptrender.StyleRoleTool), Faint: true},
				},
				{
					{Text: "?", Foreground: colorForStyle(transcriptrender.StyleRoleToolSuccess)},
					{Text: " PTY_TOOL_QUESTION", Foreground: colorForStyle(transcriptrender.StyleRoleToolQuestion), Faint: true},
				},
				{
					{Text: "•", Foreground: colorForStyle(transcriptrender.StyleRoleToolSuccess)},
					{Text: " Model requested compaction.", Foreground: colorForStyle(transcriptrender.StyleRoleTool), Faint: true},
				},
			},
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
			name: "slash_input_status_live_area_during_stream",
			script: map[string]any{
				"prompt":          "stream slash command",
				"stream_deltas":   []string{"stream slash live", "\n\nstream slash done"},
				"stream_delay_ms": 8000,
				"final":           "stream slash live\n\nstream slash done",
			},
			inputs: []pty.InputEvent{
				{After: 4 * time.Second, Bytes: []byte("queued while streaming")},
				{After: 5200 * time.Millisecond, Bytes: []byte{0x15}},
				{After: 5500 * time.Millisecond, Bytes: []byte("/status")},
				{After: 6200 * time.Millisecond, Bytes: []byte("\r")},
				{After: 7200 * time.Millisecond, Bytes: []byte("\x1b")},
			},
			expectedAppends:       []string{rightPad("stream slash live", 80)},
			allowDuplicateAppends: true,
			allowsAltScroll:       true,
			allowsFullScreen:      true,
			interruptAfter:        durationPtr(12 * time.Second),
			expectedStyledAppends: []styledAppendExpectation{
				{Text: rightPad("stream slash live", 80), Foreground: markdownTerminalForeground},
			},
			forbiddenStyledWrites: []styledAppendExpectation{
				{Text: rightPad("Server: owned by this CLI", 80), Foreground: defaultTerminalForeground},
			},
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
				"prompt":          "detail roundtrip",
				"stream_deltas":   []string{"roundtrip commentary\n", "\n"},
				"stream_delay_ms": 2000,
				"final":           "roundtrip commentary\n\nroundtrip complete",
			},
			expectedAppends: []string{rightPad("roundtrip complete", 80)},
			inputs: []pty.InputEvent{
				{After: 1500 * time.Millisecond, Bytes: []byte("\x1b[Z")},
				{After: 3200 * time.Millisecond, Bytes: []byte("\x1b[Z")},
			},
			allowsAltScroll:  true,
			allowsFullScreen: true,
			interruptAfter:   durationPtr(6 * time.Second),
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
			capture, observationsPath := runPTYFixtureScenario(t, ctx, bin, tc.name, tc.script, tc.inputs, tc.resizes, tc.interruptAfter)
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
			allAppends, err := allScenarioAppendRows(analysis, tc.expectedAppends)
			if err != nil {
				t.Fatalf("all append rows: %v", err)
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
			for _, content := range tc.expectedAnyAppends {
				if err := contentAppendedAtLeastOnce(allAppends, content); err != nil {
					t.Fatalf("expected full-window append: %v", err)
				}
			}
			for _, content := range tc.forbiddenAnyAppends {
				if err := contentNotAppended(allAppends, content); err != nil {
					t.Fatalf("forbidden full-window append: %v", err)
				}
			}
			if tc.expectedFaintDividerCount > 0 {
				if err := faintDividerAppendedAtLeast(allAppends, tc.expectedFaintDividerCount); err != nil {
					t.Fatalf("expected faint divider appends: %v", err)
				}
			}
			if len(tc.expectedStyledAppends) > 0 {
				rawAppends, err := allScenarioRawAppendRows(analysis, tc.expectedAppends)
				if err != nil {
					t.Fatalf("raw append rows: %v", err)
				}
				styledAppends := coalesceStyledAppendRuns(rawAppends)
				for _, expected := range tc.expectedStyledAppends {
					if err := styledContentAppendedAtLeastOnce(styledAppends, expected); err != nil {
						t.Fatalf("expected styled full-window append: %v", err)
					}
				}
				for _, expected := range tc.expectedStyledRows {
					if err := styledRowAppendedAtLeastOnce(styledAppends, expected); err != nil {
						t.Fatalf("expected styled row append: %v", err)
					}
				}
			}
			if len(tc.expectedStyledWrites) > 0 {
				rawWrites := allScenarioRawWrites(analysis, window)
				styledWrites := coalesceStyledAppendRuns(rawWrites)
				for _, expected := range tc.expectedStyledWrites {
					if err := styledContentAppendedAtLeastOnce(styledWrites, expected); err != nil {
						t.Fatalf("expected styled full-window write: %v", err)
					}
				}
			}
			if len(tc.forbiddenStyledWrites) > 0 {
				rawWrites := allScenarioRawWrites(analysis, window)
				styledWrites := coalesceStyledAppendRuns(rawWrites)
				for _, forbidden := range tc.forbiddenStyledWrites {
					if err := contentNotAppended(styledWrites, forbidden.Text); err != nil {
						t.Fatalf("forbidden styled full-window write: %v", err)
					}
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

func seededStyleMatrixTranscript() []map[string]any {
	return []map[string]any{
		{"kind": "message", "role": "user", "text": "PTY_STYLE_USER"},
		{"kind": "message", "role": "assistant", "text": "PTY_STYLE_ASSISTANT"},
		{"kind": "local_entry", "visibility": "O", "role": "warning", "text": "PTY_STYLE_WARNING"},
		{"kind": "local_entry", "visibility": "O", "role": "error", "text": "PTY_STYLE_ERROR"},
		{"kind": "local_entry", "visibility": "O", "role": "system", "text": "PTY_STYLE_NOTICE"},
		{"kind": "local_entry", "visibility": "O", "role": "system", "text": "PTY_STYLE_TOGGLE_FAST_ON"},
		{"kind": "local_entry", "visibility": "O", "role": "system", "text": "PTY_STYLE_TOGGLE_FAST_OFF"},
		{"kind": "local_entry", "visibility": "O", "role": "system", "text": "PTY_STYLE_TOGGLE_SUPERVISOR_ON"},
		{"kind": "local_entry", "visibility": "O", "role": "system", "text": "PTY_STYLE_TOGGLE_SUPERVISOR_OFF"},
		toolSeed("exec_command", "call_shell", map[string]any{"cmd": "printf 'PTY_TOOL_SHELL\n'"}, "PTY_TOOL_SHELL", false),
		toolSeedWithPatch("patch", "call_patch", map[string]any{"patch": patchStyleFixture("pty_patch.txt")}, patchStyleFixture("pty_patch.txt"), false),
		toolSeedWithPatch("edit", "call_edit", map[string]any{"file_path": "pty_edit.txt", "old_string": "old", "new_string": "new"}, patchStyleFixture("pty_edit.txt"), false),
		toolSeed("view_image", "call_image", map[string]any{"path": "image.png"}, "PTY_TOOL_IMAGE", false),
		toolSeed("web_search", "call_web", map[string]any{"query": "PTY_TOOL_WEB"}, "PTY_TOOL_WEB", false),
		toolSeed("custom_tool", "call_custom", map[string]any{"input": "PTY_TOOL_CUSTOM"}, "PTY_TOOL_CUSTOM", true),
		toolSeed("complete_node", "call_complete", map[string]any{"commentary": "PTY_TOOL_COMPLETE"}, "PTY_TOOL_COMPLETE", false),
		toolSeed("ask_question", "call_question", map[string]any{"question": "PTY_TOOL_QUESTION", "suggestions": []string{"yes"}}, "PTY_TOOL_QUESTION", false),
		toolSeed("trigger_handoff", "call_handoff", map[string]any{"future_agent_message": "PTY_TOOL_HANDOFF"}, "PTY_TOOL_HANDOFF", false),
	}
}

func toolSeed(name string, callID string, input map[string]any, condensed string, custom bool) map[string]any {
	rawInput, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	return map[string]any{
		"kind":           "tool_result",
		"tool_name":      name,
		"tool_call_id":   callID,
		"tool_input":     json.RawMessage(rawInput),
		"tool_output":    json.RawMessage(`{"summary":"ok"}`),
		"tool_condensed": condensed,
		"tool_custom":    custom,
	}
}

func toolSeedWithPatch(name string, callID string, input map[string]any, patchText string, custom bool) map[string]any {
	seed := toolSeed(name, callID, input, "", custom)
	seed["tool_patch"] = patchText
	return seed
}

func patchStyleFixture(path string) string {
	return "*** Begin Patch\n*** Update File: " + path + "\n@@\n-old\n+new\n*** End Patch\n"
}

func runPTYFixtureScenario(t *testing.T, ctx context.Context, bin string, name string, script map[string]any, inputs []pty.InputEvent, resizes []pty.DriverResizeEvent, interruptAfter *time.Duration) (pty.Capture, string) {
	t.Helper()
	resolvedInterruptAfter := 4 * time.Second
	if interruptAfter != nil {
		if *interruptAfter <= 0 {
			t.Fatalf("interruptAfter must be greater than zero: %s", (*interruptAfter).String())
		}
		resolvedInterruptAfter = *interruptAfter
	}
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
		Inputs:  append(append([]pty.InputEvent(nil), inputs...), pty.InputEvent{After: resolvedInterruptAfter, Bytes: []byte{0x03, 0x03}}),
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

func allScenarioAppendRows(analysis pty.Analysis, expected []string) ([]pty.AppendOperation, error) {
	if len(expected) == 0 {
		return nil, nil
	}
	window := pty.OperationWindow{Start: 0, End: len(analysis.Operations)}
	var lastTexts []string
	for boundary := analysis.Dimensions.Rows - 1; boundary > 0; boundary-- {
		appends := pty.CoalesceAppendRows(pty.ClassifyAppends(analysis, window, boundary))
		lastTexts = appendTexts(appends)
		matched := true
		for _, content := range expected {
			if err := contentAppendedAtLeastOnce(appends, content); err != nil {
				matched = false
				break
			}
		}
		if matched {
			return appends, nil
		}
	}
	return nil, fmt.Errorf("no append boundary classified full-window expected content; candidates=%q", lastTexts)
}

func allScenarioRawAppendRows(analysis pty.Analysis, expected []string) ([]pty.AppendOperation, error) {
	if len(expected) == 0 {
		return nil, nil
	}
	window := pty.OperationWindow{Start: 0, End: len(analysis.Operations)}
	var lastTexts []string
	for boundary := analysis.Dimensions.Rows - 1; boundary > 0; boundary-- {
		rawAppends := pty.ClassifyAppends(analysis, window, boundary)
		coalesced := pty.CoalesceAppendRows(rawAppends)
		lastTexts = appendTexts(coalesced)
		matched := true
		for _, content := range expected {
			if err := contentAppendedAtLeastOnce(coalesced, content); err != nil {
				matched = false
				break
			}
		}
		if matched {
			return rawAppends, nil
		}
	}
	return nil, fmt.Errorf("no raw append boundary classified full-window expected content; candidates=%q", lastTexts)
}

func allScenarioRawWrites(analysis pty.Analysis, window pty.OperationWindow) []pty.AppendOperation {
	out := make([]pty.AppendOperation, 0)
	for _, operation := range analysis.Operations[window.Start:window.End] {
		if operation.Write == nil {
			continue
		}
		out = append(out, pty.AppendOperation{Operation: operation})
	}
	return out
}

func coalesceStyledAppendRuns(appends []pty.AppendOperation) []pty.AppendOperation {
	out := make([]pty.AppendOperation, 0, len(appends))
	for _, appendOperation := range appends {
		current := appendOperation.Operation
		if current.Write == nil {
			out = append(out, appendOperation)
			continue
		}
		if len(out) == 0 {
			out = append(out, appendOperation)
			continue
		}
		previous := &out[len(out)-1].Operation
		if previous.Write == nil ||
			previous.Region.Top != current.Region.Top ||
			previous.Region.Bottom != current.Region.Bottom ||
			previous.Region.Right != current.Region.Left ||
			previous.Write.Faint != current.Write.Faint ||
			previous.Write.Foreground != current.Write.Foreground {
			out = append(out, appendOperation)
			continue
		}
		previous.Region.Right = current.Region.Right
		payload := pty.MustWritePayload(previous.Write.Text + current.Write.Text)
		payload.Faint = previous.Write.Faint
		payload.Foreground = previous.Write.Foreground
		previous.Write = &payload
	}
	return out
}

func contentAppendedAtLeastOnce(appends []pty.AppendOperation, content string) error {
	for _, appendOperation := range appends {
		if appendOperation.Operation.Write != nil && appendOperation.Operation.Write.Text == content {
			return nil
		}
	}
	for start := range appends {
		combined := ""
		for idx := start; idx < len(appends); idx++ {
			if appends[idx].Operation.Write == nil {
				break
			}
			combined += appends[idx].Operation.Write.Text
			if combined == content {
				return nil
			}
			if len(combined) >= len(content) {
				break
			}
		}
	}
	return fmt.Errorf("content append count for %q = 0, want at least 1; appends=%q", content, appendTexts(appends))
}

func contentNotAppended(appends []pty.AppendOperation, content string) error {
	for _, appendOperation := range appends {
		if appendOperation.Operation.Write != nil && appendOperation.Operation.Write.Text == content {
			return fmt.Errorf("content append for %q found", content)
		}
	}
	return nil
}

// faintDividerAppendedAtLeast asserts that at least `minCount` faint divider
// rule appends were emitted. A divider rule is a faint, non-empty append whose
// visible text is made only of the box-drawing horizontal "─" rune (the divider
// shape). This asserts structure (a divider was emitted) rather than literal
// label text, so divider wording/styling changes do not break it.
func faintDividerAppendedAtLeast(appends []pty.AppendOperation, minCount int) error {
	count := 0
	for _, appendOperation := range appends {
		write := appendOperation.Operation.Write
		if write == nil || !write.Faint || write.Text == "" {
			continue
		}
		if isPlainDividerRule(write.Text) {
			count++
		}
	}
	if count < minCount {
		return fmt.Errorf("faint divider append count = %d, want at least %d", count, minCount)
	}
	return nil
}

func isPlainDividerRule(text string) bool {
	for _, r := range text {
		if r != '─' && r != '…' {
			return false
		}
	}
	return text != ""
}

func styledContentAppendedAtLeastOnce(appends []pty.AppendOperation, expected styledAppendExpectation) error {
	var candidates []styledAppendExpectation
	for _, appendOperation := range appends {
		write := appendOperation.Operation.Write
		if write == nil || write.Text != expected.Text {
			continue
		}
		candidates = append(candidates, styledAppendExpectation{Text: write.Text, Foreground: write.Foreground, Faint: write.Faint})
		if write.Faint != expected.Faint {
			continue
		}
		if write.Foreground != expected.Foreground {
			continue
		}
		return nil
	}
	return fmt.Errorf("styled append for %q with foreground=%q faint=%t not found; candidates=%+v raw=%+v", expected.Text, expected.Foreground, expected.Faint, candidates, firstStyledAppendSamples(appends, 80))
}

func styledRowAppendedAtLeastOnce(appends []pty.AppendOperation, expected styledRowExpectation) error {
	if len(expected) == 0 {
		return nil
	}
	for idx := range appends {
		write := appends[idx].Operation.Write
		if write == nil || !styledWriteMatches(write, expected[0]) {
			continue
		}
		row := appends[idx].Operation.Region.Top
		nextCol := appends[idx].Operation.Region.Right
		matched := 1
		for cursor := idx + 1; cursor < len(appends) && matched < len(expected); cursor++ {
			op := appends[cursor].Operation
			if op.Region.Top != row {
				break
			}
			if op.Region.Left < nextCol {
				continue
			}
			if op.Write == nil {
				continue
			}
			nextCol = op.Region.Right
			if styledWriteMatches(op.Write, expected[matched]) {
				matched++
			}
		}
		if matched == len(expected) {
			return nil
		}
	}
	return fmt.Errorf("styled row sequence not found: %+v", expected)
}

func styledWriteMatches(write *pty.WritePayload, expected styledAppendExpectation) bool {
	return write.Text == expected.Text && write.Foreground == expected.Foreground && write.Faint == expected.Faint
}

func firstStyledAppendSamples(appends []pty.AppendOperation, limit int) []styledAppendExpectation {
	out := make([]styledAppendExpectation, 0, min(limit, len(appends)))
	for _, appendOperation := range appends {
		if len(out) >= limit {
			return out
		}
		write := appendOperation.Operation.Write
		if write == nil || write.Text == "" {
			continue
		}
		out = append(out, styledAppendExpectation{Text: write.Text, Foreground: write.Foreground, Faint: write.Faint})
	}
	return out
}

func appendTexts(appends []pty.AppendOperation) []string {
	out := make([]string, 0, len(appends))
	for _, appendOperation := range appends {
		if appendOperation.Operation.Write != nil {
			out = append(out, appendOperation.Operation.Write.Text)
		}
	}
	return out
}

func rightPad(value string, width int) string {
	if len(value) >= width {
		return value
	}
	return value + strings.Repeat(" ", width-len(value))
}

func durationPtr(value time.Duration) *time.Duration {
	return &value
}

func colorForStyle(role transcriptrender.StyleRole) string {
	return strings.ToLower(transcriptrender.ColorForRole(transcriptrender.ColorRoleForStyle(role), "").TrueColor)
}

func firstOperationAtOrAfterByte(operations []pty.Operation, offset int64) int {
	for index, operation := range operations {
		if operation.ByteRange.Start >= offset {
			return index
		}
	}
	return len(operations)
}
