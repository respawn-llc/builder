package runtime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestEntryPointsUseUnifiedMetaContextPreparation(t *testing.T) {
	targets := map[string]map[string]bool{
		"background.go": {
			"runQueuedNotices": true,
		},
		"compaction.go": {
			"compactContext": true,
		},
		"engine.go": {
			"SubmitUserMessage":  true,
			"SubmitWorkflowTurn": true,
		},
		"engine_queue_submission.go": {
			"SubmitQueuedUserMessages": true,
		},
		"goal.go": {
			"runGoalTurn": true,
		},
	}
	for fileName, functionNames := range targets {
		filePath := filepath.Join(".", fileName)
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, filePath, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filePath, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			fn, ok := node.(*ast.FuncDecl)
			if !ok || !functionNames[fn.Name.Name] {
				return true
			}
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, ok := selectorCallName(call.Fun)
				if !ok || !isDirectSpecialMetaContextInjector(name) {
					return true
				}
				position := fileSet.Position(call.Pos())
				t.Fatalf("%s calls %s directly at %s; use ensureMetaContextForRequest/ensureMetaContextForCompaction instead", fn.Name.Name, name, position)
				return false
			})
			return false
		})
	}
}

func TestRuntimeMetaContextRenderersStayInsideBuilder(t *testing.T) {
	bannedCalls := map[string]bool{
		"headlessModeMetaMessage":     true,
		"headlessModeExitMetaMessage": true,
		"workflowModeMetaMessage":     true,
		"worktreeModeMetaMessage":     true,
		"worktreeModeExitMetaMessage": true,
	}
	assertNoBannedRuntimeCalls(t, map[string]bool{"meta_context.go": true}, bannedCalls)
}

func TestProductionRuntimeOutputMutationsUseSteeringBoundary(t *testing.T) {
	allowedFiles := map[string]bool{
		"chat_store.go":             true,
		"engine_message_ops.go":     true,
		"engine_state.go":           true,
		"output_steering.go":        true,
		"transcript_persistence.go": true,
		"transcript_projector.go":   true,
		"transcript_scan.go":        true,
	}
	bannedCalls := map[string]bool{
		"appendAssistantMessage":                     true,
		"appendMessage":                              true,
		"appendMessageWithoutConversationUpdate":     true,
		"appendPersistedDiagnosticEntry":             true,
		"appendPersistedLocalEntry":                  true,
		"appendPersistedLocalEntryRecord":            true,
		"appendPersistedLocalEntryWithCondensedText": true,
		"appendReasoningEntries":                     true,
		"appendUserMessage":                          true,
		"appendUserMessageWithoutConversationUpdate": true,
		"emit":                                   true,
		"emitCommittedMessageTranscriptAdvanced": true,
		"emitCommittedTranscriptAdvanced":        true,
		"emitConversationUpdated":                true,
		"persistToolCompletion":                  true,
	}
	assertNoBannedRuntimeCalls(t, allowedFiles, bannedCalls)
}

func TestRawOutputMutationPrimitivesStayInsideSteeringBoundary(t *testing.T) {
	allowedFiles := map[string]bool{
		"engine_message_ops.go": true,
		"engine_state.go":       true,
		"output_steering.go":    true,
	}
	bannedCalls := map[string]bool{
		"appendMessageRaw":                   true,
		"appendPersistedLocalEntryRecordRaw": true,
		"emitRaw":                            true,
		"persistToolCompletionRaw":           true,
	}
	assertNoBannedRuntimeCalls(t, allowedFiles, bannedCalls)
}

func TestTranscriptProjectionMutationsStayInsideOutputBoundary(t *testing.T) {
	allowedFiles := map[string]bool{
		"compaction_persistence.go": true,
		"engine_message_ops.go":     true,
		"engine_state.go":           true,
		"message_lifecycle.go":      true,
		"output_steering.go":        true,
		"transcript_persistence.go": true,
		"transcript_projector.go":   true,
	}
	bannedCalls := map[string]bool{
		"AppendMessage":                         true,
		"AppendLocalEntryRecord":                true,
		"AppendCommittedEntryWithCondensedText": true,
		"AppendCommittedEntryWithVisibility":    true,
		"AppendStreamingDelta":                  true,
		"ClearStreamingAssistantState":          true,
		"RecordStoredToolCompletion":            true,
		"ReplaceHistory":                        true,
	}
	assertNoBannedRuntimeCalls(t, allowedFiles, bannedCalls)
}

func TestCommittedHistoryReplacementStoreAPIStaysInsideReplacementBoundary(t *testing.T) {
	allowedFiles := map[string]bool{
		"compaction_persistence.go": true,
	}
	bannedCalls := map[string]bool{
		"AppendEventWithCommitStatus": true,
	}
	assertNoBannedRuntimeCalls(t, allowedFiles, bannedCalls)
}

func assertNoBannedRuntimeCalls(t *testing.T, allowedFiles map[string]bool, bannedCalls map[string]bool) {
	t.Helper()
	fileSet := token.NewFileSet()
	if err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if allowedFiles[filepath.Base(path)] {
			return nil
		}
		file, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := selectorCallName(call.Fun)
			if !ok || !bannedCalls[name] {
				return true
			}
			position := fileSet.Position(call.Pos())
			t.Fatalf("%s calls %s directly at %s; route runtime output mutations through steer/queue intents", path, name, position)
			return false
		})
		return nil
	}); err != nil {
		t.Fatalf("walk runtime files: %v", err)
	}
}

func selectorCallName(expr ast.Expr) (string, bool) {
	switch typed := expr.(type) {
	case *ast.SelectorExpr:
		return typed.Sel.Name, true
	case *ast.Ident:
		return typed.Name, true
	default:
		return "", false
	}
}

func isDirectSpecialMetaContextInjector(name string) bool {
	if name == "ensureMetaContextForRequest" || name == "ensureMetaContextForCompaction" {
		return false
	}
	if strings.HasPrefix(name, "inject") && strings.HasSuffix(name, "IfNeeded") {
		return true
	}
	return name == "materializePendingWorktreeReminder"
}
