package app

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestMainSurfaceTerminalWritesStayBehindApprovedInterfaces(t *testing.T) {
	repoRoot := mainSurfaceGuardRepositoryRoot(t)
	pkgs := loadMainSurfaceGuardPackages(t, repoRoot)
	violations := collectMainSurfaceTerminalWriteViolations(pkgs, repoRoot)
	if len(violations) == 0 {
		return
	}
	sort.Strings(violations)
	t.Fatalf("main-surface terminal writes must go through NativeScrollbackBuffer or the live-area control interface:\n%s", strings.Join(violations, "\n"))
}

func TestNativeScrollbackAssistantStreamingCallSitesAreLocked(t *testing.T) {
	repoRoot := mainSurfaceGuardRepositoryRoot(t)
	callSites := collectNativeScrollbackAssistantStreamingCallSites(t, repoRoot)

	actualContexts := make([]string, 0, len(callSites))
	for _, callSite := range callSites {
		actualContexts = append(actualContexts, callSite.context)
	}
	sort.Strings(actualContexts)

	expectedContexts := []string{
		"cli/app/ui_native_surface.go:StreamAssistantCommentaryContent",
		"cli/app/ui_native_surface.go:StreamAssistantFinalAnswerContent",
	}
	if len(actualContexts) == len(expectedContexts) {
		matches := true
		for index := range expectedContexts {
			if actualContexts[index] != expectedContexts[index] {
				matches = false
				break
			}
		}
		if matches {
			return
		}
	}

	details := make([]string, 0, len(callSites))
	for _, callSite := range callSites {
		details = append(details, fmt.Sprintf("%s:%d:%d", callSite.context, callSite.line, callSite.column))
	}
	sort.Strings(details)
	t.Fatalf("StreamMarkdownAssistantContent must have exactly two production selector call sites, one for commentary and one for final-answer streaming:\n%s", strings.Join(details, "\n"))
}

func loadMainSurfaceGuardPackages(t *testing.T, repoRoot string) []*packages.Package {
	t.Helper()

	pkgs, err := packages.Load(&packages.Config{
		Dir:   repoRoot,
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Tests: true,
	}, "./cli/app", "./cli/tui/...")
	if err != nil {
		t.Fatalf("load TUI packages: %v", err)
	}
	if errors := mainSurfaceGuardPackageErrors(pkgs); len(errors) > 0 {
		t.Fatalf("TUI packages must type-check before scanning main-surface terminal writes:\n%s", strings.Join(errors, "\n"))
	}
	return pkgs
}

type nativeScrollbackAssistantStreamingCallSite struct {
	context string
	line    int
	column  int
}

func collectNativeScrollbackAssistantStreamingCallSites(t *testing.T, repoRoot string) []nativeScrollbackAssistantStreamingCallSite {
	t.Helper()

	fileSet := token.NewFileSet()
	callSites := []nativeScrollbackAssistantStreamingCallSite{}
	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if shouldSkipMainSurfaceGuardDir(entry.Name()) && path != repoRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relPath, ok := mainSurfaceGuardRelativePath(repoRoot, path)
		if !ok {
			return nil
		}
		parsedFile, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse %s: %w", relPath, err)
		}
		callSites = append(callSites, nativeScrollbackAssistantStreamingCallSitesInFile(fileSet, parsedFile, relPath)...)
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go files for StreamMarkdownAssistantContent call sites: %v", err)
	}
	return callSites
}

func shouldSkipMainSurfaceGuardDir(name string) bool {
	switch name {
	case ".git", ".kent", ".claude", "bin", "dist", "node_modules", "vendor":
		return true
	default:
		return false
	}
}

func nativeScrollbackAssistantStreamingCallSitesInFile(fileSet *token.FileSet, file *ast.File, relPath string) []nativeScrollbackAssistantStreamingCallSite {
	callSites := []nativeScrollbackAssistantStreamingCallSite{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		context := relPath + ":" + fn.Name.Name
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "StreamMarkdownAssistantContent" {
				return true
			}
			position := fileSet.Position(call.Pos())
			callSites = append(callSites, nativeScrollbackAssistantStreamingCallSite{
				context: context,
				line:    position.Line,
				column:  position.Column,
			})
			return true
		})
	}
	return callSites
}

func mainSurfaceGuardPackageErrors(pkgs []*packages.Package) []string {
	var errors []string
	for _, pkg := range pkgs {
		for _, err := range pkg.Errors {
			errors = append(errors, err.Error())
		}
	}
	sort.Strings(errors)
	return errors
}

func collectMainSurfaceTerminalWriteViolations(pkgs []*packages.Package, repoRoot string) []string {
	violationsByPosition := map[string]struct{}{}
	for _, pkg := range pkgs {
		if pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			position := pkg.Fset.Position(file.Pos())
			relPath, ok := mainSurfaceGuardRelativePath(repoRoot, position.Filename)
			if !ok || !isInteractiveTUIPath(relPath) {
				continue
			}
			for _, violation := range mainSurfaceTerminalWriteViolationsInFile(pkg, file, relPath) {
				violationsByPosition[violation] = struct{}{}
			}
		}
	}

	violations := make([]string, 0, len(violationsByPosition))
	for violation := range violationsByPosition {
		violations = append(violations, violation)
	}
	return violations
}

func mainSurfaceTerminalWriteViolationsInFile(pkg *packages.Package, file *ast.File, relPath string) []string {
	var violations []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		context := mainSurfaceGuardFunctionContext{
			name:     fn.Name.Name,
			receiver: mainSurfaceGuardReceiverName(fn),
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || mainSurfaceGuardAllowsTerminalWrite(context) {
				return true
			}
			reason, ok := mainSurfaceTerminalWriteViolationReason(call, context, pkg.TypesInfo)
			if !ok {
				return true
			}
			position := pkg.Fset.Position(call.Pos())
			violations = append(violations, fmt.Sprintf("%s:%d:%d: %s", relPath, position.Line, position.Column, reason))
			return true
		})
	}
	return violations
}

type mainSurfaceGuardFunctionContext struct {
	name     string
	receiver string
}

func mainSurfaceGuardAllowsTerminalWrite(context mainSurfaceGuardFunctionContext) bool {
	if context.receiver == "OngoingScrollbackBufferImpl" {
		switch context.name {
		case "Steer", "StreamMarkdownAssistantContent", "FinishAssistantStreaming", "writeSteerPayloadLocked", "writeAssistantStreamPayloadLocked", "writeAssistantStreamTerminatorLocked":
			return true
		}
	}
	if context.receiver == "NativeLiveAreaImpl" {
		switch context.name {
		case "erasePhysicalLocked", "renderPhysicalLocked", "renderPhysicalDuringAssistantStreamLocked":
			return true
		}
	}
	if context.receiver == "uiTerminalCursorWriter" && context.name == "Write" {
		return true
	}
	if context.receiver == "" {
		switch context.name {
		case "writeTerminalCursorBytes", "writeTerminalCursorString":
			return true
		}
	}
	switch context.receiver {
	case "belTerminalNotifier":
		return context.name == "Bell" || context.name == "Notify"
	case "osc9TerminalNotifier":
		return context.name == "Bell" || context.name == "Notify"
	default:
		return false
	}
}

func mainSurfaceTerminalWriteViolationReason(call *ast.CallExpr, context mainSurfaceGuardFunctionContext, info *types.Info) (string, bool) {
	switch {
	case isDirectTerminalWriteCall(call, info):
		return "direct os.Stdout/os.Stderr terminal write", true
	case isPackageWriteCall(call, info, "io", "WriteString"):
		return "direct io.WriteString terminal write", true
	case isDirectFmtTerminalWriteCall(call, info):
		return "direct fmt terminal write", true
	case isLegacyTerminalCursorWriterCall(call, context):
		return "legacy terminal cursor writer write path", true
	default:
		return "", false
	}
}

func isDirectTerminalWriteCall(call *ast.CallExpr, info *types.Info) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if selector.Sel.Name != "Write" && selector.Sel.Name != "WriteString" {
		return false
	}
	return isTerminalObjectSelector(selector.X, info)
}

func isDirectFmtTerminalWriteCall(call *ast.CallExpr, info *types.Info) bool {
	if !isPackageWriteCall(call, info, "fmt", "Fprint", "Fprintf", "Fprintln") || len(call.Args) == 0 {
		return false
	}
	return isTerminalObjectSelector(call.Args[0], info)
}

func isPackageWriteCall(call *ast.CallExpr, info *types.Info, packagePath string, functionNames ...string) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	obj, ok := info.Uses[selector.Sel].(*types.Func)
	if !ok || obj.Pkg() == nil || obj.Pkg().Path() != packagePath {
		return false
	}
	for _, functionName := range functionNames {
		if obj.Name() == functionName {
			return true
		}
	}
	return false
}

func isTerminalObjectSelector(expr ast.Expr, info *types.Info) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	obj, ok := info.Uses[selector.Sel].(*types.Var)
	if !ok || obj.Pkg() == nil || obj.Pkg().Path() != "os" {
		return false
	}
	return obj.Name() == "Stdout" || obj.Name() == "Stderr"
}

func isLegacyTerminalCursorWriterCall(call *ast.CallExpr, context mainSurfaceGuardFunctionContext) bool {
	if context.receiver == "uiTerminalCursorWriter" {
		return selectorName(call.Fun) == "Write" || selectorName(call.Fun) == "WriteString"
	}
	switch context.name {
	case "writeTerminalCursorBytes", "writeTerminalCursorString", "writeTerminalSequence":
		name := selectorName(call.Fun)
		return name == "Write" || name == "WriteString"
	default:
		return false
	}
}

func selectorName(expr ast.Expr) string {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	return selector.Sel.Name
}

func mainSurfaceGuardReceiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	return mainSurfaceGuardTypeName(fn.Recv.List[0].Type)
}

func mainSurfaceGuardTypeName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return mainSurfaceGuardTypeName(typed.X)
	case *ast.IndexExpr:
		return mainSurfaceGuardTypeName(typed.X)
	case *ast.IndexListExpr:
		return mainSurfaceGuardTypeName(typed.X)
	default:
		return ""
	}
}

func isInteractiveTUIPath(relPath string) bool {
	if strings.HasPrefix(relPath, "cli/tui/") {
		return true
	}
	if !strings.HasPrefix(relPath, "cli/app/") {
		return false
	}
	switch relPath {
	case "cli/app/run_prompt.go", "cli/app/runlog.go":
		return false
	default:
		return true
	}
}

func mainSurfaceGuardRelativePath(repoRoot, path string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	relPath, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return "", false
	}
	if strings.HasPrefix(relPath, "..") {
		return "", false
	}
	return filepath.ToSlash(relPath), true
}

func mainSurfaceGuardRepositoryRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}

	for {
		goModPath := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			return dir
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", goModPath, err)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repository root from %s", dir)
		}
		dir = parent
	}
}
