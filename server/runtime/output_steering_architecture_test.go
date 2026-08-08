package runtime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestTranscriptMessagePersistenceStaysBehindSteerAcrossRepository(t *testing.T) {
	t.Parallel()
	allowedCallers := map[string]map[string]bool{
		"normalizePersistedMessageWorktreeContext": {
			"prepareMessageProjection": true,
		},
		"ValidateMessage": {
			"prepareMessageProjection": true,
		},
		"tokenUsageMutationForMessage": {
			"prepareMessageProjection": true,
		},
		"sessionMessageRecordFromLLM": {
			"prepareMessageProjection": true,
		},
		"prepareMessageProjection": {
			"appendMessageRaw":             true,
			"appendQueuedUserMessageFlush": true,
			"prepareResultGroupProjection": true,
		},
		"applyPreparedMessageProjection": {
			"appendMessageRaw":             true,
			"appendQueuedUserMessageFlush": true,
			"applyResultGroupProjection":   true,
		},
		"prepareResultGroupProjection": {
			"flushResultGroup": true,
		},
		"applyResultGroupProjection": {
			"flushResultGroup": true,
		},
		"appendPreparedMessageEvent": {
			"appendMessageRaw":             true,
			"appendQueuedUserMessageFlush": true,
		},
		"appendMessageRaw":             {"applySteeringItem": true},
		"appendQueuedUserMessageFlush": {"applySteeringItem": true},
		"applySteeringItem":            {"steerOrdered": true},
	}
	sessionRuntimeBannedCalls := map[string]bool{
		"AppendCommittedEntry":                  true,
		"AppendCommittedEntryWithCondensedText": true,
		"AppendCommittedEntryWithNoticeID":      true,
		"AppendCommittedEntryWithVisibility":    true,
	}
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	sessionRuntimeRoot := filepath.ToSlash(filepath.Join(repoRoot, "server", "sessionruntime")) + "/"
	fileSet := token.NewFileSet()
	var violations []string
	if err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
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
		file, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			return err
		}
		sessionAliases := packageImportAliases(file, "core/server/session")
		sessionOwned := strings.HasPrefix(filepath.ToSlash(path), filepath.ToSlash(filepath.Join(repoRoot, "server", "session"))+"/")
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			caller := function.Name.Name
			if !sessionOwned && caller != "sessionMessageRecordFromLLM" &&
				(constructsQualifiedType(function, sessionAliases, "MessageRecord") ||
					returnsQualifiedType(function, sessionAliases, "MessageRecord")) {
				violations = append(violations, steeringViolation(
					fileSet.Position(function.Pos()),
					"constructs or returns session.MessageRecord outside the canonical runtime adapter",
				))
			}
			if function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee, ok := selectorCallName(call.Fun)
				if !ok {
					return true
				}
				if strings.HasPrefix(filepath.ToSlash(path), sessionRuntimeRoot) &&
					sessionRuntimeBannedCalls[callee] {
					violations = append(violations, steeringViolation(
						fileSet.Position(call.Pos()),
						"calls generic runtime output operation "+callee+" outside the runtime owner",
					))
				}
				callers, guarded := allowedCallers[callee]
				if !guarded || callers[caller] {
					return true
				}
				violations = append(violations, steeringViolation(
					fileSet.Position(call.Pos()),
					"calls "+callee+" outside the steer call chain",
				))
				return true
			})
		}
		return nil
	}); err != nil {
		t.Fatalf("walk repository Go files: %v", err)
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf(
			"transcript message persistence bypasses steer:\n%s",
			strings.Join(violations, "\n"),
		)
	}
}

func packageImportAliases(file *ast.File, importPath string) map[string]bool {
	aliases := map[string]bool{}
	for _, spec := range file.Imports {
		if strings.Trim(spec.Path.Value, `"`) != importPath {
			continue
		}
		if spec.Name != nil {
			aliases[spec.Name.Name] = true
		} else {
			aliases[filepath.Base(importPath)] = true
		}
	}
	return aliases
}

func constructsQualifiedType(node ast.Node, aliases map[string]bool, name string) bool {
	found := false
	ast.Inspect(node, func(inner ast.Node) bool {
		composite, ok := inner.(*ast.CompositeLit)
		if !ok {
			return true
		}
		selector, ok := composite.Type.(*ast.SelectorExpr)
		if ok && qualifiedSelectorMatches(selector, aliases, name) {
			found = true
			return false
		}
		return true
	})
	return found
}

func returnsQualifiedType(function *ast.FuncDecl, aliases map[string]bool, name string) bool {
	if function.Type.Results == nil {
		return false
	}
	found := false
	ast.Inspect(function.Type.Results, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if ok && qualifiedSelectorMatches(selector, aliases, name) {
			found = true
			return false
		}
		return true
	})
	return found
}

func qualifiedSelectorMatches(selector *ast.SelectorExpr, aliases map[string]bool, name string) bool {
	identifier, ok := selector.X.(*ast.Ident)
	return ok && aliases[identifier.Name] && selector.Sel.Name == name
}

func steeringViolation(position token.Position, message string) string {
	return position.String() + ": " + message
}
