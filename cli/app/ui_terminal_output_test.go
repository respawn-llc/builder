package app

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"core/cli/app/internal/runner"

	"github.com/google/uuid"
)

type terminalPhaseMarkerEncoderFunc func(runner.TerminalPhaseMarker) ([]byte, error)

func (fn terminalPhaseMarkerEncoderFunc) EncodeTerminalPhaseMarker(marker runner.TerminalPhaseMarker) ([]byte, error) {
	return fn(marker)
}

func TestTerminalPhaseMarkersShareRendererAndNativeOutputOrdering(t *testing.T) {
	var out bytes.Buffer
	terminal := newUITerminalOutput(&out, terminalPhaseMarkerEncoderFunc(func(marker runner.TerminalPhaseMarker) ([]byte, error) {
		if marker.Phase != runner.TerminalPhaseReadyForQuit {
			t.Fatalf("phase = %q, want ReadyForQuit", marker.Phase)
		}
		return []byte("<ready>"), nil
	}))
	renderer := newUIRendererOutputGateWriter(terminal, newUIRendererOutputGateState())

	if _, err := renderer.Write([]byte("renderer-1|")); err != nil {
		t.Fatalf("renderer write: %v", err)
	}
	if _, err := terminal.Write([]byte("native-1|")); err != nil {
		t.Fatalf("native write: %v", err)
	}
	if err := terminal.RequestTerminalPhaseMarker(runner.TerminalPhaseMarker{Phase: runner.TerminalPhaseReadyForQuit}); err != nil {
		t.Fatalf("marker request: %v", err)
	}
	if _, err := renderer.Write([]byte("renderer-2")); err != nil {
		t.Fatalf("renderer write 2: %v", err)
	}

	if got := out.String(); got != "renderer-1|native-1|<ready>renderer-2" {
		t.Fatalf("output = %q", got)
	}
}

func TestTerminalPhaseMarkerRequestsRequireEncoder(t *testing.T) {
	var out bytes.Buffer
	terminal := newUITerminalOutput(&out, nil)
	err := terminal.RequestTerminalPhaseMarker(runner.TerminalPhaseMarker{Phase: runner.TerminalPhaseScenarioComplete})
	if err == nil {
		t.Fatal("expected missing encoder error")
	}
	if out.Len() != 0 {
		t.Fatalf("output len = %d, want 0", out.Len())
	}
}

func TestTerminalPhaseMarkerCarriesTypedWindowID(t *testing.T) {
	windowID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("NewV7: %v", err)
	}
	var got runner.TerminalPhaseMarker
	terminal := newUITerminalOutput(&bytes.Buffer{}, terminalPhaseMarkerEncoderFunc(func(marker runner.TerminalPhaseMarker) ([]byte, error) {
		got = marker
		return []byte("<window-start>"), nil
	}))

	if err := terminal.RequestTerminalPhaseMarker(runner.TerminalPhaseMarker{
		Phase:    runner.TerminalPhaseWindowStart,
		WindowID: &windowID,
	}); err != nil {
		t.Fatalf("marker request: %v", err)
	}
	if _, err := terminal.Write([]byte("flush")); err != nil {
		t.Fatalf("flush write: %v", err)
	}
	if got.WindowID == nil || *got.WindowID != windowID {
		t.Fatalf("window id = %v, want %v", got.WindowID, windowID)
	}
}

func TestTerminalPhaseMarkerRejectsNonV7WindowID(t *testing.T) {
	windowID := uuid.New()
	terminal := newUITerminalOutput(&bytes.Buffer{}, terminalPhaseMarkerEncoderFunc(func(runner.TerminalPhaseMarker) ([]byte, error) {
		t.Fatal("encoder should not receive invalid marker")
		return nil, nil
	}))

	err := terminal.RequestTerminalPhaseMarker(runner.TerminalPhaseMarker{
		Phase:    runner.TerminalPhaseWindowStart,
		WindowID: &windowID,
	})
	if err == nil {
		t.Fatal("expected UUIDv7 validation error")
	}
}

func TestTerminalOutputFilePreservesTerminalFileDescriptor(t *testing.T) {
	file := &rendererOutputGateTerminalFile{fd: 42}
	terminal := newUITerminalOutputFile(file, nil)
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

func TestProductionAppCodeDoesNotConstructRawFixturePhaseMarkerOSC(t *testing.T) {
	for _, path := range productionGoFiles(t, ".") {
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
			if importPath == "core/internal/testharness/pty" {
				t.Fatalf("%s imports PTY harness facade", path)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("unquote literal in %s: %v", path, err)
			}
			switch value {
			case "kent-pty-phase", "\x1b]777;", "\x1b]777;kent-pty-phase;":
				t.Fatalf("%s contains raw fixture phase marker literal %q", path, value)
			}
			return true
		})
	}
}

func productionGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "internal" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".go" && !isGoTestFile(path) {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk production files: %v", err)
	}
	return files
}

func isGoTestFile(path string) bool {
	return strings.HasSuffix(filepath.Base(path), "_test.go")
}
