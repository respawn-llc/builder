package core_test

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	testharness "core/internal/testharness/testsetup"

	"golang.org/x/tools/go/packages"
)

type wireBoundaryFinding string

const (
	wireExecutionImport wireBoundaryFinding = "wire_execution_import"
	wireConcreteState   wireBoundaryFinding = "wire_concrete_state"
	wireInterfaceShape  wireBoundaryFinding = "wire_interface_shape"
	wirePolicyCall      wireBoundaryFinding = "wire_policy_call"
	routeMethodShape    wireBoundaryFinding = "route_method_shape"
)

func TestWireAndRouteContractBoundaries(t *testing.T) {
	pkgs := testharness.LoadTypedPackages(t, findRepoRoot(t), false, "./shared/serverapi", "./shared/apicontract")
	var findings []wireBoundaryFinding
	findings = append(findings, serverAPIBoundaryFindings(testharness.PackageByPath(t, pkgs, "core/shared/serverapi"))...)
	findings = append(findings, routeContractFindings(testharness.PackageByPath(t, pkgs, "core/shared/apicontract"))...)
	if len(findings) != 0 {
		t.Fatalf("wire contract boundary violations: %v", findings)
	}
}

func TestWireBoundaryAnalyzerRejectsEachViolationKind(t *testing.T) {
	root := t.TempDir()
	testharness.WriteFile(t, filepath.Join(root, "go.mod"), "module core\n\ngo 1.26.4\n")
	testharness.WriteFile(t, filepath.Join(root, "server/base/base.go"), "package base")
	testharness.WriteFile(t, filepath.Join(root, "shared/serverapi/fixture.go"), `package serverapi
import ("context"; _ "core/server/base"; "time")
type Request struct{}; type BadState struct{ Stream chan Request }; type BadInterface interface{ Run() }
type BadChannel chan Request; type BadCallback func()
type BadCallbacks []BadCallback
type BadStream interface{ Next(context.Context) (BadCallbacks, error); Close() error }
func (BadState) Execute(context.Context) {}
func Stamp() time.Time { return time.Now() }
`)
	testharness.WriteFile(t, filepath.Join(root, "shared/apicontract/fixture.go"), `package apicontract
import ("context"; "core/shared/serverapi")
type customError struct{ error }
type Bad interface { Run(context.Context, serverapi.Request) customError }
`)
	pkgs := testharness.LoadTypedPackages(t, root, false, "./shared/...")
	findings := append(
		serverAPIBoundaryFindings(testharness.PackageByPath(t, pkgs, "core/shared/serverapi")),
		routeContractFindings(testharness.PackageByPath(t, pkgs, "core/shared/apicontract"))...,
	)
	got := map[wireBoundaryFinding]int{}
	for _, finding := range findings {
		got[finding]++
	}
	for _, want := range []wireBoundaryFinding{wireExecutionImport, wireConcreteState, wireInterfaceShape, wirePolicyCall} {
		if got[want] == 0 {
			t.Errorf("findings = %v, want %s", got, want)
		}
	}
	if got[routeMethodShape] == 0 {
		t.Errorf("findings = %v, want exact route error violation", got)
	}
}

func serverAPIBoundaryFindings(pkg *packages.Package) []wireBoundaryFinding {
	var findings []wireBoundaryFinding
	for imported := range pkg.Imports {
		switch {
		case imported == "core/server" || strings.HasPrefix(imported, "core/server/"):
			findings = append(findings, wireExecutionImport)
		case imported == "database/sql", imported == "log", imported == "log/slog", imported == "net",
			imported == "net/http", imported == "os", imported == "os/exec", imported == "sync":
			findings = append(findings, wireExecutionImport)
		}
	}
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(node ast.Node) bool {
			if declaration, ok := node.(*ast.FuncDecl); ok &&
				signatureUsesContext(pkg.TypesInfo.Defs[declaration.Name].Type().(*types.Signature)) {
				findings = append(findings, wireConcreteState)
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			function, ok := pkg.TypesInfo.Uses[selector.Sel].(*types.Func)
			if ok && function.Pkg() != nil && function.Pkg().Path() == "time" &&
				(function.Name() == "Now" || function.Name() == "Since") {
				findings = append(findings, wirePolicyCall)
			}
			return true
		})
	}
	for _, name := range pkg.Types.Scope().Names() {
		object, ok := pkg.Types.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		underlying := types.Unalias(object.Type()).Underlying()
		if iface, ok := underlying.(*types.Interface); ok {
			if !wireStreamInterface(iface) {
				findings = append(findings, wireInterfaceShape)
			}
			continue
		}
		switch underlying.(type) {
		case *types.Chan:
			findings = append(findings, wireConcreteState)
			continue
		}
		if _, concrete := underlying.(*types.Struct); concrete && !wireDataType(object.Type(), map[types.Type]bool{}) {
			findings = append(findings, wireConcreteState)
		}
	}
	return findings
}

func routeContractFindings(pkg *packages.Package) []wireBoundaryFinding {
	var findings []wireBoundaryFinding
	for _, name := range pkg.Types.Scope().Names() {
		object, ok := pkg.Types.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		iface, ok := types.Unalias(object.Type()).Underlying().(*types.Interface)
		if !ok {
			continue
		}
		iface.Complete()
		for index := 0; index < iface.NumMethods(); index++ {
			signature, ok := iface.Method(index).Type().(*types.Signature)
			if !ok || !routeSignature(signature) {
				findings = append(findings, routeMethodShape)
			}
		}
	}
	return findings
}

func routeSignature(signature *types.Signature) bool {
	if signature.Params().Len() < 2 || !isContextType(signature.Params().At(0).Type()) {
		return false
	}
	for index := 1; index < signature.Params().Len(); index++ {
		if !packageType(signature.Params().At(index).Type(), "core/shared/serverapi", "") {
			return false
		}
	}
	switch signature.Results().Len() {
	case 1:
		return errorType(signature.Results().At(0).Type())
	case 2:
		return packageType(signature.Results().At(0).Type(), "core/shared/serverapi", "") &&
			errorType(signature.Results().At(1).Type())
	default:
		return false
	}
}

func signatureUsesContext(signature *types.Signature) bool {
	for index := 0; index < signature.Params().Len(); index++ {
		if isContextType(signature.Params().At(index).Type()) {
			return true
		}
	}
	return false
}

func wireStreamInterface(iface *types.Interface) bool {
	iface.Complete()
	if iface.NumMethods() == 1 {
		signature, ok := iface.Method(0).Type().(*types.Signature)
		return ok && signature.Params().Len() == 1 && signature.Results().Len() == 0 &&
			packageType(signature.Params().At(0).Type(), "core/shared/serverapi", "")
	}
	if iface.NumMethods() != 2 {
		return false
	}
	var next, close *types.Signature
	for index := 0; index < iface.NumMethods(); index++ {
		signature, _ := iface.Method(index).Type().(*types.Signature)
		switch iface.Method(index).Name() {
		case "Next":
			next = signature
		case "Close":
			close = signature
		}
	}
	return next != nil && next.Params().Len() == 1 && isContextType(next.Params().At(0).Type()) &&
		next.Results().Len() == 2 && wireStreamResult(next.Results().At(0).Type()) &&
		errorType(next.Results().At(1).Type()) &&
		close != nil && close.Params().Len() == 0 && close.Results().Len() == 1 && errorType(close.Results().At(0).Type())
}

func wireDataType(typ types.Type, seen map[types.Type]bool) bool {
	typ = types.Unalias(typ)
	if seen[typ] {
		return true
	}
	seen[typ] = true
	switch typed := typ.(type) {
	case *types.Pointer:
		return wireDataType(typed.Elem(), seen)
	case *types.Slice:
		return wireDataType(typed.Elem(), seen)
	case *types.Array:
		return wireDataType(typed.Elem(), seen)
	case *types.Map:
		return wireDataType(typed.Key(), seen) && wireDataType(typed.Elem(), seen)
	case *types.Struct:
		for index := 0; index < typed.NumFields(); index++ {
			if !wireDataType(typed.Field(index).Type(), seen) {
				return false
			}
		}
		return true
	case *types.Interface:
		return typed.NumMethods() == 0
	case *types.Named:
		if errorType(typed) {
			return true
		}
		if typed.Obj().Pkg() != nil {
			switch typed.Obj().Pkg().Path() {
			case "core/shared/rpcwire":
				return true
			case "core/server":
				return false
			}
		}
		return wireDataType(typed.Underlying(), seen)
	default:
		_, basic := typ.(*types.Basic)
		_, parameter := typ.(*types.TypeParam)
		return basic || parameter
	}
}

func packageType(typ types.Type, packagePath, name string) bool {
	typ = types.Unalias(typ)
	if pointer, ok := typ.(*types.Pointer); ok {
		typ = types.Unalias(pointer.Elem())
	}
	named, ok := typ.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == packagePath &&
		(name == "" || named.Obj().Name() == name)
}

func wireStreamResult(typ types.Type) bool {
	named, ok := types.Unalias(typ).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	switch named.Underlying().(type) {
	case *types.Struct, *types.Basic:
		return named.Obj().Pkg().Path() == "core/shared/serverapi" || named.Obj().Pkg().Path() == "core/shared/clientui"
	default:
		return false
	}
}

func errorType(typ types.Type) bool {
	return types.Identical(types.Unalias(typ), types.Universe.Lookup("error").Type())
}
