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

// forbiddenFullTranscriptProjectors materialize an entire events.jsonl history
// into a transcript snapshot. User-visible transcript must be served from
// bounded record windows (ReadSegmentBackward/ReadRecentRecords) projected
// through the engine's cursor page, never a
// full byte-0->EOF scan. These projectors survive only for test assertions.
var forbiddenFullTranscriptProjectors = map[string]struct{}{
	"ChatSnapshot":           {},
	"TranscriptPageSnapshot": {},
}

// walkRecordsSelectorAllowlist enumerates the production files permitted to
// scan the whole event log via MaterializedEventLog.WalkRecords. Fork is the
// only production consumer (the "cutoff at offset and copy" read the user
// exempted); sessiontest is test support that production must never import.
var walkRecordsSelectorAllowlist = map[string]struct{}{
	filepath.Join("server", "session", "fork.go"):                       {},
	filepath.Join("server", "session", "sessiontest", "sessiontest.go"): {},
}

// fullEventLogReaderIdents are the package-private helpers that read the event
// log front-to-back. They exist solely for current-log validation and the
// capability used by explicit fork/clone materialization.
var fullEventLogReaderIdents = map[string]struct{}{
	"walkCurrentEventLogComplete": {},
}

// walkHelperIdentAllowlist enumerates the files that own complete current-log
// validation and the guarded WalkRecords capability.
var walkHelperIdentAllowlist = map[string]struct{}{
	filepath.Join("server", "session", "event_log_capability.go"):        {},
	filepath.Join("server", "session", "migration_current_validator.go"): {},
}

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
		_, walkSelectorAllowed := walkRecordsSelectorAllowlist[relPath]
		_, walkHelperAllowed := walkHelperIdentAllowlist[relPath]
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok {
				if _, forbidden := fullEventLogReaderIdents[ident.Name]; forbidden && !walkHelperAllowed {
					violations = append(violations, relPath+": production code must not read the full event log via "+ident.Name+" (use bounded MaterializedEventLog record windows)")
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
			if selector.Sel.Name == "WalkRecords" && !walkSelectorAllowed {
				violations = append(violations, relPath+": production code must not scan the full event log via WalkRecords for transcript reads (use bounded MaterializedEventLog record windows)")
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
