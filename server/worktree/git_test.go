package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
)

type canceledGitCommandRunner struct{}

func (canceledGitCommandRunner) Output(context.Context, string, ...string) ([]byte, error) {
	return nil, context.Canceled
}

func (canceledGitCommandRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, int, error) {
	return nil, -1, ctx.Err()
}

type stubGitCommandResult struct {
	output   []byte
	err      error
	exitCode int
}

type stubGitCommandRunner struct {
	output   []byte
	err      error
	exitCode int
	dir      string
	args     []string
	results  map[string]stubGitCommandResult
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
	output := append([]byte(nil), s.output...)
	err := s.err
	exitCode := s.exitCode
	if specific, ok := s.results[gitCommandKey(args...)]; ok {
		output = append([]byte(nil), specific.output...)
		err = specific.err
		exitCode = specific.exitCode
	}
	if err != nil && exitCode == 0 {
		exitCode = 1
	}
	return output, exitCode, err
}

func gitCommandKey(args ...string) string { return strings.Join(args, "\x00") }

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
	if !mainEntry.IsMainWorktree || mainEntry.Branch == nil || mainEntry.Branch.Name() != "main" || mainEntry.Root != canonicalTestPath(t, workspaceRoot) {
		t.Fatalf("unexpected main entry: %+v", mainEntry)
	}
	linkedEntry := entries[1]
	if linkedEntry.IsMainWorktree || linkedEntry.Branch == nil || linkedEntry.Branch.Ref() != "refs/heads/feature/worktree" || linkedEntry.Branch.Name() != "feature/worktree" || linkedEntry.LockedReason != "bootstrap running" {
		t.Fatalf("unexpected linked entry: %+v", linkedEntry)
	}
	prunableEntry := entries[2]
	if !prunableEntry.Detached || prunableEntry.Branch != nil || prunableEntry.PrunableReason == "" || prunableEntry.Root != canonicalTestPath(t, prunableRoot) {
		t.Fatalf("unexpected prunable entry: %+v", prunableEntry)
	}
}

func TestParseGitWorktreeListPorcelainIdentifiesGitMainOnlyFromRecordZero(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantRoots  []string
		wantIsMain []bool
	}{
		{
			name: "non-bare record zero",
			body: "worktree /repo/main\nHEAD aaa111\nbranch refs/heads/main\n\n" +
				"worktree /repo/linked\nHEAD bbb222\nbranch refs/heads/feature\n",
			wantRoots:  []string{"/repo/main", "/repo/linked"},
			wantIsMain: []bool{true, false},
		},
		{
			name: "bare record zero has no Git main worktree",
			body: "worktree /repo/bare.git\nHEAD aaa111\nbare\n\n" +
				"worktree /repo/linked\nHEAD bbb222\nbranch refs/heads/feature\n",
			wantRoots:  []string{"/repo/bare.git", "/repo/linked"},
			wantIsMain: []bool{false, false},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entries, err := parseGitWorktreeListPorcelain(test.body)
			if err != nil {
				t.Fatalf("parseGitWorktreeListPorcelain: %v", err)
			}
			if len(entries) != len(test.wantRoots) {
				t.Fatalf("entries = %+v, want %d entries", entries, len(test.wantRoots))
			}
			for index, entry := range entries {
				if entry.Root != canonicalTestPath(t, test.wantRoots[index]) ||
					entry.IsMainWorktree != test.wantIsMain[index] {
					t.Fatalf("entry %d = %+v, want root %q and is_main_worktree=%t",
						index, entry, test.wantRoots[index], test.wantIsMain[index])
				}
			}
		})
	}
}

func TestForceRemovePrunableWorktreeRejectsSymlinkedRegistrationParent(t *testing.T) {
	workspaceRoot := t.TempDir()
	initGitRepo(t, workspaceRoot)
	worktreeRoot := filepath.Join(t.TempDir(), "target")
	runGit(t, workspaceRoot, "worktree", "add", "-q", "-b", "feature/prunable-symlink", worktreeRoot, "HEAD")

	commonDir := strings.TrimSpace(runGit(t, workspaceRoot, "rev-parse", "--git-common-dir"))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(workspaceRoot, commonDir)
	}
	worktreesRoot := filepath.Join(canonicalTestPath(t, commonDir), "worktrees")
	entries, err := os.ReadDir(worktreesRoot)
	if err != nil || len(entries) != 1 {
		t.Fatalf("worktree registrations = %+v, err=%v", entries, err)
	}
	externalWorktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	if err := os.Rename(worktreesRoot, externalWorktreesRoot); err != nil {
		t.Fatalf("move worktree registrations: %v", err)
	}
	if err := os.Symlink(externalWorktreesRoot, worktreesRoot); err != nil {
		t.Fatalf("symlink worktree registrations: %v", err)
	}
	if err := os.Remove(filepath.Join(worktreeRoot, ".git")); err != nil {
		t.Fatalf("remove worktree git marker: %v", err)
	}
	sentinel := filepath.Join(externalWorktreesRoot, entries[0].Name(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0o644); err != nil {
		t.Fatalf("write external sentinel: %v", err)
	}

	err = NewGitInspector(nil).ForceRemovePrunableWorktree(context.Background(), workspaceRoot, worktreeRoot)
	var recoveryError *PrunableWorktreeRecoveryError
	if !errors.As(err, &recoveryError) || recoveryError.Destructive {
		t.Fatalf("cleanup error = %v, want non-destructive recovery error", err)
	}
	if _, err := os.Stat(worktreeRoot); err != nil {
		t.Fatalf("worktree root changed after rejected cleanup: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("external registration changed after rejected cleanup: %v", err)
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
	_, err := parseGitWorktreeListPorcelain("worktree " + workspaceRoot + "\nHEAD aaa111\nunsupported nope\n")
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestParseGitWorktreeListPorcelainRejectsNamedDetachedHead(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	for name, body := range map[string]string{
		"branch_then_detached": "worktree " + workspaceRoot + "\nHEAD aaa111\nbranch refs/heads/main\ndetached\n",
		"detached_then_branch": "worktree " + workspaceRoot + "\nHEAD aaa111\ndetached\nbranch refs/heads/main\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGitWorktreeListPorcelain(body); err == nil {
				t.Fatal("parseGitWorktreeListPorcelain accepted named detached head")
			}
		})
	}
}

func TestGitInspectorAdd(t *testing.T) {
	tests := []struct {
		name           string
		spec           CreateSpec
		argsBeforeRoot []string
		argsAfterRoot  []string
		created        bool
	}{
		{"creates branch from explicit base ref", CreateSpec{BaseRef: "HEAD", CreateBranch: true, BranchName: "feature/new"}, []string{"worktree", "add", "-b", "feature/new"}, []string{"HEAD"}, true},
		{"uses existing ref without creating branch", CreateSpec{BaseRef: "feature/existing"}, []string{"worktree", "add"}, []string{"feature/existing"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspaceRoot := filepath.Join(t.TempDir(), "workspace")
			worktreeRoot := filepath.Join(t.TempDir(), "linked")
			runner := &stubGitCommandRunner{}
			created, err := NewGitInspector(runner).Add(context.Background(), workspaceRoot, worktreeRoot, tt.spec)
			if err != nil {
				t.Fatalf("Add: %v", err)
			}
			if created != tt.created {
				t.Fatalf("created branch = %t, want %t", created, tt.created)
			}
			wantArgs := append(append(slices.Clone(tt.argsBeforeRoot), canonicalTestPath(t, worktreeRoot)), tt.argsAfterRoot...)
			if !slices.Equal(runner.args, wantArgs) {
				t.Fatalf("git args=%v want=%v", runner.args, wantArgs)
			}
			if got, want := runner.dir, canonicalTestPath(t, workspaceRoot); got != want {
				t.Fatalf("git dir=%q want=%q", got, want)
			}
		})
	}
}

func TestGitInspectorProbeDirtyStateCountsMixedStatusWithoutDuplicateRenameOrCopyEntries(t *testing.T) {
	worktreeRoot := filepath.Join(t.TempDir(), "linked")
	runner := &stubGitCommandRunner{output: []byte(" M changed.go\x00?? new.go\x00R  renamed.go\x00old.go\x00C  copied.go\x00source.go\x00")}
	inspector := NewGitInspector(runner)

	state, err := inspector.ProbeDirtyState(context.Background(), worktreeRoot)
	if err != nil {
		t.Fatalf("ProbeDirtyState: %v", err)
	}
	if state.Kind != worktreepb.DirtyStateKind_DIRTY_STATE_DIRTY || state.DirtyFileCount == nil || *state.DirtyFileCount != 4 {
		t.Fatalf("dirty state = %+v, want dirty count 4", state)
	}
	if got, want := runner.args, []string{"status", "--porcelain=v1", "-z"}; !slices.Equal(got, want) {
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
	if err != nil || state.Kind != worktreepb.DirtyStateKind_DIRTY_STATE_CLEAN || state.DirtyFileCount != nil {
		t.Fatalf("clean state = %+v err=%v", state, err)
	}

	dirty := NewGitInspector(&stubGitCommandRunner{output: []byte(" M changed.go\x00")})
	state, err = dirty.ProbeDirtyState(context.Background(), root)
	if err != nil || state.Kind != worktreepb.DirtyStateKind_DIRTY_STATE_DIRTY || state.DirtyFileCount == nil || *state.DirtyFileCount != 1 {
		t.Fatalf("dirty state = %+v err=%v", state, err)
	}

	unknown := NewGitInspector(&stubGitCommandRunner{err: errors.New("status unavailable"), exitCode: 1})
	state, err = unknown.ProbeDirtyState(context.Background(), root)
	if err != nil || state.Kind != worktreepb.DirtyStateKind_DIRTY_STATE_UNKNOWN || state.DirtyFileCount != nil || state.UnknownCause == nil {
		t.Fatalf("unknown state = %+v err=%v", state, err)
	}
}

func TestGitInspectorInspectsTargetIdentityAndExactRef(t *testing.T) {
	root := t.TempDir()
	commonDir := filepath.Join(root, ".git")
	if err := os.Mkdir(commonDir, 0o755); err != nil {
		t.Fatalf("Mkdir Git marker: %v", err)
	}
	runner := &stubGitCommandRunner{results: map[string]stubGitCommandResult{
		gitCommandKey("rev-parse", "--show-toplevel"):  {output: []byte(root + "\n")},
		gitCommandKey("rev-parse", "--git-common-dir"): {output: []byte(commonDir + "\n")},
	}}
	inspector := NewGitInspector(runner)
	inspection, err := inspector.InspectTarget(context.Background(), root)
	if err != nil {
		t.Fatalf("InspectTarget: %v", err)
	}
	if inspection.Identity.TopLevelRoot != canonicalTestPath(t, root) || inspection.Identity.CommonDir != canonicalTestPath(t, commonDir) {
		t.Fatalf("inspection = %+v", inspection)
	}

	runner.results = map[string]stubGitCommandResult{
		gitCommandKey("rev-parse", "--verify", "--quiet", "refs/heads/feature/*^{object}"): {err: errors.New("exit status 1"), exitCode: 1},
	}
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
	if got, want := runner.args, []string{"worktree", "remove", "--force", canonicalTestPath(t, worktreeRoot)}; !slices.Equal(got, want) {
		t.Fatalf("git args=%v want=%v", got, want)
	}
}

func TestGitInspectorResolveCreateTarget(t *testing.T) {
	tests := []struct {
		name, target string
		results      map[string]stubGitCommandResult
		kind         CreateTargetResolutionKind
		resolvedRef  string
		invalid      bool
	}{
		{
			name: "existing branch", target: "main",
			results: map[string]stubGitCommandResult{
				gitCommandKey("rev-parse", "--verify", "--quiet", "refs/heads/main^{object}"): {output: []byte("abc123\n")},
			},
			kind: CreateTargetResolutionKindExistingBranch, resolvedRef: "main",
		},
		{
			name: "prefix-only branch", target: "feature",
			results: map[string]stubGitCommandResult{
				gitCommandKey("rev-parse", "--verify", "--quiet", "refs/heads/feature^{object}"): {err: errors.New("exit status 1"), exitCode: 1},
				gitCommandKey("rev-parse", "--verify", "--quiet", "feature^{object}"):            {err: errors.New("exit status 1"), exitCode: 1},
			},
			kind: CreateTargetResolutionKindNewBranch,
		},
		{
			name: "detached ref", target: "HEAD",
			results: map[string]stubGitCommandResult{
				gitCommandKey("rev-parse", "--verify", "--quiet", "refs/heads/HEAD^{object}"): {err: errors.New("exit status 1"), exitCode: 1},
				gitCommandKey("rev-parse", "--verify", "--quiet", "HEAD^{object}"):            {output: []byte("abc123\n")},
			},
			kind: CreateTargetResolutionKindDetachedRef, resolvedRef: "abc123",
		},
		{
			name: "new branch", target: "feature/new",
			results: map[string]stubGitCommandResult{
				gitCommandKey("rev-parse", "--verify", "--quiet", "refs/heads/feature/new^{object}"): {err: errors.New("exit status 1"), exitCode: 1},
				gitCommandKey("rev-parse", "--verify", "--quiet", "feature/new^{object}"):            {err: errors.New("exit status 1"), exitCode: 1},
			},
			kind: CreateTargetResolutionKindNewBranch,
		},
		{
			name: "invalid branch", target: "feature..bad",
			results: map[string]stubGitCommandResult{
				gitCommandKey("check-ref-format", "refs/heads/feature..bad"):               {err: errors.New("exit status 1"), exitCode: 1},
				gitCommandKey("rev-parse", "--verify", "--quiet", "feature..bad^{object}"): {err: errors.New("exit status 1"), exitCode: 1},
			},
			invalid: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolution, err := NewGitInspector(&stubGitCommandRunner{results: tt.results}).ResolveCreateTarget(
				context.Background(),
				filepath.Join(t.TempDir(), "workspace"),
				tt.target,
			)
			if tt.invalid {
				var invalidTarget *InvalidCreateTargetError
				if !errors.As(err, &invalidTarget) || invalidTarget.Target != tt.target {
					t.Fatalf("ResolveCreateTarget error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveCreateTarget: %v", err)
			}
			if resolution.Input != tt.target || resolution.Kind != tt.kind || resolution.ResolvedRef != tt.resolvedRef {
				t.Fatalf("resolution = %+v, want input %q kind %q ref %q", resolution, tt.target, tt.kind, tt.resolvedRef)
			}
		})
	}
}

func TestGitInspectorBranchExistsUsesExactRefLookup(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	runner := &stubGitCommandRunner{results: map[string]stubGitCommandResult{
		gitCommandKey("rev-parse", "--verify", "--quiet", "refs/heads/feature/*^{object}"): {err: errors.New("exit status 1"), exitCode: 1},
	}}
	inspector := NewGitInspector(runner)
	exists, err := inspector.BranchExists(context.Background(), workspaceRoot, "feature/*")
	if err != nil {
		t.Fatalf("BranchExists: %v", err)
	}
	if exists {
		t.Fatal("expected exact-ref lookup to treat glob-like branch as absent")
	}
	if got, want := runner.args, []string{"rev-parse", "--verify", "--quiet", "refs/heads/feature/*^{object}"}; !slices.Equal(got, want) {
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

func TestGitInspectorInspectProspectiveInitialTaskBranchRejectsInvalidName(t *testing.T) {
	workspaceRoot := t.TempDir()
	initGitRepo(t, workspaceRoot)

	err := NewGitInspector(nil).InspectProspectiveInitialTaskBranch(
		context.Background(),
		workspaceRoot,
		"feature..invalid",
	)

	var branchErr *InitialTaskBranchError
	if !errors.As(err, &branchErr) {
		t.Fatalf("error = %T %v, want InitialTaskBranchError", err, err)
	}
	if branchErr.Kind != InitialTaskBranchErrorInvalidName ||
		branchErr.BranchName != "feature..invalid" ||
		branchErr.Ref != nil ||
		branchErr.Remote != nil {
		t.Fatalf("initial Task branch error = %+v", branchErr)
	}
}

func TestGitInspectorInspectProspectiveInitialTaskBranchRejectsCheckoutShorthand(t *testing.T) {
	workspaceRoot := t.TempDir()
	initGitRepo(t, workspaceRoot)
	runGit(t, workspaceRoot, "switch", "-c", "feature/previous-checkout")
	runGit(t, workspaceRoot, "switch", "-")

	err := NewGitInspector(nil).InspectProspectiveInitialTaskBranch(
		context.Background(),
		workspaceRoot,
		"@{-1}",
	)

	var branchErr *InitialTaskBranchError
	if !errors.As(err, &branchErr) {
		t.Fatalf("error = %T %v, want InitialTaskBranchError", err, err)
	}
	if branchErr.Kind != InitialTaskBranchErrorInvalidName ||
		branchErr.BranchName != "@{-1}" ||
		branchErr.Ref != nil ||
		branchErr.Remote != nil {
		t.Fatalf("initial Task branch error = %+v", branchErr)
	}
}

func TestGitInspectorInspectProspectiveInitialTaskBranchRejectsUncreatableNames(t *testing.T) {
	workspaceRoot := t.TempDir()
	initGitRepo(t, workspaceRoot)

	for _, branchName := range []string{"HEAD", "-leading-dash"} {
		t.Run(branchName, func(t *testing.T) {
			err := NewGitInspector(nil).InspectProspectiveInitialTaskBranch(
				context.Background(),
				workspaceRoot,
				branchName,
			)

			var branchErr *InitialTaskBranchError
			if !errors.As(err, &branchErr) {
				t.Fatalf("error = %T %v, want InitialTaskBranchError", err, err)
			}
			if branchErr.Kind != InitialTaskBranchErrorInvalidName ||
				branchErr.BranchName != branchName ||
				branchErr.Ref != nil ||
				branchErr.Remote != nil {
				t.Fatalf("initial Task branch error = %+v", branchErr)
			}
		})
	}
}

func TestGitInspectorInspectProspectiveInitialTaskBranchFindsExactCollisions(t *testing.T) {
	workspaceRoot := t.TempDir()
	initGitRepo(t, workspaceRoot)
	commitOID := runGit(t, workspaceRoot, "rev-parse", "HEAD")
	runGit(t, workspaceRoot, "branch", "feature/local")
	runGit(t, workspaceRoot, "-c", "tag.gpgSign=false", "tag", "feature/tag")
	runGit(t, workspaceRoot, "remote", "add", "upstream", "https://example.invalid/upstream.git")
	runGit(t, workspaceRoot, "update-ref", "refs/remotes/upstream/feature/remote", commitOID)

	runner := &recordingGitCommandRunner{delegate: execGitCommandRunner{}}
	inspector := NewGitInspector(runner)
	tests := []struct {
		branchName string
		kind       *InitialTaskBranchErrorKind
		ref        *string
		remote     *string
	}{
		{
			branchName: "feature/local",
			kind:       initialTaskBranchErrorKindPointer(InitialTaskBranchErrorLocalCollision),
			ref:        stringPointer("refs/heads/feature/local"),
		},
		{
			branchName: "feature/remote",
			kind:       initialTaskBranchErrorKindPointer(InitialTaskBranchErrorRemoteTrackingCollision),
			ref:        stringPointer("refs/remotes/upstream/feature/remote"),
			remote:     stringPointer("upstream"),
		},
		{branchName: "feature/tag"},
		{branchName: "feature/new"},
	}
	for _, test := range tests {
		t.Run(test.branchName, func(t *testing.T) {
			runner.resetCalls()
			err := inspector.InspectProspectiveInitialTaskBranch(context.Background(), workspaceRoot, test.branchName)
			if test.kind == nil {
				if err != nil {
					t.Fatalf("InspectProspectiveInitialTaskBranch: %v", err)
				}
			} else {
				var branchErr *InitialTaskBranchError
				if !errors.As(err, &branchErr) {
					t.Fatalf("error = %T %v, want InitialTaskBranchError", err, err)
				}
				if branchErr.Kind != *test.kind ||
					branchErr.BranchName != test.branchName ||
					!reflectOptionalStringEqual(branchErr.Ref, test.ref) ||
					!reflectOptionalStringEqual(branchErr.Remote, test.remote) {
					t.Fatalf("initial Task branch error = %+v", branchErr)
				}
			}
			for _, call := range runner.calls {
				if len(call) == 0 {
					continue
				}
				switch call[0] {
				case "fetch", "ls-remote", "push":
					t.Fatalf("prospective branch inspection contacted a remote: %v", call)
				}
			}
		})
	}

	noRemoteRoot := t.TempDir()
	initGitRepo(t, noRemoteRoot)
	if err := inspector.InspectProspectiveInitialTaskBranch(context.Background(), noRemoteRoot, "feature/no-remotes"); err != nil {
		t.Fatalf("branch in repository without remotes rejected: %v", err)
	}
}

func initialTaskBranchErrorKindPointer(value InitialTaskBranchErrorKind) *InitialTaskBranchErrorKind {
	return &value
}

func reflectOptionalStringEqual(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
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

func TestGitInspectorResolveRevisionCommitStopsAfterCommitOID(t *testing.T) {
	workspaceRoot := t.TempDir()
	commitOID := "abc123"
	runner := &stubGitCommandRunner{results: map[string]stubGitCommandResult{
		gitCommandKey("rev-parse", "--verify", "--quiet", "HEAD^{object}"): {output: []byte("object\n")},
		gitCommandKey("rev-parse", "--verify", "--quiet", "HEAD^{commit}"): {output: []byte(commitOID + "\n")},
	}}

	resolved, err := NewGitInspector(runner).ResolveRevisionCommit(context.Background(), workspaceRoot, "HEAD")
	if err != nil {
		t.Fatalf("ResolveRevisionCommit: %v", err)
	}
	if resolved.RequestedRef != "HEAD" || resolved.CommitOID != commitOID || resolved.CanonicalRef != nil {
		t.Fatalf("resolved revision = %+v, want commit-only resolution", resolved)
	}
	if slices.Equal(runner.args, []string{"rev-parse", "--symbolic-full-name", "--verify", "--quiet", "HEAD"}) {
		t.Fatalf("ResolveRevisionCommit followed moving ref after commit capture: %v", runner.args)
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
		branchName, named := identity.NamedBranch()
		if identity.SourceTopLevel != canonicalTestPath(t, sourceRoot) ||
			identity.WorktreeTopLevel != canonicalTestPath(t, targetRoot) ||
			identity.SourceCommonDir != identity.WorktreeCommonDir ||
			!named ||
			branchName != taskBranch {
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
		detachedIdentity, err := inspector.ValidateManagedWorktreeIdentity(context.Background(), ManagedWorktreeIdentitySpec{
			SourceWorkspaceRoot:  sourceRoot,
			ExpectedWorktreeRoot: targetRoot,
		})
		if err != nil {
			t.Fatalf("detached identity: %v", err)
		}
		if branchName, named := detachedIdentity.NamedBranch(); named {
			t.Fatalf("detached branch name = %q, want absent", branchName)
		}

		sourceRoot, targetRoot = newManagedWorktree(t, taskBranch)
		inspector = NewGitInspector(nil)
		const remoteHead = "refs/remotes/origin/main"
		runGit(t, targetRoot, "update-ref", remoteHead, "HEAD")
		runGit(t, targetRoot, "symbolic-ref", "HEAD", remoteHead)
		assertIdentityError(t, validate(targetRoot), ManagedWorktreeIdentityErrorGitInspectionFailed)

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
		if branchName, named := identity.NamedBranch(); !named || branchName != "other-branch" {
			t.Fatalf("named branch = %q/%v, want other branch", branchName, named)
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
