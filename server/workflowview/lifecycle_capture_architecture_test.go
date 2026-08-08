package workflowview

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestLifecycleSensitiveReadModelsUseSharedCaptureBoundary(t *testing.T) {
	t.Parallel()
	requiredCaptures := map[string]int{
		"attention.go":         2,
		"task_detail.go":       2,
		"task_list.go":         1,
		"task_search.go":       1,
		"board.go":             2,
		"task_dependencies.go": 2,
	}
	for fileName, minimumCaptures := range requiredCaptures {
		fileName, minimumCaptures := fileName, minimumCaptures
		t.Run(fileName, func(t *testing.T) {
			t.Parallel()
			file := parseWorkflowViewArchitectureFile(t, fileName)
			captures := 0
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch selector.Sel.Name {
				case "WithSnapshot", "WithLifecycleQuery", "WithBoundedLifecycle":
					captures++
				case "BeginTx", "WithTx", "ObserveWorkflowTaskExecutions",
					"CurrentWorkflowTaskExecutionSnapshots",
					"CurrentProjectWorkflowTaskExecutionSnapshots":
					t.Errorf(
						"%s bypasses the shared lifecycle capture through %s",
						fileName,
						selector.Sel.Name,
					)
				}
				return true
			})
			if captures < minimumCaptures {
				t.Fatalf(
					"%s has %d shared lifecycle captures, want at least %d",
					fileName,
					captures,
					minimumCaptures,
				)
			}
		})
	}
}

func TestPagedTaskReadsUseBoundedLifecycleQuery(t *testing.T) {
	t.Parallel()
	for _, fileName := range []string{"task_list.go", "task_search.go"} {
		fileName := fileName
		t.Run(fileName, func(t *testing.T) {
			t.Parallel()
			file := parseWorkflowViewArchitectureFile(t, fileName)
			boundedCaptures := 0
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch selector.Sel.Name {
				case "WithLifecycleQuery":
					boundedCaptures++
				case "WithSnapshot":
					t.Errorf("%s materializes the complete lifecycle set through WithSnapshot", fileName)
				}
				return true
			})
			if boundedCaptures != 1 {
				t.Fatalf("%s bounded lifecycle captures = %d, want 1", fileName, boundedCaptures)
			}
		})
	}
}

func TestPagedLifecycleReadsNeverUseGlobalSnapshotMaterialization(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		fileName    string
		function    string
		captureName string
	}{
		{fileName: "attention.go", function: "List", captureName: "WithBoundedLifecycle"},
		{fileName: "board.go", function: "ListNodeCards", captureName: "WithBoundedLifecycle"},
	} {
		testCase := testCase
		t.Run(testCase.fileName+"."+testCase.function, func(t *testing.T) {
			t.Parallel()
			file := parseWorkflowViewArchitectureFile(t, testCase.fileName)
			captures := 0
			found := false
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Name.Name != testCase.function {
					continue
				}
				found = true
				ast.Inspect(function.Body, func(node ast.Node) bool {
					selector, ok := node.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					switch selector.Sel.Name {
					case testCase.captureName:
						captures++
					case "WithSnapshot":
						t.Errorf(
							"%s.%s materializes the complete lifecycle set through WithSnapshot",
							testCase.fileName,
							testCase.function,
						)
					}
					return true
				})
			}
			if !found {
				t.Fatalf("%s.%s is missing", testCase.fileName, testCase.function)
			}
			if captures != 1 {
				t.Fatalf(
					"%s.%s bounded lifecycle captures = %d, want 1",
					testCase.fileName,
					testCase.function,
					captures,
				)
			}
		})
	}
}

func TestReadModelsDoNotJoinLifecycleCaptureToAuthority(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list workflow view files: %v", err)
	}
	files = append(files, filepath.Join("..", "workflowexecution", "task_execution_observation.go"))
	for _, path := range files {
		if filepath.Ext(path) != ".go" ||
			filepath.Base(path) == "lifecycle_capture_architecture_test.go" ||
			strings.HasSuffix(filepath.Base(path), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "WithWorkflowTaskExecutionSnapshots",
				"CurrentWorkflowTaskExecutionSnapshots",
				"CurrentProjectTaskExecutionSnapshots",
				"CurrentProjectWorkflowTaskExecutionSnapshots",
				"CurrentScopedTaskExecutionSnapshot",
				"CurrentScopedTaskExecutionSnapshots":
				t.Errorf("%s joins a lifecycle read to Authority through %s", path, selector.Sel.Name)
			}
			return true
		})
	}
}

func TestTaskStatusProjectionExposesNoSplitCaptureAPI(t *testing.T) {
	t.Parallel()
	file := parseWorkflowViewArchitectureFile(t, "task_status_projection.go")
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil {
			continue
		}
		switch function.Name.Name {
		case "Observe", "WithDurableSnapshot":
			t.Fatalf(
				"TaskStatusProjection exposes split lifecycle capture method %s",
				function.Name.Name,
			)
		}
	}
}

func parseWorkflowViewArchitectureFile(t *testing.T, fileName string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(
		token.NewFileSet(),
		filepath.Join(".", fileName),
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("parse %s: %v", fileName, err)
	}
	return file
}
