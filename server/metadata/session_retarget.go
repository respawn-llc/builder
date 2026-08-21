package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"core/server/metadata/sqlitegen"
	"core/server/session"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

type SessionWorkspaceRetargetRequest struct {
	SessionID     string
	WorkspaceRoot string
	ProjectID     *string
}

type SessionWorkspaceRetargetPlan struct {
	SessionID                       string
	SourceProject                   serverapi.ProjectReference
	TargetProject                   serverapi.ProjectReference
	ExplicitProject                 bool
	TargetWorkspaceRoot             string
	SourceArtifactRelpath           string
	TargetArtifactRelpath           string
	SourceSessionDir                string
	TargetSessionDir                string
	SourceBinding                   *Binding
	SourceEffectiveWorkingDirectory string
	SourceCwdRelpath                string
	noOp                            bool
}

func (p SessionWorkspaceRetargetPlan) CrossProject() bool {
	return strings.TrimSpace(p.SourceProject.ID) != strings.TrimSpace(p.TargetProject.ID)
}

func (p SessionWorkspaceRetargetPlan) NoOp() bool {
	return p.noOp
}

type SessionWorkspaceRetargetResult struct {
	Binding                 Binding
	WorkspaceBindingCreated bool
	UpdatedAt               time.Time
	RebindReminder          *session.SessionRebindReminder
}

type SessionWorkspaceRetargetSource struct {
	Project                   serverapi.ProjectReference
	EffectiveWorkingDirectory string
}

func (s *Store) ResolveSessionWorkspaceRetargetSource(
	ctx context.Context,
	sessionID string,
) (SessionWorkspaceRetargetSource, error) {
	if s == nil || s.queries == nil {
		return SessionWorkspaceRetargetSource{}, errors.New("metadata store is required")
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return SessionWorkspaceRetargetSource{}, errors.New("session id is required")
	}
	state, err := getSessionWorkspaceRetargetState(ctx, s.queries, trimmedSessionID)
	if err != nil {
		return SessionWorkspaceRetargetSource{}, err
	}
	source, _, err := sessionRetargetSourceFromState(state)
	if err != nil {
		return SessionWorkspaceRetargetSource{}, err
	}
	return source, nil
}

func (s *Store) PlanSessionWorkspaceRetarget(ctx context.Context, req SessionWorkspaceRetargetRequest) (SessionWorkspaceRetargetPlan, error) {
	if s == nil || s.queries == nil {
		return SessionWorkspaceRetargetPlan{}, errors.New("metadata store is required")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return SessionWorkspaceRetargetPlan{}, errors.New("session id is required")
	}
	targetRoot, err := canonicalFilesystemPath(req.WorkspaceRoot)
	if err != nil {
		return SessionWorkspaceRetargetPlan{}, err
	}
	if err := requireExistingDirectory(targetRoot); err != nil {
		return SessionWorkspaceRetargetPlan{}, err
	}
	state, err := getSessionWorkspaceRetargetState(ctx, s.queries, sessionID)
	if err != nil {
		return SessionWorkspaceRetargetPlan{}, err
	}
	source, sourceBinding, err := sessionRetargetSourceFromState(state)
	if err != nil {
		return SessionWorkspaceRetargetPlan{}, err
	}
	sourceProject := source.Project
	if state.WorktreeID.Valid {
		return SessionWorkspaceRetargetPlan{}, newSessionRetargetWorktreeError(
			serverapi.SessionRetargetSourceWorktree,
			sessionID,
			sourceProject,
			targetRoot,
		)
	}
	if err := validateSessionWorkspaceRetargetTargetWorktree(ctx, s.queries, sessionID, sourceProject, targetRoot); err != nil {
		return SessionWorkspaceRetargetPlan{}, err
	}
	targetProject := sourceProject
	explicitProject := req.ProjectID != nil
	if explicitProject {
		targetProject.ID = strings.TrimSpace(*req.ProjectID)
		if targetProject.ID == "" {
			return SessionWorkspaceRetargetPlan{}, errors.New("project id is required when provided")
		}
		targetState, err := s.queries.GetProjectKeyState(ctx, targetProject.ID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return SessionWorkspaceRetargetPlan{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, targetProject.ID)
			}
			return SessionWorkspaceRetargetPlan{}, fmt.Errorf("get target project: %w", err)
		}
		targetProject.Name = targetState.DisplayName
	}
	if err := validateSessionWorkspaceRetargetTarget(
		ctx,
		s.queries,
		sessionID,
		sourceProject,
		targetProject,
		targetRoot,
		explicitProject,
	); err != nil {
		return SessionWorkspaceRetargetPlan{}, err
	}
	if err := validateSessionWorkspaceRetargetWorkflowOwnership(
		ctx,
		s.queries,
		sessionID,
		sourceProject,
		targetRoot,
		targetProject.ID != sourceProject.ID,
	); err != nil {
		return SessionWorkspaceRetargetPlan{}, err
	}
	noOp := sourceBinding != nil &&
		targetProject.ID == sourceProject.ID &&
		sourceBinding.CanonicalRoot == targetRoot &&
		source.EffectiveWorkingDirectory == targetRoot
	sourceDir, err := s.sessionRetargetArtifactPath(state.ArtifactRelpath)
	if err != nil {
		return SessionWorkspaceRetargetPlan{}, err
	}
	targetArtifactRelpath := state.ArtifactRelpath
	targetDir := sourceDir
	if targetProject.ID != sourceProject.ID {
		targetArtifactRelpath = filepath.ToSlash(filepath.Join("projects", targetProject.ID, "sessions", sessionID))
		targetDir, err = s.sessionRetargetArtifactPath(targetArtifactRelpath)
		if err != nil {
			return SessionWorkspaceRetargetPlan{}, err
		}
	}
	return SessionWorkspaceRetargetPlan{
		SessionID:                       sessionID,
		SourceProject:                   sourceProject,
		TargetProject:                   targetProject,
		ExplicitProject:                 explicitProject,
		TargetWorkspaceRoot:             targetRoot,
		SourceArtifactRelpath:           state.ArtifactRelpath,
		TargetArtifactRelpath:           targetArtifactRelpath,
		SourceSessionDir:                sourceDir,
		TargetSessionDir:                targetDir,
		SourceBinding:                   sourceBinding,
		SourceEffectiveWorkingDirectory: source.EffectiveWorkingDirectory,
		SourceCwdRelpath:                normalizeSessionCwdRelpath(state.CwdRelpath),
		noOp:                            noOp,
	}, nil
}

func (s *Store) CommitSessionWorkspaceRetarget(ctx context.Context, plan SessionWorkspaceRetargetPlan, updatedAt time.Time) (SessionWorkspaceRetargetResult, error) {
	if s == nil || s.queries == nil {
		return SessionWorkspaceRetargetResult{}, errors.New("metadata store is required")
	}
	if updatedAt.IsZero() {
		return SessionWorkspaceRetargetResult{}, errors.New("session retarget updated time is required")
	}
	updatedAt = updatedAt.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionWorkspaceRetargetResult{}, fmt.Errorf("begin session retarget tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if _, err := q.AcquireWorkspaceRegistrationLock(ctx); err != nil {
		return SessionWorkspaceRetargetResult{}, fmt.Errorf("lock session retarget: %w", err)
	}
	state, err := q.GetSessionWorkspaceRetargetStateByID(ctx, plan.SessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionWorkspaceRetargetResult{}, session.ErrSessionNotFound
		}
		return SessionWorkspaceRetargetResult{}, fmt.Errorf("get current session retarget state: %w", err)
	}
	if state.ProjectID != plan.SourceProject.ID || state.ArtifactRelpath != plan.SourceArtifactRelpath {
		return SessionWorkspaceRetargetResult{}, errors.New("session project or artifact changed while rebinding")
	}
	currentSourceProject := serverapi.ProjectReference{ID: state.ProjectID, Name: state.ProjectDisplayName}
	currentTargetProject := currentSourceProject
	if plan.TargetProject.ID != currentSourceProject.ID {
		targetState, err := q.GetProjectKeyState(ctx, plan.TargetProject.ID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return SessionWorkspaceRetargetResult{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, plan.TargetProject.ID)
			}
			return SessionWorkspaceRetargetResult{}, fmt.Errorf("get current target project: %w", err)
		}
		currentTargetProject = serverapi.ProjectReference{ID: plan.TargetProject.ID, Name: targetState.DisplayName}
	}
	if state.WorktreeID.Valid {
		return SessionWorkspaceRetargetResult{}, newSessionRetargetWorktreeError(
			serverapi.SessionRetargetSourceWorktree,
			plan.SessionID,
			currentSourceProject,
			plan.TargetWorkspaceRoot,
		)
	}
	currentBinding, currentEffectiveWorkingDirectory, err := sessionRetargetSourceLocation(state)
	if err != nil {
		return SessionWorkspaceRetargetResult{}, err
	}
	if !sessionRetargetBindingsEqual(currentBinding, plan.SourceBinding) ||
		currentEffectiveWorkingDirectory != plan.SourceEffectiveWorkingDirectory ||
		normalizeSessionCwdRelpath(state.CwdRelpath) != plan.SourceCwdRelpath {
		return SessionWorkspaceRetargetResult{}, errors.New("session location changed while rebinding")
	}
	if err := validateSessionWorkspaceRetargetTargetWorktree(
		ctx,
		q,
		plan.SessionID,
		currentSourceProject,
		plan.TargetWorkspaceRoot,
	); err != nil {
		return SessionWorkspaceRetargetResult{}, err
	}
	if err := validateSessionWorkspaceRetargetWorkflowOwnership(
		ctx,
		q,
		plan.SessionID,
		currentSourceProject,
		plan.TargetWorkspaceRoot,
		plan.CrossProject(),
	); err != nil {
		return SessionWorkspaceRetargetResult{}, err
	}
	if err := validateSessionWorkspaceRetargetTarget(
		ctx,
		q,
		plan.SessionID,
		currentSourceProject,
		currentTargetProject,
		plan.TargetWorkspaceRoot,
		plan.ExplicitProject,
	); err != nil {
		return SessionWorkspaceRetargetResult{}, err
	}
	if plan.NoOp() {
		if currentBinding == nil {
			return SessionWorkspaceRetargetResult{}, errors.New("no-op Session retarget requires a source binding")
		}
		return SessionWorkspaceRetargetResult{Binding: *currentBinding, UpdatedAt: updatedAt}, nil
	}
	binding, created, err := attachWorkspaceToProjectWithQueries(ctx, q, plan.TargetProject.ID, plan.TargetWorkspaceRoot, updatedAt)
	if err != nil {
		return SessionWorkspaceRetargetResult{}, err
	}
	reminder := session.SessionRebindReminder{
		SourceProject: currentSourceProject,
		TargetProject: currentTargetProject,
	}
	if plan.SourceEffectiveWorkingDirectory != binding.CanonicalRoot {
		reminder.WorkingDirectory = &binding.CanonicalRoot
	}
	normalizedReminder, err := session.NormalizeSessionRebindReminder(reminder)
	if err != nil {
		return SessionWorkspaceRetargetResult{}, fmt.Errorf("build session rebind reminder: %w", err)
	}
	reminderJSON, err := json.Marshal(normalizedReminder)
	if err != nil {
		return SessionWorkspaceRetargetResult{}, fmt.Errorf("marshal session rebind reminder: %w", err)
	}
	rows, err := q.RetargetSessionWorkspaceProject(ctx, sqlitegen.RetargetSessionWorkspaceProjectParams{
		TargetProjectID:          plan.TargetProject.ID,
		TargetWorkspaceID:        sql.NullString{String: binding.WorkspaceID, Valid: true},
		TargetArtifactRelpath:    plan.TargetArtifactRelpath,
		UpdatedAtUnixMs:          updatedAt.UnixMilli(),
		TargetWorkspaceRoot:      binding.CanonicalRoot,
		TargetWorkspaceContainer: binding.WorkspaceName,
		RebindReminderJson:       string(reminderJSON),
		SessionID:                plan.SessionID,
		SourceProjectID:          plan.SourceProject.ID,
		SourceWorkspaceID:        sessionRetargetWorkspaceID(plan.SourceBinding),
		SourceCwdRelpath:         plan.SourceCwdRelpath,
		SourceArtifactRelpath:    plan.SourceArtifactRelpath,
	})
	if err != nil {
		return SessionWorkspaceRetargetResult{}, fmt.Errorf("retarget session workspace: %w", err)
	}
	if rows == 0 {
		return SessionWorkspaceRetargetResult{}, errors.New("session project or artifact changed while rebinding")
	}
	if err := tx.Commit(); err != nil {
		return SessionWorkspaceRetargetResult{}, fmt.Errorf("commit session retarget tx: %w", err)
	}
	return SessionWorkspaceRetargetResult{
		Binding:                 binding,
		WorkspaceBindingCreated: created,
		UpdatedAt:               updatedAt,
		RebindReminder:          session.CloneSessionRebindReminder(&normalizedReminder),
	}, nil
}

func getSessionWorkspaceRetargetState(
	ctx context.Context,
	q *sqlitegen.Queries,
	sessionID string,
) (sqlitegen.GetSessionWorkspaceRetargetStateByIDRow, error) {
	state, err := q.GetSessionWorkspaceRetargetStateByID(ctx, sessionID)
	if err == nil {
		return state, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return sqlitegen.GetSessionWorkspaceRetargetStateByIDRow{}, session.ErrSessionNotFound
	}
	return sqlitegen.GetSessionWorkspaceRetargetStateByIDRow{}, fmt.Errorf("get Session retarget state: %w", err)
}

func sessionRetargetSourceFromState(
	state sqlitegen.GetSessionWorkspaceRetargetStateByIDRow,
) (SessionWorkspaceRetargetSource, *Binding, error) {
	binding, effectiveWorkingDirectory, err := sessionRetargetSourceLocation(state)
	if err != nil {
		return SessionWorkspaceRetargetSource{}, nil, err
	}
	return SessionWorkspaceRetargetSource{
		Project: serverapi.ProjectReference{
			ID:   state.ProjectID,
			Name: state.ProjectDisplayName,
		},
		EffectiveWorkingDirectory: effectiveWorkingDirectory,
	}, binding, nil
}

func sessionRetargetSourceLocation(state sqlitegen.GetSessionWorkspaceRetargetStateByIDRow) (*Binding, string, error) {
	var workspaceRoot string
	if state.RegisteredWorkspaceRoot.Valid {
		workspaceRoot = strings.TrimSpace(state.RegisteredWorkspaceRoot.String)
		if workspaceRoot == "" {
			return nil, "", errors.New("registered Session workspace root must not be blank")
		}
	} else {
		metadataDocument := sessionMetadataDocument{}
		if err := unmarshalStoredJSON(state.MetadataJson, &metadataDocument); err != nil {
			return nil, "", fmt.Errorf("decode Session workspace metadata: %w", err)
		}
		workspaceRoot = strings.TrimSpace(metadataDocument.WorkspaceRoot)
		if workspaceRoot == "" {
			return nil, "", errors.New("Session workspace root is required")
		}
	}
	baseRoot := workspaceRoot
	if state.WorktreeID.Valid {
		if !state.WorktreeRoot.Valid || strings.TrimSpace(state.WorktreeRoot.String) == "" {
			return nil, "", errors.New("session worktree root is required")
		}
		baseRoot = strings.TrimSpace(state.WorktreeRoot.String)
	}
	effectiveWorkingDirectory := effectiveWorkdirWithinRoot(baseRoot, normalizeSessionCwdRelpath(state.CwdRelpath))
	if !state.WorkspaceID.Valid {
		return nil, effectiveWorkingDirectory, nil
	}
	workspaceID := strings.TrimSpace(state.WorkspaceID.String)
	if workspaceID == "" {
		return nil, "", errors.New("session workspace id must not be blank when present")
	}
	binding := &Binding{
		ProjectID:       state.ProjectID,
		ProjectKey:      state.ProjectKey,
		ProjectName:     state.ProjectDisplayName,
		WorkspaceID:     workspaceID,
		CanonicalRoot:   workspaceRoot,
		WorkspaceName:   filepath.Base(workspaceRoot),
		WorkspaceStatus: availabilityForPath(workspaceRoot),
	}
	return binding, effectiveWorkingDirectory, nil
}

func sessionRetargetBindingsEqual(left *Binding, right *Binding) bool {
	switch {
	case left == nil || right == nil:
		return left == nil && right == nil
	default:
		return left.ProjectID == right.ProjectID &&
			left.WorkspaceID == right.WorkspaceID &&
			left.CanonicalRoot == right.CanonicalRoot
	}
}

func sessionRetargetWorkspaceID(binding *Binding) sql.NullString {
	if binding == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: binding.WorkspaceID, Valid: true}
}

func validateSessionWorkspaceRetargetTargetWorktree(
	ctx context.Context,
	q *sqlitegen.Queries,
	sessionID string,
	sourceProject serverapi.ProjectReference,
	targetRoot string,
) error {
	worktree, err := q.GetWorktreeByCanonicalRoot(ctx, targetRoot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get target worktree: %w", err)
	}
	if worktree.IsMain != 0 {
		return nil
	}
	return newSessionRetargetWorktreeError(
		serverapi.SessionRetargetTargetWorktree,
		sessionID,
		sourceProject,
		targetRoot,
	)
}

func newSessionRetargetWorktreeError(
	reason serverapi.SessionRetargetErrorReason,
	sessionID string,
	sourceProject serverapi.ProjectReference,
	targetRoot string,
) error {
	return &serverapi.SessionRetargetError{
		Reason:        reason,
		SessionID:     sessionID,
		SourceProject: sourceProject,
		TargetRoot:    targetRoot,
	}
}

func validateSessionWorkspaceRetargetWorkflowOwnership(
	ctx context.Context,
	q *sqlitegen.Queries,
	sessionID string,
	sourceProject serverapi.ProjectReference,
	targetRoot string,
	crossProject bool,
) error {
	if !crossProject {
		return nil
	}
	taskIDRows, err := q.ListSessionWorkflowTaskIDs(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("list workflow task sessions: %w", err)
	}
	taskIDs := make([]string, 0, len(taskIDRows))
	for _, taskID := range taskIDRows {
		if !taskID.Valid {
			return errors.New("workflow task session has no owning task")
		}
		taskIDs = append(taskIDs, taskID.String)
	}
	if len(taskIDs) == 0 {
		return nil
	}
	return &serverapi.SessionRetargetError{
		Reason:          serverapi.SessionRetargetWorkflowOwned,
		SessionID:       sessionID,
		SourceProject:   sourceProject,
		TargetRoot:      targetRoot,
		WorkflowTaskIDs: taskIDs,
	}
}

func validateSessionWorkspaceRetargetTarget(
	ctx context.Context,
	q *sqlitegen.Queries,
	sessionID string,
	sourceProject serverapi.ProjectReference,
	targetProject serverapi.ProjectReference,
	targetRoot string,
	explicitProject bool,
) error {
	rows, err := q.ListWorkspaceBindingsByCanonicalRoot(ctx, targetRoot)
	if err != nil {
		return fmt.Errorf("list target workspace bindings: %w", err)
	}
	candidates := make([]serverapi.ProjectReference, 0, len(rows))
	for _, row := range rows {
		if row.ProjectID == targetProject.ID {
			return nil
		}
		candidates = append(candidates, serverapi.ProjectReference{ID: row.ProjectID, Name: row.ProjectDisplayName})
	}
	if len(candidates) == 0 {
		return nil
	}
	reason := serverapi.SessionRetargetTargetProjectRequired
	if explicitProject {
		reason = serverapi.SessionRetargetTargetProjectConflict
	}
	return &serverapi.SessionRetargetError{
		Reason:            reason,
		SessionID:         sessionID,
		SourceProject:     sourceProject,
		TargetRoot:        targetRoot,
		CandidateProjects: candidates,
	}
}

func (s *Store) sessionRetargetArtifactPath(artifactRelpath string) (string, error) {
	path, err := sessionArtifactPathWithinRoot(s.persistenceRoot, artifactRelpath)
	if err != nil {
		return "", err
	}
	canonicalPath, err := canonicalFilesystemPath(path)
	if err != nil {
		return "", err
	}
	if _, err := relativePathWithinRoot(s.persistenceRoot, canonicalPath); err != nil {
		return "", err
	}
	return canonicalPath, nil
}

func attachWorkspaceToProjectWithQueries(ctx context.Context, q *sqlitegen.Queries, projectID string, canonicalRoot string, now time.Time) (Binding, bool, error) {
	binding, err := lookupProjectWorkspaceBindingWithQueries(ctx, q, projectID, canonicalRoot)
	if err == nil {
		return binding, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Binding{}, false, err
	}
	project, err := q.GetProjectKeyState(ctx, projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Binding{}, false, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
		}
		return Binding{}, false, fmt.Errorf("get project: %w", err)
	}
	workspaceCount, err := q.CountProjectWorkspaces(ctx, projectID)
	if err != nil {
		return Binding{}, false, fmt.Errorf("count project workspaces: %w", err)
	}
	workspaceID := "workspace-" + uuid.NewString()
	inserted, err := insertWorkspaceBindingWithQueries(ctx, q, workspaceBindingInsert{
		ID:            workspaceID,
		ProjectID:     projectID,
		CanonicalRoot: canonicalRoot,
		UpdatedAt:     now,
		Primary:       workspaceCount == 0,
	})
	if err != nil {
		return Binding{}, false, err
	}
	if !inserted {
		return Binding{}, false, fmt.Errorf("workspace %q could not be attached to project %q", canonicalRoot, projectID)
	}
	binding, err = lookupProjectWorkspaceBindingWithQueries(ctx, q, projectID, canonicalRoot)
	if err != nil {
		return Binding{}, false, fmt.Errorf("lookup attached workspace binding: %w", err)
	}
	binding.ProjectKey = project.ProjectKey
	return binding, true, nil
}

func lookupProjectWorkspaceBindingWithQueries(ctx context.Context, q *sqlitegen.Queries, projectID string, canonicalRoot string) (Binding, error) {
	row, err := q.GetWorkspaceBindingByProjectAndCanonicalRoot(ctx, sqlitegen.GetWorkspaceBindingByProjectAndCanonicalRootParams{
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
