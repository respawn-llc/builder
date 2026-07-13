package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
