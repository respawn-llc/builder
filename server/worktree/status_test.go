package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/server/metadata/sqlitegen"
	"core/shared/config"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"

	"google.golang.org/protobuf/encoding/protojson"
)

func updateWorktreeStatusTarget(t *testing.T, env *serviceTestEnv, worktreeID string, cwdRelpath string) {
	t.Helper()
	updateServiceTestSessionTarget(
		t,
		env,
		env.session.Meta().SessionID,
		env.binding.WorkspaceID,
		worktreeID,
		cwdRelpath,
	)
}

func mustGetWorktreeStatus(t *testing.T, env *serviceTestEnv) *worktreepb.StatusSuccess {
	t.Helper()
	status, err := env.service.GetWorktreeStatus(env.ctx, &worktreepb.StatusRequest{
		SessionId: env.session.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("GetWorktreeStatus: %v", err)
	}
	return status
}

func TestWorktreeStatusInspectsOnlyTheRecordedTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	before, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget before: %v", err)
	}
	status := mustGetWorktreeStatus(t, env)
	if len(status.Problems) != 0 {
		t.Fatalf("status problems = %+v", status.Problems)
	}
	if status.Worktree.RecordedRoot != env.workspaceRoot || status.Worktree.ObservedRoot == nil || *status.Worktree.ObservedRoot != env.workspaceRoot {
		t.Fatalf("status worktree = %+v", status.Worktree)
	}
	after, err := env.store.ResolveSessionExecutionTarget(env.ctx, env.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget after: %v", err)
	}
	if after != before {
		t.Fatalf("status mutated target: before=%+v after=%+v", before, after)
	}
}

func TestHealthyWorktreeStatusEncodesEmptyProblemsArray(t *testing.T) {
	env := newServiceTestEnv(t)
	status := mustGetWorktreeStatus(t, env)

	encoded, err := (protojson.MarshalOptions{EmitDefaultValues: true}).Marshal(status)
	if err != nil {
		t.Fatalf("Marshal status: %v", err)
	}
	var wire struct {
		Problems []json.RawMessage `json:"problems"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("Unmarshal status: %v", err)
	}
	if wire.Problems == nil || len(wire.Problems) != 0 {
		t.Fatalf("wire problems = %#v, want non-nil empty array", wire.Problems)
	}
}

func TestWorktreeStatusUsesRecordedWorktreeRootWithNestedCwd(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/status-root")
	if err := os.Mkdir(filepath.Join(created.CanonicalRoot, "pkg"), 0o755); err != nil {
		t.Fatalf("Mkdir nested cwd: %v", err)
	}
	updateWorktreeStatusTarget(t, env, created.WorktreeID, "pkg")

	status := mustGetWorktreeStatus(t, env)
	if len(status.Problems) != 0 {
		t.Fatalf("status problems = %+v", status.Problems)
	}
	if status.Worktree.RecordedRoot != created.CanonicalRoot {
		t.Fatalf("recorded root = %q, want %q", status.Worktree.RecordedRoot, created.CanonicalRoot)
	}
	if status.Worktree.ObservedRoot == nil || *status.Worktree.ObservedRoot != created.CanonicalRoot {
		t.Fatalf("observed root = %v, want %q", status.Worktree.ObservedRoot, created.CanonicalRoot)
	}
}

func TestWorktreeStatusUsesWorkspaceRootWithNestedCwd(t *testing.T) {
	env := newServiceTestEnv(t)
	if err := os.Mkdir(filepath.Join(env.workspaceRoot, "pkg"), 0o755); err != nil {
		t.Fatalf("Mkdir nested cwd: %v", err)
	}
	updateWorktreeStatusTarget(t, env, "", "pkg")

	status := mustGetWorktreeStatus(t, env)
	if status.Worktree.RecordedRoot != env.workspaceRoot {
		t.Fatalf("recorded root = %q, want %q", status.Worktree.RecordedRoot, env.workspaceRoot)
	}
	if status.Worktree.ObservedRoot == nil || *status.Worktree.ObservedRoot != env.workspaceRoot {
		t.Fatalf("observed root = %v, want %q", status.Worktree.ObservedRoot, env.workspaceRoot)
	}
}

func TestWorktreeStatusReportsWorkspaceRootWhenWorkspaceGitBindingIsMissing(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/status-workspace-binding")
	updateWorktreeStatusTarget(t, env, created.WorktreeID, ".")
	missingWorkspaceRoot, err := config.CanonicalWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	updated, err := env.store.Queries().UpdateWorkspaceBindingCanonicalRoot(env.ctx, sqlitegen.UpdateWorkspaceBindingCanonicalRootParams{
		CanonicalRootPath: missingWorkspaceRoot,
		UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
		ID:                env.binding.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("UpdateWorkspaceBindingCanonicalRoot: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated workspace bindings = %d, want 1", updated)
	}

	status := mustGetWorktreeStatus(t, env)
	if len(status.Problems) != 1 ||
		status.Problems[0].Kind != worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISSING ||
		status.Problems[0].Root == nil ||
		*status.Problems[0].Root != missingWorkspaceRoot {
		t.Fatalf("status problems = %+v, want missing workspace binding at %q", status.Problems, missingWorkspaceRoot)
	}
}

func TestWorktreeStatusSurfacesInvalidRecordedGitMetadata(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/status-invalid-metadata")
	updateWorktreeStatusTarget(t, env, created.WorktreeID, ".")
	record, err := env.store.GetWorktreeRecordByID(env.ctx, created.WorktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeRecordByID: %v", err)
	}
	record.GitMetadataJSON = "{"
	record.UpdatedAt = time.Now().UTC()
	if err := env.store.UpsertWorktreeRecord(env.ctx, record); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}

	if _, err := env.service.GetWorktreeStatus(env.ctx, &worktreepb.StatusRequest{
		SessionId: env.session.Meta().SessionID,
	}); err == nil {
		t.Fatal("GetWorktreeStatus succeeded with invalid recorded Git metadata")
	}
}

func TestWorktreeStatusPropagatesGitInspectionCancellation(t *testing.T) {
	env := newServiceTestEnv(t)
	env.service.git = NewGitInspector(canceledGitCommandRunner{})

	_, err := env.service.GetWorktreeStatus(env.ctx, &worktreepb.StatusRequest{
		SessionId: env.session.Meta().SessionID,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GetWorktreeStatus error = %v, want context canceled", err)
	}
}

func TestWorktreeStatusReportsMissingGitBinding(t *testing.T) {
	env := newServiceTestEnv(t)
	gitMarker := filepath.Join(env.workspaceRoot, ".git")
	hiddenMarker := filepath.Join(env.workspaceRoot, ".git-hidden")
	if err := os.Rename(gitMarker, hiddenMarker); err != nil {
		t.Fatalf("hide Git marker: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Rename(hiddenMarker, gitMarker); err != nil {
			t.Fatalf("restore Git marker: %v", err)
		}
	})

	status := mustGetWorktreeStatus(t, env)
	if len(status.Problems) != 1 ||
		status.Problems[0].Kind != worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISSING {
		t.Fatalf("status problems = %+v, want GitBindingMissing", status.Problems)
	}
}

type statusRefFailingGitRunner struct {
	err      error
	exitCode int
}

func (r statusRefFailingGitRunner) Output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return execGitCommandRunner{}.Output(ctx, dir, args...)
}

func (r statusRefFailingGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, int, error) {
	if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--verify" {
		return nil, r.exitCode, r.err
	}
	return execGitCommandRunner{}.Run(ctx, dir, args...)
}

func TestWorktreeStatusPropagatesRecordedRefInspectionCancellation(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/status-ref")
	updateWorktreeStatusTarget(t, env, created.WorktreeID, ".")
	env.service.git = NewGitInspector(statusRefFailingGitRunner{err: context.Canceled, exitCode: -1})

	_, err := env.service.GetWorktreeStatus(env.ctx, &worktreepb.StatusRequest{
		SessionId: env.session.Meta().SessionID,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("GetWorktreeStatus error = %v, want context canceled", err)
	}
}

func TestWorktreeStatusReportsMissingRecordedRef(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/status-missing-ref")
	updateWorktreeStatusTarget(t, env, created.WorktreeID, ".")
	env.service.git = NewGitInspector(statusRefFailingGitRunner{
		err:      errors.New("exit status 1"),
		exitCode: 1,
	})

	status := mustGetWorktreeStatus(t, env)
	if len(status.Problems) != 1 ||
		status.Problems[0].Kind != worktreepb.StatusProblemKind_WORKTREE_STATUS_PROBLEM_RECORDED_REF_MISSING {
		t.Fatalf("status problems = %+v, want RecordedRefMissing", status.Problems)
	}
}
