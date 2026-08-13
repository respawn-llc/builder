package core_test

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	testharness "core/internal/testharness/testsetup"

	"golang.org/x/tools/go/packages"
)

type jsonContractArchitectureViolationReason string

const jsonContractArchitectureJSONToken jsonContractArchitectureViolationReason = "json_token"
const jsonContractArchitectureGenericInspection jsonContractArchitectureViolationReason = "generic_json_inspection"
const jsonContractArchitectureRawSchema jsonContractArchitectureViolationReason = "raw_schema"
const jsonContractArchitectureProviderSchema jsonContractArchitectureViolationReason = "provider_schema_processing"
const jsonContractArchitectureEmbeddedSchema jsonContractArchitectureViolationReason = "embedded_schema"

type jsonContractArchitectureViolation struct {
	RelPath string
	Line    int
	Column  int
	Reason  jsonContractArchitectureViolationReason
}

func (v jsonContractArchitectureViolation) String() string {
	return fmt.Sprintf("%s:%d:%d: %s", v.RelPath, v.Line, v.Column, v.Reason)
}

func TestJSONContractArchitectureGuardRejectsJSONTokenWalker(t *testing.T) {
	pkgs, root := jsonContractArchitectureFixture(t, "server/example/parser.go", `package example

import (
	"bytes"
	"encoding/json"
)

func parse(raw []byte) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	_, _ = decoder.Token()
}
`)
	assertJSONContractArchitectureViolation(
		t,
		collectJSONContractArchitectureViolations(pkgs, root),
		jsonContractArchitectureJSONToken,
	)
}

func TestJSONContractArchitectureGuardAllowsExactTokenExclusionAndXML(t *testing.T) {
	t.Run("KENT-530 exclusion", func(t *testing.T) {
		pkgs, root := jsonContractArchitectureFixture(t, "server/session/parser.go", `package session

import (
	"bytes"
	"encoding/json"
)

func decodeEventLogHeaderFields(raw []byte) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	_, _ = decoder.Token()
}
`)
		if violations := collectJSONContractArchitectureViolations(pkgs, root); len(violations) != 0 {
			t.Fatalf("exact KENT-530 exclusion violations = %v, want none", violations)
		}
	})

	t.Run("KENT-530 exclusion is exact", func(t *testing.T) {
		pkgs, root := jsonContractArchitectureFixture(t, "server/session/parser.go", `package session

import (
	"bytes"
	"encoding/json"
)

func decodeAnotherHeader(raw []byte) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	_, _ = decoder.Token()
}
`)
		assertJSONContractArchitectureViolation(
			t,
			collectJSONContractArchitectureViolations(pkgs, root),
			jsonContractArchitectureJSONToken,
		)
	})

	t.Run("KENT-570 bounded provider metadata exclusion", func(t *testing.T) {
		pkgs, root := jsonContractArchitectureFixture(t, "server/llm/parser.go", `package llm

import (
	"bytes"
	"encoding/json"
)

func decodeCodexTurnStateMetadata(raw []byte) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	_, _ = decoder.Token()
}
`)
		if violations := collectJSONContractArchitectureViolations(pkgs, root); len(violations) != 0 {
			t.Fatalf("exact KENT-570 exclusion violations = %v, want none", violations)
		}
	})

	t.Run("KENT-570 exclusion is exact", func(t *testing.T) {
		pkgs, root := jsonContractArchitectureFixture(t, "server/llm/parser.go", `package llm

import (
	"bytes"
	"encoding/json"
)

func decodeAnotherProviderMetadata(raw []byte) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	_, _ = decoder.Token()
}
`)
		assertJSONContractArchitectureViolation(
			t,
			collectJSONContractArchitectureViolations(pkgs, root),
			jsonContractArchitectureJSONToken,
		)
	})

	t.Run("XML token reader", func(t *testing.T) {
		pkgs, root := jsonContractArchitectureFixture(t, "cli/kent/plist.go", `package kent

import (
	"bytes"
	"encoding/xml"
)

func parse(raw []byte) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	_, _ = decoder.Token()
}
`)
		if violations := collectJSONContractArchitectureViolations(pkgs, root); len(violations) != 0 {
			t.Fatalf("XML token reader violations = %v, want none", violations)
		}
	})
}

func TestJSONContractArchitectureGuardRejectsGenericDecodedObjectInspection(t *testing.T) {
	t.Run("direct inspection", func(t *testing.T) {
		pkgs, root := jsonContractArchitectureFixture(t, "server/example/parser.go", `package example

import "encoding/json"

func parse(raw []byte) {
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	_ = decoded["kind"]
}
`)
		assertJSONContractArchitectureViolation(
			t,
			collectJSONContractArchitectureViolations(pkgs, root),
			jsonContractArchitectureGenericInspection,
		)
	})

	t.Run("helper inspection", func(t *testing.T) {
		pkgs, root := jsonContractArchitectureFixture(t, "server/example/parser.go", `package example

import "encoding/json"

func parse(raw []byte) {
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	inspect(decoded)
}

func inspect(decoded map[string]any) {
	_ = decoded["kind"]
}
`)
		assertJSONContractArchitectureViolation(
			t,
			collectJSONContractArchitectureViolations(pkgs, root),
			jsonContractArchitectureGenericInspection,
		)
	})
}

func TestJSONContractArchitectureGuardRejectsRawSchemaMaps(t *testing.T) {
	t.Run("construction", func(t *testing.T) {
		pkgs, root := jsonContractArchitectureFixture(t, "server/example/schema.go", `package example

var schema = map[string]any{
	"type": "object",
	"properties": map[string]any{},
}
`)
		assertJSONContractArchitectureViolation(
			t,
			collectJSONContractArchitectureViolations(pkgs, root),
			jsonContractArchitectureRawSchema,
		)
	})

	t.Run("keyword mutation", func(t *testing.T) {
		pkgs, root := jsonContractArchitectureFixture(t, "server/example/schema.go", `package example

func closeSchema(schema map[string]any) {
	schema["additionalProperties"] = false
}
`)
		assertJSONContractArchitectureViolation(
			t,
			collectJSONContractArchitectureViolations(pkgs, root),
			jsonContractArchitectureRawSchema,
		)
	})
}

func TestJSONContractArchitectureGuardRejectsProviderSchemaProcessing(t *testing.T) {
	pkgs, root := jsonContractArchitectureFixture(t, "server/llm/schema.go", `package llm

import "encoding/json"

func parseSchema(raw []byte) {
	var schema map[string]any
	_ = json.Unmarshal(raw, &schema)
	_ = schema["properties"]
}
`)
	assertJSONContractArchitectureViolation(
		t,
		collectJSONContractArchitectureViolations(pkgs, root),
		jsonContractArchitectureProviderSchema,
	)
}

func TestJSONContractArchitectureGuardRejectsEmbeddedProductionSchema(t *testing.T) {
	pkgs, root := jsonContractArchitectureFixture(t, "server/example/example.go", "package example\n")
	testharness.WriteFile(t, filepath.Join(root, "server/tools/schemas/example.json"), `{"type":"object","properties":{}}`)
	assertJSONContractArchitectureViolation(
		t,
		collectJSONContractArchitectureViolations(pkgs, root),
		jsonContractArchitectureEmbeddedSchema,
	)
}

func TestJSONContractArchitectureGuardAllowsTypedOpaquePresentationAndExactProviderCases(t *testing.T) {
	tests := []struct {
		name    string
		relPath string
		source  string
	}{
		{
			name:    "typed decode",
			relPath: "server/example/parser.go",
			source: `package example

import "encoding/json"

type payload struct {
	Kind string ` + "`json:\"kind\"`" + `
}

func parse(raw []byte) string {
	var decoded payload
	_ = json.Unmarshal(raw, &decoded)
	return decoded.Kind
}
`,
		},
		{
			name:    "opaque RawMessage",
			relPath: "server/example/parser.go",
			source: `package example

import "encoding/json"

func carry(raw []byte) json.RawMessage {
	var decoded json.RawMessage
	_ = json.Unmarshal(raw, &decoded)
	return decoded
}
`,
		},
		{
			name:    "dependency value projection",
			relPath: "shared/jsoncontract/value.go",
			source: `package jsoncontract

import "encoding/json"

func project(raw []byte) any {
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	return decoded["value"]
}
`,
		},
		{
			name:    "KENT-535 functions",
			relPath: "server/llm/context.go",
			source: `package llm

import "encoding/json"

func parseContextWindowTokens(raw []byte) int {
	var decoded any
	_ = json.Unmarshal(raw, &decoded)
	return findPositiveIntByPreferredKeys(decoded)
}

func findPositiveIntByPreferredKeys(node any) int {
	if object, ok := node.(map[string]any); ok {
		if _, found := object["context_window"]; found {
			return 1
		}
	}
	return 0
}
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pkgs, root := jsonContractArchitectureFixture(t, test.relPath, test.source)
			if violations := collectJSONContractArchitectureViolations(pkgs, root); len(violations) != 0 {
				t.Fatalf("allowed JSON contract case violations = %v", violations)
			}
		})
	}
}

func TestProductionJSONContractArchitecture(t *testing.T) {
	repoRoot := findRepoRoot(t)
	for _, platform := range []struct {
		goos   string
		goarch string
	}{
		{goos: "darwin", goarch: "arm64"},
		{goos: "linux", goarch: "amd64"},
		{goos: "windows", goarch: "amd64"},
	} {
		t.Run(platform.goos+"/"+platform.goarch, func(t *testing.T) {
			pkgs := testharness.LoadTypedPackagesForPlatform(
				t,
				repoRoot,
				false,
				platform.goos,
				platform.goarch,
				"./server/...",
				"./cli/...",
				"./shared/...",
			)
			violations := collectJSONContractArchitectureViolations(pkgs, repoRoot)
			if len(violations) == 0 {
				return
			}
			formatted := make([]string, 0, len(violations))
			for _, violation := range violations {
				formatted = append(formatted, violation.String())
			}
			t.Fatalf("production JSON contract architecture violations:\n%s", strings.Join(formatted, "\n"))
		})
	}
}

func jsonContractArchitectureFixture(t *testing.T, relPath string, source string) ([]*packages.Package, string) {
	t.Helper()
	root := t.TempDir()
	testharness.WriteFile(t, filepath.Join(root, "go.mod"), "module core\n\ngo 1.26.4\n")
	testharness.WriteFile(t, filepath.Join(root, filepath.FromSlash(relPath)), source)
	return testharness.LoadTypedPackages(t, root, false, "./..."), root
}

func collectJSONContractArchitectureViolations(pkgs []*packages.Package, repoRoot string) []jsonContractArchitectureViolation {
	var violations []jsonContractArchitectureViolation
	seen := make(map[string]struct{})
	appendViolation := func(violation jsonContractArchitectureViolation) {
		key := violation.String()
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		violations = append(violations, violation)
	}
	for _, pkg := range pkgs {
		if !isProductionRepositoryPackage(pkg) || pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		taintedFunctions := jsonContractArchitectureTaintedFunctions(pkg)
		for _, file := range pkg.Syntax {
			relPath := jsonContractArchitectureRelativePath(repoRoot, pkg.Fset.Position(file.Pos()).Filename)
			if relPath == "" || strings.HasSuffix(relPath, "_test.go") {
				continue
			}
			if jsonContractArchitectureGeneratedFile(file) {
				continue
			}
			parents := jsonContractArchitectureParents(file)
			ast.Inspect(file, func(node ast.Node) bool {
				switch typed := node.(type) {
				case *ast.CompositeLit:
					if jsonContractArchitectureRawSchemaComposite(pkg, typed) {
						appendViolation(newJSONContractArchitectureViolation(
							pkg,
							typed.Pos(),
							relPath,
							jsonContractArchitectureRawSchema,
						))
					}
				case *ast.IndexExpr:
					if jsonContractArchitectureSchemaKeywordIndex(pkg, typed) {
						appendViolation(newJSONContractArchitectureViolation(
							pkg,
							typed.Pos(),
							relPath,
							jsonContractArchitectureRawSchema,
						))
					}
				}
				return true
			})
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Body == nil {
					continue
				}
				functionObject, _ := pkg.TypesInfo.Defs[function.Name].(*types.Func)
				genericDecode := taintedFunctions[functionObject]
				providerFunction := strings.HasPrefix(relPath, "server/llm/") &&
					!jsonContractArchitectureExactProviderExclusion(pkg.PkgPath, function.Name.Name)
				ast.Inspect(function.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if ok {
						called := jsonContractArchitectureCalledFunction(pkg, call)
						if called != nil && called.Pkg() != nil && called.Pkg().Path() == "encoding/json" && called.Name() == "Token" {
							if !jsonContractArchitectureExactTokenExclusion(pkg.PkgPath, function.Name.Name) {
								appendViolation(newJSONContractArchitectureViolation(
									pkg,
									call.Pos(),
									relPath,
									jsonContractArchitectureJSONToken,
								))
							}
						}
						if providerFunction && jsonContractArchitectureGenericJSONDecodeCall(pkg, call) {
							appendViolation(newJSONContractArchitectureViolation(
								pkg,
								call.Pos(),
								relPath,
								jsonContractArchitectureProviderSchema,
							))
						}
					}
					if jsonContractArchitectureAllowedGenericInspection(pkg.PkgPath, relPath, function.Name.Name) {
						return true
					}
					if !providerFunction && !genericDecode {
						return true
					}
					if !jsonContractArchitectureGenericStructuralInspection(pkg, node, parents) {
						return true
					}
					reason := jsonContractArchitectureGenericInspection
					if providerFunction {
						reason = jsonContractArchitectureProviderSchema
					}
					appendViolation(newJSONContractArchitectureViolation(
						pkg,
						node.Pos(),
						relPath,
						reason,
					))
					return true
				})
			}
		}
	}
	for _, violation := range jsonContractArchitectureEmbeddedSchemaViolations(repoRoot) {
		appendViolation(violation)
	}
	sort.Slice(violations, func(i, j int) bool {
		return violations[i].String() < violations[j].String()
	})
	return violations
}

func jsonContractArchitectureExactTokenExclusion(packagePath string, functionName string) bool {
	return (packagePath == "core/server/session" && functionName == "decodeEventLogHeaderFields") ||
		(packagePath == "core/server/llm" && functionName == "decodeCodexTurnStateMetadata")
}

func jsonContractArchitectureTaintedFunctions(pkg *packages.Package) map[*types.Func]bool {
	functions := make(map[*types.Func]*ast.FuncDecl)
	tainted := make(map[*types.Func]bool)
	for _, file := range pkg.Syntax {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			object, _ := pkg.TypesInfo.Defs[function.Name].(*types.Func)
			if object == nil {
				continue
			}
			functions[object] = function
			if jsonContractArchitectureFunctionHasGenericJSONDecode(pkg, function) {
				tainted[object] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for caller, function := range functions {
			if !tainted[caller] {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee := jsonContractArchitectureCalledFunction(pkg, call)
				if callee == nil || callee.Pkg() == nil || callee.Pkg().Path() != pkg.PkgPath || tainted[callee] {
					return true
				}
				for _, argument := range call.Args {
					if !jsonContractArchitectureGenericDecodeTarget(pkg.TypesInfo.TypeOf(argument)) {
						continue
					}
					tainted[callee] = true
					changed = true
					break
				}
				return true
			})
		}
	}
	return tainted
}

func jsonContractArchitectureGeneratedFile(file *ast.File) bool {
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, "// Code generated ") &&
				strings.HasSuffix(comment.Text, " DO NOT EDIT.") {
				return true
			}
		}
	}
	return false
}

func jsonContractArchitectureParents(file *ast.File) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node)
	var stack []ast.Node
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			parents[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})
	return parents
}

func jsonContractArchitectureFunctionHasGenericJSONDecode(pkg *packages.Package, function *ast.FuncDecl) bool {
	found := false
	ast.Inspect(function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok && jsonContractArchitectureGenericJSONDecodeCall(pkg, call) {
			found = true
			return false
		}
		return !found
	})
	return found
}

func jsonContractArchitectureGenericJSONDecodeCall(pkg *packages.Package, call *ast.CallExpr) bool {
	called := jsonContractArchitectureCalledFunction(pkg, call)
	if called == nil || called.Pkg() == nil || called.Pkg().Path() != "encoding/json" {
		return false
	}
	var target ast.Expr
	switch called.Name() {
	case "Unmarshal":
		if len(call.Args) < 2 {
			return false
		}
		target = call.Args[1]
	case "Decode":
		if len(call.Args) < 1 {
			return false
		}
		target = call.Args[0]
	default:
		return false
	}
	return jsonContractArchitectureGenericDecodeTarget(pkg.TypesInfo.TypeOf(target))
}

func jsonContractArchitectureGenericDecodeTarget(typ types.Type) bool {
	typ = types.Unalias(typ)
	if pointer, ok := typ.(*types.Pointer); ok {
		typ = types.Unalias(pointer.Elem())
	}
	return jsonContractArchitectureGenericMapType(typ) || jsonContractArchitectureEmptyInterface(typ)
}

func jsonContractArchitectureGenericMapType(typ types.Type) bool {
	typ = types.Unalias(typ)
	if named, ok := typ.(*types.Named); ok {
		typ = named.Underlying()
	}
	mapping, ok := typ.(*types.Map)
	if !ok {
		return false
	}
	key, ok := types.Unalias(mapping.Key()).Underlying().(*types.Basic)
	if !ok || key.Kind() != types.String {
		return false
	}
	value := types.Unalias(mapping.Elem())
	if jsonContractArchitectureEmptyInterface(value) {
		return true
	}
	named, ok := value.(*types.Named)
	return ok && named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == "encoding/json" &&
		named.Obj().Name() == "RawMessage"
}

func jsonContractArchitectureEmptyInterface(typ types.Type) bool {
	typ = types.Unalias(typ)
	if named, ok := typ.(*types.Named); ok {
		typ = named.Underlying()
	}
	value, ok := typ.(*types.Interface)
	return ok && value.NumMethods() == 0
}

func jsonContractArchitectureGenericStructuralInspection(
	pkg *packages.Package,
	node ast.Node,
	parents map[ast.Node]ast.Node,
) bool {
	switch typed := node.(type) {
	case *ast.IndexExpr:
		return jsonContractArchitectureGenericMapType(pkg.TypesInfo.TypeOf(typed.X)) &&
			!jsonContractArchitectureIndexWrite(typed, parents[typed])
	case *ast.RangeStmt:
		return jsonContractArchitectureGenericMapType(pkg.TypesInfo.TypeOf(typed.X))
	case *ast.TypeAssertExpr:
		return typed.Type != nil && jsonContractArchitectureGenericMapType(pkg.TypesInfo.TypeOf(typed.Type))
	case *ast.CallExpr:
		identifier, ok := typed.Fun.(*ast.Ident)
		return ok && identifier.Name == "len" && len(typed.Args) == 1 &&
			jsonContractArchitectureGenericMapType(pkg.TypesInfo.TypeOf(typed.Args[0]))
	default:
		return false
	}
}

func jsonContractArchitectureIndexWrite(index *ast.IndexExpr, parent ast.Node) bool {
	assignment, ok := parent.(*ast.AssignStmt)
	if !ok || assignment.Tok != token.ASSIGN {
		return false
	}
	for _, left := range assignment.Lhs {
		if left == index {
			return true
		}
	}
	return false
}

func jsonContractArchitectureAllowedGenericInspection(packagePath string, relPath string, functionName string) bool {
	if packagePath == "core/shared/jsoncontract" {
		return true
	}
	return jsonContractArchitectureExactProviderExclusion(packagePath, functionName)
}

func jsonContractArchitectureExactProviderExclusion(packagePath string, functionName string) bool {
	if packagePath != "core/server/llm" {
		return false
	}
	return functionName == "parseContextWindowTokens" ||
		functionName == "findPositiveIntByPreferredKeys"
}

var jsonContractArchitectureSchemaKeywords = map[string]struct{}{
	"$schema":               {},
	"$id":                   {},
	"$ref":                  {},
	"$defs":                 {},
	"type":                  {},
	"properties":            {},
	"additionalProperties":  {},
	"required":              {},
	"items":                 {},
	"oneOf":                 {},
	"anyOf":                 {},
	"allOf":                 {},
	"not":                   {},
	"enum":                  {},
	"const":                 {},
	"propertyNames":         {},
	"patternProperties":     {},
	"dependentSchemas":      {},
	"unevaluatedProperties": {},
}

func jsonContractArchitectureRawSchemaComposite(pkg *packages.Package, literal *ast.CompositeLit) bool {
	if !jsonContractArchitectureGenericMapType(pkg.TypesInfo.TypeOf(literal)) {
		return false
	}
	for _, element := range literal.Elts {
		keyValue, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if jsonContractArchitectureSchemaKeyword(pkg, keyValue.Key) {
			return true
		}
	}
	return false
}

func jsonContractArchitectureSchemaKeywordIndex(pkg *packages.Package, index *ast.IndexExpr) bool {
	return jsonContractArchitectureGenericMapType(pkg.TypesInfo.TypeOf(index.X)) &&
		jsonContractArchitectureSchemaKeyword(pkg, index.Index)
}

func jsonContractArchitectureSchemaKeyword(pkg *packages.Package, expression ast.Expr) bool {
	value := pkg.TypesInfo.Types[expression].Value
	if value == nil || value.Kind() != constant.String {
		return false
	}
	_, found := jsonContractArchitectureSchemaKeywords[constant.StringVal(value)]
	return found
}

func jsonContractArchitectureEmbeddedSchemaViolations(repoRoot string) []jsonContractArchitectureViolation {
	var violations []jsonContractArchitectureViolation
	for _, productionRoot := range []string{"server", "cli", "shared"} {
		root := filepath.Join(repoRoot, productionRoot)
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				switch entry.Name() {
				case ".git", "testdata", "vendor":
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".json" {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var object map[string]any
			if json.Unmarshal(raw, &object) != nil || !jsonContractArchitectureSchemaDocument(object) {
				return nil
			}
			relPath, ok := testharness.RepositoryRelativePath(repoRoot, path)
			if !ok {
				return nil
			}
			violations = append(violations, jsonContractArchitectureViolation{
				RelPath: relPath,
				Line:    1,
				Column:  1,
				Reason:  jsonContractArchitectureEmbeddedSchema,
			})
			return nil
		})
	}
	return violations
}

func jsonContractArchitectureSchemaDocument(object map[string]any) bool {
	if _, present := object["$schema"]; present {
		return true
	}
	if object["type"] == "object" {
		if _, present := object["properties"]; present {
			return true
		}
	}
	for _, keyword := range []string{"$ref", "$defs", "oneOf", "anyOf", "allOf"} {
		if _, present := object[keyword]; present {
			return true
		}
	}
	return false
}

func jsonContractArchitectureCalledFunction(pkg *packages.Package, call *ast.CallExpr) *types.Func {
	switch function := call.Fun.(type) {
	case *ast.Ident:
		called, _ := pkg.TypesInfo.Uses[function].(*types.Func)
		return called
	case *ast.SelectorExpr:
		called, _ := pkg.TypesInfo.Uses[function.Sel].(*types.Func)
		return called
	default:
		return nil
	}
}

func newJSONContractArchitectureViolation(
	pkg *packages.Package,
	position token.Pos,
	relPath string,
	reason jsonContractArchitectureViolationReason,
) jsonContractArchitectureViolation {
	source := pkg.Fset.Position(position)
	return jsonContractArchitectureViolation{
		RelPath: relPath,
		Line:    source.Line,
		Column:  source.Column,
		Reason:  reason,
	}
}

func jsonContractArchitectureRelativePath(repoRoot string, path string) string {
	relPath, ok := testharness.RepositoryRelativePath(repoRoot, path)
	if !ok {
		return ""
	}
	return relPath
}

func assertJSONContractArchitectureViolation(
	t *testing.T,
	violations []jsonContractArchitectureViolation,
	reason jsonContractArchitectureViolationReason,
) {
	t.Helper()
	for _, violation := range violations {
		if violation.Reason == reason {
			return
		}
	}
	t.Fatalf("JSON contract architecture violations = %v, want reason %q", violations, reason)
}
