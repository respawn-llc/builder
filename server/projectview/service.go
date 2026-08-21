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
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/serverapi"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
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

func (s *Service) ListProjects(ctx context.Context, req *emptypb.Empty) (*projectpb.ProjectListSuccess, error) {
	if req == nil {
		return nil, errors.New("project list request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	projects, err := s.metadata.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	success := &projectpb.ProjectListSuccess{Projects: make([]*projectpb.ProjectSummary, 0, len(projects))}
	for _, project := range projects {
		generated, err := projectSummaryToGenerated(project)
		if err != nil {
			return nil, err
		}
		success.Projects = append(success.Projects, generated)
	}
	return success, nil
}

func (s *Service) ListProjectHome(ctx context.Context, req *projectpb.ProjectHomeListRequest) (*projectpb.ProjectHomeListSuccess, error) {
	if req == nil {
		return nil, errors.New("project home list request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	pageSize := int(req.GetPageSize())
	if pageSize == 0 {
		pageSize = defaultProjectHomePageSize
	}
	if pageSize > maxProjectHomePageSize {
		pageSize = maxProjectHomePageSize
	}
	offset, err := parseProjectHomePageToken(req.GetPageToken())
	if err != nil {
		return nil, err
	}
	summaries, err := s.metadata.ListProjectHomeSummaries(ctx, pageSize+1, offset)
	if err != nil {
		return nil, err
	}
	var nextPageToken *string
	if len(summaries) > pageSize {
		summaries = summaries[:pageSize]
		value := strconv.Itoa(offset + pageSize)
		nextPageToken = &value
	}
	success := &projectpb.ProjectHomeListSuccess{
		Projects:      make([]*projectpb.ProjectHomeSummary, 0, len(summaries)),
		NextPageToken: nextPageToken,
		GeneratedAt:   timestamppb.New(time.Now().UTC()),
	}
	for _, summary := range summaries {
		project, err := projectHomeSummaryToGenerated(summary)
		if err != nil {
			return nil, err
		}
		success.Projects = append(success.Projects, project)
	}
	return success, nil
}

func (s *Service) ResolveProjectPath(ctx context.Context, req *projectpb.ResolvePathRequest) (*projectpb.ResolvePathSuccess, error) {
	if req == nil {
		return nil, errors.New("project path request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	canonicalRoot, binding, err := s.metadata.ResolveWorkspacePath(ctx, req.Path)
	if err != nil {
		return nil, err
	}
	availability, err := projectAvailabilityToGenerated(clientui.ProjectAvailability(availabilityForProjectPath(canonicalRoot)))
	if err != nil {
		return nil, err
	}
	resp := &projectpb.ResolvePathSuccess{CanonicalRoot: canonicalRoot, PathAvailability: availability}
	if binding != nil {
		resp.Binding, err = projectBindingToGenerated(*binding)
		if err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func (s *Service) PlanWorkspaceBinding(ctx context.Context, req *projectpb.PlanWorkspaceBindingRequest) (*projectpb.PlanWorkspaceBindingSuccess, error) {
	if req == nil {
		return nil, errors.New("workspace binding plan request is required")
	}
	resolved, err := s.ResolveProjectPath(ctx, &projectpb.ResolvePathRequest{Path: req.Path})
	if err != nil {
		if ambiguous, ok := serverapi.AsWorkspaceBindingAmbiguous(err); ok {
			availability, availabilityErr := projectAvailabilityToGenerated(clientui.ProjectAvailability(availabilityForProjectPath(ambiguous.CanonicalRoot)))
			if availabilityErr != nil {
				return nil, availabilityErr
			}
			resp := &projectpb.PlanWorkspaceBindingSuccess{
				CanonicalRoot:    ambiguous.CanonicalRoot,
				PathAvailability: availability,
			}
			switch req.Mode {
			case projectpb.WorkspaceBindingPlanMode_WORKSPACE_BINDING_PLAN_MODE_INTERACTIVE:
				projects, err := s.ListProjects(ctx, &emptypb.Empty{})
				if err != nil {
					return nil, err
				}
				resp.Kind = projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_SERVER_WORKSPACE_SELECTION
				resp.Projects = projects.Projects
				return resp, nil
			case projectpb.WorkspaceBindingPlanMode_WORKSPACE_BINDING_PLAN_MODE_HEADLESS:
				resp.Kind = projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_HEADLESS_REMOTE_AMBIGUOUS
				return resp, nil
			default:
				return nil, errors.New("mode must be interactive or headless")
			}
		}
		return nil, err
	}
	resp := &projectpb.PlanWorkspaceBindingSuccess{
		CanonicalRoot:    resolved.CanonicalRoot,
		PathAvailability: resolved.PathAvailability,
		Binding:          resolved.Binding,
	}
	if resolved.Binding != nil {
		resp.Kind = projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_BOUND
		return resp, nil
	}
	switch req.Mode {
	case projectpb.WorkspaceBindingPlanMode_WORKSPACE_BINDING_PLAN_MODE_INTERACTIVE:
		projects, err := s.ListProjects(ctx, &emptypb.Empty{})
		if err != nil {
			return nil, err
		}
		resp.Projects = projects.Projects
		if resolved.PathAvailability == projectpb.ProjectAvailability_PROJECT_AVAILABILITY_MISSING ||
			resolved.PathAvailability == projectpb.ProjectAvailability_PROJECT_AVAILABILITY_INACCESSIBLE {
			resp.Kind = projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_SERVER_WORKSPACE_SELECTION
			return resp, nil
		}
		resp.Kind = projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_LOCAL_UNBOUND
		return resp, nil
	case projectpb.WorkspaceBindingPlanMode_WORKSPACE_BINDING_PLAN_MODE_HEADLESS:
		if resolved.PathAvailability == projectpb.ProjectAvailability_PROJECT_AVAILABILITY_AVAILABLE {
			resp.Kind = projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_LOCAL_UNBOUND
			return resp, nil
		}
		workspace, found, err := s.selectSingleAvailableWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		if !found {
			resp.Kind = projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_HEADLESS_REMOTE_AMBIGUOUS
			return resp, nil
		}
		resp.Kind = projectpb.WorkspaceBindingPlanKind_WORKSPACE_BINDING_PLAN_KIND_HEADLESS_REMOTE_SELECTED
		resp.Workspace = workspace
		return resp, nil
	default:
		return nil, errors.New("mode must be interactive or headless")
	}
}

func (s *Service) CreateProject(ctx context.Context, req *projectpb.CreateProjectRequest) (*projectpb.CreateProjectSuccess, error) {
	if req == nil {
		return nil, errors.New("create project request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	binding, err := s.metadata.CreateProjectForWorkspaceWithKey(ctx, req.WorkspaceRoot, req.DisplayName, req.GetProjectKey())
	if err != nil {
		return nil, projectMutationStorageError(err, req.GetProjectKey())
	}
	generated, err := projectMutationBindingToGenerated(binding)
	if err != nil {
		return nil, err
	}
	return &projectpb.CreateProjectSuccess{Binding: generated}, nil
}

func (s *Service) UpdateProject(ctx context.Context, req *projectpb.UpdateProjectRequest) (*projectpb.UpdateProjectSuccess, error) {
	if req == nil {
		return nil, errors.New("update project request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	if err := s.metadata.UpdateProjectMetadata(ctx, req.ProjectId, req.DisplayName, req.GetProjectKey()); err != nil {
		return nil, projectMutationStorageError(err, req.GetProjectKey())
	}
	project, err := s.metadata.GetProjectHomeSummary(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	generated, err := projectHomeSummaryToGenerated(project)
	if err != nil {
		return nil, err
	}
	return &projectpb.UpdateProjectSuccess{Project: generated}, nil
}

func (s *Service) GetProjectEdit(ctx context.Context, req *projectpb.ProjectEditGetRequest) (*projectpb.GetProjectEditSuccess, error) {
	if req == nil {
		return nil, errors.New("get project edit request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	project, err := s.metadata.GetProjectEditMetadata(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	return &projectpb.GetProjectEditSuccess{
		ProjectId:   project.ProjectID,
		ProjectKey:  project.ProjectKey,
		DisplayName: project.DisplayName,
	}, nil
}

func (s *Service) DeleteProject(ctx context.Context, req *projectpb.DeleteProjectRequest) (*projectpb.DeleteProjectSuccess, error) {
	if req == nil {
		return nil, errors.New("delete project request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	projectID := strings.TrimSpace(req.ProjectId)
	if s.taskMutations == nil || s.workflowExecution == nil {
		return nil, errors.New("workflow execution is required for project deletion")
	}
	taskIDs, err := s.metadata.ListProjectTaskIDs(ctx, projectID)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	generatedBlockers := make([]*projectpb.ProjectDeleteBlocker, 0, len(blockers))
	for _, blocker := range blockers {
		count, conversionErr := nonNegativeInt32(blocker.Count, "project delete blocker count")
		if conversionErr != nil {
			return nil, conversionErr
		}
		generatedBlockers = append(generatedBlockers, &projectpb.ProjectDeleteBlocker{Code: blocker.Code, Count: count})
	}
	if len(blockers) > 0 {
		return &projectpb.DeleteProjectSuccess{ProjectId: projectID, Blockers: generatedBlockers}, nil
	}
	return &projectpb.DeleteProjectSuccess{ProjectId: projectID, Deleted: true}, nil
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
		Code:  "active_sessions",
		Count: count,
	}}, nil
}

func (s *Service) workspaceActiveSessionBlockers(ctx context.Context, sessionIDs []string) ([]serverapi.ProjectWorkspaceUnlinkBlocker, error) {
	count, err := s.countBlockingRuntimeActivity(ctx, sessionIDs)
	if err != nil || count == 0 {
		return nil, err
	}
	return []serverapi.ProjectWorkspaceUnlinkBlocker{{
		Code:  "active_sessions",
		Count: count,
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

func (s *Service) selectSingleAvailableWorkspace(ctx context.Context) (*projectpb.SelectedProjectWorkspace, bool, error) {
	projects, err := s.metadata.ListProjects(ctx)
	if err != nil {
		return nil, false, err
	}
	var selection *projectpb.SelectedProjectWorkspace
	count := 0
	for _, project := range projects {
		overview, err := s.metadata.GetProjectOverview(ctx, project.ProjectID)
		if err != nil {
			return nil, false, err
		}
		for _, workspace := range overview.Workspaces {
			availability := strings.TrimSpace(string(workspace.Availability))
			if availability != "" && workspace.Availability != clientui.ProjectAvailabilityAvailable {
				continue
			}
			count++
			selection = &projectpb.SelectedProjectWorkspace{ProjectId: project.ProjectID, WorkspaceId: workspace.WorkspaceID}
			if count > 1 {
				return nil, false, nil
			}
		}
	}
	if count == 0 {
		return nil, false, nil
	}
	return selection, true, nil
}

func (s *Service) ListProjectWorkspaces(ctx context.Context, req *projectpb.ProjectWorkspaceListRequest) (*projectpb.ListProjectWorkspacesSuccess, error) {
	if req == nil {
		return nil, errors.New("project workspace list request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	page, err := s.metadata.ListProjectWorkspaceCatalogPage(ctx, req.ProjectId, int(req.Offset), int(req.Limit))
	if err != nil {
		return nil, err
	}
	response := &projectpb.ListProjectWorkspacesSuccess{
		ProjectId:  req.ProjectId,
		Offset:     req.Offset,
		Workspaces: make([]*projectpb.ProjectWorkspaceCatalogSummary, 0, len(page.Workspaces)),
	}
	for _, workspace := range page.Workspaces {
		response.Workspaces = append(response.Workspaces, projectWorkspaceCatalogRowToGenerated(workspace))
	}
	if page.NextOffset != nil {
		nextOffset, err := nonNegativeInt32(*page.NextOffset, "project workspace next offset")
		if err != nil {
			return nil, err
		}
		response.NextOffset = &nextOffset
	}
	return response, nil
}

func (s *Service) GetProjectWorkspace(ctx context.Context, req *projectpb.GetProjectWorkspaceRequest) (*projectpb.GetProjectWorkspaceSuccess, error) {
	if req == nil {
		return nil, errors.New("get project workspace request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	selector, err := projectWorkspaceGetSelectorFromGenerated(req)
	if err != nil {
		return nil, err
	}
	workspace, err := s.metadata.GetProjectWorkspaceCatalogRow(ctx, req.ProjectId, selector)
	if errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		return &projectpb.GetProjectWorkspaceSuccess{
			ProjectId: req.ProjectId,
			Result:    projectpb.ProjectWorkspaceGetResult_PROJECT_WORKSPACE_GET_RESULT_NOT_ATTACHED,
		}, nil
	}
	if err != nil {
		return nil, err
	}
	return &projectpb.GetProjectWorkspaceSuccess{
		ProjectId: req.ProjectId,
		Result:    projectpb.ProjectWorkspaceGetResult_PROJECT_WORKSPACE_GET_RESULT_ATTACHED,
		Workspace: projectWorkspaceCatalogRowToGenerated(workspace),
	}, nil
}

func (s *Service) AttachWorkspaceToProject(ctx context.Context, req *projectpb.AttachWorkspaceRequest) (*projectpb.AttachWorkspaceSuccess, error) {
	if req == nil {
		return nil, errors.New("attach workspace request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	result, err := s.metadata.AttachWorkspaceToProjectWithResult(ctx, req.ProjectId, req.WorkspaceRoot)
	if err != nil {
		return nil, projectMutationStorageError(err, "")
	}
	outcome := projectpb.ProjectWorkspaceAttachOutcome_PROJECT_WORKSPACE_ATTACH_OUTCOME_ALREADY_ATTACHED
	if result.Attached {
		outcome = projectpb.ProjectWorkspaceAttachOutcome_PROJECT_WORKSPACE_ATTACH_OUTCOME_ATTACHED
	}
	binding, err := projectMutationBindingToGenerated(result.Binding)
	if err != nil {
		return nil, err
	}
	return &projectpb.AttachWorkspaceSuccess{Binding: binding, Outcome: outcome}, nil
}

func (s *Service) RebindWorkspace(ctx context.Context, req *projectpb.RebindWorkspaceRequest) (*projectpb.RebindWorkspaceSuccess, error) {
	if req == nil {
		return nil, errors.New("rebind workspace request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	prepared, err := s.metadata.PrepareWorkspaceRebind(ctx, req.OldWorkspaceRoot)
	if err != nil {
		return nil, projectMutationStorageError(err, "")
	}
	binding, err := s.metadata.RebindWorkspaceWithExpectedBinding(
		ctx,
		req.OldWorkspaceRoot,
		req.NewWorkspaceRoot,
		prepared.ProjectID,
		prepared.WorkspaceID,
	)
	if err != nil {
		return nil, projectMutationStorageError(err, "")
	}
	generated, err := projectMutationBindingToGenerated(binding)
	if err != nil {
		return nil, err
	}
	return &projectpb.RebindWorkspaceSuccess{Binding: generated}, nil
}

func projectMutationStorageError(err error, projectKey string) error {
	switch {
	case errors.Is(err, metadata.ErrProjectKeyAlreadyInUse):
		return serverapi.ProjectKeyConflictError{ProjectKey: strings.TrimSpace(projectKey)}
	case errors.Is(err, metadata.ErrWorkspaceAlreadyBound):
		return errors.Join(serverapi.ErrWorkspaceAlreadyBound, err)
	case errors.Is(err, metadata.ErrWorkspacePathMissing):
		return errors.Join(serverapi.ErrWorkspacePathMissing, err)
	default:
		return err
	}
}

func (s *Service) GetProjectOverview(ctx context.Context, req *projectpb.GetOverviewRequest) (*projectpb.GetOverviewSuccess, error) {
	if req == nil {
		return nil, errors.New("get project overview request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	overview, err := s.metadata.GetProjectOverview(ctx, req.ProjectId)
	if err != nil {
		return nil, err
	}
	project, err := projectSummaryToGenerated(overview.Project)
	if err != nil {
		return nil, err
	}
	workspaces := make([]*projectpb.ProjectWorkspaceSummary, 0, len(overview.Workspaces))
	for _, workspace := range overview.Workspaces {
		generated, err := projectWorkspaceSummaryToGenerated(workspace)
		if err != nil {
			return nil, err
		}
		workspaces = append(workspaces, generated)
	}
	return &projectpb.GetOverviewSuccess{
		Overview: &projectpb.ProjectOverview{Project: project, Workspaces: workspaces},
	}, nil
}

func (s *Service) ListSessionPage(ctx context.Context, req *projectpb.SessionPageRequest) (*projectpb.SessionPageSuccess, error) {
	if req == nil {
		return nil, errors.New("session page request is required")
	}
	if s == nil {
		return nil, errors.New("project service is required")
	}
	category, err := sessionCategoryFromGenerated(req.Category)
	if err != nil {
		return nil, err
	}
	offset := 0
	if req.Offset != nil {
		offset = int(*req.Offset)
	}
	limit := metadata.MaxSessionPageSize
	if req.Limit != nil {
		limit = int(*req.Limit)
	}
	page, err := s.metadata.ListSessionPage(ctx, req.ProjectId, category, offset, limit)
	if err != nil {
		return nil, err
	}
	return sessionPageToGenerated(page)
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

var _ servicecontract.ProjectViewService = (*Service)(nil)
