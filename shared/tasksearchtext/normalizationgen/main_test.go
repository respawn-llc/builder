package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestPinnedNormalizationTablesAreExtractedFromGoAST(t *testing.T) {
	source, err := loadPinnedSQLiteSource()
	if err != nil {
		t.Fatalf("load pinned SQLite source: %v", err)
	}

	tables, err := extractNormalizationTables(source)
	if err != nil {
		t.Fatalf("extract normalization tables: %v", err)
	}

	if got := len(tables.caseEntries); got != 163 {
		t.Fatalf("case entry count = %d, want 163", got)
	}
	if got := len(tables.caseOffsets); got != 77 {
		t.Fatalf("case offset count = %d, want 77", got)
	}
	if got := len(tables.diacriticKeys); got != 126 {
		t.Fatalf("diacritic key count = %d, want 126", got)
	}
	if got := len(tables.diacriticValues); got != 126 {
		t.Fatalf("diacritic value count = %d, want 126", got)
	}
	if tables.sqliteVersion != expectedSQLiteVersion {
		t.Fatalf("SQLite version = %q, want %q", tables.sqliteVersion, expectedSQLiteVersion)
	}
}

func TestGeneratedNormalizationDataIsFreshAndDeterministic(t *testing.T) {
	first, err := generatePinnedNormalizationData()
	if err != nil {
		t.Fatalf("generate first normalization data: %v", err)
	}
	second, err := generatePinnedNormalizationData()
	if err != nil {
		t.Fatalf("generate second normalization data: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("normalization generation is not deterministic")
	}

	const outputPath = "../normalization_generated.go"
	current, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read generated normalization data: %v", err)
	}
	if !bytes.Equal(current, first) {
		t.Fatalf(
			"generated normalization data is stale; run go run ./shared/tasksearchtext/normalizationgen generate --output %s",
			filepath.Clean(outputPath),
		)
	}
}

func TestGeneratedOutputRecordsPinnedModuleAndSQLiteProvenance(t *testing.T) {
	source, err := loadPinnedSQLiteSource()
	if err != nil {
		t.Fatalf("load pinned SQLite source: %v", err)
	}
	generated, err := generatePinnedNormalizationData()
	if err != nil {
		t.Fatalf("generate normalization data: %v", err)
	}
	constants := generatedStringConstants(t, generated)

	want := map[string]string{
		"normalizationSourceModulePath":     expectedModulePath,
		"normalizationSourceModuleVersion":  expectedModuleVersion,
		"normalizationSourceModuleChecksum": expectedModuleSum,
		"normalizationSQLiteVersion":        expectedSQLiteVersion,
		"normalizationSQLiteSourceUnit":     sqliteUnicodeSourceUnit,
		"normalizationSourceChecksum":       sourceChecksum(source),
	}
	for name, expected := range want {
		if got := constants[name]; got != expected {
			t.Errorf("generated %s = %q, want %q", name, got, expected)
		}
	}
}

func TestPinnedModuleProvenanceRejectsUnexpectedVersionOrChecksum(t *testing.T) {
	source, err := loadPinnedSQLiteSource()
	if err != nil {
		t.Fatalf("load pinned SQLite source: %v", err)
	}

	tamperedVersion := source
	tamperedVersion.module.Version = "v0.0.0"
	if err := tamperedVersion.validate(); err == nil {
		t.Fatal("unexpected module version was accepted")
	}

	tamperedChecksum := source
	tamperedChecksum.module.Sum = "h1:tampered"
	if err := tamperedChecksum.validate(); err == nil {
		t.Fatal("unexpected module checksum was accepted")
	}
}

func generatedStringConstants(t testing.TB, source []byte) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "normalization_generated.go", source, 0)
	if err != nil {
		t.Fatalf("parse generated normalization data: %v", err)
	}
	constants := make(map[string]string)
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, specification := range general.Specs {
			valueSpec, ok := specification.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range valueSpec.Names {
				if index >= len(valueSpec.Values) {
					continue
				}
				literal, ok := valueSpec.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatalf("unquote generated %s: %v", name.Name, err)
				}
				constants[name.Name] = value
			}
		}
	}
	return constants
}

func TestGeneratedOutputCheckRejectsTampering(t *testing.T) {
	generated, err := generatePinnedNormalizationData()
	if err != nil {
		t.Fatalf("generate normalization data: %v", err)
	}
	tampered := append([]byte(nil), generated...)
	tampered[len(tampered)-1] ^= 1

	if err := checkGeneratedOutput(tampered, generated); err == nil {
		t.Fatal("tampered generated output was accepted")
	}
}
