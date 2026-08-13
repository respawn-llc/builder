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
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"

	"github.com/google/uuid"
	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

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
	ID                    string
	WorkspaceID           string
	CanonicalRoot         string
	DisplayName           string
	Availability          string
	IsMain                bool
	Managed               bool
	CreatedBranch         bool
	OriginSessionID       string
	GitMetadataJSON       string
	CreationBaseCommitOID *string
	CreatedAt             time.Time
	UpdatedAt             time.Time
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

type sessionMetadataDocument struct {
	WorkspaceRoot                   string                         `json:"workspace_root"`
	WorkspaceContainer              string                         `json:"workspace_container"`
	ChatSettings                    *session.ChatSettingsOverrides `json:"chat_settings,omitempty"`
	ConversationEstablished         bool                           `json:"conversation_established"`
	PromptCacheLineageGeneration    int                            `json:"prompt_cache_lineage_generation"`
	HeadlessActive                  bool                           `json:"headless_active"`
	CompactionSoonReminderIssued    bool                           `json:"compaction_soon_reminder_issued"`
	GeneratedRecoveredWarningIssued bool                           `json:"generated_recovered_warning_issued"`
	PendingModelRecovery            *session.PendingModelRecovery  `json:"pending_model_recovery"`
	WorktreeReminder                *session.WorktreeReminderState `json:"worktree_reminder"`
	Goal                            *session.GoalState             `json:"goal"`
}

var (
	ErrInvalidProjectKey      = runtimeids.ErrInvalidProjectKey
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

	errSessionWorkspaceRootRequired      = errors.New("session workspace root is required")
	errSessionWorkspaceContainerRequired = errors.New("session workspace container is required")

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

type SessionExecutionTargetUpdate struct {
	SessionID  string
	Workspace  *SessionExecutionTargetUpdateWorkspace
	Worktree   *SessionExecutionTargetUpdateWorktree
	CwdRelpath string
}

type SessionExecutionTargetUpdateWorkspace struct {
	ID string
}

type SessionExecutionTargetUpdateWorktree struct {
	ID string
}

func SessionExecutionTargetUpdateFromReadModel(sessionID string, target clientui.SessionExecutionTarget) SessionExecutionTargetUpdate {
	var workspace *SessionExecutionTargetUpdateWorkspace
	if strings.TrimSpace(target.WorkspaceID) != "" {
		workspace = &SessionExecutionTargetUpdateWorkspace{ID: target.WorkspaceID}
	}
	var worktree *SessionExecutionTargetUpdateWorktree
	if target.Worktree != nil {
		worktree = &SessionExecutionTargetUpdateWorktree{ID: target.Worktree.ID}
	}
	return SessionExecutionTargetUpdate{
		SessionID:  sessionID,
		Workspace:  workspace,
		Worktree:   worktree,
		CwdRelpath: target.CwdRelpath,
	}
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

func (s *Store) ListProjectTaskIDs(ctx context.Context, projectID string) ([]string, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, errors.New("project id is required")
	}
	return s.queries.ListProjectTaskIDs(ctx, projectID)
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

func (s *Store) AuthoritativeSessionStoreOptions() []session.StoreOption {
	if s == nil {
		return nil
	}
	return []session.StoreOption{
		session.WithPersistenceObserver(sessionObserver{store: s}),
		session.WithPersistedSessionResolver(s),
	}
}

func (s *Store) WorkspaceChatMaterializationStoreOptions(workspaceID string) []session.StoreOption {
	if s == nil {
		return nil
	}
	return []session.StoreOption{
		session.WithPersistenceObserver(workspaceChatMaterializationObserver{
			store:       s,
			workspaceID: strings.TrimSpace(workspaceID),
		}),
		session.WithPersistedSessionResolver(s),
	}
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
		return bindingFromWorkspaceFields(
			row.ProjectID,
			row.ProjectKey,
			row.ProjectDisplayName,
			row.WorkspaceID,
			row.WorkspaceRoot,
		), nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Binding{}, serverapi.ErrWorkspaceNotRegistered
	}
	return Binding{}, fmt.Errorf("lookup workspace binding by id: %w", err)
}

func (s *Store) ResolveProjectWorkspaceSelector(ctx context.Context, projectID string, selector serverapi.ProjectWorkspaceSelector) (Binding, error) {
	if s == nil || s.queries == nil {
		return Binding{}, errors.New("metadata store is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return Binding{}, errors.New("project id is required")
	}
	if _, err := s.queries.GetProjectDisplayName(ctx, trimmedProjectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Binding{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, trimmedProjectID)
		}
		return Binding{}, fmt.Errorf("check project: %w", err)
	}
	if workspaceID := selector.WorkspaceIDValue(); workspaceID != nil {
		binding, err := s.LookupWorkspaceBindingByID(ctx, *workspaceID)
		if err != nil {
			return Binding{}, err
		}
		if strings.TrimSpace(binding.ProjectID) != trimmedProjectID {
			return Binding{}, fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, *workspaceID)
		}
		return binding, nil
	}
	workspaceRootValue := selector.WorkspaceRootValue()
	if workspaceRootValue == nil {
		return Binding{}, errors.New("workspace root selector is required")
	}
	workspaceRoot := *workspaceRootValue
	absoluteRoot, absErr := filepath.Abs(filepath.Clean(workspaceRoot))
	lookupByRoot := func(root string) (Binding, error) {
		row, err := s.queries.GetWorkspaceBindingByProjectAndCanonicalRoot(ctx, sqlitegen.GetWorkspaceBindingByProjectAndCanonicalRootParams{
			ProjectID:         trimmedProjectID,
			CanonicalRootPath: root,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return Binding{}, fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, workspaceRoot)
		}
		if err != nil {
			return Binding{}, fmt.Errorf("lookup workspace binding by path: %w", err)
		}
		return bindingFromProjectCanonicalRootRow(row), nil
	}
	// Resolve the nearest existing ancestor before appending missing path
	// components so a removed workspace remains addressable through the
	// symlinked path it was attached from.
	canonicalRoot, canonicalErr := canonicalFilesystemPath(workspaceRoot)
	if canonicalErr != nil {
		if absErr != nil {
			return Binding{}, serverapi.WorkspacePathIdentityError{WorkspaceRoot: workspaceRoot, Cause: absErr}
		}
		_, statErr := os.Stat(absoluteRoot)
		if errors.Is(statErr, os.ErrNotExist) {
			info, lstatErr := os.Lstat(absoluteRoot)
			if lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
				return Binding{}, serverapi.WorkspacePathIdentityError{WorkspaceRoot: workspaceRoot, Cause: canonicalErr}
			}
			if lstatErr != nil && !errors.Is(lstatErr, os.ErrNotExist) {
				return Binding{}, serverapi.WorkspacePathIdentityError{WorkspaceRoot: workspaceRoot, Cause: lstatErr}
			}
		} else if statErr == nil {
			return Binding{}, serverapi.WorkspacePathIdentityError{WorkspaceRoot: workspaceRoot, Cause: canonicalErr}
		}
		if binding, err := lookupByRoot(filepath.Clean(absoluteRoot)); err == nil {
			return binding, nil
		} else if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
			return Binding{}, err
		}
		return Binding{}, serverapi.WorkspacePathIdentityError{WorkspaceRoot: workspaceRoot, Cause: canonicalErr}
	}
	binding, err := lookupByRoot(canonicalRoot)
	if err == nil {
		return binding, nil
	}
	if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		return Binding{}, err
	}
	// Bindings persisted with a lexical missing-path identity remain
	// detachable, but only consider that fallback when the selected path is
	// actually absent so a replaced symlink cannot revive a stale binding.
	if _, statErr := os.Stat(absoluteRoot); errors.Is(statErr, os.ErrNotExist) {
		if binding, lexicalErr := lookupByRoot(filepath.Clean(absoluteRoot)); lexicalErr == nil {
			return binding, nil
		} else if !errors.Is(lexicalErr, serverapi.ErrWorkspaceNotRegistered) {
			return Binding{}, lexicalErr
		}
	}
	return Binding{}, err
}

func bindingFromProjectCanonicalRootRow(row sqlitegen.GetWorkspaceBindingByProjectAndCanonicalRootRow) Binding {
	return bindingFromWorkspaceFields(
		row.ProjectID,
		row.ProjectKey,
		row.ProjectDisplayName,
		row.WorkspaceID,
		row.WorkspaceRoot,
	)
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

func (s *Store) ReadWorkspaceChatDraft(ctx context.Context, workspaceID string) (*WorkspaceChatDraftDocument, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	if trimmedWorkspaceID == "" {
		return nil, errors.New("workspace id is required")
	}
	document, err := s.queries.GetWorkspaceChatDraft(ctx, trimmedWorkspaceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, trimmedWorkspaceID)
		}
		return nil, fmt.Errorf("get workspace Chat draft: %w", err)
	}
	if !document.Valid {
		return nil, nil
	}
	var draft WorkspaceChatDraftDocument
	if err := json.Unmarshal([]byte(document.String), &draft); err != nil {
		return nil, fmt.Errorf("decode workspace Chat draft: %w", err)
	}
	return &draft, nil
}

func (s *Store) ReplaceWorkspaceChatDraft(ctx context.Context, workspaceID string, draft *WorkspaceChatDraftDocument) error {
	if s == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	if trimmedWorkspaceID == "" {
		return errors.New("workspace id is required")
	}
	value := sql.NullString{}
	if draft != nil {
		if err := draft.Validate(); err != nil {
			return err
		}
		encoded, err := json.Marshal(draft)
		if err != nil {
			return fmt.Errorf("encode workspace Chat draft: %w", err)
		}
		value = sql.NullString{String: string(encoded), Valid: true}
	}
	rows, err := s.queries.ReplaceWorkspaceChatDraft(ctx, sqlitegen.ReplaceWorkspaceChatDraftParams{
		ChatDraftJson: value,
		ID:            trimmedWorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("replace workspace Chat draft: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, trimmedWorkspaceID)
	}
	return nil
}

func (s *Store) ResolveProjectSourceWorkspace(ctx context.Context, projectID string) (sqlitegen.Workspace, error) {
	if s == nil || s.queries == nil {
		return sqlitegen.Workspace{}, errors.New("metadata store is required")
	}
	workspaceID, err := ResolveProjectSourceWorkspaceID(ctx, s.queries, projectID)
	if err != nil {
		return sqlitegen.Workspace{}, err
	}
	workspace, err := s.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return sqlitegen.Workspace{}, err
	}
	if strings.TrimSpace(workspace.ProjectID) != strings.TrimSpace(projectID) {
		return sqlitegen.Workspace{}, fmt.Errorf("source workspace %q does not belong to project %q", workspaceID, strings.TrimSpace(projectID))
	}
	return workspace, nil
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
		out = append(out, worktreeRecordFromParts(row.ID, row.WorkspaceID, row.CanonicalRootPath, row.IsMain != 0, row.Managed != 0, row.CreatedBranch != 0, row.OriginSessionID, row.GitMetadataJson, row.CreationBaseCommitOid, row.CreatedAtUnixMs, row.UpdatedAtUnixMs))
	}
	return out, nil
}

func (s *Store) ListManagedWorktreeRoots(ctx context.Context) ([]string, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	rows, err := s.queries.ListManagedWorktreeRoots(ctx)
	if err != nil {
		return nil, fmt.Errorf("list managed worktree roots: %w", err)
	}
	roots := make([]string, 0, len(rows))
	for _, row := range rows {
		root := strings.TrimSpace(row)
		if root == "" {
			return nil, errors.New("managed worktree root is required")
		}
		roots = append(roots, root)
	}
	return roots, nil
}

func (s *Store) GetWorktreeRecordByID(ctx context.Context, worktreeID string) (WorktreeRecord, error) {
	if s == nil || s.queries == nil {
		return WorktreeRecord{}, errors.New("metadata store is required")
	}
	row, err := s.queries.GetWorktreeByID(ctx, strings.TrimSpace(worktreeID))
	if err != nil {
		return WorktreeRecord{}, fmt.Errorf("get worktree by id: %w", err)
	}
	return worktreeRecordFromParts(row.ID, row.WorkspaceID, row.CanonicalRootPath, row.IsMain != 0, row.Managed != 0, row.CreatedBranch != 0, row.OriginSessionID, row.GitMetadataJson, row.CreationBaseCommitOid, row.CreatedAtUnixMs, row.UpdatedAtUnixMs), nil
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
	return worktreeRecordFromParts(row.ID, row.WorkspaceID, row.CanonicalRootPath, row.IsMain != 0, row.Managed != 0, row.CreatedBranch != 0, row.OriginSessionID, row.GitMetadataJson, row.CreationBaseCommitOid, row.CreatedAtUnixMs, row.UpdatedAtUnixMs), nil
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
	creationBaseCommitOID := sql.NullString{}
	if record.CreationBaseCommitOID != nil {
		value := strings.TrimSpace(*record.CreationBaseCommitOID)
		if value == "" {
			return errors.New("worktree creation base commit oid must be non-blank when present")
		}
		creationBaseCommitOID = sql.NullString{String: value, Valid: true}
	}
	if err := s.queries.UpsertWorktree(ctx, sqlitegen.UpsertWorktreeParams{
		ID:                    strings.TrimSpace(record.ID),
		WorkspaceID:           strings.TrimSpace(record.WorkspaceID),
		CanonicalRootPath:     canonicalRoot,
		Managed:               managed,
		CreatedBranch:         createdBranch,
		OriginSessionID:       strings.TrimSpace(record.OriginSessionID),
		GitMetadataJson:       defaultJSONObject(record.GitMetadataJSON),
		CreationBaseCommitOid: creationBaseCommitOID,
		CreatedAtUnixMs:       createdAt.UnixMilli(),
		UpdatedAtUnixMs:       updatedAt.UnixMilli(),
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

func (s *Store) UpdateSessionExecutionTarget(ctx context.Context, update SessionExecutionTargetUpdate) error {
	if s == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	trimmedSessionID := strings.TrimSpace(update.SessionID)
	if trimmedSessionID == "" {
		return errors.New("session id is required")
	}
	workspaceID := sql.NullString{}
	if update.Workspace != nil {
		trimmedWorkspaceID := strings.TrimSpace(update.Workspace.ID)
		if trimmedWorkspaceID == "" {
			return errors.New("workspace id is required")
		}
		workspaceID = sql.NullString{String: trimmedWorkspaceID, Valid: true}
	}
	worktreeID := sql.NullString{}
	if update.Worktree != nil {
		if !workspaceID.Valid {
			return errors.New("workspace id is required when worktree is selected")
		}
		trimmedWorktreeID := strings.TrimSpace(update.Worktree.ID)
		if trimmedWorktreeID == "" {
			return ErrWorktreeIDRequired
		}
		record, err := s.GetWorktreeRecordByID(ctx, trimmedWorktreeID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(record.WorkspaceID) != workspaceID.String {
			return &WorktreeWorkspaceMismatchError{WorktreeID: trimmedWorktreeID, WorkspaceID: workspaceID.String}
		}
		worktreeID = sql.NullString{String: trimmedWorktreeID, Valid: true}
	}
	params := sqlitegen.UpdateSessionExecutionTargetByIDParams{
		WorkspaceID: workspaceID,
		WorktreeID:  worktreeID,
		CwdRelpath:  normalizeSessionCwdRelpath(update.CwdRelpath),
		SessionID:   trimmedSessionID,
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

type WorkspaceUnlinkRuntimeBlocker func(ctx context.Context, sessionIDs []string) ([]serverapi.ProjectWorkspaceUnlinkBlocker, func(), error)

func (s *Store) ListProjectSessionIDs(ctx context.Context, projectID string) ([]string, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	return s.queries.ListProjectSessionIDs(ctx, strings.TrimSpace(projectID))
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
	return lookupWorkspaceBindingWithQueries(ctx, s.queries, workspaceRoot)
}

func lookupWorkspaceBindingWithQueries(ctx context.Context, q *sqlitegen.Queries, workspaceRoot string) (Binding, error) {
	canonicalRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return Binding{}, err
	}
	rows, err := q.ListWorkspaceBindingsByCanonicalRoot(ctx, canonicalRoot)
	if err != nil {
		return Binding{}, err
	}
	return bindingFromCanonicalRootRows(canonicalRoot, rows)
}

func bindingFromCanonicalRootRows(canonicalRoot string, rows []sqlitegen.ListWorkspaceBindingsByCanonicalRootRow) (Binding, error) {
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
	return s.CreateProjectForWorkspaceWithKey(ctx, workspaceRoot, projectName, nil)
}

func (s *Store) CreateProjectForWorkspaceWithKey(ctx context.Context, workspaceRoot string, projectName string, projectKey *runtimeids.ProjectKey) (Binding, error) {
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
	return s.insertWorkspaceBinding(ctx, canonicalRoot, trimmedProjectName, projectKey, workspaceName, projectID, workspaceID, now, true)
}

type ProjectWorkspaceAttachResult struct {
	Binding  Binding
	Attached bool
}

func (s *Store) AttachWorkspaceToProject(ctx context.Context, projectID string, workspaceRoot string) (Binding, error) {
	result, err := s.AttachWorkspaceToProjectWithResult(ctx, projectID, workspaceRoot)
	return result.Binding, err
}

func (s *Store) AttachWorkspaceToProjectWithResult(ctx context.Context, projectID string, workspaceRoot string) (ProjectWorkspaceAttachResult, error) {
	if s == nil || s.queries == nil {
		return ProjectWorkspaceAttachResult{}, errors.New("metadata store is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return ProjectWorkspaceAttachResult{}, errors.New("project id is required")
	}
	canonicalRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return ProjectWorkspaceAttachResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectWorkspaceAttachResult{}, fmt.Errorf("begin workspace attach tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if _, err := q.AcquireWorkspaceRegistrationLock(ctx); err != nil {
		return ProjectWorkspaceAttachResult{}, fmt.Errorf("lock workspace attach: %w", err)
	}
	binding, attached, err := attachWorkspaceToProjectWithQueries(ctx, q, trimmedProjectID, canonicalRoot, time.Now().UTC())
	if err != nil {
		return ProjectWorkspaceAttachResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectWorkspaceAttachResult{}, fmt.Errorf("commit workspace attach tx: %w", err)
	}
	return ProjectWorkspaceAttachResult{Binding: binding, Attached: attached}, nil
}

// UpdateProjectMetadata updates a project's display name and, when projectKey is
// present, its project key in a single transaction. An absent projectKey leaves
// the existing key unchanged. Existing task short IDs are frozen at creation, so
// changing the key only affects the prefix applied to future tasks.
func (s *Store) UpdateProjectMetadata(ctx context.Context, projectID string, displayName string, projectKey *runtimeids.ProjectKey) error {
	if s == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return errors.New("project id is required")
	}
	var normalizedKey string
	if projectKey != nil {
		normalizedKey = projectKey.String()
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
				if IsSQLiteUniqueConstraint(err) {
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
	_, err := s.SetProjectDefaultWorkspaceAndGetSummary(ctx, projectID, workspaceID)
	return err
}

func (s *Store) SetProjectDefaultWorkspaceAndGetSummary(ctx context.Context, projectID string, workspaceID string) (serverapi.ProjectHomeSummary, error) {
	if s == nil || s.queries == nil {
		return serverapi.ProjectHomeSummary{}, errors.New("metadata store is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	if trimmedProjectID == "" {
		return serverapi.ProjectHomeSummary{}, errors.New("project id is required")
	}
	if trimmedWorkspaceID == "" {
		return serverapi.ProjectHomeSummary{}, errors.New("workspace id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return serverapi.ProjectHomeSummary{}, fmt.Errorf("begin default workspace tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	currentWorkspaceID, err := q.GetProjectPrimaryWorkspaceID(ctx, trimmedProjectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return serverapi.ProjectHomeSummary{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, trimmedProjectID)
		}
		return serverapi.ProjectHomeSummary{}, fmt.Errorf("get current project primary workspace: %w", err)
	}
	if strings.TrimSpace(currentWorkspaceID) == trimmedWorkspaceID {
		// Keep the read and the no-op mutation in the same transaction so the
		// authoritative response cannot fail after a committed change.
	} else {
		now := time.Now().UTC().UnixMilli()
		updatedProject, err := q.SetProjectPrimaryWorkspace(ctx, sqlitegen.SetProjectPrimaryWorkspaceParams{
			WorkspaceID:     trimmedWorkspaceID,
			UpdatedAtUnixMs: now,
			ProjectID:       trimmedProjectID,
		})
		if err != nil {
			return serverapi.ProjectHomeSummary{}, fmt.Errorf("set project primary workspace: %w", err)
		}
		if updatedProject == 0 {
			return serverapi.ProjectHomeSummary{}, fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, trimmedWorkspaceID)
		}
	}
	rows, err := q.ListProjectHomeSummaries(ctx, sqlitegen.ListProjectHomeSummariesParams{
		ProjectID:  sql.NullString{String: trimmedProjectID, Valid: true},
		LimitRows:  1,
		OffsetRows: 0,
	})
	if err != nil {
		return serverapi.ProjectHomeSummary{}, fmt.Errorf("read updated project home summary: %w", err)
	}
	if len(rows) == 0 {
		return serverapi.ProjectHomeSummary{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, trimmedProjectID)
	}
	if err := tx.Commit(); err != nil {
		return serverapi.ProjectHomeSummary{}, fmt.Errorf("commit default workspace tx: %w", err)
	}
	return projectHomeSummaryFromRow(rows[0]), nil
}

func (s *Store) UnlinkProjectWorkspace(ctx context.Context, projectID string, workspaceID string) ([]serverapi.ProjectWorkspaceUnlinkBlocker, error) {
	return s.UnlinkProjectWorkspaceWithPreflightBlockers(ctx, projectID, workspaceID, nil)
}

func (s *Store) UnlinkProjectWorkspaceWithPreflightBlockers(ctx context.Context, projectID string, workspaceID string, preflightBlockers []serverapi.ProjectWorkspaceUnlinkBlocker) ([]serverapi.ProjectWorkspaceUnlinkBlocker, error) {
	return s.UnlinkProjectWorkspaceWithRuntimeBlockers(ctx, projectID, workspaceID, preflightBlockers, nil)
}

func (s *Store) UnlinkProjectWorkspaceWithRuntimeBlockers(ctx context.Context, projectID string, workspaceID string, preflightBlockers []serverapi.ProjectWorkspaceUnlinkBlocker, runtimeBlocker WorkspaceUnlinkRuntimeBlocker) ([]serverapi.ProjectWorkspaceUnlinkBlocker, error) {
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
	blockers = append(append([]serverapi.ProjectWorkspaceUnlinkBlocker{}, preflightBlockers...), blockers...)
	preparedSessionIDs, err := s.queries.ListWorkspaceSessionIDs(ctx, sql.NullString{String: trimmedWorkspaceID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list workspace sessions for runtime blockers: %w", err)
	}
	releases := make([]func(), 0, 2)
	defer func() {
		for index := len(releases) - 1; index >= 0; index-- {
			releases[index]()
		}
	}()
	collectRuntimeBlockers := func(sessionIDs []string) error {
		if runtimeBlocker == nil {
			return nil
		}
		runtimeBlockers, release, err := runtimeBlocker(ctx, sessionIDs)
		if release != nil {
			releases = append(releases, release)
		}
		if err != nil {
			return err
		}
		blockers = append(blockers, runtimeBlockers...)
		return nil
	}
	if err := collectRuntimeBlockers(preparedSessionIDs); err != nil {
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
	locked, err := q.AcquireWorkspaceUnlinkWriteLock(ctx, sqlitegen.AcquireWorkspaceUnlinkWriteLockParams{
		ProjectID:   trimmedProjectID,
		WorkspaceID: trimmedWorkspaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("lock workspace unlink: %w", err)
	}
	if locked == 0 {
		return nil, fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, trimmedWorkspaceID)
	}
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
	commitSessionIDs, err := q.ListWorkspaceSessionIDs(ctx, sql.NullString{String: trimmedWorkspaceID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list workspace sessions for commit: %w", err)
	}
	blockers, err = workspaceUnlinkBlockersWithQueries(ctx, q, trimmedProjectID, workspace)
	if err != nil {
		return nil, err
	}
	if len(blockers) > 0 {
		if err := collectRuntimeBlockers(commitSessionIDs); err != nil {
			return nil, err
		}
		return blockers, nil
	}
	if !SessionIDSetsEqual(preparedSessionIDs, commitSessionIDs) {
		return nil, &serverapi.WorkspaceDetachConflictError{
			ProjectID:   trimmedProjectID,
			WorkspaceID: trimmedWorkspaceID,
		}
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

func (s *Store) ListWorkspaceSessionIDs(ctx context.Context, workspaceID string) ([]string, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	trimmed := strings.TrimSpace(workspaceID)
	return s.queries.ListWorkspaceSessionIDs(ctx, sql.NullString{String: trimmed, Valid: trimmed != ""})
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
	workspaceID := sql.NullString{String: workspace.ID, Valid: strings.TrimSpace(workspace.ID) != ""}
	nonTerminalTasks, err := q.CountNonTerminalTasksBySourceWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("count non-terminal workspace tasks: %w", err)
	}
	addCountBlocker("non_terminal_tasks", "Active or non-terminal tasks still depend on this workspace.", nonTerminalTasks)
	executableCurrentNodes, err := q.CountExecutableCurrentNodesByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("count executable workspace current nodes: %w", err)
	}
	addCountBlocker("executable_current_nodes", "Executable current nodes still depend on this workspace.", executableCurrentNodes)
	worktrees, err := q.CountWorktreesByWorkspace(ctx, workspace.ID)
	if err != nil {
		return nil, fmt.Errorf("count workspace worktrees: %w", err)
	}
	addCountBlocker("managed_owned_worktrees", "Worktrees still depend on this workspace.", worktrees)
	missingSnapshots, err := q.CountTasksMissingSourceWorkspaceSnapshot(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("count missing workspace snapshots: %w", err)
	}
	rootDisplayNameSnapshots, err := q.ListTasksMissingSourceWorkspaceDisplayName(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list root workspace snapshots: %w", err)
	}
	for _, metadataJSON := range rootDisplayNameSnapshots {
		var payload struct {
			SourceWorkspaceSnapshot struct {
				RootPath string `json:"root_path"`
			} `json:"source_workspace_snapshot"`
		}
		if err := unmarshalStoredJSON(metadataJSON, &payload); err == nil &&
			serverapi.IsFilesystemRootPath(payload.SourceWorkspaceSnapshot.RootPath) {
			missingSnapshots--
		}
	}
	missingSessionSnapshots, err := q.CountSessionsMissingWorkspaceSnapshot(ctx, workspace.ID)
	if err != nil {
		return nil, fmt.Errorf("count missing session workspace snapshots: %w", err)
	}
	addCountBlocker(
		"missing_history_snapshot",
		"Historical task or retained Session references do not have a durable workspace path/name snapshot.",
		missingSnapshots+missingSessionSnapshots,
	)
	return blockers, nil
}

func (s *Store) RebindWorkspace(ctx context.Context, oldWorkspaceRoot string, newWorkspaceRoot string) (Binding, error) {
	prepared, err := s.PrepareWorkspaceRebind(ctx, oldWorkspaceRoot)
	if err != nil {
		return Binding{}, err
	}
	return s.RebindWorkspaceWithExpectedBinding(
		ctx,
		oldWorkspaceRoot,
		newWorkspaceRoot,
		prepared.ProjectID,
		prepared.WorkspaceID,
	)
}

func (s *Store) PrepareWorkspaceRebind(ctx context.Context, oldWorkspaceRoot string) (Binding, error) {
	if s == nil || s.queries == nil {
		return Binding{}, errors.New("metadata store is required")
	}
	oldCanonicalRoot, err := canonicalFilesystemPath(oldWorkspaceRoot)
	if err != nil {
		return Binding{}, err
	}
	rows, err := s.queries.ListWorkspaceBindingsByCanonicalRoot(ctx, oldCanonicalRoot)
	if err != nil {
		return Binding{}, err
	}
	binding, err := bindingFromCanonicalRootRows(oldCanonicalRoot, rows)
	if errors.Is(err, sql.ErrNoRows) {
		return Binding{}, serverapi.ErrWorkspaceNotRegistered
	}
	return binding, err
}

func (s *Store) RebindWorkspaceWithExpectedBinding(
	ctx context.Context,
	oldWorkspaceRoot string,
	newWorkspaceRoot string,
	expectedProjectID string,
	expectedWorkspaceID string,
) (Binding, error) {
	if s == nil || s.queries == nil {
		return Binding{}, errors.New("metadata store is required")
	}
	trimmedExpectedProjectID := strings.TrimSpace(expectedProjectID)
	trimmedExpectedWorkspaceID := strings.TrimSpace(expectedWorkspaceID)
	if trimmedExpectedProjectID == "" {
		return Binding{}, errors.New("expected project id is required")
	}
	if trimmedExpectedWorkspaceID == "" {
		return Binding{}, errors.New("expected workspace id is required")
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
	if oldWorkspace.ProjectID != trimmedExpectedProjectID || oldWorkspace.ID != trimmedExpectedWorkspaceID {
		return Binding{}, fmt.Errorf(
			"workspace rebind preparation was invalidated: expected_project_id=%q expected_workspace_id=%q current_project_id=%q current_workspace_id=%q",
			trimmedExpectedProjectID,
			trimmedExpectedWorkspaceID,
			oldWorkspace.ProjectID,
			oldWorkspace.ID,
		)
	}
	if newCanonicalRoot == oldWorkspace.CanonicalRootPath {
		if err := tx.Commit(); err != nil {
			return Binding{}, fmt.Errorf("commit workspace rebind noop tx: %w", err)
		}
		return s.lookupProjectWorkspaceBinding(ctx, oldWorkspace.ProjectID, newCanonicalRoot)
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
		if IsSQLiteUniqueConstraint(err) {
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
			if IsSQLiteUniqueConstraint(updateErr) {
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
	return lookupProjectWorkspaceBindingWithQueries(ctx, s.queries, projectID, canonicalRoot)
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
	binding, err := bindingFromCanonicalRootRows(canonicalRoot, rows)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return Binding{}, fmt.Errorf("commit workspace registration lookup tx: %w", err)
		}
		return binding, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Binding{}, err
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
	storedProjectKey, err := setInitialProjectKey(ctx, q, projectID, displayName, nil, now.UnixMilli())
	if err != nil {
		return Binding{}, err
	}
	inserted, err := insertWorkspaceBindingWithQueries(ctx, q, workspaceBindingInsert{
		ID:            workspaceID,
		ProjectID:     projectID,
		CanonicalRoot: canonicalRoot,
		UpdatedAt:     now,
		Primary:       true,
	})
	if err != nil {
		return Binding{}, err
	}
	if !inserted {
		return Binding{}, fmt.Errorf("workspace %q could not be registered", canonicalRoot)
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
	return bindingFromWorkspaceFields(
		row.ProjectID,
		row.ProjectKey,
		row.ProjectDisplayName,
		row.WorkspaceID,
		row.WorkspaceRoot,
	)
}

func bindingFromWorkspaceFields(projectID string, projectKey string, projectName string, workspaceID string, workspaceRoot string) Binding {
	return Binding{
		ProjectID:       projectID,
		ProjectKey:      projectKey,
		ProjectName:     projectName,
		WorkspaceID:     workspaceID,
		CanonicalRoot:   workspaceRoot,
		WorkspaceName:   filepath.Base(workspaceRoot),
		WorkspaceStatus: availabilityForPath(workspaceRoot),
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

func IsSQLiteUniqueConstraint(err error) bool {
	return isSQLiteConstraint(err, sqlite3.SQLITE_CONSTRAINT_UNIQUE)
}

func IsSQLiteForeignKeyConstraint(err error) bool {
	return isSQLiteConstraint(err, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY)
}

func IsSQLiteTriggerConstraint(err error) bool {
	return isSQLiteConstraint(err, sqlite3.SQLITE_CONSTRAINT_TRIGGER)
}

func isSQLiteConstraint(err error, code int) bool {
	var sqliteErr *sqlitedriver.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code() == code
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
			if IsSQLiteUniqueConstraint(err) {
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

func setInitialProjectKey(ctx context.Context, q *sqlitegen.Queries, projectID string, displayName string, projectKey *runtimeids.ProjectKey, updatedAtUnixMs int64) (string, error) {
	if projectKey == nil {
		if err := setMissingProjectKey(ctx, q, projectID, displayName, updatedAtUnixMs); err != nil {
			return "", err
		}
		state, err := q.GetProjectKeyState(ctx, projectID)
		if err != nil {
			return "", fmt.Errorf("get allocated project key: %w", err)
		}
		return strings.TrimSpace(state.ProjectKey), nil
	}
	normalizedKey := projectKey.String()
	updated, err := q.SetProjectKey(ctx, sqlitegen.SetProjectKeyParams{ProjectKey: normalizedKey, UpdatedAtUnixMs: updatedAtUnixMs, ProjectID: projectID})
	if err != nil {
		if IsSQLiteUniqueConstraint(err) {
			return "", fmt.Errorf("%w: %q", ErrProjectKeyAlreadyInUse, normalizedKey)
		}
		return "", fmt.Errorf("set project key for %q: %w", projectID, err)
	}
	if updated == 0 {
		return "", fmt.Errorf("set project key for %q: project not found", projectID)
	}
	return normalizedKey, nil
}

func (s *Store) SetProjectKey(ctx context.Context, projectID string, rawProjectKey string) error {
	if s == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return errors.New("project id is required")
	}
	projectKey, err := runtimeids.ParseProjectKey(rawProjectKey)
	if err != nil {
		return err
	}
	key := projectKey.String()
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
	if strings.TrimSpace(state.ProjectKey) == key {
		return nil
	}
	updated, err := q.SetProjectKey(ctx, sqlitegen.SetProjectKeyParams{
		ProjectKey:      key,
		UpdatedAtUnixMs: time.Now().UTC().UnixMilli(),
		ProjectID:       trimmedProjectID,
	})
	if err != nil {
		if IsSQLiteUniqueConstraint(err) {
			return fmt.Errorf("%w: %q", ErrProjectKeyAlreadyInUse, key)
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
	if _, err := runtimeids.ParseProjectKey(base); err == nil && !used[base] {
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
		if _, err := runtimeids.ParseProjectKey(candidate); err == nil && !used[candidate] {
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

type workspaceBindingInsert struct {
	ID            string
	ProjectID     string
	CanonicalRoot string
	UpdatedAt     time.Time
	Primary       bool
}

func insertWorkspaceBindingWithQueries(ctx context.Context, q *sqlitegen.Queries, insert workspaceBindingInsert) (bool, error) {
	rows, err := q.InsertWorkspaceBinding(ctx, sqlitegen.InsertWorkspaceBindingParams{
		ID:                insert.ID,
		ProjectID:         insert.ProjectID,
		CanonicalRootPath: insert.CanonicalRoot,
		GitMetadataJson:   "{}",
		CreatedAtUnixMs:   insert.UpdatedAt.UnixMilli(),
		UpdatedAtUnixMs:   insert.UpdatedAt.UnixMilli(),
	})
	if err != nil {
		if IsSQLiteForeignKeyConstraint(err) {
			return false, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, insert.ProjectID)
		}
		return false, fmt.Errorf("insert workspace binding: %w", err)
	}
	if rows == 0 {
		return false, nil
	}
	if !insert.Primary {
		return true, nil
	}
	updatedProject, err := q.SetProjectPrimaryWorkspace(ctx, sqlitegen.SetProjectPrimaryWorkspaceParams{
		WorkspaceID:     insert.ID,
		UpdatedAtUnixMs: insert.UpdatedAt.UnixMilli(),
		ProjectID:       insert.ProjectID,
	})
	if err != nil {
		return false, fmt.Errorf("set project primary workspace: %w", err)
	}
	if updatedProject == 0 {
		return false, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, insert.ProjectID)
	}
	return true, nil
}

func (s *Store) insertWorkspaceBinding(ctx context.Context, canonicalRoot string, projectDisplayName string, projectKey *runtimeids.ProjectKey, workspaceDisplayName string, projectID string, workspaceID string, now time.Time, isPrimary bool) (Binding, error) {
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
	inserted, err := insertWorkspaceBindingWithQueries(ctx, q, workspaceBindingInsert{
		ID:            workspaceID,
		ProjectID:     projectID,
		CanonicalRoot: canonicalRoot,
		UpdatedAt:     now,
		Primary:       isPrimary,
	})
	if err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return Binding{}, fmt.Errorf("rollback workspace binding tx: %w", rollbackErr)
		}
		return Binding{}, fmt.Errorf("insert workspace binding: %w", err)
	}
	if !inserted {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return Binding{}, fmt.Errorf("rollback workspace binding tx: %w", rollbackErr)
		}
		if binding, recovered := s.recoverWorkspaceBindingAfterCanonicalRootConflict(ctx, canonicalRoot, workspaceID); recovered {
			return binding, nil
		}
		return Binding{}, fmt.Errorf("insert workspace binding: canonical root %q conflict was not recoverable", canonicalRoot)
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

func (s *Store) ListProjectHomeSummaries(ctx context.Context, pageSize int, offset int) ([]serverapi.ProjectHomeSummary, error) {
	return s.listProjectHomeSummaries(ctx, sql.NullString{}, pageSize, offset)
}

func (s *Store) GetProjectHomeSummary(ctx context.Context, projectID string) (serverapi.ProjectHomeSummary, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	rows, err := s.listProjectHomeSummaries(
		ctx,
		sql.NullString{String: trimmedProjectID, Valid: true},
		1,
		0,
	)
	if err != nil {
		return serverapi.ProjectHomeSummary{}, err
	}
	if len(rows) == 0 {
		return serverapi.ProjectHomeSummary{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, trimmedProjectID)
	}
	return rows[0], nil
}

func (s *Store) listProjectHomeSummaries(ctx context.Context, projectID sql.NullString, pageSize int, offset int) ([]serverapi.ProjectHomeSummary, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	rows, err := s.queries.ListProjectHomeSummaries(ctx, sqlitegen.ListProjectHomeSummariesParams{
		ProjectID:  projectID,
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
	workspaces, err := s.ListProjectWorkspaces(ctx, projectID)
	if err != nil {
		return clientui.ProjectOverview{}, err
	}
	return clientui.ProjectOverview{
		Project:    projectSummaryFromRow(project.ID, project.ProjectKey, project.DisplayName, project.RootPath, project.SessionCount, project.LatestActivityUnixMs),
		Workspaces: workspaces,
	}, nil
}

func (s *Store) ListProjectWorkspaces(ctx context.Context, projectID string) ([]clientui.ProjectWorkspaceSummary, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	rows, err := s.queries.ListProjectWorkspaces(ctx, sqlitegen.ListProjectWorkspacesParams{
		ProjectID:                strings.TrimSpace(projectID),
		WorkspaceCollectionLimit: int64(ProjectWorkspaceCollectionLimit),
	})
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
	rows, err := s.queries.ListProjectWorkspacesPage(ctx, sqlitegen.ListProjectWorkspacesPageParams{
		ProjectID:                strings.TrimSpace(projectID),
		WorkspaceCollectionLimit: int64(ProjectWorkspaceCollectionLimit),
		LimitRows:                int64(pageSize),
		OffsetRows:               int64(offset),
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

func (s *Store) ListSessionPage(ctx context.Context, req serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error) {
	if s == nil || s.queries == nil {
		return serverapi.SessionPageResponse{}, errors.New("metadata store is required")
	}
	if err := req.Validate(); err != nil {
		return serverapi.SessionPageResponse{}, err
	}
	window, err := req.ResolveWindow()
	if err != nil {
		return serverapi.SessionPageResponse{}, err
	}
	rows, err := s.queries.ListSessionPage(ctx, sqlitegen.ListSessionPageParams{
		ProjectID:  strings.TrimSpace(req.ProjectID),
		Category:   sql.NullString{String: string(req.Category), Valid: true},
		PageLimit:  int64(window.Limit + 1),
		PageOffset: int64(window.Offset),
	})
	if err != nil {
		return serverapi.SessionPageResponse{}, fmt.Errorf("list session page: %w", err)
	}
	rows, nextOffset := serverapi.TrimOffsetLookahead(window, rows)
	out := serverapi.SessionPageResponse{
		ProjectID:  strings.TrimSpace(req.ProjectID),
		Category:   req.Category,
		Sessions:   make([]clientui.SessionSummary, 0, len(rows)),
		NextOffset: nextOffset,
	}
	for _, row := range rows {
		sessionID, err := runtimeids.ParseSessionID(row.ID)
		if err != nil {
			return serverapi.SessionPageResponse{}, fmt.Errorf("validate listed session id %q: %w", row.ID, err)
		}
		category, err := sessioncontract.ParseSessionCategory(row.Category)
		if err != nil {
			return serverapi.SessionPageResponse{}, fmt.Errorf("validate listed session category for %q: %w", row.ID, err)
		}
		out.Sessions = append(out.Sessions, clientui.SessionSummary{
			SessionID:          sessionID,
			Category:           category,
			Name:               row.Name,
			FirstPromptPreview: row.FirstPromptPreview,
			UpdatedAt:          timeFromStoredTimestamp(row.UpdatedAtUnixMs),
		})
	}
	return out, nil
}

func (s *Store) ResolveSessionExecutionTarget(ctx context.Context, sessionID string) (clientui.SessionExecutionTarget, error) {
	row, err := s.resolveSessionExecutionTargetRow(ctx, sessionID)
	if err != nil {
		return clientui.SessionExecutionTarget{}, err
	}
	return sessionExecutionTargetFromRow(row), nil
}

type ActiveProjectSession struct {
	SessionID       runtimeids.SessionID
	OwningProjectID string
	ExecutionTarget clientui.SessionExecutionTarget
}

func (s *Store) ResolveActiveProjectSession(ctx context.Context, rawSessionID string) (ActiveProjectSession, error) {
	sessionID, err := runtimeids.ParseSessionID(rawSessionID)
	if err != nil {
		return ActiveProjectSession{}, err
	}
	row, err := s.resolveSessionExecutionTargetRow(ctx, sessionID.String())
	if err != nil {
		return ActiveProjectSession{}, err
	}
	if strings.TrimSpace(row.SessionID) != sessionID.String() {
		return ActiveProjectSession{}, fmt.Errorf(
			"resolved session identity mismatch: requested %q, loaded %q",
			sessionID.String(),
			row.SessionID,
		)
	}
	projectID := strings.TrimSpace(row.ProjectID)
	if projectID == "" {
		return ActiveProjectSession{}, fmt.Errorf("session %q owning project id is required", sessionID)
	}
	return ActiveProjectSession{
		SessionID:       sessionID,
		OwningProjectID: projectID,
		ExecutionTarget: sessionExecutionTargetFromRow(row),
	}, nil
}

func (s *Store) ResolveOptionalSessionExecutionTarget(ctx context.Context, sessionID string) (*clientui.SessionExecutionTarget, error) {
	row, err := s.resolveSessionExecutionTargetRow(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !row.ExecutionTargetWorkspaceBinding.Valid && !row.WorktreeID.Valid {
		return nil, nil
	}
	target := sessionExecutionTargetFromRow(row)
	return &target, nil
}

func (s *Store) ResolveSessionNavigationBinding(ctx context.Context, sessionID string) (serverapi.SessionNavigationBinding, error) {
	row, err := s.resolveSessionExecutionTargetRow(ctx, sessionID)
	if err != nil {
		return serverapi.SessionNavigationBinding{}, err
	}
	binding := serverapi.SessionNavigationBinding{
		ProjectID:   strings.TrimSpace(row.ProjectID),
		WorkspaceID: strings.TrimSpace(row.WorkspaceID),
	}
	if err := binding.Validate(); err != nil {
		return serverapi.SessionNavigationBinding{}, err
	}
	return binding, nil
}

func (s *Store) ResolveSessionProjectWorkspaceBoundary(ctx context.Context, sessionID string) (ProjectWorkspaceBoundary, error) {
	row, err := s.resolveSessionExecutionTargetRow(ctx, sessionID)
	if err != nil {
		return ProjectWorkspaceBoundary{}, err
	}
	projectID := strings.TrimSpace(row.ProjectID)
	if projectID == "" {
		return ProjectWorkspaceBoundary{}, errors.New("session project id is required")
	}
	return s.ResolveProjectWorkspaceBoundary(ctx, projectID)
}

func (s *Store) ResolveProjectWorkspaceBoundary(ctx context.Context, projectID string) (ProjectWorkspaceBoundary, error) {
	if s == nil || s.queries == nil {
		return ProjectWorkspaceBoundary{}, errors.New("metadata store is required")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return ProjectWorkspaceBoundary{}, errors.New("project id is required")
	}
	workspaces, err := s.queries.ListProjectWorkspaceBoundary(ctx, sqlitegen.ListProjectWorkspaceBoundaryParams{
		ProjectID:                projectID,
		WorkspaceCollectionLimit: int64(ProjectWorkspaceCollectionLimit),
	})
	if err != nil {
		return ProjectWorkspaceBoundary{}, err
	}
	boundary := ProjectWorkspaceBoundary{
		ProjectID:  projectID,
		Workspaces: make([]ProjectWorkspace, 0, len(workspaces)),
	}
	for _, workspace := range workspaces {
		root := strings.TrimSpace(workspace.RootPath)
		if root == "" {
			return ProjectWorkspaceBoundary{}, fmt.Errorf("project workspace %q has empty root path", workspace.ID)
		}
		workspaceID := strings.TrimSpace(workspace.ID)
		if workspaceID == "" {
			return ProjectWorkspaceBoundary{}, fmt.Errorf("project workspace %q has empty workspace id", root)
		}
		boundary.Workspaces = append(boundary.Workspaces, ProjectWorkspace{
			WorkspaceID:       &workspaceID,
			CanonicalRoot:     root,
			AttachmentOrdinal: len(boundary.Workspaces),
		})
	}
	if err := boundary.Validate(); err != nil {
		return ProjectWorkspaceBoundary{}, err
	}
	return boundary, nil
}

func (s *Store) ProjectWorkspaceAttached(ctx context.Context, projectID string, root string) (bool, error) {
	if s == nil || s.queries == nil {
		return false, errors.New("metadata store is required")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return false, errors.New("project id is required")
	}
	selector, err := serverapi.NewProjectWorkspaceSelectorForRoot(root)
	if err != nil {
		return false, err
	}
	_, err = s.ResolveProjectWorkspaceSelector(ctx, projectID, selector)
	if errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) SessionBelongsToProject(ctx context.Context, sessionID string, projectID string) (bool, error) {
	row, err := s.resolveSessionExecutionTargetRow(ctx, sessionID)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(row.ProjectID) == strings.TrimSpace(projectID), nil
}

// SessionHasWorkflowTask reports whether direct Session ownership links the
// Session to a retained workflow Task.
func (s *Store) SessionHasWorkflowTask(ctx context.Context, sessionID string) (bool, error) {
	taskID, err := s.WorkflowTaskIDForSession(ctx, sessionID)
	if err != nil {
		return false, err
	}
	return taskID != nil, nil
}

// WorkflowTaskIDForSession resolves the direct workflow Task ownership of a
// Session. A Session without Task ownership returns nil.
func (s *Store) WorkflowTaskIDForSession(ctx context.Context, sessionID string) (*string, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return nil, errors.New("session id is required")
	}
	taskIDs, err := s.queries.ListSessionWorkflowTaskIDs(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list session workflow task ids: %w", err)
	}
	if len(taskIDs) == 0 {
		return nil, nil
	}
	if len(taskIDs) != 1 || !taskIDs[0].Valid || strings.TrimSpace(taskIDs[0].String) == "" {
		return nil, fmt.Errorf("session %q has invalid workflow task ownership", id)
	}
	taskID := taskIDs[0].String
	return &taskID, nil
}

func (s *Store) resolveSessionExecutionTargetRow(ctx context.Context, sessionID string) (sqlitegen.GetSessionExecutionTargetByIDRow, error) {
	if s == nil || s.queries == nil {
		return sqlitegen.GetSessionExecutionTargetByIDRow{}, errors.New("metadata store is required")
	}
	row, err := s.queries.GetSessionExecutionTargetByID(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return sqlitegen.GetSessionExecutionTargetByIDRow{}, fmt.Errorf("get session execution target: %w", err)
	}
	return row, nil
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session snapshot import tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.upsertSessionSnapshotWithQueries(
		ctx,
		s.queries.WithTx(tx),
		snapshot,
		sessionSnapshotUpsertOptions{},
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session snapshot import tx: %w", err)
	}
	return nil
}

type sessionSnapshotUpsertOptions struct {
	workspaceID        string
	forceLaunchVisible bool
}

func (s *Store) upsertWorkspaceChatMaterializationSnapshot(
	ctx context.Context,
	workspaceID string,
	snapshot session.PersistedStoreSnapshot,
) error {
	if s == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	if trimmedWorkspaceID == "" {
		return errors.New("workspace id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin workspace Chat materialization tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if err := s.upsertSessionSnapshotWithQueries(
		ctx,
		q,
		snapshot,
		sessionSnapshotUpsertOptions{
			workspaceID:        trimmedWorkspaceID,
			forceLaunchVisible: true,
		},
	); err != nil {
		return err
	}
	rows, err := q.ReplaceWorkspaceChatDraft(ctx, sqlitegen.ReplaceWorkspaceChatDraftParams{
		ChatDraftJson: sql.NullString{},
		ID:            trimmedWorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("consume workspace Chat draft: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, trimmedWorkspaceID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workspace Chat materialization tx: %w", err)
	}
	return nil
}

func (s *Store) upsertSessionSnapshotWithQueries(
	ctx context.Context,
	q *sqlitegen.Queries,
	snapshot session.PersistedStoreSnapshot,
	options sessionSnapshotUpsertOptions,
) error {
	category, err := nullableSessionCategory(snapshot.Meta.SessionID, snapshot.Meta.Category)
	if err != nil {
		return err
	}
	if snapshot.Meta.Continuation != nil {
		continuation, err := session.NormalizeContinuationContext(*snapshot.Meta.Continuation)
		if err != nil {
			return fmt.Errorf("validate session continuation: %w", err)
		}
		snapshot.Meta.Continuation = continuation
	}
	chatSettings, err := session.NormalizeChatSettingsOverrides(snapshot.Meta.ChatSettings)
	if err != nil {
		return fmt.Errorf("validate session Chat settings: %w", err)
	}
	snapshot.Meta.ChatSettings = chatSettings
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
	if _, err := q.AcquireWorkspaceRegistrationLock(ctx); err != nil {
		return fmt.Errorf("lock session snapshot import: %w", err)
	}
	existingTarget, targetErr := q.GetSessionExecutionTargetByID(ctx, strings.TrimSpace(snapshot.Meta.SessionID))
	if targetErr != nil && !errors.Is(targetErr, sql.ErrNoRows) {
		return fmt.Errorf("get existing session execution target: %w", targetErr)
	}
	binding := Binding{}
	workspaceRoot := snapshot.Meta.WorkspaceRoot
	workspaceContainer := snapshot.Meta.WorkspaceContainer
	persistedWorktreeReminder := snapshot.Meta.WorktreeReminder
	worktreeID := sql.NullString{}
	cwdRelpath := "."
	if options.workspaceID != "" {
		workspace, err := q.GetWorkspaceByID(ctx, options.workspaceID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %q", serverapi.ErrWorkspaceNotRegistered, options.workspaceID)
		}
		if err != nil {
			return fmt.Errorf("get materialization workspace: %w", err)
		}
		sameRoot, err := canonicalWorkspaceRootsEqual(snapshot.Meta.WorkspaceRoot, workspace.CanonicalRootPath)
		if err != nil {
			return err
		}
		if !sameRoot ||
			(targetErr == nil &&
				(existingTarget.ProjectID != workspace.ProjectID ||
					existingTarget.WorkspaceID != workspace.ID)) {
			return fmt.Errorf(
				"%w: workspace %q same_root=%t session %q",
				serverapi.ErrWorkspaceNotRegistered,
				options.workspaceID,
				sameRoot,
				strings.TrimSpace(snapshot.Meta.SessionID),
			)
		}
		binding = bindingFromWorkspaceFields(
			workspace.ProjectID,
			"",
			"",
			workspace.ID,
			workspace.CanonicalRootPath,
		)
		workspaceRoot = workspace.CanonicalRootPath
		workspaceContainer = filepath.Base(workspace.CanonicalRootPath)
		persistedWorktreeReminder = nil
	} else if targetErr == nil {
		binding.ProjectID = existingTarget.ProjectID
		binding.WorkspaceID = existingTarget.WorkspaceID
		authoritativeRoot := strings.TrimSpace(existingTarget.WorkspaceRoot)
		if authoritativeRoot == "" {
			return fmt.Errorf("session %q: %w", snapshot.Meta.SessionID, errSessionWorkspaceRootRequired)
		}
		sameRoot, err := canonicalWorkspaceRootsEqual(snapshot.Meta.WorkspaceRoot, authoritativeRoot)
		if err != nil {
			return err
		}
		if !sameRoot {
			persistedWorktreeReminder = nil
		}
		workspaceRoot = authoritativeRoot
		workspaceContainer = strings.TrimSpace(existingTarget.WorkspaceSnapshotName)
		if workspaceContainer == "" {
			return fmt.Errorf("session %q: %w", snapshot.Meta.SessionID, errSessionWorkspaceContainerRequired)
		}
		worktreeID = existingTarget.WorktreeID
		cwdRelpath = normalizeSessionCwdRelpath(existingTarget.CwdRelpath)
	} else {
		binding, err = lookupWorkspaceBindingWithQueries(ctx, q, snapshot.Meta.WorkspaceRoot)
		if errors.Is(err, sql.ErrNoRows) {
			return serverapi.ErrWorkspaceNotRegistered
		}
		if err != nil {
			return err
		}
	}
	metadataJSON, err := marshalJSON(sessionMetadataDocument{
		WorkspaceRoot:                   workspaceRoot,
		WorkspaceContainer:              workspaceContainer,
		ChatSettings:                    snapshot.Meta.ChatSettings,
		ConversationEstablished:         snapshot.Meta.ConversationEstablished,
		PromptCacheLineageGeneration:    snapshot.Meta.PromptCacheLineageGeneration,
		HeadlessActive:                  snapshot.Meta.HeadlessActive,
		CompactionSoonReminderIssued:    snapshot.Meta.CompactionSoonReminderIssued,
		GeneratedRecoveredWarningIssued: snapshot.Meta.GeneratedRecoveredWarningIssued,
		PendingModelRecovery:            snapshot.Meta.PendingModelRecovery,
		WorktreeReminder:                persistedWorktreeReminder,
		Goal:                            snapshot.Meta.Goal,
	})
	if err != nil {
		return err
	}
	launchVisible := int64(0)
	if options.forceLaunchVisible || sessionLaunchVisible(snapshot.Meta) {
		launchVisible = 1
	}
	if err := q.UpsertSession(ctx, sqlitegen.UpsertSessionParams{
		ID:                   snapshot.Meta.SessionID,
		ProjectID:            binding.ProjectID,
		WorkspaceID:          sql.NullString{String: binding.WorkspaceID, Valid: strings.TrimSpace(binding.WorkspaceID) != ""},
		WorktreeID:           worktreeID,
		ArtifactRelpath:      relpath,
		Name:                 snapshot.Meta.Name,
		FirstPromptPreview:   snapshot.Meta.FirstPromptPreview,
		InputDraft:           snapshot.Meta.InputDraft,
		PreviousSessionID:    nullableSessionID(snapshot.Meta.PreviousSessionID),
		ParentAgentSessionID: nullableSessionID(snapshot.Meta.ParentAgentSessionID),
		Category:             category,
		CreatedAtUnixMs:      snapshot.Meta.CreatedAt.UTC().UnixMilli(),
		UpdatedAtUnixMs:      snapshot.Meta.UpdatedAt.UTC().UnixMilli(),
		LastSequence:         snapshot.Meta.LastSequence,
		ModelRequestCount:    snapshot.Meta.ModelRequestCount,
		LaunchVisible:        launchVisible,
		CwdRelpath:           cwdRelpath,
		ContinuationJson:     continuationJSON,
		LockedJson:           lockedJSON,
		UsageStateJson:       usageStateJSON,
		MetadataJson:         metadataJSON,
	}); err != nil {
		return fmt.Errorf("upsert session snapshot: %w", err)
	}
	return nil
}

func canonicalWorkspaceRootsEqual(left string, right string) (bool, error) {
	leftRoot, err := config.CanonicalWorkspaceRoot(left)
	if err != nil {
		return false, err
	}
	rightRoot, err := config.CanonicalWorkspaceRoot(right)
	if err != nil {
		return false, err
	}
	return leftRoot == rightRoot, nil
}

func availabilityForPath(path string) string {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "missing"
		}
		return "inaccessible"
	}
	return "available"
}

func PathAvailability(path string) clientui.ProjectAvailability {
	return clientui.ProjectAvailability(availabilityForPath(path))
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
	if meta.PreviousSessionID != nil || meta.ParentAgentSessionID != nil {
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
	category := sessionCategoryFromStored(row.Category)
	metadataPayload := sessionMetadataDocument{}
	if err := unmarshalStoredJSON(row.MetadataJson, &metadataPayload); err != nil {
		return session.Meta{}, fmt.Errorf("decode session metadata json: %w", err)
	}
	chatSettings, err := session.NormalizeChatSettingsOverrides(metadataPayload.ChatSettings)
	if err != nil {
		return session.Meta{}, fmt.Errorf("validate session Chat settings: %w", err)
	}
	var decodedContinuation session.ContinuationContext
	if err := unmarshalStoredJSON(row.ContinuationJson, &decodedContinuation); err != nil {
		return session.Meta{}, fmt.Errorf("decode continuation json: %w", err)
	}
	continuation, err := session.NormalizeContinuationContext(decodedContinuation)
	if err != nil {
		return session.Meta{}, fmt.Errorf("validate continuation json: %w", err)
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
	// An attached workspace row is authoritative; detached sessions use the
	// structured metadata payload owned by SQLite.
	workspaceRoot := strings.TrimSpace(row.WorkspaceRoot)
	if workspaceRoot == "" && strings.TrimSpace(metadataPayload.WorkspaceRoot) != "" {
		workspaceRoot = strings.TrimSpace(metadataPayload.WorkspaceRoot)
	}
	workspaceContainer := strings.TrimSpace(metadataPayload.WorkspaceContainer)
	if workspaceContainer == "" {
		workspaceContainer = filepath.Base(filepath.Clean(workspaceRoot))
	}
	previousSessionID, err := optionalSessionID(row.ID, "previous_session_id", row.PreviousSessionID)
	if err != nil {
		return session.Meta{}, err
	}
	parentAgentSessionID, err := optionalSessionID(row.ID, "parent_agent_session_id", row.ParentAgentSessionID)
	if err != nil {
		return session.Meta{}, err
	}
	return session.Meta{
		SessionID:                       row.ID,
		Category:                        category,
		Name:                            row.Name,
		FirstPromptPreview:              row.FirstPromptPreview,
		InputDraft:                      row.InputDraft,
		PreviousSessionID:               previousSessionID,
		ParentAgentSessionID:            parentAgentSessionID,
		WorkspaceRoot:                   workspaceRoot,
		WorkspaceContainer:              workspaceContainer,
		Continuation:                    continuation,
		ChatSettings:                    chatSettings,
		CreatedAt:                       timeFromStoredTimestamp(row.CreatedAtUnixMs),
		UpdatedAt:                       timeFromStoredTimestamp(row.UpdatedAtUnixMs),
		LastSequence:                    row.LastSequence,
		ConversationEstablished:         metadataPayload.ConversationEstablished,
		ModelRequestCount:               row.ModelRequestCount,
		PromptCacheLineageGeneration:    metadataPayload.PromptCacheLineageGeneration,
		HeadlessActive:                  metadataPayload.HeadlessActive,
		CompactionSoonReminderIssued:    metadataPayload.CompactionSoonReminderIssued,
		GeneratedRecoveredWarningIssued: metadataPayload.GeneratedRecoveredWarningIssued,
		PendingModelRecovery:            metadataPayload.PendingModelRecovery,
		WorktreeReminder:                metadataPayload.WorktreeReminder,
		Goal:                            metadataPayload.Goal,
		UsageState:                      usageState,
		Locked:                          locked,
	}, nil
}

func nullableSessionCategory(sessionID string, category *sessioncontract.SessionCategory) (sql.NullString, error) {
	if category == nil {
		return sql.NullString{}, nil
	}
	raw := string(*category)
	validated, err := sessioncontract.ParseSessionCategory(raw)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("session %q has invalid category %q: %w", sessionID, raw, err)
	}
	return sql.NullString{String: string(validated), Valid: true}, nil
}

func sessionCategoryFromStored(stored sql.NullString) *sessioncontract.SessionCategory {
	if !stored.Valid {
		return nil
	}
	category := sessioncontract.SessionCategory(stored.String)
	return &category
}

func nullableSessionID(sessionID *runtimeids.SessionID) sql.NullString {
	if sessionID == nil {
		return sql.NullString{}
	}
	if sessionID.IsZero() {
		panic("metadata persistence received an empty session provenance id")
	}
	return sql.NullString{String: sessionID.String(), Valid: true}
}

func optionalSessionID(ownerSessionID string, field string, stored sql.NullString) (*runtimeids.SessionID, error) {
	if !stored.Valid {
		return nil, nil
	}
	parsed, err := runtimeids.ParseSessionID(stored.String)
	if err != nil {
		return nil, fmt.Errorf("session %q has invalid %s %q: %w", ownerSessionID, field, stored.String, err)
	}
	return &parsed, nil
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
	workspaceName := displayNameForPath(row.WorkspaceRoot)
	if strings.TrimSpace(row.WorkspaceID) == "" && strings.TrimSpace(row.WorkspaceSnapshotName) != "" {
		workspaceName = strings.TrimSpace(row.WorkspaceSnapshotName)
	}
	baseRoot := strings.TrimSpace(row.WorkspaceRoot)
	var worktree *clientui.SessionExecutionWorktreeTarget
	if row.WorktreeID.Valid {
		worktreeRoot := ""
		if row.WorktreeRoot.Valid {
			worktreeRoot = row.WorktreeRoot.String
		}
		worktree = &clientui.SessionExecutionWorktreeTarget{
			ID:           row.WorktreeID.String,
			Name:         displayNameForPath(worktreeRoot),
			Root:         worktreeRoot,
			Availability: availabilityForOptionalPath(worktreeRoot),
		}
		if strings.TrimSpace(worktreeRoot) != "" {
			baseRoot = strings.TrimSpace(worktreeRoot)
		}
	}
	cwdRelpath := normalizeSessionCwdRelpath(row.CwdRelpath)
	effectiveWorkdir := effectiveWorkdirWithinRoot(baseRoot, cwdRelpath)
	return clientui.SessionExecutionTarget{
		WorkspaceID:           row.WorkspaceID,
		WorkspaceName:         workspaceName,
		WorkspaceRoot:         row.WorkspaceRoot,
		WorkspaceAvailability: clientui.ProjectAvailability(availabilityForOptionalPath(row.WorkspaceRoot)),
		Worktree:              worktree,
		CwdRelpath:            cwdRelpath,
		EffectiveWorkdir:      effectiveWorkdir,
	}
}

func worktreeRecordFromParts(id string, workspaceID string, canonicalRoot string, isMain bool, managed bool, createdBranch bool, originSessionID string, gitMetadataJSON string, creationBaseCommitOID sql.NullString, createdAtUnixMs int64, updatedAtUnixMs int64) WorktreeRecord {
	return WorktreeRecord{
		ID:                    id,
		WorkspaceID:           workspaceID,
		CanonicalRoot:         canonicalRoot,
		DisplayName:           displayNameForPath(canonicalRoot),
		Availability:          availabilityForOptionalPath(canonicalRoot),
		IsMain:                isMain,
		Managed:               managed,
		CreatedBranch:         createdBranch,
		OriginSessionID:       originSessionID,
		GitMetadataJSON:       gitMetadataJSON,
		CreationBaseCommitOID: OptionalString(creationBaseCommitOID),
		CreatedAt:             timeFromStoredTimestamp(createdAtUnixMs),
		UpdatedAt:             timeFromStoredTimestamp(updatedAtUnixMs),
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
	cleaned := filepath.Clean(relpath)
	if cleaned == "." || !filepath.IsLocal(cleaned) {
		return "", fmt.Errorf("session dir %q is outside persistence root %q: %w", target, root, ErrPathEscapesPersistenceRoot)
	}
	return filepath.ToSlash(cleaned), nil
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
		if info, lstatErr := os.Lstat(parent); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("resolve missing symlink %q: %w", parent, os.ErrNotExist)
		} else if lstatErr != nil && !errors.Is(lstatErr, os.ErrNotExist) {
			return "", lstatErr
		}
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

type workspaceChatMaterializationObserver struct {
	store       *Store
	workspaceID string
}

func (o sessionObserver) ObservePersistedStore(ctx context.Context, snapshot session.PersistedStoreSnapshot) error {
	if o.store == nil {
		return nil
	}
	return o.store.upsertSessionSnapshot(ctx, snapshot)
}

func (o workspaceChatMaterializationObserver) ObservePersistedStore(ctx context.Context, snapshot session.PersistedStoreSnapshot) error {
	if o.store == nil {
		return errors.New("metadata store is required")
	}
	return o.store.upsertWorkspaceChatMaterializationSnapshot(ctx, o.workspaceID, snapshot)
}
