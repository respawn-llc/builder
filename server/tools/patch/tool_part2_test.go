package patch

import (
	"context"
	"core/server/tools"
	"core/shared/toolspec"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

func TestOutsideWorkspaceRejectionIncludesUserCommentary(t *testing.T) {
	workspace := t.TempDir()
	outsideRoot := outsideNonTempDir(t)
	target := filepath.Join(outsideRoot, "outside.txt")
	if err := os.WriteFile(target, []byte("start\n"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	tool, err := New(workspace, true, WithOutsideWorkspaceApprover(func(context.Context, OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error) {
		return OutsideWorkspaceApproval{Decision: OutsideWorkspaceDecisionDeny, Commentary: "not allowed by policy"}, nil
	}))
	if err != nil {
		t.Fatalf("new patch tool: %v", err)
	}

	result := callPatch(t, tool, "deny-commentary", "*** Begin Patch\n*** Update File: "+target+"\n-start\n+done\n*** End Patch\n")
	if !result.IsError {
		t.Fatalf("expected error result")
	}
	errMessage := toolError(t, result)
	want := "Patch failed: user denied the edit for " + target + ".\nUser said: not allowed by policy"
	if errMessage != want {
		t.Fatalf("unexpected rejection error, got %q want %q", errMessage, want)
	}
}

func TestOutsideWorkspaceApprovalFailureUsesPatchSpecificWording(t *testing.T) {
	workspace := t.TempDir()
	outsideRoot := outsideNonTempDir(t)
	target := filepath.Join(outsideRoot, "outside.txt")
	if err := os.WriteFile(target, []byte("start\n"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	tool, err := New(workspace, true, WithOutsideWorkspaceApprover(func(context.Context, OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error) {
		return OutsideWorkspaceApproval{}, errors.New("ask failed")
	}))
	if err != nil {
		t.Fatalf("new patch tool: %v", err)
	}

	result := callPatch(t, tool, "deny-approval-error", "*** Begin Patch\n*** Update File: "+target+"\n-start\n+done\n*** End Patch\n")
	if !result.IsError {
		t.Fatalf("expected error result")
	}
	errMessage := toolError(t, result)
	if !strings.Contains(errMessage, "Patch failed: file edit approval failed") {
		t.Fatalf("expected patch approval failure wording, got %q", errMessage)
	}
	if strings.Contains(errMessage, "read approval failed") || strings.Contains(errMessage, "view_image path outside workspace") {
		t.Fatalf("unexpected non-patch wording, got %q", errMessage)
	}
}

func TestOutsideWorkspaceAddFileRequestsApprovalBeforeMissingPathChecks(t *testing.T) {
	workspace := t.TempDir()
	outsideRoot := outsideNonTempDir(t)
	target := filepath.Join(outsideRoot, "missing", "created.txt")

	approvalCalls := 0
	tool, err := New(workspace, true, WithOutsideWorkspaceApprover(func(context.Context, OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error) {
		approvalCalls++
		return OutsideWorkspaceApproval{Decision: OutsideWorkspaceDecisionAllowOnce}, nil
	}))
	if err != nil {
		t.Fatalf("new patch tool: %v", err)
	}

	result := callPatch(t, tool, "outside-add-missing", "*** Begin Patch\n*** Add File: "+target+"\n+hello\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", toolError(t, result))
	}
	if approvalCalls != 1 {
		t.Fatalf("expected one approval call, got %d", approvalCalls)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read created outside file: %v", err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("unexpected outside file content: %q", string(got))
	}
	if err := os.RemoveAll(filepath.Dir(target)); err != nil {
		t.Fatalf("cleanup created outside tree: %v", err)
	}
}

func TestPathDenyPolicyBlocksPatchOperationsBeforeMutation(t *testing.T) {
	workspace := t.TempDir()
	deniedRoot := outsideNonTempDir(t)
	normalRoot := outsideNonTempDir(t)
	if err := os.MkdirAll(filepath.Join(deniedRoot, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir denied root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deniedRoot, "update.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatalf("seed denied update: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deniedRoot, "delete.txt"), []byte("delete\n"), 0o644); err != nil {
		t.Fatalf("seed denied delete: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deniedRoot, "move-src.txt"), []byte("move\n"), 0o644); err != nil {
		t.Fatalf("seed denied move source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(normalRoot, "normal-src.txt"), []byte("normal\n"), 0o644); err != nil {
		t.Fatalf("seed normal source: %v", err)
	}
	approvals := 0
	tool := newPatchTestTool(t, workspace,
		WithAllowOutsideWorkspace(true),
		WithPathDenyPolicy(compilePatchDenyPolicyForTest(t, deniedRoot, "synthetic deny")),
		WithOutsideWorkspaceApprover(func(context.Context, OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error) {
			approvals++
			return OutsideWorkspaceApproval{Decision: OutsideWorkspaceDecisionAllowOnce}, nil
		}),
	)

	tests := []struct {
		name   string
		patch  string
		assert func(*testing.T)
	}{
		{"add", "*** Begin Patch\n*** Add File: " + filepath.Join(deniedRoot, "nested", "added.txt") + "\n+new\n*** End Patch\n", func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(deniedRoot, "nested", "added.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("denied add created file, stat err=%v", err)
			}
		}},
		{"update", "*** Begin Patch\n*** Update File: " + filepath.Join(deniedRoot, "update.txt") + "\n-old\n+new\n*** End Patch\n", func(t *testing.T) {
			assertPatchFileContent(t, filepath.Join(deniedRoot, "update.txt"), "old\n")
		}},
		{"delete", "*** Begin Patch\n*** Delete File: " + filepath.Join(deniedRoot, "delete.txt") + "\n*** End Patch\n", func(t *testing.T) {
			assertPatchFileContent(t, filepath.Join(deniedRoot, "delete.txt"), "delete\n")
		}},
		{"move-source", "*** Begin Patch\n*** Update File: " + filepath.Join(deniedRoot, "move-src.txt") + "\n*** Move to: " + filepath.Join(normalRoot, "moved.txt") + "\n-move\n+moved\n*** End Patch\n", func(t *testing.T) {
			assertPatchFileContent(t, filepath.Join(deniedRoot, "move-src.txt"), "move\n")
			if _, err := os.Stat(filepath.Join(normalRoot, "moved.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("denied move created destination, stat err=%v", err)
			}
		}},
		{"move-destination", "*** Begin Patch\n*** Update File: " + filepath.Join(normalRoot, "normal-src.txt") + "\n*** Move to: " + filepath.Join(deniedRoot, "moved-in.txt") + "\n-normal\n+moved\n*** End Patch\n", func(t *testing.T) {
			assertPatchFileContent(t, filepath.Join(normalRoot, "normal-src.txt"), "normal\n")
			if _, err := os.Stat(filepath.Join(deniedRoot, "moved-in.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("denied move created destination, stat err=%v", err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := callPatch(t, tool, "deny-"+tc.name, tc.patch)
			if !result.IsError || !strings.Contains(toolError(t, result), "synthetic deny") {
				t.Fatalf("expected synthetic deny patch error, got error=%t output=%s", result.IsError, string(result.Output))
			}
			tc.assert(t)
		})
	}
	if approvals != 0 {
		t.Fatalf("outside approvals = %d, want 0", approvals)
	}
}

func TestPathDenyPolicyPreflightsWholePatchBeforeOutsideApproval(t *testing.T) {
	workspace := t.TempDir()
	normalRoot := outsideNonTempDir(t)
	deniedRoot := outsideNonTempDir(t)
	normalTarget := filepath.Join(normalRoot, "normal.txt")
	deniedTarget := filepath.Join(deniedRoot, "denied.txt")
	if err := os.WriteFile(normalTarget, []byte("normal\n"), 0o644); err != nil {
		t.Fatalf("seed normal target: %v", err)
	}
	approvals := 0
	tool := newPatchTestTool(t, workspace,
		WithPathDenyPolicy(compilePatchDenyPolicyForTest(t, deniedRoot, "synthetic deny")),
		WithOutsideWorkspaceApprover(func(context.Context, OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error) {
			approvals++
			return OutsideWorkspaceApproval{Decision: OutsideWorkspaceDecisionAllowSession}, nil
		}),
	)

	result := callPatch(t, tool, "mixed-deny", "*** Begin Patch\n*** Update File: "+normalTarget+"\n-normal\n+changed\n*** Add File: "+deniedTarget+"\n+denied\n*** End Patch\n")
	if !result.IsError || !strings.Contains(toolError(t, result), "synthetic deny") {
		t.Fatalf("expected synthetic deny patch error, got error=%t output=%s", result.IsError, string(result.Output))
	}
	if approvals != 0 {
		t.Fatalf("outside approvals = %d, want 0", approvals)
	}
	assertPatchFileContent(t, normalTarget, "normal\n")
	if _, err := os.Stat(deniedTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied target created, stat err=%v", err)
	}
	tool.outsideWorkspaceApprover = nil
	allowedBySession := callPatch(t, tool, "session-check", "*** Begin Patch\n*** Update File: "+normalTarget+"\n-normal\n+changed\n*** End Patch\n")
	if !allowedBySession.IsError {
		t.Fatal("outside-workspace session approval was mutated before deny preflight")
	}
}

func TestOutsideWorkspaceQueuesApprovalPerFileInSinglePatch(t *testing.T) {
	workspace := t.TempDir()
	outsideRoot := outsideNonTempDir(t)
	first := filepath.Join(outsideRoot, "first", "one.txt")
	second := filepath.Join(outsideRoot, "second", "two.txt")

	requests := make([]OutsideWorkspaceRequest, 0, 2)
	tool, err := New(workspace, true, WithOutsideWorkspaceApprover(func(_ context.Context, req OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error) {
		requests = append(requests, req)
		return OutsideWorkspaceApproval{Decision: OutsideWorkspaceDecisionAllowOnce}, nil
	}))
	if err != nil {
		t.Fatalf("new patch tool: %v", err)
	}

	result := callPatch(t, tool, "outside-multi-add", "*** Begin Patch\n*** Add File: "+first+"\n+one\n*** Add File: "+second+"\n+two\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("expected success, got %s", toolError(t, result))
	}
	if len(requests) != 2 {
		t.Fatalf("expected two approval requests, got %d", len(requests))
	}
	if requests[0].ResolvedPath != first {
		t.Fatalf("unexpected first approval path: %+v", requests[0])
	}
	if requests[1].ResolvedPath != second {
		t.Fatalf("unexpected second approval path: %+v", requests[1])
	}
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: first, want: "one\n"},
		{path: second, want: "two\n"},
	} {
		got, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("read outside file %s: %v", tc.path, err)
		}
		if string(got) != tc.want {
			t.Fatalf("unexpected content for %s: %q", tc.path, string(got))
		}
	}
	if err := os.RemoveAll(filepath.Dir(first)); err != nil {
		t.Fatalf("cleanup first outside tree: %v", err)
	}
	if err := os.RemoveAll(filepath.Dir(second)); err != nil {
		t.Fatalf("cleanup second outside tree: %v", err)
	}
}

func outsideNonTempDir(t *testing.T) string {
	t.Helper()
	bases := make([]string, 0, 2)
	if wd, err := os.Getwd(); err == nil {
		bases = append(bases, wd)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		bases = append(bases, home)
	}
	for _, base := range bases {
		dir, err := os.MkdirTemp(base, "kent-patch-outside-*")
		if err != nil {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			_ = os.RemoveAll(dir)
			continue
		}
		if IsPathInTemporaryDir(abs) {
			_ = os.RemoveAll(dir)
			continue
		}
		t.Cleanup(func() {
			_ = os.RemoveAll(dir)
		})
		return abs
	}
	t.Skip("unable to create non-temporary outside directory for test")
	return ""
}

func TestTemporaryEditableRootsIncludeBasicTmpAliases(t *testing.T) {
	assertAlias := func(primary, alias string) {
		t.Helper()
		primaryInfo, err := os.Stat(primary)
		if err != nil {
			return
		}
		aliasInfo, err := os.Stat(alias)
		if err != nil {
			return
		}
		if !os.SameFile(primaryInfo, aliasInfo) {
			return
		}
		roots := tempEditableRoots()
		if !containsString(roots, filepath.Clean(primary)) {
			t.Fatalf("expected temp roots to include %q, got %v", primary, roots)
		}
		if !containsString(roots, filepath.Clean(alias)) {
			t.Fatalf("expected temp roots to include %q, got %v", alias, roots)
		}
	}

	assertAlias("/tmp", "/private/tmp")
	assertAlias("/var/tmp", "/private/var/tmp")
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func findCaseVariantExistingAlias(path string) (string, bool) {
	canonical := filepath.Clean(path)
	canonicalInfo, err := os.Stat(canonical)
	if err != nil {
		return "", false
	}
	if candidate, ok := caseAliasUsersSubstitution(canonical, canonicalInfo); ok {
		return candidate, true
	}

	parts := strings.Split(canonical, string(filepath.Separator))
	start := 0
	if filepath.IsAbs(canonical) && len(parts) > 0 && parts[0] == "" {
		start = 1
	}

	for idx := start; idx < len(parts); idx++ {
		variantPart := toggleFirstLetterCase(parts[idx])
		if variantPart == parts[idx] {
			continue
		}
		candidateParts := append([]string(nil), parts...)
		candidateParts[idx] = variantPart
		candidate := strings.Join(candidateParts, string(filepath.Separator))
		if candidate == canonical {
			continue
		}
		candidateInfo, statErr := os.Stat(candidate)
		if statErr != nil {
			continue
		}
		if os.SameFile(candidateInfo, canonicalInfo) {
			return candidate, true
		}
	}

	return "", false
}

func caseAliasUsersSubstitution(canonical string, canonicalInfo os.FileInfo) (string, bool) {
	if strings.HasPrefix(canonical, "/Users/") {
		candidate := "/users/" + strings.TrimPrefix(canonical, "/Users/")
		if info, err := os.Stat(candidate); err == nil && os.SameFile(info, canonicalInfo) {
			return candidate, true
		}
	}
	if strings.HasPrefix(canonical, "/users/") {
		candidate := "/Users/" + strings.TrimPrefix(canonical, "/users/")
		if info, err := os.Stat(candidate); err == nil && os.SameFile(info, canonicalInfo) {
			return candidate, true
		}
	}
	return "", false
}

func toggleFirstLetterCase(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return value
	}
	first := runes[0]
	upper := unicode.ToUpper(first)
	lower := unicode.ToLower(first)
	if first == upper && first == lower {
		return value
	}
	if first == upper {
		runes[0] = lower
		return string(runes)
	}
	runes[0] = upper
	return string(runes)
}

func callPatch(t *testing.T, tool *Tool, id, patchText string) tools.Result {
	t.Helper()
	input, _ := json.Marshal(map[string]any{"patch": patchText})
	result, err := tool.Call(context.Background(), tools.Call{ID: id, Name: toolspec.ToolPatch, Input: input})
	if err != nil {
		t.Fatalf("patch call error: %v", err)
	}
	return result
}

func newPatchTestTool(t *testing.T, workspace string, opts ...Option) *Tool {
	t.Helper()
	tool, err := New(workspace, true, opts...)
	if err != nil {
		t.Fatalf("new patch tool: %v", err)
	}
	return tool
}

func compilePatchDenyPolicyForTest(t *testing.T, root string, message string) tools.PathDenyPolicy {
	t.Helper()
	policy, err := tools.CompilePathDenyPolicy([]tools.PathDenyRuleConfig{{
		Message: message,
		Matcher: tools.PathMatcherConfig{Kind: tools.PathMatcherLiteral, Pattern: root, LiteralTree: true},
	}})
	if err != nil {
		t.Fatalf("compile path deny policy: %v", err)
	}
	return policy
}

func assertPatchFileContent(t *testing.T, path string, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s content = %q, want %q", path, string(got), want)
	}
}

func toolError(t *testing.T, result tools.Result) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("decode tool error output: %v", err)
	}
	return payload.Error
}

type toolFailureErrorPayload struct {
	Error      string `json:"error"`
	Kind       string `json:"kind,omitempty"`
	Path       string `json:"path,omitempty"`
	Line       int    `json:"line,omitempty"`
	NearLine   bool   `json:"near_line,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Commentary string `json:"commentary,omitempty"`
}

func toolFailurePayload(t *testing.T, result tools.Result) toolFailureErrorPayload {
	t.Helper()
	var payload toolFailureErrorPayload
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("decode tool failure output: %v", err)
	}
	return payload
}

func TestAttachFailurePathNilErrorIsNoOp(t *testing.T) {
	if got := attachFailurePath(nil, "target.txt"); got != nil {
		t.Fatalf("attachFailurePath(nil) = %v, want nil", got)
	}
}

func TestAttachFailureReasonContextNilErrorIsNoOp(t *testing.T) {
	if got := attachFailureReasonContext(nil, "hunk 1"); got != nil {
		t.Fatalf("attachFailureReasonContext(nil) = %v, want nil", got)
	}
}
