package patch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"core/internal/testharness/filemode"
	"core/server/tools"
)

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

func TestAbsoluteForeignManagedWorktreePatchIsDeniedBeforeMove(t *testing.T) {
	base := t.TempDir()
	currentRoot := filepath.Join(base, "current")
	foreignRoot := filepath.Join(base, "foreign")
	for _, dir := range []string{currentRoot, foreignRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(currentRoot, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested workdir: %v", err)
	}
	source := filepath.Join(currentRoot, "source.txt")
	destination := filepath.Join(foreignRoot, "destination.txt")
	if err := os.WriteFile(source, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	context, err := tools.NewManagedWorktreePathContext(
		base,
		&currentRoot,
		[]string{currentRoot, foreignRoot},
		func() (string, error) { return base, nil },
	)
	if err != nil {
		t.Fatalf("managed worktree path context: %v", err)
	}
	tool := newPatchTestTool(t, filepath.Join(currentRoot, "nested"), WithManagedWorktreePathContext(context))

	result := callPatch(t, tool, "foreign-move", "*** Begin Patch\n*** Update File: "+source+"\n*** Move to: "+destination+"\n-before\n+after\n*** End Patch\n")

	if !result.IsError || result.Summary == nil || *result.Summary != tools.ForeignManagedWorktreeEditDeniedMessage {
		t.Fatalf("foreign worktree patch result = %+v", result)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if string(data) != "before\n" {
		t.Fatalf("source = %q, want before", data)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination stat error = %v, want not exist", err)
	}
}

func TestPatchDeniesSiblingCreatedAfterToolStartupBeforeApproval(t *testing.T) {
	base := outsideNonTempDir(t)
	managedParent := filepath.Join(base, "kent")
	currentRoot := filepath.Join(managedParent, "current")
	currentWorkspace := filepath.Join(currentRoot, "nested")
	if err := os.MkdirAll(currentWorkspace, 0o755); err != nil {
		t.Fatalf("mkdir current Workspace: %v", err)
	}
	pathContext, err := tools.NewManagedWorktreePathContext(
		base,
		&currentRoot,
		[]string{currentRoot},
		func() (string, error) { return base, nil },
	)
	if err != nil {
		t.Fatalf("managed worktree path context: %v", err)
	}
	approvalCalls := 0
	tool := newPatchTestTool(
		t,
		currentWorkspace,
		WithManagedWorktreePathContext(pathContext),
		WithOutsideWorkspaceApprover(func(context.Context, OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error) {
			approvalCalls++
			return OutsideWorkspaceApproval{Decision: OutsideWorkspaceDecisionAllowOnce}, nil
		}),
	)

	siblingRoot := filepath.Join(managedParent, "created-later")
	if err := os.MkdirAll(siblingRoot, 0o755); err != nil {
		t.Fatalf("mkdir later sibling: %v", err)
	}
	siblingFile := filepath.Join(siblingRoot, "foreign.txt")
	if err := os.WriteFile(siblingFile, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write sibling file: %v", err)
	}

	result := callPatch(t, tool, "late-sibling", "*** Begin Patch\n*** Update File: "+siblingFile+"\n-before\n+after\n*** End Patch\n")

	if !result.IsError || result.Summary == nil || *result.Summary != tools.ForeignManagedWorktreeEditDeniedMessage {
		t.Fatalf("sibling worktree patch result = %+v", result)
	}
	if approvalCalls != 0 {
		t.Fatalf("outside-workspace approval calls = %d, want 0", approvalCalls)
	}
	assertPatchFileContent(t, siblingFile, "before\n")
}

func TestPatchDeniesEveryForeignAbsoluteTargetKind(t *testing.T) {
	base := t.TempDir()
	currentRoot := filepath.Join(base, "current")
	foreignRoot := filepath.Join(base, "foreign")
	for _, dir := range []string{currentRoot, foreignRoot, filepath.Join(currentRoot, "nested")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	context, err := tools.NewManagedWorktreePathContext(
		base,
		&currentRoot,
		[]string{currentRoot, foreignRoot},
		func() (string, error) { return base, nil },
	)
	if err != nil {
		t.Fatalf("managed worktree path context: %v", err)
	}
	tool := newPatchTestTool(t, filepath.Join(currentRoot, "nested"), WithManagedWorktreePathContext(context))
	update := filepath.Join(foreignRoot, "update.txt")
	deletePath := filepath.Join(foreignRoot, "delete.txt")
	moveSource := filepath.Join(foreignRoot, "move.txt")
	for _, path := range []string{update, deletePath, moveSource} {
		if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	tests := []struct {
		name  string
		patch string
	}{
		{name: "add", patch: "*** Begin Patch\n*** Add File: " + filepath.Join(foreignRoot, "added.txt") + "\n+after\n*** End Patch\n"},
		{name: "update", patch: "*** Begin Patch\n*** Update File: " + update + "\n-before\n+after\n*** End Patch\n"},
		{name: "delete", patch: "*** Begin Patch\n*** Delete File: " + deletePath + "\n*** End Patch\n"},
		{name: "move source", patch: "*** Begin Patch\n*** Update File: " + moveSource + "\n*** Move to: " + filepath.Join(currentRoot, "moved.txt") + "\n-before\n+after\n*** End Patch\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := callPatch(t, tool, "foreign-"+test.name, test.patch)
			if !result.IsError || result.Summary == nil || *result.Summary != tools.ForeignManagedWorktreeEditDeniedMessage {
				t.Fatalf("foreign %s result = %+v", test.name, result)
			}
		})
	}
}

func TestPatchManagedWorktreeGuardSkipsRelativeCurrentAndOutsideBase(t *testing.T) {
	base := t.TempDir()
	currentRoot := filepath.Join(base, "current")
	foreignRoot := filepath.Join(base, "foreign")
	outsideRoot := t.TempDir()
	for _, dir := range []string{currentRoot, foreignRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(currentRoot, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested workdir: %v", err)
	}
	current := filepath.Join(currentRoot, "current.txt")
	foreign := filepath.Join(foreignRoot, "foreign.txt")
	outside := filepath.Join(outsideRoot, "outside.txt")
	for _, path := range []string{current, foreign, outside} {
		if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	context, err := tools.NewManagedWorktreePathContext(
		base,
		&currentRoot,
		[]string{currentRoot, foreignRoot},
		func() (string, error) { return base, nil },
	)
	if err != nil {
		t.Fatalf("managed worktree path context: %v", err)
	}
	tool := newPatchTestTool(t, filepath.Join(currentRoot, "nested"), WithManagedWorktreePathContext(context), WithAllowOutsideWorkspace(true))

	tests := []struct {
		name    string
		path    string
		old     string
		new     string
		isError bool
	}{
		{name: "relative current", path: "../current.txt", old: "before", new: "after"},
		{name: "relative foreign", path: filepath.Join("..", "..", "foreign", "foreign.txt"), old: "before", new: "after", isError: true},
		{name: "absolute current", path: current, old: "after", new: "again"},
		{name: "absolute outside managed base", path: outside, old: "before", new: "after"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := callPatch(t, tool, "skip-"+tc.name, "*** Begin Patch\n*** Update File: "+tc.path+"\n-"+tc.old+"\n+"+tc.new+"\n*** End Patch\n")
			if result.IsError != tc.isError {
				t.Fatalf("error = %t, want %t; result=%q", result.IsError, tc.isError, patchResultText(t, result))
			}
			if len(result.ModelWarnings) != 0 {
				t.Fatalf("unexpected managed worktree warning: %+v", result.ModelWarnings)
			}
		})
	}
}

func patchResultText(t *testing.T, result tools.Result) string {
	t.Helper()
	var value any
	if err := json.Unmarshal(result.Output, &value); err != nil {
		t.Fatalf("decode patch result: %v", err)
	}
	return fmt.Sprint(value)
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
	filemode.AssertUnixPermissionMode(t, destination, 0o755)
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
	filemode.AssertUnixPermissionMode(t, target, 0o644)
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
	if payload.Reason == "" {
		t.Fatalf("expected missing-target reason, got %+v", payload)
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
	if payload.Reason == "" {
		t.Fatalf("expected existing-target reason, got %+v", payload)
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

	_, err = commitStagedFiles(nil, states, nil)
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

func TestCommitStagedFilesRollsBackOnManagedWorktreeRevalidationFailure(t *testing.T) {
	base := t.TempDir()
	currentRoot := filepath.Join(base, "current")
	foreignRoot := filepath.Join(base, "foreign")
	currentWorkspace := filepath.Join(currentRoot, "nested")
	for _, dir := range []string{currentWorkspace, foreignRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	deleteTarget := filepath.Join(currentRoot, "delete.txt")
	if err := os.WriteFile(deleteTarget, []byte("restore me\n"), 0o644); err != nil {
		t.Fatalf("seed delete target: %v", err)
	}
	foreignTarget := filepath.Join(foreignRoot, "foreign.txt")
	if err := os.Mkdir(foreignTarget, 0o755); err != nil {
		t.Fatalf("seed foreign target directory: %v", err)
	}
	stage, err := createStagedFile(filepath.Join(currentRoot, "staged.txt"), []byte("must not commit\n"), 0o644)
	if err != nil {
		t.Fatalf("stage foreign target: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(stage) })

	context, err := tools.NewManagedWorktreePathContext(
		base,
		&currentRoot,
		[]string{currentRoot, foreignRoot},
		func() (string, error) { return base, nil },
	)
	if err != nil {
		t.Fatalf("managed worktree path context: %v", err)
	}
	tool := newPatchTestTool(t, currentWorkspace, WithManagedWorktreePathContext(context))

	_, err = commitStagedFiles(
		tool,
		[]*patchFileState{{Exists: false, NewPath: foreignTarget, Original: foreignTarget, StagedPath: stage}},
		map[string]wholeFileDeletionTarget{
			deleteTarget: {},
		},
	)
	if err == nil || !strings.Contains(err.Error(), tools.ForeignManagedWorktreeEditDeniedMessage) {
		t.Fatalf("expected managed-worktree revalidation error, got %v", err)
	}
	assertPatchFileContent(t, deleteTarget, "restore me\n")
	if info, statErr := os.Stat(foreignTarget); statErr != nil || !info.IsDir() {
		t.Fatalf("foreign target changed, stat=%v info=%v", statErr, info)
	}
}

func TestCommitStagedFilesRollbackRetainsAuthorityAfterConfiguredRootChanges(t *testing.T) {
	workspace := t.TempDir()
	initialManagedBase := t.TempDir()
	first := filepath.Join(workspace, "first.txt")
	second := filepath.Join(workspace, "second.txt")
	if err := os.WriteFile(first, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("seed first target: %v", err)
	}
	firstStage, err := createStagedFile(first, []byte("patched\n"), 0o644)
	if err != nil {
		t.Fatalf("stage first target: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(firstStage) })
	secondStage, err := createStagedFile(second, []byte("blocked\n"), 0o644)
	if err != nil {
		t.Fatalf("stage second target: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(secondStage) })

	pathContext, err := tools.NewManagedWorktreePathContext(
		initialManagedBase,
		nil,
		nil,
		func() (string, error) {
			data, readErr := os.ReadFile(first)
			if readErr != nil {
				return "", readErr
			}
			if string(data) == "patched\n" {
				return workspace, nil
			}
			return initialManagedBase, nil
		},
	)
	if err != nil {
		t.Fatalf("managed worktree path context: %v", err)
	}
	tool := newPatchTestTool(t, workspace, WithManagedWorktreePathContext(pathContext))

	_, err = commitStagedFiles(tool, []*patchFileState{
		{Exists: true, NewPath: first, Original: first, StagedPath: firstStage},
		{Exists: true, NewPath: second, Original: second, StagedPath: secondStage},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), tools.ForeignManagedWorktreeEditDeniedMessage) {
		t.Fatalf("commit error = %v, want live configured-root denial", err)
	}
	assertPatchFileContent(t, first, "original\n")
	if _, statErr := os.Stat(second); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("second target stat error = %v, want not exist", statErr)
	}
}

func TestPrepareCommitStatesRevalidatesBeforeStagingForeignTargets(t *testing.T) {
	base := t.TempDir()
	currentRoot := filepath.Join(base, "current")
	foreignRoot := filepath.Join(base, "foreign")
	currentWorkspace := filepath.Join(currentRoot, "nested")
	for _, dir := range []string{currentWorkspace, foreignRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	context, err := tools.NewManagedWorktreePathContext(
		base,
		&currentRoot,
		[]string{currentRoot, foreignRoot},
		func() (string, error) { return base, nil },
	)
	if err != nil {
		t.Fatalf("managed worktree path context: %v", err)
	}
	tool := newPatchTestTool(t, currentWorkspace, WithManagedWorktreePathContext(context))
	currentTarget := filepath.Join(currentRoot, "current.txt")
	foreignTarget := filepath.Join(foreignRoot, "foreign.txt")
	state := &applyState{
		tool: tool,
		state: map[string]*patchFileState{
			currentTarget: {NewPath: currentTarget, Content: []string{"current"}, Mode: 0o644},
			foreignTarget: {NewPath: foreignTarget, Content: []string{"foreign"}, Mode: 0o644},
		},
	}

	if _, err := state.prepareCommitStates(); err == nil || !strings.Contains(err.Error(), tools.ForeignManagedWorktreeEditDeniedMessage) {
		t.Fatalf("expected managed-worktree staging error, got %v", err)
	}
	for _, root := range []string{currentRoot, foreignRoot} {
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			t.Fatalf("read %s: %v", root, readErr)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".kent-patch-") {
				t.Fatalf("staging left temporary file %s in %s", entry.Name(), root)
			}
		}
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
