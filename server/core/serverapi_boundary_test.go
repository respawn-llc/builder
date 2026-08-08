package core_test

import (
	"go/ast"
	"go/token"
	"go/types"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"

	testharness "core/internal/testharness/testsetup"

	"golang.org/x/tools/go/packages"
)

func TestSharedServerAPIContainsOnlyWireContracts(t *testing.T) {
	repoRoot := findRepoRoot(t)
	pkg := testharness.PackageByPath(t, testharness.LoadTypedPackages(t, repoRoot, false, "./shared/serverapi"), "core/shared/serverapi")
	violations := sharedServerAPIImportViolations(pkg)
	nonWireTypes := sharedServerAPINonWireTypes(pkg)
	for _, file := range pkg.Syntax {
		violations = append(violations, sharedServerAPITypeViolations(pkg, file, nonWireTypes)...)
		violations = append(violations, sharedServerAPIFunctionViolations(pkg, file, nonWireTypes)...)
		violations = append(violations, sharedServerAPIPolicyCallViolations(pkg, file)...)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("shared/serverapi boundary violations:\n%s", strings.Join(violations, "\n"))
	}
	assertSharedServerAPIWireFixtures(t)
}

func TestServiceContractPackageContainsOnlyRouteInterfaces(t *testing.T) {
	repoRoot := findRepoRoot(t)
	pkg := testharness.PackageByPath(t, testharness.LoadTypedPackages(t, repoRoot, false, "./shared/apicontract"), "core/shared/apicontract")
	violations := make([]string, 0)
	foundServiceContracts := false
	for index, file := range pkg.Syntax {
		filename := pkg.CompiledGoFiles[index]
		if path.Base(filename) != "service_contracts.go" {
			violations = append(violations, routeInterfaceDeclarationsOutsideContractFile(pkg, file)...)
			continue
		}
		foundServiceContracts = true
		violations = append(violations, serviceContractFileViolations(pkg, file)...)
	}
	if !foundServiceContracts {
		violations = append(violations, "shared/apicontract/service_contracts.go is missing")
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("shared/apicontract service boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func sharedServerAPIImportViolations(pkg *packages.Package) []string {
	violations := make([]string, 0)
	for importPath := range pkg.Imports {
		switch importPath {
		case "log", "log/slog":
			violations = append(violations, "shared/serverapi must not import logging package "+importPath)
		case "database/sql", "net", "net/http", "os", "os/exec", "sync":
			violations = append(violations, "shared/serverapi must not import execution capability package "+importPath)
		case "core/server":
			violations = append(violations, "shared/serverapi must not import server package "+importPath)
		default:
			if path.Dir(importPath) == "core/server" {
				violations = append(violations, "shared/serverapi must not import server package "+importPath)
			}
		}
	}
	return violations
}

func sharedServerAPINonWireTypes(pkg *packages.Package) map[*types.TypeName]bool {
	nonWireTypes := make(map[*types.TypeName]bool)
	for _, file := range pkg.Syntax {
		for _, declaration := range file.Decls {
			gen, ok := declaration.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				typeName, ok := pkg.TypesInfo.Defs[typeSpec.Name].(*types.TypeName)
				if !ok {
					continue
				}
				if _, interfaceType := typeName.Type().Underlying().(*types.Interface); interfaceType {
					continue
				}
				if _, concrete := types.Unalias(typeName.Type()).Underlying().(*types.Struct); !concrete {
					continue
				}
				if !isWireDataType(typeName.Type(), make(map[types.Type]struct{})) {
					nonWireTypes[typeName] = true
				}
			}
		}
	}
	return nonWireTypes
}

func sharedServerAPITypeViolations(pkg *packages.Package, file *ast.File, nonWireTypes map[*types.TypeName]bool) []string {
	violations := make([]string, 0)
	for _, declaration := range file.Decls {
		gen, ok := declaration.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			typeName, ok := pkg.TypesInfo.Defs[typeSpec.Name].(*types.TypeName)
			if !ok {
				continue
			}
			interfaceType, isInterface := typeName.Type().Underlying().(*types.Interface)
			if isInterface && !isWireStreamInterface(interfaceType) {
				violations = append(violations, testharness.SourcePosition(pkg, typeSpec.Pos()).String()+": shared/serverapi interfaces must describe wire stream endpoints")
			}
			if nonWireTypes[typeName] {
				violations = append(violations, testharness.SourcePosition(pkg, typeSpec.Pos()).String()+": shared/serverapi concrete declarations must remain wire data")
			}
		}
	}
	return violations
}

func isWireDataType(typ types.Type, seen map[types.Type]struct{}) bool {
	typ = types.Unalias(typ)
	if _, visited := seen[typ]; visited {
		return true
	}
	seen[typ] = struct{}{}
	switch typed := typ.(type) {
	case *types.Basic, *types.TypeParam:
		return true
	case *types.Pointer:
		return isWireDataType(typed.Elem(), seen)
	case *types.Slice:
		return isWireDataType(typed.Elem(), seen)
	case *types.Array:
		return isWireDataType(typed.Elem(), seen)
	case *types.Map:
		return isWireDataType(typed.Key(), seen) && isWireDataType(typed.Elem(), seen)
	case *types.Struct:
		for index := 0; index < typed.NumFields(); index++ {
			if !isWireDataType(typed.Field(index).Type(), seen) {
				return false
			}
		}
		return true
	case *types.Interface:
		typed.Complete()
		return typed.NumMethods() == 0 || isErrorType(typed)
	case *types.Named:
		if typed.Obj().Pkg() != nil {
			switch typed.Obj().Pkg().Path() {
			case "core/server", "core/shared/rpcwire":
				return typed.Obj().Pkg().Path() == "core/shared/rpcwire"
			}
		}
		return isWireDataType(typed.Underlying(), seen)
	default:
		return false
	}
}

func sharedServerAPIFunctionViolations(pkg *packages.Package, file *ast.File, nonWireTypes map[*types.TypeName]bool) []string {
	violations := make([]string, 0)
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		object, ok := pkg.TypesInfo.Defs[function.Name].(*types.Func)
		if !ok {
			continue
		}
		signature, ok := object.Type().(*types.Signature)
		if !ok {
			continue
		}
		position := testharness.SourcePosition(pkg, function.Name.Pos()).String()
		if function.Recv == nil {
			if returnsNonWireConcrete(signature, nonWireTypes) {
				violations = append(violations, position+": shared/serverapi must not construct concrete runtime objects")
			}
			if signatureUsesContext(signature) {
				violations = append(violations, position+": shared/serverapi must not expose context-bound execution functions")
			}
			continue
		}
		receiverType := serverAPIReceiverTypeName(pkg, function)
		if receiverType != nil && nonWireTypes[receiverType] {
			violations = append(violations, position+": shared/serverapi must not implement methods on non-wire concrete types")
		}
		if signatureUsesContext(signature) {
			violations = append(violations, position+": shared/serverapi methods must not own context-bound execution")
		}
	}
	return violations
}

func returnsNonWireConcrete(signature *types.Signature, nonWireTypes map[*types.TypeName]bool) bool {
	for index := 0; index < signature.Results().Len(); index++ {
		if nonWireTypeName(signature.Results().At(index).Type(), nonWireTypes) != nil {
			return true
		}
	}
	return false
}

func nonWireTypeName(typ types.Type, nonWireTypes map[*types.TypeName]bool) *types.TypeName {
	typ = types.Unalias(typ)
	if pointer, ok := typ.(*types.Pointer); ok {
		typ = types.Unalias(pointer.Elem())
	}
	named, ok := typ.(*types.Named)
	if !ok || !nonWireTypes[named.Obj()] {
		return nil
	}
	return named.Obj()
}

func serverAPIReceiverTypeName(pkg *packages.Package, function *ast.FuncDecl) *types.TypeName {
	if function.Recv == nil || len(function.Recv.List) != 1 {
		return nil
	}
	typ := types.Unalias(pkg.TypesInfo.TypeOf(function.Recv.List[0].Type))
	if pointer, ok := typ.(*types.Pointer); ok {
		typ = types.Unalias(pointer.Elem())
	}
	named, ok := typ.(*types.Named)
	if !ok {
		return nil
	}
	return named.Obj()
}

func signatureUsesContext(signature *types.Signature) bool {
	for index := 0; index < signature.Params().Len(); index++ {
		if isContextType(signature.Params().At(index).Type()) {
			return true
		}
	}
	return false
}

func assertSharedServerAPIWireFixtures(t *testing.T) {
	t.Helper()
	t.Run("rejects concrete execution owner", func(t *testing.T) {
		pkg := sharedServerAPIFixture(t, `package serverapi

import "context"

type executor struct {
	run func(context.Context) error
}

func newExecutor() executor {
	return executor{}
}

func (e *executor) execute(ctx context.Context) error {
	return e.run(ctx)
}
`)
		nonWireTypes := sharedServerAPINonWireTypes(pkg)
		var violations []string
		for _, file := range pkg.Syntax {
			violations = append(violations, sharedServerAPITypeViolations(pkg, file, nonWireTypes)...)
			violations = append(violations, sharedServerAPIFunctionViolations(pkg, file, nonWireTypes)...)
		}
		if len(violations) == 0 {
			t.Fatal("concrete execution owner must violate the shared serverapi boundary")
		}
	})

	t.Run("allows wire data", func(t *testing.T) {
		pkg := sharedServerAPIFixture(t, `package serverapi

type event struct {
	ID string
}
`)
		nonWireTypes := sharedServerAPINonWireTypes(pkg)
		var violations []string
		for _, file := range pkg.Syntax {
			violations = append(violations, sharedServerAPITypeViolations(pkg, file, nonWireTypes)...)
			violations = append(violations, sharedServerAPIFunctionViolations(pkg, file, nonWireTypes)...)
		}
		if len(violations) > 0 {
			t.Fatalf("wire data violations = %v, want none", violations)
		}
	})
}

func sharedServerAPIFixture(t *testing.T, source string) *packages.Package {
	t.Helper()
	root := t.TempDir()
	testharness.WriteFile(t, path.Join(root, "go.mod"), "module core\n\ngo 1.26.4\n")
	testharness.WriteFile(t, path.Join(root, "shared/serverapi/fixture.go"), source)
	pkgs := testharness.LoadTypedPackages(t, root, false, "./shared/serverapi")
	return testharness.PackageByPath(t, pkgs, "core/shared/serverapi")
}

func isWireStreamInterface(interfaceType *types.Interface) bool {
	interfaceType.Complete()
	if interfaceType.NumMethods() == 2 {
		next, nextFound := interfaceMethod(interfaceType, "Next")
		close, closeFound := interfaceMethod(interfaceType, "Close")
		return nextFound && closeFound && isWireNextMethod(next) && isWireCloseMethod(close)
	}
	if interfaceType.NumMethods() != 1 {
		return false
	}
	method := interfaceType.Method(0)
	signature, ok := method.Type().(*types.Signature)
	return ok && signature.Params().Len() == 1 && signature.Results().Len() == 0 && isServerAPIType(signature.Params().At(0).Type())
}

func interfaceMethod(interfaceType *types.Interface, name string) (*types.Func, bool) {
	for index := 0; index < interfaceType.NumMethods(); index++ {
		method := interfaceType.Method(index)
		if method.Name() == name {
			return method, true
		}
	}
	return nil, false
}

func isWireNextMethod(method *types.Func) bool {
	signature, ok := method.Type().(*types.Signature)
	return ok &&
		signature.Params().Len() == 1 &&
		isContextType(signature.Params().At(0).Type()) &&
		signature.Results().Len() == 2 &&
		isErrorType(signature.Results().At(1).Type())
}

func isWireCloseMethod(method *types.Func) bool {
	signature, ok := method.Type().(*types.Signature)
	return ok && signature.Params().Len() == 0 && signature.Results().Len() == 1 && isErrorType(signature.Results().At(0).Type())
}

func sharedServerAPIPolicyCallViolations(pkg *packages.Package, file *ast.File) []string {
	violations := make([]string, 0)
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		object := pkg.TypesInfo.Uses[selector.Sel]
		function, ok := object.(*types.Func)
		if !ok {
			return true
		}
		position := testharness.SourcePosition(pkg, selector.Sel.Pos()).String()
		switch {
		case function.Pkg() != nil && function.Pkg().Path() == "context" && function.Name() == "WithTimeout":
			violations = append(violations, position+": timeout policy belongs in a server package")
		case function.Pkg() != nil && function.Pkg().Path() == "time" && (function.Name() == "Now" || function.Name() == "Since"):
			violations = append(violations, position+": runtime timing policy belongs in a server package")
		case function.Name() == "Logf":
			violations = append(violations, position+": logging policy belongs in a server package")
		case function.Name() == "Close":
			violations = append(violations, position+": close orchestration belongs in a server package")
		}
		return true
	})
	return violations
}

func routeInterfaceDeclarationsOutsideContractFile(pkg *packages.Package, file *ast.File) []string {
	violations := make([]string, 0)
	ast.Inspect(file, func(node ast.Node) bool {
		typeSpec, ok := node.(*ast.TypeSpec)
		if !ok {
			return true
		}
		if _, ok := typeSpec.Type.(*ast.InterfaceType); ok {
			violations = append(violations, testharness.SourcePosition(pkg, typeSpec.Pos()).String()+": route interfaces must live in service_contracts.go")
		}
		return false
	})
	return violations
}

func serviceContractFileViolations(pkg *packages.Package, file *ast.File) []string {
	violations := serviceContractImportViolations(pkg, file)
	for _, declaration := range file.Decls {
		gen, ok := declaration.(*ast.GenDecl)
		if ok && gen.Tok == token.IMPORT {
			continue
		}
		if !ok || gen.Tok != token.TYPE {
			violations = append(violations, testharness.SourcePosition(pkg, declaration.Pos()).String()+": service contracts must contain interface declarations only")
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				violations = append(violations, testharness.SourcePosition(pkg, spec.Pos()).String()+": service contracts must contain interface declarations only")
				continue
			}
			interfaceType, ok := pkg.TypesInfo.Defs[typeSpec.Name].Type().Underlying().(*types.Interface)
			if !ok {
				violations = append(violations, testharness.SourcePosition(pkg, typeSpec.Pos()).String()+": service contracts must be interfaces")
				continue
			}
			violations = append(violations, routeMethodViolations(pkg, interfaceType)...)
		}
	}
	return violations
}

func serviceContractImportViolations(pkg *packages.Package, file *ast.File) []string {
	violations := make([]string, 0)
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			violations = append(violations, testharness.SourcePosition(pkg, spec.Pos()).String()+": invalid service contract import")
			continue
		}
		if importPath != "context" && importPath != "core/shared/serverapi" {
			violations = append(violations, testharness.SourcePosition(pkg, spec.Pos()).String()+": service contracts may only import context and serverapi")
		}
	}
	return violations
}

func routeMethodViolations(pkg *packages.Package, interfaceType *types.Interface) []string {
	interfaceType.Complete()
	violations := make([]string, 0)
	for index := 0; index < interfaceType.NumMethods(); index++ {
		method := interfaceType.Method(index)
		signature, ok := method.Type().(*types.Signature)
		if !ok || !isRouteMethod(signature) {
			violations = append(violations, testharness.SourcePosition(pkg, method.Pos()).String()+": route methods must take context plus serverapi DTOs and return serverapi DTOs or error")
		}
	}
	return violations
}

func isRouteMethod(signature *types.Signature) bool {
	if signature.Params().Len() < 2 || !isContextType(signature.Params().At(0).Type()) {
		return false
	}
	for index := 1; index < signature.Params().Len(); index++ {
		if !isServerAPIType(signature.Params().At(index).Type()) {
			return false
		}
	}
	switch signature.Results().Len() {
	case 1:
		return isErrorType(signature.Results().At(0).Type())
	case 2:
		return isServerAPIType(signature.Results().At(0).Type()) && isErrorType(signature.Results().At(1).Type())
	default:
		return false
	}
}

func isServerAPIType(typ types.Type) bool {
	switch typed := types.Unalias(typ).(type) {
	case *types.Named:
		return typed.Obj().Pkg() != nil && typed.Obj().Pkg().Path() == "core/shared/serverapi"
	case *types.Pointer:
		return isServerAPIType(typed.Elem())
	default:
		return false
	}
}

func isErrorType(typ types.Type) bool {
	interfaceType, ok := types.Unalias(typ).Underlying().(*types.Interface)
	if !ok {
		return false
	}
	interfaceType.Complete()
	if interfaceType.NumMethods() != 1 {
		return false
	}
	method := interfaceType.Method(0)
	signature, ok := method.Type().(*types.Signature)
	return ok &&
		method.Name() == "Error" &&
		signature.Params().Len() == 0 &&
		signature.Results().Len() == 1 &&
		isStringType(signature.Results().At(0).Type())
}
