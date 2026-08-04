package core

import (
	"go/ast"
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowNodeContractsDoNotExposeLegacyFields(t *testing.T) {
	tests := []struct {
		path       string
		structName string
	}{
		{path: "../workflow/types.go", structName: "AgentNode"},
		{path: "../workflow/types.go", structName: "ScriptNode"},
		{path: "../workflow/types.go", structName: "NodeFields"},
		{path: "../workflowstore/store.go", structName: "NodeRecord"},
		{path: "../../shared/serverapi/workflow.go", structName: "WorkflowNode"},
		{path: "../../shared/serverapi/workflow.go", structName: "WorkflowGraphDraftNode"},
		{path: "../../shared/serverapi/workflow.go", structName: "WorkflowNodeAddRequest"},
		{path: "../../shared/serverapi/workflow.go", structName: "WorkflowNodeUpdateRequest"},
	}
	for _, test := range tests {
		t.Run(test.structName, func(t *testing.T) {
			file := parseGoFile(t, test.path)
			structType := findStruct(t, file, test.structName)
			for _, field := range structType.Fields.List {
				for _, name := range field.Names {
					if legacyNodeFieldNames[name.Name] {
						t.Fatalf("%s.%s remains in the Node contract", test.structName, name.Name)
					}
				}
				if field.Tag == nil {
					continue
				}
				tag, ok := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Lookup("json")
				if ok && legacyNodeJSONTags[tagName(tag)] {
					t.Fatalf("%s retains legacy JSON tag %q", test.structName, tag)
				}
			}
		})
	}

}

func TestWorkflowNodeContractDeletionBoundaryHasNoFallbackProjectionOrSaveSymbols(t *testing.T) {
	for _, path := range []string{"../workflow/types.go", "../workflowstore/store.go"} {
		t.Run(path, func(t *testing.T) {
			file := parseGoFile(t, path)
			ast.Inspect(file, func(node ast.Node) bool {
				declaration, ok := node.(*ast.FuncDecl)
				if !ok {
					return true
				}
				switch declaration.Name.Name {
				case "NodePromptTemplate", "NodeInputFields", "NodeOutputFields":
					t.Fatalf("%s declares deleted Node accessor %q", path, declaration.Name.Name)
				}
				return false
			})
		})
	}

	for _, test := range []struct {
		path       string
		structName string
	}{
		{path: "../workflowstore/graph_save.go", structName: "comparableWorkflowGraphSaveNode"},
	} {
		t.Run(test.path, func(t *testing.T) {
			structType := findStruct(t, parseGoFile(t, test.path), test.structName)
			for _, field := range structType.Fields.List {
				for _, name := range field.Names {
					if legacyNodeFieldNames[name.Name] {
						t.Fatalf("%s.%s remains in graph-save Node comparison", test.structName, name.Name)
					}
				}
			}
		})
	}

	t.Run("definition projection", func(t *testing.T) {
		file := parseGoFile(t, "../workflowview/definition_projection.go")
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.CompositeLit)
			if !ok {
				return true
			}
			selector, ok := literal.Type.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "WorkflowNode" {
				return true
			}
			for _, element := range literal.Elts {
				keyValue, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := keyValue.Key.(*ast.Ident)
				if ok && legacyNodeFieldNames[key.Name] {
					t.Fatalf("definition projection retains deleted Node field %q", key.Name)
				}
			}
			return true
		})
	})
}

func TestGeneratedWorkflowNodeQueryShapesDoNotExposeLegacyFields(t *testing.T) {
	tests := []struct {
		path    string
		structs []string
	}{
		{
			path:    "../metadata/sqlitegen/models.go",
			structs: []string{"WorkflowNode"},
		},
		{
			path: "../metadata/sqlitegen/queries.sql.go",
			structs: []string{
				"GetWorkflowNodeRow",
				"ListWorkflowNodesRow",
				"InsertWorkflowNodeParams",
				"UpdateWorkflowNodeParams",
				"UpsertWorkflowNodeParams",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			file := parseGoFile(t, test.path)
			for _, structName := range test.structs {
				structType := findStruct(t, file, structName)
				for _, field := range structType.Fields.List {
					for _, name := range field.Names {
						if legacyNodeFieldNames[name.Name] ||
							name.Name == "PromptTemplate" ||
							name.Name == "InputFieldsJson" ||
							name.Name == "OutputFieldsJson" {
							t.Fatalf("%s.%s remains in generated Node query shape", structName, name.Name)
						}
					}
				}
			}
		})
	}
}

func tagName(tag string) string {
	if comma := strings.IndexByte(tag, ','); comma >= 0 {
		return tag[:comma]
	}
	return tag
}

var legacyNodeFieldNames = map[string]bool{
	"PromptTemplate": true,
	"InputFields":    true,
	"OutputFields":   true,
}

var legacyNodeJSONTags = map[string]bool{
	"prompt_template": true,
	"input_fields":    true,
	"output_fields":   true,
}
