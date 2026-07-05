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
	sort.Strings(violations)
	t.Fatalf("ongoing native scrollback architecture violations:\n%s", strings.Join(violations, "\n"))
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
		assertOngoingArchitectureViolation(t, violations, "committed transcript rows may not be retained")
	})

	t.Run("page read in ongoing path", func(t *testing.T) {
		source := `package app
func badPageRead(client interface{ GetSessionTranscriptPage() }) {
	client.GetSessionTranscriptPage()
}
`
		pkgs, root := parseOngoingArchitectureFixture(t, "cli/app/ongoing_bad.go", source)
		violations := collectOngoingArchitectureViolations(pkgs, root)
		assertOngoingArchitectureViolation(t, violations, "ongoing startup/rehydration must use transcript subscription hydration")
	})
}

func assertOngoingArchitectureViolation(t *testing.T, violations []string, reason string) {
	t.Helper()
	for _, violation := range violations {
		if strings.Contains(violation, reason) {
			return
		}
	}
	t.Fatalf("architecture violations = %v, want reason containing %q", violations, reason)
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

func collectOngoingArchitectureViolations(pkgs []*packages.Package, repoRoot string) []string {
	var violations []string
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

func ongoingArchitectureViolationsInFile(pkg *packages.Package, file *ast.File, relPath string) []string {
	var violations []string
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.ArrayType:
			if typeName(pkg.TypesInfo.TypeOf(typed.Elt)) == "core/shared/clientui.TranscriptCommittedRow" && !isSanctionedCommittedRowCollectionPath(relPath) {
				violations = append(violations, ongoingArchitectureViolation(pkg, typed.Pos(), relPath, "committed transcript rows may not be retained in client production state outside the sanctioned ongoing render path"))
			}
		case *ast.SelectorExpr:
			if isForbiddenOngoingTranscriptReadSelector(typed.Sel.Name) && isOngoingDeliveryOrSurfacePath(relPath) {
				violations = append(violations, ongoingArchitectureViolation(pkg, typed.Pos(), relPath, "ongoing startup/rehydration must use transcript subscription hydration, not page/tail/gap reads"))
			}
			if isClientUITranscriptSymbol(pkg.TypesInfo.Uses[typed.Sel]) && isUncountedAppPath(relPath) {
				violations = append(violations, ongoingArchitectureViolation(pkg, typed.Pos(), relPath, "app transcript-message handling for ongoing must live in counted session_transcript/ui_ongoing files"))
			}
			if isOngoingRawWriterSelector(typed.Sel.Name) && isAppPath(relPath) {
				violations = append(violations, ongoingArchitectureViolation(pkg, typed.Pos(), relPath, "cli/app must call ongoing Surface methods, not raw ongoing writer helpers"))
			}
		case *ast.Ident:
			if isForbiddenOngoingTranscriptReadSelector(typed.Name) && isOngoingDeliveryOrSurfacePath(relPath) {
				violations = append(violations, ongoingArchitectureViolation(pkg, typed.Pos(), relPath, "ongoing startup/rehydration must use transcript subscription hydration, not page/tail/gap reads"))
			}
		}
		return true
	})
	return violations
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

func ongoingArchitectureViolation(pkg *packages.Package, pos token.Pos, relPath, reason string) string {
	position := pkg.Fset.Position(pos)
	return fmt.Sprintf("%s:%d:%d: %s", relPath, position.Line, position.Column, reason)
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

func isSanctionedCommittedRowCollectionPath(relPath string) bool {
	return strings.HasPrefix(relPath, "cli/tui/ongoing/") || strings.HasPrefix(relPath, "shared/clientui/") || isCountedOngoingAppPath(relPath)
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
	case name == "TranscriptCommittedRow", name == "TranscriptHydration":
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
