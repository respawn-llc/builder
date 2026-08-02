package registry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"core/internal/testharness/testsetup"
)

func TestRuntimeRegistryDoesNotBypassSessionFeedSequencerForTranscriptBroker(t *testing.T) {
	repoRoot := testsetup.RepositoryRoot(t)
	path := filepath.Join(repoRoot, "server", "registry", "runtime_registry.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse runtime registry: %v", err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if selector.Sel.Name != "Publish" && selector.Sel.Name != "Subscribe" {
			return true
		}
		inner, ok := selector.X.(*ast.SelectorExpr)
		if !ok || inner.Sel.Name != "transcript" {
			return true
		}
		t.Fatalf("runtime_registry.go calls transcript broker %s directly; route through sessionFeed sequencer", selector.Sel.Name)
		return false
	})
}

func TestProductionTranscriptPublishersDoNotConstructSequencedMessages(t *testing.T) {
	repoRoot := testsetup.RepositoryRoot(t)
	for _, rel := range []string{
		filepath.Join("server", "registry"),
		filepath.Join("server", "runtimeview"),
	} {
		root := filepath.Join(repoRoot, rel)
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || strings.HasSuffix(info.Name(), "_test.go") || !strings.HasSuffix(info.Name(), ".go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				lit, ok := node.(*ast.CompositeLit)
				if !ok {
					return true
				}
				selector, ok := lit.Type.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "TranscriptMessage" {
					return true
				}
				for _, element := range lit.Elts {
					if _, ok := element.(*ast.KeyValueExpr); ok {
						t.Errorf("%s constructs a sequenced TranscriptMessage literal; publish a TranscriptEvent instead", path)
						break
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("scan transcript publishers under %s: %v", rel, err)
		}
	}
}

func TestRuntimeReadModelTranscriptProjectionHasNoLegacyKinds(t *testing.T) {
	repoRoot := testsetup.RepositoryRoot(t)
	registryFiles, err := filepath.Glob(filepath.Join(repoRoot, "server", "registry", "*.go"))
	if err != nil {
		t.Fatalf("list registry files: %v", err)
	}
	for _, path := range registryFiles {
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", base, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || pkg.Name != "clientui" {
				return true
			}
			switch selector.Sel.Name {
			case "TranscriptMessageRuntimeActivity", "TranscriptMessageInputReconciliation":
				t.Fatalf("%s references deleted legacy runtime read-model transcript kind %s", base, selector.Sel.Name)
			}
			return true
		})
	}

	path := filepath.Join(repoRoot, "server", "registry", "session_feed_sequencer.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse session feed sequencer: %v", err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "runtimeActivity" || ident.Name == "inputReconciliation" {
			t.Fatalf("session feed sequencer retains legacy read-model state %s; store one canonical runtimeReadModel", ident.Name)
		}
		return true
	})
}

func TestTranscriptSubscriptionHydrationDoesNotUseLegacyTranscriptReaders(t *testing.T) {
	repoRoot := testsetup.RepositoryRoot(t)
	forbidden := map[string]bool{
		"TranscriptSegmentPage":             true,
		"TranscriptSegmentPageForward":      true,
		"TranscriptSegmentPageFromEventLog": true,
		"GetSessionTranscriptPage":          true,
		"ReadSegmentBackward":               true,
		"ReadRecentRecords":                 true,
	}
	for _, rel := range []string{
		filepath.Join("server", "runtime", "transcript_subscription.go"),
		filepath.Join("server", "registry", "runtime_registry.go"),
		filepath.Join("server", "runtimeview", "transcript_subscription.go"),
	} {
		path := filepath.Join(repoRoot, rel)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.SelectorExpr:
				if forbidden[typed.Sel.Name] {
					t.Fatalf("%s calls forbidden legacy transcript helper %s in new subscription path", rel, typed.Sel.Name)
				}
			case *ast.Ident:
				if forbidden[typed.Name] {
					t.Fatalf("%s references forbidden legacy transcript helper %s in new subscription path", rel, typed.Name)
				}
			}
			return true
		})
	}
}

func TestTranscriptRuntimeViewProjectionDoesNotUseLegacyChatEntryShape(t *testing.T) {
	repoRoot := testsetup.RepositoryRoot(t)
	rel := filepath.Join("server", "runtimeview", "transcript_subscription.go")
	path := filepath.Join(repoRoot, rel)
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	forbidden := map[string]bool{
		"ChatEntry":    true,
		"ChatSnapshot": true,
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.Ident:
			if forbidden[typed.Name] {
				t.Fatalf("%s references forbidden legacy projection type %s", rel, typed.Name)
			}
		case *ast.SelectorExpr:
			if typed.Sel.Name == "Role" {
				t.Fatalf("%s reads legacy role field in new transcript projection", rel)
			}
		}
		return true
	})
}

func TestLegacyUntypedNoticeIsOnlyProducedByFossilProjectionPath(t *testing.T) {
	repoRoot := testsetup.RepositoryRoot(t)
	allowedFunctions := map[string]bool{
		"legacyUntypedNoticeFactFromLocalEntry": true,
	}
	for _, rel := range []string{
		filepath.Join("server", "runtime", "transcript_delivery_facts.go"),
		filepath.Join("server", "runtimeview", "transcript_subscription.go"),
	} {
		path := filepath.Join(repoRoot, rel)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			current := fn.Name.Name
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				lit, ok := node.(*ast.BasicLit)
				if !ok || basicStringLiteralValue(lit) != "legacy_untyped_notice" {
					return true
				}
				if !allowedFunctions[current] {
					t.Fatalf("%s constructs legacy_untyped_notice in %s; only the fossil local-entry projection may produce it", rel, current)
				}
				return true
			})
		}
	}
}

func basicStringLiteralValue(lit *ast.BasicLit) string {
	if lit == nil || lit.Kind != token.STRING {
		return ""
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return value
}
