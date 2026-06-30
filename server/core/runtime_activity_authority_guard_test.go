package core_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestRuntimeActivityLivenessSourcesStayAllowlisted(t *testing.T) {
	repoRoot := findRepoRoot(t)
	seen := make(map[string]map[string]struct{})
	violations := make([]string, 0)
	for _, root := range []string{"server", "shared", "cli"} {
		walkRoot := filepath.Join(repoRoot, root)
		if err := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "sqlitegen" || d.Name() == "sqlitelifecyclegen" {
					return filepath.SkipDir
				}
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
			ast.Inspect(file, func(node ast.Node) bool {
				ident, ok := node.(*ast.Ident)
				if !ok {
					return true
				}
				source, forbidden := forbiddenRuntimeLivenessIdentifier(ident.Name)
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
				position := fileSet.Position(ident.Pos())
				violations = append(violations, relPath+":"+position.String()+": runtime activity must not use transitional liveness source "+source)
				return true
			})
			return nil
		}); err != nil {
			t.Fatalf("scan runtime liveness sources: %v", err)
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
	allowedPrefixes := []string{
		filepath.Join("server", "runtime") + string(filepath.Separator),
		filepath.Join("server", "runtimeactivity") + string(filepath.Separator),
	}
	for _, root := range []string{"server"} {
		walkRoot := filepath.Join(repoRoot, root)
		if err := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "sqlitegen" || d.Name() == "sqlitelifecyclegen" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			relPath, relErr := filepath.Rel(repoRoot, path)
			if relErr != nil {
				relPath = path
			}
			for _, prefix := range allowedPrefixes {
				if strings.HasPrefix(relPath, prefix) {
					return nil
				}
			}
			fileSet := token.NewFileSet()
			file, parseErr := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
			if parseErr != nil {
				return parseErr
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
				switch selector.Sel.Name {
				case "ActiveRun", "ActiveStepSnapshot":
					position := fileSet.Position(selector.Sel.Pos())
					violations = append(violations, relPath+":"+position.String()+": server liveness must read active step through server/runtimeactivity resolver seam")
				}
				return true
			})
			return nil
		}); err != nil {
			t.Fatalf("scan active-step authority seam: %v", err)
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("runtime active-step authority seam violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestRuntimeReadModelClockConsumersDoNotUseGlobalCoordinator(t *testing.T) {
	repoRoot := findRepoRoot(t)
	for _, relPath := range []string{
		filepath.Join("server", "runtimeops", "coordinator.go"),
		filepath.Join("server", "registry", "prompt_activity_broker.go"),
	} {
		content, err := os.ReadFile(filepath.Join(repoRoot, relPath))
		if err != nil {
			t.Fatalf("read %s: %v", relPath, err)
		}
		if strings.Contains(string(content), "NextReadModelVersion") {
			t.Fatalf("%s must use the registry-owned read-model clock, not runtimeactivity.NextReadModelVersion", relPath)
		}
	}
}

func TestRuntimeClientInputIdentityBoundaryStaysRequestShaped(t *testing.T) {
	repoRoot := findRepoRoot(t)
	content, err := os.ReadFile(filepath.Join(repoRoot, "shared", "clientui", "runtime.go"))
	if err != nil {
		t.Fatalf("read RuntimeClient contract: %v", err)
	}
	for _, forbidden := range []string{
		"SubmitUserMessage(ctx context.Context, text string)",
		"SubmitUserShellCommand(ctx context.Context, command string)",
		"CompactContext(ctx context.Context, args string)",
		"SubmitQueuedUserMessages(ctx context.Context)",
		"QueueUserMessage(text string)",
	} {
		if strings.Contains(string(content), forbidden) {
			t.Fatalf("RuntimeClient must expose request-shaped input APIs with UI-owned operation refs, found %q", forbidden)
		}
	}
	sessionRuntimeClient, err := os.ReadFile(filepath.Join(repoRoot, "cli", "app", "ui_runtime_client_control.go"))
	if err != nil {
		t.Fatalf("read session runtime client controls: %v", err)
	}
	for _, forbidden := range []string{
		"func (c *sessionRuntimeClient) SubmitUserMessage(",
		"func (c *sessionRuntimeClient) SubmitUserShellCommand(",
		"func (c *sessionRuntimeClient) CompactContext(",
		"func (c *sessionRuntimeClient) SubmitQueuedUserMessages(",
		"func (c *sessionRuntimeClient) QueueUserMessage(",
		"QueueUserMessageWithClientRequestID",
	} {
		if strings.Contains(string(sessionRuntimeClient), forbidden) {
			t.Fatalf("sessionRuntimeClient must not synthesize hidden input operation refs, found %q", forbidden)
		}
	}
}

func TestRuntimeViewDoesNotExportGlobalLivenessMainViewHelper(t *testing.T) {
	repoRoot := findRepoRoot(t)
	content, err := os.ReadFile(filepath.Join(repoRoot, "server", "runtimeview", "projection.go"))
	if err != nil {
		t.Fatalf("read runtimeview projection: %v", err)
	}
	if strings.Contains(string(content), "func MainViewFromRuntime(") {
		t.Fatal("runtimeview must not expose MainViewFromRuntime; live activity/version must come from the registry read-model snapshot seam")
	}
}

func TestBUI146DecompositionBoundaries(t *testing.T) {
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	violations = append(violations, productionLineCountViolations(t, repoRoot)...)
	violations = append(violations, importBoundaryViolations(t, repoRoot, runtimeActivityImportBoundary{
		PackagePrefix: "core/server/runtimeactivity",
		ForbiddenPrefixes: []string{
			"core/server/registry",
			"core/server/runtimecontrol",
			"core/server/runtimeops",
			"core/server/runtimeview",
			"core/server/transport",
		},
	})...)
	violations = append(violations, importBoundaryViolations(t, repoRoot, runtimeActivityImportBoundary{
		PackagePrefix:     "core/server/registry",
		ForbiddenPrefixes: []string{"core/server/runtime"},
		AllowedImports: map[string]string{
			"core/server/runtime":     "transitional registry/runtime coupling; Slice 1C moves read-model publication through typed StepLifecycleSink adapters",
			"core/server/runtimeview": "transitional registry projection coupling; Slice 1B routes activity projection through runtimeactivity/runtimeview seams",
		},
	})...)
	violations = append(violations, runtimeControlLedgerOwnershipViolations(t, repoRoot)...)
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("BUI-146 decomposition guard violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestBUI146ProductionRuntimeConstructionWiresStepLifecycleSink(t *testing.T) {
	repoRoot := findRepoRoot(t)
	required := []string{
		filepath.Join("server", "sessionruntime", "service.go"),
		filepath.Join("server", "workflowrunner", "starter.go"),
		filepath.Join("server", "runprompt", "headless.go"),
	}
	var violations []string
	for _, relPath := range required {
		path := filepath.Join(repoRoot, relPath)
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", relPath, err)
		}
		found := false
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "NewStepLifecycleSink" {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || pkg.Name != "runtimewire" {
				return true
			}
			found = true
			return false
		})
		if !found {
			violations = append(violations, relPath+" must wire runtimewire.NewStepLifecycleSink into production runtime construction")
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("runtime construction StepLifecycle wiring guard violations:\n%s", strings.Join(violations, "\n"))
	}
}

func forbiddenRuntimeLivenessIdentifier(name string) (string, bool) {
	switch name {
	case "LatestRun", "AppendRunStarted", "AppendRunFinished", "MarkInFlight", "InFlightStep":
		return name, true
	default:
		return "", false
	}
}

var allowedRuntimeLivenessSources = map[string]map[string]string{}

type runtimeActivityImportBoundary struct {
	PackagePrefix     string
	ForbiddenPrefixes []string
	AllowedImports    map[string]string
}

func importBoundaryViolations(t *testing.T, repoRoot string, boundary runtimeActivityImportBoundary) []string {
	t.Helper()
	packages := loadRepoPackages(t, repoRoot)
	violations := make([]string, 0)
	for _, pkg := range packages {
		if !strings.HasPrefix(pkg.ImportPath, boundary.PackagePrefix) {
			continue
		}
		for _, imported := range pkg.Imports {
			for _, forbiddenPrefix := range boundary.ForbiddenPrefixes {
				if !importMatchesBoundary(imported, forbiddenPrefix) {
					continue
				}
				if reason := boundary.AllowedImports[imported]; strings.TrimSpace(reason) != "" {
					continue
				}
				violations = append(violations, pkg.ImportPath+" must not import "+imported)
			}
		}
	}
	return violations
}

func importMatchesBoundary(imported string, forbiddenPrefix string) bool {
	return imported == forbiddenPrefix || strings.HasPrefix(imported, forbiddenPrefix+"/")
}

func productionLineCountViolations(t *testing.T, repoRoot string) []string {
	t.Helper()
	violations := make([]string, 0)
	if err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "sqlitegen", "sqlitelifecyclegen":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			relPath = path
		}
		if allowedLargeProductionFiles[relPath] != "" {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		lineCount := len(strings.Split(string(source), "\n"))
		if lineCount > 600 {
			violations = append(violations, relPath+" has "+strconv.Itoa(lineCount)+" lines; new production files above 600 lines need a documented exception")
		}
		return nil
	}); err != nil {
		t.Fatalf("scan production line counts: %v", err)
	}
	return violations
}

var allowedLargeProductionFiles = map[string]string{
	filepath.Join("cli", "app", "session_lifecycle.go"):                              "existing lifecycle composition; BUI-146 forced-exit edits must split cleanup policy",
	filepath.Join("cli", "app", "ui_input_queue.go"):                                 "existing queue-input controller; BUI-146 operation reconciliation must delegate additions",
	filepath.Join("cli", "app", "ui_model.go"):                                       "existing UI state surface; BUI-146 must not add runtime liveness state here",
	filepath.Join("cli", "app", "ui_update.go"):                                      "existing Bubble Tea update dispatcher; BUI-146 runtime branches must delegate",
	filepath.Join("cli", "app", "ui_view.go"):                                        "existing Bubble Tea view dispatcher; BUI-146 runtime rendering must delegate",
	filepath.Join("cli", "kent", "main.go"):                                          "existing CLI composition entrypoint",
	filepath.Join("cli", "kent", "workflow_command.go"):                              "existing workflow CLI command entrypoint",
	filepath.Join("cli", "tui", "model.go"):                                          "existing TUI model core",
	filepath.Join("cli", "tui", "model_rendering.go"):                                "existing TUI renderer",
	filepath.Join("cli", "tui", "model_rendering_entries.go"):                        "existing TUI entry renderer",
	filepath.Join("cli", "tui", "scrollback", "ongoing_scrollback_buffer.go"):        "existing native scrollback buffer implementation",
	filepath.Join("cli", "tui", "transcript_projection.go"):                          "existing transcript projection implementation",
	filepath.Join("prompts", "embed.go"):                                             "existing embedded prompt catalog",
	filepath.Join("server", "launch", "planner.go"):                                  "existing launch planner composition",
	filepath.Join("server", "llm", "openai_http_input.go"):                           "existing OpenAI input serialization",
	filepath.Join("server", "llm", "openai_http_stream.go"):                          "existing OpenAI stream transport",
	filepath.Join("server", "llm", "types.go"):                                       "existing provider DTO seam",
	filepath.Join("server", "metadata", "store.go"):                                  "existing metadata persistence seam; BUI-146 liveness changes must move orchestration out",
	filepath.Join("server", "projectview", "service.go"):                             "existing project read-model service",
	filepath.Join("server", "registry", "runtime_registry.go"):                       "existing registry god file; BUI-146 edits must remain wiring/delegation only",
	filepath.Join("server", "runtime", "chat_store.go"):                              "existing bounded working-set store",
	filepath.Join("server", "runtime", "compaction.go"):                              "existing compaction orchestration; BUI-146 active-kind edits must stay focused",
	filepath.Join("server", "runtime", "engine.go"):                                  "existing runtime engine orchestration; BUI-146 active-kind/interrupt edits must delegate",
	filepath.Join("server", "runtime", "engine_state.go"):                            "existing runtime state container",
	filepath.Join("server", "runtime", "goal.go"):                                    "existing goal orchestration file; BUI-146 edits must delegate new semantics to focused helpers",
	filepath.Join("server", "runtime", "meta_context.go"):                            "existing runtime context assembly",
	filepath.Join("server", "runtime", "step_executor.go"):                           "existing step execution orchestration",
	filepath.Join("server", "runtimecontrol", "service.go"):                          "existing runtimecontrol orchestration file; BUI-146 must not add ledgers/state machines here",
	filepath.Join("server", "session", "event_log.go"):                               "existing bounded event log plumbing",
	filepath.Join("server", "session", "store.go"):                                   "existing session persistence god file; BUI-146 model recovery must move into focused helpers",
	filepath.Join("server", "sessionruntime", "service.go"):                          "existing session runtime lifecycle service; BUI-146 close-policy edits must delegate",
	filepath.Join("server", "tools", "shell", "postprocess", "file_read_context.go"): "existing shell output postprocessor",
	filepath.Join("server", "tools", "transcript_contracts.go"):                      "existing tool transcript contract catalog",
	filepath.Join("server", "transport", "gateway_unary_handlers.go"):                "existing generated-style route switch; BUI-146 owner-drop edits must stay route plumbing only",
	filepath.Join("server", "workflow", "validation.go"):                             "existing workflow validation rules",
	filepath.Join("server", "workflowrunner", "starter.go"):                          "existing workflow runner composition",
	filepath.Join("server", "workflowruntime", "completion.go"):                      "existing workflow/runtime completion seam",
	filepath.Join("server", "workflowstore", "graph_save.go"):                        "existing workflow graph persistence",
	filepath.Join("server", "workflowstore", "runs.go"):                              "workflow task-run persistence is separate from session runtime liveness",
	filepath.Join("server", "workflowstore", "store.go"):                             "existing workflow persistence seam",
	filepath.Join("server", "workflowstore", "tasks.go"):                             "existing workflow task persistence",
	filepath.Join("server", "workflowsvc", "service.go"):                             "existing workflow service orchestration",
	filepath.Join("server", "workflowview", "service.go"):                            "existing workflow read-model service",
	filepath.Join("server", "worktree", "service.go"):                                "existing worktree service; BUI-146 activity blocker edits must delegate",
	filepath.Join("shared", "apicontract", "service_contracts.go"):                   "shared route-shaped service interface seam",
	filepath.Join("shared", "client", "remote.go"):                                   "existing remote RPC client seam",
	filepath.Join("shared", "client", "runtime_client.go"):                           "shared runtime client adapter seam",
	filepath.Join("shared", "config", "config_registry.go"):                          "existing config registry",
	filepath.Join("shared", "serverapi", "requests.go"):                              "shared request DTO seam groups route-shaped contracts",
	filepath.Join("shared", "serverapi", "responses.go"):                             "shared response DTO seam groups route-shaped contracts",
	filepath.Join("shared", "serverapi", "workflow.go"):                              "workflow-domain DTO seam separate from session runtime liveness",
}

func runtimeControlLedgerOwnershipViolations(t *testing.T, repoRoot string) []string {
	t.Helper()
	path := filepath.Join(repoRoot, "server", "runtimecontrol", "service.go")
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse runtimecontrol service: %v", err)
	}
	violations := make([]string, 0)
	ast.Inspect(file, func(node ast.Node) bool {
		typeSpec, ok := node.(*ast.TypeSpec)
		if !ok {
			return true
		}
		name := strings.ToLower(typeSpec.Name.Name)
		if strings.Contains(name, "ledger") || strings.Contains(name, "operationcoordinator") || strings.Contains(name, "interruptstate") {
			position := fileSet.Position(typeSpec.Pos())
			violations = append(violations, "server/runtimecontrol/service.go:"+position.String()+": service.go must not own runtimeops ledger or interrupt state machine type "+typeSpec.Name.Name)
		}
		return true
	})
	return violations
}
