package edit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"core/internal/testharness/filemode"
	testsetup "core/internal/testharness/testsetup"
	"core/server/tools"
	patchtool "core/server/tools/patch"
	"core/shared/toolspec"
)

func TestCreateMissingFileReturnsJSONString(t *testing.T) {
	dir := t.TempDir()
	tool := newTestTool(t, dir)

	result := callEdit(t, tool, map[string]any{
		"path":       "nested/a.txt",
		"old_string": "",
		"new_string": "hello\n",
	})

	requireEditSuccess(t, result)
	assertJSONText(t, result.Output, "ok")
	assertEditTestFileContent(t, filepath.Join(dir, "nested", "a.txt"), "hello\n")
	if result.Presentation != nil || result.PresentationDelta != nil {
		t.Fatalf("edit handler must not own transcript presentation, got presentation=%+v delta=%+v", result.Presentation, result.PresentationDelta)
	}
}

func TestSuccessfulAbsoluteForeignManagedWorktreeEditWarns(t *testing.T) {
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
	foreignFile := filepath.Join(foreignRoot, "foreign.txt")
	writeEditTestFile(t, foreignFile, "before\n", 0o644)
	context, err := tools.NewManagedWorktreePathContext(base, &currentRoot)
	if err != nil {
		t.Fatalf("managed worktree path context: %v", err)
	}
	tool := newTestTool(t, filepath.Join(currentRoot, "nested"), WithManagedWorktreePathContext(context))

	result := callEdit(t, tool, map[string]any{
		"path":       foreignFile,
		"old_string": "before",
		"new_string": "after",
	})

	requireEditSuccess(t, result)
	if len(result.ModelWarnings) != 1 || result.ModelWarnings[0].Kind != tools.ModelWarningForeignManagedWorktreeEdit {
		t.Fatalf("managed worktree warning = %+v", result.ModelWarnings)
	}
	assertEditTestFileContent(t, foreignFile, "after\n")
}

func TestEditManagedWorktreeWarningSkipsNonForeignOrFailedTargets(t *testing.T) {
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
	currentFile := filepath.Join(currentRoot, "current.txt")
	foreignFile := filepath.Join(foreignRoot, "foreign.txt")
	outsideFile := filepath.Join(outsideRoot, "outside.txt")
	writeEditTestFile(t, currentFile, "before\n", 0o644)
	writeEditTestFile(t, foreignFile, "before\n", 0o644)
	writeEditTestFile(t, outsideFile, "before\n", 0o644)
	context, err := tools.NewManagedWorktreePathContext(base, &currentRoot)
	if err != nil {
		t.Fatalf("managed worktree path context: %v", err)
	}
	tool := newTestTool(t, filepath.Join(currentRoot, "nested"), WithManagedWorktreePathContext(context), WithAllowOutsideWorkspace(true))

	tests := []struct {
		name    string
		path    string
		old     string
		new     string
		isError bool
	}{
		{name: "relative current", path: filepath.Join("..", "current.txt"), old: "before", new: "after"},
		{name: "absolute current", path: currentFile, old: "after", new: "again"},
		{name: "absolute outside managed base", path: outsideFile, old: "before", new: "after"},
		{name: "failed foreign", path: foreignFile, old: "missing", new: "after", isError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := callEdit(t, tool, map[string]any{
				"path":       tc.path,
				"old_string": tc.old,
				"new_string": tc.new,
			})
			if result.IsError != tc.isError {
				t.Fatalf("error = %t, want %t; result=%q", result.IsError, tc.isError, toolResultText(t, result))
			}
			if len(result.ModelWarnings) != 0 {
				t.Fatalf("unexpected managed worktree warning: %+v", result.ModelWarnings)
			}
		})
	}
}

func TestExactReplaceAndReplaceAll(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	writeEditTestFile(t, target, "one two one\n", 0o644)
	tool := newTestTool(t, dir)

	first := callEdit(t, tool, map[string]any{
		"path":       "a.txt",
		"old_string": "one",
		"new_string": "ONE",
	})
	if !first.IsError || !strings.Contains(toolResultText(t, first), "matched 2 occurrences") {
		t.Fatalf("expected multiple occurrence failure, got %+v text=%q", first, toolResultText(t, first))
	}

	second := callEdit(t, tool, map[string]any{
		"path":        "a.txt",
		"old_string":  "one",
		"new_string":  "ONE",
		"replace_all": true,
	})
	requireEditSuccess(t, second)
	assertEditTestFileContent(t, target, "ONE two ONE\n")
}

func TestEditReplacementPreservesExecutableMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "script.sh")
	writeEditTestFile(t, target, "#!/bin/sh\necho old\n", 0o755)
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatalf("mark seed file executable: %v", err)
	}
	tool := newTestTool(t, dir)

	result := callEdit(t, tool, map[string]any{
		"path":       "script.sh",
		"old_string": "echo old",
		"new_string": "echo new",
	})
	requireEditSuccess(t, result)
	assertEditTestFileContent(t, target, "#!/bin/sh\necho new\n")
	filemode.AssertUnixPermissionMode(t, target, 0o755)
}

func TestInputAliasesAndConflicts(t *testing.T) {
	dir := t.TempDir()
	tool := newTestTool(t, dir)

	ok := callEdit(t, tool, map[string]any{
		"filePath":   "a.txt",
		"oldText":    "",
		"newText":    "hello",
		"replaceAll": true,
	})
	requireEditSuccess(t, ok)

	conflict := callEdit(t, tool, map[string]any{
		"path":      "a.txt",
		"file_path": "b.txt",
		"oldText":   "",
		"newText":   "hello",
	})
	if !conflict.IsError || !strings.Contains(toolResultText(t, conflict), "conflicting aliases") {
		t.Fatalf("expected conflict failure, got %q", toolResultText(t, conflict))
	}
}

func TestCreateRejectsNonEmptyAndAllowsWhitespaceOnly(t *testing.T) {
	dir := t.TempDir()
	nonEmpty := filepath.Join(dir, "non-empty.txt")
	writeEditTestFile(t, nonEmpty, "already\n", 0o644)
	blank := filepath.Join(dir, "blank.txt")
	writeEditTestFile(t, blank, "  \n\t", 0o644)
	tool := newTestTool(t, dir)

	rejected := callEdit(t, tool, map[string]any{"path": "non-empty.txt", "old_string": "", "new_string": "new"})
	if !rejected.IsError || !strings.Contains(toolResultText(t, rejected), "already contains text") {
		t.Fatalf("expected non-empty rejection, got %q", toolResultText(t, rejected))
	}
	allowed := callEdit(t, tool, map[string]any{"path": "blank.txt", "old_string": "", "new_string": "new"})
	requireEditSuccess(t, allowed)
	assertEditTestFileContent(t, blank, "new")
}

func TestEncodingAndBinaryGuards(t *testing.T) {
	dir := t.TempDir()
	writeEditTestFile(t, filepath.Join(dir, "nul.txt"), "a\x00b", 0o644)
	tool := newTestTool(t, dir)

	nul := callEdit(t, tool, map[string]any{"path": "nul.txt", "old_string": "a", "new_string": "b"})
	if !nul.IsError || !strings.Contains(toolResultText(t, nul), "binary file rejected") {
		t.Fatalf("expected binary rejection, got %q", toolResultText(t, nul))
	}
	png := callEdit(t, tool, map[string]any{"path": "image.png", "old_string": "", "new_string": "text"})
	if !png.IsError || !strings.Contains(toolResultText(t, png), "binary file extension") {
		t.Fatalf("expected extension rejection, got %q", toolResultText(t, png))
	}
}

func TestDeletionIncludesFollowingNewlineAfterUniqueness(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	writeEditTestFile(t, target, "before\nremove me\nafter\n", 0o644)
	tool := newTestTool(t, dir)

	result := callEdit(t, tool, map[string]any{"path": "a.txt", "old_string": "remove me", "new_string": ""})
	requireEditSuccess(t, result)
	assertEditTestFileContent(t, target, "before\nafter\n")
}

func TestContextAwareFallbackRejectsCommonMiddleLineWithoutBoundaryMatch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	original := "alpha\nTODO\nomega\n"
	writeEditTestFile(t, target, original, 0o644)
	tool := newTestTool(t, dir)

	result := callEdit(t, tool, map[string]any{
		"path":       "a.txt",
		"old_string": "before\nTODO\nafter\n",
		"new_string": "changed\n",
	})
	if !result.IsError || !strings.Contains(toolResultText(t, result), "matched 0 occurrences") {
		t.Fatalf("expected 0-match failure, got %+v text=%q", result, toolResultText(t, result))
	}
	assertEditTestFileContent(t, target, original)
}

func TestContextAwareFallbackRejectsMismatchedInteriorLines(t *testing.T) {
	content := "header\nkeep one\nTODO\nkeep two\nfooter\n"
	old := "header\nwant one\nTODO\nwant two\nfooter\n"

	matches := contextAwareMatches(content, old)
	if len(matches) != 0 {
		t.Fatalf("context-aware matches = %+v, want none", matches)
	}
}

func TestContextAwareFallbackAcceptsNormalizedBoundaryAndMiddleLines(t *testing.T) {
	content := "alpha   beta\nTODO item\nomega   tail\n"
	old := "alpha beta\nTODO item\nomega tail\n"

	matches := contextAwareMatches(content, old)
	if len(matches) != 1 {
		t.Fatalf("context-aware matches = %d, want 1", len(matches))
	}
	if matches[0].actual != content {
		t.Fatalf("matched actual = %q, want %q", matches[0].actual, content)
	}
}

func TestPreserveCurlyQuotesKeepsOpeningSingleQuote(t *testing.T) {
	got := preserveCurlyQuotes("‘old’", "'new'")
	if got != "‘new’" {
		t.Fatalf("preserved quote replacement = %q, want %q", got, "‘new’")
	}
}

func TestOutsideWorkspaceAncestorAliasUsesSingleCallApproval(t *testing.T) {
	workspace := t.TempDir()
	outside := newNonTemporaryOutsideDir(t)
	targetDir := filepath.Join(outside, "target")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatalf("create outside target dir: %v", err)
	}
	target := filepath.Join(targetDir, "target.txt")
	writeEditTestFile(t, target, "old\n", 0o644)
	alias := filepath.Join(outside, "alias")
	if err := os.Symlink(targetDir, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	prompts := 0
	tool := newTestTool(t, workspace, WithOutsideWorkspaceApprover(func(context.Context, tools.FSGuardRequest) (tools.FSGuardApproval, error) {
		prompts++
		return tools.FSGuardApproval{Decision: tools.FSGuardDecisionAllowOnce}, nil
	}))

	result := callEdit(t, tool, map[string]any{"path": filepath.Join(alias, "target.txt"), "old_string": "old", "new_string": "new"})
	requireEditSuccess(t, result)
	if prompts != 1 {
		t.Fatalf("outside approval prompts = %d, want 1", prompts)
	}
	assertEditTestFileContent(t, target, "new\n")
}

func TestOutsideWorkspaceMissingAncestorAliasUsesSingleCallApproval(t *testing.T) {
	workspace := t.TempDir()
	outside := newNonTemporaryOutsideDir(t)
	targetDir := filepath.Join(outside, "target")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatalf("create outside target dir: %v", err)
	}
	alias := filepath.Join(outside, "alias")
	if err := os.Symlink(targetDir, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	prompts := 0
	tool := newTestTool(t, workspace, WithOutsideWorkspaceApprover(func(context.Context, tools.FSGuardRequest) (tools.FSGuardApproval, error) {
		prompts++
		return tools.FSGuardApproval{Decision: tools.FSGuardDecisionAllowOnce}, nil
	}))

	result := callEdit(t, tool, map[string]any{"path": filepath.Join(alias, "new.txt"), "old_string": "", "new_string": "new\n"})
	requireEditSuccess(t, result)
	if prompts != 1 {
		t.Fatalf("outside approval prompts = %d, want 1", prompts)
	}
	assertEditTestFileContent(t, filepath.Join(targetDir, "new.txt"), "new\n")
}

func TestOutsideWorkspaceFinalSymlinkRequiresRealPathApproval(t *testing.T) {
	workspace := t.TempDir()
	outside := newNonTemporaryOutsideDir(t)
	target := filepath.Join(outside, "target.txt")
	writeEditTestFile(t, target, "old\n", 0o644)
	link := filepath.Join(outside, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	prompts := 0
	tool := newTestTool(t, workspace, WithOutsideWorkspaceApprover(func(context.Context, tools.FSGuardRequest) (tools.FSGuardApproval, error) {
		prompts++
		return tools.FSGuardApproval{Decision: tools.FSGuardDecisionAllowOnce}, nil
	}))

	result := callEdit(t, tool, map[string]any{"path": link, "old_string": "old", "new_string": "new"})
	requireEditSuccess(t, result)
	if prompts != 2 {
		t.Fatalf("outside approval prompts = %d, want 2", prompts)
	}
}

func TestPathDenyPolicyBlocksCreateReplaceAndRealSymlinkTargets(t *testing.T) {
	workspace := t.TempDir()
	deniedRoot := newNonTemporaryOutsideDir(t)
	existing := filepath.Join(deniedRoot, "existing.txt")
	writeEditTestFile(t, existing, "old\n", 0o644)
	policy, err := tools.CompileLiteralTreePathDenyPolicy(deniedRoot, "synthetic deny")
	if err != nil {
		t.Fatalf("compile path deny policy: %v", err)
	}
	prompts := 0
	tool := newTestTool(t, workspace,
		WithPathDenyPolicy(policy),
		WithOutsideWorkspaceApprover(func(context.Context, tools.FSGuardRequest) (tools.FSGuardApproval, error) {
			prompts++
			return tools.FSGuardApproval{Decision: tools.FSGuardDecisionAllowOnce}, nil
		}),
	)

	createTarget := filepath.Join(deniedRoot, "created.txt")
	created := callEdit(t, tool, map[string]any{"path": createTarget, "old_string": "", "new_string": "new\n"})
	if !created.IsError || !strings.Contains(toolResultText(t, created), "synthetic deny") {
		t.Fatalf("expected synthetic deny create error, got %q", toolResultText(t, created))
	}
	if _, err := os.Stat(createTarget); !os.IsNotExist(err) {
		t.Fatalf("denied create wrote file, stat err=%v", err)
	}

	replaced := callEdit(t, tool, map[string]any{"path": existing, "old_string": "old", "new_string": "new"})
	if !replaced.IsError || !strings.Contains(toolResultText(t, replaced), "synthetic deny") {
		t.Fatalf("expected synthetic deny replace error, got %q", toolResultText(t, replaced))
	}
	assertEditTestFileContent(t, existing, "old\n")

	alias := filepath.Join(workspace, "alias")
	if err := os.Symlink(deniedRoot, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	symlinked := callEdit(t, tool, map[string]any{"path": filepath.Join(alias, "existing.txt"), "old_string": "old", "new_string": "new"})
	if !symlinked.IsError || !strings.Contains(toolResultText(t, symlinked), "synthetic deny") {
		t.Fatalf("expected synthetic deny symlink error, got %q", toolResultText(t, symlinked))
	}
	if prompts != 0 {
		t.Fatalf("outside approval prompts = %d, want 0", prompts)
	}
}

func writeEditTestFile(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write test file %q: %v", path, err)
	}
}

func assertEditTestFileContent(t *testing.T, path string, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read test file %q: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("test file %q content = %q, want %q", path, string(got), want)
	}
}

func requireEditSuccess(t *testing.T, result tools.Result) {
	t.Helper()
	if result.IsError {
		t.Fatalf("expected edit success, got %s", string(result.Output))
	}
}

func newNonTemporaryOutsideDir(t *testing.T) string {
	t.Helper()
	return testsetup.NonTemporaryDirectory(
		t,
		"kent-edit-outside-",
		patchtool.IsPathInTemporaryDir,
	)
}

func newTestTool(t *testing.T, dir string, opts ...Option) *Tool {
	t.Helper()
	tool, err := New(dir, true, opts...)
	if err != nil {
		t.Fatalf("new edit tool: %v", err)
	}
	return tool
}

func callEdit(t *testing.T, tool *Tool, payload map[string]any) tools.Result {
	t.Helper()
	input, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	result, err := tool.Call(context.Background(), tools.Call{ID: "call", Name: toolspec.ToolEdit, Input: input})
	if err != nil {
		t.Fatalf("edit call error: %v", err)
	}
	return result
}

func toolResultText(t *testing.T, result tools.Result) string {
	t.Helper()
	var text string
	if err := json.Unmarshal(result.Output, &text); err != nil {
		t.Fatalf("decode result output: %v", err)
	}
	return text
}

func assertJSONText(t *testing.T, raw json.RawMessage, want string) {
	t.Helper()
	got := toolResultText(t, tools.Result{Output: raw})
	if got != want {
		t.Fatalf("result output = %q, want %q", got, want)
	}
}
