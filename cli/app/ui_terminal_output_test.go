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
)

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
		"core/internal/testharness/pty":            {},
		"core/internal/testharness/pty/analyzer":   {},
		"core/internal/testharness/pty/checkpoint": {},
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
