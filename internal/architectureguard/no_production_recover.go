package architectureguard

import (
	"go/ast"
)

func CheckNoProductionRecover(root string) error {
	return checkProductionGo(root, goASTPolicy{
		errorHeading: "recover is forbidden in production Go code",
		inspect: func(source goSourceFile) []string {
			var violations []string
			ast.Inspect(source.file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				identifier, ok := call.Fun.(*ast.Ident)
				if !ok || identifier.Name != "recover" {
					return true
				}
				violations = append(violations, source.violation(call, ""))
				return true
			})
			return violations
		},
	})
}
