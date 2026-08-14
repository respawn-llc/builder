package architectureguard

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

func CheckNoProductionRecover(root string) error {
	root = filepath.Clean(root)
	var violations []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			identifier, ok := call.Fun.(*ast.Ident)
			if !ok || identifier.Name != "recover" {
				return true
			}
			position := fileSet.Position(call.Pos())
			relative, relativeErr := filepath.Rel(root, position.Filename)
			if relativeErr != nil {
				relative = position.Filename
			}
			violations = append(violations, fmt.Sprintf("%s:%d", relative, position.Line))
			return true
		})
		return nil
	})
	if err != nil {
		return err
	}
	if len(violations) == 0 {
		return nil
	}
	sort.Strings(violations)
	return errors.New("recover is forbidden in production Go code:\n" + strings.Join(violations, "\n"))
}
