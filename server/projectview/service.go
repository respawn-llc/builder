package projectview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"core/server/metadata"
	"core/server/requestmemo"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type Service struct {
	metadata          *metadata.Store
	projectID         string
	runtimeGuard      runtimeSessionGuard
	projectMutations  *requestmemo.MutationLaneRegistry[string]
	mutationPermit    *workflowexecution.MutationPermit
	workflowExecution interface {
		EnsureTaskQuiescent(workflow.TaskID) error
	}
	workflowStore *workflowstore.Store
}

// runtimeSessionGuard is an internal composition collaborator. Production
// composition always installs the authority adapter via WithRuntimeAuthority.
type runtimeSessionGuard interface {
	CountBlockingRuntimeActivity(context.Context, []string) (int, error)
	BlockSessionStarts(context.Context, []string) (func(), error)
}

type authorityRuntimeSessionGuard struct {
	authority *sessionruntime.Authority
}

func (g authorityRuntimeSessionGuard) CountBlockingRuntimeActivity(ctx context.Context, sessionIDs []string) (int, error) {
	if g.authority == nil {
		return 0, nil
	}
	if err := context.Cause(ctx); err != nil {
		return 0, err
	}
	ids, err := sessionruntime.ParseSessionIDs(sessionIDs)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		active, err := g.authority.HasBlockingRuntimeActivity(ctx, id.String())
		if err != nil {
			return 0, err
		}
		if active {
			count++
		}
	}
	return count, nil
}

func (g authorityRuntimeSessionGuard) BlockSessionStarts(ctx context.Context, sessionIDs []string) (func(), error) {
	if g.authority == nil {
		return func() {}, nil
	}
	ids, err := sessionruntime.ParseSessionIDs(sessionIDs)
	if err != nil || len(ids) == 0 {
		return func() {}, err
	}
	release, err := g.authority.BlockSessionStarts(ctx, ids, sessionruntime.SessionStartBlockMaintenance)
	if err != nil {
		return nil, err
	}
	return func() {
		if err := release.Close(context.Background()); err != nil {
			panic(fmt.Sprintf("release project session start block: %v", err))
		}
	}, nil
}

// ErrSessionArtifactEscapesRoot is returned when a session artifact path
// resolves outside its project sessions root. Callers and tests match this with
// errors.Is rather than comparing rendered message text.
var ErrSessionArtifactEscapesRoot = errors.New("session artifact path escapes project sessions root")

const (
	defaultProjectHomePageSize = 50
	maxProjectHomePageSize     = 100
	defaultWorkspacePageSize   = 100
	maxWorkspacePageSize       = 100
)

func NewMetadataService(metadataStore *metadata.Store, projectID string) (*Service, error) {
	if metadataStore == nil {
		return nil, errors.New("metadata store is required")
	}
	return &Service{
		metadata:         metadataStore,
		projectID:        strings.TrimSpace(projectID),
		projectMutations: requestmemo.NewMutationLaneRegistry[string](),
	}, nil
}

func (s *Service) WithRuntimeAuthority(authority *sessionruntime.Authority) *Service {
	if s == nil {
		return nil
	}
	if authority == nil {
		s.runtimeGuard = nil
	} else {
		s.runtimeGuard = authorityRuntimeSessionGuard{authority: authority}
	}
	return s
}

func (s *Service) WithWorkflowExecution(permit *workflowexecution.MutationPermit, execution interface {
	EnsureTaskQuiescent(workflow.TaskID) error
}, store *workflowstore.Store) *Service {
	if s == nil {
		return nil
	}
	s.mutationPermit = permit
	s.workflowExecution = execution
	s.workflowStore = store
	return s
}

func (s *Service) ProjectID() string {
	if s == nil {
		return ""
	}
	return s.projectID
}

func (s *Service) ListProjects(ctx context.Context, _ serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error) {
	if s == nil {
		return serverapi.ProjectListResponse{}, errors.New("project service is required")
	}
	projects, err := s.metadata.ListProjects(ctx)
	if err != nil {
		return serverapi.ProjectListResponse{}, err
	}
	if trimmedProjectID := strings.TrimSpace(s.projectID); trimmedProjectID != "" {
		filtered := make([]clientui.ProjectSummary, 0, 1)
		for _, project := range projects {
			if strings.TrimSpace(project.ProjectID) == trimmedProjectID {
				filtered = append(filtered, project)
				break
			}
		}
		return serverapi.ProjectListResponse{Projects: filtered}, nil
	}
	return serverapi.ProjectListResponse{Projects: projects}, nil
}

func (s *Service) ListProjectHome(ctx context.Context, req serverapi.ProjectHomeListRequest) (serverapi.ProjectHomeListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectHomeListResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectHomeListResponse{}, errors.New("project service is required")
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = defaultProjectHomePageSize
	}
	if pageSize > maxProjectHomePageSize {
		pageSize = maxProjectHomePageSize
	}
	offset, err := parseProjectHomePageToken(req.PageToken)
	if err != nil {
		return serverapi.ProjectHomeListResponse{}, err
	}
	scopedProjectID := strings.TrimSpace(s.projectID)
	summaries, err := s.metadata.ListProjectHomeSummaries(ctx, scopedProjectID, pageSize+1, offset)
	if err != nil {
		return serverapi.ProjectHomeListResponse{}, err
	}
	nextPageToken := ""
	if len(summaries) > pageSize {
		summaries = summaries[:pageSize]
		nextPageToken = strconv.Itoa(offset + pageSize)
	}
	return serverapi.ProjectHomeListResponse{
		Projects:          summaries,
		NextPageToken:     nextPageToken,
		GeneratedAtUnixMs: time.Now().UTC().UnixMilli(),
	}, nil
}

func (s *Service) ResolveProjectPath(ctx context.Context, req serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectResolvePathResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectResolvePathResponse{}, errors.New("project service is required")
	}
	canonicalRoot, binding, err := s.metadata.ResolveWorkspacePath(ctx, req.Path)
	if err != nil {
		return serverapi.ProjectResolvePathResponse{}, err
	}
	resp := serverapi.ProjectResolvePathResponse{CanonicalRoot: canonicalRoot}
	resp.PathAvailability = clientui.ProjectAvailability(availabilityForProjectPath(canonicalRoot))
	if binding != nil {
		mapped := projectBindingFromMetadata(*binding)
		resp.Binding = &mapped
	}
	return resp, nil
}

func (s *Service) PlanWorkspaceBinding(ctx context.Context, req serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectBindingPlanResponse{}, err
	}
	resolved, err := s.ResolveProjectPath(ctx, serverapi.ProjectResolvePathRequest{Path: req.Path})
	if err != nil {
		if ambiguous, ok := serverapi.AsWorkspaceBindingAmbiguous(err); ok {
			resp := serverapi.ProjectBindingPlanResponse{
				CanonicalRoot:    ambiguous.CanonicalRoot,
				PathAvailability: clientui.ProjectAvailability(availabilityForProjectPath(ambiguous.CanonicalRoot)),
			}
			switch req.Mode {
			case serverapi.ProjectBindingPlanModeInteractive:
				projects, err := s.ListProjects(ctx, serverapi.ProjectListRequest{})
				if err != nil {
					return serverapi.ProjectBindingPlanResponse{}, err
				}
				resp.Kind = serverapi.ProjectBindingPlanKindServerWorkspaceSelection
				resp.Projects = projects.Projects
				return resp, nil
			case serverapi.ProjectBindingPlanModeHeadless:
				resp.Kind = serverapi.ProjectBindingPlanKindHeadlessRemoteAmbiguous
				return resp, nil
			default:
				return serverapi.ProjectBindingPlanResponse{}, errors.New("mode must be interactive or headless")
			}
		}
		return serverapi.ProjectBindingPlanResponse{}, err
	}
	resp := serverapi.ProjectBindingPlanResponse{
		CanonicalRoot:    resolved.CanonicalRoot,
		PathAvailability: resolved.PathAvailability,
		Binding:          resolved.Binding,
	}
	if resolved.Binding != nil {
		resp.Kind = serverapi.ProjectBindingPlanKindBound
		return resp, nil
	}
	switch req.Mode {
	case serverapi.ProjectBindingPlanModeInteractive:
		projects, err := s.ListProjects(ctx, serverapi.ProjectListRequest{})
		if err != nil {
			return serverapi.ProjectBindingPlanResponse{}, err
		}
		resp.Projects = projects.Projects
		if resolved.PathAvailability == clientui.ProjectAvailabilityMissing || resolved.PathAvailability == clientui.ProjectAvailabilityInaccessible {
			resp.Kind = serverapi.ProjectBindingPlanKindServerWorkspaceSelection
			return resp, nil
		}
		resp.Kind = serverapi.ProjectBindingPlanKindLocalUnbound
		return resp, nil
	case serverapi.ProjectBindingPlanModeHeadless:
		if resolved.PathAvailability == clientui.ProjectAvailabilityAvailable {
			resp.Kind = serverapi.ProjectBindingPlanKindLocalUnbound
			return resp, nil
		}
		workspace, found, err := s.selectSingleAvailableWorkspace(ctx)
		if err != nil {
			return serverapi.ProjectBindingPlanResponse{}, err
		}
		if !found {
			resp.Kind = serverapi.ProjectBindingPlanKindHeadlessRemoteAmbiguous
			return resp, nil
		}
		resp.Kind = serverapi.ProjectBindingPlanKindHeadlessRemoteSelected
		resp.Workspace = &workspace
		return resp, nil
	default:
		return serverapi.ProjectBindingPlanResponse{}, errors.New("mode must be interactive or headless")
	}
}

func (s *Service) CreateProject(ctx context.Context, req serverapi.ProjectCreateRequest) (serverapi.ProjectCreateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectCreateResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectCreateResponse{}, errors.New("project service is required")
	}
	binding, err := s.metadata.CreateProjectForWorkspaceWithKey(ctx, req.WorkspaceRoot, req.DisplayName, req.ProjectKey)
	if err != nil {
		return serverapi.ProjectCreateResponse{}, err
	}
	return serverapi.ProjectCreateResponse{Binding: projectBindingFromMetadata(binding)}, nil
}

func (s *Service) UpdateProject(ctx context.Context, req serverapi.ProjectUpdateRequest) (serverapi.ProjectUpdateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectUpdateResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectUpdateResponse{}, errors.New("project service is required")
	}
	if err := s.requireProjectID(req.ProjectID); err != nil {
		return serverapi.ProjectUpdateResponse{}, err
	}
	lease, err := s.acquireProjectMutationLease(ctx, req.ProjectID)
	if err != nil {
		return serverapi.ProjectUpdateResponse{}, err
	}
	defer lease.Release()
	if err := s.metadata.UpdateProjectMetadata(ctx, req.ProjectID, req.DisplayName, req.ProjectKey); err != nil {
		return serverapi.ProjectUpdateResponse{}, err
	}
	project, err := s.projectHomeSummary(ctx, req.ProjectID)
	if err != nil {
		return serverapi.ProjectUpdateResponse{}, err
	}
	return serverapi.ProjectUpdateResponse{Project: project}, nil
}

func (s *Service) GetProjectEdit(ctx context.Context, req serverapi.ProjectEditGetRequest) (serverapi.ProjectEditGetResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectEditGetResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectEditGetResponse{}, errors.New("project service is required")
	}
	if err := s.requireProjectID(req.ProjectID); err != nil {
		return serverapi.ProjectEditGetResponse{}, err
	}
	project, err := s.projectHomeSummary(ctx, req.ProjectID)
	if err != nil {
		return serverapi.ProjectEditGetResponse{}, err
	}
	workspaces, err := s.ListProjectWorkspaces(ctx, serverapi.ProjectWorkspaceListRequest{
		ProjectID: req.ProjectID,
		PageSize:  req.PageSize,
		PageToken: req.PageToken,
	})
	if err != nil {
		return serverapi.ProjectEditGetResponse{}, err
	}
	return serverapi.ProjectEditGetResponse{
		ProjectID:          project.ProjectID,
		ProjectKey:         project.ProjectKey,
		DisplayName:        project.DisplayName,
		DefaultWorkspaceID: workspaces.DefaultWorkspaceID,
		Workspaces:         workspaces.Workspaces,
		NextPageToken:      workspaces.NextPageToken,
	}, nil
}

func (s *Service) DeleteProject(ctx context.Context, req serverapi.ProjectDeleteRequest) (serverapi.ProjectDeleteResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectDeleteResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectDeleteResponse{}, errors.New("project service is required")
	}
	if err := s.requireProjectID(req.ProjectID); err != nil {
		return serverapi.ProjectDeleteResponse{}, err
	}
	projectID := strings.TrimSpace(req.ProjectID)
	lease, err := s.acquireProjectMutationLease(ctx, projectID)
	if err != nil {
		return serverapi.ProjectDeleteResponse{}, err
	}
	defer lease.Release()
	if s.mutationPermit == nil || s.workflowExecution == nil {
		return serverapi.ProjectDeleteResponse{}, errors.New("workflow execution is required for project deletion")
	}

	runtimeBlocker := func(ctx context.Context, sessionIDs []string) ([]serverapi.ProjectDeleteBlocker, func(), error) {
		return withRuntimeBlockers(ctx, sessionIDs, s.projectActiveSessionBlockers, s.blockSessionStarts)
	}
	deleteProject := func(ctx context.Context) ([]serverapi.ProjectDeleteBlocker, error) {
		taskIDs, err := s.metadata.ListProjectTaskIDs(ctx, projectID)
		if err != nil {
			return nil, err
		}
		for _, taskID := range taskIDs {
			if err := s.workflowExecution.EnsureTaskQuiescent(workflow.TaskID(taskID)); err != nil {
				return nil, err
			}
		}
		if s.workflowStore == nil {
			return nil, errors.New("workflow store is required for project deletion")
		}
		return s.workflowStore.DeleteProject(ctx, workflowstore.ProjectDeleteRequest{
			ProjectID:      projectID,
			RuntimeBlocker: runtimeBlocker,
			Artifacts: projectSessionDeleteArtifacts{
				persistenceRoot: s.metadata.PersistenceRoot(),
				projectID:       projectID,
			},
		})
	}
	var blockers []serverapi.ProjectDeleteBlocker
	err = s.mutationPermit.Run(ctx, func(ctx context.Context) error {
		var runErr error
		blockers, runErr = deleteProject(ctx)
		return runErr
	})
	if err != nil {
		return serverapi.ProjectDeleteResponse{}, err
	}
	if len(blockers) > 0 {
		return serverapi.ProjectDeleteResponse{ProjectID: projectID, Deleted: false, Blockers: blockers}, nil
	}
	return serverapi.ProjectDeleteResponse{ProjectID: projectID, Deleted: true}, nil
}

func (s *Service) blockSessionStarts(ctx context.Context, sessionIDs []string) (func(), error) {
	if s == nil || s.runtimeGuard == nil {
		return func() {}, nil
	}
	return s.runtimeGuard.BlockSessionStarts(ctx, sessionIDs)
}

func withRuntimeBlockers[T any](
	ctx context.Context,
	sessionIDs []string,
	check func(context.Context, []string) ([]T, error),
	block func(context.Context, []string) (func(), error),
) ([]T, func(), error) {
	blockers, err := check(ctx, sessionIDs)
	if err != nil || len(blockers) > 0 {
		return blockers, nil, err
	}
	release, err := block(ctx, sessionIDs)
	if err != nil {
		return nil, nil, err
	}
	blockers, err = check(ctx, sessionIDs)
	if err != nil {
		release()
		return nil, nil, err
	}
	return blockers, release, nil
}

func (s *Service) projectActiveSessionBlockers(ctx context.Context, sessionIDs []string) ([]serverapi.ProjectDeleteBlocker, error) {
	count, err := s.countBlockingRuntimeActivity(ctx, sessionIDs)
	if err != nil || count == 0 {
		return nil, err
	}
	return []serverapi.ProjectDeleteBlocker{{
		Code:    "active_sessions",
		Message: "Project has active runtime sessions.",
		Count:   count,
	}}, nil
}

func (s *Service) workspaceActiveSessionBlockers(ctx context.Context, sessionIDs []string) ([]serverapi.ProjectWorkspaceUnlinkBlocker, error) {
	count, err := s.countBlockingRuntimeActivity(ctx, sessionIDs)
	if err != nil || count == 0 {
		return nil, err
	}
	return []serverapi.ProjectWorkspaceUnlinkBlocker{{
		Code:    "active_sessions",
		Message: "Active runtime sessions still depend on this workspace.",
		Count:   count,
	}}, nil
}

func (s *Service) countBlockingRuntimeActivity(ctx context.Context, sessionIDs []string) (int, error) {
	if s == nil || s.runtimeGuard == nil {
		return 0, nil
	}
	return s.runtimeGuard.CountBlockingRuntimeActivity(ctx, sessionIDs)
}

type projectSessionDeleteArtifacts struct {
	persistenceRoot string
	projectID       string
}

func (a projectSessionDeleteArtifacts) Recover(state workflowstore.ProjectDeleteArtifactRecovery) (bool, error) {
	sessionsRoot, tombstoneRoot, err := a.paths()
	if err != nil {
		return false, err
	}
	sessionsExists, err := pathExists(sessionsRoot)
	if err != nil {
		return false, err
	}
	tombstoneExists, err := pathExists(tombstoneRoot)
	if err != nil {
		return false, err
	}
	switch state {
	case workflowstore.ProjectDeleteArtifactRecoveryProjectPresent:
		if !tombstoneExists {
			return false, nil
		}
		if sessionsExists {
			return false, fmt.Errorf("project sessions root and delete tombstone both exist")
		}
		if err := os.Rename(tombstoneRoot, sessionsRoot); err != nil {
			return false, fmt.Errorf("restore project sessions tombstone: %w", err)
		}
		return true, nil
	case workflowstore.ProjectDeleteArtifactRecoveryProjectAbsent:
		if !tombstoneExists {
			return false, nil
		}
		if sessionsExists {
			return false, fmt.Errorf("deleted project sessions root and delete tombstone both exist")
		}
		if err := os.RemoveAll(tombstoneRoot); err != nil {
			return false, fmt.Errorf("finalize project sessions tombstone: %w", err)
		}
		return true, nil
	default:
		return false, fmt.Errorf("unknown project delete artifact recovery state %d", state)
	}
}

func (a projectSessionDeleteArtifacts) Validate(artifact workflowstore.ProjectSessionArtifact) error {
	if strings.TrimSpace(artifact.SessionID) == "" {
		return errors.New("session artifact id is required")
	}
	sessionsRoot, target, err := a.artifactPath(artifact.ArtifactRelpath)
	if err != nil {
		return err
	}
	if err := rejectSymlinkComponents(sessionsRoot, target); err != nil {
		return fmt.Errorf("validate session artifact path %q: %w", artifact.ArtifactRelpath, err)
	}
	return nil
}

func (a projectSessionDeleteArtifacts) Stage() error {
	sessionsRoot, tombstoneRoot, err := a.paths()
	if err != nil {
		return err
	}
	if exists, err := pathExists(tombstoneRoot); err != nil {
		return err
	} else if exists {
		return errors.New("project sessions delete tombstone already exists")
	}
	exists, err := pathExists(sessionsRoot)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err := os.Rename(sessionsRoot, tombstoneRoot); err != nil {
		return fmt.Errorf("stage project sessions root: %w", err)
	}
	return nil
}

func (a projectSessionDeleteArtifacts) Restore() error {
	sessionsRoot, tombstoneRoot, err := a.paths()
	if err != nil {
		return err
	}
	tombstoneExists, err := pathExists(tombstoneRoot)
	if err != nil {
		return err
	}
	if !tombstoneExists {
		return nil
	}
	if sessionsExists, err := pathExists(sessionsRoot); err != nil {
		return err
	} else if sessionsExists {
		return errors.New("project sessions root exists while restoring delete tombstone")
	}
	if err := os.Rename(tombstoneRoot, sessionsRoot); err != nil {
		return fmt.Errorf("restore staged project sessions root: %w", err)
	}
	return nil
}

func (a projectSessionDeleteArtifacts) Finalize() error {
	_, tombstoneRoot, err := a.paths()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(tombstoneRoot); err != nil {
		return fmt.Errorf("remove staged project sessions root: %w", err)
	}
	return nil
}

func (a projectSessionDeleteArtifacts) paths() (string, string, error) {
	root, err := persistenceRootPath(a.persistenceRoot)
	if err != nil {
		return "", "", err
	}
	projectID := strings.TrimSpace(a.projectID)
	if projectID == "" || filepath.Base(projectID) != projectID {
		return "", "", fmt.Errorf("invalid project id %q", a.projectID)
	}
	sessionsRoot := filepath.Join(root, "projects", projectID, "sessions")
	tombstoneRoot := sessionsRoot + ".deleting"
	if err := rejectSymlinkComponents(root, sessionsRoot); err != nil {
		return "", "", fmt.Errorf("validate project sessions root: %w", err)
	}
	if err := rejectSymlinkComponents(root, tombstoneRoot); err != nil {
		return "", "", fmt.Errorf("validate project sessions tombstone: %w", err)
	}
	return sessionsRoot, tombstoneRoot, nil
}

func (a projectSessionDeleteArtifacts) artifactPath(relpath string) (string, string, error) {
	cleanRelpath := filepath.Clean(strings.TrimSpace(relpath))
	if cleanRelpath == "." || filepath.IsAbs(cleanRelpath) || cleanRelpath == ".." || strings.HasPrefix(cleanRelpath, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("invalid session artifact path %q", relpath)
	}
	sessionsRoot, _, err := a.paths()
	if err != nil {
		return "", "", err
	}
	root, err := persistenceRootPath(a.persistenceRoot)
	if err != nil {
		return "", "", err
	}
	target, err := filepath.Abs(filepath.Join(root, cleanRelpath))
	if err != nil {
		return "", "", fmt.Errorf("resolve session artifact path: %w", err)
	}
	inside, err := filepath.Rel(sessionsRoot, target)
	if err != nil {
		return "", "", fmt.Errorf("validate session artifact path: %w", err)
	}
	if inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("session artifact path %q escapes project sessions root: %w", relpath, ErrSessionArtifactEscapesRoot)
	}
	return sessionsRoot, target, nil
}

func persistenceRootPath(persistenceRoot string) (string, error) {
	root, err := filepath.Abs(filepath.Clean(persistenceRoot))
	if err != nil {
		return "", fmt.Errorf("resolve persistence root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve persistence root symlinks: %w", err)
	}
	return root, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("inspect project session artifact path %q: %w", path, err)
}

func rejectSymlinkComponents(root string, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("resolve path containment: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrSessionArtifactEscapesRoot
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect path component %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSessionArtifactEscapesRoot
		}
	}
	return nil
}

func (s *Service) selectSingleAvailableWorkspace(ctx context.Context) (serverapi.ProjectWorkspacePlanSelected, bool, error) {
	projects, err := s.ListProjects(ctx, serverapi.ProjectListRequest{})
	if err != nil {
		return serverapi.ProjectWorkspacePlanSelected{}, false, err
	}
	selection := serverapi.ProjectWorkspacePlanSelected{}
	count := 0
	for _, project := range projects.Projects {
		overview, err := s.GetProjectOverview(ctx, serverapi.ProjectGetOverviewRequest{ProjectID: project.ProjectID})
		if err != nil {
			return serverapi.ProjectWorkspacePlanSelected{}, false, err
		}
		for _, workspace := range overview.Overview.Workspaces {
			availability := strings.TrimSpace(string(workspace.Availability))
			if availability != "" && workspace.Availability != clientui.ProjectAvailabilityAvailable {
				continue
			}
			count++
			selection = serverapi.ProjectWorkspacePlanSelected{ProjectID: project.ProjectID, WorkspaceID: workspace.WorkspaceID}
			if count > 1 {
				return serverapi.ProjectWorkspacePlanSelected{}, false, nil
			}
		}
	}
	if count == 0 {
		return serverapi.ProjectWorkspacePlanSelected{}, false, nil
	}
	return selection, true, nil
}

func (s *Service) ListProjectWorkspaces(ctx context.Context, req serverapi.ProjectWorkspaceListRequest) (serverapi.ProjectWorkspaceListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectWorkspaceListResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectWorkspaceListResponse{}, errors.New("project service is required")
	}
	if err := s.requireProjectID(req.ProjectID); err != nil {
		return serverapi.ProjectWorkspaceListResponse{}, err
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = defaultWorkspacePageSize
	}
	if pageSize > maxWorkspacePageSize {
		pageSize = maxWorkspacePageSize
	}
	offset, err := parseProjectHomePageToken(req.PageToken)
	if err != nil {
		return serverapi.ProjectWorkspaceListResponse{}, err
	}
	project, err := s.projectHomeSummary(ctx, req.ProjectID)
	if err != nil {
		return serverapi.ProjectWorkspaceListResponse{}, err
	}
	workspaces, err := s.metadata.ListProjectWorkspacesPage(ctx, req.ProjectID, pageSize+1, offset)
	if err != nil {
		return serverapi.ProjectWorkspaceListResponse{}, err
	}
	nextPageToken := ""
	if len(workspaces) > pageSize {
		workspaces = workspaces[:pageSize]
		nextPageToken = strconv.Itoa(offset + pageSize)
	} else if len(workspaces) == pageSize && offset+len(workspaces) == metadata.ProjectWorkspaceCollectionLimit {
		nextPageToken = strconv.Itoa(offset + pageSize)
	}
	response := serverapi.ProjectWorkspaceListResponse{
		ProjectID:          strings.TrimSpace(req.ProjectID),
		Workspaces:         make([]serverapi.ProjectWorkspaceSummary, 0, len(workspaces)),
		DefaultWorkspaceID: project.PrimaryWorkspace.WorkspaceID,
		NextPageToken:      nextPageToken,
	}
	for _, workspace := range workspaces {
		summary := projectWorkspaceSummaryFromClientUI(workspace)
		response.Workspaces = append(response.Workspaces, summary)
	}
	return response, nil
}

func (s *Service) AttachWorkspaceToProject(ctx context.Context, req serverapi.ProjectAttachWorkspaceRequest) (serverapi.ProjectAttachWorkspaceResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectAttachWorkspaceResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectAttachWorkspaceResponse{}, errors.New("project service is required")
	}
	if err := s.requireProjectID(req.ProjectID); err != nil {
		return serverapi.ProjectAttachWorkspaceResponse{}, err
	}
	lease, err := s.acquireProjectMutationLease(ctx, req.ProjectID)
	if err != nil {
		return serverapi.ProjectAttachWorkspaceResponse{}, err
	}
	defer lease.Release()
	binding, err := s.metadata.AttachWorkspaceToProject(ctx, req.ProjectID, req.WorkspaceRoot)
	if err != nil {
		return serverapi.ProjectAttachWorkspaceResponse{}, err
	}
	return serverapi.ProjectAttachWorkspaceResponse{Binding: projectBindingFromMetadata(binding)}, nil
}

func (s *Service) RebindWorkspace(ctx context.Context, req serverapi.ProjectRebindWorkspaceRequest) (serverapi.ProjectRebindWorkspaceResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectRebindWorkspaceResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectRebindWorkspaceResponse{}, errors.New("project service is required")
	}
	prepared, err := s.metadata.PrepareWorkspaceRebind(ctx, req.OldWorkspaceRoot)
	if err != nil {
		return serverapi.ProjectRebindWorkspaceResponse{}, err
	}
	if err := s.requireProjectID(prepared.ProjectID); err != nil {
		return serverapi.ProjectRebindWorkspaceResponse{}, err
	}
	lease, err := s.acquireProjectMutationLease(ctx, prepared.ProjectID)
	if err != nil {
		return serverapi.ProjectRebindWorkspaceResponse{}, err
	}
	defer lease.Release()
	binding, err := s.metadata.RebindWorkspaceWithExpectedBinding(
		ctx,
		req.OldWorkspaceRoot,
		req.NewWorkspaceRoot,
		prepared.ProjectID,
		prepared.WorkspaceID,
	)
	if err != nil {
		return serverapi.ProjectRebindWorkspaceResponse{}, err
	}
	return serverapi.ProjectRebindWorkspaceResponse{Binding: projectBindingFromMetadata(binding)}, nil
}

func (s *Service) GetProjectOverview(ctx context.Context, req serverapi.ProjectGetOverviewRequest) (serverapi.ProjectGetOverviewResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectGetOverviewResponse{}, err
	}
	if err := s.requireProjectID(req.ProjectID); err != nil {
		return serverapi.ProjectGetOverviewResponse{}, err
	}
	overview, err := s.metadata.GetProjectOverview(ctx, req.ProjectID)
	if err != nil {
		return serverapi.ProjectGetOverviewResponse{}, err
	}
	return serverapi.ProjectGetOverviewResponse{Overview: overview}, nil
}

func (s *Service) ListSessionPage(ctx context.Context, req serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionPageResponse{}, err
	}
	if err := s.requireProjectID(req.ProjectID); err != nil {
		return serverapi.SessionPageResponse{}, err
	}
	return s.metadata.ListSessionPage(ctx, req)
}

func (s *Service) requireProjectID(projectID string) error {
	if s == nil {
		return errors.New("project service is required")
	}
	if trimmedProjectID := strings.TrimSpace(s.projectID); trimmedProjectID != "" && strings.TrimSpace(projectID) != trimmedProjectID {
		return fmt.Errorf("project %q not available", strings.TrimSpace(projectID))
	}
	return nil
}

func (s *Service) acquireProjectMutationLease(ctx context.Context, projectID string) (*requestmemo.MutationLaneLease[string], error) {
	if s == nil || s.projectMutations == nil {
		return nil, errors.New("project mutation lanes are required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return nil, errors.New("project mutation requires a project id")
	}
	return s.projectMutations.Acquire(ctx, trimmedProjectID)
}

func (s *Service) projectHomeSummary(ctx context.Context, projectID string) (serverapi.ProjectHomeSummary, error) {
	projects, err := s.metadata.ListProjectHomeSummaries(ctx, projectID, 1, 0)
	if err != nil {
		return serverapi.ProjectHomeSummary{}, err
	}
	if len(projects) == 0 {
		return serverapi.ProjectHomeSummary{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, strings.TrimSpace(projectID))
	}
	return projects[0], nil
}

func parseProjectHomePageToken(token string) (int, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(trimmed)
	if err != nil || offset < 0 {
		return 0, errors.New("page_token is invalid")
	}
	return offset, nil
}

func availabilityForProjectPath(path string) string {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return string(clientui.ProjectAvailabilityMissing)
		}
		return string(clientui.ProjectAvailabilityInaccessible)
	}
	return string(clientui.ProjectAvailabilityAvailable)
}

func projectWorkspaceSummaryFromClientUI(workspace clientui.ProjectWorkspaceSummary) serverapi.ProjectWorkspaceSummary {
	return serverapi.ProjectWorkspaceSummary{
		WorkspaceID:     workspace.WorkspaceID,
		DisplayName:     workspace.DisplayName,
		RootPath:        workspace.RootPath,
		Availability:    string(workspace.Availability),
		IsPrimary:       workspace.IsPrimary,
		UpdatedAtUnixMs: workspace.UpdatedAt.UnixMilli(),
	}
}

func projectBindingFromMetadata(binding metadata.Binding) serverapi.ProjectBinding {
	return serverapi.ProjectBinding{
		ProjectID:       binding.ProjectID,
		ProjectKey:      binding.ProjectKey,
		ProjectName:     binding.ProjectName,
		WorkspaceID:     binding.WorkspaceID,
		CanonicalRoot:   binding.CanonicalRoot,
		WorkspaceName:   binding.WorkspaceName,
		WorkspaceStatus: binding.WorkspaceStatus,
	}
}

var _ servicecontract.ProjectViewService = (*Service)(nil)
