package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strconv"
)

func annotateFile(path string) error {
	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read generated source: %w", err)
	}
	annotated, err := annotateSource(source)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, annotated, 0o644); err != nil {
		return fmt.Errorf("write annotated source: %w", err)
	}
	return nil
}

func annotateSource(source []byte) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "queries.sql.go", source, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse generated source: %w", err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || !isQueriesMethod(function) || function.Body == nil {
			continue
		}
		if function.Name.Name == "WithTx" {
			continue
		}
		if err := monitorGeneratedMethod(function); err != nil {
			return nil, err
		}
	}
	var output bytes.Buffer
	if err := format.Node(&output, fset, file); err != nil {
		return nil, fmt.Errorf("format annotated source: %w", err)
	}
	return output.Bytes(), nil
}

func monitorGeneratedMethod(function *ast.FuncDecl) error {
	if generatedMethodAlreadyMonitored(function.Body) {
		return nil
	}
	if function.Type.Results == nil || len(function.Type.Results.List) == 0 {
		return fmt.Errorf("generated query method %s has no error result", function.Name.Name)
	}
	errorResult := function.Type.Results.List[len(function.Type.Results.List)-1]
	errorType, ok := errorResult.Type.(*ast.Ident)
	if !ok || errorType.Name != "error" {
		return fmt.Errorf("generated query method %s does not return error last", function.Name.Name)
	}
	const resultErrorName = "metadataOperationErr"
	errorResult.Names = []*ast.Ident{ast.NewIdent(resultErrorName)}
	nameUnnamedResults(function.Type.Results)

	guardReturn := make([]ast.Expr, 0, resultCount(function.Type.Results))
	for _, result := range function.Type.Results.List[:len(function.Type.Results.List)-1] {
		count := len(result.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			guardReturn = append(guardReturn, zeroValue(result.Type))
		}
	}
	guardReturn = append(guardReturn, ast.NewIdent(resultErrorName))
	guard := &ast.IfStmt{
		Init: &ast.AssignStmt{
			Lhs: []ast.Expr{ast.NewIdent(resultErrorName)},
			Tok: token.ASSIGN,
			Rhs: []ast.Expr{&ast.CallExpr{
				Fun: &ast.SelectorExpr{X: ast.NewIdent("q"), Sel: ast.NewIdent("beforeOperation")},
			}},
		},
		Cond: &ast.BinaryExpr{
			X:  ast.NewIdent(resultErrorName),
			Op: token.NEQ,
			Y:  ast.NewIdent("nil"),
		},
		Body: &ast.BlockStmt{List: []ast.Stmt{
			&ast.ReturnStmt{Results: guardReturn},
		}},
	}
	complete := &ast.DeferStmt{Call: &ast.CallExpr{
		Fun: &ast.FuncLit{
			Type: &ast.FuncType{Params: &ast.FieldList{}},
			Body: &ast.BlockStmt{List: []ast.Stmt{
				&ast.AssignStmt{
					Lhs: []ast.Expr{ast.NewIdent(resultErrorName)},
					Tok: token.ASSIGN,
					Rhs: []ast.Expr{&ast.CallExpr{
						Fun: &ast.SelectorExpr{X: ast.NewIdent("q"), Sel: ast.NewIdent("completeOperation")},
						Args: []ast.Expr{
							ast.NewIdent("ctx"),
							&ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(function.Name.Name)},
							ast.NewIdent(resultErrorName),
						},
					}},
				},
			}},
		},
	}}
	function.Body.List = append([]ast.Stmt{guard, complete}, function.Body.List...)
	return nil
}

func generatedMethodAlreadyMonitored(body *ast.BlockStmt) bool {
	if body == nil || len(body.List) == 0 {
		return false
	}
	statement, ok := body.List[0].(*ast.IfStmt)
	if !ok {
		return false
	}
	assignment, ok := statement.Init.(*ast.AssignStmt)
	if !ok || len(assignment.Rhs) != 1 {
		return false
	}
	call, ok := assignment.Rhs[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "beforeOperation"
}

func nameUnnamedResults(results *ast.FieldList) {
	index := 0
	for _, result := range results.List[:len(results.List)-1] {
		if len(result.Names) != 0 {
			index += len(result.Names)
			continue
		}
		result.Names = []*ast.Ident{ast.NewIdent(fmt.Sprintf("metadataOperationResult%d", index))}
		index++
	}
}

func resultCount(results *ast.FieldList) int {
	count := 0
	for _, result := range results.List {
		if len(result.Names) == 0 {
			count++
		} else {
			count += len(result.Names)
		}
	}
	return count
}

func zeroValue(expression ast.Expr) ast.Expr {
	switch value := expression.(type) {
	case *ast.ArrayType, *ast.MapType, *ast.InterfaceType, *ast.FuncType, *ast.StarExpr:
		return ast.NewIdent("nil")
	case *ast.Ident:
		switch value.Name {
		case "string":
			return &ast.BasicLit{Kind: token.STRING, Value: `""`}
		case "bool":
			return ast.NewIdent("false")
		case "error":
			return ast.NewIdent("nil")
		case "int", "int8", "int16", "int32", "int64",
			"uint", "uint8", "uint16", "uint32", "uint64",
			"float32", "float64":
			return &ast.BasicLit{Kind: token.INT, Value: "0"}
		default:
			return &ast.CompositeLit{Type: expression}
		}
	default:
		return &ast.CompositeLit{Type: expression}
	}
}

func isQueriesMethod(function *ast.FuncDecl) bool {
	if function.Recv == nil || len(function.Recv.List) != 1 {
		return false
	}
	receiver, ok := function.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	receiverType, ok := receiver.X.(*ast.Ident)
	return ok && receiverType.Name == "Queries"
}
