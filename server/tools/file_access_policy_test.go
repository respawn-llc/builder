package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"core/internal/testharness/testsetup"
)

func filesystemRootForTest(t *testing.T, path string) FilesystemRoot {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve filesystem root %q: %v", path, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		t.Fatalf("stat filesystem root %q: %v", path, err)
	}
	return FilesystemRoot{LexicalPath: path, RealPath: real, Info: info}
}

func filesystemContextForTest(t *testing.T, workingDirectory string, projectRoots ...string) FilesystemContext {
	t.Helper()
	root := filesystemRootForTest(t, workingDirectory)
	context := FilesystemContext{Access: FileAccessScope{
		WorkingDirectory:    root,
		ExecutionTargetRoot: root,
		ProjectWorkspace:    ProjectWorkspaceScope{ProjectID: "test"},
	}}
	for _, projectRoot := range projectRoots {
		context.Access.ProjectWorkspace.Roots = append(
			context.Access.ProjectWorkspace.Roots,
			ProjectWorkspaceRoot{FilesystemRoot: filesystemRootForTest(t, projectRoot)},
		)
	}
	return context
}

func newFileAccessPolicyForTest(t *testing.T, config FileAccessPolicyConfig) *FileAccessPolicy {
	t.Helper()
	policy, err := NewFileAccessPolicy(config)
	if err != nil {
		t.Fatalf("new file access policy: %v", err)
	}
	return policy
}

func nonTemporaryDirectoryForTest(t *testing.T) string {
	t.Helper()
	return testsetup.NonTemporaryDirectory(t, "kent-file-access-", IsPathInTemporaryDir)
}

func TestFileAccessPolicyTrustsExecutionTargetAndProjectWorkspaceRoots(t *testing.T) {
	executionTarget := t.TempDir()
	projectRoot := nonTemporaryDirectoryForTest(t)
	outside := nonTemporaryDirectoryForTest(t)
	policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
		Context: filesystemContextForTest(t, executionTarget, projectRoot),
		Mode:    FileAccessRead,
	})
	call := policy.BeginCall()

	targets := []struct {
		requested string
		resolved  string
	}{
		{
			requested: filepath.Join(executionTarget, "execution.txt"),
			resolved:  filepath.Join(filesystemRootForTest(t, executionTarget).RealPath, "execution.txt"),
		},
		{
			requested: filepath.Join(projectRoot, "project.txt"),
			resolved:  filepath.Join(filesystemRootForTest(t, projectRoot).RealPath, "project.txt"),
		},
	}
	preparationTargets := make([]FileAccessTarget, 0, len(targets))
	for _, target := range targets {
		preparationTargets = append(preparationTargets, FileAccessTarget{
			RequestedPath: target.requested,
			ResolvedPath:  target.resolved,
		})
	}
	if outcome := call.Prepare(context.Background(), preparationTargets); !outcome.IsAllowed() {
		t.Fatalf("prepare trusted targets = %+v", outcome)
	}
	for _, target := range targets {
		outcome := call.Authorize(context.Background(), target.requested, target.resolved)
		if outcome.Kind != FileAccessAllowed || outcome.Reason != FileAccessReasonTrustedRoot {
			t.Fatalf("authorize trusted target %q = %+v", target.requested, outcome)
		}
	}

	target := filepath.Join(outside, "outside.txt")
	outcome := policy.BeginCall().Authorize(context.Background(), target, target)
	if outcome.Kind != FileAccessDeniedOutsideWorkspace {
		t.Fatalf("authorize outside target = %+v", outcome)
	}
}

func TestFileAccessPolicyConfiguredAndTemporaryAllowancesBypassApproval(t *testing.T) {
	workspace := nonTemporaryDirectoryForTest(t)
	ordinaryOutside := filepath.Join(nonTemporaryDirectoryForTest(t), "ordinary.txt")
	temporaryOutside := filepath.Join(t.TempDir(), "temporary.txt")

	for _, test := range []struct {
		name       string
		target     string
		configured bool
		reason     FileAccessReason
	}{
		{name: "configured", target: ordinaryOutside, configured: true, reason: FileAccessReasonConfiguredAllow},
		{name: "temporary", target: temporaryOutside, reason: FileAccessReasonTemporaryAllow},
	} {
		t.Run(test.name, func(t *testing.T) {
			approvalCalls := 0
			policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
				Context:               filesystemContextForTest(t, workspace),
				Mode:                  FileAccessMutation,
				AllowOutsideWorkspace: test.configured,
				Approver: func(context.Context, FileAccessApprovalRequest) (FileAccessApproval, error) {
					approvalCalls++
					return FileAccessApproval{Kind: FileAccessApprovalDeny}, nil
				},
			})
			outcome := policy.BeginCall().Authorize(context.Background(), test.target, test.target)
			if outcome.Kind != FileAccessAllowed || outcome.Reason != test.reason {
				t.Fatalf("authorize = %+v", outcome)
			}
			if approvalCalls != 0 {
				t.Fatalf("approval calls = %d, want 0", approvalCalls)
			}
		})
	}
}

func TestFileAccessPolicyAllowOnceIsExactAndCallScoped(t *testing.T) {
	workspace := nonTemporaryDirectoryForTest(t)
	outsideRoot := nonTemporaryDirectoryForTest(t)
	first := filepath.Join(outsideRoot, "first.txt")
	second := filepath.Join(outsideRoot, "second.txt")
	approvalCalls := 0
	policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
		Context: filesystemContextForTest(t, workspace),
		Mode:    FileAccessMutation,
		Approver: func(context.Context, FileAccessApprovalRequest) (FileAccessApproval, error) {
			approvalCalls++
			return FileAccessApproval{Kind: FileAccessApprovalAllowOnce}, nil
		},
	})

	call := policy.BeginCall()
	assertFileAccessReason(t, call.Prepare(context.Background(), []FileAccessTarget{
		{RequestedPath: first, ResolvedPath: first},
		{RequestedPath: second, ResolvedPath: second},
	}), FileAccessReasonAllowOnce)
	assertFileAccessReason(t, call.Authorize(context.Background(), first, first), FileAccessReasonCallAllow)
	assertFileAccessReason(t, call.Authorize(context.Background(), second, second), FileAccessReasonCallAllow)
	assertFileAccessReason(t, policy.BeginCall().Authorize(context.Background(), first, first), FileAccessReasonAllowOnce)
	if approvalCalls != 2 {
		t.Fatalf("approval calls = %d, want one per prepared call", approvalCalls)
	}
}

func TestFileAccessCallRejectsChangedAndUndisclosedTargetsWithoutAnotherApproval(t *testing.T) {
	workspace := nonTemporaryDirectoryForTest(t)
	outside := nonTemporaryDirectoryForTest(t)
	first := filepath.Join(outside, "first.txt")
	second := filepath.Join(outside, "second.txt")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte(path), 0o644); err != nil {
			t.Fatalf("write target %q: %v", path, err)
		}
	}
	approvalCalls := 0
	policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
		Context: filesystemContextForTest(t, workspace),
		Mode:    FileAccessRead,
		Approver: func(context.Context, FileAccessApprovalRequest) (FileAccessApproval, error) {
			approvalCalls++
			return FileAccessApproval{Kind: FileAccessApprovalAllowOnce}, nil
		},
	})
	call := policy.BeginCall()
	if outcome := call.Authorize(context.Background(), first, first); !outcome.IsAllowed() {
		t.Fatalf("initial target = %+v", outcome)
	}
	for name, target := range map[string]FileAccessTarget{
		"changed":     {RequestedPath: first, ResolvedPath: second},
		"undisclosed": {RequestedPath: second, ResolvedPath: second},
	} {
		if outcome := call.Authorize(context.Background(), target.RequestedPath, target.ResolvedPath); outcome.IsAllowed() {
			t.Fatalf("%s target was allowed: %+v", name, outcome)
		}
	}
	if approvalCalls != 1 {
		t.Fatalf("approval calls = %d, want no retry", approvalCalls)
	}
}

func TestFileAccessCallPreservesDistinctAliasesWhileSharingCanonicalAuthorization(t *testing.T) {
	workspace := nonTemporaryDirectoryForTest(t)
	outside := nonTemporaryDirectoryForTest(t)
	target := filepath.Join(outside, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	aliasA := filepath.Join(outside, "alias-a.txt")
	aliasB := filepath.Join(outside, "alias-b.txt")
	for _, alias := range []string{aliasA, aliasB} {
		if err := os.Symlink(target, alias); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}
	approvalCalls := 0
	var approvalRequest FileAccessApprovalRequest
	policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
		Context: filesystemContextForTest(t, workspace),
		Mode:    FileAccessRead,
		Approver: func(_ context.Context, request FileAccessApprovalRequest) (FileAccessApproval, error) {
			approvalCalls++
			approvalRequest = request
			return FileAccessApproval{Kind: FileAccessApprovalAllowOnce}, nil
		},
	})
	call := policy.BeginCall()
	requested := []string{aliasA, aliasB, aliasA}
	prepared := call.Prepare(context.Background(), []FileAccessTarget{
		{RequestedPath: aliasA, ResolvedPath: target},
		{RequestedPath: aliasB, ResolvedPath: target},
		{RequestedPath: aliasA, ResolvedPath: target},
	})
	if !prepared.IsAllowed() {
		t.Fatalf("prepare aliases = %+v", prepared)
	}
	wantDisclosures := []FileAccessTarget{
		{RequestedPath: aliasA, ResolvedPath: target},
		{RequestedPath: aliasB, ResolvedPath: target},
	}
	if !reflect.DeepEqual(approvalRequest.Targets, wantDisclosures) {
		t.Fatalf("disclosures = %+v, want %+v", approvalRequest.Targets, wantDisclosures)
	}
	for index, alias := range requested {
		outcome := call.Authorize(context.Background(), alias, target)
		if !outcome.IsAllowed() {
			t.Fatalf("authorize alias %d = %+v", index, outcome)
		}
		if outcome.Request.RequestedPath != alias || outcome.Request.ResolvedPath != target {
			t.Fatalf("alias %d projection = %+v", index, outcome.Request)
		}
	}
	if approvalCalls != 1 {
		t.Fatalf("approval calls = %d, want one canonical authorization", approvalCalls)
	}
}

func TestFileAccessPolicyApprovalKindsProduceClosedReasons(t *testing.T) {
	workspace := nonTemporaryDirectoryForTest(t)
	outside := filepath.Join(nonTemporaryDirectoryForTest(t), "outside.txt")
	for _, test := range []struct {
		name   string
		kind   FileAccessApprovalKind
		reason FileAccessReason
	}{
		{name: "fresh session", kind: FileAccessApprovalAllowSession, reason: FileAccessReasonAllowSession},
		{name: "cached session", kind: FileAccessApprovalSessionCached, reason: FileAccessReasonSessionAllow},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
				Context: filesystemContextForTest(t, workspace),
				Mode:    FileAccessRead,
				Approver: func(context.Context, FileAccessApprovalRequest) (FileAccessApproval, error) {
					return FileAccessApproval{Kind: test.kind}, nil
				},
			})
			assertFileAccessReason(t, policy.BeginCall().Authorize(context.Background(), outside, outside), test.reason)
		})
	}
}

func TestFileAccessPolicySurfacesDenialAndApprovalFailure(t *testing.T) {
	workspace := nonTemporaryDirectoryForTest(t)
	outside := filepath.Join(nonTemporaryDirectoryForTest(t), "outside.txt")
	commentary := "not allowed"
	approvalErr := errors.New("ask failed")

	for _, test := range []struct {
		name     string
		approver FileAccessApprover
		kind     FileAccessOutcomeKind
	}{
		{
			name: "denied",
			approver: func(context.Context, FileAccessApprovalRequest) (FileAccessApproval, error) {
				return FileAccessApproval{Kind: FileAccessApprovalDeny, Commentary: &commentary}, nil
			},
			kind: FileAccessDeniedByUser,
		},
		{
			name: "approval failed",
			approver: func(context.Context, FileAccessApprovalRequest) (FileAccessApproval, error) {
				return FileAccessApproval{}, approvalErr
			},
			kind: FileAccessApprovalFailed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
				Context:  filesystemContextForTest(t, workspace),
				Mode:     FileAccessRead,
				Approver: test.approver,
			})
			outcome := policy.BeginCall().Authorize(context.Background(), outside, outside)
			if outcome.Kind != test.kind {
				t.Fatalf("outcome = %+v", outcome)
			}
			if test.kind == FileAccessDeniedByUser && (outcome.Commentary == nil || *outcome.Commentary != commentary) {
				t.Fatalf("denial commentary = %v", outcome.Commentary)
			}
			if test.kind == FileAccessApprovalFailed && !errors.Is(outcome.Cause, approvalErr) {
				t.Fatalf("approval failure cause = %v", outcome.Cause)
			}
		})
	}
}

func TestFileAccessPolicyPathDenyWinsBeforeEveryAllowPath(t *testing.T) {
	workspace := nonTemporaryDirectoryForTest(t)
	deniedRoot := t.TempDir()
	target := filepath.Join(deniedRoot, "file.txt")
	policyConfig, err := CompileLiteralTreePathDenyPolicy(deniedRoot, "deny generated")
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	approvalCalls := 0
	policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
		Context:               filesystemContextForTest(t, workspace),
		Mode:                  FileAccessMutation,
		AllowOutsideWorkspace: true,
		Approver: func(context.Context, FileAccessApprovalRequest) (FileAccessApproval, error) {
			approvalCalls++
			return FileAccessApproval{Kind: FileAccessApprovalAllowOnce}, nil
		},
		PathDenyPolicy: policyConfig,
	})
	call := policy.BeginCall()
	approvedOutside := filepath.Join(nonTemporaryDirectoryForTest(t), "approved.txt")
	assertFileAccessReason(t, call.Authorize(context.Background(), approvedOutside, approvedOutside), FileAccessReasonConfiguredAllow)
	outcome := call.Authorize(context.Background(), target, target)
	if outcome.Kind != FileAccessDeniedByPathPolicy || outcome.PathDeny == nil || outcome.PathDeny.Message != "deny generated" {
		t.Fatalf("path-deny outcome = %+v", outcome)
	}
	if approvalCalls != 0 {
		t.Fatalf("approval calls = %d, want 0", approvalCalls)
	}
}

func TestFileAccessPolicyPathDenyChecksLexicalRequestedPath(t *testing.T) {
	workspace := nonTemporaryDirectoryForTest(t)
	deniedRoot := filepath.Join(workspace, ".generated")
	if err := os.MkdirAll(deniedRoot, 0o755); err != nil {
		t.Fatalf("create denied root: %v", err)
	}
	outsideRoot := nonTemporaryDirectoryForTest(t)
	if err := os.Symlink(outsideRoot, filepath.Join(deniedRoot, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	denyPolicy, err := CompileLiteralTreePathDenyPolicy(deniedRoot, "deny generated")
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	approvalCalls := 0
	policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
		Context: filesystemContextForTest(t, workspace),
		Mode:    FileAccessMutation,
		Approver: func(context.Context, FileAccessApprovalRequest) (FileAccessApproval, error) {
			approvalCalls++
			return FileAccessApproval{Kind: FileAccessApprovalAllowOnce}, nil
		},
		PathDenyPolicy: denyPolicy,
	})

	outcome := policy.BeginCall().Authorize(
		context.Background(),
		filepath.Join(".generated", "link", "file.txt"),
		filepath.Join(outsideRoot, "file.txt"),
	)
	if outcome.Kind != FileAccessDeniedByPathPolicy {
		t.Fatalf("lexical path-deny outcome = %+v", outcome)
	}
	if approvalCalls != 0 {
		t.Fatalf("approval calls = %d, want 0", approvalCalls)
	}
}

func TestFileAccessPolicyPathIdentityFailurePrecedesApproval(t *testing.T) {
	workspace := nonTemporaryDirectoryForTest(t)
	loop := filepath.Join(workspace, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Skipf("symlink loop unavailable: %v", err)
	}
	denyPolicy, err := CompileLiteralTreePathDenyPolicy(workspace, "deny")
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	approvalCalls := 0
	policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
		Context: filesystemContextForTest(t, workspace),
		Mode:    FileAccessMutation,
		Approver: func(context.Context, FileAccessApprovalRequest) (FileAccessApproval, error) {
			approvalCalls++
			return FileAccessApproval{Kind: FileAccessApprovalAllowOnce}, nil
		},
		PathDenyPolicy: denyPolicy,
	})

	outcome := policy.BeginCall().Authorize(context.Background(), loop, loop)
	if outcome.Kind != FileAccessPolicyFailed || outcome.Cause == nil {
		t.Fatalf("path identity outcome = %+v", outcome)
	}
	if approvalCalls != 0 {
		t.Fatalf("approval calls = %d, want 0", approvalCalls)
	}
}

func TestFileAccessPolicyMutationDeniesForeignManagedWorktreeBeforeApproval(t *testing.T) {
	base := nonTemporaryDirectoryForTest(t)
	current := filepath.Join(base, "current")
	foreign := filepath.Join(base, "foreign")
	for _, root := range []string{current, foreign} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("create managed root %q: %v", root, err)
		}
	}
	managed, err := NewManagedWorktreePathContext(base, &current, []string{current, foreign})
	if err != nil {
		t.Fatalf("new managed worktree context: %v", err)
	}
	filesystemContext := filesystemContextForTest(t, current)
	filesystemContext.ManagedWorktree = managed
	approvalCalls := 0
	policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
		Context: filesystemContext,
		Mode:    FileAccessMutation,
		Approver: func(context.Context, FileAccessApprovalRequest) (FileAccessApproval, error) {
			approvalCalls++
			return FileAccessApproval{Kind: FileAccessApprovalAllowOnce}, nil
		},
	})

	target := filepath.Join(foreign, "file.txt")
	outcome := policy.BeginCall().Authorize(context.Background(), target, target)
	if outcome.Kind != FileAccessDeniedForeignManagedWorktree {
		t.Fatalf("foreign managed worktree outcome = %+v", outcome)
	}
	if approvalCalls != 0 {
		t.Fatalf("approval calls = %d, want 0", approvalCalls)
	}
	if err := policy.ValidateMutationTarget(target); !errors.Is(err, ErrForeignManagedWorktreeEdit) {
		t.Fatalf("revalidate foreign target = %v", err)
	}
}

func TestFileAccessPolicyReadDoesNotApplyMutationPolicy(t *testing.T) {
	base := nonTemporaryDirectoryForTest(t)
	current := filepath.Join(base, "current")
	foreign := filepath.Join(base, "foreign")
	for _, root := range []string{current, foreign} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("create managed root %q: %v", root, err)
		}
	}
	managed, err := NewManagedWorktreePathContext(base, &current, []string{current, foreign})
	if err != nil {
		t.Fatalf("new managed worktree context: %v", err)
	}
	filesystemContext := filesystemContextForTest(t, current)
	filesystemContext.ManagedWorktree = managed
	policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
		Context:               filesystemContext,
		Mode:                  FileAccessRead,
		AllowOutsideWorkspace: true,
	})
	target := filepath.Join(foreign, "file.png")
	outcome := policy.BeginCall().Authorize(context.Background(), target, target)
	if outcome.Kind != FileAccessAllowed || outcome.Reason != FileAccessReasonConfiguredAllow {
		t.Fatalf("read foreign managed worktree outcome = %+v", outcome)
	}
	if outcome := policy.CheckMutationTarget(target, target); outcome.Kind != FileAccessPolicyFailed {
		t.Fatalf("read mutation check = %+v", outcome)
	}
	if err := policy.ValidateMutationTarget(target); err == nil {
		t.Fatal("read policy unexpectedly accepted mutation revalidation")
	}
}

func TestFileAccessPolicyMutationRevalidationIsManagedWorktreeOnly(t *testing.T) {
	workspace := nonTemporaryDirectoryForTest(t)
	deniedRoot := nonTemporaryDirectoryForTest(t)
	denyPolicy, err := CompileLiteralTreePathDenyPolicy(deniedRoot, "deny")
	if err != nil {
		t.Fatalf("compile path deny policy: %v", err)
	}
	policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
		Context:        filesystemContextForTest(t, workspace),
		Mode:           FileAccessMutation,
		PathDenyPolicy: denyPolicy,
	})
	if err := policy.ValidateMutationTarget(filepath.Join(deniedRoot, "file.txt")); err != nil {
		t.Fatalf("narrow mutation revalidation reran path policy: %v", err)
	}
}

func TestFileAccessPolicyRejectsInvalidConfiguration(t *testing.T) {
	context := filesystemContextForTest(t, t.TempDir())
	denyPolicy, err := CompileLiteralTreePathDenyPolicy(t.TempDir(), "deny")
	if err != nil {
		t.Fatalf("compile path deny policy: %v", err)
	}
	for _, config := range []FileAccessPolicyConfig{
		{Context: FilesystemContext{}, Mode: FileAccessRead},
		{Context: context},
		{Context: context, Mode: FileAccessRead, PathDenyPolicy: denyPolicy},
	} {
		if _, err := NewFileAccessPolicy(config); err == nil {
			t.Fatalf("accepted invalid config: %+v", config)
		}
	}
}

func TestFileAccessPolicySymlinkEscapeIsOutsideTrustedRoot(t *testing.T) {
	workspace := nonTemporaryDirectoryForTest(t)
	outside := nonTemporaryDirectoryForTest(t)
	link := filepath.Join(workspace, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
		Context: filesystemContextForTest(t, workspace),
		Mode:    FileAccessRead,
	})
	requested := filepath.Join(link, "file.txt")
	resolved := filepath.Join(outside, "file.txt")
	outcome := policy.BeginCall().Authorize(context.Background(), requested, resolved)
	if outcome.Kind != FileAccessDeniedOutsideWorkspace {
		t.Fatalf("symlink escape outcome = %+v", outcome)
	}
}

func TestFileAccessPolicyCaseAliasUsesFilesystemIdentity(t *testing.T) {
	workspace := t.TempDir()
	variant, ok := findCaseVariantExistingAliasForTest(workspace)
	if !ok {
		t.Skip("filesystem does not provide a case-variant alias")
	}
	resolved, err := filepath.EvalSymlinks(variant)
	if err != nil {
		t.Fatalf("resolve case alias: %v", err)
	}
	policy := newFileAccessPolicyForTest(t, FileAccessPolicyConfig{
		Context: filesystemContextForTest(t, workspace),
		Mode:    FileAccessRead,
	})
	outcome := policy.BeginCall().Authorize(context.Background(), variant, resolved)
	if outcome.Kind != FileAccessAllowed || outcome.Reason != FileAccessReasonTrustedRoot {
		t.Fatalf("case alias outcome = %+v", outcome)
	}
}

func TestTemporaryPathAliasesAreRecognized(t *testing.T) {
	if !IsPathInTemporaryDir(filepath.Join(os.TempDir(), "kent", "file.txt")) {
		t.Fatalf("OS temporary directory %q was not recognized", os.TempDir())
	}
	for _, pair := range [][2]string{{"/tmp", "/private/tmp"}, {"/var/tmp", "/private/var/tmp"}} {
		leftInfo, leftErr := os.Stat(pair[0])
		rightInfo, rightErr := os.Stat(pair[1])
		if leftErr != nil || rightErr != nil || !os.SameFile(leftInfo, rightInfo) {
			continue
		}
		if !IsPathInTemporaryDir(filepath.Join(pair[0], "kent", "file.txt")) ||
			!IsPathInTemporaryDir(filepath.Join(pair[1], "kent", "file.txt")) {
			t.Fatalf("temporary aliases %q and %q were not both recognized", pair[0], pair[1])
		}
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
		t.Fatal("literal tree matched textual sibling")
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
	for _, target := range []string{
		filepath.Join(root, ".generated"),
		filepath.Join(root, ".generated", "skills", "one"),
	} {
		if match, ok, matchErr := globPolicy.Match(target); matchErr != nil || !ok || match.Message != "glob message" {
			t.Fatalf("glob match %q = %+v/%t err=%v", target, match, ok, matchErr)
		}
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

func assertFileAccessReason(t *testing.T, outcome FileAccessOutcome, want FileAccessReason) {
	t.Helper()
	if outcome.Kind != FileAccessAllowed || outcome.Reason != want {
		t.Fatalf("file access outcome = %+v, want allowed reason %q", outcome, want)
	}
}

func pathDenyLabelForTest(label string) *string {
	return &label
}

func findCaseVariantExistingAliasForTest(path string) (string, bool) {
	canonical := filepath.Clean(path)
	canonicalInfo, err := os.Stat(canonical)
	if err != nil {
		return "", false
	}
	parts := strings.Split(canonical, string(filepath.Separator))
	for index, part := range parts {
		runes := []rune(part)
		if len(runes) == 0 {
			continue
		}
		upper := unicode.ToUpper(runes[0])
		lower := unicode.ToLower(runes[0])
		if upper == lower {
			continue
		}
		if runes[0] == upper {
			runes[0] = lower
		} else {
			runes[0] = upper
		}
		variantParts := append([]string(nil), parts...)
		variantParts[index] = string(runes)
		variant := strings.Join(variantParts, string(filepath.Separator))
		variantInfo, err := os.Stat(variant)
		if err == nil && os.SameFile(variantInfo, canonicalInfo) {
			return variant, true
		}
	}
	return "", false
}
