package core_test

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	testharness "core/internal/testharness/testsetup"

	"golang.org/x/tools/go/packages"
)

func TestValidatedReceiptsCannotEscapeSynchronousCallbacks(t *testing.T) {
	repoRoot := findRepoRoot(t)
	loaded := testharness.LoadTypedPackages(t, repoRoot, false, "./server/...", "./shared/...")
	violations := make([]string, 0)
	for _, pkg := range loaded {
		if pkg.PkgPath == "core/shared/apicontract" {
			continue
		}
		for _, file := range pkg.Syntax {
			immediateConsumers := immediateValidatedConsumers(file)
			ast.Inspect(file, func(node ast.Node) bool {
				switch typed := node.(type) {
				case *ast.TypeSpec:
					if validatedReceiptStorageType(pkg.TypesInfo.TypeOf(typed.Type)) {
						violations = append(violations, validatedViolation(pkg, typed.Pos(), "Validated receipt cannot be stored in a named container"))
					}
				case *ast.FuncDecl:
					if fieldListContainsValidated(pkg, typed.Type.Results) {
						violations = append(violations, validatedViolation(pkg, typed.Pos(), "Validated receipt cannot be returned"))
					}
				case *ast.ChanType:
					if validatedReceiptType(pkg.TypesInfo.TypeOf(typed.Value)) {
						violations = append(violations, validatedViolation(pkg, typed.Pos(), "Validated receipt cannot be sent through a channel"))
					}
				case *ast.GoStmt:
					if containsValidatedReceiptOrTrustedCall(pkg, typed.Call) {
						violations = append(violations, validatedViolation(pkg, typed.Pos(), "Validated receipt or trusted invocation cannot escape into a goroutine"))
					}
				case *ast.FuncLit:
					if immediateConsumers[typed] {
						return true
					}
					if containsValidatedReceiptOrTrustedCall(pkg, typed.Body) {
						violations = append(violations, validatedViolation(pkg, typed.Pos(), "Validated receipt or trusted invocation must remain in an immediate WithValidated callback"))
					}
				case *ast.CallExpr:
					selector, ok := typed.Fun.(*ast.SelectorExpr)
					if ok && selector.Sel.IsExported() && selector.Sel.Name != "WithValidated" &&
						strings.HasSuffix(selector.Sel.Name, "Validated") &&
						!nodeInsideImmediateConsumer(file, typed, immediateConsumers) {
						violations = append(violations, validatedViolation(pkg, typed.Pos(), "trusted invocation must occur inside an immediate WithValidated callback"))
					}
				}
				return true
			})
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("validated receipt architecture violations:\n%s", strings.Join(violations, "\n"))
	}
}

func validatedViolation(pkg *packages.Package, pos token.Pos, message string) string {
	return testharness.SourcePosition(pkg, pos).String() + ": " + message
}

func immediateValidatedConsumers(file *ast.File) map[*ast.FuncLit]bool {
	result := make(map[*ast.FuncLit]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "WithValidated" {
			return true
		}
		for _, argument := range call.Args {
			if consumer, ok := argument.(*ast.FuncLit); ok {
				result[consumer] = true
			}
		}
		return true
	})
	return result
}

func nodeInsideImmediateConsumer(file *ast.File, target ast.Node, consumers map[*ast.FuncLit]bool) bool {
	for consumer := range consumers {
		if consumer.Body.Pos() <= target.Pos() && target.End() <= consumer.Body.End() {
			return true
		}
	}
	return false
}

func fieldListContainsValidated(pkg *packages.Package, fields *ast.FieldList) bool {
	if fields == nil {
		return false
	}
	for _, field := range fields.List {
		if validatedReceiptType(pkg.TypesInfo.TypeOf(field.Type)) {
			return true
		}
	}
	return false
}

func validatedReceiptStorageType(typ types.Type) bool {
	if typ == nil {
		return false
	}
	switch typed := types.Unalias(typ).Underlying().(type) {
	case *types.Struct:
		for index := 0; index < typed.NumFields(); index++ {
			if validatedReceiptType(typed.Field(index).Type()) {
				return true
			}
		}
	case *types.Slice:
		return validatedReceiptType(typed.Elem())
	case *types.Array:
		return validatedReceiptType(typed.Elem())
	case *types.Map:
		return validatedReceiptType(typed.Key()) || validatedReceiptType(typed.Elem())
	case *types.Chan:
		return validatedReceiptType(typed.Elem())
	}
	return false
}

func validatedReceiptType(typ types.Type) bool {
	if typ == nil {
		return false
	}
	switch typed := types.Unalias(typ).(type) {
	case *types.Named:
		object := typed.Obj()
		return object != nil && object.Pkg() != nil &&
			object.Pkg().Path() == "core/shared/apicontract" &&
			object.Name() == "Validated"
	case *types.Pointer:
		return validatedReceiptType(typed.Elem())
	case *types.Slice:
		return validatedReceiptType(typed.Elem())
	case *types.Array:
		return validatedReceiptType(typed.Elem())
	case *types.Map:
		return validatedReceiptType(typed.Key()) || validatedReceiptType(typed.Elem())
	case *types.Chan:
		return validatedReceiptType(typed.Elem())
	default:
		return false
	}
}

func containsValidatedReceiptOrTrustedCall(pkg *packages.Package, node ast.Node) bool {
	found := false
	ast.Inspect(node, func(candidate ast.Node) bool {
		if found {
			return false
		}
		if expression, ok := candidate.(ast.Expr); ok && validatedReceiptType(pkg.TypesInfo.TypeOf(expression)) {
			found = true
			return false
		}
		if call, ok := candidate.(*ast.CallExpr); ok {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.IsExported() &&
				selector.Sel.Name != "WithValidated" && strings.HasSuffix(selector.Sel.Name, "Validated") {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
