package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type diagnostic struct {
	Rule     string
	Path     string
	Function string
	Detail   string
}

type productionFunction struct {
	path string
	name string
	body *ast.BlockStmt
}

var rawConstructorOwners = map[string]bool{
	"server/metadata/session_append_projection.go:projectSessionAppend": true,
	"server/metadata/operation.go:BeginImmediateTransaction":            true,
	"server/metadata/operation.go:RunOperation":                         true,
}

var rawDatabaseOwners = map[string]bool{
	"server/metadata/session_append_projection.go:projectSessionAppend": true,
	"server/metadata/operation.go:BeginTransaction":                     true,
	"server/metadata/operation.go:BeginImmediateTransaction":            true,
	"server/metadata/operation.go:RunOperation":                         true,
}

var startupOwners = map[string]bool{
	"server/metadata/db.go:readMetadataVersion":                      true,
	"server/metadata/db.go:runMigrations":                            true,
	"server/metadata/db.go:repairWorkflowIdentityMigrationCollision": true,
}

func main() {
	root := "."
	if len(os.Args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: metadataownershiplint [repository-root]")
		os.Exit(2)
	}
	if len(os.Args) == 2 {
		root = os.Args[1]
	}
	diagnostics, err := lintRepository(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, item := range diagnostics {
		fmt.Fprintf(os.Stderr, "%s:%s: %s (%s)\n", item.Path, item.Function, item.Rule, item.Detail)
	}
	if len(diagnostics) != 0 {
		os.Exit(1)
	}
}

func lintRepository(root string) ([]diagnostic, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	functions, err := productionDatabaseFunctions(root)
	if err != nil {
		return nil, err
	}
	var diagnostics []diagnostic
	for _, function := range functions {
		key := function.path + ":" + function.name
		calls := calledSelectors(function.body)
		for _, raw := range []string{"BeginTx", "Conn", "ExecContext", "PrepareContext", "QueryContext", "QueryRowContext"} {
			if calls[raw] && !rawDatabaseOwners[key] {
				diagnostics = append(diagnostics, diagnostic{
					Rule:     "raw-database-call",
					Path:     function.path,
					Function: function.name,
					Detail:   raw,
				})
			}
		}
		for _, constructor := range invalidGeneratedQueryConstructors(function.body, rawConstructorOwners[key]) {
			diagnostics = append(diagnostics, diagnostic{
				Rule:     "invalid-generated-query-constructor",
				Path:     function.path,
				Function: function.name,
				Detail:   constructor,
			})
		}
		if startupOwners[key] && !calls["runStartupDatabaseOperation"] {
			diagnostics = append(diagnostics, diagnostic{
				Rule:     "startup-operation-without-owner",
				Path:     function.path,
				Function: function.name,
			})
		}
		if function.path != "server/metadata/operation.go" &&
			(calls["BeginTransaction"] || calls["BeginImmediateTransaction"]) &&
			!calls["Settle"] {
			diagnostics = append(diagnostics, diagnostic{
				Rule:     "transaction-without-settlement",
				Path:     function.path,
				Function: function.name,
			})
		}
		if calls["WithTx"] && function.path != "server/metadata/operation.go" {
			diagnostics = append(diagnostics, diagnostic{
				Rule:     "transaction-query-constructor-outside-owner",
				Path:     function.path,
				Function: function.name,
			})
		}
	}
	generated, err := generatedQueryDiagnostics(root)
	if err != nil {
		return nil, err
	}
	diagnostics = append(diagnostics, generated...)
	sort.Slice(diagnostics, func(i, j int) bool {
		left, right := diagnostics[i], diagnostics[j]
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.Function != right.Function {
			return left.Function < right.Function
		}
		if left.Rule != right.Rule {
			return left.Rule < right.Rule
		}
		return left.Detail < right.Detail
	})
	return diagnostics, nil
}

func productionDatabaseFunctions(root string) ([]productionFunction, error) {
	var functions []productionFunction
	for _, relativeRoot := range []string{
		"server/metadata",
		"server/workflowstore",
		"server/workflowview",
		"server/worktree",
	} {
		searchRoot := filepath.Join(root, relativeRoot)
		err := filepath.WalkDir(searchRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				switch entry.Name() {
				case "querygen", "lifecyclegen", "sqlitegen", "sqlitelifecyclegen":
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			file, err := parseGoFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Body == nil {
					continue
				}
				functions = append(functions, productionFunction{
					path: filepath.ToSlash(relative),
					name: function.Name.Name,
					body: function.Body,
				})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return functions, nil
}

func generatedQueryDiagnostics(root string) ([]diagnostic, error) {
	generatedRoot := filepath.Join(root, "server", "metadata", "sqlitegen")
	entries, err := os.ReadDir(generatedRoot)
	if err != nil {
		return nil, err
	}
	var diagnostics []diagnostic
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" ||
			strings.HasSuffix(entry.Name(), "_test.go") ||
			entry.Name() == "db.go" ||
			entry.Name() == "diagnostics.go" ||
			entry.Name() == "models.go" ||
			entry.Name() == "sqlite_extensions.go" {
			continue
		}
		path := filepath.Join(generatedRoot, entry.Name())
		file, err := parseGoFile(path)
		if err != nil {
			return nil, err
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil || !queriesReceiver(function) {
				continue
			}
			calls := calledSelectors(function.Body)
			if !calls["beforeOperation"] {
				diagnostics = append(diagnostics, diagnostic{
					Rule:     "generated-query-without-operation-boundary",
					Path:     filepath.ToSlash(filepath.Join("server", "metadata", "sqlitegen", entry.Name())),
					Function: function.Name.Name,
					Detail:   "beforeOperation",
				})
			}
			if !calls["completeOperation"] {
				diagnostics = append(diagnostics, diagnostic{
					Rule:     "generated-query-without-operation-boundary",
					Path:     filepath.ToSlash(filepath.Join("server", "metadata", "sqlitegen", entry.Name())),
					Function: function.Name.Name,
					Detail:   "completeOperation",
				})
			}
		}
	}
	return diagnostics, nil
}

func invalidGeneratedQueryConstructors(node ast.Node, rawAllowed bool) []string {
	var invalid []string
	ast.Inspect(node, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "sqlitegen" {
			return true
		}
		switch selector.Sel.Name {
		case "NewRaw":
			if !rawAllowed {
				invalid = append(invalid, "NewRaw")
			}
		case "New":
			if len(call.Args) != 1 || !monitoredDBTXExpression(call.Args[0]) {
				invalid = append(invalid, "New")
			}
		}
		return true
	})
	return invalid
}

func monitoredDBTXExpression(expression ast.Expr) bool {
	composite, ok := expression.(*ast.CompositeLit)
	if !ok {
		return false
	}
	identifier, ok := composite.Type.(*ast.Ident)
	return ok && identifier.Name == "monitoredDBTX"
}

func parseGoFile(path string) (*ast.File, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return file, nil
}

func calledSelectors(node ast.Node) map[string]bool {
	calls := make(map[string]bool)
	ast.Inspect(node, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch function := call.Fun.(type) {
		case *ast.SelectorExpr:
			calls[function.Sel.Name] = true
		case *ast.Ident:
			calls[function.Name] = true
		}
		return true
	})
	return calls
}

func queriesReceiver(function *ast.FuncDecl) bool {
	if function.Recv == nil || len(function.Recv.List) != 1 {
		return false
	}
	pointer, ok := function.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	receiver, ok := pointer.X.(*ast.Ident)
	return ok && receiver.Name == "Queries"
}
