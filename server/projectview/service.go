package projectview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"core/server/metadata"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type Service struct {
	metadata  *metadata.Store
	projectID string
}

// ErrSessionArtifactEscapesRoot is returned when a session artifact path
// resolves outside its project sessions root. Callers and tests match this with
// errors.Is rather than comparing rendered message text.
var ErrSessionArtifactEscapesRoot = errors.New("session artifact path escapes project sessions root")

var projectDeleteLocks = keyedProjectDeleteLocks{locks: map[string]*projectDeleteLock{}}

type keyedProjectDeleteLocks struct {
	mu    sync.Mutex
	locks map[string]*projectDeleteLock
}

type projectDeleteLock struct {
	mu   sync.Mutex
	refs int
}

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
	return &Service{metadata: metadataStore, projectID: strings.TrimSpace(projectID)}, nil
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

func (s *Service) SetDefaultWorkspace(ctx context.Context, req serverapi.ProjectDefaultWorkspaceSetRequest) (serverapi.ProjectDefaultWorkspaceSetResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, errors.New("project service is required")
	}
	if err := s.requireProjectID(req.ProjectID); err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, err
	}
	if err := s.metadata.SetProjectDefaultWorkspace(ctx, req.ProjectID, req.WorkspaceID); err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, err
	}
	project, err := s.projectHomeSummary(ctx, req.ProjectID)
	if err != nil {
		return serverapi.ProjectDefaultWorkspaceSetResponse{}, err
	}
	return serverapi.ProjectDefaultWorkspaceSetResponse{Project: project}, nil
}

func (s *Service) UnlinkWorkspaceFromProject(ctx context.Context, req serverapi.ProjectWorkspaceUnlinkRequest) (serverapi.ProjectWorkspaceUnlinkResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, errors.New("project service is required")
	}
	if err := s.requireProjectID(req.ProjectID); err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, err
	}
	blockers, err := s.metadata.UnlinkProjectWorkspace(ctx, req.ProjectID, req.WorkspaceID)
	if err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, err
	}
	resp := serverapi.ProjectWorkspaceUnlinkResponse{
		ProjectID:   strings.TrimSpace(req.ProjectID),
		WorkspaceID: strings.TrimSpace(req.WorkspaceID),
		Blockers:    blockers,
		Unlinked:    len(blockers) == 0,
	}
	if !resp.Unlinked {
		return resp, nil
	}
	projects, err := s.metadata.ListProjectHomeSummaries(ctx, req.ProjectID, 1, 0)
	if err != nil {
		return serverapi.ProjectWorkspaceUnlinkResponse{}, err
	}
	if len(projects) > 0 {
		resp.Project = &projects[0]
	}
	return resp, nil
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
	unlock := lockProjectDelete(projectID)
	defer unlock()

	if _, err := s.projectHomeSummary(ctx, projectID); err != nil {
		return serverapi.ProjectDeleteResponse{}, err
	}
	blockers, err := s.metadata.DeleteProject(ctx, projectID, func(artifact metadata.ProjectSessionArtifact, remove bool) error {
		return deleteSessionArtifact(s.metadata.PersistenceRoot(), projectID, artifact.ArtifactRelpath, remove)
	})
	if err != nil {
		return serverapi.ProjectDeleteResponse{}, err
	}
	if len(blockers) > 0 {
		return serverapi.ProjectDeleteResponse{ProjectID: projectID, Deleted: false, Blockers: blockers}, nil
	}
	return serverapi.ProjectDeleteResponse{ProjectID: projectID, Deleted: true}, nil
}

func lockProjectDelete(projectID string) func() {
	// Artifact paths are persisted under projects/<projectID>/sessions and are DB-unique.
	// A per-project lock serializes retries for the same tree without blocking disjoint projects.
	projectDeleteLocks.mu.Lock()
	lock := projectDeleteLocks.locks[projectID]
	if lock == nil {
		lock = &projectDeleteLock{}
		projectDeleteLocks.locks[projectID] = lock
	}
	lock.refs++
	projectDeleteLocks.mu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		projectDeleteLocks.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(projectDeleteLocks.locks, projectID)
		}
		projectDeleteLocks.mu.Unlock()
	}
}

func deleteSessionArtifact(persistenceRoot string, projectID string, relpath string, remove bool) error {
	cleanRelpath := filepath.Clean(strings.TrimSpace(relpath))
	if cleanRelpath == "." || filepath.IsAbs(cleanRelpath) || cleanRelpath == ".." || strings.HasPrefix(cleanRelpath, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid session artifact path %q", relpath)
	}
	root, err := filepath.Abs(filepath.Clean(persistenceRoot))
	if err != nil {
		return fmt.Errorf("resolve persistence root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve persistence root symlinks: %w", err)
	}
	projectRoot := filepath.Join(root, "projects", strings.TrimSpace(projectID), "sessions")
	target, err := filepath.Abs(filepath.Join(root, cleanRelpath))
	if err != nil {
		return fmt.Errorf("resolve session artifact path: %w", err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("resolve session artifact symlinks: %w", err)
	}
	if err == nil {
		target = resolvedTarget
	}
	inside, err := filepath.Rel(projectRoot, target)
	if err != nil {
		return fmt.Errorf("validate session artifact path: %w", err)
	}
	if inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return fmt.Errorf("session artifact path %q escapes project sessions root: %w", relpath, ErrSessionArtifactEscapesRoot)
	}
	if !remove {
		return nil
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("delete session artifact %q: %w", relpath, err)
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
	binding, err := s.metadata.RebindWorkspace(ctx, req.OldWorkspaceRoot, req.NewWorkspaceRoot)
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

func (s *Service) ListSessionsByProject(ctx context.Context, req serverapi.SessionListByProjectRequest) (serverapi.SessionListByProjectResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.SessionListByProjectResponse{}, err
	}
	if err := s.requireProjectID(req.ProjectID); err != nil {
		return serverapi.SessionListByProjectResponse{}, err
	}
	sessions, err := s.metadata.ListSessionsByProject(ctx, req.ProjectID)
	if err != nil {
		return serverapi.SessionListByProjectResponse{}, err
	}
	return serverapi.SessionListByProjectResponse{Sessions: sessions}, nil
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
