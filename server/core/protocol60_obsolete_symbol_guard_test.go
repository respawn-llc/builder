package core_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestProtocol60ObsoleteSymbolsStayDeletedRepositoryWide(t *testing.T) {
	repoRoot := findRepoRoot(t)
	var findings []string
	for _, relPath := range repositoryGoSourcePaths(t, repoRoot) {
		path := filepath.Join(repoRoot, relPath)
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", relPath, err)
		}
		findings = append(findings, protocol60ObsoleteGoFindings(fileSet, file, relPath)...)
	}
	if len(findings) == 0 {
		return
	}
	sort.Strings(findings)
	t.Fatalf("obsolete protocol symbols restored:\n%s", strings.Join(findings, "\n"))
}

func TestProtocol60ObsoleteSymbolGuardRejectsNegativeFixture(t *testing.T) {
	oldPrompt := "Pending" + "PromptEvent"
	oldEvent := "Event"
	oldRoute := "prompt." + "subscribeActivity"
	oldPhase := "legacy_" + "final_answer"
	source := "package fixture\n" +
		"import clientui \"core/shared/clientui\"\n" +
		"type " + oldPrompt + " struct{}\n" +
		"var _ clientui." + oldEvent + "\n" +
		"const route = \"" + oldRoute + "\"\n" +
		"const phase = \"" + oldPhase + "\"\n"
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "fixture.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse negative fixture: %v", err)
	}
	if findings := protocol60ObsoleteGoFindings(fileSet, file, "fixture.go"); len(findings) != 4 {
		t.Fatalf("negative fixture findings = %v, want four obsolete-symbol findings", findings)
	}
}

func repositoryGoSourcePaths(t *testing.T, repoRoot string) []string {
	t.Helper()
	command := exec.Command("git", "ls-files", "-co", "--exclude-standard", "-z", "--", "*.go")
	command.Dir = repoRoot
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list repository Go sources: %v", err)
	}
	rawPaths := bytes.Split(output, []byte{0})
	paths := make([]string, 0, len(rawPaths))
	for _, rawPath := range rawPaths {
		if len(rawPath) == 0 {
			continue
		}
		relPath := filepath.Clean(string(rawPath))
		if _, err := os.Stat(filepath.Join(repoRoot, relPath)); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("stat repository Go source %s: %v", relPath, err)
		}
		paths = append(paths, relPath)
	}
	sort.Strings(paths)
	return paths
}

func protocol60ObsoleteGoFindings(fileSet *token.FileSet, file *ast.File, relPath string) []string {
	obsolete := protocol60ObsoleteGoIdentifiers()
	obsoleteClientUI := protocol60ObsoleteClientUIIdentifiers()
	obsoleteWire := protocol60ObsoleteWireValues()
	clientUIAliases := make(map[string]struct{})
	var findings []string
	addFinding := func(pos token.Pos, symbol string) {
		position := fileSet.Position(pos)
		findings = append(findings, relPath+":"+strconv.Itoa(position.Line)+": obsolete protocol symbol "+symbol)
	}

	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		if importPath == "core/server/"+"runtimefeed" {
			addFinding(spec.Pos(), importPath)
		}
		if importPath != "core/shared/"+"clientui" {
			continue
		}
		alias := "clientui"
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		if alias != "_" && alias != "." {
			clientUIAliases[alias] = struct{}{}
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.Ident:
			if _, forbidden := obsolete[typed.Name]; forbidden {
				addFinding(typed.Pos(), typed.Name)
			}
		case *ast.SelectorExpr:
			alias, ok := typed.X.(*ast.Ident)
			if !ok {
				break
			}
			if _, clientUI := clientUIAliases[alias.Name]; !clientUI {
				break
			}
			if _, forbidden := obsoleteClientUI[typed.Sel.Name]; forbidden {
				addFinding(typed.Sel.Pos(), alias.Name+"."+typed.Sel.Name)
			}
		case *ast.TypeSpec:
			if file.Name.Name != "clientui" {
				break
			}
			if _, forbidden := obsoleteClientUI[typed.Name.Name]; forbidden {
				addFinding(typed.Name.Pos(), typed.Name.Name)
			}
		case *ast.BasicLit:
			if typed.Kind != token.STRING {
				break
			}
			value, err := strconv.Unquote(typed.Value)
			if err == nil {
				if _, forbidden := obsoleteWire[value]; forbidden {
					addFinding(typed.Pos(), value)
				}
			}
		case *ast.Field:
			if typed.Tag == nil {
				break
			}
			tag, err := strconv.Unquote(typed.Tag.Value)
			if err != nil {
				break
			}
			jsonName, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
			if _, forbidden := obsoleteWire[jsonName]; forbidden {
				addFinding(typed.Tag.Pos(), jsonName)
			}
		}
		return true
	})
	return findings
}

func protocol60ObsoleteGoIdentifiers() map[string]struct{} {
	return map[string]struct{}{
		"Assistant" + "PhaseLegacyFinal":                    {},
		"Dependency" + "PromptActivity":                     {},
		"Dependency" + "SessionActivity":                    {},
		"Method" + "PromptActivityComplete":                 {},
		"Method" + "PromptActivityEvent":                    {},
		"Method" + "PromptSubscribeActivity":                {},
		"Method" + "SessionActivityComplete":                {},
		"Method" + "SessionActivityEvent":                   {},
		"Method" + "SessionSubscribeActivity":               {},
		"Pending" + "PromptEvent":                           {},
		"Pending" + "PromptEventType":                       {},
		"Prompt" + "Activity":                               {},
		"Prompt" + "ActivityEventParams":                    {},
		"Prompt" + "ActivityService":                        {},
		"Prompt" + "ActivitySubscribeRequest":               {},
		"Prompt" + "ActivitySubscription":                   {},
		"Session" + "Activity":                              {},
		"Session" + "ActivityEventParams":                   {},
		"Session" + "ActivityService":                       {},
		"Session" + "ActivitySubscribeRequest":              {},
		"Session" + "ActivitySubscription":                  {},
		"Session" + "TranscriptSubscribeRequest":            {},
		"Session" + "TranscriptSubscription":                {},
		"Transcript" + "AssistantStreamAbortReason":         {},
		"Transcript" + "CacheWarningData":                   {},
		"Transcript" + "DiagnosticData":                     {},
		"Transcript" + "MessagePendingSessionPrompt":        {},
		"Transcript" + "MessageQueuedOrSteeredMessageState": {},
		"Transcript" + "Notice":                             {},
		"Transcript" + "NoticeData":                         {},
		"Transcript" + "PendingSessionPrompt":               {},
		"Transcript" + "PendingSessionPromptData":           {},
		"Transcript" + "QueuedOrSteeredMessageState":        {},
		"Transcript" + "RecoveryCause":                      {},
		"Transcript" + "RowGroupKind":                       {},
		"Transcript" + "SubscriptionMessage":                {},
		"Transcript" + "SubscriptionMessageKind":            {},
		"Transcript" + "ToolAbortReason":                    {},
		"prompt" + "ActivityBroker":                         {},
		"protocol59" + "TranscriptProjection":               {},
		"runtime" + "feed":                                  {},
		"runtimefeed" + "TestAssistantStreamID":             {},
		"runtimefeed" + "TestBackgroundActivityID":          {},
		"runtimefeed" + "TestClientRequestID":               {},
		"runtimefeed" + "TestQueueItemID":                   {},
		"runtimefeed" + "TestRunID":                         {},
		"runtimefeed" + "TestRuntimeReadModelUpdate":        {},
		"runtimefeed" + "TestSessionID":                     {},
		"runtimefeed" + "TestSessionIdentity":               {},
		"runtimefeed" + "TestSessionStatus":                 {},
		"runtimefeed" + "TestStepID":                        {},
		"session" + "ActivityBroker":                        {},
	}
}

func protocol60ObsoleteClientUIIdentifiers() map[string]struct{} {
	return map[string]struct{}{
		"Background" + "ShellEvent":         {},
		"Chat" + "Entry":                    {},
		"Compaction" + "Status":             {},
		"Event":                             {},
		"Event" + "Kind":                    {},
		"QueuedUserMessage" + "StatusEvent": {},
		"Reasoning" + "Delta":               {},
		"Run" + "State":                     {},
		"Runtime" + "ActivityOptions":       {},
		"Runtime" + "GoalStatusUpdate":      {},
		"Tool" + "CallMeta":                 {},
		"Tool" + "CallRenderBehavior":       {},
		"Tool" + "PresentationKind":         {},
		"Tool" + "RenderHint":               {},
		"Tool" + "RenderKind":               {},
		"Tool" + "ShellDialect":             {},
	}
}

func protocol60ObsoleteWireValues() map[string]struct{} {
	return map[string]struct{}{
		"legacy_" + "final_answer":       {},
		"prompt." + "activity":           {},
		"prompt." + "activity.complete":  {},
		"prompt." + "subscribeActivity":  {},
		"prompt_" + "activity":           {},
		"session." + "activity":          {},
		"session." + "activity.complete": {},
		"session." + "subscribeActivity": {},
		"session_" + "activity":          {},
	}
}
