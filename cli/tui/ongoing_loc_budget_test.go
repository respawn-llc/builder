package tui

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	ongoingLoCReviewLimit = 8000
	ongoingLoCHardLimit   = 10000
)

func TestOngoingProductionLoCBudget(t *testing.T) {
	report := ongoingLoCBudgetReport(t, ongoingLoCRepositoryRoot(t))
	if report.count <= ongoingLoCReviewLimit {
		return
	}
	t.Fatalf("ongoing production LoC = %d across %d files; above %d requires explicit follow-up review and above %d is forbidden\n%s",
		report.count,
		len(report.files),
		ongoingLoCReviewLimit,
		ongoingLoCHardLimit,
		strings.Join(report.files, "\n"),
	)
}

func TestOngoingProductionLoCBudgetRejectsOverBudgetFixture(t *testing.T) {
	root := t.TempDir()
	writeOngoingBudgetFixtureFile(t, filepath.Join(root, "cli/tui/ongoing/surface.go"), ongoingBudgetLines(ongoingLoCReviewLimit+1))
	writeOngoingBudgetFixtureFile(t, filepath.Join(root, "cli/app/ui.go"), "package app\n")

	report := ongoingLoCBudgetReport(t, root)
	if report.count != ongoingLoCReviewLimit+1 {
		t.Fatalf("fixture LoC = %d, want %d", report.count, ongoingLoCReviewLimit+1)
	}
	if report.count <= ongoingLoCReviewLimit {
		t.Fatalf("fixture did not exceed review limit: %+v", report)
	}
}

func TestOngoingProductionLoCBudgetCountsExternalImporters(t *testing.T) {
	root := t.TempDir()
	writeOngoingBudgetFixtureFile(t, filepath.Join(root, "cli/tui/ongoing/surface.go"), "package ongoing\n")
	writeOngoingBudgetFixtureFile(t, filepath.Join(root, "cli/app/ui.go"), `package app

import _ "core/cli/tui/ongoing"

func useOngoingImport() {}
`)

	report := ongoingLoCBudgetReport(t, root)
	if report.count != 6 {
		t.Fatalf("fixture LoC = %d, want 6; files=%v", report.count, report.files)
	}
	if len(report.files) != 2 {
		t.Fatalf("counted files = %v, want ongoing file and importer", report.files)
	}
}

type ongoingLoCReport struct {
	count int
	files []string
}

func ongoingLoCBudgetReport(t *testing.T, root string) ongoingLoCReport {
	t.Helper()
	var report ongoingLoCReport
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if ignoredBudgetDir(entry.Name()) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)
		if !isProductionGoFile(relPath) || !isOngoingBudgetFile(t, root, relPath) {
			return nil
		}
		lines, err := countPhysicalLines(path)
		if err != nil {
			return err
		}
		report.count += lines
		report.files = append(report.files, fmt.Sprintf("%s: %d", relPath, lines))
		return nil
	})
	if err != nil {
		t.Fatalf("walk ongoing budget files: %v", err)
	}
	sort.Strings(report.files)
	return report
}

func isOngoingBudgetFile(t *testing.T, root, relPath string) bool {
	if strings.HasPrefix(relPath, "cli/tui/ongoing/") {
		return true
	}
	if strings.HasPrefix(relPath, "cli/app/") {
		base := filepath.Base(relPath)
		if strings.HasPrefix(base, "ui_ongoing_") || strings.HasPrefix(base, "ongoing_") || strings.HasPrefix(base, "session_transcript_") {
			return true
		}
	}
	return importsOngoingPackage(t, filepath.Join(root, filepath.FromSlash(relPath)))
}

func importsOngoingPackage(t *testing.T, path string) bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse imports for %s: %v", path, err)
	}
	for _, imported := range file.Imports {
		value, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			t.Fatalf("unquote import in %s: %v", path, err)
		}
		if value == "core/cli/tui/ongoing" {
			return true
		}
	}
	return false
}

func countPhysicalLines(path string) (int, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(content) == 0 {
		return 0, nil
	}
	lines := strings.Count(string(content), "\n")
	if content[len(content)-1] != '\n' {
		lines++
	}
	return lines, nil
}

func isProductionGoFile(relPath string) bool {
	return strings.HasSuffix(relPath, ".go") && !strings.HasSuffix(relPath, "_test.go")
}

func ignoredBudgetDir(name string) bool {
	switch name {
	case ".git", "node_modules", "dist", "build", "bin":
		return true
	default:
		return false
	}
}

func ongoingBudgetLines(count int) string {
	var builder strings.Builder
	builder.WriteString("package ongoing\n")
	for i := 1; i < count; i++ {
		builder.WriteString("// budget fixture line\n")
	}
	return builder.String()
}

func writeOngoingBudgetFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
}

func ongoingLoCRepositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat go.mod: %v", err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repository root from %s", dir)
		}
		dir = parent
	}
}
