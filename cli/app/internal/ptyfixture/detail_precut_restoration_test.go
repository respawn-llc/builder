package ptyfixture

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"core/cli/tui/transcriptrender"
	"core/internal/testharness/pty"
	"core/shared/theme"
)

func TestDetailPrecutRestorationPTY(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("CI", "")

	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelBuild()

	bin := buildPTYFixtureBinary(t, buildCtx)

	scenarioCtx, cancelScenario := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelScenario()
	completionDrain := 45 * time.Second
	inputPlan := newDetailPrecutFramePlan()
	capture, _ := runPTYFixtureScenarioWithInputPlan(
		t,
		scenarioCtx,
		bin,
		"detail_precut_restoration",
		map[string]any{
			"seed_transcript": detailPrecutSeedTranscript(),
			"final":           "PTY_DETAIL_FINAL",
		},
		nil,
		ptyFixtureInputPlan{frameSequences: []pty.FrameInputSequence{inputPlan.sequence()}},
		nil,
		&completionDrain,
	)
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("analyze capture: %v", err)
	}
	writeDetailPrecutRestorationCaptureProof(t, capture, analysis)

	checkpoints := collectDetailPrecutFrameCheckpoints(t, capture, inputPlan)
	assertDetailTerminalModeWindows(t, analysis, 1)
	assertNewestDetailSelection(t, checkpoints.initialNewest)
	assertSelectedShellDetail(t, checkpoints.selectedShell)
	assertSelectedMarkdownDetail(t, checkpoints.selectedMarkdown)
	assertOngoingScreenRestored(t, capture, analysis)
	writeDetailPrecutRestorationProof(t, capture, analysis, checkpoints.proofScreens())
}

func detailPrecutSeedTranscript() []map[string]any {
	entries := make([]map[string]any, 0, 10)
	for index := 0; index < 4; index++ {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		entries = append(entries, map[string]any{
			"kind": "message",
			"role": role,
			"text": fmt.Sprintf("PTY_DETAIL_FILL_%02d", index),
		})
	}
	for index := 0; index < 4; index++ {
		entries = append(entries, map[string]any{
			"kind":           "message",
			"role":           "assistant",
			"condensed_text": fmt.Sprintf("PTY_MD_COMPACT_%02d", index),
			"text": fmt.Sprintf(
				"# PTY_MD_HEADING_%02d\n\n**PTY_MD_BOLD_%02d** *PTY_MD_ITALIC_%02d* [PTY_MD_LINK_%02d](https://example.com) `PTY_MD_CODE_%02d`\n\nPTY_MD_TAIL_%02d",
				index,
				index,
				index,
				index,
				index,
				index,
			),
		})
	}
	for index := 0; index < 2; index++ {
		entry := toolSeed(
			"exec_command",
			fmt.Sprintf("30e702a9-86c4-4e7e-9f50-2f59e6f64f%02d", index),
			map[string]any{"cmd": fmt.Sprintf("if [ -n \"$HOME\" ]; then printf '%%s' \"PTY_SHELL_%02d\"; fi # comment %02d", index, index)},
			fmt.Sprintf("PTY_SHELL_COMPACT_%02d", index),
			false,
		)
		entry["tool_summary"] = fmt.Sprintf("PTY_SHELL_RESULT_%02d", index)
		entries = append(entries, entry)
	}
	return entries
}

func assertDetailTerminalModeWindows(t *testing.T, analysis pty.Analysis, want int) {
	t.Helper()
	altEnabled, altDisabled := 0, 0
	scrollEnabled, scrollDisabled := 0, 0
	mouseCaptureEnabled := false
	for _, change := range analysis.PrivateModeChanges {
		switch change.Mode {
		case 1049:
			if change.Enabled {
				altEnabled++
			} else {
				altDisabled++
			}
		case 1007:
			if change.Enabled {
				scrollEnabled++
			} else {
				scrollDisabled++
			}
		case 1000, 1002, 1003, 1006:
			mouseCaptureEnabled = mouseCaptureEnabled || change.Enabled
		}
	}
	if altEnabled != want || altDisabled != want {
		t.Fatalf("alternate-screen windows = enabled %d disabled %d, want %d/%d", altEnabled, altDisabled, want, want)
	}
	if scrollEnabled != want || scrollDisabled != want {
		t.Fatalf("alternate-scroll windows = enabled %d disabled %d, want %d/%d", scrollEnabled, scrollDisabled, want, want)
	}
	if mouseCaptureEnabled {
		t.Fatal("detail PTY enabled terminal mouse capture")
	}
}

func analyzeCapturePrefix(t *testing.T, capture pty.Capture, byteOffset int64) pty.Analysis {
	t.Helper()
	if len(capture.Resizes) != 0 {
		t.Fatalf("detail prefix analysis does not accept resized capture: %+v", capture.Resizes)
	}
	if byteOffset < 0 || byteOffset > int64(len(capture.Raw)) {
		t.Fatalf("capture prefix byte offset = %d, raw bytes = %d", byteOffset, len(capture.Raw))
	}
	var chunks []pty.Chunk
	if byteOffset > 0 {
		chunks = []pty.Chunk{pty.NewChunk(0, 0, capture.Raw[:byteOffset])}
	}
	prefix, err := pty.NewCapture(capture.Dimensions, chunks)
	if err != nil {
		t.Fatalf("create capture prefix: %v", err)
	}
	analysis, err := pty.Analyze(prefix)
	if err != nil {
		t.Fatalf("analyze capture prefix ending at %d: %v", byteOffset, err)
	}
	return analysis
}

func assertNewestDetailSelection(t *testing.T, screen pty.ScreenSnapshot) {
	t.Helper()
	rows, background := assertContinuousSelectedLens(t, screen)
	contentRows := selectedContentRows(screen, rows)
	if len(contentRows) != 1 {
		t.Fatalf("newest selected content rows = %v, want one compact row", contentRows)
	}
	if got, want := contentRows[0], screen.Dimensions.Rows-2; got != want {
		t.Fatalf("newest selected row = %d, want bottom chat row %d", got, want)
	}
	if got := screen.Cells[contentRows[0]][1].Content; got != transcriptrender.AssistantSymbol {
		t.Fatalf(
			"newest non-expandable selected symbol = %q, want assistant role symbol %q",
			got,
			transcriptrender.AssistantSymbol,
		)
	}
	assertSelectedVisualSpacer(t, screen, rows, background)
}

func assertSelectedShellDetail(t *testing.T, screen pty.ScreenSnapshot) {
	t.Helper()
	rows, background := assertContinuousSelectedLens(t, screen)
	contentRows := selectedContentRows(screen, rows)
	if len(contentRows) == 0 {
		t.Fatal("selected shell has no content row")
	}
	if got := screen.Cells[contentRows[0]][1].Content; got != transcriptrender.DetailCollapsedAffordance {
		t.Fatalf(
			"selected collapsed shell affordance = %q, want %q: row=%q",
			got,
			transcriptrender.DetailCollapsedAffordance,
			screen.TextInRegion(pty.Region{Top: contentRows[0], Bottom: contentRows[0] + 1, Left: 0, Right: screen.Dimensions.Cols}),
		)
	}
	foregrounds := make(map[string]struct{})
	faintSemanticCells := 0
	for _, row := range contentRows {
		for col := 1; col < screen.Dimensions.Cols; col++ {
			cell := screen.Cells[row][col]
			if cell.Content == "" || cell.Content == " " {
				continue
			}
			if cell.Faint {
				faintSemanticCells++
			}
			if cell.Foreground != "" {
				foregrounds[cell.Foreground] = struct{}{}
			}
		}
	}
	if faintSemanticCells == 0 {
		t.Fatalf(
			"selected shell lost faint syntax styling: foregrounds=%d row=%q",
			len(foregrounds),
			screen.TextInRegion(pty.Region{Top: contentRows[0], Bottom: contentRows[0] + 1, Left: 0, Right: screen.Dimensions.Cols}),
		)
	}
	if len(foregrounds) < 3 {
		t.Fatalf("selected shell foreground roles = %d, want at least 3", len(foregrounds))
	}
	assertSelectedVisualSpacer(t, screen, rows, background)
}

func assertSelectedMarkdownDetail(t *testing.T, screen pty.ScreenSnapshot) {
	t.Helper()
	rows, background := assertContinuousSelectedLens(t, screen)
	contentRows := selectedContentRows(screen, rows)
	if len(contentRows) < 4 {
		t.Fatalf(
			"expanded Markdown selected rows = %v, want multiline content: selected=%q",
			contentRows,
			screenRowTexts(screen, contentRows),
		)
	}
	if got := screen.Cells[contentRows[0]][1].Content; got != transcriptrender.DetailExpandedAffordance {
		t.Fatalf(
			"selected expanded Markdown affordance = %q, want %q",
			got,
			transcriptrender.DetailExpandedAffordance,
		)
	}
	foregrounds := make(map[string]struct{})
	hasBold, hasItalic, hasUnderline, hasFaintGuide := false, false, false, false
	for _, row := range contentRows {
		for col := 1; col < screen.Dimensions.Cols; col++ {
			cell := screen.Cells[row][col]
			if cell.Content == "" || cell.Content == " " {
				continue
			}
			hasBold = hasBold || cell.Bold
			hasItalic = hasItalic || cell.Italic
			hasUnderline = hasUnderline || cell.Underline
			if (cell.Content == transcriptrender.DetailContinuationGuide ||
				cell.Content == transcriptrender.DetailContinuationClosingGuide) &&
				cell.Faint {
				hasFaintGuide = true
			}
			if cell.Foreground != "" {
				foregrounds[cell.Foreground] = struct{}{}
			}
		}
	}
	if !hasBold || !hasItalic || !hasUnderline || !hasFaintGuide {
		t.Fatalf(
			"selected Markdown attributes = bold %t italic %t underline %t faint-guide %t",
			hasBold,
			hasItalic,
			hasUnderline,
			hasFaintGuide,
		)
	}
	if len(foregrounds) < 2 {
		t.Fatalf("selected Markdown foreground roles = %d, want foreground and primary roles", len(foregrounds))
	}
	assertSelectedVisualSpacer(t, screen, rows, background)
}

func assertContinuousSelectedLens(t *testing.T, screen pty.ScreenSnapshot) ([]int, string) {
	t.Helper()
	rows := make([]int, 0)
	selectedBackground := ""
	for row, cells := range screen.Cells {
		if len(cells) == 0 || cells[0].Content != theme.SelectionRailGlyph {
			continue
		}
		rows = append(rows, row)
		if cells[0].Background == "" {
			t.Fatalf("selected rail row %d has no background", row)
		}
		if selectedBackground == "" {
			selectedBackground = cells[0].Background
		}
		if cells[0].Background != selectedBackground {
			t.Fatalf("selected rail row %d background = %q, want %q", row, cells[0].Background, selectedBackground)
		}
		for col, cell := range cells {
			if cell.Background != selectedBackground {
				left := max(0, col-2)
				right := min(len(cells), col+3)
				t.Fatalf(
					"selected row %d cell %d background = %q, want continuous fill %q: nearby=%+v row=%q",
					row,
					col,
					cell.Background,
					selectedBackground,
					cells[left:right],
					screen.TextInRegion(pty.Region{Top: row, Bottom: row + 1, Left: 0, Right: screen.Dimensions.Cols}),
				)
			}
		}
	}
	if len(rows) == 0 {
		t.Fatalf(
			"detail screen has no selected rail rows: cursor=%+v nonblank_rows=%v",
			screen.Cursor,
			nonBlankScreenRows(screen),
		)
	}
	return rows, selectedBackground
}

func selectedContentRows(screen pty.ScreenSnapshot, selectedRows []int) []int {
	rows := make([]int, 0, len(selectedRows))
	for _, row := range selectedRows {
		for col := 1; col < screen.Dimensions.Cols; col++ {
			content := screen.Cells[row][col].Content
			if content != "" && content != " " {
				rows = append(rows, row)
				break
			}
		}
	}
	return rows
}

func screenRowTexts(screen pty.ScreenSnapshot, rows []int) []string {
	texts := make([]string, 0, len(rows))
	for _, row := range rows {
		texts = append(texts, screen.TextInRegion(pty.Region{
			Top:    row,
			Bottom: row + 1,
			Left:   0,
			Right:  screen.Dimensions.Cols,
		}))
	}
	return texts
}

func nonBlankScreenRows(screen pty.ScreenSnapshot) []int {
	rows := make([]int, 0)
	for row, cells := range screen.Cells {
		for _, cell := range cells {
			if cell.Content == "" || cell.Content == " " {
				continue
			}
			rows = append(rows, row)
			break
		}
	}
	return rows
}

func assertSelectedVisualSpacer(t *testing.T, screen pty.ScreenSnapshot, selectedRows []int, background string) {
	t.Helper()
	for _, row := range selectedRows {
		hasContent := false
		for col := 1; col < screen.Dimensions.Cols; col++ {
			cell := screen.Cells[row][col]
			if cell.Background != background {
				t.Fatalf("selected spacer candidate row %d cell %d background = %q, want %q", row, col, cell.Background, background)
			}
			hasContent = hasContent || (cell.Content != "" && cell.Content != " ")
		}
		if !hasContent {
			return
		}
	}
	t.Fatal("detail selection has no visual spacer row")
}

func assertOngoingScreenRestored(t *testing.T, capture pty.Capture, analysis pty.Analysis) {
	t.Helper()
	var firstEnable *pty.Operation
	var lastDisable *pty.Operation
	for index := range analysis.Operations {
		operation := &analysis.Operations[index]
		if operation.Kind != pty.OperationModeChange ||
			operation.PrivateMode == nil ||
			operation.PrivateMode.Mode != 1049 {
			continue
		}
		if operation.PrivateMode.Enabled && firstEnable == nil {
			firstEnable = operation
		}
		if !operation.PrivateMode.Enabled {
			lastDisable = operation
		}
	}
	if firstEnable == nil || lastDisable == nil {
		t.Fatal("detail alternate-screen boundaries missing")
	}
	before := analyzeCapturePrefix(t, capture, firstEnable.ByteRange.Start).Screen
	after := analyzeCapturePrefix(t, capture, lastDisable.ByteRange.End).Screen
	protectedRows := max(1, before.Dimensions.Rows-3)
	protectedCells := 0
	for row := 0; row < protectedRows; row++ {
		for col := 0; col < before.Dimensions.Cols; col++ {
			if before.Cells[row][col].Content == "" || before.Cells[row][col].Content == " " {
				continue
			}
			protectedCells++
			if before.Cells[row][col] != after.Cells[row][col] {
				t.Fatalf(
					"ongoing cell changed after detail roundtrip at row=%d col=%d: before=%+v after=%+v",
					row,
					col,
					before.Cells[row][col],
					after.Cells[row][col],
				)
			}
		}
	}
	if protectedCells == 0 {
		t.Fatal("ongoing restoration check found no visible protected cells")
	}
}

func writeDetailPrecutRestorationProof(t *testing.T, capture pty.Capture, analysis pty.Analysis, proofs []detailPrecutProofScreen) {
	t.Helper()
	writeDetailPrecutRestorationCaptureProof(t, capture, analysis)
	proofDir := detailPrecutRestorationProofDir(t)
	for _, proof := range proofs {
		snapshotCapture, err := pty.NewCapture(proof.screen.Dimensions, nil)
		if err != nil {
			t.Fatalf("create detail proof snapshot %q: %v", proof.name, err)
		}
		snapshotCapture.ReadLoopDone = true
		snapshotAnalysis := pty.Analysis{
			Dimensions: proof.screen.Dimensions,
			Screen:     proof.screen,
		}
		if err := pty.WriteArtifacts(filepath.Join(proofDir, proof.name), snapshotCapture, snapshotAnalysis, nil); err != nil {
			t.Fatalf("write detail proof snapshot %q: %v", proof.name, err)
		}
	}
}

func writeDetailPrecutRestorationCaptureProof(t *testing.T, capture pty.Capture, analysis pty.Analysis) {
	t.Helper()
	proofDir := detailPrecutRestorationProofDir(t)
	if err := pty.WriteArtifacts(filepath.Join(proofDir, "capture"), capture, analysis, nil); err != nil {
		t.Fatalf("write detail PTY capture proof: %v", err)
	}
}

func detailPrecutRestorationProofDir(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root for detail proof: %v", err)
	}
	return filepath.Join(repoRoot, ".kent", "qa", "proofs", "KENT-196-detail-precut-restoration")
}
