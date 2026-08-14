package core_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

var forbiddenFullTranscriptProjectors = map[string]struct{}{"ChatSnapshot": {}, "TranscriptPageSnapshot": {}}
var fullEventLogWalkSelectorAllowlist = map[string]struct{}{filepath.Join("server", "session", "fork.go"): {}, filepath.Join("server", "session", "sessiontest", "sessiontest.go"): {}}
var fullEventLogWalkSelectors = map[string]struct{}{"WalkEvents": {}, "WalkRecords": {}}
var fullEventLogReaderIdents = map[string]struct{}{"walkEventsFile": {}, "walkEventsFromReader": {}}
var walkHelperIdentAllowlist = map[string]struct{}{filepath.Join("server", "session", "event_log.go"): {}, filepath.Join("server", "session", "store.go"): {}}

func TestProductionTranscriptReadsStayBounded(t *testing.T) {
	repoRoot := findRepoRoot(t)
	violations := make([]string, 0)
	if err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipMaterializationScanDir(d.Name()) {
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
		_, walkSelectorAllowed := fullEventLogWalkSelectorAllowlist[relPath]
		_, walkHelperAllowed := walkHelperIdentAllowlist[relPath]
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok {
				if _, forbidden := fullEventLogReaderIdents[ident.Name]; forbidden && !walkHelperAllowed {
					violations = append(violations, relPath+": production code must not read the full event log via "+ident.Name+" (use ReadSegmentBackward/ReadRecentEvents/ReadEventsBackwardUntil)")
				}
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, forbidden := forbiddenFullTranscriptProjectors[selector.Sel.Name]; forbidden {
				violations = append(violations, relPath+": production code must not call full-transcript projector "+selector.Sel.Name+" (serve bounded cursor pages instead)")
			}
			if _, forbidden := fullEventLogWalkSelectors[selector.Sel.Name]; forbidden && !walkSelectorAllowed {
				violations = append(violations, relPath+": production code must not scan the full event log via "+selector.Sel.Name+" for transcript reads (use ReadSegmentBackward/ReadRecentEvents/ReadEventsBackwardUntil)")
			}
			return true
		})
		return nil
	}); err != nil {
		t.Fatalf("scan repository for unbounded transcript reads: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("unbounded transcript read violations:\n%s", strings.Join(violations, "\n"))
	}
}
