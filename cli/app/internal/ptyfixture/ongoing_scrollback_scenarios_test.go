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

var postResponseResizeCompletionDrain = 2 * time.Second

func TestOngoingNativeScrollbackPTYScenarios(t *testing.T) {
	buildCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	bin := buildPTYFixtureBinary(t, buildCtx)

	for _, tc := range []struct {
		name                      string
		script                    map[string]any
		env                       []string
		inputs                    []pty.InputEvent
		resizes                   []pty.DriverResizeEvent
		expectedAppends           []string
		expectedScrollbackAppends []string
		expectedAnyAppends        []string
		forbiddenAnyAppends       []string
		expectedScreenRows        []string
		allowsAltScroll           bool
		allowsFullScreen          bool
		completionDrain           *time.Duration
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
			name: "hydrated_legacy_final_assistant_full_answer",
			script: map[string]any{
				"seed_transcript": []map[string]any{
					{
						"kind":           "message",
						"role":           "assistant",
						"text":           "PTY_HYDRATED_FIRST\nPTY_HYDRATED_SECOND\nPTY_HYDRATED_THIRD",
						"condensed_text": "PTY_HYDRATED_COMPACT",
					},
				},
				"final": "hydration fixture complete",
			},
			expectedAppends: []string{
				"❯ hydrated_legacy_final_assistant_full_answer",
				"❮ hydration fixture complete",
			},
			expectedAnyAppends: []string{
				"❮ PTY_HYDRATED_FIRST PTY_HYDRATED_SECOND PTY_HYDRATED_THIRD",
			},
			forbiddenAnyAppends: []string{"❮ PTY_HYDRATED_COMPACT"},
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
			name: "markdown_streaming_promotion_and_final_tail",
			script: map[string]any{
				"prompt":        "stream markdown",
				"stream_deltas": []string{"Plain stable.\n\nUse `INLINE_CODE`.\n\n```text\nBLOCK_CODE\n```\n\n", "volatile tail"},
				"final":         "Plain stable.\n\nUse `INLINE_CODE`.\n\n```text\nBLOCK_CODE\n```\n\nvolatile tail",
			},
			expectedAppends:           []string{"Plain stable."},
			expectedScrollbackAppends: []string{"volatile tail"},
		},
		{
			name: "stable_history_not_replayed_after_resize",
			script: map[string]any{
				"prompt": "resize stable history",
				"final":  "stable history not replayed after resize",
			},
			expectedAppends: []string{"❮ stable history not replayed after resize"},
			resizes: []pty.DriverResizeEvent{{
				After:      500 * time.Millisecond,
				Dimensions: pty.MustDimensions(18, 72),
			}},
			completionDrain: &postResponseResizeCompletionDrain,
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
			name: "live_patch_call_structured_preview",
			script: map[string]any{
				"prompt": "apply a patch",
				"steps": []map[string]any{
					{
						"tool_calls": []map[string]any{
							{
								"id":   "87cffd9a-d9e4-49b5-a2a7-61c5e043b991",
								"name": "patch",
								"input": map[string]any{
									"patch": "*** Begin Patch\n*** Add File: pty_live_patch.txt\n+PATCH_LIVE_CONTENT\n*** End Patch\n",
								},
							},
						},
					},
					{
						"expected_tool_results": []map[string]any{
							{"CallID": "87cffd9a-d9e4-49b5-a2a7-61c5e043b991", "Name": "patch"},
						},
						"final": "patch lifecycle complete",
					},
				},
			},
			expectedAppends:     []string{"❮ patch lifecycle complete"},
			expectedAnyAppends:  []string{"⇄ ./pty_live_patch.txt +1"},
			forbiddenAnyAppends: []string{"⇄ tool call"},
		},
		{
			name: "live_ask_question_call_input_preview",
			script: map[string]any{
				"prompt": "ask a question",
				"steps": []map[string]any{
					{
						"tool_calls": []map[string]any{
							{
								"id":   "7cf8ac4b-0551-4814-a312-37e039523c1c",
								"name": "ask_question",
								"input": map[string]any{
									"question":                 "PTY_LIVE_QUESTION",
									"suggestions":              []string{"accept", "decline"},
									"recommended_option_index": 1,
								},
							},
						},
					},
					{
						"expected_tool_results": []map[string]any{
							{"CallID": "7cf8ac4b-0551-4814-a312-37e039523c1c", "Name": "ask_question"},
						},
						"final": "question lifecycle complete",
					},
				},
			},
			inputs:              []pty.InputEvent{{After: 1500 * time.Millisecond, Bytes: []byte("\r")}},
			expectedAppends:     []string{"❮ question lifecycle complete"},
			expectedAnyAppends:  []string{"? PTY_LIVE_QUESTION"},
			forbiddenAnyAppends: []string{"? tool call"},
		},
		{
			name: "live_background_shell_completion_style",
			env:  []string{"KENT_MINIMUM_EXEC_TO_BG_SECONDS=1"},
			script: map[string]any{
				"prompt": "start a background shell",
				"steps": []map[string]any{
					{
						"tool_calls": []map[string]any{
							{
								"id":   "28e08736-9539-41c1-a96c-56baf10e4fa4",
								"name": "exec_command",
								"input": map[string]any{
									"cmd":           "sleep 2; echo $((51515150+1))",
									"yield_time_ms": 1000,
								},
							},
						},
					},
					{
						"expected_tool_results": []map[string]any{
							{"CallID": "28e08736-9539-41c1-a96c-56baf10e4fa4", "Name": "exec_command"},
						},
						"final": "background launch complete",
					},
					{
						"final": "background continuation complete",
					},
				},
			},
			expectedAppends: []string{"❯ live_background_shell_completion_style"},
			expectedAnyAppends: []string{
				"ℹ Background shell 1000 completed (exit 0)",
			},
			expectedScreenRows: []string{"$ sleep 2; echo $((51515150+1))  " + transcriptrender.BackgroundedShellSuffix},
		},
		{
			name: "live_tool_promotion_and_input_dispositions",
			script: map[string]any{
				"prompt": "observe live tool lifecycle",
				"steps": []map[string]any{
					{
						"tool_calls": []map[string]any{
							{
								"id":    "4c2725e5-9997-45f9-8aaf-a79c1ae523f6",
								"name":  "exec_command",
								"input": map[string]any{"cmd": "sleep 5; echo $((42424241+1))"},
							},
						},
					},
					{
						"expected_tool_results": []map[string]any{
							{"CallID": "4c2725e5-9997-45f9-8aaf-a79c1ae523f6", "Name": "exec_command"},
						},
						"final": "live lifecycle complete",
					},
					{
						"final": "queued lifecycle complete",
					},
				},
			},
			inputs: []pty.InputEvent{
				{After: 1500 * time.Millisecond, Bytes: []byte("queued after tool start")},
				{After: 1700 * time.Millisecond, Bytes: []byte("\t")},
				{After: 2100 * time.Millisecond, Bytes: []byte("steering after tool start")},
				{After: 2300 * time.Millisecond, Bytes: []byte("\r")},
			},
			expectedAppends:    []string{"❮ live lifecycle complete", "❮ queued lifecycle complete"},
			expectedScreenRows: []string{"$ sleep 5; echo $((42424241+1))"},
		},
		{
			name: "live_failed_tools_retain_input",
			env:  []string{"SHELL=/bin/sh"},
			script: map[string]any{
				"prompt": "run failing tools",
				"steps": []map[string]any{
					{
						"tool_calls": []map[string]any{
							{
								"id":   "c02cd36b-4a5f-4c66-b632-d762cf424bb5",
								"name": "exec_command",
								"input": map[string]any{
									"cmd":     "echo $((61616160+1))",
									"workdir": "missing-workdir",
								},
							},
							{
								"id":   "9a728c41-f7ca-4776-922f-30166f146d6c",
								"name": "patch",
								"input": map[string]any{
									"patch": "*** Begin Patch\n*** Update File: pty_missing_patch.txt\n@@\n-old\n+new\n*** End Patch\n",
								},
							},
						},
					},
					{
						"expected_tool_results": []map[string]any{
							{"CallID": "c02cd36b-4a5f-4c66-b632-d762cf424bb5", "Name": "exec_command"},
							{"CallID": "9a728c41-f7ca-4776-922f-30166f146d6c", "Name": "patch"},
						},
						"final": "failed tool lifecycle complete",
					},
				},
			},
			expectedAppends: []string{"❯ live_failed_tools_retain_input"},
			expectedAnyAppends: []string{
				"❮ failed tool lifecycle complete",
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			scenarioCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			capture, observationsPath := runPTYFixtureScenario(t, scenarioCtx, bin, tc.name, tc.script, tc.env, tc.inputs, tc.resizes, tc.completionDrain)
			analysis, err := pty.Analyze(capture)
			if err != nil {
				t.Fatalf("analyze capture: %v", err)
			}
			window, err := scenarioOperationWindow(analysis)
			if err != nil {
				t.Fatalf("resolve scenario operation window: %v", err)
			}
			appends, err := scenarioAppendRowsWithBoundaryChecks(analysis, window, tc.expectedAppends)
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
				if err := contentAppendedExactlyOnce(appends, content); err != nil {
					t.Fatalf("append cardinality: %v", err)
				}
			}
			scrollbackAppends := scrollbackTransactionWrites(analysis, window)
			for _, content := range tc.expectedScrollbackAppends {
				if err := contentAppendedExactlyOnce(scrollbackAppends, content); err != nil {
					t.Fatalf("scrollback append cardinality: %v; appends=%q", err, appendTexts(scrollbackAppends))
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
			for _, row := range tc.expectedScreenRows {
				if err := screenRowAppearsExactlyOnce(analysis.Screen, row); err != nil {
					t.Fatalf("expected completed screen row: %v", err)
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

func runPTYFixtureScenario(t *testing.T, ctx context.Context, bin string, name string, script map[string]any, env []string, inputs []pty.InputEvent, resizes []pty.DriverResizeEvent, configuredCompletionDrain *time.Duration) (pty.Capture, string) {
	t.Helper()
	return runPTYFixtureScenarioWithInputPlan(
		t,
		ctx,
		bin,
		name,
		script,
		env,
		ptyFixtureInputPlan{scheduled: inputs},
		resizes,
		configuredCompletionDrain,
	)
}

type ptyFixtureInputPlan struct {
	scheduled      []pty.InputEvent
	frameSequences []pty.FrameInputSequence
}

func runPTYFixtureScenarioWithInputPlan(t *testing.T, ctx context.Context, bin string, name string, script map[string]any, env []string, inputPlan ptyFixtureInputPlan, resizes []pty.DriverResizeEvent, configuredCompletionDrain *time.Duration) (pty.Capture, string) {
	t.Helper()
	completionDrain := time.Duration(0)
	completionPhase := pty.PhaseScenarioFinalApplied
	if configuredCompletionDrain != nil {
		if *configuredCompletionDrain <= 0 {
			t.Fatalf("completion drain must be positive: %s", configuredCompletionDrain.String())
		}
		completionDrain = *configuredCompletionDrain
		completionPhase = pty.PhaseScenarioComplete
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
	env = append(env, ptyFixtureProcessEnv(
		t,
		root,
		filepath.Join(root, "workspace"),
		filepath.Join(root, "persistence"),
		scriptPath,
		observationsPath,
	))
	phaseInputs := make([]pty.PhaseInputEvent, 0, len(inputPlan.scheduled)+2)
	phaseInputs = append(phaseInputs, pty.PhaseInputEvent{Phase: pty.PhaseScenarioStart, Bytes: []byte(name + "\r")})
	for _, input := range inputPlan.scheduled {
		phaseInputs = append(phaseInputs, pty.PhaseInputEvent{
			Phase: pty.PhaseScenarioStart,
			After: input.After,
			Bytes: input.Bytes,
		})
	}
	phaseInputs = append(phaseInputs, pty.PhaseInputEvent{
		Phase: completionPhase,
		After: completionDrain,
		Bytes: []byte{0x03, 0x03},
	})
	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path:                bin,
		Env:                 append([]string(nil), env...),
		Dimensions:          pty.MustDimensions(24, 80),
		PhaseInputs:         phaseInputs,
		FrameInputSequences: inputPlan.frameSequences,
		Resizes:             resizes,
		Timeout:             75 * time.Second,
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

func scenarioAppendRowsWithBoundaryChecks(analysis pty.Analysis, window pty.OperationWindow, expected []string) ([]logicalAppendRow, error) {
	if len(expected) == 0 {
		return nil, fmt.Errorf("scenario requires exact appended content expectations")
	}
	var lastTexts []string
	for boundary := analysis.Dimensions.Rows - 1; boundary > 0; boundary-- {
		appends := classifyLogicalAppendRows(analysis, window, boundary)
		lastTexts = appendTexts(appends)
		matched := true
		for _, content := range expected {
			if err := contentAppendedExactlyOnce(appends, content); err != nil {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		appendAnalysis := analysis
		appendAnalysis.Operations = appendOperations(appends)
		if err := pty.NoWritesAbove(appendAnalysis, pty.OperationWindow{Start: 0, End: len(appendAnalysis.Operations)}, boundary); err != nil {
			return nil, err
		}
		return appends, nil
	}
	return nil, fmt.Errorf("no non-zero append boundary classified expected content exactly once; candidates=%q", lastTexts)
}

func allScenarioAppendRows(analysis pty.Analysis, expected []string) ([]logicalAppendRow, error) {
	if len(expected) == 0 {
		return nil, nil
	}
	window := pty.OperationWindow{Start: 0, End: len(analysis.Operations)}
	var lastTexts []string
	for boundary := analysis.Dimensions.Rows - 1; boundary > 0; boundary-- {
		appends := classifyLogicalAppendRows(analysis, window, boundary)
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

func scrollbackTransactionWrites(analysis pty.Analysis, window pty.OperationWindow) []logicalAppendRow {
	out := make([]pty.AppendOperation, 0)
	inRestrictedScrollRegion := false
	for _, transaction := range analysis.Operations[window.Start:window.End] {
		for _, operation := range pty.OperationRecords(transaction) {
			if operation.Kind == pty.OperationScrollRegionChange {
				inRestrictedScrollRegion = operation.Region.Bottom < analysis.Dimensions.Rows
				continue
			}
			if inRestrictedScrollRegion && operation.Write != nil {
				out = append(out, pty.AppendOperation{Operation: operation})
			}
		}
	}
	return coalesceCompleteAppendRows(out)
}

type logicalAppendRow struct {
	segments []pty.AppendOperation
}

// A logical append row is all ordered semantic writes that land on one terminal
// row at or below the immutable boundary. A row can contain differently styled
// segments, so it must never be represented by one synthetic write payload.
func classifyLogicalAppendRows(analysis pty.Analysis, window pty.OperationWindow, immutableBoundary int) []logicalAppendRow {
	if window.Start < 0 || window.End < window.Start || window.End > len(analysis.Operations) {
		panic(fmt.Sprintf("invalid operation window: window=%+v operation_count=%d", window, len(analysis.Operations)))
	}
	appends := make([]pty.AppendOperation, 0)
	for _, transaction := range analysis.Operations[window.Start:window.End] {
		for _, operation := range pty.OperationRecords(transaction) {
			if operation.Kind != pty.OperationWrite || operation.Region.Top < immutableBoundary {
				continue
			}
			if operation.Write == nil {
				panic(fmt.Sprintf("append write operation missing payload: sequence=%d byte_range=%+v", operation.Sequence, operation.ByteRange))
			}
			appends = append(appends, pty.AppendOperation{Operation: operation})
		}
	}
	return coalesceCompleteAppendRows(appends)
}

func (row logicalAppendRow) text() string {
	var out strings.Builder
	for _, segment := range row.segments {
		if segment.Operation.Write == nil {
			panic(fmt.Sprintf("logical append row contains operation without write payload: sequence=%d", segment.Operation.Sequence))
		}
		out.WriteString(segment.Operation.Write.Text())
	}
	return out.String()
}

func coalesceCompleteAppendRows(appends []pty.AppendOperation) []logicalAppendRow {
	out := make([]logicalAppendRow, 0, len(appends))
	for _, appendOperation := range appends {
		current := appendOperation.Operation
		if current.Write == nil || len(out) == 0 {
			out = append(out, logicalAppendRow{segments: []pty.AppendOperation{appendOperation}})
			continue
		}
		previous := &out[len(out)-1]
		previousOperation := previous.segments[len(previous.segments)-1].Operation
		if previousOperation.Write == nil ||
			previousOperation.Region.Top != current.Region.Top ||
			previousOperation.Region.Bottom != current.Region.Bottom ||
			previousOperation.Region.Right != current.Region.Left {
			out = append(out, logicalAppendRow{segments: []pty.AppendOperation{appendOperation}})
			continue
		}
		previous.segments = append(previous.segments, appendOperation)
	}
	return out
}

func appendOperations(rows []logicalAppendRow) []pty.Operation {
	operations := make([]pty.Operation, 0)
	for _, row := range rows {
		for _, segment := range row.segments {
			operations = append(operations, segment.Operation)
		}
	}
	return operations
}

func contentAppendedExactlyOnce(appends []logicalAppendRow, content string) error {
	count := 0
	for _, row := range appends {
		if row.text() == content {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("content append count for %q = %d, want exactly 1", content, count)
	}
	return nil
}

func contentAppendedAtLeastOnce(appends []logicalAppendRow, content string) error {
	for _, row := range appends {
		if row.text() == content {
			return nil
		}
	}
	return fmt.Errorf("content append count for %q = 0, want at least 1; appends=%q", content, appendTexts(appends))
}

func contentNotAppended(appends []logicalAppendRow, content string) error {
	for _, row := range appends {
		if row.text() == content {
			return fmt.Errorf("content append for %q found", content)
		}
	}
	return nil
}

func appendTexts(appends []logicalAppendRow) []string {
	out := make([]string, 0, len(appends))
	for _, row := range appends {
		out = append(out, row.text())
	}
	return out
}

func screenRowAppearsExactlyOnce(screen pty.ScreenSnapshot, expected string) error {
	rows := make([]string, 0, len(screen.Cells))
	count := 0
	for _, cells := range screen.Cells {
		var row strings.Builder
		for _, cell := range cells {
			row.WriteString(cell.Content)
		}
		text := strings.TrimRight(row.String(), " ")
		if text != "" {
			rows = append(rows, text)
		}
		if text == expected {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("screen row count for %q = %d, want exactly 1; complete_rows=%q", expected, count, rows)
	}
	return nil
}

func firstOperationAtOrAfterByte(operations []pty.Operation, offset int64) int {
	for index, operation := range operations {
		if operation.ByteRange.Start >= offset {
			return index
		}
	}
	return len(operations)
}
