package core_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
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
		"QueueRuntimeUserMessage(",
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
		"func (c *sessionRuntimeClient) QueueRuntimeUserMessage(",
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
			if rawEngineBridges[selector.Sel.Name] &&
				!approvedRawEngineAdapterDirs[filepath.ToSlash(filepath.Dir(relPath))] &&
				!approvedRawEngineAdapterFiles[filepath.ToSlash(relPath)] {
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
var approvedRawEngineAdapterFiles = map[string]bool{"server/runtimecommand/execution.go": true}

func forbiddenRuntimeLivenessIdentifier(name string) (string, bool) {
	switch name {
	case "LatestRun", "AppendRunStarted", "AppendRunFinished", "MarkInFlight", "InFlightStep":
		return name, true
	default:
		return "", false
	}
}

var allowedRuntimeLivenessSources = map[string]map[string]string{}
