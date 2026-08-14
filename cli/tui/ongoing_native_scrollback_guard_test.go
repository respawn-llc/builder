package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestOngoingModeDoesNotExposeAppManagedViewportSeams(t *testing.T) {
	forbiddenTypes := map[string]string{
		"ScrollOngoingMsg":    "ongoing mode must not expose an app-managed scroll command",
		"SetOngoingScrollMsg": "ongoing mode must not expose a restore/apply scroll command",
	}
	forbiddenFields := map[string]string{
		"ongoingScroll":               "ongoing mode must not store app-owned transcript scroll",
		"snapOngoingOnViewportResize": "ongoing mode must not restore app-owned transcript scroll on resize",
	}

	for _, dir := range []string{".", "../app"} {
		pkgs := parseProductionPackages(t, dir)
		for _, pkg := range pkgs {
			for fileName, file := range pkg.Files {
				ast.Inspect(file, func(node ast.Node) bool {
					switch typed := node.(type) {
					case *ast.TypeSpec:
						if reason, forbidden := forbiddenTypes[typed.Name.Name]; forbidden {
							t.Fatalf("%s reintroduced %s: %s", fileName, typed.Name.Name, reason)
						}
					case *ast.Field:
						for _, name := range typed.Names {
							if reason, forbidden := forbiddenFields[name.Name]; forbidden {
								t.Fatalf("%s reintroduced %s: %s", fileName, name.Name, reason)
							}
						}
					}
					return true
				})
			}
		}
	}

	appPkgs := parseProductionPackages(t, "../app")
	for _, pkg := range appPkgs {
		for fileName, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "OngoingScroll" {
					return true
				}
				t.Fatalf("%s uses OngoingScroll from production app code; ongoing history navigation belongs to native terminal scrollback", fileName)
				return true
			})
		}
	}
}

func parseProductionPackages(t *testing.T, dir string) map[string]*ast.Package {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), filepath.Clean(dir), func(info fs.FileInfo) bool {
		name := info.Name()
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	}, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	return pkgs
}
