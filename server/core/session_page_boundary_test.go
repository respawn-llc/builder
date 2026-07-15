package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"testing"

	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

func TestSessionPageContractRemovesUnboundedListingShapes(t *testing.T) {
	overview := reflect.TypeOf(clientui.ProjectOverview{})
	if _, present := overview.FieldByName("Sessions"); present {
		t.Fatal("ProjectOverview still exposes unbounded Sessions")
	}

	projectViews := reflect.TypeOf((*servicecontract.ProjectViewService)(nil)).Elem()
	if _, present := projectViews.MethodByName("ListSessionsByProject"); present {
		t.Fatal("ProjectViewService still exposes ListSessionsByProject")
	}
	if _, present := projectViews.MethodByName("ListSessionPage"); !present {
		t.Fatal("ProjectViewService does not expose ListSessionPage")
	}

	summary := reflect.TypeOf(clientui.SessionSummary{})
	sessionID, present := summary.FieldByName("SessionID")
	if !present || sessionID.Type != reflect.TypeOf(runtimeids.SessionID{}) {
		t.Fatalf("SessionSummary.SessionID type = %v, want runtimeids.SessionID", sessionID.Type)
	}
	category, present := summary.FieldByName("Category")
	if !present || category.Type != reflect.TypeOf(sessioncontract.SessionCategory("")) {
		t.Fatalf("SessionSummary.Category type = %v, want sessioncontract.SessionCategory", category.Type)
	}
}

func TestSessionPageMigrationRemovesProductionCallersOfOldListing(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	fileset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "target", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || filepath.Base(path) == "session_page_boundary_test.go" {
			return nil
		}
		if len(path) >= len("_test.go") && path[len(path)-len("_test.go"):] == "_test.go" {
			return nil
		}
		file, err := parser.ParseFile(fileset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.Ident:
				if value.Name == "listSessionSummaries" {
					t.Errorf("%s retains listSessionSummaries", path)
				}
			case *ast.SelectorExpr:
				if value.Sel.Name == "ListSessionsByProject" {
					t.Errorf("%s retains ListSessionsByProject", path)
				}
				if value.Sel.Name == "Sessions" {
					if parent, ok := value.X.(*ast.SelectorExpr); ok && parent.Sel.Name == "Overview" {
						t.Errorf("%s retains ProjectOverview.Sessions", path)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk production Go files: %v", err)
	}
}
