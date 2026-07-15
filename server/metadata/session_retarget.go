package metadata

import (
	"context"
	"database/sql"
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
	SessionID             string
	SourceProject         serverapi.ProjectReference
	TargetProject         serverapi.ProjectReference
	ExplicitProject       bool
	TargetWorkspaceRoot   string
	SourceArtifactRelpath string
	TargetArtifactRelpath string
	SourceSessionDir      string
	TargetSessionDir      string
}

func (p SessionWorkspaceRetargetPlan) CrossProject() bool {
	return strings.TrimSpace(p.SourceProject.ID) != strings.TrimSpace(p.TargetProject.ID)
}

type SessionWorkspaceRetargetResult struct {
	Binding                 Binding
	WorkspaceBindingCreated bool
	UpdatedAt               time.Time
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
	state, err := s.queries.GetSessionWorkspaceRetargetStateByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionWorkspaceRetargetPlan{}, session.ErrSessionNotFound
		}
		return SessionWorkspaceRetargetPlan{}, fmt.Errorf("get session retarget state: %w", err)
	}
	sourceProject := serverapi.ProjectReference{ID: state.ProjectID, Name: state.ProjectDisplayName}
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
		state.HasWorkflowSession != 0,
	); err != nil {
		return SessionWorkspaceRetargetPlan{}, err
	}
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
		SessionID:             sessionID,
		SourceProject:         sourceProject,
		TargetProject:         targetProject,
		ExplicitProject:       explicitProject,
		TargetWorkspaceRoot:   targetRoot,
		SourceArtifactRelpath: state.ArtifactRelpath,
		TargetArtifactRelpath: targetArtifactRelpath,
		SourceSessionDir:      sourceDir,
		TargetSessionDir:      targetDir,
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
	if err := validateSessionWorkspaceRetargetWorkflowOwnership(
		ctx,
		q,
		plan.SessionID,
		plan.SourceProject,
		plan.TargetWorkspaceRoot,
		plan.CrossProject(),
		state.HasWorkflowSession != 0,
	); err != nil {
		return SessionWorkspaceRetargetResult{}, err
	}
	if err := validateSessionWorkspaceRetargetTarget(
		ctx,
		q,
		plan.SessionID,
		plan.SourceProject,
		plan.TargetProject,
		plan.TargetWorkspaceRoot,
		plan.ExplicitProject,
	); err != nil {
		return SessionWorkspaceRetargetResult{}, err
	}
	binding, created, err := attachWorkspaceToProjectWithQueries(ctx, q, plan.TargetProject.ID, plan.TargetWorkspaceRoot, updatedAt)
	if err != nil {
		return SessionWorkspaceRetargetResult{}, err
	}
	rows, err := q.RetargetSessionWorkspaceProject(ctx, sqlitegen.RetargetSessionWorkspaceProjectParams{
		TargetProjectID:          plan.TargetProject.ID,
		TargetWorkspaceID:        sql.NullString{String: binding.WorkspaceID, Valid: true},
		TargetArtifactRelpath:    plan.TargetArtifactRelpath,
		UpdatedAtUnixMs:          updatedAt.UnixMilli(),
		TargetWorkspaceRoot:      binding.CanonicalRoot,
		TargetWorkspaceContainer: binding.WorkspaceName,
		SessionID:                plan.SessionID,
		SourceProjectID:          plan.SourceProject.ID,
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
	return SessionWorkspaceRetargetResult{Binding: binding, WorkspaceBindingCreated: created, UpdatedAt: updatedAt}, nil
}

func validateSessionWorkspaceRetargetWorkflowOwnership(
	ctx context.Context,
	q *sqlitegen.Queries,
	sessionID string,
	sourceProject serverapi.ProjectReference,
	targetRoot string,
	crossProject bool,
	hasWorkflowSession bool,
) error {
	if !crossProject {
		return nil
	}
	taskIDs, err := q.ListSessionWorkflowTaskIDs(ctx, sql.NullString{String: sessionID, Valid: true})
	if err != nil {
		return fmt.Errorf("list workflow task sessions: %w", err)
	}
	if !hasWorkflowSession && len(taskIDs) == 0 {
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
