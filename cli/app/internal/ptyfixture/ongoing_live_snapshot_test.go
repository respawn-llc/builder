package ptyfixture

import (
	"fmt"
	"strings"
	"time"

	"core/cli/tui/transcriptrender"
	"core/internal/testharness/pty"
)

type liveSnapshotExpectation struct {
	At                    time.Duration
	PendingShell          livePendingShellExpectation
	Queued                liveStyledLineExpectation
	Steering              liveStyledLineExpectation
	CompletedShellCommand string
	ForbiddenCompleted    []string
}

type livePendingShellExpectation struct {
	Command string
}

type liveStyledLineExpectation struct {
	Text       string
	Foreground string
	Faint      bool
}

func assertLiveSnapshot(capture pty.Capture, expected liveSnapshotExpectation) error {
	snapshotCapture, err := captureThrough(capture, expected.At)
	if err != nil {
		return err
	}
	analysis, err := pty.Analyze(snapshotCapture)
	if err != nil {
		return fmt.Errorf("analyze capture through %s: %w", expected.At, err)
	}
	if err := pendingShellVisibleWithSemanticStyle(analysis.Screen, expected.PendingShell); err != nil {
		return err
	}
	if err := styledLineVisible(analysis.Screen, expected.Queued); err != nil {
		return fmt.Errorf("queued input: %w", err)
	}
	if err := styledLineVisible(analysis.Screen, expected.Steering); err != nil {
		return fmt.Errorf("steering input: %w", err)
	}
	return nil
}

func captureThrough(capture pty.Capture, at time.Duration) (pty.Capture, error) {
	chunks := make([]pty.Chunk, 0, len(capture.Chunks))
	for _, chunk := range capture.Chunks {
		if chunk.At > at {
			break
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		return pty.Capture{}, fmt.Errorf("capture has no terminal output through %s", at)
	}
	resizes := make([]pty.ResizeEvent, 0, len(capture.Resizes))
	for _, resize := range capture.Resizes {
		if resize.At <= at {
			resizes = append(resizes, resize)
		}
	}
	return pty.NewCaptureWithEvents(capture.Dimensions, chunks, resizes)
}

func pendingShellVisibleWithSemanticStyle(screen pty.ScreenSnapshot, expected livePendingShellExpectation) error {
	row, start, end, ok := screenRowContaining(screen, expected.Command)
	if !ok {
		return fmt.Errorf("pending shell command %q not visible; screen=%q", expected.Command, screen.RenderText())
	}
	var prefix strings.Builder
	for _, cell := range row[:start] {
		prefix.WriteString(cell.Content)
	}
	if trimmed := strings.TrimSpace(prefix.String()); trimmed == "" || trimmed == "$" {
		return fmt.Errorf("shell command %q is not shown as a pending row: prefix=%q", expected.Command, prefix.String())
	}
	commandCells := row[start:end]
	syntaxColors := map[string]struct{}{
		colorForStyle(transcriptrender.StyleRoleToolShellPrimary):   {},
		colorForStyle(transcriptrender.StyleRoleToolShellSecondary): {},
		colorForStyle(transcriptrender.StyleRoleToolShellWarning):   {},
		colorForStyle(transcriptrender.StyleRoleToolShellError):     {},
	}
	foundSyntax := false
	for _, cell := range commandCells {
		if cell.Content == "" {
			continue
		}
		if !cell.Faint {
			return fmt.Errorf("pending shell command cell is not faint: text=%q foreground=%q", cell.Content, cell.Foreground)
		}
		if _, ok := syntaxColors[cell.Foreground]; ok {
			foundSyntax = true
		}
	}
	if !foundSyntax {
		return fmt.Errorf("pending shell command has no semantic syntax color: command=%q", expected.Command)
	}
	return nil
}

func styledLineVisible(screen pty.ScreenSnapshot, expected liveStyledLineExpectation) error {
	row, start, end, ok := screenRowContaining(screen, expected.Text)
	if !ok {
		return fmt.Errorf("line %q not visible; screen=%q", expected.Text, screen.RenderText())
	}
	for _, cell := range row[start:end] {
		if cell.Content == "" {
			continue
		}
		if cell.Foreground != expected.Foreground || cell.Faint != expected.Faint {
			return fmt.Errorf(
				"line %q cell style = foreground %q faint=%t, want foreground %q faint=%t",
				expected.Text,
				cell.Foreground,
				cell.Faint,
				expected.Foreground,
				expected.Faint,
			)
		}
	}
	return nil
}

func committedShellIsOnlyCompletedCommandRow(screen pty.ScreenSnapshot, command string) error {
	want := "$ " + command
	count := 0
	for _, row := range screen.Cells {
		_, _, found := cellRangeContaining(row, command)
		if !found {
			continue
		}
		count++
		var visible strings.Builder
		for _, cell := range row {
			visible.WriteString(cell.Content)
		}
		if got := strings.TrimRight(visible.String(), " "); got != want {
			return fmt.Errorf("command row = %q, want committed input-first row %q", got, want)
		}
	}
	if count != 1 {
		return fmt.Errorf("completed command row count for %q = %d, want 1; screen=%q", command, count, screen.RenderText())
	}
	return nil
}

func committedToolInputLeadsOnlyCompletedRow(screen pty.ScreenSnapshot, input string, symbol string) error {
	wantPrefix := symbol + " " + input
	count := 0
	for _, row := range screen.Cells {
		_, _, found := cellRangeContaining(row, input)
		if !found {
			continue
		}
		count++
		var visible strings.Builder
		for _, cell := range row {
			visible.WriteString(cell.Content)
		}
		got := strings.TrimRight(visible.String(), " ")
		if got != wantPrefix && !strings.HasPrefix(got, wantPrefix+" ") {
			return fmt.Errorf("tool row = %q, want committed input-first prefix %q", got, wantPrefix)
		}
	}
	if count != 1 {
		return fmt.Errorf("completed tool row count for %q = %d, want 1; screen=%q", input, count, screen.RenderText())
	}
	return nil
}

func toolRowSymbolUsesRole(screen pty.ScreenSnapshot, body string, symbol string, role transcriptrender.StyleRole) error {
	row, start, _, ok := screenRowContaining(screen, body)
	if !ok {
		return fmt.Errorf("tool row body %q not visible; screen=%q", body, screen.RenderText())
	}
	expectedForeground := colorForStyle(role)
	for _, cell := range row[:start] {
		if cell.Content != symbol {
			continue
		}
		if cell.Foreground != expectedForeground || cell.Faint {
			return fmt.Errorf(
				"tool symbol %q before %q style = foreground %q faint=%t, want foreground %q faint=false",
				symbol,
				body,
				cell.Foreground,
				cell.Faint,
				expectedForeground,
			)
		}
		return nil
	}
	return fmt.Errorf("tool symbol %q not found before %q", symbol, body)
}

func backgroundCompletionVisibleWithSemanticStyle(screen pty.ScreenSnapshot) error {
	row, start, _, ok := screenRowContaining(screen, "Background shell ")
	if !ok {
		return fmt.Errorf("background completion row not visible; screen=%q", screen.RenderText())
	}
	expectedSymbol := liveStyledLineExpectation{
		Text:       "ℹ",
		Foreground: colorForStyle(transcriptrender.StyleRoleToolSuccess),
	}
	symbolFound := false
	for _, cell := range row[:start] {
		if cell.Content != expectedSymbol.Text {
			continue
		}
		symbolFound = true
		if cell.Foreground != expectedSymbol.Foreground || cell.Faint != expectedSymbol.Faint {
			return fmt.Errorf(
				"background completion symbol style = foreground %q faint=%t, want foreground %q faint=%t",
				cell.Foreground,
				cell.Faint,
				expectedSymbol.Foreground,
				expectedSymbol.Faint,
			)
		}
	}
	if !symbolFound {
		return fmt.Errorf("background completion symbol not found before body")
	}

	end := len(row)
	for end > start && strings.TrimSpace(row[end-1].Content) == "" {
		end--
	}
	expectedBody := liveStyledLineExpectation{
		Foreground: colorForStyle(transcriptrender.StyleRoleNoticeForegroundFaint),
		Faint:      true,
	}
	for _, cell := range row[start:end] {
		if cell.Content == "" {
			continue
		}
		if cell.Foreground != expectedBody.Foreground || cell.Faint != expectedBody.Faint {
			return fmt.Errorf(
				"background completion body cell %q style = foreground %q faint=%t, want foreground %q faint=%t",
				cell.Content,
				cell.Foreground,
				cell.Faint,
				expectedBody.Foreground,
				expectedBody.Faint,
			)
		}
	}
	return nil
}

func screenRowContaining(screen pty.ScreenSnapshot, text string) ([]pty.Cell, int, int, bool) {
	for _, row := range screen.Cells {
		if start, end, ok := cellRangeContaining(row, text); ok {
			return row, start, end, true
		}
	}
	return nil, 0, 0, false
}

func cellRangeContaining(row []pty.Cell, text string) (int, int, bool) {
	for start := range row {
		var visible strings.Builder
		for end := start; end < len(row); end++ {
			visible.WriteString(row[end].Content)
			candidate := visible.String()
			if candidate == text {
				return start, end + 1, true
			}
			if len(candidate) >= len(text) {
				break
			}
		}
	}
	return 0, 0, false
}
