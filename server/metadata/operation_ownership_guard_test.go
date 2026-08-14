package metadata

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"core/server/metadata/sqlitegen"
)

type productionDatabaseFunction struct {
	path string
	name string
	body *ast.BlockStmt
}

func TestProductionMetadataDatabaseOperationsHaveOneOwner(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	functions := productionDatabaseFunctions(t, root)
	rawConstructors := map[string]bool{
		"server/metadata/session_append_projection.go:projectSessionAppend": true,
		"server/metadata/operation.go:BeginImmediateTransaction":            true,
		"server/metadata/operation.go:RunOperation":                         true,
	}
	rawDatabaseFunctions := map[string]bool{
		"server/metadata/session_append_projection.go:projectSessionAppend": true,
		"server/metadata/operation.go:BeginTransaction":                     true,
		"server/metadata/operation.go:BeginImmediateTransaction":            true,
		"server/metadata/operation.go:RunOperation":                         true,
	}
	startupOwners := map[string]bool{
		"server/metadata/db.go:readMetadataVersion":                      true,
		"server/metadata/db.go:runMigrations":                            true,
		"server/metadata/db.go:repairWorkflowIdentityMigrationCollision": true,
	}
	for _, function := range functions {
		key := function.path + ":" + function.name
		calls := calledSelectors(function.body)
		for _, raw := range []string{"BeginTx", "Conn", "ExecContext", "PrepareContext", "QueryContext", "QueryRowContext"} {
			if calls[raw] && !rawDatabaseFunctions[key] {
				t.Errorf("%s calls raw %s outside metadata's operation owner", key, raw)
			}
		}
		if invalid := invalidGeneratedQueryConstructors(function.body, rawConstructors[key]); len(invalid) != 0 {
			t.Errorf("%s has invalid generated-query constructors: %v", key, invalid)
		}
		if startupOwners[key] && !calls["runStartupDatabaseOperation"] {
			t.Errorf("%s does not enclose its direct startup database calls", key)
		}
		if function.path != "server/metadata/operation.go" &&
			(calls["BeginTransaction"] || calls["BeginImmediateTransaction"]) {
			if !calls["Settle"] {
				t.Errorf("%s opens a monitored transaction without settling the same owner", key)
			}
		}
		if calls["WithTx"] && function.path != "server/metadata/operation.go" {
			t.Errorf("%s constructs transaction queries outside metadata's owner", key)
		}
	}
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

func TestGeneratedDatabaseAdaptersOwnWholeCalls(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	generatedRoot := filepath.Join(root, "server", "metadata", "sqlitegen")
	entries, err := os.ReadDir(generatedRoot)
	if err != nil {
		t.Fatal(err)
	}
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
		file := parseGoFile(t, path)
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil || !queriesReceiver(function) {
				continue
			}
			calls := calledSelectors(function.Body)
			if !calls["beforeOperation"] || !calls["completeOperation"] {
				t.Errorf("%s:%s does not enclose its generated database call in one owner", entry.Name(), function.Name.Name)
			}
		}
	}
}

func TestGeneratedQueryOwnershipModesDoNotNest(t *testing.T) {
	t.Parallel()

	store := openInMemoryMetadataTestStore(t, t.TempDir())
	store.queries = sqlitegen.New(monitoredDBTX{DBTX: store.db, monitor: store})
	if !store.Queries().IsMonitored() {
		t.Fatal("nontransaction generated adapter is not monitored")
	}
	transaction, err := store.BeginTransaction(context.Background(), "ownership guard", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !transaction.Queries().IsRaw() || transaction.Queries().IsMonitored() {
		t.Fatal("transaction generated adapter must be raw and unmonitored")
	}
	var settleErr error
	transaction.Settle(context.Background(), &settleErr)
	if settleErr != nil {
		t.Fatal(settleErr)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve metadata guard path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func productionDatabaseFunctions(t *testing.T, root string) []productionDatabaseFunction {
	t.Helper()
	var functions []productionDatabaseFunction
	for _, relativeRoot := range []string{
		"server/metadata",
		"server/workflowstore",
		"server/workflowview",
		"server/worktree",
	} {
		searchRoot := filepath.Join(root, relativeRoot)
		err := filepath.WalkDir(searchRoot, func(path string, entry os.DirEntry, walkErr error) error {
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
			file := parseGoFile(t, path)
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Body == nil {
					continue
				}
				functions = append(functions, productionDatabaseFunction{
					path: filepath.ToSlash(relative),
					name: function.Name.Name,
					body: function.Body,
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return functions
}

func parseGoFile(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
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
