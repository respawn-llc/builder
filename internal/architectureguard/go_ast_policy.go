package architectureguard

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

type goASTPolicy struct {
	errorHeading string
	inspect      func(goSourceFile) []string
}

type goSourceFile struct {
	relativePath string
	fileSet      *token.FileSet
	file         *ast.File
}

func checkProductionGo(root string, policy goASTPolicy) error {
	root = filepath.Clean(root)
	buildContext := build.Default
	buildContext.CgoEnabled = true
	buildContext.UseAllFiles = true
	var violations []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		switch entry.Name() {
		case ".git", "node_modules", "vendor":
			return filepath.SkipDir
		}

		buildPackage, importErr := buildContext.ImportDir(path, 0)
		var noGoError *build.NoGoError
		if errors.As(importErr, &noGoError) {
			return nil
		}
		if importErr != nil {
			return fmt.Errorf("inspect Go package %s: %w", path, importErr)
		}

		names := append(append([]string{}, buildPackage.GoFiles...), buildPackage.CgoFiles...)
		sort.Strings(names)
		for _, name := range names {
			filename := filepath.Join(path, name)
			fileSet := token.NewFileSet()
			file, parseErr := parser.ParseFile(fileSet, filename, nil, 0)
			if parseErr != nil {
				return fmt.Errorf("parse %s: %w", filename, parseErr)
			}
			relative, relativeErr := filepath.Rel(root, filename)
			if relativeErr != nil {
				return relativeErr
			}
			violations = append(violations, policy.inspect(goSourceFile{
				relativePath: filepath.ToSlash(relative),
				fileSet:      fileSet,
				file:         file,
			})...)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(violations) == 0 {
		return nil
	}
	sort.Strings(violations)
	return errors.New(policy.errorHeading + ":\n" + strings.Join(violations, "\n"))
}

func (source goSourceFile) locationViolation(node ast.Node) string {
	position := source.fileSet.Position(node.Pos())
	return fmt.Sprintf("%s:%d", source.relativePath, position.Line)
}

func (source goSourceFile) detailedViolation(node ast.Node, detail string) string {
	return source.locationViolation(node) + ": " + detail
}
