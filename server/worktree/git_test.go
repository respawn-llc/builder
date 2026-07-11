package worktree

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"core/server/workflow"
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

func TestGitInspectorResolveExecutionTargetUsesTypedGitFacts(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	key := func(args ...string) string { return strings.Join(args, "\x00") }
	gitRepository := map[string][]byte{
		key("rev-parse", "--is-inside-work-tree"): []byte("true\n"),
	}

	t.Run("symbolic head", func(t *testing.T) {
		runner := &stubGitCommandRunner{outputs: map[string][]byte{
			key("rev-parse", "--is-inside-work-tree"):                gitRepository[key("rev-parse", "--is-inside-work-tree")],
			key("symbolic-ref", "--quiet", "HEAD"):                   []byte("refs/heads/main\n"),
			key("rev-parse", "--verify", "--quiet", "HEAD^{commit}"): []byte("01cafe\n"),
		}}
		resolution, err := NewGitInspector(runner).ResolveExecutionTarget(context.Background(), workspaceRoot, workflow.ExecutionPolicyHead, nil)
		if err != nil {
			t.Fatalf("ResolveExecutionTarget: %v", err)
		}
		if resolution.Source.Kind != workflow.ExecutionTargetSourceNamedRef ||
			resolution.Source.NamedRef == nil ||
			*resolution.Source.NamedRef != "refs/heads/main" ||
			resolution.Source.Commit != "01cafe" {
			t.Fatalf("HEAD resolution = %+v, want symbolic named source", resolution)
		}
	})

	t.Run("detached head", func(t *testing.T) {
		runner := &stubGitCommandRunner{
			outputs: map[string][]byte{
				key("rev-parse", "--is-inside-work-tree"):                gitRepository[key("rev-parse", "--is-inside-work-tree")],
				key("rev-parse", "--verify", "--quiet", "HEAD^{commit}"): []byte("02cafe\n"),
			},
			errors: map[string]error{
				key("symbolic-ref", "--quiet", "HEAD"): errors.New("exit status 1"),
			},
			exitCodes: map[string]int{
				key("symbolic-ref", "--quiet", "HEAD"): 1,
			},
		}
		resolution, err := NewGitInspector(runner).ResolveExecutionTarget(context.Background(), workspaceRoot, workflow.ExecutionPolicyHead, nil)
		if err != nil {
			t.Fatalf("ResolveExecutionTarget: %v", err)
		}
		if resolution.Source.Kind != workflow.ExecutionTargetSourceDetachedCommit ||
			resolution.Source.NamedRef != nil ||
			resolution.Source.Commit != "02cafe" {
			t.Fatalf("detached HEAD resolution = %+v, want detached source", resolution)
		}
	})

	t.Run("default branch uses upstream remote", func(t *testing.T) {
		runner := &stubGitCommandRunner{outputs: map[string][]byte{
			key("rev-parse", "--is-inside-work-tree"):                                            gitRepository[key("rev-parse", "--is-inside-work-tree")],
			key("symbolic-ref", "--quiet", "HEAD"):                                               []byte("refs/heads/feature/current\n"),
			key("for-each-ref", "--format=%(upstream:remotename)", "refs/heads/feature/current"): []byte("upstream\n"),
			key("symbolic-ref", "--quiet", "refs/remotes/upstream/HEAD"):                         []byte("refs/remotes/upstream/trunk\n"),
			key("rev-parse", "--verify", "--quiet", "refs/remotes/upstream/trunk^{commit}"):      []byte("03cafe\n"),
		}}
		resolution, err := NewGitInspector(runner).ResolveExecutionTarget(context.Background(), workspaceRoot, workflow.ExecutionPolicyDefaultBranch, nil)
		if err != nil {
			t.Fatalf("ResolveExecutionTarget: %v", err)
		}
		if resolution.Source.Kind != workflow.ExecutionTargetSourceNamedRef ||
			resolution.Source.NamedRef == nil ||
			*resolution.Source.NamedRef != "refs/remotes/upstream/trunk" ||
			resolution.Source.Commit != "03cafe" {
			t.Fatalf("default branch resolution = %+v, want upstream default pointer", resolution)
		}
	})

	t.Run("default branch falls back to origin", func(t *testing.T) {
		runner := &stubGitCommandRunner{outputs: map[string][]byte{
			key("rev-parse", "--is-inside-work-tree"):                                    gitRepository[key("rev-parse", "--is-inside-work-tree")],
			key("symbolic-ref", "--quiet", "HEAD"):                                       []byte("refs/heads/current\n"),
			key("for-each-ref", "--format=%(upstream:remotename)", "refs/heads/current"): nil,
			key("symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"):                   []byte("refs/remotes/origin/main\n"),
			key("rev-parse", "--verify", "--quiet", "refs/remotes/origin/main^{commit}"): []byte("04cafe\n"),
		}}
		resolution, err := NewGitInspector(runner).ResolveExecutionTarget(context.Background(), workspaceRoot, workflow.ExecutionPolicyDefaultBranch, nil)
		if err != nil {
			t.Fatalf("ResolveExecutionTarget: %v", err)
		}
		if resolution.Source.NamedRef == nil || *resolution.Source.NamedRef != "refs/remotes/origin/main" || resolution.Source.Commit != "04cafe" {
			t.Fatalf("origin default branch resolution = %+v, want origin default pointer", resolution)
		}
	})

	t.Run("custom ref records canonical named ref", func(t *testing.T) {
		requestedRef := "release"
		runner := &stubGitCommandRunner{outputs: map[string][]byte{
			key("rev-parse", "--is-inside-work-tree"):                       gitRepository[key("rev-parse", "--is-inside-work-tree")],
			key("rev-parse", "--verify", "--quiet", "release^{commit}"):     []byte("05cafe\n"),
			key("rev-parse", "--symbolic-full-name", "--verify", "release"): []byte("refs/heads/release\n"),
		}}
		resolution, err := NewGitInspector(runner).ResolveExecutionTarget(context.Background(), workspaceRoot, workflow.ExecutionPolicyCustomRef, &requestedRef)
		if err != nil {
			t.Fatalf("ResolveExecutionTarget: %v", err)
		}
		if resolution.Source.Kind != workflow.ExecutionTargetSourceNamedRef ||
			resolution.Source.NamedRef == nil ||
			*resolution.Source.NamedRef != "refs/heads/release" ||
			resolution.Source.Commit != "05cafe" {
			t.Fatalf("custom resolution = %+v, want canonical named source", resolution)
		}
	})
}

func TestGitInspectorResolveExecutionTargetReportsTypedResolutionFailures(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	key := func(args ...string) string { return strings.Join(args, "\x00") }

	t.Run("non Git workspace", func(t *testing.T) {
		runner := &stubGitCommandRunner{
			errors: map[string]error{
				key("rev-parse", "--is-inside-work-tree"): errors.New("exit status 128"),
			},
			exitCodes: map[string]int{
				key("rev-parse", "--is-inside-work-tree"): 128,
			},
		}
		_, err := NewGitInspector(runner).ResolveExecutionTarget(context.Background(), workspaceRoot, workflow.ExecutionPolicyHead, nil)
		assertExecutionTargetResolutionFailure(t, err, ExecutionTargetResolutionFailureNonGit)
	})

	t.Run("unavailable default pointer", func(t *testing.T) {
		runner := &stubGitCommandRunner{
			outputs: map[string][]byte{
				key("rev-parse", "--is-inside-work-tree"):                                    []byte("true\n"),
				key("symbolic-ref", "--quiet", "HEAD"):                                       []byte("refs/heads/current\n"),
				key("for-each-ref", "--format=%(upstream:remotename)", "refs/heads/current"): nil,
			},
			errors: map[string]error{
				key("symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"): errors.New("exit status 1"),
			},
			exitCodes: map[string]int{
				key("symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"): 1,
			},
		}
		_, err := NewGitInspector(runner).ResolveExecutionTarget(context.Background(), workspaceRoot, workflow.ExecutionPolicyDefaultBranch, nil)
		assertExecutionTargetResolutionFailure(t, err, ExecutionTargetResolutionFailureUnavailable)
	})

	t.Run("missing custom ref", func(t *testing.T) {
		requestedRef := "missing"
		runner := &stubGitCommandRunner{
			outputs: map[string][]byte{
				key("rev-parse", "--is-inside-work-tree"): []byte("true\n"),
			},
			errors: map[string]error{
				key("rev-parse", "--verify", "--quiet", "missing^{commit}"): errors.New("exit status 1"),
			},
			exitCodes: map[string]int{
				key("rev-parse", "--verify", "--quiet", "missing^{commit}"): 1,
			},
		}
		_, err := NewGitInspector(runner).ResolveExecutionTarget(context.Background(), workspaceRoot, workflow.ExecutionPolicyCustomRef, &requestedRef)
		assertExecutionTargetResolutionFailure(t, err, ExecutionTargetResolutionFailureInvalidRef)
	})
}

func TestGitInspectorCommonDirectoryUsesCanonicalGitIdentity(t *testing.T) {
	workspaceRoot := t.TempDir()
	commonDir := t.TempDir()
	runner := &stubGitCommandRunner{outputs: map[string][]byte{
		strings.Join([]string{"rev-parse", "--path-format=absolute", "--git-common-dir"}, "\x00"): []byte(commonDir + "\n"),
	}}

	actual, err := NewGitInspector(runner).CommonDirectory(context.Background(), workspaceRoot)
	if err != nil {
		t.Fatalf("CommonDirectory: %v", err)
	}
	if actual != canonicalTestPath(t, commonDir) {
		t.Fatalf("common directory = %q, want %q", actual, canonicalTestPath(t, commonDir))
	}
}

func TestGitInspectorResolveExecutionTargetAgainstGitRepository(t *testing.T) {
	workspaceRoot := t.TempDir()
	initGitRepo(t, workspaceRoot)
	commit := strings.TrimSpace(runGit(t, workspaceRoot, "rev-parse", "HEAD"))
	runGit(t, workspaceRoot, "update-ref", "refs/remotes/origin/main", commit)
	runGit(t, workspaceRoot, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	inspector := NewGitInspector(nil)

	head, err := inspector.ResolveExecutionTarget(context.Background(), workspaceRoot, workflow.ExecutionPolicyHead, nil)
	if err != nil {
		t.Fatalf("ResolveExecutionTarget HEAD: %v", err)
	}
	if head.Source.Kind != workflow.ExecutionTargetSourceNamedRef ||
		head.Source.NamedRef == nil ||
		!strings.HasPrefix(*head.Source.NamedRef, "refs/heads/") ||
		head.Source.Commit != commit {
		t.Fatalf("HEAD resolution = %+v, want source branch and commit %q", head, commit)
	}

	defaultBranch, err := inspector.ResolveExecutionTarget(context.Background(), workspaceRoot, workflow.ExecutionPolicyDefaultBranch, nil)
	if err != nil {
		t.Fatalf("ResolveExecutionTarget default branch: %v", err)
	}
	if defaultBranch.Source.NamedRef == nil ||
		*defaultBranch.Source.NamedRef != "refs/remotes/origin/main" ||
		defaultBranch.Source.Commit != commit {
		t.Fatalf("default branch resolution = %+v, want origin default pointer and commit %q", defaultBranch, commit)
	}

	customRef := "HEAD"
	custom, err := inspector.ResolveExecutionTarget(context.Background(), workspaceRoot, workflow.ExecutionPolicyCustomRef, &customRef)
	if err != nil {
		t.Fatalf("ResolveExecutionTarget custom HEAD: %v", err)
	}
	if custom.Source.Kind != workflow.ExecutionTargetSourceNamedRef ||
		custom.Source.NamedRef == nil ||
		*custom.Source.NamedRef != *head.Source.NamedRef ||
		custom.Source.Commit != commit {
		t.Fatalf("custom HEAD resolution = %+v, want canonical HEAD source %+v", custom, head.Source)
	}
}

func assertExecutionTargetResolutionFailure(t *testing.T, err error, want ExecutionTargetResolutionFailureKind) {
	t.Helper()
	var failure *ExecutionTargetResolutionError
	if !errors.As(err, &failure) || failure.Kind != want {
		t.Fatalf("resolution error = %v, want typed failure %q", err, want)
	}
}

func TestGitInspectorBranchExistsUsesExactRefLookup(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	runner := &stubGitCommandRunner{
		errors: map[string]error{
			strings.Join([]string{"show-ref", "--verify", "--quiet", "refs/heads/feature/*"}, "\x00"): errors.New("exit status 1"),
		},
		exitCodes: map[string]int{
			strings.Join([]string{"show-ref", "--verify", "--quiet", "refs/heads/feature/*"}, "\x00"): 1,
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
	if got, want := runner.args, []string{"show-ref", "--verify", "--quiet", "refs/heads/feature/*"}; !equalStrings(got, want) {
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
