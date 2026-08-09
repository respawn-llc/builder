package sessionruntime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestQuestionBatchValidationHasOneDefinitionAndCanonicalCallSites(t *testing.T) {
	files := []string{
		"prompt_answer_batch.go",
		"prompt_follow_up.go",
		"question_batch_validation.go",
	}
	var definitions int
	callers := make([]string, 0, 2)
	for _, filename := range files {
		file, err := parser.ParseFile(token.NewFileSet(), filename, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", filename, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if function.Name.Name == "validateQuestionBatchMetadata" {
				definitions++
			}
			if function.Body == nil {
				continue
			}
			found := false
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				identifier, ok := call.Fun.(*ast.Ident)
				if ok && identifier.Name == "validateQuestionBatchMetadata" {
					found = true
				}
				return true
			})
			if found {
				callers = append(callers, filepath.Base(filename)+":"+function.Name.Name)
			}
		}
	}
	sort.Strings(callers)
	wantCallers := []string{
		"prompt_answer_batch.go:promptResolutionForCommand",
		"prompt_follow_up.go:subscribePromptFollowUp",
	}
	if definitions != 1 {
		t.Fatalf("QuestionBatch validator definitions = %d, want 1", definitions)
	}
	if !reflect.DeepEqual(callers, wantCallers) {
		t.Fatalf("QuestionBatch validator callers = %v, want %v", callers, wantCallers)
	}
}

func TestFollowUpStateCarriesValidatedQuestionBatchDescriptor(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "prompt_follow_up.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse prompt_follow_up.go: %v", err)
	}
	for _, declaration := range file.Decls {
		generic, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, specification := range generic.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != "promptFollowUpState" {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				t.Fatal("promptFollowUpState is not a struct")
			}
			for _, field := range structType.Fields.List {
				if len(field.Names) != 1 || field.Names[0].Name != "descriptor" {
					continue
				}
				pointer, ok := field.Type.(*ast.StarExpr)
				if !ok {
					t.Fatal("promptFollowUpState descriptor is not a pointer")
				}
				identifier, ok := pointer.X.(*ast.Ident)
				if !ok || identifier.Name != "validatedQuestionBatchDescriptor" {
					t.Fatalf("promptFollowUpState descriptor type = %#v", field.Type)
				}
				return
			}
			t.Fatal("promptFollowUpState has no validated descriptor")
		}
	}
	t.Fatal("promptFollowUpState declaration missing")
}
