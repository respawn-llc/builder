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
		annotateBlock(function.Body)
	}
	var output bytes.Buffer
	if err := format.Node(&output, fset, file); err != nil {
		return nil, fmt.Errorf("format annotated source: %w", err)
	}
	return output.Bytes(), nil
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

type queryCall struct {
	query ast.Expr
	args  []ast.Expr
}

func annotateBlock(block *ast.BlockStmt) {
	if block == nil {
		return
	}
	rows := map[string]queryCall{}
	annotated := make([]ast.Stmt, 0, len(block.List))
	for index, statement := range block.List {
		annotateNestedBlocks(statement)
		assignment, isAssignment := statement.(*ast.AssignStmt)
		if !isAssignment {
			annotated = append(annotated, statement)
			continue
		}
		if rowName, call, ok := queryRowAssignment(assignment); ok {
			rows[rowName] = call
		}
		if rowName, scan, ok := rowScanAssignment(assignment); ok {
			if call, found := rows[rowName]; found {
				scan.Rhs[0] = diagnosticCall(scan.Rhs[0], call)
			}
		}
		annotated = append(annotated, statement)
		if call, ok := databaseCallAssignment(assignment); ok && !isDiagnosticAssignment(nextStatement(block.List, index)) {
			annotated = append(annotated, wrapAssignedError(call))
		}
	}
	block.List = annotated
}

func nextStatement(statements []ast.Stmt, index int) ast.Stmt {
	next := index + 1
	if next >= len(statements) {
		return nil
	}
	return statements[next]
}

func isDiagnosticAssignment(statement ast.Stmt) bool {
	assignment, ok := statement.(*ast.AssignStmt)
	if !ok || assignment.Tok != token.ASSIGN || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
		return false
	}
	left, ok := assignment.Lhs[0].(*ast.Ident)
	if !ok || left.Name != "err" {
		return false
	}
	call, ok := assignment.Rhs[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	function, ok := call.Fun.(*ast.Ident)
	return ok && function.Name == "recordQueryError"
}

func annotateNestedBlocks(statement ast.Stmt) {
	switch statement := statement.(type) {
	case *ast.BlockStmt:
		annotateBlock(statement)
	case *ast.IfStmt:
		annotateBlock(statement.Body)
		annotateNestedBlocks(statement.Else)
	case *ast.ForStmt:
		annotateBlock(statement.Body)
	case *ast.RangeStmt:
		annotateBlock(statement.Body)
	case *ast.SwitchStmt:
		annotateBlock(statement.Body)
	case *ast.TypeSwitchStmt:
		annotateBlock(statement.Body)
	case *ast.SelectStmt:
		annotateBlock(statement.Body)
	case *ast.CaseClause:
		annotateBlock(&ast.BlockStmt{List: statement.Body})
	}
}

func queryRowAssignment(assignment *ast.AssignStmt) (string, queryCall, bool) {
	if len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
		return "", queryCall{}, false
	}
	rowName, ok := assignment.Lhs[0].(*ast.Ident)
	if !ok {
		return "", queryCall{}, false
	}
	call, ok := databaseMethodCall(assignment.Rhs[0], "QueryRowContext")
	if !ok {
		return "", queryCall{}, false
	}
	return rowName.Name, call, true
}

func rowScanAssignment(assignment *ast.AssignStmt) (string, *ast.AssignStmt, bool) {
	if len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
		return "", nil, false
	}
	errName, ok := assignment.Lhs[0].(*ast.Ident)
	if !ok || errName.Name != "err" {
		return "", nil, false
	}
	call, ok := assignment.Rhs[0].(*ast.CallExpr)
	if !ok {
		return "", nil, false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Scan" {
		return "", nil, false
	}
	row, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", nil, false
	}
	return row.Name, assignment, true
}

func databaseCallAssignment(assignment *ast.AssignStmt) (queryCall, bool) {
	if len(assignment.Rhs) != 1 || !assignmentAssignsError(assignment) {
		return queryCall{}, false
	}
	call, ok := databaseMethodCall(assignment.Rhs[0], "ExecContext")
	if ok {
		return call, true
	}
	return databaseMethodCall(assignment.Rhs[0], "QueryContext")
}

func assignmentAssignsError(assignment *ast.AssignStmt) bool {
	for _, left := range assignment.Lhs {
		identifier, ok := left.(*ast.Ident)
		if ok && identifier.Name == "err" {
			return true
		}
	}
	return false
}

func databaseMethodCall(expression ast.Expr, method string) (queryCall, bool) {
	call, ok := expression.(*ast.CallExpr)
	if !ok || len(call.Args) < 2 {
		return queryCall{}, false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != method {
		return queryCall{}, false
	}
	database, ok := selector.X.(*ast.SelectorExpr)
	if !ok || database.Sel.Name != "db" {
		return queryCall{}, false
	}
	queries, ok := database.X.(*ast.Ident)
	if !ok || queries.Name != "q" {
		return queryCall{}, false
	}
	return queryCall{query: call.Args[1], args: call.Args[2:]}, true
}

func diagnosticCall(cause ast.Expr, call queryCall) ast.Expr {
	arguments := []ast.Expr{
		ast.NewIdent("ctx"),
		cause,
		call.query,
		&ast.BasicLit{
			Kind:  token.INT,
			Value: strconv.Itoa(len(call.args)),
		},
	}
	return &ast.CallExpr{Fun: ast.NewIdent("recordQueryError"), Args: arguments}
}

func wrapAssignedError(call queryCall) ast.Stmt {
	return &ast.AssignStmt{
		Lhs: []ast.Expr{ast.NewIdent("err")},
		Tok: token.ASSIGN,
		Rhs: []ast.Expr{diagnosticCall(ast.NewIdent("err"), call)},
	}
}
