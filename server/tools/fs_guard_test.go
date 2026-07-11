package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		WorkspaceRoot:     workspace,
		WorkspaceRootReal: real,
		WorkspaceRootInfo: info,
		WorkspaceOnly:     true,
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

	secondPolicy, err := CompilePathDenyPolicy([]PathDenyRuleConfig{
		{Message: "miss", Matcher: PathMatcherConfig{Kind: PathMatcherLiteral, Pattern: filepath.Join(t.TempDir(), "other"), LiteralTree: true}},
		{Message: "second message", Matcher: PathMatcherConfig{Kind: PathMatcherLiteral, Pattern: filepath.Join(root, "skills"), LiteralTree: true}},
	})
	if err != nil {
		t.Fatalf("compile second policy: %v", err)
	}
	match, ok, matchErr := secondPolicy.Match(filepath.Join(root, "skills", "a"))
	if matchErr != nil {
		t.Fatalf("match second rule: %v", matchErr)
	}
	if !ok || match.Message != "second message" {
		t.Fatalf("second rule match = %+v/%t, want second message", match, ok)
	}
	if _, ok, matchErr := secondPolicy.Match(filepath.Join(root, "other")); matchErr != nil || ok {
		if matchErr != nil {
			t.Fatalf("match no-match path: %v", matchErr)
		}
		t.Fatalf("expected no match fallthrough")
	}
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

	if _, err := CompilePathDenyPolicy([]PathDenyRuleConfig{{Message: "bad", Matcher: PathMatcherConfig{Kind: PathMatcherGlob, Pattern: filepath.Join(root, "[")}}}); err == nil {
		t.Fatal("expected invalid glob compile error")
	}
	if _, err := CompilePathDenyPolicy([]PathDenyRuleConfig{{Message: "bad", Matcher: PathMatcherConfig{Kind: PathMatcherKind("unsupported"), Pattern: root}}}); err == nil {
		t.Fatal("expected unsupported matcher compile error")
	}
	if _, err := CompilePathDenyPolicy([]PathDenyRuleConfig{{Label: pathDenyLabelForTest("  "), Message: "bad", Matcher: PathMatcherConfig{Kind: PathMatcherLiteral, Pattern: root}}}); err == nil {
		t.Fatal("expected blank diagnostic label compile error")
	}
}

func pathDenyLabelForTest(label string) *string {
	return &label
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
		WorkspaceRoot:         workspace,
		WorkspaceRootReal:     real,
		WorkspaceRootInfo:     info,
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
		WorkspaceRoot:         workspace,
		WorkspaceRootReal:     real,
		WorkspaceRootInfo:     info,
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
		WorkspaceRoot:     workspace,
		WorkspaceRootReal: real,
		WorkspaceRootInfo: info,
		WorkspaceOnly:     true,
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
