package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"

	"core/shared/serverapi"
)

type recordingGitCommandRunner struct {
	delegate            gitCommandRunner
	calls               [][]string
	failWorktreeAdd     error
	failPostCreateList  error
	failWorktreeRemove  error
	succeedBranchDelete bool
	worktreeAdded       bool
	createdRoot         string
}

func (r *recordingGitCommandRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, int, error) {
	r.record(args)
	if r.worktreeAdded && slices.Equal(args, []string{"worktree", "list", "--porcelain"}) && r.failPostCreateList != nil {
		return nil, 1, r.failPostCreateList
	}
	return r.delegate.Run(ctx, dir, args...)
}

func (r *recordingGitCommandRunner) Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	r.record(args)
	if len(args) >= 2 && args[0] == "worktree" && args[1] == "add" {
		if r.failWorktreeAdd != nil {
			return nil, r.failWorktreeAdd
		}
		output, err := r.delegate.Output(ctx, dir, args...)
		if err == nil {
			r.worktreeAdded = true
			if len(args) >= 2 {
				r.createdRoot = args[len(args)-2]
			}
		}
		return output, err
	}
	if len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" && r.failWorktreeRemove != nil {
		return nil, r.failWorktreeRemove
	}
	if r.succeedBranchDelete && slices.Equal(args, []string{"branch", "-d", "feature/post-create-cleanup"}) {
		return nil, nil
	}
	return r.delegate.Output(ctx, dir, args...)
}

func (r *recordingGitCommandRunner) record(args []string) {
	r.calls = append(r.calls, append([]string(nil), args...))
}

func (r *recordingGitCommandRunner) resetCalls() {
	r.calls = nil
}

func hasGitCall(calls [][]string, want []string) bool {
	for _, call := range calls {
		if slices.Equal(call, want) {
			return true
		}
	}
	return false
}

func TestCreateWorktreeRejectsBlankBaseRefBeforeGit(t *testing.T) {
	env := newServiceTestEnv(t)
	runner := &recordingGitCommandRunner{delegate: execGitCommandRunner{}}
	env.service.git = NewGitInspector(runner)

	_, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ClientRequestID:  "blank-base-ref",
		SessionID:        env.session.Meta().SessionID,
		CreateBranch:     true,
		BranchName:       "feature/blank-base-ref",
	})
	var typed *serverapi.WorktreeCreateError
	if !errors.As(err, &typed) || typed.Owner != serverapi.WorktreeCreateErrorOwnerBaseRef {
		t.Fatalf("CreateWorktree error = %T %v, want Base-ref-owned error", err, err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("Git calls = %v, want none", runner.calls)
	}
}

func TestCreateWorktreeResolvesBaseRefOnceAndUsesCommitOIDForAdd(t *testing.T) {
	env := newServiceTestEnv(t)
	runner := &recordingGitCommandRunner{delegate: execGitCommandRunner{}}
	env.service.git = NewGitInspector(runner)
	commitOID := runGit(t, env.workspaceRoot, "rev-parse", "HEAD")

	_, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ClientRequestID:  "resolved-base-ref",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "HEAD",
		CreateBranch:     true,
		BranchName:       "feature/resolved-base-ref",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if !hasGitCall(runner.calls, []string{"rev-parse", "--verify", "--quiet", "HEAD^{object}"}) ||
		!hasGitCall(runner.calls, []string{"rev-parse", "--verify", "--quiet", "HEAD^{commit}"}) ||
		!hasGitCall(runner.calls, []string{"rev-parse", "--symbolic-full-name", "--verify", "--quiet", "HEAD"}) {
		t.Fatalf("Base-ref resolution calls = %v, want one authoritative resolution", runner.calls)
	}
	resolutionCount := 0
	for _, call := range runner.calls {
		if slices.Equal(call, []string{"rev-parse", "--verify", "--quiet", "HEAD^{object}"}) {
			resolutionCount++
		}
	}
	if resolutionCount != 1 {
		t.Fatalf("object resolution count = %d, want one", resolutionCount)
	}
	var addCall []string
	for _, call := range runner.calls {
		if len(call) >= 2 && call[0] == "worktree" && call[1] == "add" {
			addCall = call
			break
		}
	}
	if len(addCall) == 0 {
		t.Fatalf("Git calls = %v, want worktree add", runner.calls)
	}
	if addCall[len(addCall)-1] != commitOID {
		t.Fatalf("worktree add args = %v, want resolved commit oid %q", addCall, commitOID)
	}
	if slices.Contains(addCall, "HEAD") {
		t.Fatalf("worktree add received moving Base ref: %v", addCall)
	}
}

func TestCreateWorktreeBaseRefResolutionFailuresAreBaseRefOwned(t *testing.T) {
	env := newServiceTestEnv(t)
	runner := &recordingGitCommandRunner{delegate: execGitCommandRunner{}}
	env.service.git = NewGitInspector(runner)
	blobPath := t.TempDir() + "/blob"
	writeFile(t, blobPath, "blob object")
	blobOID := runGit(t, env.workspaceRoot, "hash-object", "-w", blobPath)
	tests := []struct {
		name    string
		baseRef string
	}{
		{name: "invalid", baseRef: "HEAD^{not-a-revision}"},
		{name: "unresolved", baseRef: "missing-revision"},
		{name: "non-commit", baseRef: blobOID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, expectedErr := env.service.git.ResolveRevision(env.ctx, env.workspaceRoot, test.baseRef)
			var expectedResolutionErr *GitRevisionResolutionError
			if !errors.As(expectedErr, &expectedResolutionErr) {
				t.Fatalf("direct resolution error = %T %v, want GitRevisionResolutionError", expectedErr, expectedErr)
			}
			runner.resetCalls()
			_, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
				SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
				ClientRequestID:  "resolve-" + test.name,
				SessionID:        env.session.Meta().SessionID,
				BaseRef:          test.baseRef,
				CreateBranch:     true,
				BranchName:       "feature/resolve-" + test.name,
			})
			var typed *serverapi.WorktreeCreateError
			if !errors.As(err, &typed) || typed.Owner != serverapi.WorktreeCreateErrorOwnerBaseRef {
				t.Fatalf("CreateWorktree error = %T %v, want Base-ref-owned error", err, err)
			}
			if typed.Diagnostic != expectedErr.Error() {
				t.Fatalf("diagnostic = %q, want resolver diagnostic %q", typed.Diagnostic, expectedErr.Error())
			}
			var resolutionErr *GitRevisionResolutionError
			if !errors.As(err, &resolutionErr) {
				t.Fatalf("CreateWorktree error = %T %v, want resolver cause", err, err)
			}
		})
	}
}

func TestCreateWorktreePostResolutionAddFailureIsFormOwned(t *testing.T) {
	env := newServiceTestEnv(t)
	addErr := errors.New("worktree add lost resolved object")
	runner := &recordingGitCommandRunner{
		delegate:        execGitCommandRunner{},
		failWorktreeAdd: addErr,
	}
	env.service.git = NewGitInspector(runner)

	_, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ClientRequestID:  "post-resolution-add",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "HEAD",
		CreateBranch:     true,
		BranchName:       "feature/post-resolution-add",
	})
	var typed *serverapi.WorktreeCreateError
	if !errors.As(err, &typed) || typed.Owner != serverapi.WorktreeCreateErrorOwnerForm {
		t.Fatalf("CreateWorktree error = %T %v, want form-owned error", err, err)
	}
	if !errors.Is(err, addErr) {
		t.Fatalf("CreateWorktree error = %v, want add cause %v", err, addErr)
	}
	if typed.Diagnostic != addErr.Error() {
		t.Fatalf("diagnostic = %q, want add diagnostic %q", typed.Diagnostic, addErr.Error())
	}
}

func TestCreateWorktreeExistingAndDetachedTargetsDoNotResolveBaseRefAgain(t *testing.T) {
	env := newServiceTestEnv(t)
	runGit(t, env.workspaceRoot, "branch", "feature/existing-create")
	runner := &recordingGitCommandRunner{delegate: execGitCommandRunner{}}
	env.service.git = NewGitInspector(runner)

	_, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ClientRequestID:  "existing-target",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "feature/existing-create",
		BranchName:       "",
	})
	if err != nil {
		t.Fatalf("CreateWorktree existing branch: %v", err)
	}
	if hasGitCall(runner.calls, []string{"rev-parse", "--verify", "--quiet", "feature/existing-create^{commit}"}) {
		t.Fatalf("existing branch creation resolved Base ref a second time: %v", runner.calls)
	}

	runner.resetCalls()
	_, err = env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ClientRequestID:  "detached-target",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "HEAD",
	})
	var typed *serverapi.WorktreeCreateError
	if !errors.As(err, &typed) || typed.Owner != serverapi.WorktreeCreateErrorOwnerForm {
		t.Fatalf("detached CreateWorktree error = %T %v, want form-owned error", err, err)
	}
	if hasGitCall(runner.calls, []string{"rev-parse", "--verify", "--quiet", "HEAD^{commit}"}) {
		t.Fatalf("detached creation resolved Base ref a second time: %v", runner.calls)
	}
}

func TestCreateWorktreeJoinsPostCreateAndCleanupCausesBeforeFormClassification(t *testing.T) {
	env := newServiceTestEnv(t)
	postCreateErr := errors.New("post-create inspection failed")
	cleanupErr := errors.New("cleanup remove failed")
	runner := &recordingGitCommandRunner{
		delegate:            execGitCommandRunner{},
		failPostCreateList:  postCreateErr,
		failWorktreeRemove:  cleanupErr,
		succeedBranchDelete: true,
	}
	env.service.git = NewGitInspector(runner)

	_, err := env.service.CreateWorktree(env.ctx, serverapi.WorktreeCreateRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ClientRequestID:  "post-create-cleanup",
		SessionID:        env.session.Meta().SessionID,
		BaseRef:          "HEAD",
		CreateBranch:     true,
		BranchName:       "feature/post-create-cleanup",
	})
	var listErr *GitWorktreeListError
	if !errors.As(err, &listErr) || !errors.Is(err, postCreateErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("CreateWorktree error = %T %v, want joined list and cleanup causes", err, err)
	}
	var typed *serverapi.WorktreeCreateError
	if !errors.As(err, &typed) || typed.Owner != serverapi.WorktreeCreateErrorOwnerForm {
		t.Fatalf("CreateWorktree error = %T %v, want form-owned error", err, err)
	}
	expectedSource := errors.Join(
		&GitWorktreeListError{
			Kind:  GitWorktreeListErrorInspectionFailed,
			Cause: fmt.Errorf("git worktree list --porcelain: %w", postCreateErr),
		},
		fmt.Errorf("remove failed worktree %q: %w", runner.createdRoot, cleanupErr),
	)
	if typed.Diagnostic != expectedSource.Error() {
		t.Fatalf("diagnostic = %q, want joined source diagnostic %q", typed.Diagnostic, expectedSource.Error())
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}
