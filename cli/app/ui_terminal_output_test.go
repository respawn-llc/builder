package app

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

type terminalOutputWriteResult struct {
	n   int
	err error
}

type terminalOutputScriptWriter struct {
	results  []terminalOutputWriteResult
	writes   int
	payloads [][]byte
}

func (w *terminalOutputScriptWriter) Write(payload []byte) (int, error) {
	w.writes++
	w.payloads = append(w.payloads, append([]byte(nil), payload...))
	if len(w.results) == 0 {
		return len(payload), nil
	}
	result := w.results[0]
	w.results = w.results[1:]
	return result.n, result.err
}

func TestTerminalOutputRestoreBypassesLatchWithoutRetryingPayload(t *testing.T) {
	expected := errors.New("terminal unavailable")
	out := &terminalOutputScriptWriter{
		results: []terminalOutputWriteResult{
			{err: expected},
			{n: len("\x1b[?1007l" + xansi.ResetModeAltScreenSaveCursor + xansi.ShowCursor)},
		},
	}
	terminal := newUITerminalOutput(out)
	if _, err := terminal.Write([]byte("frame")); !errors.Is(err, expected) {
		t.Fatalf("write error = %v, want %v", err, expected)
	}

	terminal.Restore()

	if out.writes != 2 {
		t.Fatalf("underlying writes = %d, want failed payload plus one restoration", out.writes)
	}
	if got := string(out.payloads[1]); got != "\x1b[?1007l"+xansi.ResetModeAltScreenSaveCursor+xansi.ShowCursor {
		t.Fatalf("restoration = %q", got)
	}
	for _, payload := range out.payloads[1:] {
		if string(payload) == "frame" {
			t.Fatal("terminal output retried failed stdout payload")
		}
	}
}

func TestTerminalOutputRunErrorPrefersLatchedTerminalFailure(t *testing.T) {
	terminalFailure := errors.New("terminal unavailable")
	loopFailure := errors.New("renderer stopped")
	out := &terminalOutputScriptWriter{
		results: []terminalOutputWriteResult{
			{err: terminalFailure},
			{n: len("\x1b[?1007l" + xansi.ResetModeAltScreenSaveCursor + xansi.ShowCursor)},
		},
	}
	output := newUITerminalOutput(out)
	if _, err := output.Write([]byte("frame")); !errors.Is(err, terminalFailure) {
		t.Fatalf("write error = %v, want %v", err, terminalFailure)
	}

	err := terminalOutputRunError(output, loopFailure)

	if !errors.Is(err, terminalFailure) {
		t.Fatalf("run error = %v, want terminal failure", err)
	}
	if errors.Is(err, loopFailure) {
		t.Fatalf("run error retained secondary loop failure: %v", err)
	}
}

func TestTerminalOutputLatchesFirstWriteError(t *testing.T) {
	expected := errors.New("terminal unavailable")
	out := &terminalOutputScriptWriter{results: []terminalOutputWriteResult{{err: expected}}}
	terminal := newUITerminalOutput(out)

	if _, err := terminal.Write([]byte("first")); !errors.Is(err, expected) {
		t.Fatalf("first write error = %v, want %v", err, expected)
	}
	if _, err := terminal.Write([]byte("second")); !errors.Is(err, expected) {
		t.Fatalf("second write error = %v, want latched %v", err, expected)
	}
	if out.writes != 1 {
		t.Fatalf("underlying writes = %d, want 1", out.writes)
	}
	if err := terminal.Err(); !errors.Is(err, expected) {
		t.Fatalf("latched error = %v, want %v", err, expected)
	}
}

func TestTerminalOutputLatchesShortWrite(t *testing.T) {
	out := &terminalOutputScriptWriter{results: []terminalOutputWriteResult{{n: 2}}}
	terminal := newUITerminalOutput(out)

	if n, err := terminal.Write([]byte("frame")); n != 2 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("first write = %d, %v, want 2, %v", n, err, io.ErrShortWrite)
	}
	if _, err := terminal.Write([]byte("retry")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("second write error = %v, want latched short write", err)
	}
	if out.writes != 1 {
		t.Fatalf("underlying writes = %d, want no stdout retry", out.writes)
	}
}

func TestTerminalOutputSharesRendererAndNativeWriteOrdering(t *testing.T) {
	var out bytes.Buffer
	terminal := newUITerminalOutput(&out)
	renderer := newUIRendererOutputGateWriter(terminal, newUIRendererOutputGateState())

	if _, err := renderer.Write([]byte("renderer-1|")); err != nil {
		t.Fatalf("renderer write: %v", err)
	}
	if _, err := terminal.Write([]byte("native-1|")); err != nil {
		t.Fatalf("native write: %v", err)
	}
	if _, err := renderer.Write([]byte("renderer-2")); err != nil {
		t.Fatalf("renderer write 2: %v", err)
	}

	if got := out.String(); got != "renderer-1|native-1|renderer-2" {
		t.Fatalf("output = %q", got)
	}
}

func TestTerminalOutputAnnouncesInputReadinessOnFirstWrite(t *testing.T) {
	var out bytes.Buffer
	terminal := newUITerminalOutput(&out)
	if err := terminal.AnnounceInputReady(); err != nil {
		t.Fatalf("announce input ready: %v", err)
	}
	if _, err := terminal.Write([]byte("frame")); err != nil {
		t.Fatalf("write first frame: %v", err)
	}
	if got, want := out.String(), xansi.ShowCursor+"frame"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestTerminalOutputRejectsLateInputReadinessAnnouncement(t *testing.T) {
	terminal := newUITerminalOutput(&bytes.Buffer{})
	if _, err := terminal.Write([]byte("frame")); err != nil {
		t.Fatalf("write first frame: %v", err)
	}
	if err := terminal.AnnounceInputReady(); err == nil {
		t.Fatal("expected late readiness announcement error")
	}
}

func TestTerminalOutputFilePreservesTerminalFileDescriptor(t *testing.T) {
	file := &rendererOutputGateTerminalFile{fd: 42}
	terminal := newUITerminalOutputFile(file)
	terminalFile, ok := any(terminal).(interface{ Fd() uintptr })
	if !ok {
		t.Fatalf("expected terminal output to preserve Fd for Bubble Tea TTY detection, got %T", terminal)
	}
	if got := terminalFile.Fd(); got != 42 {
		t.Fatalf("fd = %d, want 42", got)
	}
	if _, err := terminal.Write([]byte("native output")); err != nil {
		t.Fatalf("write terminal output: %v", err)
	}
	if got := file.String(); got != "native output" {
		t.Fatalf("forwarded output = %q", got)
	}
}

func TestProductionAppAndTUICodeCannotDependOnPTYCheckpointProtocol(t *testing.T) {
	forbiddenImports := map[string]struct{}{
		"core/internal/testharness/pty":          {},
		"core/internal/testharness/pty/analyzer": {},
	}
	forbiddenIdentifiers := map[string]struct{}{
		"TerminalPhase":                   {},
		"TerminalPhaseMarker":             {},
		"TerminalPhaseMarkerEncoder":      {},
		"TerminalPhaseMarkerSink":         {},
		"TerminalPhaseMarkerSinkObserver": {},
		"uiTerminalRenderPhaseState":      {},
	}
	for _, path := range productionGoFiles(t, ".", "../tui") {
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("unquote import %s: %v", spec.Path.Value, err)
			}
			if _, forbidden := forbiddenImports[importPath]; forbidden {
				t.Fatalf("%s imports PTY checkpoint dependency %q", path, importPath)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if identifier, ok := node.(*ast.Ident); ok {
				if _, forbidden := forbiddenIdentifiers[identifier.Name]; forbidden {
					t.Fatalf("%s names PTY checkpoint protocol identifier %q", path, identifier.Name)
				}
			}
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("unquote literal in %s: %v", path, err)
			}
			switch value {
			case "kent-pty-checkpoint", "\x1b]777;", "\x1b]777;kent-pty-checkpoint;":
				t.Fatalf("%s contains raw PTY checkpoint literal %q", path, value)
			}
			return true
		})
	}
}

func productionGoFiles(t *testing.T, roots ...string) []string {
	t.Helper()
	var files []string
	for _, root := range roots {
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if filepath.Ext(path) == ".go" && !isGoTestFile(path) {
				files = append(files, path)
			}
			return nil
		}); err != nil {
			t.Fatalf("walk production files under %s: %v", root, err)
		}
	}
	return files
}

func isGoTestFile(path string) bool {
	return strings.HasSuffix(filepath.Base(path), "_test.go")
}
