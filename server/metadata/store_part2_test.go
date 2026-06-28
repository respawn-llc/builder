package metadata

import (
	"context"
	"core/server/metadata/sqlitegen"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/config"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolvePersistedSessionRejectsEscapingArtifactRelpath(t *testing.T) {
	ctx := context.Background()
	store, _, binding := newMetadataTestStore(t)
	now := time.Now().UTC().UnixMilli()
	if err := store.queries.UpsertSession(ctx, sqlitegen.UpsertSessionParams{
		ID:                 "session-escape",
		ProjectID:          binding.ProjectID,
		WorkspaceID:        sql.NullString{String: binding.WorkspaceID, Valid: true},
		WorktreeID:         sql.NullString{},
		ArtifactRelpath:    "../escape",
		Name:               "",
		FirstPromptPreview: "",
		InputDraft:         "",
		ParentSessionID:    "",
		CreatedAtUnixMs:    now,
		UpdatedAtUnixMs:    now,
		LastSequence:       0,
		ModelRequestCount:  0,
		InFlightStep:       0,
		LaunchVisible:      0,
		CwdRelpath:         ".",
		ContinuationJson:   "{}",
		LockedJson:         "{}",
		UsageStateJson:     "{}",
		MetadataJson:       "{}",
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	_, err := store.ResolvePersistedSession(ctx, "session-escape")
	if !errors.Is(err, ErrPathEscapesPersistenceRoot) {
		t.Fatalf("expected escaping artifact relpath error, got %v", err)
	}
}

func TestSessionExecutionTargetClampsEscapingCwdRelpath(t *testing.T) {
	target := sessionExecutionTargetFromRow(sqlitegen.GetSessionExecutionTargetByIDRow{
		WorkspaceID:   "workspace-1",
		WorkspaceRoot: "/tmp/workspace",
		WorktreeRoot:  "",
		CwdRelpath:    "../../other-project",
	})
	if target.CwdRelpath != "." {
		t.Fatalf("cwd relpath = %q, want .", target.CwdRelpath)
	}
	if target.EffectiveWorkdir != "/tmp/workspace" {
		t.Fatalf("effective workdir = %q, want /tmp/workspace", target.EffectiveWorkdir)
	}

	target = sessionExecutionTargetFromRow(sqlitegen.GetSessionExecutionTargetByIDRow{
		WorkspaceID:   "workspace-1",
		WorkspaceRoot: "/tmp/workspace",
		WorktreeRoot:  "/tmp/workspace/worktree-a",
		CwdRelpath:    "/tmp/absolute",
	})
	if target.CwdRelpath != "." {
		t.Fatalf("absolute cwd relpath = %q, want .", target.CwdRelpath)
	}
	if target.EffectiveWorkdir != "/tmp/workspace/worktree-a" {
		t.Fatalf("absolute effective workdir = %q, want /tmp/workspace/worktree-a", target.EffectiveWorkdir)
	}
}

func TestResolveSessionExecutionTargetUsesMetadataAuthority(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sess := createMetadataTestSession(t, store, cfg, binding)

	target, err := store.ResolveSessionExecutionTarget(ctx, sess.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if target.WorkspaceID != binding.WorkspaceID {
		t.Fatalf("workspace id = %q, want %q", target.WorkspaceID, binding.WorkspaceID)
	}
	if target.WorkspaceRoot != canonicalRoot {
		t.Fatalf("workspace root = %q, want %q", target.WorkspaceRoot, canonicalRoot)
	}
	if target.CwdRelpath != "." {
		t.Fatalf("cwd relpath = %q, want .", target.CwdRelpath)
	}
	if target.EffectiveWorkdir != canonicalRoot {
		t.Fatalf("effective workdir = %q, want %q", target.EffectiveWorkdir, canonicalRoot)
	}
}

func TestObservedSessionMetadataPersistencePreservesExecutionTarget(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	worktreeRoot := filepath.Join(cfg.WorkspaceRoot, "wt-a")
	worktreeSubdir := filepath.Join(worktreeRoot, "pkg")
	canonicalWorktreeRoot := createMetadataTestWorktree(t, ctx, store, binding.WorkspaceID, "worktree-a", worktreeRoot)
	if err := os.MkdirAll(worktreeSubdir, 0o755); err != nil {
		t.Fatalf("MkdirAll worktreeSubdir: %v", err)
	}
	sess := createMetadataTestSession(t, store, cfg, binding)
	if err := store.UpdateSessionExecutionTargetByID(ctx, sess.Meta().SessionID, binding.WorkspaceID, "worktree-a", "pkg"); err != nil {
		t.Fatalf("UpdateSessionExecutionTargetByID: %v", err)
	}
	reopened, err := session.OpenByID(cfg.PersistenceRoot, sess.Meta().SessionID, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.OpenByID: %v", err)
	}
	if err := reopened.SetName("hello"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	target, err := store.ResolveSessionExecutionTarget(ctx, sess.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if target.WorktreeID != "worktree-a" {
		t.Fatalf("worktree id = %q, want worktree-a", target.WorktreeID)
	}
	if target.WorktreeRoot != canonicalWorktreeRoot {
		t.Fatalf("worktree root = %q, want %q", target.WorktreeRoot, canonicalWorktreeRoot)
	}
	if target.CwdRelpath != "pkg" {
		t.Fatalf("cwd relpath = %q, want pkg", target.CwdRelpath)
	}
	canonicalWorktreeSubdir, err := config.CanonicalWorkspaceRoot(worktreeSubdir)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot worktreeSubdir: %v", err)
	}
	if target.EffectiveWorkdir != canonicalWorktreeSubdir {
		t.Fatalf("effective workdir = %q, want %q", target.EffectiveWorkdir, canonicalWorktreeSubdir)
	}
}

func TestUpdateSessionExecutionTargetByIDRejectsCrossWorkspaceWorktree(t *testing.T) {
	ctx := context.Background()
	store, cfgA, bindingA := newMetadataTestStore(t)
	workspaceB := t.TempDir()
	cfgB, err := config.Load(workspaceB, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load workspaceB: %v", err)
	}
	bindingB, err := store.RegisterWorkspaceBinding(ctx, cfgB.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding workspaceB: %v", err)
	}
	projectSessionsDir := filepath.Join(filepath.Join(cfgA.PersistenceRoot, "projects"), bindingA.ProjectID, "sessions")
	sess, err := session.Create(projectSessionsDir, filepath.Base(projectSessionsDir), cfgA.WorkspaceRoot, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	worktreeRoot := filepath.Join(cfgB.WorkspaceRoot, "wt-b")
	createMetadataTestWorktree(t, ctx, store, bindingB.WorkspaceID, "worktree-b", worktreeRoot)

	err = store.UpdateSessionExecutionTargetByID(ctx, sess.Meta().SessionID, bindingA.WorkspaceID, "worktree-b", ".")
	var mismatch *WorktreeWorkspaceMismatchError
	if !errors.As(err, &mismatch) || mismatch.WorktreeID != "worktree-b" || mismatch.WorkspaceID != bindingA.WorkspaceID {
		t.Fatalf("UpdateSessionExecutionTargetByID error = %v", err)
	}
}

func TestUpsertWorktreeRecordRejectsMissingRequiredFields(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	baseRecord := WorktreeRecord{
		ID:              "worktree-a",
		WorkspaceID:     binding.WorkspaceID,
		CanonicalRoot:   filepath.Join(cfg.WorkspaceRoot, "wt-a"),
		DisplayName:     "wt-a",
		Availability:    "available",
		GitMetadataJSON: `{}`,
	}
	tests := []struct {
		name   string
		mutate func(*WorktreeRecord)
		want   error
	}{
		{name: "id", mutate: func(record *WorktreeRecord) { record.ID = "  " }, want: ErrWorktreeIDRequired},
		{name: "workspace id", mutate: func(record *WorktreeRecord) { record.WorkspaceID = "  " }, want: ErrWorktreeWorkspaceIDRequired},
		{name: "canonical root", mutate: func(record *WorktreeRecord) { record.CanonicalRoot = "  " }, want: ErrWorktreeCanonicalRootRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := baseRecord
			tt.mutate(&record)
			err := store.UpsertWorktreeRecord(ctx, record)
			if !errors.Is(err, tt.want) {
				t.Fatalf("UpsertWorktreeRecord error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestResolvePersistedSessionUsesReboundWorkspaceRoot(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	projectSessionsDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	sess, err := session.Create(projectSessionsDir, filepath.Base(projectSessionsDir), cfg.WorkspaceRoot, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sess.SetName("hello"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if err := sess.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	oldWorkspace := cfg.WorkspaceRoot
	newWorkspace := filepath.Join(t.TempDir(), "workspace-moved")
	if err := os.Rename(oldWorkspace, newWorkspace); err != nil {
		t.Fatalf("Rename workspace: %v", err)
	}
	if _, err := store.RebindWorkspace(ctx, oldWorkspace, newWorkspace); err != nil {
		t.Fatalf("RebindWorkspace: %v", err)
	}
	record, err := store.ResolvePersistedSession(ctx, sess.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	canonicalNewWorkspace, err := config.CanonicalWorkspaceRoot(newWorkspace)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot newWorkspace: %v", err)
	}
	if record.Meta == nil {
		t.Fatal("expected resolved metadata")
	}
	if record.Meta.WorkspaceRoot != canonicalNewWorkspace {
		t.Fatalf("resolved workspace root = %q, want %q", record.Meta.WorkspaceRoot, canonicalNewWorkspace)
	}
}

func TestHiddenDurableSessionStaysOutOfProjectListingsUntilVisible(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sess := createMetadataTestSession(t, store, cfg, binding)

	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects before visibility: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected one project, got %+v", projects)
	}
	if projects[0].SessionCount != 0 {
		t.Fatalf("hidden durable session must not affect project session count, got %+v", projects[0])
	}

	sessions, err := store.ListSessionsByProject(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListSessionsByProject before visibility: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected hidden durable session to stay out of listings, got %+v", sessions)
	}

	if err := sess.SetName("incident triage"); err != nil {
		t.Fatalf("SetName: %v", err)
	}

	projects, err = store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects after visibility: %v", err)
	}
	if projects[0].SessionCount != 1 {
		t.Fatalf("visible session must affect project session count, got %+v", projects[0])
	}

	sessions, err = store.ListSessionsByProject(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListSessionsByProject after visibility: %v", err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != sess.Meta().SessionID {
		t.Fatalf("expected newly visible session in listings, got %+v", sessions)
	}
	if sessions[0].Name != "incident triage" {
		t.Fatalf("session name = %q, want incident triage", sessions[0].Name)
	}
}

func TestSessionLaunchVisibilityTransitions(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*testing.T, *session.Store)
		wantVisible bool
	}{
		{
			name:        "input draft makes session launch-visible",
			wantVisible: true,
			mutate: func(t *testing.T, sess *session.Store) {
				t.Helper()
				if err := sess.SetInputDraft("draft prompt"); err != nil {
					t.Fatalf("SetInputDraft: %v", err)
				}
			},
		},
		{
			name:        "parent linkage makes session launch-visible",
			wantVisible: true,
			mutate: func(t *testing.T, sess *session.Store) {
				t.Helper()
				if err := sess.SetParentSessionID("session-parent"); err != nil {
					t.Fatalf("SetParentSessionID: %v", err)
				}
			},
		},
		{
			name:        "first user prompt makes session launch-visible",
			wantVisible: true,
			mutate: func(t *testing.T, sess *session.Store) {
				t.Helper()
				if _, _, err := sess.AppendEvent("step-1", "message", map[string]any{"role": "user", "content": "Investigate broken startup flow\nmore detail"}); err != nil {
					t.Fatalf("AppendEvent: %v", err)
				}
			},
		},
		{
			name:        "non-user events keep prepared session hidden",
			wantVisible: false,
			mutate: func(t *testing.T, sess *session.Store) {
				t.Helper()
				if _, _, err := sess.AppendEvent("step-1", "message", map[string]any{"role": "assistant", "content": "warming up"}); err != nil {
					t.Fatalf("AppendEvent: %v", err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, cfg, binding := newMetadataTestStore(t)
			sess := createMetadataTestSession(t, store, cfg, binding)

			assertProjectSessionListingCount(t, ctx, store, binding.ProjectID, 0)

			tc.mutate(t, sess)

			wantCount := 0
			if tc.wantVisible {
				wantCount = 1
			}
			listed := assertProjectSessionListingCount(t, ctx, store, binding.ProjectID, wantCount)
			if !tc.wantVisible {
				return
			}
			if listed[0].SessionID != sess.Meta().SessionID {
				t.Fatalf("listed session id = %q, want %q", listed[0].SessionID, sess.Meta().SessionID)
			}
		})
	}
}

func assertProjectSessionListingCount(t *testing.T, ctx context.Context, store *Store, projectID string, want int) []clientui.SessionSummary {
	t.Helper()
	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected one project, got %+v", projects)
	}
	if projects[0].SessionCount != want {
		t.Fatalf("project session count = %d, want %d", projects[0].SessionCount, want)
	}
	sessions, err := store.ListSessionsByProject(ctx, projectID)
	if err != nil {
		t.Fatalf("ListSessionsByProject: %v", err)
	}
	if len(sessions) != want {
		t.Fatalf("listed session count = %d, want %d, sessions=%+v", len(sessions), want, sessions)
	}
	return sessions
}

func newMetadataTestStore(t *testing.T) (*Store, config.App, Binding) {
	t.Helper()
	return newMetadataTestStoreForBoundWorkspace(t, t.TempDir())
}

func newMetadataTestStoreForBoundWorkspace(t *testing.T, workspace string) (*Store, config.App, Binding) {
	t.Helper()
	store, cfg := newMetadataTestStoreForWorkspace(t, workspace)
	binding, err := store.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	return store, cfg, binding
}

func createMetadataTestSession(t *testing.T, store *Store, cfg config.App, binding Binding) *session.Store {
	t.Helper()
	projectSessionsDir := filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	sess, err := session.Create(projectSessionsDir, filepath.Base(projectSessionsDir), cfg.WorkspaceRoot, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sess.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	return sess
}

func createMetadataTestWorktree(t *testing.T, ctx context.Context, store *Store, workspaceID string, id string, root string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll worktree root: %v", err)
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(root)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if err := store.UpsertWorktreeRecord(ctx, WorktreeRecord{
		ID:              id,
		WorkspaceID:     workspaceID,
		CanonicalRoot:   canonicalRoot,
		DisplayName:     filepath.Base(canonicalRoot),
		Availability:    "available",
		GitMetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	return canonicalRoot
}

func newMetadataTestStoreWithoutBinding(t *testing.T) (*Store, config.App) {
	t.Helper()
	return newMetadataTestStoreForWorkspace(t, t.TempDir())
}

func newMetadataTestStoreForWorkspace(t *testing.T, workspace string) (*Store, config.App) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	store, err := Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, cfg
}
