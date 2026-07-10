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
	if op.Write == nil || op.Write.Text() != "hello" {
		t.Fatalf("operation write payload = %#v, want text %q", op.Write, "hello")
	}
	if op.ByteRange != (pty.ByteRange{Start: 0, End: 5}) {
		t.Fatalf("operation byte range = %#v, want [0,5)", op.ByteRange)
	}
	if op.ChunkIndex != 0 {
		t.Fatalf("operation chunk index = %d, want 0", op.ChunkIndex)
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

func TestAnalyzeRejectsMalformedPublicCaptureInsteadOfPanicking(t *testing.T) {
	t.Parallel()

	capture := pty.Capture{
		Dimensions: pty.MustDimensions(2, 4),
		Raw:        []byte("x"),
		Resizes: []pty.ResizeEvent{{
			Placement:  pty.BeforeFirstChunk(),
			Offset:     0,
			Dimensions: pty.Dimensions{},
		}},
	}
	if _, err := pty.Analyze(capture); err == nil {
		t.Fatal("Analyze succeeded for malformed public capture")
	}
}

func TestAnalyzePreservesChunkAndTimestampMetadataAcrossReplay(t *testing.T) {
	t.Parallel()

	capture, err := pty.NewCapture(
		pty.MustDimensions(2, 8),
		[]pty.Chunk{
			pty.NewChunk(0, time.Millisecond, []byte("\x1b[?1049h")),
			pty.NewChunk(1, 2*time.Millisecond, []byte("x")),
		},
	)
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(analysis.PrivateModeChanges) != 1 || analysis.PrivateModeChanges[0].ChunkIndex != 0 || analysis.PrivateModeChanges[0].CapturedAt != time.Millisecond {
		t.Fatalf("mode metadata = %#v", analysis.PrivateModeChanges)
	}
	for _, operation := range analysis.Operations {
		if operation.Kind == pty.OperationWrite && (operation.ChunkIndex != 1 || operation.CapturedAt != 2*time.Millisecond) {
			t.Fatalf("write metadata = %#v", operation)
		}
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

func TestScreenSnapshotReportsBlankFrame(t *testing.T) {
	t.Parallel()

	blank := pty.NewScreenSnapshot(pty.MustDimensions(2, 4))
	if !blank.IsBlank() {
		t.Fatal("new screen snapshot should be blank")
	}
	if diagnostic := blank.BlankFrameDiagnostic(); diagnostic != nil {
		t.Fatalf("blank frame diagnostic = %+v, want nil", diagnostic)
	}
	written := analyzeBytes(t, []byte("x"), pty.MustDimensions(2, 4)).Screen
	if written.IsBlank() {
		t.Fatal("screen with content should not be blank")
	}
	diagnostic := written.BlankFrameDiagnostic()
	if diagnostic == nil {
		t.Fatal("nonblank screen diagnostic = nil")
	}
	if diagnostic.Dimensions != (pty.MustDimensions(2, 4)) || diagnostic.Position != (pty.Position{Row: 0, Col: 0}) || diagnostic.Content != "x" {
		t.Fatalf("blank frame diagnostic = %+v", diagnostic)
	}
}

func TestDimensionsRejectOutOfRangeAndOversizedGeometryBeforeScreenAllocation(t *testing.T) {
	t.Parallel()

	for _, dimensions := range []pty.Dimensions{
		{Rows: 0, Cols: 1},
		{Rows: 201, Cols: 1},
		{Rows: 1, Cols: 501},
		{Rows: 201, Cols: 500},
	} {
		if _, err := pty.NewDimensions(dimensions.Rows, dimensions.Cols); err == nil {
			t.Fatalf("NewDimensions(%+v) succeeded", dimensions)
		}
	}

	if _, err := pty.NewCaptureWithEvents(
		pty.MustDimensions(1, 1),
		nil,
		[]pty.ResizeEvent{{Placement: pty.BeforeFirstChunk(), Dimensions: pty.Dimensions{Rows: 201, Cols: 500}}},
	); err == nil {
		t.Fatal("NewCaptureWithEvents accepted oversized resize")
	}

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewScreenSnapshot accepted invalid dimensions")
		}
	}()
	_ = pty.NewScreenSnapshot(pty.Dimensions{Rows: 201, Cols: 500})
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
