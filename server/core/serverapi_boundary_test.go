package core_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedServerAPIContainsOnlyWireContracts(t *testing.T) {
	repoRoot := findRepoRoot(t)
	root := filepath.Join(repoRoot, "shared", "serverapi")
	violations := make([]string, 0)
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			relPath = path
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			switch importPath {
			case "log", "log/slog":
				violations = append(violations, relPath+": shared wire contract must not import logging package "+importPath)
			}
			if strings.HasPrefix(importPath, "core/server/") {
				violations = append(violations, relPath+": shared wire contract must not import server package "+importPath)
			}
		}
		for _, decl := range file.Decls {
			switch typed := decl.(type) {
			case *ast.GenDecl:
				if typed.Tok != token.TYPE {
					continue
				}
				for _, spec := range typed.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					name := typeSpec.Name.Name
					if strings.HasSuffix(name, "Service") {
						violations = append(violations, relPath+": shared/serverapi must not declare service type "+name)
					}
					if isServerOwnedExecutionTypeName(name) {
						violations = append(violations, relPath+": server-owned execution type "+name+" belongs in server package")
					}
					if _, ok := typeSpec.Type.(*ast.InterfaceType); ok && !isAllowedServerAPIInterfaceName(name) {
						violations = append(violations, relPath+": interface "+name+" must be a wire subscription/progress sink, not execution policy")
					}
					iface, ok := typeSpec.Type.(*ast.InterfaceType)
					if !ok {
						continue
					}
					for _, method := range iface.Methods.List {
						if len(method.Names) != 1 {
							continue
						}
						methodName := method.Names[0].Name
						switch methodName {
						case "SubmitUserMessage", "DroppedEvents", "Logf":
							violations = append(violations, relPath+": runtime lifecycle method "+name+"."+methodName+" belongs in server package")
						case "Close":
							if !strings.HasSuffix(name, "Subscription") {
								violations = append(violations, relPath+": close semantics on "+name+" belong in server package")
							}
						}
					}
				}
			case *ast.FuncDecl:
				name := typed.Name.Name
				if strings.HasPrefix(name, "New") && strings.HasSuffix(name, "Service") {
					violations = append(violations, relPath+": shared/serverapi must not construct service "+name)
				}
				if strings.HasPrefix(name, "New") && isServerOwnedExecutionTypeName(strings.TrimPrefix(name, "New")) {
					violations = append(violations, relPath+": shared/serverapi must not construct server-owned execution type "+name)
				}
				if typed.Recv != nil && len(typed.Recv.List) == 1 && strings.HasSuffix(receiverTypeNameForServerAPITest(typed.Recv.List[0].Type), "Service") {
					violations = append(violations, relPath+": shared/serverapi must not implement service method "+name)
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case pkg.Name == "context" && selector.Sel.Name == "WithTimeout":
				violations = append(violations, relPath+": timeout policy context.WithTimeout belongs in server package")
			case pkg.Name == "time" && (selector.Sel.Name == "Now" || selector.Sel.Name == "Since"):
				violations = append(violations, relPath+": runtime timing policy time."+selector.Sel.Name+" belongs in server package")
			case selector.Sel.Name == "Logf":
				violations = append(violations, relPath+": logging policy Logf belongs in server package")
			case selector.Sel.Name == "Close":
				violations = append(violations, relPath+": close orchestration belongs in server package")
			}
			return true
		})
		return nil
	}); err != nil {
		t.Fatalf("scan shared/serverapi boundary: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("shared/serverapi boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestServiceContractPackageContainsOnlyRouteInterfaces(t *testing.T) {
	repoRoot := findRepoRoot(t)
	root := filepath.Join(repoRoot, "shared", "apicontract")
	violations := make([]string, 0)
	foundServiceContracts := false
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			relPath = path
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		if filepath.Base(path) != "service_contracts.go" {
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.TYPE {
					continue
				}
				for _, spec := range gen.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if ok && strings.HasSuffix(typeSpec.Name.Name, "Service") {
						violations = append(violations, relPath+": service contract "+typeSpec.Name.Name+" must live in service_contracts.go")
					}
				}
			}
			return nil
		}
		foundServiceContracts = true
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if importPath != "context" && importPath != "core/shared/serverapi" {
				violations = append(violations, relPath+": service contract may only import context and serverapi, got "+importPath)
			}
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if ok && gen.Tok == token.IMPORT {
				continue
			}
			if !ok || gen.Tok != token.TYPE {
				violations = append(violations, relPath+": service contract must contain type interface declarations only")
				continue
			}
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if _, ok := typeSpec.Type.(*ast.InterfaceType); !ok {
					violations = append(violations, relPath+": "+typeSpec.Name.Name+" must be an interface")
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("scan service contract boundary: %v", err)
	}
	if !foundServiceContracts {
		violations = append(violations, "shared/apicontract/service_contracts.go is missing")
	}
	if len(violations) > 0 {
		t.Fatalf("shared/apicontract service boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func receiverTypeNameForServerAPITest(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return receiverTypeNameForServerAPITest(typed.X)
	default:
		return ""
	}
}

func isAllowedServerAPIInterfaceName(name string) bool {
	return strings.HasSuffix(name, "Subscription") ||
		strings.HasSuffix(name, "ProgressSink")
}

func isServerOwnedExecutionTypeName(name string) bool {
	for _, term := range []string{
		"Broker",
		"Controller",
		"Engine",
		"Gate",
		"Handle",
		"Headless",
		"Launcher",
		"Lifecycle",
		"Log",
		"Manager",
		"Orchestrator",
		"Policy",
		"PromptAssistantMessage",
		"Runner",
		"RuntimeHandle",
		"RuntimeSession",
		"Service",
		"Registry",
		"Timeout",
	} {
		if strings.Contains(name, term) {
			return true
		}
	}
	return false
}
