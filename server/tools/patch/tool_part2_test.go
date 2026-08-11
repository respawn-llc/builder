package patch

import (
	"context"
	"core/internal/testharness/runtimewirefixture"
	"core/internal/testharness/testsetup"
	"core/server/tools"
	"core/shared/toolspec"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type outsidePatchFixture struct {
	*testing.T
	outsideRoot string
}

func newOutsidePatchFixture(t *testing.T) outsidePatchFixture {
	t.Helper()
	return outsidePatchFixture{T: t, outsideRoot: outsideNonTempDir(t)}
}

func (f outsidePatchFixture) tool(opts ...Option) *Tool {
	f.Helper()
	return newPatchTestTool(f.T, f.TempDir(), opts...)
}

func (f outsidePatchFixture) denyPolicyTool(root string, approvals *int, opts ...Option) *Tool {
	f.Helper()
	opts = append(
		opts,
		WithPathDenyPolicy(compileLiteralTreeDenyPolicy(f.T, root, "synthetic deny")),
		WithOutsideWorkspaceApprover(func(context.Context, tools.FileAccessRequest) (tools.FileAccessApproval, error) {
			(*approvals)++
			return tools.FileAccessApproval{Kind: tools.FileAccessApprovalAllowOnce}, nil
		}),
	)
	return f.tool(opts...)
}

func (f outsidePatchFixture) write(path, content string) {
	f.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.Fatalf("write %s: %v", path, err)
	}
}

func outsideUpdateApprovalError(
	t *testing.T,
	id string,
	approver tools.FileAccessApprover,
) (string, string) {
	t.Helper()
	fixture := newOutsidePatchFixture(t)
	target := filepath.Join(fixture.outsideRoot, "outside.txt")
	fixture.write(target, "start\n")
	tool := fixture.tool(WithOutsideWorkspaceApprover(approver))
	result := callPatch(t, tool, id, "*** Begin Patch\n*** Update File: "+target+"\n-start\n+done\n*** End Patch\n")
	if !result.IsError {
		t.Fatal("expected error result")
	}
	return toolError(t, result), target
}

func TestOutsideWorkspaceRejectionIncludesUserCommentary(t *testing.T) {
	commentary := "not allowed by policy"
	errMessage, target := outsideUpdateApprovalError(t, "deny-commentary", func(context.Context, tools.FileAccessRequest) (tools.FileAccessApproval, error) {
		return tools.FileAccessApproval{Kind: tools.FileAccessApprovalDeny, Commentary: &commentary}, nil
	})
	want := "Patch failed: user denied the edit for " + target + ".\nUser said: not allowed by policy"
	if errMessage != want {
		t.Fatalf("unexpected rejection error, got %q want %q", errMessage, want)
	}
}

func TestOutsideWorkspaceApprovalFailureUsesPatchSpecificWording(t *testing.T) {
	errMessage, _ := outsideUpdateApprovalError(t, "deny-approval-error", func(context.Context, tools.FileAccessRequest) (tools.FileAccessApproval, error) {
		return tools.FileAccessApproval{}, errors.New("ask failed")
	})
	if !strings.Contains(errMessage, "Patch failed: file edit approval failed") {
		t.Fatalf("expected patch approval failure wording, got %q", errMessage)
	}
	if strings.Contains(errMessage, "read approval failed") || strings.Contains(errMessage, "view_image path outside workspace") {
		t.Fatalf("unexpected non-patch wording, got %q", errMessage)
	}
}

func TestPathDenyPolicyBlocksPatchOperationsBeforeMutation(t *testing.T) {
	fixture := newOutsidePatchFixture(t)
	deniedRoot := fixture.outsideRoot
	normalRoot := outsideNonTempDir(t)
	if err := os.MkdirAll(filepath.Join(deniedRoot, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir denied root: %v", err)
	}
	fixture.write(filepath.Join(deniedRoot, "update.txt"), "old\n")
	fixture.write(filepath.Join(deniedRoot, "delete.txt"), "delete\n")
	fixture.write(filepath.Join(deniedRoot, "move-src.txt"), "move\n")
	fixture.write(filepath.Join(normalRoot, "normal-src.txt"), "normal\n")
	approvals := 0
	tool := fixture.denyPolicyTool(deniedRoot, &approvals, WithAllowOutsideWorkspace(true))

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
	fixture := newOutsidePatchFixture(t)
	normalRoot := fixture.outsideRoot
	deniedRoot := outsideNonTempDir(t)
	normalTarget := filepath.Join(normalRoot, "normal.txt")
	deniedTarget := filepath.Join(deniedRoot, "denied.txt")
	fixture.write(normalTarget, "normal\n")
	approvals := 0
	tool := fixture.denyPolicyTool(deniedRoot, &approvals)

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
}

func TestPathDenyPolicyPreflightsLexicalSymlinkTargetBeforeOutsideApproval(t *testing.T) {
	fixture := newOutsidePatchFixture(t)
	deniedRoot := fixture.outsideRoot
	normalRoot := outsideNonTempDir(t)
	link := filepath.Join(deniedRoot, "link")
	if err := os.Symlink(normalRoot, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	approvals := 0
	tool := fixture.denyPolicyTool(deniedRoot, &approvals)

	target := filepath.Join(link, "via-link.txt")
	result := callPatch(t, tool, "lexical-symlink-deny", "*** Begin Patch\n*** Add File: "+target+"\n+denied\n*** End Patch\n")
	if !result.IsError || !strings.Contains(toolError(t, result), "synthetic deny") {
		t.Fatalf("expected synthetic deny patch error, got error=%t output=%s", result.IsError, string(result.Output))
	}
	if approvals != 0 {
		t.Fatalf("outside approvals = %d, want 0", approvals)
	}
	if _, err := os.Stat(filepath.Join(normalRoot, "via-link.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink target created, stat err=%v", err)
	}
}

func TestOutsideWorkspaceMultiAddRequestsApprovalForEveryTarget(t *testing.T) {
	fixture := newOutsidePatchFixture(t)
	first := filepath.Join(fixture.outsideRoot, "first", "one.txt")
	second := filepath.Join(fixture.outsideRoot, "second", "two.txt")
	requests := make([]tools.FileAccessRequest, 0, 2)
	tool := fixture.tool(WithOutsideWorkspaceApprover(func(_ context.Context, request tools.FileAccessRequest) (tools.FileAccessApproval, error) {
		requests = append(requests, request)
		return tools.FileAccessApproval{Kind: tools.FileAccessApprovalAllowOnce}, nil
	}))

	result := callPatch(t, tool, "outside-multi-add", "*** Begin Patch\n*** Add File: "+first+"\n+one\n*** Add File: "+second+"\n+two\n*** End Patch\n")
	if result.IsError {
		t.Fatalf("multi-add result = error: %s", toolError(t, result))
	}
	if len(requests) != 2 || requests[0].ResolvedPath != first || requests[1].ResolvedPath != second {
		t.Fatalf("approval requests = %+v, want ordered targets %q then %q", requests, first, second)
	}
	assertPatchFileContent(t, first, "one\n")
	assertPatchFileContent(t, second, "two\n")
}

func outsideNonTempDir(t *testing.T) string {
	t.Helper()
	return testsetup.NonTemporaryDirectory(t, "kent-patch-outside-", tools.IsPathInTemporaryDir)
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
	return newPatchTestToolWithContext(t, runtimewirefixture.FilesystemContext(t, workspace), opts...)
}

func newPatchTestToolWithContext(t *testing.T, filesystemContext tools.FilesystemContext, opts ...Option) *Tool {
	t.Helper()
	tool, err := New(filesystemContext, opts...)
	if err != nil {
		t.Fatalf("new patch tool: %v", err)
	}
	return tool
}

func compileLiteralTreeDenyPolicy(t *testing.T, root string, message string) tools.PathDenyPolicy {
	t.Helper()
	policy, err := tools.CompileLiteralTreePathDenyPolicy(root, message)
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
	Error      string  `json:"error"`
	Kind       string  `json:"kind,omitempty"`
	Path       string  `json:"path,omitempty"`
	Line       int     `json:"line,omitempty"`
	NearLine   bool    `json:"near_line,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	Commentary *string `json:"commentary,omitempty"`
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
