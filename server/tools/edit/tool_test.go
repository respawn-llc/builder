package edit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"core/internal/testharness/filemode"
	"core/server/tools"
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

	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}
	assertJSONText(t, result.Output, "ok")
	got, err := os.ReadFile(filepath.Join(dir, "nested", "a.txt"))
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("created content = %q", string(got))
	}
	if result.Presentation != nil || result.PresentationDelta != nil {
		t.Fatalf("edit handler must not own transcript presentation, got presentation=%+v delta=%+v", result.Presentation, result.PresentationDelta)
	}
}

func TestExactReplaceAndReplaceAll(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("one two one\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
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
	if second.IsError {
		t.Fatalf("expected success, got %s", string(second.Output))
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if string(data) != "ONE two ONE\n" {
		t.Fatalf("edited content = %q", string(data))
	}
}

func TestEditReplacementPreservesExecutableMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatalf("seed executable file: %v", err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatalf("mark seed file executable: %v", err)
	}
	tool := newTestTool(t, dir)

	result := callEdit(t, tool, map[string]any{
		"path":       "script.sh",
		"old_string": "echo old",
		"new_string": "echo new",
	})
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if got, want := string(data), "#!/bin/sh\necho new\n"; got != want {
		t.Fatalf("edited content = %q, want %q", got, want)
	}
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
	if ok.IsError {
		t.Fatalf("expected alias success, got %s", string(ok.Output))
	}

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
	if err := os.WriteFile(nonEmpty, []byte("already\n"), 0o644); err != nil {
		t.Fatalf("seed non-empty: %v", err)
	}
	blank := filepath.Join(dir, "blank.txt")
	if err := os.WriteFile(blank, []byte("  \n\t"), 0o644); err != nil {
		t.Fatalf("seed blank: %v", err)
	}
	tool := newTestTool(t, dir)

	rejected := callEdit(t, tool, map[string]any{"path": "non-empty.txt", "old_string": "", "new_string": "new"})
	if !rejected.IsError || !strings.Contains(toolResultText(t, rejected), "already contains text") {
		t.Fatalf("expected non-empty rejection, got %q", toolResultText(t, rejected))
	}
	allowed := callEdit(t, tool, map[string]any{"path": "blank.txt", "old_string": "", "new_string": "new"})
	if allowed.IsError {
		t.Fatalf("expected blank replacement success, got %s", string(allowed.Output))
	}
	got, err := os.ReadFile(blank)
	if err != nil {
		t.Fatalf("read blank replacement: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("blank replacement = %q", string(got))
	}
}

func TestEncodingAndBinaryGuards(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nul.txt"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatalf("seed nul: %v", err)
	}
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
	if err := os.WriteFile(target, []byte("before\nremove me\nafter\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	tool := newTestTool(t, dir)

	result := callEdit(t, tool, map[string]any{"path": "a.txt", "old_string": "remove me", "new_string": ""})
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if string(got) != "before\nafter\n" {
		t.Fatalf("deleted content = %q", string(got))
	}
}

func TestContextAwareFallbackRejectsCommonMiddleLineWithoutBoundaryMatch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	original := "alpha\nTODO\nomega\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	tool := newTestTool(t, dir)

	result := callEdit(t, tool, map[string]any{
		"path":       "a.txt",
		"old_string": "before\nTODO\nafter\n",
		"new_string": "changed\n",
	})
	if !result.IsError || !strings.Contains(toolResultText(t, result), "matched 0 occurrences") {
		t.Fatalf("expected 0-match failure, got %+v text=%q", result, toolResultText(t, result))
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(got) != original {
		t.Fatalf("file was unexpectedly changed: %q", string(got))
	}
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
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("seed outside target: %v", err)
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

	result := callEdit(t, tool, map[string]any{"path": filepath.Join(alias, "target.txt"), "old_string": "old", "new_string": "new"})
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}
	if prompts != 1 {
		t.Fatalf("outside approval prompts = %d, want 1", prompts)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "new\n" {
		t.Fatalf("target content = %q", string(got))
	}
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
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}
	if prompts != 1 {
		t.Fatalf("outside approval prompts = %d, want 1", prompts)
	}
	got, err := os.ReadFile(filepath.Join(targetDir, "new.txt"))
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "new\n" {
		t.Fatalf("target content = %q", string(got))
	}
}

func TestOutsideWorkspaceFinalSymlinkRequiresRealPathApproval(t *testing.T) {
	workspace := t.TempDir()
	outside := newNonTemporaryOutsideDir(t)
	target := filepath.Join(outside, "target.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("seed outside target: %v", err)
	}
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
	if result.IsError {
		t.Fatalf("expected success, got %s", string(result.Output))
	}
	if prompts != 2 {
		t.Fatalf("outside approval prompts = %d, want 2", prompts)
	}
}

func TestPathDenyPolicyBlocksCreateReplaceAndRealSymlinkTargets(t *testing.T) {
	workspace := t.TempDir()
	deniedRoot := newNonTemporaryOutsideDir(t)
	if err := os.WriteFile(filepath.Join(deniedRoot, "existing.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatalf("seed denied existing file: %v", err)
	}
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

	replaced := callEdit(t, tool, map[string]any{"path": filepath.Join(deniedRoot, "existing.txt"), "old_string": "old", "new_string": "new"})
	if !replaced.IsError || !strings.Contains(toolResultText(t, replaced), "synthetic deny") {
		t.Fatalf("expected synthetic deny replace error, got %q", toolResultText(t, replaced))
	}
	got, err := os.ReadFile(filepath.Join(deniedRoot, "existing.txt"))
	if err != nil {
		t.Fatalf("read denied existing file: %v", err)
	}
	if string(got) != "old\n" {
		t.Fatalf("denied replace changed file to %q", string(got))
	}

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

func newNonTemporaryOutsideDir(t *testing.T) string {
	t.Helper()
	outside, err := os.MkdirTemp(".", "edit-outside-approval-")
	if err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	outside, err = filepath.Abs(outside)
	if err != nil {
		t.Fatalf("resolve outside dir: %v", err)
	}
	if filepath.IsAbs(outside) && strings.Contains(outside, string(filepath.Separator)+"tmp"+string(filepath.Separator)) {
		t.Skip("test outside dir is under temporary editable root")
	}
	return outside
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
