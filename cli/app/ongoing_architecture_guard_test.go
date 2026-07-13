package app

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestOngoingClientArchitectureGuards(t *testing.T) {
	repoRoot := mainSurfaceGuardRepositoryRoot(t)
	pkgs := loadOngoingArchitectureGuardPackages(t, repoRoot)
	violations := collectOngoingArchitectureViolations(pkgs, repoRoot)
	if len(violations) == 0 {
		return
	}
	sort.Slice(violations, func(i, j int) bool {
		return violations[i].String() < violations[j].String()
	})
	formatted := make([]string, 0, len(violations))
	for _, violation := range violations {
		formatted = append(formatted, violation.String())
	}
	t.Fatalf("ongoing native scrollback architecture violations:\n%s", strings.Join(formatted, "\n"))
}

func TestOngoingClientArchitectureGuardsRejectNegativeFixture(t *testing.T) {
	t.Run("committed row mirror", func(t *testing.T) {
		source := `package app

import "core/shared/clientui"

type badMirror struct {
	rows []clientui.TranscriptCommittedRow
}
`
		pkgs, root := parseOngoingArchitectureFixture(t, "cli/app/ui.go", source)
		violations := collectOngoingArchitectureViolations(pkgs, root)
		assertOngoingArchitectureViolation(t, violations, ongoingArchitectureViolationCommittedRowsRetained)
	})

	t.Run("committed row mirror nested behind projection", func(t *testing.T) {
		source := `package app

import "core/shared/clientui"

type detailEntry struct {
	row clientui.TranscriptCommittedRow
}

type badProjection struct {
	entries []detailEntry
}

type DetailPresentationSnapshot struct {
	projection badProjection
}
`
		pkgs, root := parseOngoingArchitectureFixture(t, "cli/app/ui.go", source)
		violations := collectOngoingArchitectureViolations(pkgs, root)
		assertOngoingArchitectureViolation(t, violations, ongoingArchitectureViolationCommittedRowsRetained)
	})

	t.Run("bounded detail owner", func(t *testing.T) {
		source := `package app

import "core/shared/clientui"

type uiDetailTranscriptWindow struct {
	entries []clientui.TranscriptCommittedRow
}

type uiDetailTranscriptMergeResult struct {
	trimmedFrontEntries []clientui.TranscriptCommittedRow
}
`
		pkgs, root := parseOngoingArchitectureFixture(t, "cli/app/ui_detail_transcript_window.go", source)
		if violations := collectOngoingArchitectureViolations(pkgs, root); len(violations) != 0 {
			t.Fatalf("bounded detail ownership violations = %v, want none", violations)
		}
	})

	t.Run("page read in ongoing path", func(t *testing.T) {
		source := `package app
func badPageRead(client interface{ GetSessionTranscriptPage() }) {
	client.GetSessionTranscriptPage()
}
`
		pkgs, root := parseOngoingArchitectureFixture(t, "cli/app/ongoing_bad.go", source)
		violations := collectOngoingArchitectureViolations(pkgs, root)
		assertOngoingArchitectureViolation(t, violations, ongoingArchitectureViolationPageRead)
	})
}

func assertOngoingArchitectureViolation(t *testing.T, violations []ongoingArchitectureViolation, reason ongoingArchitectureViolationReason) {
	t.Helper()
	for _, violation := range violations {
		if violation.Reason == reason {
			return
		}
	}
	t.Fatalf("architecture violations = %v, want reason %q", violations, reason)
}

func loadOngoingArchitectureGuardPackages(t *testing.T, repoRoot string) []*packages.Package {
	t.Helper()
	pkgs, err := packages.Load(&packages.Config{
		Dir:   repoRoot,
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Tests: false,
	}, "./cli/app", "./cli/tui/...")
	if err != nil {
		t.Fatalf("load ongoing architecture packages: %v", err)
	}
	if errors := mainSurfaceGuardPackageErrors(pkgs); len(errors) > 0 {
		t.Fatalf("ongoing architecture packages must type-check before scanning:\n%s", strings.Join(errors, "\n"))
	}
	return pkgs
}

func collectOngoingArchitectureViolations(pkgs []*packages.Package, repoRoot string) []ongoingArchitectureViolation {
	var violations []ongoingArchitectureViolation
	for _, pkg := range pkgs {
		if pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			position := pkg.Fset.Position(file.Pos())
			relPath := ongoingArchitectureRelativePath(repoRoot, position.Filename)
			if !isOngoingClientProductionPath(relPath) {
				continue
			}
			violations = append(violations, ongoingArchitectureViolationsInFile(pkg, file, relPath)...)
		}
	}
	return violations
}

func ongoingArchitectureViolationsInFile(pkg *packages.Package, file *ast.File, relPath string) []ongoingArchitectureViolation {
	var violations []ongoingArchitectureViolation
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.TypeSpec:
			violations = append(violations, committedRowStateViolations(pkg, typed, relPath)...)
		case *ast.SelectorExpr:
			if isForbiddenOngoingTranscriptReadSelector(typed.Sel.Name) && isOngoingDeliveryOrSurfacePath(relPath) {
				violations = append(violations, newOngoingArchitectureViolation(pkg, typed.Pos(), relPath, ongoingArchitectureViolationPageRead))
			}
			if isClientUITranscriptSymbol(pkg.TypesInfo.Uses[typed.Sel]) && isUncountedAppPath(relPath) {
				violations = append(violations, newOngoingArchitectureViolation(pkg, typed.Pos(), relPath, ongoingArchitectureViolationUncountedAppPath))
			}
			if isOngoingRawWriterSelector(typed.Sel.Name) && isAppPath(relPath) {
				violations = append(violations, newOngoingArchitectureViolation(pkg, typed.Pos(), relPath, ongoingArchitectureViolationRawWriter))
			}
		case *ast.Ident:
			if isForbiddenOngoingTranscriptReadSelector(typed.Name) && isOngoingDeliveryOrSurfacePath(relPath) {
				violations = append(violations, newOngoingArchitectureViolation(pkg, typed.Pos(), relPath, ongoingArchitectureViolationPageRead))
			}
		}
		return true
	})
	return violations
}

func committedRowStateViolations(pkg *packages.Package, spec *ast.TypeSpec, relPath string) []ongoingArchitectureViolation {
	structType, ok := spec.Type.(*ast.StructType)
	if !ok {
		return nil
	}
	var violations []ongoingArchitectureViolation
	for _, field := range structType.Fields.List {
		fieldType := pkg.TypesInfo.TypeOf(field.Type)
		if !isCommittedRowCollectionType(fieldType) &&
			!(spec.Name.Name == "DetailPresentationSnapshot" &&
				typeTransitivelyContainsCommittedRow(fieldType, make(map[types.Type]struct{}))) {
			continue
		}
		if len(field.Names) == 0 {
			violations = append(violations, newOngoingArchitectureViolation(pkg, field.Pos(), relPath, ongoingArchitectureViolationCommittedRowsRetained))
			continue
		}
		for _, fieldName := range field.Names {
			if isSanctionedDetailCommittedRowState(pkg.PkgPath, spec.Name.Name, fieldName.Name) {
				continue
			}
			violations = append(violations, newOngoingArchitectureViolation(pkg, fieldName.Pos(), relPath, ongoingArchitectureViolationCommittedRowsRetained))
		}
	}
	return violations
}

func isCommittedRowCollectionType(typ types.Type) bool {
	slice, ok := typ.(*types.Slice)
	return ok && typeName(slice.Elem()) == "core/shared/clientui.TranscriptCommittedRow"
}

func typeTransitivelyContainsCommittedRow(typ types.Type, seen map[types.Type]struct{}) bool {
	if typeName(typ) == "core/shared/clientui.TranscriptCommittedRow" {
		return true
	}
	if _, visited := seen[typ]; visited {
		return false
	}
	seen[typ] = struct{}{}
	switch typed := typ.(type) {
	case *types.Alias:
		return typeTransitivelyContainsCommittedRow(typed.Rhs(), seen)
	case *types.Named:
		return typeTransitivelyContainsCommittedRow(typed.Underlying(), seen)
	case *types.Pointer:
		return typeTransitivelyContainsCommittedRow(typed.Elem(), seen)
	case *types.Struct:
		for index := 0; index < typed.NumFields(); index++ {
			if typeTransitivelyContainsCommittedRow(typed.Field(index).Type(), seen) {
				return true
			}
		}
	case *types.Slice:
		return typeTransitivelyContainsCommittedRow(typed.Elem(), seen)
	case *types.Array:
		return typeTransitivelyContainsCommittedRow(typed.Elem(), seen)
	case *types.Map:
		return typeTransitivelyContainsCommittedRow(typed.Key(), seen) ||
			typeTransitivelyContainsCommittedRow(typed.Elem(), seen)
	}
	return false
}

func isSanctionedDetailCommittedRowState(packagePath, ownerType, fieldName string) bool {
	switch packagePath {
	case "core/cli/app":
		switch ownerType {
		case "uiDetailTranscriptWindow":
			return fieldName == "entries"
		case "uiDetailTranscriptMergeResult":
			return fieldName == "trimmedFrontEntries"
		}
	case "core/cli/tui":
		return ownerType == "SetDetailTranscriptPageMsg" && fieldName == "TrimmedFrontEntries"
	}
	return false
}

func parseOngoingArchitectureFixture(t *testing.T, relPath, source string) ([]*packages.Package, string) {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module core\n\ngo 1.26.4\n")
	writeTestFile(t, filepath.Join(dir, filepath.FromSlash(relPath)), source)
	writeTestFile(t, filepath.Join(dir, "shared/clientui/transcript_contract.go"), `package clientui

type TranscriptCommittedRow struct{}
`)
	pkgs, err := packages.Load(&packages.Config{
		Dir:   dir,
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Tests: false,
	}, "./cli/app")
	if err != nil {
		t.Fatalf("load fixture package: %v", err)
	}
	if errors := mainSurfaceGuardPackageErrors(pkgs); len(errors) > 0 {
		t.Fatalf("fixture package must type-check:\n%s", strings.Join(errors, "\n"))
	}
	return pkgs, dir
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
}

type ongoingArchitectureViolationReason string

const (
	ongoingArchitectureViolationCommittedRowsRetained ongoingArchitectureViolationReason = "committed_rows_retained"
	ongoingArchitectureViolationPageRead              ongoingArchitectureViolationReason = "page_read"
	ongoingArchitectureViolationUncountedAppPath      ongoingArchitectureViolationReason = "uncounted_app_path"
	ongoingArchitectureViolationRawWriter             ongoingArchitectureViolationReason = "raw_writer"
)

type ongoingArchitectureViolation struct {
	RelPath string
	Line    int
	Column  int
	Reason  ongoingArchitectureViolationReason
}

func (v ongoingArchitectureViolation) String() string {
	return fmt.Sprintf("%s:%d:%d: %s", v.RelPath, v.Line, v.Column, v.Reason.Message())
}

func (r ongoingArchitectureViolationReason) Message() string {
	switch r {
	case ongoingArchitectureViolationCommittedRowsRetained:
		return "committed transcript rows may not be retained in client production state outside the bounded detail owners"
	case ongoingArchitectureViolationPageRead:
		return "ongoing startup/rehydration must use transcript subscription hydration, not page/tail/gap reads"
	case ongoingArchitectureViolationUncountedAppPath:
		return "app transcript-message handling for ongoing must live in counted session_transcript/ui_ongoing files"
	case ongoingArchitectureViolationRawWriter:
		return "cli/app must call ongoing Surface methods, not raw ongoing writer helpers"
	default:
		return string(r)
	}
}

func newOngoingArchitectureViolation(pkg *packages.Package, pos token.Pos, relPath string, reason ongoingArchitectureViolationReason) ongoingArchitectureViolation {
	position := pkg.Fset.Position(pos)
	return ongoingArchitectureViolation{
		RelPath: relPath,
		Line:    position.Line,
		Column:  position.Column,
		Reason:  reason,
	}
}

func ongoingArchitectureRelativePath(repoRoot, path string) string {
	if repoRoot == "" {
		return filepath.ToSlash(path)
	}
	relPath, ok := mainSurfaceGuardRelativePath(repoRoot, path)
	if !ok {
		return ""
	}
	return relPath
}

func isOngoingClientProductionPath(relPath string) bool {
	if relPath == "" || strings.HasSuffix(relPath, "_test.go") {
		return false
	}
	return isAppPath(relPath) || strings.HasPrefix(relPath, "cli/tui/")
}

func isAppPath(relPath string) bool {
	return strings.HasPrefix(relPath, "cli/app/")
}

func isUncountedAppPath(relPath string) bool {
	return isAppPath(relPath) && !isCountedOngoingAppPath(relPath)
}

func isCountedOngoingAppPath(relPath string) bool {
	if !isAppPath(relPath) {
		return false
	}
	base := filepath.Base(relPath)
	return strings.HasPrefix(base, "ui_ongoing_") || strings.HasPrefix(base, "ongoing_") || strings.HasPrefix(base, "session_transcript_")
}

func isOngoingDeliveryOrSurfacePath(relPath string) bool {
	return strings.HasPrefix(relPath, "cli/tui/ongoing/") || isCountedOngoingAppPath(relPath)
}

func isForbiddenOngoingTranscriptReadSelector(name string) bool {
	switch name {
	case "TranscriptSegmentPage",
		"TranscriptSegmentPageForward",
		"TranscriptSegmentPageFromStore",
		"GetSessionTranscriptPage",
		"ReadSegmentBackward",
		"ReadEventsBackwardUntil":
		return true
	default:
		return false
	}
}

func isClientUITranscriptSymbol(obj types.Object) bool {
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != "core/shared/clientui" {
		return false
	}
	switch name := obj.Name(); {
	case strings.HasPrefix(name, "TranscriptMessage"):
		return true
	case name == "TranscriptHydration":
		return true
	default:
		return false
	}
}

func isOngoingRawWriterSelector(name string) bool {
	switch name {
	case "WriteRaw", "WriteEscape", "WriteEscapes", "AppendRaw", "AppendEscape", "BuildEscape", "NewRawWriter":
		return true
	default:
		return false
	}
}

func typeName(typ types.Type) string {
	if typ == nil {
		return ""
	}
	named, ok := typ.(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return ""
	}
	return named.Obj().Pkg().Path() + "." + named.Obj().Name()
}
