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
	runtimeAuthority  *sessionruntime.Authority
	taskMutations     *workflowexecution.TaskMutationCoordinator
	workflowExecution interface {
		EnsureTaskQuiescent(workflow.TaskID) error
	}
	workflowStore *workflowstore.Store
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

func NewMetadataService(metadataStore *metadata.Store) (*Service, error) {
	if metadataStore == nil {
		return nil, errors.New("metadata store is required")
	}
	return &Service{metadata: metadataStore}, nil
}

func (s *Service) WithRuntimeAuthority(authority *sessionruntime.Authority) *Service {
	if s == nil {
		return nil
	}
	s.runtimeAuthority = authority
	return s
}

func (s *Service) WithWorkflowExecution(taskMutations *workflowexecution.TaskMutationCoordinator, execution interface {
	EnsureTaskQuiescent(workflow.TaskID) error
}, store *workflowstore.Store) *Service {
	if s == nil {
		return nil
	}
	s.taskMutations = taskMutations
	s.workflowExecution = execution
	s.workflowStore = store
	return s
}

func (s *Service) ListProjects(ctx context.Context, _ serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error) {
	if s == nil {
		return serverapi.ProjectListResponse{}, errors.New("project service is required")
	}
	projects, err := s.metadata.ListProjects(ctx)
	if err != nil {
		return serverapi.ProjectListResponse{}, err
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
	summaries, err := s.metadata.ListProjectHomeSummaries(ctx, pageSize+1, offset)
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
	project, err := s.metadata.GetProjectEditMetadata(ctx, req.ProjectID)
	if err != nil {
		return serverapi.ProjectEditGetResponse{}, err
	}
	return serverapi.ProjectEditGetResponse{
		ProjectID:   project.ProjectID,
		ProjectKey:  project.ProjectKey,
		DisplayName: project.DisplayName,
	}, nil
}

func (s *Service) DeleteProject(ctx context.Context, req serverapi.ProjectDeleteRequest) (serverapi.ProjectDeleteResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ProjectDeleteResponse{}, err
	}
	if s == nil {
		return serverapi.ProjectDeleteResponse{}, errors.New("project service is required")
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if s.taskMutations == nil || s.workflowExecution == nil {
		return serverapi.ProjectDeleteResponse{}, errors.New("workflow execution is required for project deletion")
	}
	taskIDs, err := s.metadata.ListProjectTaskIDs(ctx, projectID)
	if err != nil {
		return serverapi.ProjectDeleteResponse{}, err
	}

	deleteProject := func(ctx context.Context) ([]serverapi.ProjectDeleteBlocker, error) {
		for _, taskID := range taskIDs {
			if err := s.workflowExecution.EnsureTaskQuiescent(workflow.TaskID(taskID)); err != nil {
				return nil, err
			}
		}
		if s.workflowStore == nil {
			return nil, errors.New("workflow store is required for project deletion")
		}
		sessionIDs, err := s.metadata.ListProjectSessionIDs(ctx, projectID)
		if err != nil {
			return nil, err
		}
		runtimeBlockers, release, err := withRuntimeBlockers(
			ctx,
			sessionIDs,
			s.projectActiveSessionBlockers,
			s.blockSessionStarts,
		)
		if release != nil {
			defer release()
		}
		if err != nil || len(runtimeBlockers) > 0 {
			return runtimeBlockers, err
		}
		currentTaskIDs, err := s.metadata.ListProjectTaskIDs(ctx, projectID)
		if err != nil {
			return nil, err
		}
		if !metadata.StringSetsEqual(taskIDs, currentTaskIDs) {
			return nil, workflowstore.ErrProjectDeletePreparationInvalidated
		}
		blockers, err := s.workflowStore.DeleteProject(ctx, workflowstore.ProjectDeleteRequest{
			ProjectID:          projectID,
			ExpectedSessionIDs: sessionIDs,
		})
		if err != nil || len(blockers) > 0 {
			return blockers, err
		}
		if err := deleteProjectSessionArtifacts(s.metadata.PersistenceRoot(), projectID); err != nil {
			return nil, fmt.Errorf("project %q was deleted, but session artifact cleanup failed: %w", projectID, err)
		}
		return nil, nil
	}
	workflowTaskIDs := make([]workflow.TaskID, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		workflowTaskIDs = append(workflowTaskIDs, workflow.TaskID(taskID))
	}
	var blockers []serverapi.ProjectDeleteBlocker
	err = s.taskMutations.RunMany(ctx, workflowTaskIDs, func(ctx context.Context) error {
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
	if s == nil || s.runtimeAuthority == nil {
		return func() {}, nil
	}
	ids, err := sessionruntime.ParseSessionIDs(sessionIDs)
	if err != nil || len(ids) == 0 {
		return func() {}, err
	}
	release, err := s.runtimeAuthority.BlockSessionStarts(ctx, ids, sessionruntime.SessionStartBlockMaintenance)
	if err != nil {
		return nil, err
	}
	return func() {
		if err := release.Close(context.Background()); err != nil {
			panic(fmt.Sprintf("release project session start block: %v", err))
		}
	}, nil
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
	if s == nil || s.runtimeAuthority == nil {
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
		active, err := s.runtimeAuthority.HasBlockingRuntimeActivity(ctx, id.String())
		if err != nil {
			return 0, err
		}
		if active {
			count++
		}
	}
	return count, nil
}

func deleteProjectSessionArtifacts(persistenceRoot string, projectID string) error {
	root, err := persistenceRootPath(persistenceRoot)
	if err != nil {
		return err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" || projectID == "." || projectID == ".." || filepath.Base(projectID) != projectID {
		return fmt.Errorf("invalid project id %q", projectID)
	}
	sessionsRoot := filepath.Join(root, "projects", projectID, "sessions")
	if err := rejectSymlinkComponents(root, sessionsRoot); err != nil {
		return fmt.Errorf("validate project sessions root: %w", err)
	}
	if err := os.RemoveAll(sessionsRoot); err != nil {
		return fmt.Errorf("remove project sessions root: %w", err)
	}
	return nil
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
	if s == nil {
		return serverapi.ProjectGetOverviewResponse{}, errors.New("project service is required")
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
	if s == nil {
		return serverapi.SessionPageResponse{}, errors.New("project service is required")
	}
	return s.metadata.ListSessionPage(ctx, req)
}

func (s *Service) projectHomeSummary(ctx context.Context, projectID string) (serverapi.ProjectHomeSummary, error) {
	return s.metadata.GetProjectHomeSummary(ctx, projectID)
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
