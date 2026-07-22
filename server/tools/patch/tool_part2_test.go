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

func (f outsidePatchFixture) denyPolicyTool(root string, decision OutsideWorkspaceDecision, approvals *int, opts ...Option) *Tool {
	f.Helper()
	opts = append(
		opts,
		WithPathDenyPolicy(compileLiteralTreeDenyPolicy(f.T, root, "synthetic deny")),
		WithOutsideWorkspaceApprover(func(context.Context, OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error) {
			(*approvals)++
			return OutsideWorkspaceApproval{Decision: decision}, nil
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
	approver func(context.Context, OutsideWorkspaceRequest) (OutsideWorkspaceApproval, error),
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
	tool := fixture.denyPolicyTool(deniedRoot, OutsideWorkspaceDecisionAllowOnce, &approvals, WithAllowOutsideWorkspace(true))

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
			if !result.IsError {
				t.Fatalf("expected denied patch error, got error=%t", result.IsError)
			}
			tc.assert(t)
		})
	}
	if approvals != 0 {
		t.Fatalf("outside approvals = %d, want 0", approvals)
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
			if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
				t.Fatalf("cleanup outside directory after Abs failure: %v", cleanupErr)
			}
			continue
		}
		if IsPathInTemporaryDir(abs) {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatalf("cleanup temporary outside-directory candidate: %v", err)
			}
			continue
		}
		t.Cleanup(func() {
			if err := os.RemoveAll(dir); err != nil {
				t.Errorf("cleanup outside directory %q: %v", dir, err)
			}
		})
		return abs
	}
	t.Skip("unable to create non-temporary outside directory for test")
	panic("unreachable after testing.T.Skip")
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
