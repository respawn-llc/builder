package core_test

import (
	"go/ast"
	"go/token"
	"go/types"
	"path"
	"sort"
	"strconv"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestSharedServerAPIContainsOnlyWireContracts(t *testing.T) {
	repoRoot := findRepoRoot(t)
	pkg := structuredGuardPackageByPath(t, loadStructuredGuardPackages(t, repoRoot, false, "./shared/serverapi"), "core/shared/serverapi")
	violations := sharedServerAPIImportViolations(pkg)
	for _, file := range pkg.Syntax {
		violations = append(violations, sharedServerAPITypeViolations(pkg, file)...)
		violations = append(violations, sharedServerAPIPolicyCallViolations(pkg, file)...)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("shared/serverapi boundary violations:\n%s", joinStructuredGuardLines(violations))
	}
}

func TestServiceContractPackageContainsOnlyRouteInterfaces(t *testing.T) {
	repoRoot := findRepoRoot(t)
	pkg := structuredGuardPackageByPath(t, loadStructuredGuardPackages(t, repoRoot, false, "./shared/apicontract"), "core/shared/apicontract")
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
		t.Fatalf("shared/apicontract service boundary violations:\n%s", joinStructuredGuardLines(violations))
	}
}

func sharedServerAPIImportViolations(pkg *packages.Package) []string {
	violations := make([]string, 0)
	for importPath := range pkg.Imports {
		switch importPath {
		case "log", "log/slog":
			violations = append(violations, "shared/serverapi must not import logging package "+importPath)
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

func sharedServerAPITypeViolations(pkg *packages.Package, file *ast.File) []string {
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
			interfaceType, ok := pkg.TypesInfo.Defs[typeSpec.Name].Type().Underlying().(*types.Interface)
			if ok && !isWireStreamInterface(interfaceType) {
				violations = append(violations, structuredGuardPosition(pkg, typeSpec.Pos())+": shared/serverapi interfaces must describe wire stream endpoints")
			}
		}
	}
	return violations
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
		position := structuredGuardPosition(pkg, selector.Sel.Pos())
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
			violations = append(violations, structuredGuardPosition(pkg, typeSpec.Pos())+": route interfaces must live in service_contracts.go")
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
			violations = append(violations, structuredGuardPosition(pkg, declaration.Pos())+": service contracts must contain interface declarations only")
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				violations = append(violations, structuredGuardPosition(pkg, spec.Pos())+": service contracts must contain interface declarations only")
				continue
			}
			interfaceType, ok := pkg.TypesInfo.Defs[typeSpec.Name].Type().Underlying().(*types.Interface)
			if !ok {
				violations = append(violations, structuredGuardPosition(pkg, typeSpec.Pos())+": service contracts must be interfaces")
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
			violations = append(violations, structuredGuardPosition(pkg, spec.Pos())+": invalid service contract import")
			continue
		}
		if importPath != "context" && importPath != "core/shared/serverapi" {
			violations = append(violations, structuredGuardPosition(pkg, spec.Pos())+": service contracts may only import context and serverapi")
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
			violations = append(violations, structuredGuardPosition(pkg, method.Pos())+": route methods must take context plus serverapi DTOs and return serverapi DTOs or error")
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
	return types.Identical(types.Unalias(typ), types.Universe.Lookup("error").Type())
}
