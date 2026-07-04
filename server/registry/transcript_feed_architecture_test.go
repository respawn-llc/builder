package registry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

func TestRuntimeRegistryDoesNotBypassSessionFeedSequencerForTranscriptBroker(t *testing.T) {
	repoRoot := findRegistryRepoRoot(t)
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

func TestTranscriptSubscriptionHydrationDoesNotUseLegacyTranscriptReaders(t *testing.T) {
	repoRoot := findRegistryRepoRoot(t)
	forbidden := map[string]bool{
		"TranscriptSegmentPage":                true,
		"TranscriptSegmentPageForward":         true,
		"TranscriptSegmentPageFromStore":       true,
		"GetSessionTranscriptPage":             true,
		"GetSessionCommittedTranscriptSuffix":  true,
		"CommittedTranscriptSuffixFromRuntime": true,
		"ReadSegmentBackward":                  true,
		"ReadEventsBackwardUntil":              true,
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
	repoRoot := findRegistryRepoRoot(t)
	rel := filepath.Join("server", "runtimeview", "transcript_subscription.go")
	path := filepath.Join(repoRoot, rel)
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	forbidden := map[string]bool{
		"ChatEntry":          true,
		"ChatSnapshot":       true,
		"TranscriptMetadata": true,
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
	repoRoot := findRegistryRepoRoot(t)
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

func findRegistryRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}
