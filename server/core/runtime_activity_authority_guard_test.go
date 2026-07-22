package core_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	testharness "core/internal/testharness/testsetup"

	"golang.org/x/tools/go/packages"
)

func TestRuntimeActivityLivenessSourcesStayAllowlisted(t *testing.T) {
	repoRoot := findRepoRoot(t)
	seen := make(map[string]map[string]struct{})
	violations := make([]string, 0)
	pkgs := testharness.LoadTypedPackages(t, repoRoot, false, "./server/...", "./shared/...", "./cli/...")
	assertCoreRepositoryModule(t, pkgs)
	for _, pkg := range pkgs {
		if !isProductionRepositoryPackage(pkg) {
			continue
		}
		for _, file := range pkg.Syntax {
			relPath, found := testharness.RepositoryRelativePath(repoRoot, pkg.Fset.Position(file.Pos()).Filename)
			if !found {
				t.Fatalf("runtime liveness guard source is outside repository: %s", pkg.Fset.Position(file.Pos()).Filename)
			}
			ast.Inspect(file, func(node ast.Node) bool {
				ident, ok := node.(*ast.Ident)
				if !ok {
					return true
				}
				source, forbidden := forbiddenRuntimeLivenessFunction(pkg.TypesInfo.Uses[ident])
				if !forbidden {
					return true
				}
				allowedSources := allowedRuntimeLivenessSources[relPath]
				if _, allowed := allowedSources[source]; allowed {
					if seen[relPath] == nil {
						seen[relPath] = make(map[string]struct{})
					}
					seen[relPath][source] = struct{}{}
					return true
				}
				position := testharness.SourcePosition(pkg, ident.Pos())
				violations = append(violations, relPath+":"+position.String()+": runtime activity must not use transitional liveness source "+source)
				return true
			})
		}
	}
	for relPath, sources := range allowedRuntimeLivenessSources {
		if strings.TrimSpace(relPath) == "" {
			violations = append(violations, "runtime liveness allowlist contains empty path")
			continue
		}
		for source, reason := range sources {
			if strings.TrimSpace(reason) == "" {
				violations = append(violations, relPath+": runtime liveness allowlist for "+source+" has empty rationale")
			}
			if _, found := seen[relPath][source]; !found {
				violations = append(violations, relPath+": remove stale runtime liveness allowlist entry for "+source)
			}
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("runtime activity liveness source guard violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestRuntimeActivityActiveStepAuthorityStaysBehindResolverSeam(t *testing.T) {
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	pkgs := testharness.LoadTypedPackages(t, repoRoot, false, "./server/...")
	assertCoreRepositoryModule(t, pkgs)
	for _, pkg := range pkgs {
		if !isProductionRepositoryPackage(pkg) || runtimeActivityAuthorityPackage[pkg.PkgPath] {
			continue
		}
		for _, file := range pkg.Syntax {
			relPath, found := testharness.RepositoryRelativePath(repoRoot, pkg.Fset.Position(file.Pos()).Filename)
			if !found {
				t.Fatalf("runtime active-step guard source is outside repository: %s", pkg.Fset.Position(file.Pos()).Filename)
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if isForbiddenRuntimeActiveStepMethod(pkg.TypesInfo.Selections[selector]) {
					position := testharness.SourcePosition(pkg, selector.Sel.Pos())
					violations = append(violations, relPath+":"+position.String()+": server liveness must read active step through server/runtimeactivity resolver seam")
				}
				return true
			})
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("runtime active-step authority seam violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestRuntimeReadModelClockConsumersDoNotUseGlobalCoordinator(t *testing.T) {
	repoRoot := findRepoRoot(t)
	pkg := testharness.PackageByPath(t, testharness.LoadTypedPackages(t, repoRoot, false, "./server/runtimeops"), "core/server/runtimeops")
	foundCoordinator := false
	for index, file := range pkg.Syntax {
		relPath, ok := testharness.RepositoryRelativePath(repoRoot, pkg.CompiledGoFiles[index])
		if !ok || relPath != "server/runtimeops/coordinator.go" {
			continue
		}
		foundCoordinator = true
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if isRuntimeActivityFunction(pkg.TypesInfo.Uses[selector.Sel], "NextReadModelVersion") {
				t.Fatalf("server/runtimeops/coordinator.go must use the registry-owned read-model clock, not runtimeactivity.NextReadModelVersion")
			}
			return true
		})
	}
	if !foundCoordinator {
		t.Fatal("server/runtimeops/coordinator.go must remain in the runtime read-model clock guard")
	}
}

func TestRuntimeClientInputIdentityBoundaryStaysRequestShaped(t *testing.T) {
	repoRoot := findRepoRoot(t)
	pkgs := testharness.LoadTypedPackages(t, repoRoot, false, "./shared/clientui", "./cli/app")
	assertRuntimeClientDoesNotExposeLegacyInputSignatures(t, testharness.PackageByPath(t, pkgs, "core/shared/clientui"))
	assertSessionRuntimeClientDoesNotSynthesizeInputIdentity(t, testharness.PackageByPath(t, pkgs, "core/cli/app"), repoRoot)
}

func TestRuntimeViewDoesNotExportGlobalLivenessMainViewHelper(t *testing.T) {
	repoRoot := findRepoRoot(t)
	pkg := testharness.PackageByPath(t, testharness.LoadTypedPackages(t, repoRoot, false, "./server/runtimeview"), "core/server/runtimeview")
	foundProjection := false
	for index, file := range pkg.Syntax {
		relPath, ok := testharness.RepositoryRelativePath(repoRoot, pkg.CompiledGoFiles[index])
		if !ok || relPath != "server/runtimeview/projection.go" {
			continue
		}
		foundProjection = true
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && function.Name.Name == "MainViewFromRuntime" {
				t.Fatal("runtimeview must not expose MainViewFromRuntime; live activity/version must come from the registry read-model snapshot seam")
			}
		}
	}
	if !foundProjection {
		t.Fatal("server/runtimeview/projection.go must remain in the global-liveness helper guard")
	}
}

func isRuntimeActivityFunction(object types.Object, name string) bool {
	function, ok := object.(*types.Func)
	return ok && function.Pkg() != nil && function.Pkg().Path() == "core/server/runtimeactivity" && function.Name() == name
}

func assertRuntimeClientDoesNotExposeLegacyInputSignatures(t *testing.T, pkg *packages.Package) {
	t.Helper()
	runtimeClientObject := pkg.Types.Scope().Lookup("RuntimeClient")
	if runtimeClientObject == nil {
		t.Fatal("shared/clientui.RuntimeClient is missing")
	}
	runtimeClient, ok := runtimeClientObject.Type().Underlying().(*types.Interface)
	if !ok {
		t.Fatal("shared/clientui.RuntimeClient must remain an interface")
	}
	for index := 0; index < runtimeClient.NumMethods(); index++ {
		method := runtimeClient.Method(index)
		if legacyRuntimeClientInputSignature(method) {
			t.Fatalf("RuntimeClient must expose request-shaped input APIs with UI-owned operation refs, found legacy %s signature", method.Name())
		}
	}
}

func legacyRuntimeClientInputSignature(method *types.Func) bool {
	signature, ok := method.Type().(*types.Signature)
	if !ok {
		return false
	}
	switch method.Name() {
	case "SubmitUserMessage", "SubmitUserShellCommand", "CompactContext":
		return signatureHasContextAndSingleString(signature)
	case "SubmitQueuedUserMessages":
		return signatureHasContextOnly(signature)
	case "QueueUserMessage":
		return signatureHasSingleString(signature)
	default:
		return false
	}
}

func signatureHasContextAndSingleString(signature *types.Signature) bool {
	return signature.Params().Len() == 2 &&
		isContextType(signature.Params().At(0).Type()) &&
		isStringType(signature.Params().At(1).Type())
}

func signatureHasContextOnly(signature *types.Signature) bool {
	return signature.Params().Len() == 1 && isContextType(signature.Params().At(0).Type())
}

func signatureHasSingleString(signature *types.Signature) bool {
	return signature.Params().Len() == 1 && isStringType(signature.Params().At(0).Type())
}

func isContextType(typ types.Type) bool {
	named, ok := types.Unalias(typ).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "context" && named.Obj().Name() == "Context"
}

func isStringType(typ types.Type) bool {
	return types.Identical(types.Unalias(typ), types.Typ[types.String])
}

func assertSessionRuntimeClientDoesNotSynthesizeInputIdentity(t *testing.T, pkg *packages.Package, repoRoot string) {
	t.Helper()
	foundControlFile := false
	for index, file := range pkg.Syntax {
		relPath, ok := testharness.RepositoryRelativePath(repoRoot, pkg.CompiledGoFiles[index])
		if !ok || relPath != "cli/app/ui_runtime_client_control.go" {
			continue
		}
		foundControlFile = true
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || !isSessionRuntimeClientMethod(pkg, function) {
				continue
			}
			switch function.Name.Name {
			case "SubmitUserMessage", "SubmitUserShellCommand", "CompactContext", "SubmitQueuedUserMessages", "QueueUserMessage", "QueueUserMessageWithClientRequestID":
				t.Fatalf("sessionRuntimeClient must not synthesize hidden input operation refs, found %s", function.Name.Name)
			}
		}
	}
	if !foundControlFile {
		t.Fatal("cli/app/ui_runtime_client_control.go must remain in the runtime input identity guard")
	}
}

func isSessionRuntimeClientMethod(pkg *packages.Package, function *ast.FuncDecl) bool {
	if function.Recv == nil || len(function.Recv.List) != 1 {
		return false
	}
	receiverType := pkg.TypesInfo.TypeOf(function.Recv.List[0].Type)
	pointer, ok := types.Unalias(receiverType).(*types.Pointer)
	if ok {
		receiverType = pointer.Elem()
	}
	named, ok := types.Unalias(receiverType).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "core/cli/app" && named.Obj().Name() == "sessionRuntimeClient"
}

func TestProductionRuntimeAuthorityAdaptersStayCentralized(t *testing.T) {
	repoRoot := findRepoRoot(t)
	compositionPath := filepath.Join("server", "core", "composition.go")
	var authorityConfigured, foundSink bool
	if err := walkProductionGoFiles(repoRoot, func(path, relPath string) error {
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if declaration, ok := node.(*ast.FuncDecl); ok {
				if forbiddenRawEngineGetters[declaration.Name.Name] {
					t.Errorf("%s:%d declares forbidden raw Engine getter %s", relPath, fileSet.Position(declaration.Name.Pos()).Line, declaration.Name.Name)
				}
				if rawEngineBridges[declaration.Name.Name] && filepath.ToSlash(filepath.Dir(relPath)) != "server/sessionruntime" {
					t.Errorf("%s:%d declares raw Engine bridge %s outside sessionruntime", relPath, fileSet.Position(declaration.Name.Pos()).Line, declaration.Name.Name)
				}
				return true
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if rawEngineBridges[selector.Sel.Name] && !approvedRawEngineAdapterDirs[filepath.ToSlash(filepath.Dir(relPath))] {
				t.Errorf("%s:%d calls raw Engine bridge %s outside an approved Authority adapter", relPath, fileSet.Position(selector.Sel.Pos()).Line, selector.Sel.Name)
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if relPath == compositionPath && pkg.Name == "sessionruntime" && selector.Sel.Name == "NewAuthority" && len(call.Args) == 1 {
				if options, ok := call.Args[0].(*ast.CompositeLit); ok {
					for _, element := range options.Elts {
						if field, ok := element.(*ast.KeyValueExpr); ok {
							key, ok := field.Key.(*ast.Ident)
							authorityConfigured = authorityConfigured || ok && key.Name == "StepLifecycle"
						}
					}
				}
			}
			if pkg.Name != "runtimewire" || selector.Sel.Name != "NewStepLifecycleSink" {
				return true
			}
			foundSink = true
			if relPath != compositionPath {
				t.Errorf("%s constructs runtimewire.NewStepLifecycleSink outside Authority composition", relPath)
			}
			return true
		})
		return nil
	}); err != nil {
		t.Fatalf("scan production StepLifecycle construction: %v", err)
	}
	if !authorityConfigured || !foundSink {
		t.Fatalf("%s Authority StepLifecycle composition = configured:%t sink:%t", compositionPath, authorityConfigured, foundSink)
	}
}

var rawEngineBridges = map[string]bool{"WithRuntime": true, "WithCurrentRuntime": true, "WithEngine": true}
var forbiddenRawEngineGetters = map[string]bool{"ResolveRuntime": true, "GetRuntime": true, "CurrentRuntime": true, "Runtime": true, "ResolveEngine": true, "GetEngine": true, "Engine": true}
var approvedRawEngineAdapterDirs = map[string]bool{"server/sessionruntime": true, "server/runtimecontrol": true, "server/runprompt": true, "server/workflowrunner": true, "server/sessionview": true}

func forbiddenRuntimeLivenessFunction(object types.Object) (string, bool) {
	function, ok := object.(*types.Func)
	if !ok || function.Pkg() == nil {
		return "", false
	}
	switch function.Pkg().Path() {
	case "core/server/runtime", "core/server/runtimeactivity":
	default:
		return "", false
	}
	switch function.Name() {
	case "LatestRun", "AppendRunStarted", "AppendRunFinished", "MarkInFlight", "InFlightStep":
		return function.Name(), true
	default:
		return "", false
	}
}

var runtimeActivityAuthorityPackage = map[string]bool{
	"core/server/runtime":         true,
	"core/server/runtimeactivity": true,
}

func isForbiddenRuntimeActiveStepMethod(selection *types.Selection) bool {
	if selection == nil {
		return false
	}
	function, ok := selection.Obj().(*types.Func)
	if !ok || function.Pkg() == nil || function.Pkg().Path() != "core/server/runtime" {
		return false
	}
	switch function.Name() {
	case "ActiveRun", "ActiveStepSnapshot":
		return true
	default:
		return false
	}
}

var allowedRuntimeLivenessSources = map[string]map[string]string{}
