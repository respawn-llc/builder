package metadata

import (
	"context"
	"core/server/metadata/sqlitegen"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/sessioncontract"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolvePersistedSessionRejectsEscapingArtifactRelpath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, binding := newMetadataTestStore(t)
	now := time.Now().UTC().UnixMilli()
	if err := store.queries.UpsertSession(ctx, sqlitegen.UpsertSessionParams{
		ID:                   "session-escape",
		ProjectID:            binding.ProjectID,
		WorkspaceID:          sql.NullString{String: binding.WorkspaceID, Valid: true},
		WorktreeID:           sql.NullString{},
		ArtifactRelpath:      "../escape",
		Name:                 "",
		FirstPromptPreview:   "",
		InputDraft:           "",
		PreviousSessionID:    sql.NullString{},
		ParentAgentSessionID: sql.NullString{},
		CreatedAtUnixMs:      now,
		UpdatedAtUnixMs:      now,
		LastSequence:         0,
		ModelRequestCount:    0,
		LaunchVisible:        0,
		CwdRelpath:           ".",
		ContinuationJson:     "{}",
		LockedJson:           "{}",
		UsageStateJson:       "{}",
		MetadataJson:         "{}",
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	_, err := store.ResolvePersistedSession(ctx, "session-escape")
	if !errors.Is(err, ErrPathEscapesPersistenceRoot) {
		t.Fatalf("expected escaping artifact relpath error, got %v", err)
	}
}

func TestResolvePersistedSessionValidatesContinuationRoleJSON(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sess := createMetadataTestSession(t, store, cfg, binding)
	tests := []struct {
		name     string
		payload  string
		wantRole *string
		wantErr  bool
	}{
		{name: "omitted role", payload: `{}`},
		{name: "null role", payload: `{"agent_role":null}`},
		{name: "custom role", payload: `{"agent_role":"worker"}`, wantRole: sessiontest.AgentRole("worker")},
		{name: "fast role", payload: `{"agent_role":"fast"}`, wantRole: sessiontest.AgentRole("fast")},
		{name: "empty role", payload: `{"agent_role":""}`, wantErr: true},
		{name: "whitespace role", payload: `{"agent_role":" \t "}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := store.db.ExecContext(ctx, "UPDATE sessions SET continuation_json = ? WHERE id = ?", tt.payload, sess.Meta().SessionID); err != nil {
				t.Fatalf("persist continuation JSON: %v", err)
			}
			record, err := store.ResolvePersistedSession(ctx, sess.Meta().SessionID)
			if tt.wantErr {
				if !errors.Is(err, session.ErrInvalidContinuationAgentRole) {
					t.Fatalf("ResolvePersistedSession error = %v, want invalid role", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePersistedSession: %v", err)
			}
			got := record.Meta.Continuation
			if tt.wantRole == nil {
				if got != nil && got.AgentRole != nil {
					t.Fatalf("continuation = %+v, want absent role", got)
				}
				return
			}
			if got == nil || got.AgentRole == nil || *got.AgentRole != *tt.wantRole {
				t.Fatalf("continuation = %+v, want role %q", got, *tt.wantRole)
			}
		})
	}
}

func TestImportSessionSnapshotRejectsInvalidContinuationRole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sess := createMetadataTestSession(t, store, cfg, binding)
	snapshot := session.PersistedStoreSnapshot{
		SessionDir: sess.Dir(),
		Meta:       persistedMetaFromMetadata(sess.Meta()),
	}
	snapshot.Meta.Continuation = &session.ContinuationContext{AgentRole: sessiontest.AgentRole(" ")}

	err := store.ImportSessionSnapshot(ctx, snapshot)
	if !errors.Is(err, session.ErrInvalidContinuationAgentRole) {
		t.Fatalf("ImportSessionSnapshot error = %v, want invalid continuation role", err)
	}
	record, err := store.ResolvePersistedSession(ctx, sess.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession after rejected write: %v", err)
	}
	if record.Meta.Continuation != nil {
		t.Fatalf("continuation = %+v, want unchanged absent continuation", record.Meta.Continuation)
	}
}

func TestImportSessionSnapshotRejectsInvalidSessionCategory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sessionID := "session-invalid-category"
	invalid := sessioncontract.SessionCategory("worker")
	err := store.ImportSessionSnapshot(ctx, session.PersistedStoreSnapshot{
		SessionDir: config.ProjectSessionDir(cfg, binding.ProjectID, sessionID),
		Meta: session.Meta{
			SessionID:          sessionID,
			Category:           &invalid,
			WorkspaceRoot:      binding.CanonicalRoot,
			WorkspaceContainer: filepath.Base(binding.CanonicalRoot),
			CreatedAt:          time.Now().UTC(),
			UpdatedAt:          time.Now().UTC(),
		},
	})
	if err == nil {
		t.Fatal("ImportSessionSnapshot accepted invalid category")
	}
}

func TestSessionCategoryResolverRejectsInvalidStoredCategory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sess := createMetadataTestSession(t, store, cfg, binding)
	if _, err := store.db.ExecContext(ctx, `PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatalf("disable check constraints: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE sessions SET category = 'worker' WHERE id = ?`, sess.Meta().SessionID); err != nil {
		t.Fatalf("seed invalid stored category: %v", err)
	}
	if _, err := store.ResolvePersistedSession(ctx, sess.Meta().SessionID); err == nil {
		t.Fatal("ResolvePersistedSession accepted invalid stored category")
	}
}

func TestSessionExecutionTargetClampsEscapingCwdRelpath(t *testing.T) {
	t.Parallel()
	target := sessionExecutionTargetFromRow(sqlitegen.GetSessionExecutionTargetByIDRow{
		WorkspaceID:   "workspace-1",
		WorkspaceRoot: "/tmp/workspace",
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
		WorktreeID:    sql.NullString{String: "worktree-a", Valid: true},
		WorktreeRoot:  sql.NullString{String: "/tmp/workspace/worktree-a", Valid: true},
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
	t.Parallel()
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
	navigationBinding, err := store.ResolveSessionNavigationBinding(ctx, sess.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionNavigationBinding: %v", err)
	}
	if navigationBinding.ProjectID != binding.ProjectID || navigationBinding.WorkspaceID != binding.WorkspaceID {
		t.Fatalf("navigation binding = %+v, want project=%q workspace=%q", navigationBinding, binding.ProjectID, binding.WorkspaceID)
	}
}

func TestResolveSessionProjectWorkspaceBoundaryUsesOwningProject(t *testing.T) {
	store, cfg, source := newMetadataTestStore(t)
	sess := createMetadataTestSession(t, store, cfg, source)
	sourceSibling, err := store.AttachWorkspaceToProject(t.Context(), source.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject source sibling: %v", err)
	}
	foreign, err := store.CreateProjectForWorkspace(t.Context(), t.TempDir(), "Foreign")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace foreign: %v", err)
	}
	foreignOnly, err := store.AttachWorkspaceToProject(t.Context(), foreign.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject foreign-only: %v", err)
	}

	boundary, err := store.ResolveSessionProjectWorkspaceBoundary(t.Context(), sess.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionProjectWorkspaceBoundary: %v", err)
	}
	if boundary.ProjectID != source.ProjectID {
		t.Fatalf("boundary project id = %q, want %q", boundary.ProjectID, source.ProjectID)
	}
	if _, err := store.db.ExecContext(t.Context(), "UPDATE sessions SET workspace_id = NULL WHERE id = ?", sess.Meta().SessionID); err != nil {
		t.Fatalf("unlink retained session: %v", err)
	}
	if _, err := store.ResolveSessionProjectWorkspaceBoundary(t.Context(), sess.Meta().SessionID); err != nil {
		t.Fatalf("resolve unlinked retained session boundary: %v", err)
	}
	roots := boundary.Workspaces
	if len(roots) != 2 {
		t.Fatalf("boundary roots = %+v, want two source project roots", roots)
	}
	rootsByPath := map[string]bool{}
	for _, root := range roots {
		rootsByPath[root.CanonicalRoot] = true
	}
	if !rootsByPath[source.CanonicalRoot] || !rootsByPath[sourceSibling.CanonicalRoot] {
		t.Fatalf("boundary roots = %+v, want source project roots", roots)
	}
	for _, workspace := range roots {
		if workspace.WorkspaceID != nil && *workspace.WorkspaceID == foreignOnly.WorkspaceID {
			t.Fatalf("boundary included foreign project workspace %q", foreignOnly.WorkspaceID)
		}
	}
}

func TestResolveProjectWorkspaceBoundaryPreservesRowIDOrderOnTimestampTies(t *testing.T) {
	store, _, source := newMetadataTestStore(t)
	ctx := context.Background()
	first, err := store.AttachWorkspaceToProject(ctx, source.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject first: %v", err)
	}
	second, err := store.AttachWorkspaceToProject(ctx, source.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject second: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"UPDATE workspaces SET created_at_unix_ms = ? WHERE project_id = ?",
		int64(123), source.ProjectID,
	); err != nil {
		t.Fatalf("set tied workspace timestamps: %v", err)
	}

	boundary, err := store.ResolveProjectWorkspaceBoundary(ctx, source.ProjectID)
	if err != nil {
		t.Fatalf("ResolveProjectWorkspaceBoundary: %v", err)
	}
	if len(boundary.Workspaces) != 3 {
		t.Fatalf("boundary workspace count = %d, want 3", len(boundary.Workspaces))
	}
	wantNewestFirst := []string{second.CanonicalRoot, first.CanonicalRoot, source.CanonicalRoot}
	for index, want := range wantNewestFirst {
		if got := boundary.Workspaces[index].CanonicalRoot; got != want {
			t.Fatalf("boundary workspace %d = %q, want %q", index, got, want)
		}
	}

	retargeted, added, err := boundary.WithWorkspace(ProjectWorkspace{CanonicalRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("WithWorkspace: %v", err)
	}
	if !added {
		t.Fatal("WithWorkspace reported no insertion")
	}
	for index, want := range wantNewestFirst {
		if got := retargeted.Workspaces[index+1].CanonicalRoot; got != want {
			t.Fatalf("retargeted workspace %d = %q, want %q", index, got, want)
		}
	}
}

func TestProjectWorkspaceCollectionRetainsExactlyNewest500AndExactLookupReachesOmittedWorkspace(t *testing.T) {
	store, _, source := newMetadataTestStore(t)
	ctx := context.Background()
	roots := make([]string, 0, ProjectWorkspaceCollectionLimit+1)
	roots = append(roots, source.CanonicalRoot)
	for index := 1; index <= ProjectWorkspaceCollectionLimit; index++ {
		binding, err := store.AttachWorkspaceToProject(ctx, source.ProjectID, t.TempDir())
		if err != nil {
			t.Fatalf("AttachWorkspaceToProject %d: %v", index, err)
		}
		roots = append(roots, binding.CanonicalRoot)
	}
	if _, err := store.db.ExecContext(ctx,
		"UPDATE workspaces SET created_at_unix_ms = ? WHERE project_id = ?",
		int64(123), source.ProjectID,
	); err != nil {
		t.Fatalf("set tied workspace timestamps: %v", err)
	}

	boundary, err := store.ResolveProjectWorkspaceBoundary(ctx, source.ProjectID)
	if err != nil {
		t.Fatalf("ResolveProjectWorkspaceBoundary: %v", err)
	}
	if len(boundary.Workspaces) != ProjectWorkspaceCollectionLimit {
		t.Fatalf("boundary count = %d, want %d", len(boundary.Workspaces), ProjectWorkspaceCollectionLimit)
	}
	if boundary.Workspaces[0].CanonicalRoot != roots[ProjectWorkspaceCollectionLimit] {
		t.Fatalf("boundary newest root = %q, want %q", boundary.Workspaces[0].CanonicalRoot, roots[ProjectWorkspaceCollectionLimit])
	}
	if boundary.Workspaces[ProjectWorkspaceCollectionLimit-1].CanonicalRoot != roots[1] {
		t.Fatalf("boundary oldest retained root = %q, want %q", boundary.Workspaces[ProjectWorkspaceCollectionLimit-1].CanonicalRoot, roots[1])
	}
	for _, workspace := range boundary.Workspaces {
		if workspace.CanonicalRoot == source.CanonicalRoot {
			t.Fatal("boundary included omitted oldest Workspace")
		}
	}

	unpaged, err := store.ListProjectWorkspaces(ctx, source.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkspaces: %v", err)
	}
	if len(unpaged) != ProjectWorkspaceCollectionLimit {
		t.Fatalf("unpaged workspace count = %d, want %d", len(unpaged), ProjectWorkspaceCollectionLimit)
	}
	paged, err := store.ListProjectWorkspacesPage(ctx, source.ProjectID, ProjectWorkspaceCollectionLimit+1, 0)
	if err != nil {
		t.Fatalf("ListProjectWorkspacesPage: %v", err)
	}
	if len(paged) != ProjectWorkspaceCollectionLimit {
		t.Fatalf("paged workspace count = %d, want %d", len(paged), ProjectWorkspaceCollectionLimit)
	}
	attached, err := store.ProjectWorkspaceAttached(ctx, source.ProjectID, source.CanonicalRoot)
	if err != nil {
		t.Fatalf("ProjectWorkspaceAttached omitted oldest: %v", err)
	}
	if !attached {
		t.Fatal("exact Workspace lookup failed for omitted oldest Workspace")
	}

	target := t.TempDir()
	retargeted, added, err := boundary.WithWorkspace(ProjectWorkspace{CanonicalRoot: target})
	if err != nil {
		t.Fatalf("WithWorkspace: %v", err)
	}
	if !added || len(retargeted.Workspaces) != ProjectWorkspaceCollectionLimit {
		t.Fatalf("retargeted count = %d, added=%t, want %d/true", len(retargeted.Workspaces), added, ProjectWorkspaceCollectionLimit)
	}
	if retargeted.Workspaces[0].CanonicalRoot != target ||
		retargeted.Workspaces[ProjectWorkspaceCollectionLimit-1].CanonicalRoot != roots[2] {
		t.Fatalf("retargeted boundary first=%q last=%q, want %q/%q", retargeted.Workspaces[0].CanonicalRoot, retargeted.Workspaces[ProjectWorkspaceCollectionLimit-1].CanonicalRoot, target, roots[2])
	}
}

func TestObservedSessionMetadataPersistencePreservesExecutionTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	worktreeRoot := filepath.Join(cfg.WorkspaceRoot, "wt-a")
	worktreeSubdir := filepath.Join(worktreeRoot, "pkg")
	canonicalWorktreeRoot := createMetadataTestWorktree(t, ctx, store, binding.WorkspaceID, "worktree-a", worktreeRoot)
	if err := os.MkdirAll(worktreeSubdir, 0o755); err != nil {
		t.Fatalf("MkdirAll worktreeSubdir: %v", err)
	}
	sess := createMetadataTestSession(t, store, cfg, binding)
	pinned := time.Unix(123, 0).UTC()
	if _, err := store.db.ExecContext(ctx, "UPDATE sessions SET updated_at_unix_ms = ? WHERE id = ?", pinned.UnixMilli(), sess.Meta().SessionID); err != nil {
		t.Fatalf("pin session updated at: %v", err)
	}
	if err := store.UpdateSessionExecutionTarget(ctx, SessionExecutionTargetUpdate{SessionID: sess.Meta().SessionID, Workspace: &SessionExecutionTargetUpdateWorkspace{ID: binding.WorkspaceID}, Worktree: &SessionExecutionTargetUpdateWorktree{ID: "worktree-a"}, CwdRelpath: "pkg"}); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget: %v", err)
	}
	resolved, err := store.ResolvePersistedSession(ctx, sess.Meta().SessionID)
	if err != nil || !resolved.Meta.UpdatedAt.Equal(pinned) {
		t.Fatalf("resolved updated at = %v, error = %v, want %v", resolved.Meta.UpdatedAt, err, pinned)
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
	if target.Worktree == nil || target.Worktree.ID != "worktree-a" {
		t.Fatalf("worktree = %+v, want worktree-a", target.Worktree)
	}
	if target.Worktree == nil || target.Worktree.Root != canonicalWorktreeRoot {
		t.Fatalf("worktree root = %+v, want %q", target.Worktree, canonicalWorktreeRoot)
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

func TestUpdateSessionExecutionTargetRejectsCrossWorkspaceWorktree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfgA, bindingA := newMetadataTestStore(t)
	workspaceB := t.TempDir()
	cfgB := loadMetadataTestConfig(t, workspaceB, cfgA.PersistenceRoot)
	bindingB, err := store.RegisterWorkspaceBinding(ctx, cfgB.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding workspaceB: %v", err)
	}
	projectSessionsDir := filepath.Join(filepath.Join(cfgA.PersistenceRoot, "projects"), bindingA.ProjectID, "sessions")
	sess, err := session.Create(projectSessionsDir, filepath.Base(projectSessionsDir), cfgA.WorkspaceRoot, sessioncontract.SessionCategoryMain, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	worktreeRoot := filepath.Join(cfgB.WorkspaceRoot, "wt-b")
	createMetadataTestWorktree(t, ctx, store, bindingB.WorkspaceID, "worktree-b", worktreeRoot)

	err = store.UpdateSessionExecutionTarget(ctx, SessionExecutionTargetUpdate{SessionID: sess.Meta().SessionID, Workspace: &SessionExecutionTargetUpdateWorkspace{ID: bindingA.WorkspaceID}, Worktree: &SessionExecutionTargetUpdateWorktree{ID: "worktree-b"}, CwdRelpath: "."})
	var mismatch *WorktreeWorkspaceMismatchError
	if !errors.As(err, &mismatch) || mismatch.WorktreeID != "worktree-b" || mismatch.WorkspaceID != bindingA.WorkspaceID {
		t.Fatalf("UpdateSessionExecutionTarget error = %v", err)
	}
}

func TestUpdateSessionExecutionTargetAllowsNullableWorkspaceTargetFromReadModel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sess := createMetadataTestSession(t, store, cfg, binding)
	if _, err := store.db.ExecContext(ctx, "UPDATE sessions SET workspace_id = NULL, worktree_id = NULL, cwd_relpath = 'pkg' WHERE id = ?", sess.Meta().SessionID); err != nil {
		t.Fatalf("clear session workspace target: %v", err)
	}
	target, err := store.ResolveSessionExecutionTarget(ctx, sess.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if target.WorkspaceID != "" || target.Worktree != nil {
		t.Fatalf("target = %+v, want nullable workspace root snapshot target", target)
	}

	if err := store.UpdateSessionExecutionTarget(ctx, SessionExecutionTargetUpdateFromReadModel(sess.Meta().SessionID, target)); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget nullable workspace: %v", err)
	}
	var storedWorkspaceID sql.NullString
	if err := store.db.QueryRowContext(ctx, "SELECT workspace_id FROM sessions WHERE id = ?", sess.Meta().SessionID).Scan(&storedWorkspaceID); err != nil {
		t.Fatalf("scan workspace_id: %v", err)
	}
	if storedWorkspaceID.Valid {
		t.Fatalf("stored workspace_id = %+v, want SQL NULL", storedWorkspaceID)
	}
}

func TestUpsertWorktreeRecordRejectsMissingRequiredFields(t *testing.T) {
	t.Parallel()
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

func TestSessionLaunchVisibilityTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		mutate      func(*testing.T, *Store, config.App, Binding, *session.Store)
		wantVisible bool
		wantName    string
		wantPreview string
	}{
		{
			name:        "name makes session launch-visible",
			wantVisible: true,
			wantName:    "incident triage",
			mutate: func(t *testing.T, _ *Store, _ config.App, _ Binding, sess *session.Store) {
				t.Helper()
				if err := sess.SetName("incident triage"); err != nil {
					t.Fatalf("SetName: %v", err)
				}
			},
		},
		{
			name:        "input draft makes session launch-visible",
			wantVisible: true,
			mutate: func(t *testing.T, _ *Store, _ config.App, _ Binding, sess *session.Store) {
				t.Helper()
				if err := sess.SetInputDraft("draft prompt"); err != nil {
					t.Fatalf("SetInputDraft: %v", err)
				}
			},
		},
		{
			name:        "first user prompt makes session launch-visible",
			wantVisible: true,
			wantPreview: "Investigate broken startup flow",
			mutate: func(t *testing.T, _ *Store, _ config.App, _ Binding, sess *session.Store) {
				t.Helper()
				appendMetadataMessage(
					t,
					sess,
					"step-1",
					session.MessageRoleUser,
					"Investigate broken startup flow\nmore detail",
				)
			},
		},
		{
			name:        "non-user events keep prepared session hidden",
			wantVisible: false,
			mutate: func(t *testing.T, _ *Store, _ config.App, _ Binding, sess *session.Store) {
				t.Helper()
				appendMetadataMessage(t, sess, "step-1", session.MessageRoleAssistant, "warming up")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, cfg, binding := newMetadataTestStore(t)
			sess := createMetadataTestSession(t, store, cfg, binding)

			assertProjectSessionListingCount(t, ctx, store, binding.ProjectID, 0)

			tc.mutate(t, store, cfg, binding, sess)

			wantCount := 0
			if tc.wantVisible {
				wantCount = 1
			}
			listed := assertProjectSessionListingCount(t, ctx, store, binding.ProjectID, wantCount)
			if !tc.wantVisible {
				return
			}
			if listed[0].SessionID.String() != sess.Meta().SessionID {
				t.Fatalf("listed session id = %q, want %q", listed[0].SessionID, sess.Meta().SessionID)
			}
			if tc.wantName == "" && listed[0].Name != nil {
				t.Fatalf("listed session name = %q, want absent", *listed[0].Name)
			}
			if tc.wantName != "" && (listed[0].Name == nil || *listed[0].Name != tc.wantName) {
				t.Fatalf("listed session name = %v, want %q", listed[0].Name, tc.wantName)
			}
			if tc.wantPreview == "" && listed[0].FirstPromptPreview != nil {
				t.Fatalf("listed first prompt preview = %q, want absent", *listed[0].FirstPromptPreview)
			}
			if tc.wantPreview != "" &&
				(listed[0].FirstPromptPreview == nil || *listed[0].FirstPromptPreview != tc.wantPreview) {
				t.Fatalf("listed first prompt preview = %v, want %q", listed[0].FirstPromptPreview, tc.wantPreview)
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
	page, err := store.ListSessionPage(ctx, projectID, sessioncontract.SessionCategoryMain, 0, 20)
	if err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	if len(page.Sessions) != want {
		t.Fatalf("listed session count = %d, want %d, sessions=%+v", len(page.Sessions), want, page.Sessions)
	}
	return page.Sessions
}

func newMetadataTestStore(t *testing.T) (*Store, config.App, Binding) {
	t.Helper()
	return newMetadataTestStoreForBoundWorkspace(t, t.TempDir())
}

func newFileBackedMetadataTestStore(t *testing.T) (*Store, config.App, Binding) {
	t.Helper()
	cfg := loadMetadataTestConfig(t, t.TempDir(), filepath.Join(t.TempDir(), "persistence"))
	store, err := Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("Open metadata test store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close metadata test store: %v", err)
		}
	})
	binding, err := store.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	return store, cfg, binding
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
	sess, err := session.Create(projectSessionsDir, filepath.Base(projectSessionsDir), cfg.WorkspaceRoot, sessioncontract.SessionCategoryMain, store.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
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
	cfg := loadMetadataTestConfig(t, workspace, filepath.Join(t.TempDir(), "persistence"))
	store := openInMemoryMetadataTestStore(t, cfg.PersistenceRoot)
	return store, cfg
}

func loadMetadataTestConfig(t *testing.T, workspace string, persistenceRoot string) config.App {
	t.Helper()
	cfg, err := config.Load(workspace, config.LoadOptions{ConfigRoot: persistenceRoot})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}
