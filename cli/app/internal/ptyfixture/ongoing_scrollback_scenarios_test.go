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
	"core/internal/testharness/pty/appfixture"
	"core/shared/clientui"
	"core/shared/theme"
	"core/shared/transcript"
)

const (
	detailFrameShiftTab = "\x1b[Z"
	detailFrameTab      = "\t"
	detailFrameUp       = "\x1b[A"
)

func TestOngoingNativeScrollbackPTYScenarios(t *testing.T) {
	buildCtx, cancel := context.WithTimeout(context.Background(), ptyFixtureTestTimeout)
	defer cancel()

	bin := buildPTYFixtureBinary(t, buildCtx)
	modelMismatchCompletionDrain := time.Second

	for _, tc := range []struct {
		name                      string
		script                    map[string]any
		env                       []string
		inputs                    []pty.InputEvent
		frameInputs               []pty.FrameInputSequence
		resizes                   []pty.DriverResizeEvent
		frameResizes              []pty.FrameResizeEvent
		expectedAppends           []string
		expectedScrollbackAppends []string
		expectedAnyAppends        []string
		forbiddenAnyAppends       []string
		expectedScreenRows        []string
		expectedWarningAppends    int
		expectedDetailWarnings    int
		assertDetailWarnings      bool
		allowsAltScroll           bool
		allowsFullScreen          bool
		completionInFrameSequence bool
		completionDrain           *time.Duration
	}{
		{
			name: "provider_model_mismatch_hidden_from_normal_ongoing",
			env:  []string{"KENT_DEBUG=0"},
			script: map[string]any{
				"prompt":       "normal model mismatch",
				"served_model": "served-model",
				"final":        "normal mismatch answer",
			},
			frameInputs: []pty.FrameInputSequence{{
				Phase: pty.PhaseScenarioComplete,
				Inputs: []pty.FrameInput{
					{
						Readiness:  pty.ReadinessRendererFrame,
						AfterPhase: phasePointer(pty.PhaseScenarioFinalApplied),
						Bytes:      []byte(detailFrameShiftTab),
					},
					{
						Readiness:  pty.ReadinessRendererFrame,
						AfterPhase: phasePointer(pty.PhaseDetailInitialPageApplied),
						Bytes:      []byte(detailFrameUp),
					},
					{Readiness: pty.ReadinessRendererFrame, Bytes: []byte(detailFrameTab)},
					{Readiness: pty.ReadinessNormalBufferRestored, Bytes: []byte{0x03, 0x03}},
				},
			}},
			expectedAppends:           []string{transcriptrender.AssistantSymbol + " normal mismatch answer"},
			expectedDetailWarnings:    1,
			assertDetailWarnings:      true,
			allowsAltScroll:           true,
			allowsFullScreen:          true,
			completionInFrameSequence: true,
		},
		{
			name: "provider_model_mismatch_visible_in_debug_ongoing",
			env:  []string{"KENT_DEBUG=1"},
			script: map[string]any{
				"prompt":       "debug model mismatch",
				"served_model": "served-model",
				"final":        "debug mismatch answer",
			},
			expectedAppends: []string{
				transcriptrender.AssistantSymbol + " debug mismatch answer",
			},
			expectedWarningAppends: 1,
			completionDrain:        &modelMismatchCompletionDrain,
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
			frameInputs: []pty.FrameInputSequence{{
				Phase: pty.PhaseToolStarted,
				Inputs: []pty.FrameInput{{
					Readiness: pty.ReadinessRendererFrame,
					Bytes:     []byte("\r"),
				}},
			}},
			expectedAppends:     []string{"❮ question lifecycle complete"},
			forbiddenAnyAppends: []string{"? PTY_LIVE_QUESTION", "? tool call"},
			completionDrain:     &modelMismatchCompletionDrain,
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
								"input": map[string]any{"cmd": "sleep 1; echo $((42424241+1))"},
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
			frameInputs: []pty.FrameInputSequence{{
				Phase: pty.PhaseToolStarted,
				Inputs: []pty.FrameInput{
					{Readiness: pty.ReadinessRendererFrame, Bytes: []byte("queued after tool start")},
					{Readiness: pty.ReadinessInputApplied, Bytes: []byte("\t")},
					{Readiness: pty.ReadinessInputApplied, Bytes: []byte("steering after tool start")},
					{Readiness: pty.ReadinessInputApplied, Bytes: []byte("\r")},
				},
			}},
			expectedAppends:    []string{"❮ live lifecycle complete", "❮ queued lifecycle complete"},
			expectedScreenRows: []string{"$ sleep 1; echo $((42424241+1))"},
			completionDrain:    &modelMismatchCompletionDrain,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			scenarioCtx, cancel := context.WithTimeout(context.Background(), ptyFixtureTestTimeout)
			defer cancel()
			capture, _ := runPTYFixtureScenarioWithInputPlan(
				t,
				scenarioCtx,
				bin,
				tc.name,
				tc.script,
				tc.env,
				ptyFixtureInputPlan{
					scheduled: tc.inputs, frameSequences: tc.frameInputs, frameResizes: tc.frameResizes,
					completionInFrameSequence: tc.completionInFrameSequence,
				},
				tc.resizes,
				tc.completionDrain,
			)
			if len(capture.Resizes) != len(tc.resizes)+len(tc.frameResizes) {
				t.Fatalf(
					"capture resize count = %d, want scheduled=%d frame-gated=%d",
					len(capture.Resizes),
					len(tc.resizes),
					len(tc.frameResizes),
				)
			}
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
			if got := countProviderModelMismatchAppendRows(allAppends); got != tc.expectedWarningAppends {
				t.Fatalf("provider-model mismatch append count = %d, want %d", got, tc.expectedWarningAppends)
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
			if tc.assertDetailWarnings {
				assertScenarioDetailWarningRows(t, capture, tc.expectedDetailWarnings)
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

type ptyFixtureInputPlan struct {
	scheduled                 []pty.InputEvent
	frameSequences            []pty.FrameInputSequence
	frameResizes              []pty.FrameResizeEvent
	completionInFrameSequence bool
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
	if !inputPlan.completionInFrameSequence && len(inputPlan.frameResizes) == 0 {
		phaseInputs = append(phaseInputs, pty.PhaseInputEvent{
			Phase: completionPhase,
			After: completionDrain,
			Bytes: []byte{0x03, 0x03},
		})
	}
	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path:                bin,
		Args:                []string{appfixture.ProcessTestRunArgument},
		Env:                 append([]string(nil), env...),
		Dimensions:          pty.MustDimensions(24, 80),
		PhaseInputs:         phaseInputs,
		FrameInputSequences: inputPlan.frameSequences,
		FrameResizes:        inputPlan.frameResizes,
		Resizes:             resizes,
		Timeout:             5 * time.Second,
	})
	if err != nil {
		t.Fatalf("run fixture: %v raw=%q", err, string(capture.Raw))
	}
	return capture, observationsPath
}

func assertScenarioDetailWarningRows(t *testing.T, capture pty.Capture, expected int) {
	t.Helper()
	if len(capture.FrameInputDispatches) < 4 {
		t.Fatalf("detail frame input dispatches = %d, want at least 4", len(capture.FrameInputDispatches))
	}
	exitDispatch := capture.FrameInputDispatches[2]
	screens, err := pty.ReplayCheckpointScreens(capture, []pty.ReplayCheckpoint{{
		ByteOffset: exitDispatch.ReadyBoundaryEndByteOffset,
	}})
	if err != nil {
		t.Fatalf("replay detail screen: %v", err)
	}
	if got := countProviderModelMismatchScreenRows(screens[0]); got != expected {
		t.Fatalf("detail warning row count = %d, want %d", got, expected)
	}
}

func phasePointer(phase pty.PhaseKind) *pty.PhaseKind {
	return &phase
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

func countProviderModelMismatchAppendRows(appends []logicalAppendRow) int {
	warningColor := transcriptrender.ColorForRole(transcriptrender.ColorRoleWarning, "dark")
	expectedText := providerModelMismatchRenderedText(transcriptrender.ModeOngoingStable)
	count := 0
	for _, row := range appends {
		if row.text() != expectedText {
			continue
		}
		for _, segment := range row.segments {
			write := segment.Operation.Write
			if write != nil && colorMatches(write.Foreground, warningColor) {
				count++
				break
			}
		}
	}
	return count
}

func countProviderModelMismatchScreenRows(screen pty.ScreenSnapshot) int {
	expectedText := " " + providerModelMismatchRenderedText(transcriptrender.ModeOngoingStable)
	count := 0
	for _, row := range screen.Cells {
		var text strings.Builder
		for _, cell := range row {
			text.WriteString(cell.Content)
		}
		if strings.TrimRight(text.String(), " ") == expectedText {
			count++
		}
	}
	return count
}

func providerModelMismatchRenderedText(mode transcriptrender.Mode) string {
	return transcriptrender.RenderCommittedRow(clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing, Integrity: transcript.RowIntegrityValid, Kind: clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{Reason: clientui.TranscriptNoticeProviderModelMismatch, Severity: clientui.TranscriptNoticeWarning,
			ProviderModelMismatch: &transcript.ProviderModelMismatchNotice{RequestedModel: "gpt-5", ServedModel: "served-model"}},
	}, 80, "dark", mode).Lines[0].Plain()
}

func colorMatches(actual string, expected theme.Color) bool {
	return strings.EqualFold(actual, expected.ANSI) ||
		strings.EqualFold(actual, expected.ANSI256) ||
		strings.EqualFold(actual, expected.TrueColor)
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

func countScreenRowsWithCell(screen pty.ScreenSnapshot, expected string) int {
	count := 0
	for _, row := range screen.Cells {
		for _, cell := range row {
			if cell.Content == expected {
				count++
				break
			}
		}
	}
	return count
}

func firstOperationAtOrAfterByte(operations []pty.Operation, offset int64) int {
	for index, operation := range operations {
		if operation.ByteRange.Start >= offset {
			return index
		}
	}
	return len(operations)
}
