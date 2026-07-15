package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"core/shared/serverapi"
)

type canceledGitCommandRunner struct{}

func (canceledGitCommandRunner) Output(context.Context, string, ...string) ([]byte, error) {
	return nil, context.Canceled
}

func (canceledGitCommandRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, int, error) {
	return nil, -1, ctx.Err()
}

type stubGitCommandRunner struct {
	output    []byte
	err       error
	exitCode  int
	dir       string
	args      []string
	outputs   map[string][]byte
	errors    map[string]error
	exitCodes map[string]int
}

func (s *stubGitCommandRunner) Output(_ context.Context, dir string, args ...string) ([]byte, error) {
	output, exitCode, err := s.Run(context.Background(), dir, args...)
	if err != nil {
		return nil, formatGitRunError(exitCode, err, output, args...)
	}
	return output, nil
}

func (s *stubGitCommandRunner) Run(_ context.Context, dir string, args ...string) ([]byte, int, error) {
	s.dir = dir
	s.args = append([]string(nil), args...)
	key := strings.Join(args, "\x00")
	output := append([]byte(nil), s.output...)
	if s.outputs != nil {
		if specific, ok := s.outputs[key]; ok {
			output = append([]byte(nil), specific...)
		}
	}
	err := s.err
	if s.errors != nil {
		if specific, ok := s.errors[key]; ok {
			err = specific
		}
	}
	exitCode := s.exitCode
	if s.exitCodes != nil {
		if specific, ok := s.exitCodes[key]; ok {
			exitCode = specific
		}
	}
	if err != nil && exitCode == 0 {
		exitCode = 1
	}
	return output, exitCode, err
}

func TestGitInspectorListParsesPorcelainTopology(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	markGitRepository(t, workspaceRoot)
	linkedRoot := filepath.Join(t.TempDir(), "linked")
	prunableRoot := filepath.Join(t.TempDir(), "missing-linked")
	runner := &stubGitCommandRunner{output: []byte("worktree " + workspaceRoot + "\nHEAD aaa111\nbranch refs/heads/main\n\nworktree " + linkedRoot + "\nHEAD bbb222\nbranch refs/heads/feature/worktree\nlocked bootstrap running\n\nworktree " + prunableRoot + "\nHEAD ccc333\ndetached\nprunable gitdir file points to non-existent location\n")}
	inspector := NewGitInspector(runner)
	entries, err := inspector.List(context.Background(), workspaceRoot)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got, want := runner.args, []string{"worktree", "list", "--porcelain"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("git args=%v want=%v", got, want)
	}
	if got, want := runner.dir, canonicalTestPath(t, workspaceRoot); got != want {
		t.Fatalf("git dir=%q want=%q", got, want)
	}
	if len(entries) != 3 {
		t.Fatalf("entries=%d want 3", len(entries))
	}
	mainEntry := entries[0]
	if !mainEntry.IsMain || mainEntry.BranchName != "main" || mainEntry.Root != canonicalTestPath(t, workspaceRoot) {
		t.Fatalf("unexpected main entry: %+v", mainEntry)
	}
	linkedEntry := entries[1]
	if linkedEntry.IsMain || linkedEntry.BranchRef != "refs/heads/feature/worktree" || linkedEntry.BranchName != "feature/worktree" || linkedEntry.LockedReason != "bootstrap running" {
		t.Fatalf("unexpected linked entry: %+v", linkedEntry)
	}
	prunableEntry := entries[2]
	if !prunableEntry.Detached || prunableEntry.BranchName != "" || prunableEntry.PrunableReason == "" || prunableEntry.Root != canonicalTestPath(t, prunableRoot) {
		t.Fatalf("unexpected prunable entry: %+v", prunableEntry)
	}
}

func TestGitInspectorListClassifiesActualNonRepositoryWithoutRunningGit(t *testing.T) {
	workspaceRoot := t.TempDir()
	runner := &stubGitCommandRunner{}
	inspector := NewGitInspector(runner)

	_, err := inspector.List(context.Background(), workspaceRoot)
	var listErr *GitWorktreeListError
	if !errors.As(err, &listErr) {
		t.Fatalf("List error = %T, want typed worktree-list error", err)
	}
	if listErr.Kind != GitWorktreeListErrorNotRepository {
		t.Fatalf("worktree-list error kind = %q, want not repository", listErr.Kind)
	}
	if runner.args != nil {
		t.Fatalf("non-repository probe ran git command %v", runner.args)
	}
}

func TestGitInspectorListClassifiesUnrelatedExit128AsInspectionFailure(t *testing.T) {
	workspaceRoot := t.TempDir()
	markGitRepository(t, workspaceRoot)
	rawDiagnostic := errors.New("fatal diagnostic whose wording is not a contract")
	inspector := NewGitInspector(&stubGitCommandRunner{
		output:   []byte("opaque stderr"),
		err:      rawDiagnostic,
		exitCode: 128,
	})

	_, err := inspector.List(context.Background(), workspaceRoot)
	var listErr *GitWorktreeListError
	if !errors.As(err, &listErr) {
		t.Fatalf("List error = %T, want typed worktree-list error", err)
	}
	if listErr.Kind != GitWorktreeListErrorInspectionFailed {
		t.Fatalf("worktree-list error kind = %q, want inspection failed", listErr.Kind)
	}
	if !errors.Is(err, rawDiagnostic) {
		t.Fatal("typed worktree-list error discarded the diagnostic cause")
	}
}

func markGitRepository(t *testing.T, workspaceRoot string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(workspaceRoot, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll .git: %v", err)
	}
}

func TestParseGitWorktreeListPorcelainRejectsUnsupportedKeys(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	_, err := parseGitWorktreeListPorcelain("worktree "+workspaceRoot+"\nHEAD aaa111\nunsupported nope\n", workspaceRoot)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestGitInspectorAddCreatesBranchFromExplicitBaseRef(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	worktreeRoot := filepath.Join(t.TempDir(), "linked")
	runner := &stubGitCommandRunner{outputs: map[string][]byte{
		strings.Join([]string{"worktree", "add", "-b", "feature/new", canonicalTestPath(t, worktreeRoot), "HEAD"}, "\x00"): nil,
	}}
	inspector := NewGitInspector(runner)
	created, err := inspector.Add(context.Background(), workspaceRoot, worktreeRoot, CreateSpec{BaseRef: "HEAD", CreateBranch: true, BranchName: "feature/new"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !created {
		t.Fatal("expected created branch=true for new branch")
	}
	if got, want := runner.args, []string{"worktree", "add", "-b", "feature/new", canonicalTestPath(t, worktreeRoot), "HEAD"}; !equalStrings(got, want) {
		t.Fatalf("git args=%v want=%v", got, want)
	}
	if got, want := runner.dir, canonicalTestPath(t, workspaceRoot); got != want {
		t.Fatalf("git dir=%q want=%q", got, want)
	}
}

func TestGitInspectorAddRejectsCreateBranchWithoutBaseRef(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	worktreeRoot := filepath.Join(t.TempDir(), "linked")
	runner := &stubGitCommandRunner{}
	inspector := NewGitInspector(runner)

	_, err := inspector.Add(context.Background(), workspaceRoot, worktreeRoot, CreateSpec{CreateBranch: true, BranchName: "feature/new"})

	if !errors.Is(err, ErrBaseRefRequired) {
		t.Fatalf("error = %v, want base ref validation", err)
	}
	if runner.args != nil {
		t.Fatalf("expected no git command, got %v", runner.args)
	}
}

func TestGitInspectorDirtyFileCountUsesPorcelainStatus(t *testing.T) {
	worktreeRoot := filepath.Join(t.TempDir(), "linked")
	runner := &stubGitCommandRunner{outputs: map[string][]byte{
		strings.Join([]string{"status", "--porcelain=v1", "-z"}, "\x00"): []byte(" M changed.go\x00?? new.go\x00R  renamed.go\x00old.go\x00"),
	}}
	inspector := NewGitInspector(runner)

	count, err := inspector.DirtyFileCount(context.Background(), worktreeRoot)
	if err != nil {
		t.Fatalf("DirtyFileCount: %v", err)
	}
	if count != 3 {
		t.Fatalf("dirty count = %d, want 3", count)
	}
	if got, want := runner.args, []string{"status", "--porcelain=v1", "-z"}; !equalStrings(got, want) {
		t.Fatalf("git args=%v want=%v", got, want)
	}
	if got, want := runner.dir, canonicalTestPath(t, worktreeRoot); got != want {
		t.Fatalf("git dir=%q want=%q", got, want)
	}
}

func TestGitInspectorProbeDirtyStateUsesTypedCleanDirtyAndUnknownResults(t *testing.T) {
	root := t.TempDir()
	clean := NewGitInspector(&stubGitCommandRunner{})
	state, err := clean.ProbeDirtyState(context.Background(), root)
	if err != nil || state.Kind != serverapi.WorktreeDirtyStateClean || state.DirtyFileCount == nil || *state.DirtyFileCount != 0 {
		t.Fatalf("clean state = %+v err=%v", state, err)
	}

	dirty := NewGitInspector(&stubGitCommandRunner{output: []byte(" M changed.go\x00")})
	state, err = dirty.ProbeDirtyState(context.Background(), root)
	if err != nil || state.Kind != serverapi.WorktreeDirtyStateDirty || state.DirtyFileCount == nil || *state.DirtyFileCount != 1 {
		t.Fatalf("dirty state = %+v err=%v", state, err)
	}

	unknown := NewGitInspector(&stubGitCommandRunner{err: errors.New("status unavailable"), exitCode: 1})
	state, err = unknown.ProbeDirtyState(context.Background(), root)
	if err != nil || state.Kind != serverapi.WorktreeDirtyStateUnknown || state.DirtyFileCount != nil || state.UnknownCause == nil {
		t.Fatalf("unknown state = %+v err=%v", state, err)
	}
}

func TestGitInspectorInspectsTargetIdentityAndExactRef(t *testing.T) {
	root := t.TempDir()
	commonDir := filepath.Join(root, ".git")
	if err := os.Mkdir(commonDir, 0o755); err != nil {
		t.Fatalf("Mkdir Git marker: %v", err)
	}
	runner := &stubGitCommandRunner{outputs: map[string][]byte{
		strings.Join([]string{"rev-parse", "--show-toplevel"}, "\x00"):  []byte(root + "\n"),
		strings.Join([]string{"rev-parse", "--git-common-dir"}, "\x00"): []byte(commonDir + "\n"),
	}}
	inspector := NewGitInspector(runner)
	inspection, err := inspector.InspectTarget(context.Background(), root)
	if err != nil {
		t.Fatalf("InspectTarget: %v", err)
	}
	if inspection.Identity.TopLevelRoot != canonicalTestPath(t, root) || inspection.Identity.CommonDir != canonicalTestPath(t, commonDir) {
		t.Fatalf("inspection = %+v", inspection)
	}

	runner.errors = map[string]error{strings.Join([]string{"rev-parse", "--verify", "--quiet", "refs/heads/feature/*^{object}"}, "\x00"): errors.New("exit status 1")}
	runner.exitCodes = map[string]int{strings.Join([]string{"rev-parse", "--verify", "--quiet", "refs/heads/feature/*^{object}"}, "\x00"): 1}
	exists, err := inspector.RefExists(context.Background(), root, "refs/heads/feature/*")
	if err != nil || exists {
		t.Fatalf("RefExists = %t, %v; want false, nil", exists, err)
	}
}

func TestGitInspectorFindCreatedWorktreeUsesCanonicalRoot(t *testing.T) {
	workspaceRoot := t.TempDir()
	markGitRepository(t, workspaceRoot)
	createdRoot := filepath.Join(t.TempDir(), "created")
	runner := &stubGitCommandRunner{output: []byte("worktree " + workspaceRoot + "\nHEAD aaa111\nbranch refs/heads/main\n\nworktree " + createdRoot + "\nHEAD bbb222\nbranch refs/heads/feature\n")}
	created, found, err := NewGitInspector(runner).FindCreatedWorktree(context.Background(), workspaceRoot, createdRoot)
	if err != nil || !found || created.Root != canonicalTestPath(t, createdRoot) {
		t.Fatalf("FindCreatedWorktree = %+v found=%t err=%v", created, found, err)
	}
}

func TestGitInspectorRemoveUsesForceWhenRequested(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	worktreeRoot := filepath.Join(t.TempDir(), "linked")
	runner := &stubGitCommandRunner{}
	inspector := NewGitInspector(runner)

	if err := inspector.Remove(context.Background(), workspaceRoot, worktreeRoot, true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got, want := runner.args, []string{"worktree", "remove", "--force", canonicalTestPath(t, worktreeRoot)}; !equalStrings(got, want) {
		t.Fatalf("git args=%v want=%v", got, want)
	}
}

func TestGitInspectorAddUsesExistingRefWithoutCreatingBranch(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	worktreeRoot := filepath.Join(t.TempDir(), "linked")
	runner := &stubGitCommandRunner{outputs: map[string][]byte{
		strings.Join([]string{"worktree", "add", canonicalTestPath(t, worktreeRoot), "feature/existing"}, "\x00"): nil,
	}}
	inspector := NewGitInspector(runner)
	created, err := inspector.Add(context.Background(), workspaceRoot, worktreeRoot, CreateSpec{BaseRef: "feature/existing", CreateBranch: false})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if created {
		t.Fatal("expected created branch=false for existing ref")
	}
	if got, want := runner.args, []string{"worktree", "add", canonicalTestPath(t, worktreeRoot), "feature/existing"}; !equalStrings(got, want) {
		t.Fatalf("git args=%v want=%v", got, want)
	}
	if got, want := runner.dir, canonicalTestPath(t, workspaceRoot); got != want {
		t.Fatalf("git dir=%q want=%q", got, want)
	}
}

func TestGitInspectorResolveCreateTargetClassifiesExistingBranch(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	runner := &stubGitCommandRunner{outputs: map[string][]byte{
		strings.Join([]string{"rev-parse", "--verify", "--quiet", "refs/heads/main^{object}"}, "\x00"): []byte("abc123\n"),
	}}
	inspector := NewGitInspector(runner)
	resolution, err := inspector.ResolveCreateTarget(context.Background(), workspaceRoot, "main")
	if err != nil {
		t.Fatalf("ResolveCreateTarget: %v", err)
	}
	if resolution.Kind != CreateTargetResolutionKindExistingBranch || resolution.ResolvedRef != "main" {
		t.Fatalf("unexpected resolution: %+v", resolution)
	}
}

func TestGitInspectorResolveCreateTargetTreatsPrefixOnlyBranchAsNewBranch(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	runner := &stubGitCommandRunner{
		errors: map[string]error{
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "refs/heads/feature^{object}"}, "\x00"): errors.New("exit status 1"),
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "feature^{object}"}, "\x00"):            errors.New("exit status 1"),
		},
		exitCodes: map[string]int{
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "refs/heads/feature^{object}"}, "\x00"): 1,
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "feature^{object}"}, "\x00"):            1,
		},
	}
	inspector := NewGitInspector(runner)
	resolution, err := inspector.ResolveCreateTarget(context.Background(), workspaceRoot, "feature")
	if err != nil {
		t.Fatalf("ResolveCreateTarget: %v", err)
	}
	if resolution.Kind != CreateTargetResolutionKindNewBranch {
		t.Fatalf("unexpected resolution: %+v", resolution)
	}
}

func TestGitInspectorResolveCreateTargetClassifiesDetachedRef(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	runner := &stubGitCommandRunner{
		outputs: map[string][]byte{
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "HEAD^{object}"}, "\x00"): []byte("abc123\n"),
		},
		errors: map[string]error{
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "refs/heads/HEAD^{object}"}, "\x00"): errors.New("exit status 1"),
		},
		exitCodes: map[string]int{
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "refs/heads/HEAD^{object}"}, "\x00"): 1,
		},
	}
	inspector := NewGitInspector(runner)
	resolution, err := inspector.ResolveCreateTarget(context.Background(), workspaceRoot, "HEAD")
	if err != nil {
		t.Fatalf("ResolveCreateTarget: %v", err)
	}
	if resolution.Kind != CreateTargetResolutionKindDetachedRef || resolution.ResolvedRef != "abc123" {
		t.Fatalf("unexpected resolution: %+v", resolution)
	}
}

func TestGitInspectorResolveCreateTargetClassifiesNewBranch(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	runner := &stubGitCommandRunner{
		errors: map[string]error{
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "refs/heads/feature/new^{object}"}, "\x00"): errors.New("exit status 1"),
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "feature/new^{object}"}, "\x00"):            errors.New("exit status 1"),
		},
		exitCodes: map[string]int{
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "refs/heads/feature/new^{object}"}, "\x00"): 1,
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "feature/new^{object}"}, "\x00"):            1,
		},
	}
	inspector := NewGitInspector(runner)
	resolution, err := inspector.ResolveCreateTarget(context.Background(), workspaceRoot, "feature/new")
	if err != nil {
		t.Fatalf("ResolveCreateTarget: %v", err)
	}
	if resolution.Kind != CreateTargetResolutionKindNewBranch {
		t.Fatalf("unexpected resolution: %+v", resolution)
	}
}

func TestGitInspectorResolveCreateTargetRejectsInvalidBranchName(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	runner := &stubGitCommandRunner{
		errors: map[string]error{
			strings.Join([]string{"check-ref-format", "--branch", "feature..bad"}, "\x00"):              errors.New("exit status 128"),
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "feature..bad^{object}"}, "\x00"): errors.New("exit status 1"),
		},
		exitCodes: map[string]int{
			strings.Join([]string{"check-ref-format", "--branch", "feature..bad"}, "\x00"):              128,
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "feature..bad^{object}"}, "\x00"): 1,
		},
	}
	inspector := NewGitInspector(runner)
	_, err := inspector.ResolveCreateTarget(context.Background(), workspaceRoot, "feature..bad")
	var invalidTarget *InvalidCreateTargetError
	if !errors.As(err, &invalidTarget) || invalidTarget.Target != "feature..bad" {
		t.Fatalf("ResolveCreateTarget error = %v", err)
	}
}

func TestGitInspectorBranchExistsUsesExactRefLookup(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	runner := &stubGitCommandRunner{
		errors: map[string]error{
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "refs/heads/feature/*^{object}"}, "\x00"): errors.New("exit status 1"),
		},
		exitCodes: map[string]int{
			strings.Join([]string{"rev-parse", "--verify", "--quiet", "refs/heads/feature/*^{object}"}, "\x00"): 1,
		},
	}
	inspector := NewGitInspector(runner)
	exists, err := inspector.BranchExists(context.Background(), workspaceRoot, "feature/*")
	if err != nil {
		t.Fatalf("BranchExists: %v", err)
	}
	if exists {
		t.Fatal("expected exact-ref lookup to treat glob-like branch as absent")
	}
	if got, want := runner.args, []string{"rev-parse", "--verify", "--quiet", "refs/heads/feature/*^{object}"}; !equalStrings(got, want) {
		t.Fatalf("git args=%v want=%v", got, want)
	}
}

func TestFormatGitRunErrorWrapsNegativeExitCause(t *testing.T) {
	err := formatGitRunError(-1, context.Canceled, []byte("killed"), "worktree", "list")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected wrapped context cancellation, got %v", err)
	}
}

func TestGitInspectorIsValidBranchNamePropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inspector := NewGitInspector(canceledGitCommandRunner{})
	valid, err := inspector.isValidBranchName(ctx, t.TempDir(), "feature/canceled")
	if valid {
		t.Fatal("expected invalid branch result on cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("isValidBranchName error = %v, want context canceled", err)
	}
}

func TestGitInspectorResolveRevisionPeelsCommitAndReportsCanonicalRef(t *testing.T) {
	workspaceRoot := t.TempDir()
	initGitRepo(t, workspaceRoot)
	commitOID := runGit(t, workspaceRoot, "rev-parse", "HEAD")
	headRef := runGit(t, workspaceRoot, "symbolic-ref", "HEAD")
	runGit(t, workspaceRoot, "branch", "feature/target")
	runGit(t, workspaceRoot, "-c", "tag.gpgSign=false", "tag", "v0")
	runGit(t, workspaceRoot, "-c", "tag.gpgSign=false", "tag", "-a", "v1", "-m", "release")
	blobPath := filepath.Join(workspaceRoot, "blob.txt")
	if err := os.WriteFile(blobPath, []byte("not a commit\n"), 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	blobOID := runGit(t, workspaceRoot, "hash-object", "-w", "blob.txt")

	inspector := NewGitInspector(nil)
	for _, test := range []struct {
		name     string
		revision string
		wantRef  *string
	}{
		{name: "head", revision: "HEAD", wantRef: stringPointer(headRef)},
		{name: "branch", revision: "feature/target", wantRef: stringPointer("refs/heads/feature/target")},
		{name: "tag", revision: "v0", wantRef: stringPointer("refs/tags/v0")},
		{name: "annotated tag", revision: "v1", wantRef: stringPointer("refs/tags/v1")},
		{name: "commit", revision: commitOID},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolution, err := inspector.ResolveRevision(context.Background(), workspaceRoot, test.revision)
			if err != nil {
				t.Fatalf("ResolveRevision(%q): %v", test.revision, err)
			}
			if resolution.CommitOID != commitOID {
				t.Fatalf("commit oid = %q, want %q", resolution.CommitOID, commitOID)
			}
			if (resolution.CanonicalRef == nil) != (test.wantRef == nil) || resolution.CanonicalRef != nil && *resolution.CanonicalRef != *test.wantRef {
				t.Fatalf("canonical ref = %v, want %v", resolution.CanonicalRef, test.wantRef)
			}
		})
	}

	_, err := inspector.ResolveRevision(context.Background(), workspaceRoot, blobOID)
	var resolutionErr *GitRevisionResolutionError
	if !errors.As(err, &resolutionErr) || resolutionErr.Kind != GitRevisionResolutionErrorNonCommit {
		t.Fatalf("ResolveRevision(non-commit) error = %v, want typed non-commit error", err)
	}
	_, err = inspector.ResolveRevision(context.Background(), workspaceRoot, "missing-revision")
	if !errors.As(err, &resolutionErr) || resolutionErr.Kind != GitRevisionResolutionErrorInvalidRevision {
		t.Fatalf("ResolveRevision(missing) error = %v, want typed invalid revision error", err)
	}

	runGit(t, workspaceRoot, "checkout", "-q", "--detach")
	detached, err := inspector.ResolveHEAD(context.Background(), workspaceRoot)
	if err != nil {
		t.Fatalf("ResolveHEAD(detached): %v", err)
	}
	if detached.CommitOID != commitOID || detached.CanonicalRef != nil {
		t.Fatalf("detached head resolution = %+v, want commit %q without canonical ref", detached, commitOID)
	}
}

func TestGitInspectorResolveDefaultBranchUsesConfiguredRemoteHEAD(t *testing.T) {
	newRepositoryWithRemoteHEAD := func(t *testing.T, remoteName string, branchName string) string {
		t.Helper()
		workspaceRoot := t.TempDir()
		initGitRepo(t, workspaceRoot)
		commitOID := runGit(t, workspaceRoot, "rev-parse", "HEAD")
		runGit(t, workspaceRoot, "remote", "add", remoteName, "https://example.invalid/"+remoteName+".git")
		runGit(t, workspaceRoot, "update-ref", "refs/remotes/"+remoteName+"/"+branchName, commitOID)
		runGit(t, workspaceRoot, "symbolic-ref", "refs/remotes/"+remoteName+"/HEAD", "refs/remotes/"+remoteName+"/"+branchName)
		return workspaceRoot
	}

	t.Run("prefers origin", func(t *testing.T) {
		workspaceRoot := newRepositoryWithRemoteHEAD(t, "origin", "main")
		commitOID := runGit(t, workspaceRoot, "rev-parse", "HEAD")
		runGit(t, workspaceRoot, "remote", "add", "upstream", "https://example.invalid/upstream.git")
		runGit(t, workspaceRoot, "update-ref", "refs/remotes/upstream/trunk", commitOID)
		runGit(t, workspaceRoot, "symbolic-ref", "refs/remotes/upstream/HEAD", "refs/remotes/upstream/trunk")

		resolution, err := NewGitInspector(nil).ResolveDefaultBranch(context.Background(), workspaceRoot)
		if err != nil {
			t.Fatalf("ResolveDefaultBranch: %v", err)
		}
		if resolution.RemoteName != "origin" || resolution.Ref != "refs/remotes/origin/main" {
			t.Fatalf("resolution = %+v, want origin refs/remotes/origin/main", resolution)
		}
	})

	t.Run("uses exactly one non-origin remote", func(t *testing.T) {
		workspaceRoot := newRepositoryWithRemoteHEAD(t, "upstream", "trunk")

		resolution, err := NewGitInspector(nil).ResolveDefaultBranch(context.Background(), workspaceRoot)
		if err != nil {
			t.Fatalf("ResolveDefaultBranch: %v", err)
		}
		if resolution.RemoteName != "upstream" || resolution.Ref != "refs/remotes/upstream/trunk" {
			t.Fatalf("resolution = %+v, want upstream refs/remotes/upstream/trunk", resolution)
		}
	})

	t.Run("rejects missing and ambiguous remote heads", func(t *testing.T) {
		missingRoot := t.TempDir()
		initGitRepo(t, missingRoot)
		_, err := NewGitInspector(nil).ResolveDefaultBranch(context.Background(), missingRoot)
		var missingErr *GitDefaultBranchResolutionError
		if !errors.As(err, &missingErr) || missingErr.Kind != GitDefaultBranchResolutionErrorMissing {
			t.Fatalf("missing ResolveDefaultBranch error = %v, want typed missing error", err)
		}

		ambiguousRoot := newRepositoryWithRemoteHEAD(t, "upstream", "trunk")
		commitOID := runGit(t, ambiguousRoot, "rev-parse", "HEAD")
		runGit(t, ambiguousRoot, "remote", "add", "fork", "https://example.invalid/fork.git")
		runGit(t, ambiguousRoot, "update-ref", "refs/remotes/fork/main", commitOID)
		runGit(t, ambiguousRoot, "symbolic-ref", "refs/remotes/fork/HEAD", "refs/remotes/fork/main")
		_, err = NewGitInspector(nil).ResolveDefaultBranch(context.Background(), ambiguousRoot)
		var ambiguousErr *GitDefaultBranchResolutionError
		if !errors.As(err, &ambiguousErr) || ambiguousErr.Kind != GitDefaultBranchResolutionErrorAmbiguous {
			t.Fatalf("ambiguous ResolveDefaultBranch error = %v, want typed ambiguous error", err)
		}
	})
}

func TestGitInspectorValidateManagedWorktreeIdentity(t *testing.T) {
	newManagedWorktree := func(t *testing.T, branchName string) (string, string) {
		t.Helper()
		sourceRoot := t.TempDir()
		initGitRepo(t, sourceRoot)
		targetRoot := filepath.Join(t.TempDir(), "target")
		runGit(t, sourceRoot, "worktree", "add", "-q", "-b", branchName, targetRoot, "HEAD")
		return sourceRoot, targetRoot
	}
	assertIdentityError := func(t *testing.T, err error, want ManagedWorktreeIdentityErrorKind) {
		t.Helper()
		var identityErr *ManagedWorktreeIdentityError
		if !errors.As(err, &identityErr) || identityErr.Kind != want {
			t.Fatalf("identity error = %v, want typed %q error", err, want)
		}
	}

	t.Run("healthy even after task branch advances", func(t *testing.T) {
		const taskBranch = "task-healthy"
		sourceRoot, targetRoot := newManagedWorktree(t, taskBranch)
		inspector := NewGitInspector(nil)

		identity, err := inspector.ValidateManagedWorktreeIdentity(context.Background(), ManagedWorktreeIdentitySpec{
			SourceWorkspaceRoot:  sourceRoot,
			ExpectedWorktreeRoot: targetRoot,
		})
		if err != nil {
			t.Fatalf("ValidateManagedWorktreeIdentity: %v", err)
		}
		if identity.SourceTopLevel != canonicalTestPath(t, sourceRoot) || identity.WorktreeTopLevel != canonicalTestPath(t, targetRoot) || identity.SourceCommonDir != identity.WorktreeCommonDir || identity.SymbolicHead != "refs/heads/"+taskBranch {
			t.Fatalf("identity = %+v", identity)
		}

		if err := os.WriteFile(filepath.Join(targetRoot, "advanced.txt"), []byte("advanced\n"), 0o644); err != nil {
			t.Fatalf("write advanced commit: %v", err)
		}
		runGit(t, targetRoot, "add", "advanced.txt")
		runGit(t, targetRoot, "commit", "-q", "-m", "advance task branch")
		if _, err := inspector.ValidateManagedWorktreeIdentity(context.Background(), ManagedWorktreeIdentitySpec{
			SourceWorkspaceRoot:  sourceRoot,
			ExpectedWorktreeRoot: targetRoot,
		}); err != nil {
			t.Fatalf("advanced branch identity: %v", err)
		}
	})

	t.Run("classifies unusable roots and repositories", func(t *testing.T) {
		const taskBranch = "task-invalid"
		sourceRoot, targetRoot := newManagedWorktree(t, taskBranch)
		inspector := NewGitInspector(nil)
		validate := func(expectedRoot string) error {
			_, err := inspector.ValidateManagedWorktreeIdentity(context.Background(), ManagedWorktreeIdentitySpec{
				SourceWorkspaceRoot:  sourceRoot,
				ExpectedWorktreeRoot: expectedRoot,
			})
			return err
		}

		assertIdentityError(t, validate(filepath.Join(t.TempDir(), "missing")), ManagedWorktreeIdentityErrorRootMissing)

		notDirectory := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(notDirectory, []byte("file\n"), 0o644); err != nil {
			t.Fatalf("write non-directory root: %v", err)
		}
		assertIdentityError(t, validate(notDirectory), ManagedWorktreeIdentityErrorRootNotDirectory)
		assertIdentityError(t, validate(t.TempDir()), ManagedWorktreeIdentityErrorNotGitWorktree)

		nestedRoot := filepath.Join(targetRoot, "nested")
		if err := os.MkdirAll(nestedRoot, 0o755); err != nil {
			t.Fatalf("MkdirAll nested root: %v", err)
		}
		assertIdentityError(t, validate(nestedRoot), ManagedWorktreeIdentityErrorTopLevelMismatch)

		otherSourceRoot, otherTargetRoot := newManagedWorktree(t, taskBranch)
		_ = otherSourceRoot
		assertIdentityError(t, validate(otherTargetRoot), ManagedWorktreeIdentityErrorSourceRepositoryMismatch)

		runGit(t, targetRoot, "checkout", "-q", "--detach")
		assertIdentityError(t, validate(targetRoot), ManagedWorktreeIdentityErrorDetachedHead)

		sourceRoot, targetRoot = newManagedWorktree(t, taskBranch)
		inspector = NewGitInspector(nil)
		runGit(t, targetRoot, "checkout", "-q", "-b", "other-branch")
		identity, err := inspector.ValidateManagedWorktreeIdentity(context.Background(), ManagedWorktreeIdentitySpec{
			SourceWorkspaceRoot:  sourceRoot,
			ExpectedWorktreeRoot: targetRoot,
		})
		if err != nil {
			t.Fatalf("named branch identity: %v", err)
		}
		if identity.SymbolicHead != "refs/heads/other-branch" {
			t.Fatalf("named branch symbolic head = %q, want other branch", identity.SymbolicHead)
		}

		_, err = inspector.ValidateManagedWorktreeIdentity(context.Background(), ManagedWorktreeIdentitySpec{
			SourceWorkspaceRoot:  filepath.Join(t.TempDir(), "missing-source"),
			ExpectedWorktreeRoot: targetRoot,
		})
		assertIdentityError(t, err, ManagedWorktreeIdentityErrorGitInspectionFailed)
	})

	if runtime.GOOS != "windows" {
		t.Run("classifies inaccessible root", func(t *testing.T) {
			sourceRoot, _ := newManagedWorktree(t, "task-inaccessible")
			parentRoot := t.TempDir()
			targetRoot := filepath.Join(parentRoot, "locked")
			if err := os.MkdirAll(targetRoot, 0o700); err != nil {
				t.Fatalf("MkdirAll inaccessible root: %v", err)
			}
			if err := os.Chmod(parentRoot, 0o000); err != nil {
				t.Fatalf("Chmod inaccessible parent: %v", err)
			}
			defer func() {
				if err := os.Chmod(parentRoot, 0o700); err != nil {
					t.Fatalf("restore inaccessible parent permissions: %v", err)
				}
			}()
			_, err := NewGitInspector(nil).ValidateManagedWorktreeIdentity(context.Background(), ManagedWorktreeIdentitySpec{
				SourceWorkspaceRoot:  sourceRoot,
				ExpectedWorktreeRoot: targetRoot,
			})
			assertIdentityError(t, err, ManagedWorktreeIdentityErrorRootInaccessible)
		})
	}
}

func TestGitInspectorResolutionPropagatesCancellationAndClassifiesCommandFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewGitInspector(canceledGitCommandRunner{}).ResolveRevision(ctx, t.TempDir(), "HEAD")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ResolveRevision error = %v, want context canceled", err)
	}

	runner := &stubGitCommandRunner{
		err:      errors.New("git unavailable"),
		exitCode: -1,
	}
	_, err = NewGitInspector(runner).ResolveRevision(context.Background(), t.TempDir(), "HEAD")
	var resolutionErr *GitRevisionResolutionError
	if !errors.As(err, &resolutionErr) || resolutionErr.Kind != GitRevisionResolutionErrorGitFailure || !errors.Is(err, runner.err) {
		t.Fatalf("failed ResolveRevision error = %v, want typed git failure that wraps command error", err)
	}

	_, err = NewGitInspector(canceledGitCommandRunner{}).ResolveDefaultBranch(ctx, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ResolveDefaultBranch error = %v, want context canceled", err)
	}

	_, err = NewGitInspector(runner).ResolveDefaultBranch(context.Background(), t.TempDir())
	var defaultBranchErr *GitDefaultBranchResolutionError
	if !errors.As(err, &defaultBranchErr) || defaultBranchErr.Kind != GitDefaultBranchResolutionErrorGitFailure || !errors.Is(err, runner.err) {
		t.Fatalf("failed ResolveDefaultBranch error = %v, want typed git failure that wraps command error", err)
	}
}

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err == nil {
		return filepath.Clean(canonical)
	}
	abs, absErr := filepath.Abs(path)
	if absErr != nil {
		t.Fatalf("abs path %q: %v", path, absErr)
	}
	return filepath.Clean(abs)
}

func stringPointer(value string) *string {
	return &value
}

func equalStrings(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
