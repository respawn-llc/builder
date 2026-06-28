package core_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type goListPackage struct {
	ImportPath string
	Imports    []string
}

func TestArchitectureBoundaries(t *testing.T) {
	repoRoot := findRepoRoot(t)
	packages := loadRepoPackages(t, repoRoot)
	violations := make([]string, 0)
	for _, pkg := range packages {
		importPath := strings.TrimSpace(pkg.ImportPath)
		if importPath == "" {
			continue
		}
		for _, imported := range pkg.Imports {
			trimmedImport := strings.TrimSpace(imported)
			if trimmedImport == "" || !strings.HasPrefix(trimmedImport, "core/") {
				continue
			}
			switch {
			case strings.HasPrefix(importPath, "core/server/") && strings.HasPrefix(trimmedImport, "core/cli/"):
				violations = append(violations, importPath+" must not import frontend package "+trimmedImport)
			case strings.HasPrefix(importPath, "core/shared/") && strings.HasPrefix(trimmedImport, "core/cli/"):
				violations = append(violations, importPath+" must not import frontend package "+trimmedImport)
			case strings.HasPrefix(importPath, "core/shared/") && strings.HasPrefix(trimmedImport, "core/server/"):
				violations = append(violations, importPath+" must not import server package "+trimmedImport)
			case strings.HasPrefix(importPath, "core/cli/") && trimmedImport == "core/server/metadata":
				violations = append(violations, importPath+" must not import persistence metadata package "+trimmedImport)
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("architecture boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestSharedClientUIRemainsDTOOnly(t *testing.T) {
	repoRoot := findRepoRoot(t)
	clientUIRoot := filepath.Join(repoRoot, "shared", "clientui")
	allowedTypes := map[string]struct{}{
		"ApprovalDecision":                 {},
		"ApprovalOption":                   {},
		"ApprovalPromptAnswer":             {},
		"BackgroundProcess":                {},
		"BackgroundShellEvent":             {},
		"ChatEntry":                        {},
		"ChatSnapshot":                     {},
		"CommittedTranscriptSuffix":        {},
		"CommittedTranscriptSuffixRequest": {},
		"CompactionLifecycle":              {},
		"CompactionStatus":                 {},
		"ConversationFreshness":            {},
		"EntryVisibility":                  {},
		"Event":                            {},
		"EventKind":                        {},
		"ExternalRuntimeState":             {},
		"ExternalRuntimeStatus":            {},
		"MessagePhase":                     {},
		"MessageType":                      {},
		"PendingApproval":                  {},
		"PendingAsk":                       {},
		"PendingPromptEvent":               {},
		"PendingPromptEventType":           {},
		"ProcessClient":                    {},
		"ProcessOutputChunk":               {},
		"ProjectAvailability":              {},
		"ProjectOverview":                  {},
		"ProjectSummary":                   {},
		"ProjectWorkspaceSummary":          {},
		"PromptAnswer":                     {},
		"QueuedUserMessage":                {},
		"QueuedUserMessageFailureReason":   {},
		"QueuedUserMessageStatus":          {},
		"QueuedUserMessageStatusEvent":     {},
		"ReasoningDelta":                   {},
		"ReviewerLifecycle":                {},
		"RunLifecycle":                     {},
		"RunLifecyclePhase":                {},
		"RunMode":                          {},
		"RunState":                         {},
		"RunStatus":                        {},
		"RunView":                          {},
		"RuntimeClient":                    {},
		"RuntimeConnectionLifecycle":       {},
		"RuntimeContextUsage":              {},
		"RuntimeGoal":                      {},
		"RuntimeGoalStatusUpdate":          {},
		"RuntimeGoalStatus":                {},
		"RuntimeMainView":                  {},
		"RuntimeSessionView":               {},
		"RuntimeStatus":                    {},
		"SessionExecutionTarget":           {},
		"SessionSummary":                   {},
		"ToolCallMeta":                     {},
		"ToolCallRenderBehavior":           {},
		"ToolPresentationKind":             {},
		"ToolRenderHint":                   {},
		"ToolRenderKind":                   {},
		"ToolShellDialect":                 {},
		"TranscriptMetadata":               {},
		"TranscriptPage":                   {},
		"TranscriptPageRequest":            {},
		"TranscriptRecoveryCause":          {},
		"UpdateStatus":                     {},
		"UserTurnSubmission":               {},
		"WorkflowSessionStatus":            {},
	}
	allowedFuncs := map[string]struct{}{
		"CompactionLifecycle.IsRunning":             {},
		"ConversationFreshness.IsFresh":             {},
		"IdleRunLifecycle":                          {},
		"MustRunLifecycle":                          {},
		"NewCompactionLifecycle":                    {},
		"NewReviewerLifecycle":                      {},
		"NewRunLifecycle":                           {},
		"NewRuntimeConnectionLifecycle":             {},
		"NormalizeMessagePhase":                     {},
		"NormalizeSessionExecutionTarget":           {},
		"NormalizeThinkingLevel":                    {},
		"PendingPromptEvent.IsZero":                 {},
		"ReviewerLifecycle.IsBlocking":              {},
		"ReviewerLifecycle.IsRunning":               {},
		"ReviewerLifecycle.Validate":                {},
		"RunLifecycle.IsFinished":                   {},
		"RunLifecycle.IsGoalLoopRunning":            {},
		"RunLifecycle.IsRunning":                    {},
		"RunLifecycle.Validate":                     {},
		"RuntimeConnectionLifecycle.IsDisconnected": {},
		"SessionExecutionTargetIsZero":              {},
		"SessionExecutionTargetsEqual":              {},
	}
	violations := make([]string, 0)
	if err := filepath.WalkDir(clientUIRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			relPath = path
		}
		for _, decl := range file.Decls {
			switch typedDecl := decl.(type) {
			case *ast.FuncDecl:
				name := funcDeclBoundaryName(typedDecl)
				if _, allowed := allowedFuncs[name]; allowed {
					continue
				}
				if funcUsesRuntimeEventPolicyType(typedDecl.Type) {
					violations = append(violations, relPath+": DTO-only package must not define runtime-event policy helper "+name)
					continue
				}
				violations = append(violations, relPath+": DTO-only package added function "+name+" without DTO-boundary review")
			case *ast.GenDecl:
				if typedDecl.Tok != token.TYPE {
					continue
				}
				for _, spec := range typedDecl.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					name := typeSpec.Name.Name
					if _, allowed := allowedTypes[name]; !allowed {
						violations = append(violations, relPath+": DTO-only package added type "+name+" without DTO-boundary review")
					}
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("scan shared clientui DTO boundaries: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("shared/clientui DTO boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func funcDeclBoundaryName(fn *ast.FuncDecl) string {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		if fn == nil {
			return ""
		}
		return fn.Name.Name
	}
	return receiverTypeName(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

func receiverTypeName(expr ast.Expr) string {
	switch typedExpr := expr.(type) {
	case *ast.Ident:
		return typedExpr.Name
	case *ast.StarExpr:
		return receiverTypeName(typedExpr.X)
	default:
		return ""
	}
}

func funcUsesRuntimeEventPolicyType(funcType *ast.FuncType) bool {
	if funcType == nil {
		return false
	}
	for _, fields := range []*ast.FieldList{funcType.Params, funcType.Results} {
		if fields == nil {
			continue
		}
		for _, field := range fields.List {
			if exprUsesRuntimeEventPolicyType(field.Type) {
				return true
			}
		}
	}
	return false
}

func exprUsesRuntimeEventPolicyType(expr ast.Expr) bool {
	switch typedExpr := expr.(type) {
	case *ast.Ident:
		switch typedExpr.Name {
		case "Event", "ReasoningDelta", "BackgroundShellEvent":
			return true
		default:
			return false
		}
	case *ast.StarExpr:
		return exprUsesRuntimeEventPolicyType(typedExpr.X)
	case *ast.ArrayType:
		return exprUsesRuntimeEventPolicyType(typedExpr.Elt)
	case *ast.MapType:
		return exprUsesRuntimeEventPolicyType(typedExpr.Key) || exprUsesRuntimeEventPolicyType(typedExpr.Value)
	default:
		return false
	}
}

func TestCLIPackagesDoNotImportServerOutsideCompositionBridges(t *testing.T) {
	repoRoot := findRepoRoot(t)
	// Keep CLI -> server imports concentrated in documented local composition
	// seams. UI, TUI, status, and command handlers must use shared contracts.
	// Every exception below is an exact file/import pair introduced by deleting
	// a one-line bridge; new server imports still fail by default.
	allowedServerImportsByFile := allowedCLIServerImports()
	actualAllowedServerImportsByFile := make(map[string]map[string]struct{})
	violations := make([]string, 0)
	walkRoot := filepath.Join(repoRoot, "cli")
	if err := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			relPath = path
		}
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if !strings.HasPrefix(importPath, "core/server/") {
				continue
			}
			allowedImports := allowedServerImportsByFile[relPath]
			if _, allowed := allowedImports[importPath]; allowed {
				if actualAllowedServerImportsByFile[relPath] == nil {
					actualAllowedServerImportsByFile[relPath] = make(map[string]struct{})
				}
				actualAllowedServerImportsByFile[relPath][importPath] = struct{}{}
				continue
			}
			violations = append(violations, relPath+": CLI production file must not import server package "+importPath)
		}
		return nil
	}); err != nil {
		t.Fatalf("scan cli server imports: %v", err)
	}
	for relPath, expectedImports := range allowedServerImportsByFile {
		actualImports := actualAllowedServerImportsByFile[relPath]
		for importPath, reason := range expectedImports {
			if strings.TrimSpace(reason) == "" {
				violations = append(violations, relPath+": allowed server import "+importPath+" must include rationale text")
			}
			if _, found := actualImports[importPath]; !found {
				violations = append(violations, relPath+": remove stale allowed server import "+importPath+" from architecture test")
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("cli to server import boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func allowedCLIServerImports() map[string]map[string]string {
	return map[string]map[string]string{
		filepath.Join("cli", "app", "auth_gate.go"): {
			"core/server/auth":        "auth startup gate owns auth state conversion after deleting the app bridge package",
			"core/server/authservice": "auth startup gate owns auth flow conversion after deleting the app bridge package",
		},
		filepath.Join("cli", "app", "remote_auth_bootstrap.go"): {
			"core/server/auth": "remote auth bootstrap constructs server auth grants at the startup boundary",
		},
		filepath.Join("cli", "app", "onboarding_flow.go"): {
			"core/server/llm": "onboarding displays server-owned model catalog labels after deleting the app bridge package",
		},
		filepath.Join("cli", "app", "onboarding_render.go"): {
			"core/server/llm": "onboarding displays server-owned model catalog labels after deleting the app bridge package",
		},
		filepath.Join("cli", "app", "onboarding_run.go"): {
			"core/server/llm": "onboarding resolves server-owned model metadata after deleting the app bridge package",
		},
		filepath.Join("cli", "app", "onboarding_workflow.go"): {
			"core/server/llm": "onboarding workflow renders server-owned model labels after deleting the app bridge package",
		},
		filepath.Join("cli", "app", "session_server_target.go"): {
			"core/server/startup": "interactive session server launches a local serve daemon after deleting the app bridge package",
		},
		filepath.Join("cli", "app", "ui_layout_rendering_status.go"): {
			"core/server/llm": "status line uses the server-owned model display label after deleting the app bridge package",
		},
		filepath.Join("cli", "app", "internal", "status", "statuscollect_model.go"): {
			"core/server/llm": "status collection uses the server-owned model display label after deleting the app bridge package",
		},
		filepath.Join("cli", "app", "internal", "status", "statuscollect_collect.go"): {
			"core/server/runtime": "status collection reads runtime memory status at the CLI status boundary",
		},
		filepath.Join("cli", "app", "internal", "status", "statuscollect_environment.go"): {
			"core/server/runtime": "status collection reads runtime memory status at the CLI status boundary",
		},
		filepath.Join("cli", "app", "internal", "onboarding", "onboardingimport_skill_metadata.go"): {
			"core/server/runtime": "onboarding import reads server skill metadata at the import boundary",
		},
		filepath.Join("cli", "app", "internal", "authui", "authflowadapter_flow.go"): {
			"core/server/auth":        "auth adapter intentionally translates server auth types for app startup",
			"core/server/authservice": "auth adapter intentionally translates server auth-flow types for app startup",
		},
		filepath.Join("cli", "app", "internal", "authui", "authoauth_runner.go"): {
			"core/server/auth": "OAuth runner owns server auth OAuth calls after deleting one-line adapters",
		},
		filepath.Join("cli", "app", "internal", "authui", "oauthadapter_oauth.go"): {
			"core/server/auth": "OAuth adapter re-exports server auth OAuth DTO aliases",
		},
		filepath.Join("cli", "app", "internal", "startupconfig", "config.go"): {
			"core/server/bootstrap": "startup config resolves server bootstrap context at the startup boundary",
		},
		filepath.Join("cli", "app", "internal", "embeddedattach", "embeddedstartup_start.go"): {
			"core/server/auth":        "embedded startup composes server auth readiness",
			"core/server/authservice": "embedded startup composes server auth flow readiness",
			"core/server/startup":     "embedded startup delegates to server startup",
		},
		filepath.Join("cli", "app", "internal", "onboarding", "onboardingready_ready.go"): {
			"core/server/auth":    "onboarding readiness requires server auth manager types",
			"core/server/startup": "onboarding readiness delegates to the server-owned onboarding flow",
		},
		filepath.Join("cli", "kent", "server_bridge.go"): {
			"core/server/sessionservice": "kent CLI bridge retains non-trivial fallback behavior",
		},
		filepath.Join("cli", "kent", "serve.go"): {
			"core/server/startup": "kent serve command is a composition root",
		},
	}
}

func TestCLIAppUIFilesDoNotAddServerImports(t *testing.T) {
	repoRoot := findRepoRoot(t)
	actualServerImportsByFile := make(map[string]map[string]struct{})
	violations := make([]string, 0)
	walkRoot := filepath.Join(repoRoot, "cli", "app")
	if err := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != walkRoot {
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasPrefix(base, "ui") || !strings.HasSuffix(base, ".go") || strings.HasSuffix(base, "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			relPath = path
		}
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if !strings.HasPrefix(importPath, "core/server/") {
				continue
			}
			if actualServerImportsByFile[relPath] == nil {
				actualServerImportsByFile[relPath] = make(map[string]struct{})
			}
			actualServerImportsByFile[relPath][importPath] = struct{}{}
			if !isAllowedCLIAppRootServerImport(relPath, importPath) {
				violations = append(violations, relPath+": UI file must not add server import "+importPath)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("scan cli app UI sources: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("cli app UI server import boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestCLIUIFilesDoNotBypassServerAttachService(t *testing.T) {
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	for _, root := range []string{
		filepath.Join(repoRoot, "cli", "app"),
		filepath.Join(repoRoot, "cli", "tui"),
	} {
		if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != root && root == filepath.Join(repoRoot, "cli", "app") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			base := filepath.Base(path)
			isAppUI := root == filepath.Join(repoRoot, "cli", "app") && strings.HasPrefix(base, "ui")
			isTUI := strings.Contains(filepath.ToSlash(path), "/cli/tui/")
			if !isAppUI && !isTUI {
				return nil
			}
			fileSet := token.NewFileSet()
			file, parseErr := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
			if parseErr != nil {
				return parseErr
			}
			relPath, relErr := filepath.Rel(repoRoot, path)
			if relErr != nil {
				relPath = path
			}
			for _, spec := range file.Imports {
				importPath := strings.Trim(spec.Path.Value, "\"")
				if importPath == "core/cli/app/internal/serverattach" || importPath == "core/cli/app/internal/remoteattach" {
					violations = append(violations, relPath+": UI files must not import startup attachment package "+importPath)
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("scan UI sources under %s: %v", root, err)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("cli UI server attach bypass violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestCLIAppRootFilesDoNotImportServerPackages(t *testing.T) {
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	walkCLIAppRootFiles(t, repoRoot, false, parser.ImportsOnly, func(source parsedGoSource) {
		for _, spec := range source.File.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if strings.HasPrefix(importPath, "core/server/") && !isAllowedCLIAppRootServerImport(source.RelPath, importPath) {
				violations = append(violations, source.RelPath+": app root package must not import server package "+importPath)
			}
		}
	})
	if len(violations) > 0 {
		t.Fatalf("cli app root server import boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestCLIAppDoesNotReintroduceEmbeddedServerServiceLocator(t *testing.T) {
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	walkCLIAppRootFiles(t, repoRoot, true, parser.SkipObjectResolution, func(source parsedGoSource) {
		ast.Inspect(source.File, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if ok && ident.Name == "embeddedServer" {
				violations = append(violations, source.RelPath+": must not reintroduce embeddedServer service-locator identifier")
			}
			return true
		})
	})
	if len(violations) > 0 {
		t.Fatalf("cli app embeddedServer service-locator violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestCLIAppStartupEntrypointsUseServerAttach(t *testing.T) {
	repoRoot := findRepoRoot(t)
	for _, relPath := range []string{
		filepath.Join("cli", "app", "session_server_target.go"),
		filepath.Join("cli", "app", "run_prompt_target.go"),
	} {
		path := filepath.Join(repoRoot, relPath)
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", relPath, err)
		}
		importsServerAttach := false
		violations := make([]string, 0)
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			switch importPath {
			case "core/cli/app/internal/serverattach":
				importsServerAttach = true
			case "core/cli/app/internal/targetstartup", "core/cli/app/internal/targetresolve":
				violations = append(violations, relPath+": startup entrypoint must use serverattach instead of "+importPath)
			}
		}
		if !importsServerAttach {
			violations = append(violations, relPath+": startup entrypoint must import serverattach")
		}
		usesResolve := false
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if ident.Name == "serverattach" && selector.Sel.Name == "Resolve" {
				usesResolve = true
			}
			if ident.Name == "remoteattach" && (selector.Sel.Name == "DialHeadless" || selector.Sel.Name == "DialInteractive") {
				violations = append(violations, relPath+": startup entrypoint must not call remoteattach."+selector.Sel.Name+" directly")
			}
			return true
		})
		if !usesResolve {
			violations = append(violations, relPath+": startup entrypoint must resolve targets through serverattach.Resolve")
		}
		if len(violations) > 0 {
			t.Fatalf("startup server attach boundary violations:\n%s", strings.Join(violations, "\n"))
		}
	}
}

func TestCLIAppStartupFilesDoNotReachIntoEmbeddedInternals(t *testing.T) {
	repoRoot := findRepoRoot(t)
	for _, relPath := range []string{
		filepath.Join("cli", "app", "session_server_target.go"),
		filepath.Join("cli", "app", "run_prompt_target.go"),
		filepath.Join("cli", "app", "session_lifecycle.go"),
	} {
		path := filepath.Join(repoRoot, relPath)
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", relPath, err)
		}
		violations := make([]string, 0)
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == "inner" {
				violations = append(violations, relPath+": startup files must use narrow embedded attachments instead of embeddedAppServer.inner")
			}
			return true
		})
		if len(violations) > 0 {
			t.Fatalf("embedded startup boundary violations:\n%s", strings.Join(violations, "\n"))
		}
	}
}

type parsedGoSource struct {
	RelPath string
	File    *ast.File
}

func walkCLIAppRootFiles(t *testing.T, repoRoot string, includeTests bool, mode parser.Mode, visit func(parsedGoSource)) {
	t.Helper()
	root := filepath.Join(repoRoot, "cli", "app")
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || (!includeTests && strings.HasSuffix(path, "_test.go")) {
			return nil
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, mode)
		if parseErr != nil {
			return parseErr
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			relPath = path
		}
		visit(parsedGoSource{RelPath: relPath, File: file})
		return nil
	}); err != nil {
		t.Fatalf("scan cli app root sources: %v", err)
	}
}

func TestCLIAppSplitFilesDoNotImportServerPackages(t *testing.T) {
	repoRoot := findRepoRoot(t)
	files := []string{
		filepath.Join("cli", "app", "auth_oauth_presenter.go"),
		filepath.Join("cli", "app", "onboarding_render.go"),
		filepath.Join("cli", "app", "onboarding_workflow.go"),
		filepath.Join("cli", "app", "runtime_attachment.go"),
		filepath.Join("cli", "app", "ui_native_surface.go"),
		filepath.Join("cli", "app", "ui_renderer_output_gate.go"),
		filepath.Join("cli", "app", "ui_process_render.go"),
		filepath.Join("cli", "app", "ui_runtime_client_control.go"),
		filepath.Join("cli", "app", "ui_status_overlay_format.go"),
	}
	violations := make([]string, 0)
	for _, relPath := range files {
		path := filepath.Join(repoRoot, relPath)
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parse %s imports: %v", relPath, parseErr)
		}
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if strings.HasPrefix(importPath, "core/server/") && !isAllowedCLIAppRootServerImport(relPath, importPath) {
				violations = append(violations, relPath+": split app file must not import server package "+importPath)
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("cli app split-file server import boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func isAllowedCLIAppRootServerImport(relPath string, importPath string) bool {
	_, allowed := allowedCLIServerImports()[relPath][importPath]
	return allowed
}

func TestCLITUIFilesDoNotImportServerPackages(t *testing.T) {
	repoRoot := findRepoRoot(t)
	allowedServerImportsByFile := map[string]map[string]struct{}{}
	actualServerImportsByFile := make(map[string]map[string]struct{})
	tuiRoot := filepath.Join(repoRoot, "cli", "tui")
	violations := make([]string, 0)
	if err := filepath.WalkDir(tuiRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			relPath = path
		}
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			if !strings.HasPrefix(importPath, "core/server/") {
				continue
			}
			if actualServerImportsByFile[relPath] == nil {
				actualServerImportsByFile[relPath] = make(map[string]struct{})
			}
			actualServerImportsByFile[relPath][importPath] = struct{}{}
			if _, allowed := allowedServerImportsByFile[relPath][importPath]; !allowed {
				violations = append(violations, relPath+": TUI package must not add server import "+importPath)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("scan cli tui sources: %v", err)
	}
	for relPath, expectedImports := range allowedServerImportsByFile {
		actualImports := actualServerImportsByFile[relPath]
		for importPath := range expectedImports {
			if _, found := actualImports[importPath]; !found {
				violations = append(violations, relPath+": remove stale allowed server import "+importPath+" from architecture test")
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("cli tui server import boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

type cliInternalBoundaryCase struct {
	Name                 string
	Packages             []string
	Label                string
	ForbidServer         bool
	ForbidAllCore        bool
	ServerViolationLabel string
}

func TestCLIAppInternalPackageBoundaries(t *testing.T) {
	cases := []cliInternalBoundaryCase{
		{Name: "Status", Packages: []string{"status"}, Label: "status package"},
		{Name: "RuntimeAttach", Packages: []string{"runtimeattach"}, Label: "runtime connection package", ForbidServer: true},
		{Name: "AuthUI", Packages: []string{"authui"}, Label: "auth UI package"},
		{Name: "ServerAttach", Packages: []string{"serverattach"}, Label: "server attach package", ForbidServer: true},
		{Name: "DaemonLaunch", Packages: []string{"daemonlaunch"}, Label: "daemon launch package", ForbidAllCore: true},
		{Name: "RemoteAttach", Packages: []string{"remoteattach"}, Label: "remote attach package", ForbidServer: true},
		{Name: "ProjectBinding", Packages: []string{"projectbinding"}, Label: "project binding package", ForbidServer: true},
		{Name: "WorktreeUI", Packages: []string{"worktreeui"}, Label: "worktree UI package", ForbidServer: true},
		{Name: "StartupConfig", Packages: []string{"startupconfig"}, Label: "startup config package"},
		{Name: "Onboarding", Packages: []string{"onboarding"}, Label: "onboarding package"},
		{Name: "EmbeddedAttach", Packages: []string{"embeddedattach"}, Label: "embedded attach package"},
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			assertCLIAppInternalPackageBoundary(t, tc)
		})
	}
}

func assertCLIAppInternalPackageBoundary(t *testing.T, tc cliInternalBoundaryCase) {
	t.Helper()
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	for _, packageName := range tc.Packages {
		root := filepath.Join(repoRoot, "cli", "app", "internal", packageName)
		if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fileSet := token.NewFileSet()
			file, parseErr := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
			if parseErr != nil {
				return parseErr
			}
			relPath, relErr := filepath.Rel(repoRoot, path)
			if relErr != nil {
				relPath = path
			}
			for _, spec := range file.Imports {
				importPath := strings.Trim(spec.Path.Value, "\"")
				switch {
				case tc.ForbidAllCore && strings.HasPrefix(importPath, "core/"):
					violations = append(violations, relPath+": "+tc.Label+" must not import core packages "+importPath)
				case tc.ForbidServer && strings.HasPrefix(importPath, "core/server/"):
					message := tc.ServerViolationLabel
					if message == "" {
						message = tc.Label + " must not import server package"
					}
					violations = append(violations, relPath+": "+message+" "+importPath)
				case importPath == "github.com/charmbracelet/bubbletea":
					violations = append(violations, relPath+": "+tc.Label+" must not import Bubble Tea")
				case importPath == "core/cli/app/commands":
					violations = append(violations, relPath+": "+tc.Label+" must not import app commands")
				case importPath == "core/cli/app":
					violations = append(violations, relPath+": "+tc.Label+" must not import app package")
				}
			}
			ast.Inspect(file, func(node ast.Node) bool {
				ident, ok := node.(*ast.Ident)
				if ok && ident.Name == "uiModel" {
					violations = append(violations, relPath+": "+tc.Label+" must not reference uiModel")
				}
				return true
			})
			return nil
		}); err != nil {
			t.Fatalf("scan cli app internal %s sources: %v", packageName, err)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("cli app internal %s boundary violations:\n%s", tc.Name, strings.Join(violations, "\n"))
	}
}

func TestCLIDoesNotCallPersistenceStorageAPIsDirectly(t *testing.T) {
	repoRoot := findRepoRoot(t)
	forbiddenCalls := map[string]map[string]struct{}{
		"core/server/metadata": {
			"Open":                     {},
			"ResolveBinding":           {},
			"RegisterBinding":          {},
			"EnsureWorkspaceBinding":   {},
			"ResolveWorkspacePath":     {},
			"AttachWorkspaceToProject": {},
			"RebindWorkspace":          {},
		},
		"core/server/session": {
			"Open":         {},
			"OpenByID":     {},
			"Create":       {},
			"NewLazy":      {},
			"ListSessions": {},
		},
	}
	violations := make([]string, 0)
	walkRoot := filepath.Join(repoRoot, "cli")
	if err := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		importAliases := make(map[string]string)
		for _, spec := range file.Imports {
			importPath := strings.Trim(spec.Path.Value, "\"")
			alias := ""
			if spec.Name != nil {
				alias = strings.TrimSpace(spec.Name.Name)
			} else {
				alias = filepath.Base(importPath)
			}
			importAliases[alias] = importPath
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
			ident, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			importPath, ok := importAliases[ident.Name]
			if !ok {
				return true
			}
			forbiddenSelectors, ok := forbiddenCalls[importPath]
			if !ok {
				return true
			}
			if _, forbidden := forbiddenSelectors[selector.Sel.Name]; forbidden {
				relPath, relErr := filepath.Rel(repoRoot, path)
				if relErr != nil {
					relPath = path
				}
				violations = append(violations, relPath+": frontend must not call "+importPath+"."+selector.Sel.Name)
			}
			return true
		})
		return nil
	}); err != nil {
		t.Fatalf("scan cli sources: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("cli persistence boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func loadRepoPackages(t *testing.T, repoRoot string) []goListPackage {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = repoRoot
	cmd.Env = filteredGoListEnv()
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list packages: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	packages := make([]goListPackage, 0)
	for decoder.More() {
		var pkg goListPackage
		if err := decoder.Decode(&pkg); err != nil {
			t.Fatalf("decode go list package json: %v", err)
		}
		packages = append(packages, pkg)
	}
	return packages
}

func filteredGoListEnv() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, "ENV=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root")
		}
		dir = parent
	}
}
