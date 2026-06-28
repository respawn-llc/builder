package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"core/server/metadata/sqlitegen"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"

	"github.com/google/uuid"
	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var statPathForAvailability = os.Stat

// SetAvailabilityStatForTest overrides path availability probing and returns a restore function.
// It exists to keep availability-driven tests deterministic across platforms.
func SetAvailabilityStatForTest(fn func(string) (os.FileInfo, error)) func() {
	previous := statPathForAvailability
	if fn == nil {
		statPathForAvailability = os.Stat
	} else {
		statPathForAvailability = fn
	}
	return func() {
		statPathForAvailability = previous
	}
}

type Binding struct {
	ProjectID       string
	ProjectKey      string
	ProjectName     string
	WorkspaceID     string
	CanonicalRoot   string
	WorkspaceName   string
	WorkspaceStatus string
}

type WorktreeRecord struct {
	ID              string
	WorkspaceID     string
	CanonicalRoot   string
	DisplayName     string
	Availability    string
	IsMain          bool
	Managed         bool
	CreatedBranch   bool
	OriginSessionID string
	GitMetadataJSON string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type WorktreeSessionBlocker struct {
	SessionID   string
	SessionName string
	UpdatedAt   time.Time
}

type Store struct {
	persistenceRoot string
	db              *sql.DB
	queries         *sqlitegen.Queries
}

var (
	ErrInvalidProjectKey      = errors.New("invalid project key")
	ErrProjectKeyAlreadyInUse = errors.New("project key already in use")

	// ErrWorkspaceAlreadyBound is returned when a rebind target canonical root
	// is already bound to a workspace. Callers match it via errors.Is.
	ErrWorkspaceAlreadyBound = errors.New("workspace is already bound")
	// ErrWorktreeAlreadyBound is returned when a rebind worktree canonical root
	// is already bound. Callers match it via errors.Is.
	ErrWorktreeAlreadyBound = errors.New("worktree is already bound")
	// ErrWorkspacePathMissing is returned when a workspace path does not exist
	// on disk. Callers match it via errors.Is.
	ErrWorkspacePathMissing = errors.New("workspace path does not exist")
	// ErrPathEscapesPersistenceRoot is returned when a resolved path escapes or
	// lands outside the persistence root. Callers match it via errors.Is.
	ErrPathEscapesPersistenceRoot = errors.New("path escapes persistence root")

	// Worktree record required-field validation sentinels.
	ErrWorktreeIDRequired            = errors.New("worktree id is required")
	ErrWorktreeWorkspaceIDRequired   = errors.New("workspace id is required")
	ErrWorktreeCanonicalRootRequired = errors.New("worktree canonical root is required")
)

// WorktreeWorkspaceMismatchError reports that a worktree is not owned by the
// expected workspace. It exposes the involved identifiers so callers can
// inspect them via errors.As instead of parsing message wording.
type WorktreeWorkspaceMismatchError struct {
	WorktreeID  string
	WorkspaceID string
}

func (e *WorktreeWorkspaceMismatchError) Error() string {
	return fmt.Sprintf("worktree %q does not belong to workspace %q", e.WorktreeID, e.WorkspaceID)
}

func (s *Store) PersistenceRoot() string {
	if s == nil {
		return ""
	}
	return s.persistenceRoot
}

func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

func (s *Store) Queries() *sqlitegen.Queries {
	if s == nil {
		return nil
	}
	return s.queries
}

var registerWorkspaceBindingAfterLookupMissHook func()
var insertWorkspaceBindingAfterProjectUpsertHook func()
var rebindWorkspaceBeforeUpdateHook func()

func Open(persistenceRoot string) (*Store, error) {
	trimmedRoot := strings.TrimSpace(persistenceRoot)
	if trimmedRoot == "" {
		return nil, errors.New("persistence root is required")
	}
	return OpenAtPath(trimmedRoot, filepath.Join(trimmedRoot, "db", "main.sqlite3"))
}

func OpenAtPath(persistenceRoot string, databasePath string) (*Store, error) {
	trimmedRoot := strings.TrimSpace(persistenceRoot)
	trimmedDatabasePath := strings.TrimSpace(databasePath)
	if trimmedRoot == "" {
		return nil, errors.New("persistence root is required")
	}
	if trimmedDatabasePath == "" {
		return nil, errors.New("database path is required")
	}
	db, err := openDatabaseAtPath(trimmedRoot, trimmedDatabasePath)
	if err != nil {
		return nil, err
	}
	store := &Store{
		persistenceRoot: trimmedRoot,
		db:              db,
		queries:         sqlitegen.New(db),
	}
	if err := store.BackfillProjectKeys(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func ResolveBinding(ctx context.Context, persistenceRoot string, workspaceRoot string) (Binding, error) {
	store, err := Open(persistenceRoot)
	if err != nil {
		return Binding{}, err
	}
	defer func() { _ = store.Close() }()
	return store.EnsureWorkspaceBinding(ctx, workspaceRoot)
}

func RegisterBinding(ctx context.Context, persistenceRoot string, workspaceRoot string) (Binding, error) {
	store, err := Open(persistenceRoot)
	if err != nil {
		return Binding{}, err
	}
	defer func() { _ = store.Close() }()
	return store.RegisterWorkspaceBinding(ctx, workspaceRoot)
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) SessionStoreOptions() []session.StoreOption {
	if s == nil {
		return nil
	}
	return []session.StoreOption{
		session.WithPersistenceObserver(sessionObserver{store: s}),
		session.WithPersistedSessionResolver(s),
	}
}

func (s *Store) AuthoritativeSessionStoreOptions() []session.StoreOption {
	if s == nil {
		return nil
	}
	return append(s.SessionStoreOptions(), session.WithFilelessMetadataPersistence())
}

func (s *Store) EnsureWorkspaceBinding(ctx context.Context, workspaceRoot string) (Binding, error) {
	binding, err := s.lookupWorkspaceBinding(ctx, workspaceRoot)
	if err == nil {
		return binding, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Binding{}, serverapi.ErrWorkspaceNotRegistered
	}
	return Binding{}, err
}

func (s *Store) ResolveWorkspacePath(ctx context.Context, workspaceRoot string) (string, *Binding, error) {
	if s == nil || s.queries == nil {
		return "", nil, errors.New("metadata store is required")
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return "", nil, err
	}
	binding, err := s.lookupWorkspaceBinding(ctx, canonicalRoot)
	if err == nil {
		return canonicalRoot, &binding, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		// Managed task worktrees are tracked as worktrees rather than standalone
		// workspace bindings, but project-scoped commands run from the worktree
		// root still need to resolve to the owning project.
		worktree, worktreeErr := s.GetWorktreeRecordByCanonicalRoot(ctx, canonicalRoot)
		if worktreeErr == nil {
			binding, bindingErr := s.LookupWorkspaceBindingByID(ctx, worktree.WorkspaceID)
			if bindingErr != nil {
				return "", nil, bindingErr
			}
			return canonicalRoot, &binding, nil
		}
		if !errors.Is(worktreeErr, sql.ErrNoRows) {
			return "", nil, worktreeErr
		}
		return canonicalRoot, nil, nil
	}
	return "", nil, err
}

func (s *Store) LookupWorkspaceBindingByID(ctx context.Context, workspaceID string) (Binding, error) {
	if s == nil || s.queries == nil {
		return Binding{}, errors.New("metadata store is required")
	}
	row, err := s.queries.GetWorkspaceBindingByID(ctx, strings.TrimSpace(workspaceID))
	if err == nil {
		return Binding{
			ProjectID:       row.ProjectID,
			ProjectKey:      row.ProjectKey,
			ProjectName:     row.ProjectDisplayName,
			WorkspaceID:     row.WorkspaceID,
			CanonicalRoot:   row.WorkspaceRoot,
			WorkspaceName:   filepath.Base(row.WorkspaceRoot),
			WorkspaceStatus: availabilityForPath(row.WorkspaceRoot),
		}, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Binding{}, serverapi.ErrWorkspaceNotRegistered
	}
	return Binding{}, fmt.Errorf("lookup workspace binding by id: %w", err)
}

func (s *Store) GetWorkspaceByID(ctx context.Context, workspaceID string) (sqlitegen.Workspace, error) {
	if s == nil || s.queries == nil {
		return sqlitegen.Workspace{}, errors.New("metadata store is required")
	}
	row, err := s.queries.GetWorkspaceByID(ctx, strings.TrimSpace(workspaceID))
	if err != nil {
		return sqlitegen.Workspace{}, fmt.Errorf("get workspace by id: %w", err)
	}
	return row, nil
}

func (s *Store) ListWorktreeRecordsByWorkspaceID(ctx context.Context, workspaceID string) ([]WorktreeRecord, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	rows, err := s.queries.ListWorktreesByWorkspaceID(ctx, strings.TrimSpace(workspaceID))
	if err != nil {
		return nil, fmt.Errorf("list worktrees by workspace id: %w", err)
	}
	out := make([]WorktreeRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, worktreeRecordFromParts(row.ID, row.WorkspaceID, row.CanonicalRootPath, row.IsMain != 0, row.Managed != 0, row.CreatedBranch != 0, row.OriginSessionID, row.GitMetadataJson, row.CreatedAtUnixMs, row.UpdatedAtUnixMs))
	}
	return out, nil
}

func (s *Store) GetWorktreeRecordByID(ctx context.Context, worktreeID string) (WorktreeRecord, error) {
	if s == nil || s.queries == nil {
		return WorktreeRecord{}, errors.New("metadata store is required")
	}
	row, err := s.queries.GetWorktreeByID(ctx, strings.TrimSpace(worktreeID))
	if err != nil {
		return WorktreeRecord{}, fmt.Errorf("get worktree by id: %w", err)
	}
	return worktreeRecordFromParts(row.ID, row.WorkspaceID, row.CanonicalRootPath, row.IsMain != 0, row.Managed != 0, row.CreatedBranch != 0, row.OriginSessionID, row.GitMetadataJson, row.CreatedAtUnixMs, row.UpdatedAtUnixMs), nil
}

func (s *Store) GetWorktreeRecordByCanonicalRoot(ctx context.Context, worktreeRoot string) (WorktreeRecord, error) {
	if s == nil || s.queries == nil {
		return WorktreeRecord{}, errors.New("metadata store is required")
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		return WorktreeRecord{}, err
	}
	row, err := s.queries.GetWorktreeByCanonicalRoot(ctx, canonicalRoot)
	if err != nil {
		return WorktreeRecord{}, fmt.Errorf("get worktree by canonical root: %w", err)
	}
	return worktreeRecordFromParts(row.ID, row.WorkspaceID, row.CanonicalRootPath, row.IsMain != 0, row.Managed != 0, row.CreatedBranch != 0, row.OriginSessionID, row.GitMetadataJson, row.CreatedAtUnixMs, row.UpdatedAtUnixMs), nil
}

func (s *Store) UpsertWorktreeRecord(ctx context.Context, record WorktreeRecord) error {
	if s == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	if strings.TrimSpace(record.ID) == "" {
		return ErrWorktreeIDRequired
	}
	if strings.TrimSpace(record.WorkspaceID) == "" {
		return ErrWorktreeWorkspaceIDRequired
	}
	if strings.TrimSpace(record.CanonicalRoot) == "" {
		return ErrWorktreeCanonicalRootRequired
	}
	now := time.Now().UTC()
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := record.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(record.CanonicalRoot)
	if err != nil {
		return err
	}
	managed := int64(0)
	if record.Managed {
		managed = 1
	}
	createdBranch := int64(0)
	if record.CreatedBranch {
		createdBranch = 1
	}
	if err := s.queries.UpsertWorktree(ctx, sqlitegen.UpsertWorktreeParams{
		ID:                strings.TrimSpace(record.ID),
		WorkspaceID:       strings.TrimSpace(record.WorkspaceID),
		CanonicalRootPath: canonicalRoot,
		Managed:           managed,
		CreatedBranch:     createdBranch,
		OriginSessionID:   strings.TrimSpace(record.OriginSessionID),
		GitMetadataJson:   defaultJSONObject(record.GitMetadataJSON),
		CreatedAtUnixMs:   createdAt.UnixMilli(),
		UpdatedAtUnixMs:   updatedAt.UnixMilli(),
	}); err != nil {
		return fmt.Errorf("upsert worktree: %w", err)
	}
	return nil
}

func (s *Store) DeleteWorktreeRecordByID(ctx context.Context, worktreeID string) error {
	if s == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	if _, err := s.queries.DeleteWorktreeByID(ctx, strings.TrimSpace(worktreeID)); err != nil {
		return fmt.Errorf("delete worktree by id: %w", err)
	}
	return nil
}

func (s *Store) UpdateSessionExecutionTargetByID(ctx context.Context, sessionID string, workspaceID string, worktreeID string, cwdRelpath string) error {
	if s == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	trimmedWorktreeID := strings.TrimSpace(worktreeID)
	if trimmedWorktreeID != "" {
		record, err := s.GetWorktreeRecordByID(ctx, trimmedWorktreeID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(record.WorkspaceID) != trimmedWorkspaceID {
			return &WorktreeWorkspaceMismatchError{WorktreeID: trimmedWorktreeID, WorkspaceID: trimmedWorkspaceID}
		}
	}
	params := sqlitegen.UpdateSessionExecutionTargetByIDParams{
		WorkspaceID:     sql.NullString{String: trimmedWorkspaceID, Valid: trimmedWorkspaceID != ""},
		WorktreeID:      sql.NullString{String: trimmedWorktreeID, Valid: trimmedWorktreeID != ""},
		CwdRelpath:      normalizeSessionCwdRelpath(cwdRelpath),
		UpdatedAtUnixMs: time.Now().UTC().UnixMilli(),
		SessionID:       strings.TrimSpace(sessionID),
	}
	rows, err := s.queries.UpdateSessionExecutionTargetByID(ctx, params)
	if err != nil {
		return fmt.Errorf("update session execution target: %w", err)
	}
	if rows == 0 {
		return session.ErrSessionNotFound
	}
	return nil
}

// DeleteSessionRecordByID removes a session metadata row and dependent records.
func (s *Store) DeleteSessionRecordByID(ctx context.Context, sessionID string) error {
	if s == nil || s.db == nil {
		return errors.New("metadata store is required")
	}
	if _, err := s.queries.DeleteSessionRecordByID(ctx, strings.TrimSpace(sessionID)); err != nil {
		return fmt.Errorf("delete session record: %w", err)
	}
	return nil
}

type ProjectSessionArtifact struct {
	SessionID       string
	ArtifactRelpath string
}

func projectDeleteBlockersFromCounts(counts sqlitegen.GetProjectDeleteBlockerCountsRow) []serverapi.ProjectDeleteBlocker {
	blockers := []serverapi.ProjectDeleteBlocker{}
	add := func(code string, message string, count int64) {
		if count > 0 {
			blockers = append(blockers, serverapi.ProjectDeleteBlocker{Code: code, Message: message, Count: int(count)})
		}
	}
	add("active_sessions", "Project has sessions with in-flight steps.", counts.ActiveSessions)
	add("non_terminal_tasks", "Project has active or non-terminal tasks.", counts.NonTerminalTasks)
	add("active_runs", "Project has active workflow runs.", counts.ActiveRuns)
	add("runnable_runs", "Project has runnable workflow runs.", counts.RunnableRuns)
	return blockers
}

func (s *Store) DeleteProject(ctx context.Context, projectID string, deleteArtifact func(ProjectSessionArtifact, bool) error) ([]serverapi.ProjectDeleteBlocker, error) {
	if s == nil || s.db == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	if deleteArtifact == nil {
		return nil, errors.New("session artifact delete callback is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin project delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	trimmedProjectID := strings.TrimSpace(projectID)
	q := s.queries.WithTx(tx)
	locked, err := q.AcquireProjectDeleteWriteLock(ctx, trimmedProjectID)
	if err != nil {
		return nil, fmt.Errorf("lock project delete: %w", err)
	}
	if locked == 0 {
		return nil, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, trimmedProjectID)
	}
	counts, err := q.GetProjectDeleteBlockerCounts(ctx, trimmedProjectID)
	if err != nil {
		return nil, fmt.Errorf("count project delete blockers: %w", err)
	}
	if blockers := projectDeleteBlockersFromCounts(counts); len(blockers) > 0 {
		return blockers, nil
	}
	artifacts, err := q.ListProjectSessionArtifacts(ctx, trimmedProjectID)
	if err != nil {
		return nil, fmt.Errorf("list project session artifacts: %w", err)
	}
	for _, artifact := range artifacts {
		if err := deleteArtifact(ProjectSessionArtifact{SessionID: artifact.ID, ArtifactRelpath: artifact.ArtifactRelpath}, false); err != nil {
			return nil, err
		}
	}
	if err := q.DeleteProjectTasks(ctx, trimmedProjectID); err != nil {
		return nil, fmt.Errorf("delete project tasks: %w", err)
	}
	rows, err := q.DeleteProject(ctx, trimmedProjectID)
	if err != nil {
		return nil, fmt.Errorf("delete project: %w", err)
	}
	if rows == 0 {
		return nil, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, trimmedProjectID)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit project delete tx: %w", err)
	}
	_ = deleteArtifact(ProjectSessionArtifact{ArtifactRelpath: filepath.ToSlash(filepath.Join("projects", trimmedProjectID, "sessions"))}, true)
	return nil, nil
}

func (s *Store) ListSessionsTargetingWorktree(ctx context.Context, worktreeID string) ([]WorktreeSessionBlocker, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	rows, err := s.queries.ListSessionsTargetingWorktree(ctx, sql.NullString{String: strings.TrimSpace(worktreeID), Valid: strings.TrimSpace(worktreeID) != ""})
	if err != nil {
		return nil, fmt.Errorf("list sessions targeting worktree: %w", err)
	}
	out := make([]WorktreeSessionBlocker, 0, len(rows))
	for _, row := range rows {
		out = append(out, WorktreeSessionBlocker{SessionID: row.ID, SessionName: row.Name, UpdatedAt: timeFromStoredTimestamp(row.UpdatedAtUnixMs)})
	}
	return out, nil
}

func (s *Store) lookupWorkspaceBinding(ctx context.Context, workspaceRoot string) (Binding, error) {
	if s == nil || s.queries == nil {
		return Binding{}, errors.New("metadata store is required")
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return Binding{}, err
	}
	rows, err := s.queries.ListWorkspaceBindingsByCanonicalRoot(ctx, canonicalRoot)
	if err != nil {
		return Binding{}, err
	}
	switch len(rows) {
	case 0:
		return Binding{}, sql.ErrNoRows
	case 1:
		return bindingFromCanonicalRootRow(rows[0]), nil
	default:
		projectIDs := make([]string, 0, len(rows))
		for _, row := range rows {
			projectIDs = append(projectIDs, row.ProjectID)
		}
		return Binding{}, serverapi.WorkspaceBindingAmbiguousError{CanonicalRoot: canonicalRoot, ProjectIDs: projectIDs}
	}
}

func (s *Store) CreateProjectForWorkspace(ctx context.Context, workspaceRoot string, projectName string) (Binding, error) {
	return s.CreateProjectForWorkspaceWithKey(ctx, workspaceRoot, projectName, "")
}

func (s *Store) CreateProjectForWorkspaceWithKey(ctx context.Context, workspaceRoot string, projectName string, projectKey string) (Binding, error) {
	if s == nil || s.queries == nil {
		return Binding{}, errors.New("metadata store is required")
	}
	trimmedProjectName := strings.TrimSpace(projectName)
	if trimmedProjectName == "" {
		return Binding{}, errors.New("project name is required")
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return Binding{}, err
	}
	now := time.Now().UTC()
	projectID := "project-" + uuid.NewString()
	workspaceID := "workspace-" + uuid.NewString()
	workspaceName := filepath.Base(canonicalRoot)
	return s.insertWorkspaceBinding(ctx, canonicalRoot, trimmedProjectName, strings.TrimSpace(projectKey), workspaceName, projectID, workspaceID, now, true)
}

func (s *Store) AttachWorkspaceToProject(ctx context.Context, projectID string, workspaceRoot string) (Binding, error) {
	if s == nil || s.queries == nil {
		return Binding{}, errors.New("metadata store is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return Binding{}, errors.New("project id is required")
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return Binding{}, err
	}
	if binding, err := s.lookupProjectWorkspaceBinding(ctx, trimmedProjectID, canonicalRoot); err == nil {
		return binding, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Binding{}, err
	}
	projectName, err := s.queries.GetProjectDisplayName(ctx, trimmedProjectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Binding{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, trimmedProjectID)
		}
		return Binding{}, fmt.Errorf("get project display name: %w", err)
	}
	workspaceCount, err := s.queries.CountProjectWorkspaces(ctx, trimmedProjectID)
	if err != nil {
		return Binding{}, fmt.Errorf("count project workspaces: %w", err)
	}
	now := time.Now().UTC()
	workspaceID := "workspace-" + uuid.NewString()
	binding, err := s.insertWorkspaceBinding(ctx, canonicalRoot, projectName, "", filepath.Base(canonicalRoot), trimmedProjectID, workspaceID, now, workspaceCount == 0)
	if err != nil {
		return Binding{}, err
	}
	if strings.TrimSpace(binding.ProjectID) != trimmedProjectID {
		return Binding{}, fmt.Errorf("workspace %q is already bound to project %q", binding.CanonicalRoot, binding.ProjectID)
	}
	return binding, nil
}

// UpdateProjectMetadata updates a project's display name and, when projectKey is
// non-empty, its project key in a single transaction. An empty projectKey leaves
// the existing key unchanged. Existing task short IDs are frozen at creation, so
// changing the key only affects the prefix applied to future tasks.
func (s *Store) UpdateProjectMetadata(ctx context.Context, projectID string, displayName string, projectKey string) error {
	if s == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return errors.New("project id is required")
	}
	var normalizedKey string
	if strings.TrimSpace(projectKey) != "" {
		var err error
		normalizedKey, err = normalizeProjectKey(projectKey)
		if err != nil {
			return err
		}
	}
	now := time.Now().UTC().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin project metadata tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	updated, err := q.SetProjectDisplayName(ctx, sqlitegen.SetProjectDisplayNameParams{
		ProjectID:       trimmedProjectID,
		DisplayName:     displayName,
		UpdatedAtUnixMs: now,
	})
	if err != nil {
		return fmt.Errorf("set project display name: %w", err)
	}
	if updated == 0 {
		return fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, trimmedProjectID)
	}
	if normalizedKey != "" {
		state, err := q.GetProjectKeyState(ctx, trimmedProjectID)
		if err != nil {
			return fmt.Errorf("get project key state: %w", err)
		}
		if strings.TrimSpace(state.ProjectKey) != normalizedKey {
			if _, err := q.SetProjectKey(ctx, sqlitegen.SetProjectKeyParams{
				ProjectKey:      normalizedKey,
				UpdatedAtUnixMs: now,
				ProjectID:       trimmedProjectID,
			}); err != nil {
				if isSQLiteUniqueConstraint(err) {
					return fmt.Errorf("%w: %q", ErrProjectKeyAlreadyInUse, normalizedKey)
				}
				return fmt.Errorf("set project key: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit project metadata tx: %w", err)
	}
	return nil
}

func (s *Store) SetProjectDefaultWorkspace(ctx context.Context, projectID string, workspaceID string) error {
	if s == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	if trimmedProjectID == "" {
		return errors.New("project id is required")
	}
	if trimmedWorkspaceID == "" {
		return errors.New("workspace id is required")
	}
	workspace, err := s.GetWorkspaceByID(ctx, trimmedWorkspaceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, trimmedWorkspaceID)
		}
		return err
	}
	if strings.TrimSpace(workspace.ProjectID) != trimmedProjectID {
		return fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, trimmedWorkspaceID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin default workspace tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	now := time.Now().UTC().UnixMilli()
	updatedProject, err := q.SetProjectPrimaryWorkspace(ctx, sqlitegen.SetProjectPrimaryWorkspaceParams{
		WorkspaceID:     trimmedWorkspaceID,
		UpdatedAtUnixMs: now,
		ProjectID:       trimmedProjectID,
	})
	if err != nil {
		return fmt.Errorf("set project primary workspace: %w", err)
	}
	if updatedProject == 0 {
		return fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, trimmedProjectID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit default workspace tx: %w", err)
	}
	return nil
}

func (s *Store) UnlinkProjectWorkspace(ctx context.Context, projectID string, workspaceID string) ([]serverapi.ProjectWorkspaceUnlinkBlocker, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	if trimmedProjectID == "" {
		return nil, errors.New("project id is required")
	}
	if trimmedWorkspaceID == "" {
		return nil, errors.New("workspace id is required")
	}
	workspace, err := s.GetWorkspaceByID(ctx, trimmedWorkspaceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, trimmedWorkspaceID)
		}
		return nil, err
	}
	if strings.TrimSpace(workspace.ProjectID) != trimmedProjectID {
		return nil, fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, trimmedWorkspaceID)
	}

	blockers, err := workspaceUnlinkBlockersWithQueries(ctx, s.queries, trimmedProjectID, workspace)
	if err != nil {
		return nil, err
	}
	if len(blockers) > 0 {
		return blockers, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin workspace unlink tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	workspace, err = q.GetWorkspaceByID(ctx, trimmedWorkspaceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, trimmedWorkspaceID)
		}
		return nil, fmt.Errorf("get workspace by id: %w", err)
	}
	if strings.TrimSpace(workspace.ProjectID) != trimmedProjectID {
		return nil, fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, trimmedWorkspaceID)
	}
	blockers, err = workspaceUnlinkBlockersWithQueries(ctx, q, trimmedProjectID, workspace)
	if err != nil {
		return nil, err
	}
	if len(blockers) > 0 {
		return blockers, nil
	}
	rows, err := q.DeleteWorkspaceBindingByID(ctx, sqlitegen.DeleteWorkspaceBindingByIDParams{ProjectID: trimmedProjectID, WorkspaceID: trimmedWorkspaceID})
	if err != nil {
		return nil, fmt.Errorf("delete workspace binding: %w", err)
	}
	if rows == 0 {
		return nil, fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, trimmedWorkspaceID)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit workspace unlink tx: %w", err)
	}
	return nil, nil
}

func workspaceUnlinkBlockersWithQueries(ctx context.Context, q *sqlitegen.Queries, projectID string, workspace sqlitegen.Workspace) ([]serverapi.ProjectWorkspaceUnlinkBlocker, error) {
	blockers := []serverapi.ProjectWorkspaceUnlinkBlocker{}
	addCountBlocker := func(code string, message string, count int64) {
		if count > 0 {
			blockers = append(blockers, serverapi.ProjectWorkspaceUnlinkBlocker{Code: code, Message: message, Count: int(count)})
		}
	}
	primaryWorkspaceID, err := q.GetProjectPrimaryWorkspaceID(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("get project primary workspace: %w", err)
	}
	if strings.TrimSpace(primaryWorkspaceID) == strings.TrimSpace(workspace.ID) {
		blockers = append(blockers, serverapi.ProjectWorkspaceUnlinkBlocker{Code: "default_workspace", Message: "Workspace is the project default workspace."})
	}
	workspaceCount, err := q.CountProjectWorkspaces(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("count project workspaces: %w", err)
	}
	if workspaceCount <= 1 {
		blockers = append(blockers, serverapi.ProjectWorkspaceUnlinkBlocker{Code: "only_workspace", Message: "Project must keep at least one workspace."})
	}
	workspaceID := sql.NullString{String: workspace.ID, Valid: strings.TrimSpace(workspace.ID) != ""}
	nonTerminalTasks, err := q.CountNonTerminalTasksBySourceWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("count non-terminal workspace tasks: %w", err)
	}
	addCountBlocker("non_terminal_tasks", "Active or non-terminal tasks still depend on this workspace.", nonTerminalTasks)
	activeSessions, err := q.CountActiveSessionsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("count active workspace sessions: %w", err)
	}
	addCountBlocker("active_sessions", "Active sessions still depend on this workspace.", activeSessions)
	activeRuns, err := q.CountActiveTaskRunsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("count active workspace runs: %w", err)
	}
	addCountBlocker("active_runs", "Active runs still depend on this workspace.", activeRuns)
	ownedWorktrees, err := q.CountManagedOwnedWorktreesByWorkspace(ctx, workspace.ID)
	if err != nil {
		return nil, fmt.Errorf("count managed owned worktrees: %w", err)
	}
	addCountBlocker("managed_owned_worktrees", "Kent-managed owned worktrees still depend on this workspace.", ownedWorktrees)
	missingSnapshots, err := q.CountTasksMissingSourceWorkspaceSnapshot(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("count missing workspace snapshots: %w", err)
	}
	addCountBlocker("missing_history_snapshot", "Historical task references do not have a durable workspace path/name snapshot.", missingSnapshots)
	return blockers, nil
}

func (s *Store) RebindWorkspace(ctx context.Context, oldWorkspaceRoot string, newWorkspaceRoot string) (Binding, error) {
	if s == nil || s.queries == nil {
		return Binding{}, errors.New("metadata store is required")
	}
	oldCanonicalRoot, err := canonicalFilesystemPath(oldWorkspaceRoot)
	if err != nil {
		return Binding{}, err
	}
	newCanonicalRoot, err := canonicalFilesystemPath(newWorkspaceRoot)
	if err != nil {
		return Binding{}, err
	}
	if err := requireExistingDirectory(newCanonicalRoot); err != nil {
		return Binding{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Binding{}, fmt.Errorf("begin workspace rebind tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)

	oldWorkspace, err := singleWorkspaceByCanonicalRoot(ctx, q, oldCanonicalRoot)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Binding{}, serverapi.ErrWorkspaceNotRegistered
		}
		return Binding{}, fmt.Errorf("get old workspace binding: %w", err)
	}
	if newCanonicalRoot == oldWorkspace.CanonicalRootPath {
		if err := tx.Commit(); err != nil {
			return Binding{}, fmt.Errorf("commit workspace rebind noop tx: %w", err)
		}
		return s.lookupProjectWorkspaceBinding(ctx, oldWorkspace.ProjectID, newCanonicalRoot)
	}
	if existing, err := q.GetWorkspaceBindingByProjectAndCanonicalRoot(ctx, sqlitegen.GetWorkspaceBindingByProjectAndCanonicalRootParams{
		ProjectID:         oldWorkspace.ProjectID,
		CanonicalRootPath: newCanonicalRoot,
	}); err == nil {
		if existing.WorkspaceID == oldWorkspace.ID {
			if err := tx.Commit(); err != nil {
				return Binding{}, fmt.Errorf("commit workspace rebind noop tx: %w", err)
			}
			return s.lookupProjectWorkspaceBinding(ctx, oldWorkspace.ProjectID, newCanonicalRoot)
		}
		return Binding{}, fmt.Errorf("workspace %q: %w", newCanonicalRoot, ErrWorkspaceAlreadyBound)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Binding{}, fmt.Errorf("get new workspace binding: %w", err)
	}
	worktrees, err := q.ListWorktreesByWorkspaceID(ctx, oldWorkspace.ID)
	if err != nil {
		return Binding{}, fmt.Errorf("list workspace worktrees: %w", err)
	}
	if rebindWorkspaceBeforeUpdateHook != nil {
		rebindWorkspaceBeforeUpdateHook()
	}
	now := time.Now().UTC().UnixMilli()
	rows, err := q.UpdateWorkspaceBindingCanonicalRoot(ctx, sqlitegen.UpdateWorkspaceBindingCanonicalRootParams{
		ID:                oldWorkspace.ID,
		CanonicalRootPath: newCanonicalRoot,
		UpdatedAtUnixMs:   now,
	})
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return Binding{}, fmt.Errorf("rollback workspace rebind tx: %w", rollbackErr)
		}
		if binding, lookupErr := s.lookupProjectWorkspaceBinding(ctx, oldWorkspace.ProjectID, newCanonicalRoot); lookupErr == nil && binding.WorkspaceID != oldWorkspace.ID {
			return Binding{}, fmt.Errorf("workspace %q: %w", newCanonicalRoot, ErrWorkspaceAlreadyBound)
		}
		if isSQLiteUniqueConstraint(err) {
			return Binding{}, fmt.Errorf("workspace %q: %w", newCanonicalRoot, ErrWorkspaceAlreadyBound)
		}
		return Binding{}, fmt.Errorf("update workspace binding canonical root: %w", err)
	}
	if rows == 0 {
		return Binding{}, fmt.Errorf("update workspace binding canonical root: workspace %q was not updated", oldCanonicalRoot)
	}
	for _, worktree := range worktrees {
		newWorktreeRoot, mapErr := rebindDescendantPath(oldCanonicalRoot, newCanonicalRoot, worktree.CanonicalRootPath)
		if mapErr != nil {
			return Binding{}, mapErr
		}
		updatedRows, updateErr := q.UpdateWorktreeCanonicalRoot(ctx, sqlitegen.UpdateWorktreeCanonicalRootParams{
			ID:                worktree.ID,
			CanonicalRootPath: newWorktreeRoot,
			UpdatedAtUnixMs:   now,
		})
		if updateErr != nil {
			if isSQLiteUniqueConstraint(updateErr) {
				return Binding{}, fmt.Errorf("worktree %q: %w", newWorktreeRoot, ErrWorktreeAlreadyBound)
			}
			return Binding{}, fmt.Errorf("update worktree canonical root: %w", updateErr)
		}
		if updatedRows == 0 {
			return Binding{}, fmt.Errorf("update worktree canonical root: worktree %q was not updated", worktree.CanonicalRootPath)
		}
	}
	if err := tx.Commit(); err != nil {
		return Binding{}, fmt.Errorf("commit workspace rebind tx: %w", err)
	}
	return s.lookupProjectWorkspaceBinding(ctx, oldWorkspace.ProjectID, newCanonicalRoot)
}

func singleWorkspaceByCanonicalRoot(ctx context.Context, q *sqlitegen.Queries, canonicalRoot string) (sqlitegen.Workspace, error) {
	rows, err := q.ListWorkspacesByCanonicalRoot(ctx, canonicalRoot)
	if err != nil {
		return sqlitegen.Workspace{}, err
	}
	switch len(rows) {
	case 0:
		return sqlitegen.Workspace{}, sql.ErrNoRows
	case 1:
		return rows[0], nil
	default:
		projectIDs := make([]string, 0, len(rows))
		for _, row := range rows {
			projectIDs = append(projectIDs, row.ProjectID)
		}
		return sqlitegen.Workspace{}, serverapi.WorkspaceBindingAmbiguousError{CanonicalRoot: canonicalRoot, ProjectIDs: projectIDs}
	}
}

func (s *Store) lookupProjectWorkspaceBinding(ctx context.Context, projectID string, canonicalRoot string) (Binding, error) {
	if s == nil || s.queries == nil {
		return Binding{}, errors.New("metadata store is required")
	}
	row, err := s.queries.GetWorkspaceBindingByProjectAndCanonicalRoot(ctx, sqlitegen.GetWorkspaceBindingByProjectAndCanonicalRootParams{
		ProjectID:         strings.TrimSpace(projectID),
		CanonicalRootPath: strings.TrimSpace(canonicalRoot),
	})
	if err != nil {
		return Binding{}, err
	}
	return Binding{
		ProjectID:       row.ProjectID,
		ProjectKey:      row.ProjectKey,
		ProjectName:     row.ProjectDisplayName,
		WorkspaceID:     row.WorkspaceID,
		CanonicalRoot:   row.WorkspaceRoot,
		WorkspaceName:   filepath.Base(row.WorkspaceRoot),
		WorkspaceStatus: availabilityForPath(row.WorkspaceRoot),
	}, nil
}

func (s *Store) RetargetSessionWorkspace(ctx context.Context, sessionID string, newWorkspaceRoot string) (Binding, error) {
	if s == nil || s.queries == nil {
		return Binding{}, errors.New("metadata store is required")
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return Binding{}, errors.New("session id is required")
	}
	newCanonicalRoot, err := canonicalFilesystemPath(newWorkspaceRoot)
	if err != nil {
		return Binding{}, err
	}
	if err := requireExistingDirectory(newCanonicalRoot); err != nil {
		return Binding{}, err
	}

	targetRow, err := s.queries.GetSessionExecutionTargetByID(ctx, trimmedSessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Binding{}, session.ErrSessionNotFound
		}
		return Binding{}, fmt.Errorf("get session execution target: %w", err)
	}
	projectID := strings.TrimSpace(targetRow.ProjectID)
	if projectID == "" {
		return Binding{}, fmt.Errorf("session %q has no project", trimmedSessionID)
	}

	binding, err := s.AttachWorkspaceToProject(ctx, projectID, newCanonicalRoot)
	if err != nil {
		return Binding{}, err
	}
	record, err := s.ResolvePersistedSession(ctx, trimmedSessionID)
	if err != nil {
		return Binding{}, err
	}
	opened, err := session.Open(record.SessionDir, s.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		return Binding{}, err
	}
	if err := opened.SetWorkspaceRoot(binding.CanonicalRoot); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

func (s *Store) RegisterWorkspaceBinding(ctx context.Context, workspaceRoot string) (Binding, error) {
	if s == nil || s.queries == nil {
		return Binding{}, errors.New("metadata store is required")
	}
	if binding, err := s.lookupWorkspaceBinding(ctx, workspaceRoot); err == nil {
		return binding, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Binding{}, err
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return Binding{}, err
	}
	if registerWorkspaceBindingAfterLookupMissHook != nil {
		registerWorkspaceBindingAfterLookupMissHook()
	}
	return s.registerWorkspaceBindingConverged(ctx, canonicalRoot)
}

func (s *Store) registerWorkspaceBindingConverged(ctx context.Context, canonicalRoot string) (Binding, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Binding{}, fmt.Errorf("begin workspace registration tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if _, err := q.AcquireWorkspaceRegistrationLock(ctx); err != nil {
		return Binding{}, fmt.Errorf("acquire workspace registration lock: %w", err)
	}
	rows, err := q.ListWorkspaceBindingsByCanonicalRoot(ctx, canonicalRoot)
	if err != nil {
		return Binding{}, fmt.Errorf("lookup workspace binding: %w", err)
	}
	switch len(rows) {
	case 0:
	case 1:
		if err := tx.Commit(); err != nil {
			return Binding{}, fmt.Errorf("commit workspace registration lookup tx: %w", err)
		}
		return bindingFromCanonicalRootRow(rows[0]), nil
	default:
		projectIDs := make([]string, 0, len(rows))
		for _, row := range rows {
			projectIDs = append(projectIDs, row.ProjectID)
		}
		return Binding{}, serverapi.WorkspaceBindingAmbiguousError{CanonicalRoot: canonicalRoot, ProjectIDs: projectIDs}
	}

	now := time.Now().UTC()
	projectID := "project-" + uuid.NewString()
	workspaceID := "workspace-" + uuid.NewString()
	displayName := filepath.Base(canonicalRoot)
	if err := q.UpsertProject(ctx, sqlitegen.UpsertProjectParams{
		ID:              projectID,
		DisplayName:     displayName,
		CreatedAtUnixMs: now.UnixMilli(),
		UpdatedAtUnixMs: now.UnixMilli(),
		MetadataJson:    "{}",
	}); err != nil {
		return Binding{}, fmt.Errorf("upsert project: %w", err)
	}
	storedProjectKey, err := setInitialProjectKey(ctx, q, projectID, displayName, "", now.UnixMilli())
	if err != nil {
		return Binding{}, err
	}
	if _, err := q.InsertWorkspaceBinding(ctx, sqlitegen.InsertWorkspaceBindingParams{
		ID:                workspaceID,
		ProjectID:         projectID,
		CanonicalRootPath: canonicalRoot,
		GitMetadataJson:   "{}",
		CreatedAtUnixMs:   now.UnixMilli(),
		UpdatedAtUnixMs:   now.UnixMilli(),
	}); err != nil {
		return Binding{}, fmt.Errorf("insert workspace binding: %w", err)
	}
	updatedProject, err := q.SetProjectPrimaryWorkspace(ctx, sqlitegen.SetProjectPrimaryWorkspaceParams{
		WorkspaceID:     workspaceID,
		UpdatedAtUnixMs: now.UnixMilli(),
		ProjectID:       projectID,
	})
	if err != nil {
		return Binding{}, fmt.Errorf("set project primary workspace: %w", err)
	}
	if updatedProject == 0 {
		return Binding{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
	}
	if err := tx.Commit(); err != nil {
		return Binding{}, fmt.Errorf("commit workspace registration tx: %w", err)
	}
	return Binding{
		ProjectID:       projectID,
		ProjectKey:      storedProjectKey,
		ProjectName:     displayName,
		WorkspaceID:     workspaceID,
		CanonicalRoot:   canonicalRoot,
		WorkspaceName:   displayName,
		WorkspaceStatus: availabilityForPath(canonicalRoot),
	}, nil
}

func bindingFromCanonicalRootRow(row sqlitegen.ListWorkspaceBindingsByCanonicalRootRow) Binding {
	return Binding{
		ProjectID:       row.ProjectID,
		ProjectKey:      row.ProjectKey,
		ProjectName:     row.ProjectDisplayName,
		WorkspaceID:     row.WorkspaceID,
		CanonicalRoot:   row.WorkspaceRoot,
		WorkspaceName:   filepath.Base(row.WorkspaceRoot),
		WorkspaceStatus: availabilityForPath(row.WorkspaceRoot),
	}
}

func requireExistingDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("workspace path %q: %w", path, ErrWorkspacePathMissing)
		}
		return fmt.Errorf("stat workspace path %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace path %q is not a directory", path)
	}
	return nil
}

func rebindDescendantPath(oldRoot string, newRoot string, descendant string) (string, error) {
	if descendant == oldRoot {
		return newRoot, nil
	}
	prefix := oldRoot + string(filepath.Separator)
	if !strings.HasPrefix(descendant, prefix) {
		return "", fmt.Errorf("worktree %q is outside workspace %q", descendant, oldRoot)
	}
	rel, err := filepath.Rel(oldRoot, descendant)
	if err != nil {
		return "", fmt.Errorf("rebind descendant path %q: %w", descendant, err)
	}
	return filepath.Clean(filepath.Join(newRoot, rel)), nil
}

func isSQLiteUniqueConstraint(err error) bool {
	var sqliteErr *sqlitedriver.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

func (s *Store) BackfillProjectKeys(ctx context.Context) error {
	if s == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin project key backfill tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	rows, err := q.ListProjectKeyRows(ctx)
	if err != nil {
		return fmt.Errorf("list project keys: %w", err)
	}
	used := map[string]bool{}
	for _, row := range rows {
		key := strings.TrimSpace(row.ProjectKey)
		if key != "" {
			used[key] = true
		}
	}
	now := time.Now().UTC().UnixMilli()
	for _, row := range rows {
		if strings.TrimSpace(row.ProjectKey) != "" {
			continue
		}
		key := suggestProjectKey(row.DisplayName, row.ID, used)
		used[key] = true
		updated, err := q.SetProjectKey(ctx, sqlitegen.SetProjectKeyParams{ProjectKey: key, UpdatedAtUnixMs: now, ProjectID: row.ID})
		if err != nil {
			return fmt.Errorf("set project key for %q: %w", row.ID, err)
		}
		if updated == 0 {
			return fmt.Errorf("set project key for %q: project not found", row.ID)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit project key backfill tx: %w", err)
	}
	return nil
}

func setMissingProjectKey(ctx context.Context, q *sqlitegen.Queries, projectID string, displayName string, updatedAtUnixMs int64) error {
	const maxProjectKeyRetries = 8
	for attempt := 0; attempt < maxProjectKeyRetries; attempt++ {
		rows, err := q.ListProjectKeyRows(ctx)
		if err != nil {
			return fmt.Errorf("list project keys: %w", err)
		}
		used := map[string]bool{}
		alreadySet := false
		for _, row := range rows {
			key := strings.TrimSpace(row.ProjectKey)
			if key != "" {
				used[key] = true
			}
			if row.ID == projectID && key != "" {
				alreadySet = true
			}
		}
		if alreadySet {
			return nil
		}
		key := suggestProjectKey(displayName, projectID, used)
		updated, err := q.SetProjectKey(ctx, sqlitegen.SetProjectKeyParams{ProjectKey: key, UpdatedAtUnixMs: updatedAtUnixMs, ProjectID: projectID})
		if err != nil {
			if isSQLiteUniqueConstraint(err) {
				continue
			}
			return fmt.Errorf("set project key for %q: %w", projectID, err)
		}
		if updated == 0 {
			return fmt.Errorf("set project key for %q: project not found", projectID)
		}
		return nil
	}
	return fmt.Errorf("set project key for %q: exhausted unique-key retries", projectID)
}

func setInitialProjectKey(ctx context.Context, q *sqlitegen.Queries, projectID string, displayName string, projectKey string, updatedAtUnixMs int64) (string, error) {
	trimmedKey := strings.TrimSpace(projectKey)
	if trimmedKey == "" {
		if err := setMissingProjectKey(ctx, q, projectID, displayName, updatedAtUnixMs); err != nil {
			return "", err
		}
		state, err := q.GetProjectKeyState(ctx, projectID)
		if err != nil {
			return "", fmt.Errorf("get allocated project key: %w", err)
		}
		return strings.TrimSpace(state.ProjectKey), nil
	}
	normalizedKey, err := normalizeProjectKey(trimmedKey)
	if err != nil {
		return "", err
	}
	updated, err := q.SetProjectKey(ctx, sqlitegen.SetProjectKeyParams{ProjectKey: normalizedKey, UpdatedAtUnixMs: updatedAtUnixMs, ProjectID: projectID})
	if err != nil {
		if isSQLiteUniqueConstraint(err) {
			return "", fmt.Errorf("%w: %q", ErrProjectKeyAlreadyInUse, normalizedKey)
		}
		return "", fmt.Errorf("set project key for %q: %w", projectID, err)
	}
	if updated == 0 {
		return "", fmt.Errorf("set project key for %q: project not found", projectID)
	}
	return normalizedKey, nil
}

func (s *Store) SetProjectKey(ctx context.Context, projectID string, projectKey string) error {
	if s == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return errors.New("project id is required")
	}
	normalizedKey, err := normalizeProjectKey(projectKey)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin set project key tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	state, err := q.GetProjectKeyState(ctx, trimmedProjectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, trimmedProjectID)
		}
		return fmt.Errorf("get project key state: %w", err)
	}
	// The key is mutable even after tasks exist: existing task short IDs are
	// frozen at creation, so a rename only changes the prefix of future tasks.
	if strings.TrimSpace(state.ProjectKey) == normalizedKey {
		return nil
	}
	updated, err := q.SetProjectKey(ctx, sqlitegen.SetProjectKeyParams{
		ProjectKey:      normalizedKey,
		UpdatedAtUnixMs: time.Now().UTC().UnixMilli(),
		ProjectID:       trimmedProjectID,
	})
	if err != nil {
		if isSQLiteUniqueConstraint(err) {
			return fmt.Errorf("%w: %q", ErrProjectKeyAlreadyInUse, normalizedKey)
		}
		return fmt.Errorf("set project key: %w", err)
	}
	if updated == 0 {
		return fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, trimmedProjectID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set project key tx: %w", err)
	}
	return nil
}

func (s *Store) AllocateProjectTaskSequence(ctx context.Context, projectID string) (string, int64, error) {
	if s == nil || s.queries == nil {
		return "", 0, errors.New("metadata store is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return "", 0, errors.New("project id is required")
	}
	row, err := s.queries.AllocateProjectTaskSequence(ctx, sqlitegen.AllocateProjectTaskSequenceParams{
		ProjectID:       trimmedProjectID,
		UpdatedAtUnixMs: time.Now().UTC().UnixMilli(),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, trimmedProjectID)
		}
		return "", 0, fmt.Errorf("allocate project task sequence: %w", err)
	}
	key := strings.TrimSpace(row.ProjectKey)
	if key == "" {
		if err := s.BackfillProjectKeys(ctx); err != nil {
			return "", 0, err
		}
		state, stateErr := s.queries.GetProjectKeyState(ctx, trimmedProjectID)
		if stateErr != nil {
			return "", 0, fmt.Errorf("get allocated project key: %w", stateErr)
		}
		key = strings.TrimSpace(state.ProjectKey)
		if key == "" {
			return "", 0, fmt.Errorf("%w: missing allocated project key for %q", ErrInvalidProjectKey, trimmedProjectID)
		}
	}
	return key, row.NextTaskSeq - 1, nil
}

func normalizeProjectKey(raw string) (string, error) {
	key := strings.ToUpper(strings.TrimSpace(raw))
	if !isValidProjectKey(key) {
		return "", fmt.Errorf("%w: must match ^[A-Z][A-Z0-9]{1,7}$", ErrInvalidProjectKey)
	}
	return key, nil
}

func isValidProjectKey(key string) bool {
	if len(key) < 2 || len(key) > 8 {
		return false
	}
	for i, r := range key {
		if i == 0 {
			if r < 'A' || r > 'Z' {
				return false
			}
			continue
		}
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func suggestProjectKey(displayName string, projectID string, used map[string]bool) string {
	base := projectKeyBase(displayName)
	if len(base) < 2 {
		base = projectKeyBase(projectID)
	}
	if len(base) < 2 {
		base = "PRJ"
	}
	if len(base) > 3 {
		base = base[:3]
	}
	if isValidProjectKey(base) && !used[base] {
		return base
	}
	for suffix := 2; ; suffix++ {
		suffixText := strconv.Itoa(suffix)
		prefixLimit := 8 - len(suffixText)
		prefix := base
		if len(prefix) > prefixLimit {
			prefix = prefix[:prefixLimit]
		}
		if len(prefix) < 1 {
			prefix = "P"
		}
		candidate := prefix + suffixText
		if isValidProjectKey(candidate) && !used[candidate] {
			return candidate
		}
	}
}

func projectKeyBase(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			upper := unicode.ToUpper(r)
			if upper >= 'A' && upper <= 'Z' || upper >= '0' && upper <= '9' {
				b.WriteRune(upper)
			}
		}
		if b.Len() >= 8 {
			break
		}
	}
	base := b.String()
	if base == "" {
		return ""
	}
	if base[0] < 'A' || base[0] > 'Z' {
		base = "P" + base
	}
	if len(base) == 1 {
		base += "R"
	}
	if len(base) > 8 {
		base = base[:8]
	}
	return base
}

func (s *Store) insertWorkspaceBinding(ctx context.Context, canonicalRoot string, projectDisplayName string, projectKey string, workspaceDisplayName string, projectID string, workspaceID string, now time.Time, isPrimary bool) (Binding, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Binding{}, fmt.Errorf("begin workspace binding tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if err := q.UpsertProject(ctx, sqlitegen.UpsertProjectParams{
		ID:              projectID,
		DisplayName:     projectDisplayName,
		CreatedAtUnixMs: now.UnixMilli(),
		UpdatedAtUnixMs: now.UnixMilli(),
		MetadataJson:    "{}",
	}); err != nil {
		return Binding{}, fmt.Errorf("upsert project: %w", err)
	}
	storedProjectKey, err := setInitialProjectKey(ctx, q, projectID, projectDisplayName, projectKey, now.UnixMilli())
	if err != nil {
		return Binding{}, err
	}
	if insertWorkspaceBindingAfterProjectUpsertHook != nil {
		insertWorkspaceBindingAfterProjectUpsertHook()
	}
	rows, err := q.InsertWorkspaceBinding(ctx, sqlitegen.InsertWorkspaceBindingParams{
		ID:                workspaceID,
		ProjectID:         projectID,
		CanonicalRootPath: canonicalRoot,
		GitMetadataJson:   "{}",
		CreatedAtUnixMs:   now.UnixMilli(),
		UpdatedAtUnixMs:   now.UnixMilli(),
	})
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return Binding{}, fmt.Errorf("rollback workspace binding tx: %w", rollbackErr)
		}
		return Binding{}, fmt.Errorf("insert workspace binding: %w", err)
	}
	if rows == 0 {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return Binding{}, fmt.Errorf("rollback workspace binding tx: %w", rollbackErr)
		}
		if binding, recovered := s.recoverWorkspaceBindingAfterCanonicalRootConflict(ctx, canonicalRoot, workspaceID); recovered {
			return binding, nil
		}
		return Binding{}, fmt.Errorf("insert workspace binding: canonical root %q conflict was not recoverable", canonicalRoot)
	}
	if isPrimary {
		updatedProject, err := q.SetProjectPrimaryWorkspace(ctx, sqlitegen.SetProjectPrimaryWorkspaceParams{
			WorkspaceID:     workspaceID,
			UpdatedAtUnixMs: now.UnixMilli(),
			ProjectID:       projectID,
		})
		if err != nil {
			return Binding{}, fmt.Errorf("set project primary workspace: %w", err)
		}
		if updatedProject == 0 {
			return Binding{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
		}
	}
	if err := tx.Commit(); err != nil {
		return Binding{}, fmt.Errorf("commit workspace binding tx: %w", err)
	}
	return Binding{
		ProjectID:       projectID,
		ProjectKey:      storedProjectKey,
		ProjectName:     projectDisplayName,
		WorkspaceID:     workspaceID,
		CanonicalRoot:   canonicalRoot,
		WorkspaceName:   workspaceDisplayName,
		WorkspaceStatus: availabilityForPath(canonicalRoot),
	}, nil
}

func (s *Store) recoverWorkspaceBindingAfterCanonicalRootConflict(ctx context.Context, canonicalRoot string, workspaceID string) (Binding, bool) {
	binding, lookupErr := s.lookupWorkspaceBinding(ctx, canonicalRoot)
	if lookupErr != nil {
		return Binding{}, false
	}
	if strings.TrimSpace(binding.WorkspaceID) == strings.TrimSpace(workspaceID) {
		return Binding{}, false
	}
	return binding, true
}

func (s *Store) ListProjects(ctx context.Context) ([]clientui.ProjectSummary, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	rows, err := s.queries.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out := make([]clientui.ProjectSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, projectSummaryFromRow(row.ID, row.ProjectKey, row.DisplayName, row.RootPath, row.SessionCount, row.LatestActivityUnixMs))
	}
	return out, nil
}

func (s *Store) ListProjectHomeSummaries(ctx context.Context, projectID string, pageSize int, offset int) ([]serverapi.ProjectHomeSummary, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	if pageSize < 0 {
		return nil, errors.New("page size must be non-negative")
	}
	if offset < 0 {
		return nil, errors.New("offset must be non-negative")
	}
	rows, err := s.queries.ListProjectHomeSummaries(ctx, sqlitegen.ListProjectHomeSummariesParams{
		ProjectID:  strings.TrimSpace(projectID),
		LimitRows:  int64(pageSize),
		OffsetRows: int64(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("list project home summaries: %w", err)
	}
	out := make([]serverapi.ProjectHomeSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, projectHomeSummaryFromRow(row))
	}
	return out, nil
}

func (s *Store) GetProjectOverview(ctx context.Context, projectID string) (clientui.ProjectOverview, error) {
	if s == nil || s.queries == nil {
		return clientui.ProjectOverview{}, errors.New("metadata store is required")
	}
	project, err := s.queries.GetProjectSummary(ctx, strings.TrimSpace(projectID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return clientui.ProjectOverview{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, strings.TrimSpace(projectID))
		}
		return clientui.ProjectOverview{}, fmt.Errorf("get project summary: %w", err)
	}
	sessions, err := s.ListSessionsByProject(ctx, projectID)
	if err != nil {
		return clientui.ProjectOverview{}, err
	}
	workspaces, err := s.ListProjectWorkspaces(ctx, projectID)
	if err != nil {
		return clientui.ProjectOverview{}, err
	}
	return clientui.ProjectOverview{
		Project:    projectSummaryFromRow(project.ID, project.ProjectKey, project.DisplayName, project.RootPath, project.SessionCount, project.LatestActivityUnixMs),
		Workspaces: workspaces,
		Sessions:   sessions,
	}, nil
}

func (s *Store) ListProjectWorkspaces(ctx context.Context, projectID string) ([]clientui.ProjectWorkspaceSummary, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	rows, err := s.queries.ListProjectWorkspaces(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("list project workspaces: %w", err)
	}
	out := make([]clientui.ProjectWorkspaceSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, projectWorkspaceSummaryFromRow(row.ID, row.RootPath, row.IsPrimary != 0, row.SessionCount, row.LatestActivityUnixMs))
	}
	return out, nil
}

func (s *Store) ListProjectWorkspacesPage(ctx context.Context, projectID string, pageSize int, offset int) ([]clientui.ProjectWorkspaceSummary, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	if pageSize < 0 {
		return nil, errors.New("page size must be non-negative")
	}
	if offset < 0 {
		return nil, errors.New("offset must be non-negative")
	}
	rows, err := s.queries.ListProjectWorkspacesPage(ctx, sqlitegen.ListProjectWorkspacesPageParams{
		ProjectID:  strings.TrimSpace(projectID),
		LimitRows:  int64(pageSize),
		OffsetRows: int64(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("list project workspaces page: %w", err)
	}
	out := make([]clientui.ProjectWorkspaceSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, projectWorkspaceSummaryFromRow(row.ID, row.RootPath, row.IsPrimary != 0, row.SessionCount, row.LatestActivityUnixMs))
	}
	return out, nil
}

func (s *Store) ListSessionsByProject(ctx context.Context, projectID string) ([]clientui.SessionSummary, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	rows, err := s.queries.ListSessionsByProject(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("list project sessions: %w", err)
	}
	out := make([]clientui.SessionSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, clientui.SessionSummary{
			SessionID:          row.ID,
			Name:               row.Name,
			FirstPromptPreview: row.FirstPromptPreview,
			UpdatedAt:          timeFromStoredTimestamp(row.UpdatedAtUnixMs),
		})
	}
	return out, nil
}

func (s *Store) ResolveSessionExecutionTarget(ctx context.Context, sessionID string) (clientui.SessionExecutionTarget, error) {
	if s == nil || s.queries == nil {
		return clientui.SessionExecutionTarget{}, errors.New("metadata store is required")
	}
	row, err := s.queries.GetSessionExecutionTargetByID(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return clientui.SessionExecutionTarget{}, fmt.Errorf("get session execution target: %w", err)
	}
	return sessionExecutionTargetFromRow(row), nil
}

func (s *Store) SessionBelongsToProject(ctx context.Context, sessionID string, projectID string) (bool, error) {
	if s == nil || s.queries == nil {
		return false, errors.New("metadata store is required")
	}
	row, err := s.queries.GetSessionExecutionTargetByID(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return false, fmt.Errorf("get session execution target: %w", err)
	}
	return strings.TrimSpace(row.ProjectID) == strings.TrimSpace(projectID), nil
}

func (s *Store) ResolvePersistedSession(ctx context.Context, sessionID string) (session.PersistedSessionRecord, error) {
	if s == nil || s.queries == nil {
		return session.PersistedSessionRecord{}, errors.New("metadata store is required")
	}
	row, err := s.queries.GetSessionRecordByID(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return session.PersistedSessionRecord{}, fmt.Errorf("%w: %q", session.ErrSessionNotFound, strings.TrimSpace(sessionID))
		}
		return session.PersistedSessionRecord{}, fmt.Errorf("get session record: %w", err)
	}
	meta, err := sessionMetaFromRecordRow(row)
	if err != nil {
		return session.PersistedSessionRecord{}, err
	}
	sessionDir, err := sessionArtifactPathWithinRoot(s.persistenceRoot, row.ArtifactRelpath)
	if err != nil {
		return session.PersistedSessionRecord{}, err
	}
	return session.PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta:       &meta,
	}, nil
}

func (s *Store) ImportSessionSnapshot(ctx context.Context, snapshot session.PersistedStoreSnapshot) error {
	return s.upsertSessionSnapshot(ctx, snapshot)
}

func (s *Store) upsertSessionSnapshot(ctx context.Context, snapshot session.PersistedStoreSnapshot) error {
	if s == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	binding, err := s.EnsureWorkspaceBinding(ctx, snapshot.Meta.WorkspaceRoot)
	if err != nil {
		if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
			return err
		}
		existingTarget, targetErr := s.queries.GetSessionExecutionTargetByID(ctx, strings.TrimSpace(snapshot.Meta.SessionID))
		if targetErr != nil {
			if errors.Is(targetErr, sql.ErrNoRows) {
				return err
			}
			return fmt.Errorf("get existing session execution target: %w", targetErr)
		}
		binding = Binding{ProjectID: existingTarget.ProjectID}
	}
	relpath, err := relativePathWithinRoot(s.persistenceRoot, snapshot.SessionDir)
	if err != nil {
		return err
	}
	continuationJSON, err := marshalJSON(snapshot.Meta.Continuation)
	if err != nil {
		return err
	}
	lockedJSON, err := marshalJSON(snapshot.Meta.Locked)
	if err != nil {
		return err
	}
	usageStateJSON, err := marshalJSON(snapshot.Meta.UsageState)
	if err != nil {
		return err
	}
	persistedWorktreeReminder := snapshot.Meta.WorktreeReminder
	worktreeID := sql.NullString{}
	cwdRelpath := "."
	if existingTarget, targetErr := s.queries.GetSessionExecutionTargetByID(ctx, strings.TrimSpace(snapshot.Meta.SessionID)); targetErr == nil {
		if strings.TrimSpace(binding.WorkspaceID) != "" && strings.TrimSpace(existingTarget.WorkspaceID) == binding.WorkspaceID {
			worktreeID = existingTarget.WorktreeID
			cwdRelpath = normalizeSessionCwdRelpath(existingTarget.CwdRelpath)
		} else {
			persistedWorktreeReminder = nil
		}
	} else if !errors.Is(targetErr, sql.ErrNoRows) {
		return fmt.Errorf("get existing session execution target: %w", targetErr)
	}
	metadataJSON, err := marshalJSON(map[string]any{
		"workspace_root":                     snapshot.Meta.WorkspaceRoot,
		"workspace_container":                snapshot.Meta.WorkspaceContainer,
		"headless_active":                    snapshot.Meta.HeadlessActive,
		"compaction_soon_reminder_issued":    snapshot.Meta.CompactionSoonReminderIssued,
		"generated_recovered_warning_issued": snapshot.Meta.GeneratedRecoveredWarningIssued,
		"worktree_reminder":                  persistedWorktreeReminder,
		"goal":                               snapshot.Meta.Goal,
	})
	if err != nil {
		return err
	}
	inFlightStep := int64(0)
	if snapshot.Meta.InFlightStep {
		inFlightStep = 1
	}
	launchVisible := int64(0)
	if sessionLaunchVisible(snapshot.Meta) {
		launchVisible = 1
	}
	return s.queries.UpsertSession(ctx, sqlitegen.UpsertSessionParams{
		ID:                 snapshot.Meta.SessionID,
		ProjectID:          binding.ProjectID,
		WorkspaceID:        sql.NullString{String: binding.WorkspaceID, Valid: strings.TrimSpace(binding.WorkspaceID) != ""},
		WorktreeID:         worktreeID,
		ArtifactRelpath:    relpath,
		Name:               snapshot.Meta.Name,
		FirstPromptPreview: snapshot.Meta.FirstPromptPreview,
		InputDraft:         snapshot.Meta.InputDraft,
		ParentSessionID:    snapshot.Meta.ParentSessionID,
		CreatedAtUnixMs:    snapshot.Meta.CreatedAt.UTC().UnixMilli(),
		UpdatedAtUnixMs:    snapshot.Meta.UpdatedAt.UTC().UnixMilli(),
		LastSequence:       snapshot.Meta.LastSequence,
		ModelRequestCount:  snapshot.Meta.ModelRequestCount,
		InFlightStep:       inFlightStep,
		LaunchVisible:      launchVisible,
		CwdRelpath:         cwdRelpath,
		ContinuationJson:   continuationJSON,
		LockedJson:         lockedJSON,
		UsageStateJson:     usageStateJSON,
		MetadataJson:       metadataJSON,
	})
}

func availabilityForPath(path string) string {
	if _, err := statPathForAvailability(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "missing"
		}
		return "inaccessible"
	}
	return "available"
}

func availabilityForOptionalPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	return availabilityForPath(trimmed)
}

func displayNameForPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	base := filepath.Base(filepath.Clean(trimmed))
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

func sessionLaunchVisible(meta session.Meta) bool {
	if strings.TrimSpace(meta.Name) != "" {
		return true
	}
	if strings.TrimSpace(meta.FirstPromptPreview) != "" {
		return true
	}
	if strings.TrimSpace(meta.InputDraft) != "" {
		return true
	}
	if strings.TrimSpace(meta.ParentSessionID) != "" {
		return true
	}
	return meta.ModelRequestCount > 0
}

func marshalJSON(v any) (string, error) {
	if v == nil {
		return "{}", nil
	}
	body, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal metadata json: %w", err)
	}
	if string(body) == "null" {
		return "{}", nil
	}
	return string(body), nil
}

func defaultJSONObject(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "{}"
	}
	return trimmed
}

func sessionMetaFromRecordRow(row sqlitegen.GetSessionRecordByIDRow) (session.Meta, error) {
	metadataPayload := struct {
		WorkspaceRoot                   string                         `json:"workspace_root"`
		WorkspaceContainer              string                         `json:"workspace_container"`
		HeadlessActive                  bool                           `json:"headless_active"`
		CompactionSoonReminderIssued    bool                           `json:"compaction_soon_reminder_issued"`
		GeneratedRecoveredWarningIssued bool                           `json:"generated_recovered_warning_issued"`
		WorktreeReminder                *session.WorktreeReminderState `json:"worktree_reminder"`
		Goal                            *session.GoalState             `json:"goal"`
	}{}
	if err := unmarshalStoredJSON(row.MetadataJson, &metadataPayload); err != nil {
		return session.Meta{}, fmt.Errorf("decode session metadata json: %w", err)
	}
	continuation := &session.ContinuationContext{}
	if err := unmarshalStoredJSON(row.ContinuationJson, continuation); err != nil {
		return session.Meta{}, fmt.Errorf("decode continuation json: %w", err)
	}
	if strings.TrimSpace(continuation.OpenAIBaseURL) == "" && strings.TrimSpace(continuation.AgentRole) == "" {
		continuation = nil
	}
	locked := &session.LockedContract{}
	if err := unmarshalStoredJSON(row.LockedJson, locked); err != nil {
		return session.Meta{}, fmt.Errorf("decode locked json: %w", err)
	}
	if locked.LockedAt.IsZero() && strings.TrimSpace(locked.Model) == "" && len(locked.EnabledTools) == 0 && locked.ProviderContract.ProviderID == "" {
		locked = nil
	}
	usageState := &session.UsageState{}
	if err := unmarshalStoredJSON(row.UsageStateJson, usageState); err != nil {
		return session.Meta{}, fmt.Errorf("decode usage state json: %w", err)
	}
	if *usageState == (session.UsageState{}) {
		usageState = nil
	}
	// The joined workspace row is authoritative. The JSON payload may still
	// contain a historical snapshot captured before an explicit rebind.
	workspaceRoot := strings.TrimSpace(row.WorkspaceRoot)
	if workspaceRoot == "" && strings.TrimSpace(metadataPayload.WorkspaceRoot) != "" {
		workspaceRoot = strings.TrimSpace(metadataPayload.WorkspaceRoot)
	}
	workspaceContainer := strings.TrimSpace(metadataPayload.WorkspaceContainer)
	if workspaceContainer == "" {
		workspaceContainer = filepath.Base(filepath.Clean(workspaceRoot))
	}
	return session.Meta{
		SessionID:                       row.ID,
		Name:                            row.Name,
		FirstPromptPreview:              row.FirstPromptPreview,
		InputDraft:                      row.InputDraft,
		ParentSessionID:                 row.ParentSessionID,
		WorkspaceRoot:                   workspaceRoot,
		WorkspaceContainer:              workspaceContainer,
		Continuation:                    continuation,
		CreatedAt:                       timeFromStoredTimestamp(row.CreatedAtUnixMs),
		UpdatedAt:                       timeFromStoredTimestamp(row.UpdatedAtUnixMs),
		LastSequence:                    row.LastSequence,
		ModelRequestCount:               row.ModelRequestCount,
		InFlightStep:                    row.InFlightStep != 0,
		HeadlessActive:                  metadataPayload.HeadlessActive,
		CompactionSoonReminderIssued:    metadataPayload.CompactionSoonReminderIssued,
		GeneratedRecoveredWarningIssued: metadataPayload.GeneratedRecoveredWarningIssued,
		WorktreeReminder:                metadataPayload.WorktreeReminder,
		Goal:                            metadataPayload.Goal,
		UsageState:                      usageState,
		Locked:                          locked,
	}, nil
}

func unmarshalStoredJSON(body string, target any) error {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return nil
	}
	return json.Unmarshal([]byte(trimmed), target)
}

func projectSummaryFromRow(projectID string, projectKey string, displayName string, rootPath string, sessionCount int64, latestActivityUnixMs int64) clientui.ProjectSummary {
	return clientui.ProjectSummary{
		ProjectID:    projectID,
		ProjectKey:   projectKey,
		DisplayName:  displayName,
		RootPath:     rootPath,
		Availability: clientui.ProjectAvailability(availabilityForPath(rootPath)),
		SessionCount: int(sessionCount),
		UpdatedAt:    timeFromStoredTimestamp(latestActivityUnixMs),
	}
}

func projectWorkspaceSummaryFromRow(workspaceID string, rootPath string, isPrimary bool, sessionCount int64, latestActivityUnixMs int64) clientui.ProjectWorkspaceSummary {
	return clientui.ProjectWorkspaceSummary{
		WorkspaceID:  workspaceID,
		DisplayName:  displayNameForPath(rootPath),
		RootPath:     rootPath,
		Availability: clientui.ProjectAvailability(availabilityForPath(rootPath)),
		IsPrimary:    isPrimary,
		SessionCount: int(sessionCount),
		UpdatedAt:    timeFromStoredTimestamp(latestActivityUnixMs),
	}
}

func projectHomeSummaryFromRow(row sqlitegen.ListProjectHomeSummariesRow) serverapi.ProjectHomeSummary {
	return serverapi.ProjectHomeSummary{
		ProjectID:   row.ProjectID,
		ProjectKey:  row.ProjectKey,
		DisplayName: row.DisplayName,
		PrimaryWorkspace: serverapi.ProjectWorkspaceSummary{
			WorkspaceID:     row.PrimaryWorkspaceID,
			DisplayName:     displayNameForPath(row.PrimaryWorkspaceRootPath),
			RootPath:        row.PrimaryWorkspaceRootPath,
			Availability:    availabilityForPath(row.PrimaryWorkspaceRootPath),
			IsPrimary:       true,
			UpdatedAtUnixMs: row.PrimaryWorkspaceUpdatedAtUnixMs,
		},
		DefaultWorkflowID:    row.DefaultWorkflowID,
		DefaultWorkflowName:  row.DefaultWorkflowName,
		DefaultWorkflowValid: row.DefaultWorkflowValid != 0,
		UpdatedAtUnixMs:      row.LatestActivityUnixMs,
		TaskCount:            int(row.TaskCount),
		AttentionCount:       int(row.AttentionCount),
		WorkflowCount:        int(row.WorkflowCount),
	}
}

func sessionExecutionTargetFromRow(row sqlitegen.GetSessionExecutionTargetByIDRow) clientui.SessionExecutionTarget {
	worktreeID := ""
	if row.WorktreeID.Valid {
		worktreeID = row.WorktreeID.String
	}
	workspaceName := displayNameForPath(row.WorkspaceRoot)
	if strings.TrimSpace(row.WorkspaceID) == "" && strings.TrimSpace(row.WorkspaceSnapshotName) != "" {
		workspaceName = strings.TrimSpace(row.WorkspaceSnapshotName)
	}
	baseRoot := strings.TrimSpace(row.WorkspaceRoot)
	if strings.TrimSpace(row.WorktreeRoot) != "" {
		baseRoot = strings.TrimSpace(row.WorktreeRoot)
	}
	cwdRelpath := normalizeSessionCwdRelpath(row.CwdRelpath)
	effectiveWorkdir := effectiveWorkdirWithinRoot(baseRoot, cwdRelpath)
	return clientui.SessionExecutionTarget{
		WorkspaceID:           row.WorkspaceID,
		WorkspaceName:         workspaceName,
		WorkspaceRoot:         row.WorkspaceRoot,
		WorkspaceAvailability: availabilityForOptionalPath(row.WorkspaceRoot),
		WorktreeID:            worktreeID,
		WorktreeName:          displayNameForPath(row.WorktreeRoot),
		WorktreeRoot:          row.WorktreeRoot,
		WorktreeAvailability:  availabilityForOptionalPath(row.WorktreeRoot),
		CwdRelpath:            cwdRelpath,
		EffectiveWorkdir:      effectiveWorkdir,
	}
}

func worktreeRecordFromParts(id string, workspaceID string, canonicalRoot string, isMain bool, managed bool, createdBranch bool, originSessionID string, gitMetadataJSON string, createdAtUnixMs int64, updatedAtUnixMs int64) WorktreeRecord {
	return WorktreeRecord{
		ID:              id,
		WorkspaceID:     workspaceID,
		CanonicalRoot:   canonicalRoot,
		DisplayName:     displayNameForPath(canonicalRoot),
		Availability:    availabilityForOptionalPath(canonicalRoot),
		IsMain:          isMain,
		Managed:         managed,
		CreatedBranch:   createdBranch,
		OriginSessionID: originSessionID,
		GitMetadataJSON: gitMetadataJSON,
		CreatedAt:       timeFromStoredTimestamp(createdAtUnixMs),
		UpdatedAt:       timeFromStoredTimestamp(updatedAtUnixMs),
	}
}

func normalizeSessionCwdRelpath(value string) string {
	trimmed := filepath.ToSlash(strings.TrimSpace(value))
	if trimmed == "" || trimmed == "/" {
		return "."
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(trimmed)))
	if cleaned == "" || cleaned == "/" {
		return "."
	}
	if filepath.IsAbs(filepath.FromSlash(cleaned)) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "."
	}
	return cleaned
}

func effectiveWorkdirWithinRoot(baseRoot string, cwdRelpath string) string {
	trimmedBase := strings.TrimSpace(baseRoot)
	if trimmedBase == "" {
		return ""
	}
	normalizedRelpath := normalizeSessionCwdRelpath(cwdRelpath)
	if normalizedRelpath == "." {
		return trimmedBase
	}
	candidate := filepath.Clean(filepath.Join(trimmedBase, filepath.FromSlash(normalizedRelpath)))
	rel, err := filepath.Rel(trimmedBase, candidate)
	if err != nil {
		return trimmedBase
	}
	cleanedRel := filepath.Clean(rel)
	if cleanedRel == ".." || strings.HasPrefix(cleanedRel, ".."+string(filepath.Separator)) {
		return trimmedBase
	}
	return candidate
}

func relativePathWithinRoot(root string, target string) (string, error) {
	canonicalRoot, err := canonicalFilesystemPath(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize persistence root: %w", err)
	}
	canonicalTarget, err := canonicalFilesystemPath(target)
	if err != nil {
		return "", fmt.Errorf("canonicalize session dir: %w", err)
	}
	relpath, err := filepath.Rel(canonicalRoot, canonicalTarget)
	if err != nil {
		return "", fmt.Errorf("compute session artifact relpath: %w", err)
	}
	cleaned := filepath.ToSlash(filepath.Clean(relpath))
	if cleaned == "." || filepath.IsAbs(filepath.FromSlash(cleaned)) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("session dir %q is outside persistence root %q: %w", target, root, ErrPathEscapesPersistenceRoot)
	}
	return cleaned, nil
}

func canonicalFilesystemPath(path string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return canonical, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := absolute
	suffix := make([]string, 0, 4)
	for {
		next := filepath.Dir(parent)
		if next == parent {
			return absolute, nil
		}
		suffix = append([]string{filepath.Base(parent)}, suffix...)
		parent = next
		canonicalParent, parentErr := filepath.EvalSymlinks(parent)
		if parentErr == nil {
			parts := append([]string{canonicalParent}, suffix...)
			return filepath.Join(parts...), nil
		}
		if !errors.Is(parentErr, os.ErrNotExist) {
			return "", parentErr
		}
	}
}

func sessionArtifactPathWithinRoot(root string, artifactRelpath string) (string, error) {
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(artifactRelpath))))
	if cleaned == "" || cleaned == "." || filepath.IsAbs(filepath.FromSlash(cleaned)) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("session artifact relpath %q escapes persistence root %q: %w", artifactRelpath, root, ErrPathEscapesPersistenceRoot)
	}
	return filepath.Join(root, filepath.FromSlash(cleaned)), nil
}

func timeFromStoredTimestamp(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	const unixMillisUpperBound = int64(1_000_000_000_000_000)
	if value < unixMillisUpperBound {
		return time.UnixMilli(value).UTC()
	}
	return time.Unix(0, value).UTC()
}

type sessionObserver struct {
	store *Store
}

func (o sessionObserver) ObservePersistedStore(ctx context.Context, snapshot session.PersistedStoreSnapshot) error {
	if o.store == nil {
		return nil
	}
	return o.store.upsertSessionSnapshot(ctx, snapshot)
}
