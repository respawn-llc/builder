package analyzer_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"
)

func TestAnalyzePrintableChunkRecordsScreenAndWriteOperation(t *testing.T) {
	t.Parallel()

	capture, err := pty.NewCapture(
		pty.MustDimensions(3, 10),
		[]pty.Chunk{pty.NewChunk(0, time.Millisecond, []byte("hello"))},
	)
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}

	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if got := analysis.Screen.TextInRegion(pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 5}); got != "hello" {
		t.Fatalf("screen text = %q, want %q", got, "hello")
	}
	if len(analysis.Operations) != 1 {
		t.Fatalf("operation count = %d, want 1: %#v", len(analysis.Operations), analysis.Operations)
	}
	op := analysis.Operations[0]
	if op.Kind != pty.OperationWrite {
		t.Fatalf("operation kind = %v, want %v", op.Kind, pty.OperationWrite)
	}
	if op.Region != (pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 5}) {
		t.Fatalf("operation region = %#v, want first row columns [0,5)", op.Region)
	}
	if op.Write == nil || op.Write.Text != "hello" {
		t.Fatalf("operation write payload = %#v, want text %q", op.Write, "hello")
	}
	if op.ByteRange != (pty.ByteRange{Start: 0, End: 5}) {
		t.Fatalf("operation byte range = %#v, want [0,5)", op.ByteRange)
	}
	if op.ChunkIndex != 0 {
		t.Fatalf("operation chunk index = %d, want 0", op.ChunkIndex)
	}
}

func TestAnalyzePersistsCellStyleFacts(t *testing.T) {
	t.Parallel()

	analysis := analyzeBytes(t, []byte(
		"\x1b[1;3;4;38;2;17;34;51;48;2;68;85;102mB\x1b[0m"+
			"\x1b[2;38;2;17;34;51;48;2;68;85;102mF\x1b[0m",
	), pty.MustDimensions(2, 4))

	boldCell := analysis.Screen.Cells[0][0]
	if boldCell.Content != "B" {
		t.Fatalf("bold cell content = %q, want B", boldCell.Content)
	}
	if boldCell.Foreground == "" || boldCell.Background == "" || boldCell.Foreground == boldCell.Background {
		t.Fatalf("bold cell colors = foreground %q background %q, want distinct terminal colors", boldCell.Foreground, boldCell.Background)
	}
	if !boldCell.Bold || boldCell.Faint || !boldCell.Italic || !boldCell.Underline {
		t.Fatalf("bold cell attributes = bold=%t faint=%t italic=%t underline=%t", boldCell.Bold, boldCell.Faint, boldCell.Italic, boldCell.Underline)
	}

	faintCell := analysis.Screen.Cells[0][1]
	if faintCell.Content != "F" || !faintCell.Faint || faintCell.Bold {
		t.Fatalf("faint cell = content %q bold=%t faint=%t", faintCell.Content, faintCell.Bold, faintCell.Faint)
	}

	var boldWrite *pty.WritePayload
	var faintWrite *pty.WritePayload
	for _, operation := range analysis.Operations {
		if operation.Kind != pty.OperationWrite || operation.Write == nil {
			continue
		}
		switch operation.Write.Text {
		case "B":
			boldWrite = operation.Write
		case "F":
			faintWrite = operation.Write
		}
	}
	if boldWrite == nil || faintWrite == nil {
		t.Fatalf("styled write operations missing: bold=%#v faint=%#v", boldWrite, faintWrite)
	}
	if boldWrite.Foreground != boldCell.Foreground || boldWrite.Background != boldCell.Background {
		t.Fatalf("bold write colors = foreground %q background %q, want cell colors %q/%q", boldWrite.Foreground, boldWrite.Background, boldCell.Foreground, boldCell.Background)
	}
	if !boldWrite.Bold || boldWrite.Faint || !boldWrite.Italic || !boldWrite.Underline {
		t.Fatalf("bold write attributes = bold=%t faint=%t italic=%t underline=%t", boldWrite.Bold, boldWrite.Faint, boldWrite.Italic, boldWrite.Underline)
	}
	if !faintWrite.Faint || faintWrite.Bold {
		t.Fatalf("faint write attributes = bold=%t faint=%t", faintWrite.Bold, faintWrite.Faint)
	}
}

func TestAnalyzeEmptyCapturePreservesBlankScreen(t *testing.T) {
	t.Parallel()

	capture, err := pty.NewCapture(pty.MustDimensions(2, 4), nil)
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}

	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(capture.Chunks) != 0 || len(capture.Raw) != 0 {
		t.Fatalf("capture should stay empty, got chunks=%#v raw=%#v", capture.Chunks, capture.Raw)
	}
	if len(analysis.Operations) != 0 {
		t.Fatalf("operation count = %d, want 0: %#v", len(analysis.Operations), analysis.Operations)
	}
	if !analysis.Screen.IsBlank() {
		t.Fatalf("screen should be blank: %#v", analysis.Screen)
	}
}

func TestNewCaptureRejectsAfterChunkResizeForEmptyCapture(t *testing.T) {
	t.Parallel()

	_, err := pty.NewCaptureWithEvents(
		pty.MustDimensions(2, 4),
		nil,
		[]pty.ResizeEvent{{Placement: pty.AfterChunk(0), At: time.Millisecond, Dimensions: pty.MustDimensions(3, 4)}},
	)
	if err == nil {
		t.Fatal("NewCaptureWithEvents succeeded for after-chunk resize without chunks")
	}
}

func TestAnalyzeCursorMovementRecordsTypedOperation(t *testing.T) {
	t.Parallel()

	analysis := analyzeBytes(t, []byte("a\x1b[2;4Hb"), pty.MustDimensions(3, 10))

	requireOperation(t, analysis, pty.OperationCursorMove, pty.Region{Top: 1, Bottom: 2, Left: 3, Right: 4})
	if got := analysis.Screen.TextInRegion(pty.Region{Top: 1, Bottom: 2, Left: 3, Right: 4}); got != "b" {
		t.Fatalf("moved write cell = %q, want %q", got, "b")
	}
}

func TestAnalyzeEraseCommandsRecordTypedRegions(t *testing.T) {
	t.Parallel()

	analysis := analyzeBytes(t, []byte("abcdef\x1b[1;3H\x1b[K\x1b[2J"), pty.MustDimensions(3, 6))

	requireOperation(t, analysis, pty.OperationErase, pty.Region{Top: 0, Bottom: 1, Left: 2, Right: 6})
	requireOperation(t, analysis, pty.OperationErase, pty.Region{Top: 0, Bottom: 3, Left: 0, Right: 6})
}

func TestAnalyzeScrollRegionChangeRecordsTypedOperation(t *testing.T) {
	t.Parallel()

	analysis := analyzeBytes(t, []byte("\x1b[2;4r"), pty.MustDimensions(5, 8))

	requireOperation(t, analysis, pty.OperationScrollRegionChange, pty.Region{Top: 1, Bottom: 4, Left: 0, Right: 8})
}

func TestAnalyzePrivateMode1007RecordsDiagnostic(t *testing.T) {
	t.Parallel()

	analysis := analyzeBytes(t, []byte("\x1b[?1007h\x1b[?1007l"), pty.MustDimensions(2, 8))

	if len(analysis.PrivateModeChanges) != 2 {
		t.Fatalf("private mode change count = %d, want 2: %#v", len(analysis.PrivateModeChanges), analysis.PrivateModeChanges)
	}
	if got := analysis.PrivateModeChanges[0]; got.Mode != 1007 || !got.Enabled {
		t.Fatalf("first private mode change = %#v, want enabled 1007", got)
	}
	if got := analysis.PrivateModeChanges[1]; got.Mode != 1007 || got.Enabled {
		t.Fatalf("second private mode change = %#v, want disabled 1007", got)
	}
	modeOps := 0
	for _, operation := range analysis.Operations {
		if operation.Kind == pty.OperationModeChange {
			modeOps++
			if operation.PrivateMode == nil || operation.PrivateMode.Mode != 1007 {
				t.Fatalf("mode operation = %#v", operation)
			}
		}
	}
	if modeOps != 2 {
		t.Fatalf("mode operation count = %d, want 2 in operation log", modeOps)
	}
}

func TestAnalyzeDECPrivateModesRecordTypedOperations(t *testing.T) {
	t.Parallel()

	analysis := analyzeBytes(t, []byte("\x1b[?1049h\x1b[?1000;1006h\x1b[?25l"), pty.MustDimensions(2, 8))
	want := []struct {
		mode    int
		enabled bool
	}{
		{mode: 1049, enabled: true},
		{mode: 1000, enabled: true},
		{mode: 1006, enabled: true},
		{mode: 25, enabled: false},
	}
	modeOps := make([]pty.PrivateModeChange, 0)
	for _, operation := range analysis.Operations {
		if operation.Kind == pty.OperationModeChange && operation.PrivateMode != nil {
			modeOps = append(modeOps, *operation.PrivateMode)
		}
	}
	if len(modeOps) != len(want) {
		t.Fatalf("mode operations = %#v, want %d entries", modeOps, len(want))
	}
	for i, expected := range want {
		if modeOps[i].Mode != expected.mode || modeOps[i].Enabled != expected.enabled {
			t.Fatalf("mode operation %d = %#v, want mode=%d enabled=%v", i, modeOps[i], expected.mode, expected.enabled)
		}
	}
}

func TestAnalyzeAlternateScreenRestoresNormalBuffer(t *testing.T) {
	t.Parallel()

	analysis := analyzeBytes(
		t,
		[]byte("normal\x1b[?1049h\x1b[2J\x1b[Halternate\x1b[?1049l"),
		pty.MustDimensions(2, 12),
	)
	if got := analysis.Screen.TextInRegion(pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 6}); got != "normal" {
		t.Fatalf("restored normal buffer = %q, want %q", got, "normal")
	}
}

func TestScreenSnapshotReportsBlankFrame(t *testing.T) {
	t.Parallel()

	blank := pty.NewScreenSnapshot(pty.MustDimensions(2, 4))
	if !blank.IsBlank() {
		t.Fatal("new screen snapshot should be blank")
	}
	written := analyzeBytes(t, []byte("x"), pty.MustDimensions(2, 4)).Screen
	if written.IsBlank() {
		t.Fatal("screen with content should not be blank")
	}
}

func TestAnalyzeResizeEventRecordsTypedOperation(t *testing.T) {
	t.Parallel()

	capture, err := pty.NewCaptureWithEvents(
		pty.MustDimensions(2, 4),
		[]pty.Chunk{pty.NewChunk(0, time.Millisecond, []byte("a"))},
		[]pty.ResizeEvent{{Placement: pty.AfterChunk(0), At: 2 * time.Millisecond, Dimensions: pty.MustDimensions(3, 6)}},
	)
	if err != nil {
		t.Fatalf("NewCaptureWithEvents: %v", err)
	}
	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	requireOperation(t, analysis, pty.OperationResize, pty.Region{Top: 0, Bottom: 3, Left: 0, Right: 6})
	if analysis.Screen.Dimensions != (pty.MustDimensions(3, 6)) {
		t.Fatalf("screen dimensions = %#v, want 3x6", analysis.Screen.Dimensions)
	}
}

func TestCaptureRejectsOutOfOrderResizePlacement(t *testing.T) {
	t.Parallel()

	_, err := pty.NewCaptureWithEvents(
		pty.MustDimensions(2, 4),
		[]pty.Chunk{pty.NewChunk(0, time.Millisecond, []byte("a"))},
		[]pty.ResizeEvent{
			{Placement: pty.AfterChunk(0), At: time.Millisecond, Dimensions: pty.MustDimensions(3, 6)},
			{Placement: pty.BeforeFirstChunk(), At: 2 * time.Millisecond, Dimensions: pty.MustDimensions(4, 6)},
		},
	)
	if err == nil {
		t.Fatal("NewCaptureWithEvents succeeded for out-of-order resize placement")
	}
}

func TestAnalyzeSplitEscapeSequencePreservesParserState(t *testing.T) {
	t.Parallel()

	capture, err := pty.NewCapture(
		pty.MustDimensions(3, 8),
		[]pty.Chunk{
			pty.NewChunk(0, time.Millisecond, []byte("\x1b[2;")),
			pty.NewChunk(1, 2*time.Millisecond, []byte("3Hx")),
		},
	)
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	requireOperation(t, analysis, pty.OperationCursorMove, pty.Region{Top: 1, Bottom: 2, Left: 2, Right: 3})
	if got := analysis.Screen.TextInRegion(pty.Region{Top: 1, Bottom: 2, Left: 2, Right: 3}); got != "x" {
		t.Fatalf("split cursor target cell = %q, want %q", got, "x")
	}
}

func TestAnalyzerDoesNotUseRegexOrSubstringTerminalDetection(t *testing.T) {
	t.Parallel()

	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob analyzer files: %v", err)
	}
	for _, path := range matches {
		if filepath.Base(path) == "analyze_test.go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imported := range file.Imports {
			if imported.Path.Value == `"regexp"` {
				t.Fatalf("%s imports regexp; terminal byte analysis must use parser/emulator types", path)
			}
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if (ident.Name == "bytes" || ident.Name == "strings") && (selector.Sel.Name == "Contains" || selector.Sel.Name == "Index") {
				t.Fatalf("%s uses %s.%s; terminal byte analysis must not use substring detection", path, ident.Name, selector.Sel.Name)
			}
			return true
		})
	}
}

func analyzeBytes(t *testing.T, payload []byte, dimensions pty.Dimensions) pty.Analysis {
	t.Helper()
	capture, err := pty.NewCapture(dimensions, []pty.Chunk{pty.NewChunk(0, time.Millisecond, payload)})
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	return analysis
}

func requireOperation(t *testing.T, analysis pty.Analysis, kind pty.OperationKind, region pty.Region) {
	t.Helper()
	for _, op := range analysis.Operations {
		if op.Kind == kind && op.Region == region {
			return
		}
	}
	t.Fatalf("operation kind=%v region=%#v not found in %#v", kind, region, analysis.Operations)
}
