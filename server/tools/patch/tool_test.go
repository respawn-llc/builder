package patch

import (
	"context"
	patchformat "core/shared/transcript/patchformat"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestDeleteFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "1", "*** Begin Patch\n*** Delete File: a.txt\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected file deleted, stat err=%v", err)
	}
}

func TestNewMissingWorkspaceSuggestsRebind(t *testing.T) {
	missingWorkspace := filepath.Join(t.TempDir(), "workspace-removed")

	_, err := New(missingWorkspace, true)
	if err == nil {
		t.Fatal("expected error for missing workspace")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
	want := `workspace root ` + strconv.Quote(missingWorkspace) + ` is missing`
	if got := err.Error(); got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestDeleteParticipatesInAtomicPatchCommit(t *testing.T) {
	dir := t.TempDir()
	deleteTarget := filepath.Join(dir, "delete.txt")
	keepTarget := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(deleteTarget, []byte("delete me\n"), 0o644); err != nil {
		t.Fatalf("write delete target: %v", err)
	}
	if err := os.WriteFile(keepTarget, []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write keep target: %v", err)
	}

	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "atomic-delete", "*** Begin Patch\n*** Delete File: delete.txt\n*** Add File: added.txt\n+hello\n*** Update File: keep.txt\n-two\n+two\n*** End Patch\n")
	if !result.IsError {
		t.Fatalf("expected tool error result")
	}

	deleted, err := os.ReadFile(deleteTarget)
	if err != nil {
		t.Fatalf("read delete target after failure: %v", err)
	}
	if string(deleted) != "delete me\n" {
		t.Fatalf("unexpected delete target contents after rollback: %q", string(deleted))
	}
	if _, err := os.Stat(filepath.Join(dir, "added.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected added file absent after rollback, stat err=%v", err)
	}
	kept, err := os.ReadFile(keepTarget)
	if err != nil {
		t.Fatalf("read keep target after failure: %v", err)
	}
	if string(kept) != "one\n" {
		t.Fatalf("unexpected keep target contents after rollback: %q", string(kept))
	}
}

func TestDeleteAddUpdateCommitTogether(t *testing.T) {
	dir := t.TempDir()
	deleteTarget := filepath.Join(dir, "delete.txt")
	updateTarget := filepath.Join(dir, "update.txt")
	if err := os.WriteFile(deleteTarget, []byte("remove me\n"), 0o644); err != nil {
		t.Fatalf("write delete target: %v", err)
	}
	if err := os.WriteFile(updateTarget, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write update target: %v", err)
	}

	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "mixed-success", "*** Begin Patch\n*** Delete File: delete.txt\n*** Add File: added.txt\n+hello\n*** Update File: update.txt\n one\n-two\n+two updated\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	if _, err := os.Stat(deleteTarget); !os.IsNotExist(err) {
		t.Fatalf("expected delete target removed, stat err=%v", err)
	}
	added, err := os.ReadFile(filepath.Join(dir, "added.txt"))
	if err != nil {
		t.Fatalf("read added file: %v", err)
	}
	if string(added) != "hello\n" {
		t.Fatalf("unexpected added file contents: %q", string(added))
	}
	updated, err := os.ReadFile(updateTarget)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	if string(updated) != "one\ntwo updated\n" {
		t.Fatalf("unexpected updated file contents: %q", string(updated))
	}
}

func TestDeleteThenMoveToSamePathCommitsReplacement(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dest := filepath.Join(dir, "dest.txt")
	if err := os.WriteFile(src, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(dest, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write destination: %v", err)
	}

	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "replace-move", "*** Begin Patch\n*** Delete File: dest.txt\n*** Update File: src.txt\n*** Move to: dest.txt\n line1\n-line2\n+line2 moved\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("expected source removed after move, stat err=%v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read replacement destination: %v", err)
	}
	if string(data) != "line1\nline2 moved\n" {
		t.Fatalf("unexpected replacement destination contents: %q", string(data))
	}
}

func TestDeleteThenAddNestedFileReplacesFileWithDirectory(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "tools")
	if err := os.WriteFile(blocker, []byte("old blocker\n"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "replace-file-dir", "*** Begin Patch\n*** Delete File: tools\n*** Add File: tools/main.go\n+package main\n+\n+func main() {}\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	info, err := os.Stat(filepath.Join(dir, "tools"))
	if err != nil {
		t.Fatalf("stat tools directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected tools to become a directory, mode=%v", info.Mode())
	}
	data, err := os.ReadFile(filepath.Join(dir, "tools", "main.go"))
	if err != nil {
		t.Fatalf("read nested replacement file: %v", err)
	}
	if string(data) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("unexpected nested replacement contents: %q", string(data))
	}
}

func TestAddUpdateMove(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "one.txt")
	if err := os.WriteFile(src, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "2", "*** Begin Patch\n*** Add File: new.txt\n+hello\n*** Update File: one.txt\n*** Move to: moved.txt\n line1\n-line2\n+line2-updated\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("old path still exists")
	}
	moved, err := os.ReadFile(filepath.Join(dir, "moved.txt"))
	if err != nil {
		t.Fatalf("read moved file: %v", err)
	}
	if string(moved) != "line1\nline2-updated\n" {
		t.Fatalf("unexpected moved contents: %q", string(moved))
	}
	added, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil {
		t.Fatalf("read added file: %v", err)
	}
	if string(added) != "hello\n" {
		t.Fatalf("unexpected added contents: %q", string(added))
	}
}

func TestUpdateFilePreservesExecutableMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatalf("seed executable file: %v", err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatalf("mark seed file executable: %v", err)
	}
	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "preserve-executable-mode", "*** Begin Patch\n*** Update File: script.sh\n #!/bin/sh\n-echo old\n+echo new\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	if got, want := string(data), "#!/bin/sh\necho new\n"; got != want {
		t.Fatalf("updated content = %q, want %q", got, want)
	}
	assertUnixFileMode(t, target, 0o755)
}

func TestUpdateAndMoveFilePreservesExecutableMode(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "script.sh")
	destination := filepath.Join(dir, "bin", "script.sh")
	if err := os.WriteFile(source, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatalf("seed executable file: %v", err)
	}
	if err := os.Chmod(source, 0o755); err != nil {
		t.Fatalf("mark seed file executable: %v", err)
	}
	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "move-preserve-executable-mode", "*** Begin Patch\n*** Update File: script.sh\n*** Move to: bin/script.sh\n #!/bin/sh\n-echo old\n+echo new\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected source removed after move, stat err=%v", err)
	}

	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read moved file: %v", err)
	}
	if got, want := string(data), "#!/bin/sh\necho new\n"; got != want {
		t.Fatalf("moved content = %q, want %q", got, want)
	}
	assertUnixFileMode(t, destination, 0o755)
}

func TestUpdateFileUsesCodexStyleContextHeader(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.go")
	if err := os.WriteFile(target, []byte("package main\n\nfunc one() {\n\tprintln(1)\n}\n\nfunc two() {\n\tprintln(2)\n}\n"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "ctx", "*** Begin Patch\n*** Update File: a.go\n@@ func two() {\n-\tprintln(2)\n+\tprintln(22)\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !strings.Contains(string(data), "println(22)") || strings.Contains(string(data), "println(2)\n") {
		t.Fatalf("unexpected target contents: %q", string(data))
	}
}

func TestUpdateFileEndOfFileMarkerAnchorsMatch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("same\nend\nsame\nend\n"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "eof", "*** Begin Patch\n*** Update File: a.txt\n@@\n same\n-end\n+finish\n*** End of File\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "same\nend\nsame\nfinish\n" {
		t.Fatalf("unexpected target contents: %q", string(data))
	}
}

func TestUpdateFileAcceptsWhitespacePaddedEndOfFileMarker(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "eof-padding", "*** Begin Patch\n*** Update File: a.txt\n@@\n-one\n+ONE\n two\n  *** End of File  \n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != "ONE\ntwo\n" {
		t.Fatalf("unexpected target contents: %q", string(data))
	}
}

func TestParseEditHunksPreservesSoleEndOfFileMarker(t *testing.T) {
	hunks, err := parseEditHunks([]patchformat.ChangeLine{{EndOfFile: true}})
	if err != nil {
		t.Fatalf("parseEditHunks: %v", err)
	}
	if len(hunks) != 1 || !hunks[0].endOfFile || len(hunks[0].changes) != 0 {
		t.Fatalf("expected sole EOF marker hunk preserved, got %+v", hunks)
	}
}

func TestParseHunkHeaderParsesPositionedAndContextHeaders(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		want    hunkHeader
		wantErr bool
	}{
		{name: "empty context", line: "@@", want: hunkHeader{}},
		{name: "named context", line: "@@ function body", want: hunkHeader{context: "function body"}},
		{name: "default counts", line: "@@ -4 +7 @@", want: hunkHeader{hasPosition: true, oldStart: 4, oldCount: 1, newStart: 7, newCount: 1}},
		{name: "explicit counts", line: "@@ -4,2 +7,3 @@ section", want: hunkHeader{hasPosition: true, oldStart: 4, oldCount: 2, newStart: 7, newCount: 3}},
		{name: "zero counts", line: "@@ -0,0 +1,0 @@", want: hunkHeader{hasPosition: true, oldStart: 0, oldCount: 0, newStart: 1, newCount: 0}},
		{name: "missing terminator", line: "@@ -1 +1", wantErr: true},
		{name: "missing old start", line: "@@ - +1 @@", wantErr: true},
		{name: "missing old count", line: "@@ -1, +1 @@", wantErr: true},
		{name: "missing new start", line: "@@ -1 + @@", wantErr: true},
		{name: "invalid terminator suffix", line: "@@ -1 +1 @@section", wantErr: true},
		{name: "overflow", line: "@@ -999999999999999999999999999999 +1 @@", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseHunkHeader(tc.line)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseHunkHeader(%q) = %+v, want error", tc.line, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseHunkHeader(%q): %v", tc.line, err)
			}
			if got != tc.want {
				t.Fatalf("parseHunkHeader(%q) = %+v, want %+v", tc.line, got, tc.want)
			}
		})
	}
}

func TestUpdateFileRejectsEmptyHunk(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("one\n"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "empty-update", "*** Begin Patch\n*** Update File: a.txt\n*** End Patch\n")
	if !result.IsError {
		t.Fatal("expected empty update hunk to fail")
	}
	payload := toolFailurePayload(t, result)
	if payload.Kind != "malformed_syntax" || !strings.Contains(payload.Reason, "empty") {
		t.Fatalf("expected malformed empty hunk failure, got %+v", payload)
	}
}

func TestUpdateFileAllowsMoveOnlyHunk(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(src, []byte("content\n"), 0o644); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "move-only", "*** Begin Patch\n*** Update File: src.txt\n*** Move to: dst.txt\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected source removed, stat err=%v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(data) != "content\n" {
		t.Fatalf("unexpected destination contents: %q", data)
	}
}

func TestAddFileInNewDirectory(t *testing.T) {
	dir := t.TempDir()
	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "3", "*** Begin Patch\n*** Add File: nested/new/file.txt\n+hello\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	data, err := os.ReadFile(filepath.Join(dir, "nested", "new", "file.txt"))
	if err != nil {
		t.Fatalf("read added file: %v", err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("unexpected file content: %q", string(data))
	}
}

func TestAddFileUsesDefaultNonExecutableMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "new-script.sh")
	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "add-default-mode", "*** Begin Patch\n*** Add File: new-script.sh\n+#!/bin/sh\n+echo new\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read added file: %v", err)
	}
	if got, want := string(data), "#!/bin/sh\necho new\n"; got != want {
		t.Fatalf("added content = %q, want %q", got, want)
	}
	assertUnixFileMode(t, target, 0o644)
}

func assertUnixFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %04o, want %04o", path, got, want)
	}
}

func TestUpdateAnchorsToHeaderInRepeatedBlocks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "repeat.txt")
	seed := "alpha\nblock-start\nx\nblock-end\nmid\nblock-start\nx\nblock-end\nomega\n"
	if err := os.WriteFile(target, []byte(seed), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "4", "*** Begin Patch\n*** Update File: repeat.txt\n@@ -6,3 +6,3 @@\n block-start\n-x\n+y\n block-end\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	want := "alpha\nblock-start\nx\nblock-end\nmid\nblock-start\ny\nblock-end\nomega\n"
	if string(got) != want {
		t.Fatalf("unexpected updated content:\n%s", string(got))
	}
}

func TestUpdateAnchoredHeaderAllowsFuzz(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "fuzz.txt")
	seed := "line1\nb\nc\nd\nline5\n"
	if err := os.WriteFile(target, []byte(seed), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "5", "*** Begin Patch\n*** Update File: fuzz.txt\n@@ -4,3 +4,3 @@\n b\n-c\n+C\n d\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	want := "line1\nb\nC\nd\nline5\n"
	if string(got) != want {
		t.Fatalf("unexpected updated content:\n%s", string(got))
	}
}

func TestUpdateAnchoredHeaderFailsOutsideFuzz(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "far.txt")
	seed := "line1\nb\nc\nd\nline5\n"
	if err := os.WriteFile(target, []byte(seed), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "6", "*** Begin Patch\n*** Update File: far.txt\n@@ -30,3 +30,3 @@\n b\n-c\n+C\n d\n*** End Patch\n")
	if !result.IsError {
		t.Fatalf("expected patch failure outside fuzz window")
	}
	payload := toolFailurePayload(t, result)
	if payload.Kind != "out_of_bounds" {
		t.Fatalf("expected out_of_bounds failure, got %+v", payload)
	}
	if payload.Line != 30 {
		t.Fatalf("expected line 30 in failure payload, got %+v", payload)
	}
	if !strings.Contains(payload.Error, "outside file bounds") {
		t.Fatalf("expected friendly out-of-bounds error, got %+v", payload)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read file after failed patch: %v", err)
	}
	if string(got) != seed {
		t.Fatalf("file changed despite failed patch:\n%s", string(got))
	}
}

func TestMalformedPatchReturnsStructuredFailure(t *testing.T) {
	dir := t.TempDir()
	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "malformed", "*** Begin Patch\n*** Update File: a.txt\n-invalid\n")
	if !result.IsError {
		t.Fatal("expected malformed patch failure")
	}
	payload := toolFailurePayload(t, result)
	if payload.Kind != "malformed_syntax" {
		t.Fatalf("expected malformed_syntax payload, got %+v", payload)
	}
	if !strings.Contains(payload.Error, "Patch failed: malformed patch syntax.") {
		t.Fatalf("expected friendly malformed syntax error, got %+v", payload)
	}
	if payload.Reason == "" {
		t.Fatalf("expected detailed syntax reason, got %+v", payload)
	}
}

func TestUpdateMissingTargetReturnsStructuredFailure(t *testing.T) {
	dir := t.TempDir()
	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "missing-target", "*** Begin Patch\n*** Update File: missing.txt\n-old\n+new\n*** End Patch\n")
	if !result.IsError {
		t.Fatal("expected missing target failure")
	}
	payload := toolFailurePayload(t, result)
	if payload.Kind != "target_missing" {
		t.Fatalf("expected target_missing payload, got %+v", payload)
	}
	if payload.Path != "missing.txt" {
		t.Fatalf("expected missing path in payload, got %+v", payload)
	}
	if got, want := payload.Error, "Patch failed: target file does not exist: missing.txt.\nReason: cannot update a file that does not exist"; got != want {
		t.Fatalf("unexpected missing target error = %q, want %q", got, want)
	}
}

func TestUpdateContentMismatchPreservesTargetPathInFailurePayload(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "content-mismatch", "*** Begin Patch\n*** Update File: a.txt\n@@\n-one\n+uno\n three\n*** End Patch\n")
	if !result.IsError {
		t.Fatal("expected content mismatch failure")
	}
	payload := toolFailurePayload(t, result)
	if payload.Kind != "content_mismatch" {
		t.Fatalf("expected content_mismatch payload, got %+v", payload)
	}
	if payload.Path != "a.txt" {
		t.Fatalf("expected target path in payload, got %+v", payload)
	}
	if !strings.Contains(payload.Reason, "hunk 1:") {
		t.Fatalf("expected hunk context in reason, got %+v", payload)
	}
	if strings.Contains(payload.Path, "hunk 1") {
		t.Fatalf("expected real file path instead of hunk label, got %+v", payload)
	}
}

func TestAddExistingTargetReturnsStructuredFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "exists.txt")
	if err := os.WriteFile(target, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}
	tool := newPatchTestTool(t, dir)

	result := callPatch(t, tool, "existing-target", "*** Begin Patch\n*** Add File: exists.txt\n+new\n*** End Patch\n")
	if !result.IsError {
		t.Fatal("expected existing target failure")
	}
	payload := toolFailurePayload(t, result)
	if payload.Kind != "target_exists" {
		t.Fatalf("expected target_exists payload, got %+v", payload)
	}
	if payload.Path != "exists.txt" {
		t.Fatalf("expected existing path in payload, got %+v", payload)
	}
	if got, want := payload.Error, "Patch failed: target file already exists: exists.txt.\nReason: cannot add a file over an existing path"; got != want {
		t.Fatalf("unexpected existing target error = %q, want %q", got, want)
	}
}

func TestCommitStagedFilesRollsBackCommittedTargetsOnLaterFailure(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.txt")
	if err := os.WriteFile(first, []byte("original-first\n"), 0o644); err != nil {
		t.Fatalf("seed first file: %v", err)
	}

	blockingDir := filepath.Join(dir, "z-blocking-dir")
	if err := os.Mkdir(blockingDir, 0o755); err != nil {
		t.Fatalf("seed blocking dir: %v", err)
	}

	firstStage, err := createStagedFile(first, []byte("patched-first\n"), 0o644)
	if err != nil {
		t.Fatalf("stage first file: %v", err)
	}
	defer func() { _ = os.Remove(firstStage) }()
	secondStage, err := createStagedFile(blockingDir, []byte("patched-second\n"), 0o644)
	if err != nil {
		t.Fatalf("stage second file: %v", err)
	}
	defer func() { _ = os.Remove(secondStage) }()

	states := []*patchFileState{
		{Exists: true, NewPath: first, Original: first, StagedPath: firstStage},
		{Exists: true, NewPath: blockingDir, Original: blockingDir, StagedPath: secondStage},
	}

	err = commitStagedFiles(states, nil)
	if err == nil {
		t.Fatal("expected transactional commit failure")
	}

	gotFirst, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("read first file: %v", err)
	}
	if string(gotFirst) != "original-first\n" {
		t.Fatalf("first file not rolled back: %q", string(gotFirst))
	}

	info, err := os.Stat(blockingDir)
	if err != nil {
		t.Fatalf("stat blocking dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("blocking path changed type")
	}
}

func TestOutsideWorkspaceEditAllowedWhenConfigured(t *testing.T) {
	workspace := t.TempDir()
	outsideRoot := outsideNonTempDir(t)
	target := filepath.Join(outsideRoot, "outside.txt")
	if err := os.WriteFile(target, []byte("start\n"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	tool := newPatchTestTool(t, workspace, WithAllowOutsideWorkspace(true))

	result := callPatch(t, tool, "allow-config", "*** Begin Patch\n*** Update File: "+target+"\n-start\n+done\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}
	if string(got) != "done\n" {
		t.Fatalf("outside file not updated: %q", string(got))
	}
}

func TestOutsideWorkspaceTempDirAllowedWithoutApproval(t *testing.T) {
	workspace := t.TempDir()
	outsideRoot := t.TempDir()
	target := filepath.Join(outsideRoot, "outside.txt")
	if err := os.WriteFile(target, []byte("start\n"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	tool := newPatchTestTool(t, workspace)

	result := callPatch(t, tool, "allow-temp-default", "*** Begin Patch\n*** Update File: "+target+"\n-start\n+done\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success for temp outside path, got %s", string(result.Output))
	}
}

func TestOutsideWorkspaceTempDirBypassesApprover(t *testing.T) {
	workspace := t.TempDir()
	outsideRoot := t.TempDir()
	target := filepath.Join(outsideRoot, "outside.txt")
	if err := os.WriteFile(target, []byte("start\n"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	approveCalls := 0
	tool := newPatchTestTool(t, workspace, WithOutsideWorkspaceApprover(func(context.Context, OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error) {
		approveCalls++
		return OutsideWorkspaceApproval{Decision: OutsideWorkspaceDecisionDeny}, nil
	}))

	result := callPatch(t, tool, "allow-temp-bypass", "*** Begin Patch\n*** Update File: "+target+"\n-start\n+done\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success for temp outside path, got %s", string(result.Output))
	}
	if approveCalls != 0 {
		t.Fatalf("expected temp exclusion to bypass approver, got %d calls", approveCalls)
	}
}

func TestCaseVariantAbsoluteInWorkspaceDoesNotTriggerOutsideApproval(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "inside.txt")
	if err := os.WriteFile(target, []byte("start\n"), 0o644); err != nil {
		t.Fatalf("seed inside file: %v", err)
	}

	variantWorkspace, ok := findCaseVariantExistingAlias(workspace)
	if !ok {
		t.Skip("filesystem does not provide a case-variant alias for workspace path")
	}
	variantTarget := filepath.Join(variantWorkspace, "inside.txt")

	approveCalls := 0
	tool := newPatchTestTool(t, workspace, WithOutsideWorkspaceApprover(func(context.Context, OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error) {
		approveCalls++
		return OutsideWorkspaceApproval{Decision: OutsideWorkspaceDecisionDeny}, nil
	}))

	result := callPatch(t, tool, "case-variant-inside", "*** Begin Patch\n*** Update File: "+variantTarget+"\n-start\n+done\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success for case-variant absolute in-workspace target, got %s", string(result.Output))
	}
	if approveCalls != 0 {
		t.Fatalf("expected no outside-workspace approval prompts, got %d", approveCalls)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read inside file: %v", err)
	}
	if string(got) != "done\n" {
		t.Fatalf("inside file not updated: %q", string(got))
	}
}

func TestOutsideWorkspaceEditRejectionContainsSteeringMessage(t *testing.T) {
	workspace := t.TempDir()
	outsideRoot := outsideNonTempDir(t)
	target := filepath.Join(outsideRoot, "outside.txt")
	if err := os.WriteFile(target, []byte("start\n"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	approveCalls := 0
	tool := newPatchTestTool(t, workspace, WithOutsideWorkspaceApprover(func(context.Context, OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error) {
		approveCalls++
		return OutsideWorkspaceApproval{Decision: OutsideWorkspaceDecisionDeny}, nil
	}))

	result := callPatch(t, tool, "deny-outside", "*** Begin Patch\n*** Update File: "+target+"\n-start\n+done\n*** End Patch\n")
	if !result.IsError {
		t.Fatalf("expected error result")
	}
	if approveCalls != 1 {
		t.Fatalf("expected one approval call, got %d", approveCalls)
	}
	errMessage := toolError(t, result)
	want := "Patch failed: user denied the edit for " + target + "."
	if errMessage != want {
		t.Fatalf("unexpected steering guidance in error, got %q want %q", errMessage, want)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}
	if string(got) != "start\n" {
		t.Fatalf("outside file changed despite rejection: %q", string(got))
	}
}

func TestOutsideWorkspaceAllowSessionSkipsFuturePrompts(t *testing.T) {
	workspace := t.TempDir()
	outsideRoot := outsideNonTempDir(t)
	first := filepath.Join(outsideRoot, "first.txt")
	second := filepath.Join(outsideRoot, "second.txt")
	if err := os.WriteFile(first, []byte("one\n"), 0o644); err != nil {
		t.Fatalf("seed first file: %v", err)
	}
	if err := os.WriteFile(second, []byte("two\n"), 0o644); err != nil {
		t.Fatalf("seed second file: %v", err)
	}

	approveCalls := 0
	tool := newPatchTestTool(t, workspace, WithOutsideWorkspaceApprover(func(context.Context, OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error) {
		approveCalls++
		return OutsideWorkspaceApproval{Decision: OutsideWorkspaceDecisionAllowSession}, nil
	}))

	result := callPatch(t, tool, "allow-session-1", "*** Begin Patch\n*** Update File: "+first+"\n-one\n+one-updated\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected first patch success, got %s", string(result.Output))
	}
	result = callPatch(t, tool, "allow-session-2", "*** Begin Patch\n*** Update File: "+second+"\n-two\n+two-updated\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected second patch success, got %s", string(result.Output))
	}
	if approveCalls != 1 {
		t.Fatalf("expected one approval call, got %d", approveCalls)
	}
}

func TestOutsideWorkspaceAllowOncePromptsEachCall(t *testing.T) {
	workspace := t.TempDir()
	outsideRoot := outsideNonTempDir(t)
	target := filepath.Join(outsideRoot, "outside.txt")
	if err := os.WriteFile(target, []byte("start\n"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	approveCalls := 0
	tool := newPatchTestTool(t, workspace, WithOutsideWorkspaceApprover(func(context.Context, OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error) {
		approveCalls++
		return OutsideWorkspaceApproval{Decision: OutsideWorkspaceDecisionAllowOnce}, nil
	}))

	result := callPatch(t, tool, "allow-once-1", "*** Begin Patch\n*** Update File: "+target+"\n-start\n+mid\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected first patch success, got %s", string(result.Output))
	}
	result = callPatch(t, tool, "allow-once-2", "*** Begin Patch\n*** Update File: "+target+"\n-mid\n+done\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected second patch success, got %s", string(result.Output))
	}
	if approveCalls != 2 {
		t.Fatalf("expected two approval calls, got %d", approveCalls)
	}
}
