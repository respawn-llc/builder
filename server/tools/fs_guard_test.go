package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func singleFileAccessScopeForTest(root string, real string, info os.FileInfo) FileAccessScope {
	filesystemRoot := FilesystemRoot{LexicalPath: root, RealPath: real, Info: info}
	return FileAccessScope{WorkingDirectory: filesystemRoot, ExecutionTargetRoot: filesystemRoot}
}

func TestProjectWorkspaceBoundaryWithWorkspaceKeepsNewestBoundedMembership(t *testing.T) {
	boundary := ProjectWorkspaceBoundary{ProjectID: "project", Roots: []ProjectWorkspaceRoot{
		{FilesystemRoot: FilesystemRoot{LexicalPath: "newer"}},
		{FilesystemRoot: FilesystemRoot{LexicalPath: "older"}},
	}}
	next, added := boundary.WithWorkspace(ProjectWorkspaceRoot{FilesystemRoot: FilesystemRoot{LexicalPath: "newest"}}, 2)
	if !added || len(next.Roots) != 2 || next.Roots[0].LexicalPath != "newest" || next.Roots[1].LexicalPath != "newer" {
		t.Fatalf("bounded membership = %+v, added=%t", next, added)
	}
	if duplicate, added := next.WithWorkspace(next.Roots[1], 2); added || len(duplicate.Roots) != 2 {
		t.Fatalf("duplicate membership changed boundary = %+v, added=%t", duplicate, added)
	}
}

func TestDefaultUserDeniedIncludesRejectionInstruction(t *testing.T) {
	workspace := t.TempDir()
	real, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		t.Fatalf("stat workspace: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	guard := NewFSGuard(FSGuardConfig{
		Scope:         singleFileAccessScopeForTest(workspace, real, info),
		WorkspaceOnly: true,
		Approver: func(context.Context, FSGuardRequest) (FSGuardApproval, error) {
			return FSGuardApproval{Decision: FSGuardDecisionDeny, Commentary: "no"}, nil
		},
		RejectionInstruction: "ask user for a safe path",
		ErrorLabels:          FSGuardErrorLabels{OutsidePath: "outside"},
	})

	_, err = guard.Allow(context.Background(), outside, outside, nil)
	if err == nil {
		t.Fatal("expected denial error")
	}
	if got := err.Error(); !strings.Contains(got, "no") || !strings.Contains(got, "ask user for a safe path") {
		t.Fatalf("denial error = %q, want commentary and instruction", got)
	}
}

func TestPathDenyPolicyLiteralTreeAndMultipleRuleMessages(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".generated")
	policy, err := CompilePathDenyPolicy([]PathDenyRuleConfig{
		{Label: pathDenyLabelForTest("first"), Message: "first message", Matcher: PathMatcherConfig{Kind: PathMatcherLiteral, Pattern: root, LiteralTree: true}},
		{Label: pathDenyLabelForTest("second"), Message: "second message", Matcher: PathMatcherConfig{Kind: PathMatcherLiteral, Pattern: filepath.Join(root, "skills"), LiteralTree: true}},
	})
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	for _, candidate := range []string{root, filepath.Join(root, "skills", "a", "SKILL.md")} {
		match, ok, matchErr := policy.Match(candidate)
		if matchErr != nil {
			t.Fatalf("match %q: %v", candidate, matchErr)
		}
		if !ok || match.Message != "first message" {
			t.Fatalf("match %q = %+v/%t, want first message", candidate, match, ok)
		}
	}
	if _, ok, matchErr := policy.Match(root + "-backup"); matchErr != nil || ok {
		if matchErr != nil {
			t.Fatalf("match textual sibling: %v", matchErr)
		}
		t.Fatalf("literal tree matched textual sibling")
	}
}

func pathDenyLabelForTest(label string) *string {
	return &label
}

func TestPathDenyPolicyGlobAndCompileErrors(t *testing.T) {
	root := t.TempDir()
	globPolicy, err := CompilePathDenyPolicy([]PathDenyRuleConfig{{
		Message: "glob message",
		Matcher: PathMatcherConfig{Kind: PathMatcherGlob, Pattern: filepath.Join(root, ".generated", "**")},
	}})
	if err != nil {
		t.Fatalf("compile glob policy: %v", err)
	}
	if match, ok, matchErr := globPolicy.Match(filepath.Join(root, ".generated", "skills", "one")); matchErr != nil || !ok || match.Message != "glob message" {
		t.Fatalf("glob match = %+v/%t err=%v", match, ok, matchErr)
	}
	if match, ok, matchErr := globPolicy.Match(filepath.Join(root, ".generated")); matchErr != nil || !ok || match.Message != "glob message" {
		t.Fatalf("glob root match = %+v/%t err=%v", match, ok, matchErr)
	}
	for _, rule := range []PathDenyRuleConfig{
		{Message: "bad", Matcher: PathMatcherConfig{Kind: PathMatcherGlob, Pattern: filepath.Join(root, "[")}},
		{Message: "bad", Matcher: PathMatcherConfig{Kind: PathMatcherKind("unsupported"), Pattern: root}},
		{Label: pathDenyLabelForTest("  "), Message: "bad", Matcher: PathMatcherConfig{Kind: PathMatcherLiteral, Pattern: root}},
	} {
		if _, err := CompilePathDenyPolicy([]PathDenyRuleConfig{rule}); err == nil {
			t.Fatal("expected invalid path deny rule error")
		}
	}
}

func TestFSGuardPathDenyWinsBeforeAllowAndApprovalPaths(t *testing.T) {
	workspace := t.TempDir()
	real, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		t.Fatalf("stat workspace: %v", err)
	}
	target := filepath.Join(workspace, ".generated", "file.txt")
	policy, err := CompilePathDenyPolicy([]PathDenyRuleConfig{{
		Message: "deny generated",
		Matcher: PathMatcherConfig{Kind: PathMatcherLiteral, Pattern: filepath.Join(workspace, ".generated"), LiteralTree: true},
	}})
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	approverCalls := 0
	approvedLogs := 0
	sessionAllowed := true
	approvedOutside := map[string]bool{target: true}
	guard := NewFSGuard(FSGuardConfig{
		Scope:                 singleFileAccessScopeForTest(workspace, real, info),
		WorkspaceOnly:         false,
		AllowOutsideWorkspace: true,
		Approver: func(context.Context, FSGuardRequest) (FSGuardApproval, error) {
			approverCalls++
			return FSGuardApproval{Decision: FSGuardDecisionAllowSession}, nil
		},
		SessionAllowed:       func() bool { return sessionAllowed },
		SetSessionAllowed:    func(bool) { sessionAllowed = false },
		TemporaryPathAllowed: func(string) bool { return true },
		OnApproved:           func(FSGuardRequest, string) { approvedLogs++ },
		PathDenyPolicy:       policy,
	})
	_, err = guard.Allow(context.Background(), target, target, approvedOutside)
	if err == nil || !strings.Contains(err.Error(), "deny generated") {
		t.Fatalf("guard denial error = %v, want policy message", err)
	}
	if approverCalls != 0 || approvedLogs != 0 || !sessionAllowed || !approvedOutside[target] {
		t.Fatalf("deny mutated approval state: approver=%d logs=%d session=%t approved=%v", approverCalls, approvedLogs, sessionAllowed, approvedOutside)
	}
}

func TestFSGuardPathDenyChecksLexicalRequestedPathBeforeSymlinkResolution(t *testing.T) {
	workspace := t.TempDir()
	real, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		t.Fatalf("stat workspace: %v", err)
	}
	deniedRoot := filepath.Join(workspace, ".generated")
	if err := os.MkdirAll(deniedRoot, 0o755); err != nil {
		t.Fatalf("create denied root: %v", err)
	}
	outsideRoot := t.TempDir()
	if err := os.Symlink(outsideRoot, filepath.Join(deniedRoot, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	policy, err := CompilePathDenyPolicy([]PathDenyRuleConfig{{
		Message: "deny generated",
		Matcher: PathMatcherConfig{Kind: PathMatcherLiteral, Pattern: deniedRoot, LiteralTree: true},
	}})
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	requested := filepath.Join(".generated", "link", "file.txt")
	resolved := filepath.Join(outsideRoot, "file.txt")
	approverCalls := 0
	guard := NewFSGuard(FSGuardConfig{
		Scope:                 singleFileAccessScopeForTest(workspace, real, info),
		WorkspaceOnly:         false,
		AllowOutsideWorkspace: true,
		Approver: func(context.Context, FSGuardRequest) (FSGuardApproval, error) {
			approverCalls++
			return FSGuardApproval{Decision: FSGuardDecisionAllowOnce}, nil
		},
		PathDenyPolicy: policy,
	})

	_, err = guard.Allow(context.Background(), requested, resolved, nil)
	if err == nil || !strings.Contains(err.Error(), "deny generated") {
		t.Fatalf("guard denial error = %v, want lexical generated denial", err)
	}
	if approverCalls != 0 {
		t.Fatalf("approver calls = %d, want 0", approverCalls)
	}
}

func TestFSGuardPathDenyIdentityErrorIsSurfacedBeforeApproval(t *testing.T) {
	workspace := t.TempDir()
	real, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		t.Fatalf("stat workspace: %v", err)
	}
	loop := filepath.Join(workspace, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Skipf("symlink loop unavailable: %v", err)
	}
	policy, err := CompilePathDenyPolicy([]PathDenyRuleConfig{{
		Message: "deny generated",
		Matcher: PathMatcherConfig{Kind: PathMatcherLiteral, Pattern: workspace, LiteralTree: true},
	}})
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	target := loop
	if _, _, matchErr := policy.Match(target); matchErr == nil {
		t.Fatal("expected policy match to surface path identity error")
	}
	approverCalls := 0
	guard := NewFSGuard(FSGuardConfig{
		Scope:         singleFileAccessScopeForTest(workspace, real, info),
		WorkspaceOnly: true,
		Approver: func(context.Context, FSGuardRequest) (FSGuardApproval, error) {
			approverCalls++
			return FSGuardApproval{Decision: FSGuardDecisionAllowOnce}, nil
		},
		PathDenyPolicy: policy,
	})
	_, err = guard.Allow(context.Background(), target, target, nil)
	if err == nil || !strings.Contains(err.Error(), "path deny policy check") {
		t.Fatalf("guard error = %v, want surfaced path deny identity error", err)
	}
	if approverCalls != 0 {
		t.Fatalf("approver calls = %d, want 0", approverCalls)
	}
}
