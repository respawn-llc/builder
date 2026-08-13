package runprompt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestRunPromptCanonicalizationHasOneOwnerPreparationBoundary(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "service.go", nil, 0)
	if err != nil {
		t.Fatalf("parse service.go: %v", err)
	}

	var runPrompt *ast.FuncDecl
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "runPrompt" {
			runPrompt = function
			break
		}
	}
	if runPrompt == nil {
		t.Fatal("runPrompt owner execution function is missing")
	}

	ast.Inspect(runPrompt.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if selector.Sel.Name == "TrimSpace" || selector.Sel.Name == "Validate" {
			t.Errorf("runPrompt must consume the canonical owner copy without calling %s again", selector.Sel.Name)
		}
		return true
	})
}
