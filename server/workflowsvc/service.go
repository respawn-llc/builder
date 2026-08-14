package workflowsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowscript"
	"core/server/workflowstore"
	"core/server/workflowview"
	"core/server/worktree"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/worktreecontract"
)

type Service struct {
	store                *workflowstore.Store
	readModels           ReadModels
	roleResolver         workflow.RoleResolver
	executionTargets     executionTargetInfrastructure
	taskWorktreeCleanup  taskWorktreeDeleter
	events               *workflowProjectEventBroker
	attentionFinalizer   workflowAttentionFinalizer
	setupEvents          workflowTaskSetupEventPublisher
	taskMutations        *workflowexecution.TaskMutationCoordinator
	currentNodeExecution interface {
		StartTask(context.Context, workflow.TaskID, workflowexecution.TaskStartPreparation, workflowexecution.TaskPreparationFinalizer) (workflowstore.StartTaskResult, error)
		PromoteConcurrencyQueuedTask(context.Context, workflow.TaskID) ([]workflow.CurrentNode, bool, error)
		PreflightTaskResume(context.Context, workflow.TaskID) (workflowexecution.TaskResumePreflight, error)
		ResumeTask(context.Context, workflow.TaskID) (workflowexecution.TaskResumeResult, error)
		ResumeTaskWithPreparation(context.Context, workflow.TaskID, workflowexecution.TaskStartPreparation, workflowexecution.TaskPreparationFinalizer) (workflowexecution.TaskResumeResult, error)
		ApplyPendingApproval(context.Context, workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error)
		ApplyManualMove(context.Context, workflowstore.ManualMovePreparation, *workflowstore.ExecutionTargetCandidate) (workflowstore.ManualMoveResult, error)
		ManualMoveDisposition(workflow.TaskID) (workflowexecution.ManualMoveDisposition, error)
		InterruptForManualMove(context.Context, workflow.TaskID, func() error) error
		Interrupt(context.Context, workflowexecution.InterruptSelector) error
		EnsureTaskQuiescent(workflow.TaskID) error
		CompleteSessionCurrentNode(context.Context, runtimeids.SessionID, string, map[string]string, string) (workflowstore.CurrentNodeCompletionResult, error)
		CompleteIdleCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector, string, map[string]string, string) (workflowstore.CurrentNodeCompletionResult, error)
	}
}

type initiatingActionTargetDecision struct {
	prepared          *preparedInitiatingActionTarget
	selectionRequired *serverapi.WorkflowExecutionTargetSelectionRequirement
}

type preparedInitiatingActionTarget struct {
	candidate                *workflowstore.ExecutionTargetCandidate
	retainedWorktree         *serverapi.WorktreeTopologyEntry
	retainedPreviousWorktree *serverapi.RetainedPreviousWorktree
	setupResult              *worktree.WorktreeSetupResult
}

type initiatingActionTargetPreflight struct {
	context                workflowstore.TaskExecutionTargetContext
	selection              workflow.ExecutionTargetSelection
	explicit               bool
	initialBranchAssertion *string
	pendingBranchReplaced  bool
	unavailable            initiatingActionTargetUnavailable
}

type initiatingActionTargetUnavailable uint8

const (
	initiatingActionTargetRequestSelection initiatingActionTargetUnavailable = iota
	initiatingActionTargetInterrupt
)

type initiatingActionRequest struct {
	taskID                  workflow.TaskID
	setupOperationID        *serverapi.WorktreeSetupOperationID
	requiresExecutionTarget bool
	targetPreflight         initiatingActionTargetPreflight
	afterTargetResolution   func() error
}

type initiatingActionResult[T any] struct {
	applied                  *T
	selectionRequired        *serverapi.WorkflowExecutionTargetSelectionRequirement
	retainedPreviousWorktree *serverapi.RetainedPreviousWorktree
}

type initiatingActionPreflight struct {
	unsatisfiedDependencyCount int
	target                     initiatingActionTargetPreflight
}

type manualMovePreflight struct {
	preparation                workflowstore.ManualMovePreparation
	unsatisfiedDependencyCount int
	target                     initiatingActionTargetPreflight
}

type manualMoveNoOpBeforeInterruptError struct {
	currentNodes []workflow.CurrentNode
}

func (e *manualMoveNoOpBeforeInterruptError) Error() string {
	return "manual move became a no-op before interruption"
}

type executionTargetInfrastructure interface {
	InspectProspectiveInitialTaskBranch(context.Context, InitialTaskBranchInspectionRequest) error
	AssertInitialTaskBranch(context.Context, InitialTaskBranchAssertionRequest) error
	ResolveExecutionTarget(context.Context, ExecutionTargetResolveRequest) (workflowstore.ExecutionTargetSnapshot, error)
	MaterializeExecutionTarget(context.Context, ExecutionTargetMaterializeRequest) (ExecutionTargetMaterialization, error)
	RestoreExecutionTarget(context.Context, ExecutionTargetRestoreRequest) error
}

type InitialTaskBranchInspectionRequest struct {
	SourceWorkspaceRoot string
	BranchName          string
}

type InitialTaskBranchAssertionRequest struct {
	TaskID     workflow.TaskID
	BranchName string
}

type ExecutionTargetResolveRequest struct {
	SourceWorkspaceRoot string
	Selection           workflow.ExecutionTargetSelection
}

type ExecutionTargetMaterializeRequest struct {
	TaskID                 workflow.TaskID
	SetupOperationID       *serverapi.WorktreeSetupOperationID
	Snapshot               workflowstore.ExecutionTargetSnapshot
	SetupRequirement       worktreecontract.SetupRequirement
	InitialBranchAssertion *string
}

type ExecutionTargetMaterialization struct {
	RetainedRoot             *workflowstore.ManagedExecutionRoot
	SetupResult              *worktree.WorktreeSetupResult
	RetainedWorktree         *serverapi.WorktreeTopologyEntry
	RetainedPreviousWorktree *serverapi.RetainedPreviousWorktree
}

type ExecutionTargetRestoreRequest struct {
	TaskID                 workflow.TaskID
	SetupOperationID       *serverapi.WorktreeSetupOperationID
	InitialBranchAssertion *string
}

var errExecutionTargetInfrastructureRequired = errors.New("execution target infrastructure is required")

type taskWorktreeDeleter interface {
	EnsureTaskWorktreeDeletable(ctx context.Context, taskID string) error
	DeleteTaskWorktree(ctx context.Context, taskID string) error
}

type workflowAttentionFinalizer interface {
	FinalizeTaskResolution(workflowstore.TaskAttentionResolution)
	PublishPendingApproval(context.Context, workflow.ApprovalID)
}

type workflowTaskSetupEventPublisher interface {
	PublishWorkflowTaskSetupEvent(serverapi.WorktreeSetupEvent)
}

const (
	workflowAttentionFinalizationTimeout = 5 * time.Second
)

type Option func(*Service)

func WithCurrentNodeExecution(execution interface {
	StartTask(context.Context, workflow.TaskID, workflowexecution.TaskStartPreparation, workflowexecution.TaskPreparationFinalizer) (workflowstore.StartTaskResult, error)
	PromoteConcurrencyQueuedTask(context.Context, workflow.TaskID) ([]workflow.CurrentNode, bool, error)
	PreflightTaskResume(context.Context, workflow.TaskID) (workflowexecution.TaskResumePreflight, error)
	ResumeTask(context.Context, workflow.TaskID) (workflowexecution.TaskResumeResult, error)
	ResumeTaskWithPreparation(context.Context, workflow.TaskID, workflowexecution.TaskStartPreparation, workflowexecution.TaskPreparationFinalizer) (workflowexecution.TaskResumeResult, error)
	ApplyPendingApproval(context.Context, workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error)
	ApplyManualMove(context.Context, workflowstore.ManualMovePreparation, *workflowstore.ExecutionTargetCandidate) (workflowstore.ManualMoveResult, error)
	ManualMoveDisposition(workflow.TaskID) (workflowexecution.ManualMoveDisposition, error)
	InterruptForManualMove(context.Context, workflow.TaskID, func() error) error
	Interrupt(context.Context, workflowexecution.InterruptSelector) error
	EnsureTaskQuiescent(workflow.TaskID) error
	CompleteSessionCurrentNode(context.Context, runtimeids.SessionID, string, map[string]string, string) (workflowstore.CurrentNodeCompletionResult, error)
	CompleteIdleCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector, string, map[string]string, string) (workflowstore.CurrentNodeCompletionResult, error)
}) Option {
	return func(s *Service) {
		s.currentNodeExecution = execution
	}
}

func WithExecutionTargetInfrastructure(infrastructure executionTargetInfrastructure) Option {
	return func(s *Service) {
		s.executionTargets = infrastructure
	}
}

func WithTaskWorktreeDeleter(deleter taskWorktreeDeleter) Option {
	return func(s *Service) {
		s.taskWorktreeCleanup = deleter
	}
}

func WithWorkflowAttentionFinalizer(finalizer workflowAttentionFinalizer) Option {
	return func(s *Service) {
		s.attentionFinalizer = finalizer
	}
}

func WithWorkflowTaskSetupEventPublisher(publisher workflowTaskSetupEventPublisher) Option {
	return func(s *Service) {
		s.setupEvents = publisher
	}
}

func New(store *workflowstore.Store, readModels ReadModels, roleResolver workflow.RoleResolver, taskMutations *workflowexecution.TaskMutationCoordinator, opts ...Option) (*Service, error) {
	if store == nil {
		return nil, errors.New("workflow store is required")
	}
	if taskMutations == nil {
		return nil, errors.New("task mutation coordinator is required")
	}
	if err := readModels.validate(); err != nil {
		return nil, err
	}
	events := newWorkflowProjectEventBroker()
	store.SetWorkflowEventPublisher(events)
	service := &Service{
		store:         store,
		readModels:    readModels,
		roleResolver:  roleResolver,
		events:        events,
		taskMutations: taskMutations,
	}
	for _, opt := range opts {
		opt(service)
	}
	if service.currentNodeExecution == nil {
		return nil, errors.New("current node workflow execution is required")
	}
	return service, nil
}

func (s *Service) CreateWorkflow(ctx context.Context, req serverapi.WorkflowCreateRequest) (serverapi.WorkflowCreateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowCreateResponse{}, err
	}
	created, err := s.store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: req.Name, Description: req.Description})
	if err != nil {
		return serverapi.WorkflowCreateResponse{}, err
	}
	return serverapi.WorkflowCreateResponse{Workflow: workflowRecord(created)}, nil
}

func (s *Service) CreateAndLinkWorkflowToProject(ctx context.Context, request serverapi.WorkflowCreateAndLinkProjectRequest) (serverapi.WorkflowCreateAndLinkProjectResponse, error) {
	if err := request.Validate(); err != nil {
		return serverapi.WorkflowCreateAndLinkProjectResponse{}, err
	}
	created, link, err := s.store.CreateAndLinkWorkflow(ctx, workflowstore.CreateAndLinkWorkflowRequest{
		Name:          request.Name,
		Description:   request.Description,
		ProjectID:     request.ProjectID,
		DefaultPolicy: workflowStoreDefaultPolicy(request.DefaultPolicy),
	})
	if err != nil {
		return serverapi.WorkflowCreateAndLinkProjectResponse{}, err
	}
	s.publishProjectWorkflowEvent(ctx, request.ProjectID, created.ID, serverapi.WorkflowProjectEventResourceWorkflowLink, serverapi.WorkflowProjectEventActionLinked, link.ID)
	return serverapi.WorkflowCreateAndLinkProjectResponse{Workflow: workflowRecord(created), Link: projectWorkflowLink(link)}, nil
}

func (s *Service) publishWorkflowEvent(ctx context.Context, event workflowstore.WorkflowEventRecord) {
	if err := s.store.PublishWorkflowEvent(ctx, event); err != nil {
		slog.Warn("publish workflow event failed", "project_id", event.ProjectID, "workflow_id", event.WorkflowID, "resource", event.Resource, "action", event.Action, "primary_entity_id", event.PrimaryEntityID, "related_ids", event.RelatedIDs, "error", err)
	}
}

func (s *Service) publishProjectWorkflowEvent(ctx context.Context, projectID string, workflowID runtimeids.WorkflowID, resource serverapi.WorkflowProjectEventResource, action serverapi.WorkflowProjectEventAction, primaryEntityID string, relatedIDs ...string) {
	s.publishWorkflowEvent(ctx, workflowstore.WorkflowEventRecord{
		ProjectID:       &projectID,
		WorkflowID:      &workflowID,
		Resource:        resource,
		Action:          action,
		PrimaryEntityID: primaryEntityID,
		RelatedIDs:      relatedIDs,
	})
}

func (s *Service) publishGlobalWorkflowEvent(ctx context.Context, workflowID runtimeids.WorkflowID, resource serverapi.WorkflowProjectEventResource, action serverapi.WorkflowProjectEventAction, primaryEntityID string, relatedIDs ...string) {
	s.publishWorkflowEvent(ctx, workflowstore.WorkflowEventRecord{
		WorkflowID:      &workflowID,
		Resource:        resource,
		Action:          action,
		PrimaryEntityID: primaryEntityID,
		RelatedIDs:      relatedIDs,
	})
}

func (s *Service) publishLinkedWorkflowEvent(ctx context.Context, workflowID runtimeids.WorkflowID, resource serverapi.WorkflowProjectEventResource, action serverapi.WorkflowProjectEventAction, primaryEntityID string, relatedIDs ...string) {
	s.publishGlobalWorkflowEvent(ctx, workflowID, resource, action, primaryEntityID, relatedIDs...)
	links, err := s.store.ListWorkflowProjectLinks(ctx, workflowID)
	if err != nil {
		slog.Warn("list workflow project links for event failed", "workflow_id", workflowID.String(), "resource", resource, "action", action, "primary_entity_id", strings.TrimSpace(primaryEntityID), "related_ids", relatedIDs, "error", err)
		return
	}
	seen := map[string]bool{}
	for _, link := range links {
		projectID := strings.TrimSpace(link.ProjectID)
		if projectID == "" || seen[projectID] {
			continue
		}
		seen[projectID] = true
		s.publishProjectWorkflowEvent(ctx, projectID, workflowID, resource, action, primaryEntityID, relatedIDs...)
	}
}

func (s *Service) UpdateWorkflow(ctx context.Context, req serverapi.WorkflowUpdateRequest) (serverapi.WorkflowGetResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowGetResponse{}, err
	}
	if _, err := s.store.RunWorkflowGraphSaveOperation(ctx, req.WorkflowID, func(ctx context.Context) (workflowstore.WorkflowGraphSaveResult, error) {
		return workflowstore.WorkflowGraphSaveResult{}, s.store.UpdateWorkflowInfo(ctx, req.WorkflowID, req.Name, req.Description)
	}); err != nil {
		return serverapi.WorkflowGetResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflow, serverapi.WorkflowProjectEventActionUpdated, req.WorkflowID.String())
	return s.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: req.WorkflowID})
}

func (s *Service) ListWorkflows(ctx context.Context, req serverapi.WorkflowListRequest) (serverapi.WorkflowListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowListResponse{}, err
	}
	window, err := serverapi.ResolveWorkflowOffsetWindow(req.Offset, req.Limit)
	if err != nil {
		return serverapi.WorkflowListResponse{}, err
	}
	var workflowID *runtimeids.WorkflowID
	if req.WorkflowID != nil {
		workflowID = req.WorkflowID
	}
	rows, err := s.store.ListWorkflows(ctx, workflowstore.ListWorkflowsRequest{
		Offset:     window.Offset,
		Limit:      window.Limit,
		Query:      req.Query,
		ProjectID:  req.ProjectID,
		WorkflowID: workflowID,
	})
	if err != nil {
		return serverapi.WorkflowListResponse{}, err
	}
	out := make([]serverapi.WorkflowRecord, 0, len(rows.Workflows))
	for _, row := range rows.Workflows {
		out = append(out, workflowRecord(row))
	}
	return serverapi.WorkflowListResponse{Workflows: out, ProjectID: rows.ProjectID, NextOffset: rows.NextOffset}, nil
}

func (s *Service) GetWorkflow(ctx context.Context, req serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowGetResponse{}, err
	}
	def, _, err := s.readModels.Definitions.GetDefinition(ctx, req.WorkflowID)
	if err != nil {
		return serverapi.WorkflowGetResponse{}, err
	}
	return serverapi.WorkflowGetResponse{Definition: def}, nil
}

func (s *Service) LinkWorkflowToProject(ctx context.Context, request serverapi.WorkflowLinkProjectRequest) (serverapi.WorkflowLinkProjectResponse, error) {
	if err := request.Validate(); err != nil {
		return serverapi.WorkflowLinkProjectResponse{}, err
	}
	link, err := s.store.LinkWorkflowWithDefaultPolicy(ctx, request.ProjectID, request.WorkflowID, workflowStoreDefaultPolicy(request.DefaultPolicy))
	if err != nil {
		return serverapi.WorkflowLinkProjectResponse{}, err
	}
	s.publishProjectWorkflowEvent(ctx, request.ProjectID, request.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflowLink, serverapi.WorkflowProjectEventActionLinked, link.ID)
	return serverapi.WorkflowLinkProjectResponse{Link: projectWorkflowLink(link)}, nil
}

func (s *Service) ListProjectWorkflowLinks(ctx context.Context, req serverapi.WorkflowListProjectLinksRequest) (serverapi.WorkflowListProjectLinksResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowListProjectLinksResponse{}, err
	}
	links, err := s.store.ListProjectWorkflowLinks(ctx, req.ProjectID)
	if err != nil {
		return serverapi.WorkflowListProjectLinksResponse{}, err
	}
	out := make([]serverapi.ProjectWorkflowLink, 0, len(links))
	for _, link := range links {
		out = append(out, projectWorkflowLink(link))
	}
	return serverapi.WorkflowListProjectLinksResponse{Links: out}, nil
}

func (s *Service) SetDefaultProjectWorkflowLink(ctx context.Context, req serverapi.WorkflowSetDefaultProjectLinkRequest) (serverapi.WorkflowSetDefaultProjectLinkResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowSetDefaultProjectLinkResponse{}, err
	}
	link, err := s.store.SetDefaultProjectWorkflowLink(ctx, req.ProjectID, req.WorkflowID)
	if err != nil {
		return serverapi.WorkflowSetDefaultProjectLinkResponse{}, err
	}
	s.publishProjectWorkflowEvent(ctx, req.ProjectID, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflowLink, serverapi.WorkflowProjectEventActionDefaultChanged, link.ID)
	return serverapi.WorkflowSetDefaultProjectLinkResponse{Link: projectWorkflowLink(link)}, nil
}

func (s *Service) UnlinkWorkflowFromProject(ctx context.Context, req serverapi.WorkflowUnlinkProjectRequest) (serverapi.WorkflowUnlinkProjectResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowUnlinkProjectResponse{}, err
	}
	result, err := s.store.UnlinkProjectWorkflow(ctx, req.LinkID, req.ReplacementDefaultLinkID)
	resp := workflowUnlinkProjectResponse(result)
	if err != nil {
		return resp, err
	}
	if result.Unlinked {
		s.publishProjectWorkflowEvent(ctx, result.ProjectID, result.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflowLink, serverapi.WorkflowProjectEventActionUnlinked, req.LinkID)
	}
	return resp, nil
}

func (s *Service) PreviewWorkflowDelete(ctx context.Context, req serverapi.WorkflowDeletePreviewRequest) (serverapi.WorkflowDeletePreviewResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowDeletePreviewResponse{}, err
	}
	impact, err := s.store.PreviewWorkflowDelete(ctx, req.WorkflowID)
	if err != nil {
		return serverapi.WorkflowDeletePreviewResponse{}, err
	}
	return serverapi.WorkflowDeletePreviewResponse{Impact: workflowDeleteImpact(impact)}, nil
}

func (s *Service) DeleteWorkflow(ctx context.Context, req serverapi.WorkflowDeleteRequest) (serverapi.WorkflowDeleteResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowDeleteResponse{}, err
	}
	taskIDs, err := s.store.ListWorkflowTaskIDs(ctx, req.WorkflowID)
	if err != nil {
		return serverapi.WorkflowDeleteResponse{}, err
	}
	var response serverapi.WorkflowDeleteResponse
	err = s.taskMutations.RunMany(ctx, taskIDs, func(ctx context.Context) error {
		if err := s.ensureWorkflowTasksQuiescent(ctx, req.WorkflowID); err != nil {
			return err
		}
		var err error
		response, err = s.deleteWorkflow(ctx, req)
		return err
	})
	return response, err
}

func (s *Service) ensureWorkflowTasksQuiescent(ctx context.Context, workflowID runtimeids.WorkflowID) error {
	if s == nil || s.currentNodeExecution == nil {
		return errors.New("current node workflow execution is required")
	}
	taskIDs, err := s.store.ListWorkflowTaskIDs(ctx, workflowID)
	if err != nil {
		return err
	}
	for _, taskID := range taskIDs {
		if err := s.currentNodeExecution.EnsureTaskQuiescent(taskID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) deleteWorkflow(ctx context.Context, req serverapi.WorkflowDeleteRequest) (serverapi.WorkflowDeleteResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowDeleteResponse{}, err
	}
	links, err := s.store.ListWorkflowProjectLinks(ctx, req.WorkflowID)
	if err != nil {
		return serverapi.WorkflowDeleteResponse{}, err
	}
	result, err := s.store.DeleteWorkflow(ctx, workflowstore.WorkflowDeleteRequest{
		WorkflowID:           req.WorkflowID,
		Confirmed:            req.Confirmed,
		ExpectedVersion:      req.ExpectedVersion,
		ExpectedProjectCount: req.ExpectedProjectCount,
		ExpectedLinkCount:    req.ExpectedLinkCount,
		ExpectedTaskCount:    req.ExpectedTaskCount,
		CleanupArtifacts:     req.CleanupArtifacts,
	})
	if err != nil {
		return serverapi.WorkflowDeleteResponse{}, err
	}
	resp := workflowDeleteResponse(result)
	if !resp.Deleted {
		return resp, nil
	}
	s.finalizeWorkflowAttentionResolution(ctx, result)
	s.publishGlobalWorkflowEvent(ctx, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflow, serverapi.WorkflowProjectEventActionDeleted, req.WorkflowID.String())
	seen := map[string]bool{}
	for _, link := range links {
		projectID := strings.TrimSpace(link.ProjectID)
		if projectID == "" || seen[projectID] {
			continue
		}
		seen[projectID] = true
		s.publishProjectWorkflowEvent(ctx, projectID, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflow, serverapi.WorkflowProjectEventActionDeleted, req.WorkflowID.String())
	}
	return resp, nil
}

func (s *Service) ValidateWorkflow(ctx context.Context, req serverapi.WorkflowValidateRequest) (serverapi.WorkflowValidateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowValidateResponse{}, err
	}
	def, _, err := s.store.GetDefinition(ctx, req.WorkflowID)
	if err != nil {
		return serverapi.WorkflowValidateResponse{}, err
	}
	mode := workflow.ValidationContext(req.Mode)
	if mode == "" {
		mode = workflow.ValidationContextDraft
	}
	result := workflowscript.EvaluateDefinition(def, []workflow.ValidationContext{mode}, s.roleResolver, nil)[mode]
	resp := workflowValidationResponse(def.ID, result)
	return resp, nil
}

func (s *Service) ValidateWorkflowScriptPath(ctx context.Context, req serverapi.WorkflowScriptPathValidateRequest) (serverapi.WorkflowValidateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowValidateResponse{}, err
	}
	def, _, err := s.store.GetDefinition(ctx, req.WorkflowID)
	if err != nil {
		return serverapi.WorkflowValidateResponse{}, err
	}
	diagnostics := workflowscript.Validate(workflowscript.ValidationRequest{RawPath: req.ScriptPath})
	errors := make([]serverapi.WorkflowValidationError, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		errors = append(errors, scriptPathValidationError(def.ID, workflow.NodeID(req.NodeID), diagnostic))
	}
	return serverapi.WorkflowValidateResponse{
		Valid:  workflowValidationErrorsValid(errors),
		Errors: errors,
	}, nil
}

func (s *Service) ValidateWorkflowGraphDraft(ctx context.Context, req serverapi.WorkflowGraphValidateDraftRequest) (serverapi.WorkflowGraphValidateDraftResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowGraphValidateDraftResponse{}, err
	}
	def, err := s.workflowGraphDraftDefinition(ctx, req.WorkflowID, req.Metadata, req.Graph)
	if err != nil {
		return serverapi.WorkflowGraphValidateDraftResponse{}, err
	}
	return serverapi.WorkflowGraphValidateDraftResponse{
		Results:       s.workflowGraphValidationResultsForDefinition(def, req.Modes),
		DerivedWiring: workflowview.DerivedWiring(def, s.roleResolver),
	}, nil
}

func (s *Service) DeriveWorkflowGraphWiring(ctx context.Context, req serverapi.WorkflowGraphDeriveWiringRequest) (serverapi.WorkflowGraphDeriveWiringResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowGraphDeriveWiringResponse{}, err
	}
	def, err := s.workflowGraphDraftDefinition(ctx, req.WorkflowID, nil, req.Graph)
	if err != nil {
		return serverapi.WorkflowGraphDeriveWiringResponse{}, err
	}
	return serverapi.WorkflowGraphDeriveWiringResponse{
		DerivedWiring: workflowview.DerivedWiring(def, s.roleResolver),
	}, nil
}

func (s *Service) PreviewWorkflowGraphSave(ctx context.Context, req serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error) {
	if err := req.ValidateRPC(); err != nil {
		return serverapi.WorkflowGraphSavePreviewResponse{}, err
	}
	result, err := s.store.RunWorkflowGraphSaveOperation(ctx, req.WorkflowID, func(ctx context.Context) (workflowstore.WorkflowGraphSaveResult, error) {
		currentVersion, err := s.workflowGraphSaveCurrentVersion(ctx, req.WorkflowID)
		if err != nil {
			return workflowstore.WorkflowGraphSaveResult{}, err
		}
		if currentVersion != req.ExpectedVersion {
			return workflowstore.WorkflowGraphSaveVersionChangedResult(currentVersion), nil
		}
		if err := req.Validate(); err != nil {
			return workflowstore.WorkflowGraphSaveResult{}, err
		}
		storeRequest, err := workflowGraphStoreSaveRequest(req.WorkflowID, req.ExpectedVersion, req.Metadata, req.Graph, nil)
		if err != nil {
			return workflowstore.WorkflowGraphSaveResult{}, err
		}
		return s.store.PreviewWorkflowGraphSave(ctx, storeRequest)
	})
	if err != nil {
		return serverapi.WorkflowGraphSavePreviewResponse{}, workflowGraphSaveError(err)
	}
	resp := workflowGraphSavePreviewResponse(result, workflowGraphSaveValidationResponses(result))
	if err := resp.Validate(); err != nil {
		return serverapi.WorkflowGraphSavePreviewResponse{}, fmt.Errorf("project workflow graph save preview response: %w", err)
	}
	return resp, nil
}

func (s *Service) SaveWorkflowGraph(ctx context.Context, req serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error) {
	if err := req.ValidateRPC(); err != nil {
		return serverapi.WorkflowGraphSaveResponse{}, err
	}
	result, err := s.store.RunWorkflowGraphSaveOperation(ctx, req.WorkflowID, func(ctx context.Context) (workflowstore.WorkflowGraphSaveResult, error) {
		currentVersion, err := s.workflowGraphSaveCurrentVersion(ctx, req.WorkflowID)
		if err != nil {
			return workflowstore.WorkflowGraphSaveResult{}, err
		}
		if currentVersion != req.ExpectedVersion {
			return workflowstore.WorkflowGraphSaveVersionChangedResult(currentVersion), nil
		}
		if err := req.Validate(); err != nil {
			return workflowstore.WorkflowGraphSaveResult{}, err
		}
		storeRequest, err := workflowGraphStoreSaveRequest(req.WorkflowID, req.ExpectedVersion, req.Metadata, req.Graph, req.Confirmation)
		if err != nil {
			return workflowstore.WorkflowGraphSaveResult{}, err
		}
		return s.store.SaveWorkflowGraph(ctx, storeRequest)
	})
	if err != nil {
		return serverapi.WorkflowGraphSaveResponse{}, workflowGraphSaveError(err)
	}
	resp := workflowGraphSaveResponse(result, workflowGraphSaveValidationResponses(result))
	if result.Saved {
		definition, _ := workflowview.ProjectDefinition(result.Definition, result.Record, s.roleResolver)
		resp.Definition = &definition
		resp.CurrentVersion = result.Record.Version
		if result.Changed {
			s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflow, serverapi.WorkflowProjectEventActionGraphSaved, req.WorkflowID.String())
		}
	}
	if err := resp.Validate(); err != nil {
		return serverapi.WorkflowGraphSaveResponse{}, fmt.Errorf("project workflow graph save response: %w", err)
	}
	return resp, nil
}

func (s *Service) workflowGraphSaveCurrentVersion(ctx context.Context, workflowID runtimeids.WorkflowID) (int64, error) {
	_, record, err := s.store.GetDefinition(ctx, workflowID)
	if err != nil {
		return 0, err
	}
	return record.Version, nil
}

func workflowGraphSaveError(err error) error {
	var ownershipErr workflowstore.WorkflowGraphIdentityOwnershipError
	if errors.As(err, &ownershipErr) {
		return serverapi.WorkflowRequestValidationError{
			Code:    serverapi.WorkflowRequestErrorInvalidValue,
			Field:   ownershipErr.Field,
			Message: ownershipErr.Error(),
		}
	}
	return err
}

func (s *Service) CreateWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCreateRequest) (serverapi.WorkflowTaskCreateResponse, error) {
	if err := req.ValidateRPC(); err != nil {
		return serverapi.WorkflowTaskCreateResponse{}, err
	}
	var workflowID *runtimeids.WorkflowID
	if req.WorkflowID != nil {
		workflowID = req.WorkflowID
	}
	taskRequest := workflowstore.CreateTaskRequest{
		ProjectID:         req.ProjectID,
		WorkflowID:        workflowID,
		Title:             req.Title,
		Body:              req.Body,
		SourceURL:         req.SourceURL,
		SourceWorkspaceID: req.SourceWorkspaceID,
		LabelIDs:          req.LabelIDs,
	}
	for _, requestedIntent := range req.DependencyIntents {
		intent := workflow.TaskDependencyCreateIntent{
			RelatedTaskID: workflow.TaskID(requestedIntent.RelatedTaskID),
		}
		switch requestedIntent.NewTaskRole {
		case serverapi.WorkflowTaskDependencyRoleBlocker:
			intent.NewTaskRole = workflow.TaskDependencyRoleBlocker
		case serverapi.WorkflowTaskDependencyRoleBlocked:
			intent.NewTaskRole = workflow.TaskDependencyRoleBlocked
		}
		taskRequest.DependencyIntents = append(taskRequest.DependencyIntents, intent)
	}
	task, err := s.store.CreateTask(ctx, taskRequest)
	if err != nil {
		var policyErr workflow.TaskDependencyPolicyError
		if errors.As(err, &policyErr) {
			return serverapi.WorkflowTaskCreateResponse{}, workflowTaskDependencyError(err)
		}
		return serverapi.WorkflowTaskCreateResponse{}, workflowTaskCreateError(err, req.ProjectID)
	}
	s.publishProjectWorkflowEvent(ctx, task.ProjectID, task.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionCreated, string(task.ID))
	if len(req.DependencyIntents) > 0 {
		relatedIDs := make([]string, 0, len(req.DependencyIntents))
		for _, intent := range req.DependencyIntents {
			relatedIDs = append(relatedIDs, intent.RelatedTaskID)
		}
		s.publishProjectWorkflowEvent(ctx, task.ProjectID, task.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionDependenciesChanged, string(task.ID), relatedIDs...)
	}
	detail, err := s.readModels.TaskDetail.GetTask(ctx, string(task.ID))
	if err != nil {
		return serverapi.WorkflowTaskCreateResponse{}, err
	}
	return serverapi.WorkflowTaskCreateResponse{Task: detail.Summary}, nil
}

func (s *Service) AddWorkflowTaskDependency(ctx context.Context, req serverapi.WorkflowTaskDependencyAddRequest) (serverapi.WorkflowTaskDependencyAddResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskDependencyAddResponse{}, err
	}
	result, err := s.store.AddTaskDependency(ctx, workflowstore.TaskDependencyAddRequest{
		BlockerTaskID: workflow.TaskID(req.BlockerTaskID),
		BlockedTaskID: workflow.TaskID(req.BlockedTaskID),
	})
	if err != nil {
		return serverapi.WorkflowTaskDependencyAddResponse{}, workflowTaskDependencyError(err)
	}
	if result.Outcome == workflowstore.TaskDependencyAdded {
		s.publishProjectWorkflowEvent(ctx, result.ProjectID, result.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionDependenciesChanged, string(result.BlockerTaskID), string(result.BlockedTaskID))
	}
	return serverapi.WorkflowTaskDependencyAddResponse{
		Outcome:        serverapi.WorkflowTaskDependencyOutcome(result.Outcome),
		BlockerTaskID:  string(result.BlockerTaskID),
		BlockerShortID: result.BlockerShortID,
		BlockedTaskID:  string(result.BlockedTaskID),
		BlockedShortID: result.BlockedShortID,
	}, nil
}

func (s *Service) RemoveWorkflowTaskDependency(ctx context.Context, req serverapi.WorkflowTaskDependencyRemoveRequest) (serverapi.WorkflowTaskDependencyRemoveResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskDependencyRemoveResponse{}, err
	}
	result, err := s.store.RemoveTaskDependency(ctx, workflowstore.TaskDependencyRemoveRequest{
		BlockerTaskID: workflow.TaskID(req.BlockerTaskID),
		BlockedTaskID: workflow.TaskID(req.BlockedTaskID),
	})
	if err != nil {
		return serverapi.WorkflowTaskDependencyRemoveResponse{}, workflowTaskDependencyError(err)
	}
	if result.Outcome == workflowstore.TaskDependencyRemoved {
		s.publishProjectWorkflowEvent(ctx, result.ProjectID, result.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionDependenciesChanged, string(result.BlockerTaskID), string(result.BlockedTaskID))
	}
	return serverapi.WorkflowTaskDependencyRemoveResponse{
		Outcome:        serverapi.WorkflowTaskDependencyOutcome(result.Outcome),
		BlockerTaskID:  string(result.BlockerTaskID),
		BlockerShortID: result.BlockerShortID,
		BlockedTaskID:  string(result.BlockedTaskID),
		BlockedShortID: result.BlockedShortID,
	}, nil
}

func (s *Service) ListWorkflowTaskDependencies(ctx context.Context, req serverapi.WorkflowTaskDependencyListRequest) (serverapi.WorkflowTaskDependencyListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskDependencyListResponse{}, err
	}
	return s.readModels.TaskDependencies.ListTaskDependencies(ctx, req.TaskID, req.Direction)
}

func workflowTaskDependencyError(err error) error {
	var policyErr workflow.TaskDependencyPolicyError
	if !errors.As(err, &policyErr) {
		return err
	}
	apiErr := &serverapi.WorkflowTaskDependencyError{
		Reason:        serverapi.WorkflowTaskDependencyErrorReason(policyErr.Reason),
		BlockerTaskID: string(policyErr.BlockerTaskID),
		BlockedTaskID: string(policyErr.BlockedTaskID),
	}
	if policyErr.MissingTaskID != nil {
		value := string(*policyErr.MissingTaskID)
		apiErr.MissingTaskID = &value
	}
	if policyErr.CurrentCount != nil {
		value := int(*policyErr.CurrentCount)
		apiErr.CurrentCount = &value
	}
	if policyErr.Limit != nil {
		value := int(*policyErr.Limit)
		apiErr.Limit = &value
	}
	return apiErr
}

func workflowTaskCreateError(err error, projectID string) error {
	var selectionErr workflowstore.TaskWorkflowSelectionError
	if errors.As(err, &selectionErr) {
		var reason serverapi.WorkflowTaskCreateSelectionReason
		switch selectionErr.Reason {
		case workflowstore.TaskWorkflowSelectionNoLinkedWorkflows:
			reason = serverapi.WorkflowTaskCreateSelectionReasonNoLinkedWorkflows
		case workflowstore.TaskWorkflowSelectionWorkflowNotLinked:
			reason = serverapi.WorkflowTaskCreateSelectionReasonWorkflowNotLinked
		case workflowstore.TaskWorkflowSelectionAmbiguousWithoutDefault:
			reason = serverapi.WorkflowTaskCreateSelectionReasonAmbiguousWithoutDefault
		default:
			return err
		}
		workflowID := selectionErr.WorkflowID
		return &serverapi.WorkflowTaskCreateSelectionError{
			Reason:     reason,
			ProjectID:  selectionErr.ProjectID,
			WorkflowID: workflowID,
		}
	}
	var conflictErr workflowstore.TaskCreateConflictError
	if errors.As(err, &conflictErr) && conflictErr.Reason == workflowstore.TaskCreateConflictSerialization {
		return &serverapi.WorkflowTaskCreateConflictError{
			Reason: serverapi.WorkflowTaskCreateConflictReasonSerialization,
		}
	}
	return workflowLabelError(err, workflowLabelErrorScope{projectID: &projectID})
}

func workflowTaskStartError(err error) error {
	var conflict workflowstore.TaskStartConflictError
	if !errors.As(err, &conflict) {
		return err
	}
	switch conflict.Reason {
	case workflowstore.TaskStartConflictAlreadyStarted:
		return &serverapi.WorkflowTaskStartConflictError{
			TaskID: string(conflict.TaskID),
			Reason: serverapi.WorkflowTaskStartConflictAlreadyStarted,
		}
	default:
		return err
	}
}

func (s *Service) UpdateWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskUpdateRequest) (serverapi.WorkflowTaskUpdateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskUpdateResponse{}, err
	}
	task, err := s.store.UpdateTask(ctx, workflowstore.UpdateTaskRequest{TaskID: workflow.TaskID(req.TaskID), Title: req.Title, Body: req.Body, SourceWorkspaceID: req.SourceWorkspaceID})
	if err != nil {
		return serverapi.WorkflowTaskUpdateResponse{}, err
	}
	s.publishProjectWorkflowEvent(ctx, task.ProjectID, task.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionUpdated, string(task.ID))
	detail, err := s.readModels.TaskDetail.GetTask(ctx, string(task.ID))
	if err != nil {
		return serverapi.WorkflowTaskUpdateResponse{}, err
	}
	return serverapi.WorkflowTaskUpdateResponse{Task: detail.Summary}, nil
}

func (s *Service) StartWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskStartResponse, error) {
	return s.startWorkflowTask(ctx, req)
}

func (s *Service) startWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskStartResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskStartResponse{}, err
	}
	if err := s.authorizeWorkflowTaskMutation(ctx, workflow.TaskID(req.TaskID), req.InvokingSessionID); err != nil {
		return serverapi.WorkflowTaskStartResponse{}, err
	}
	taskID := workflow.TaskID(req.TaskID)
	preflight, err := workflowexecution.RunTaskMutation(ctx, s.taskMutations, taskID, func(ctx context.Context) (initiatingActionPreflight, error) {
		if err := s.currentNodeExecution.EnsureTaskQuiescent(taskID); err != nil {
			return initiatingActionPreflight{}, err
		}
		if err := s.store.ValidateTaskStart(ctx, taskID); err != nil {
			return initiatingActionPreflight{}, err
		}
		if req.ProceedDespiteDependencies {
			target, err := s.preflightInitiatingActionTarget(ctx, taskID, req.ExecutionTarget, req.BranchName)
			return initiatingActionPreflight{target: target}, err
		}
		count, err := s.readModels.TaskDependencies.CountUnsatisfiedBlockers(ctx, req.TaskID)
		if err != nil {
			return initiatingActionPreflight{}, err
		}
		if count > 0 {
			return initiatingActionPreflight{unsatisfiedDependencyCount: count}, nil
		}
		target, err := s.preflightInitiatingActionTarget(ctx, taskID, req.ExecutionTarget, req.BranchName)
		return initiatingActionPreflight{target: target}, err
	})
	if err != nil {
		return serverapi.WorkflowTaskStartResponse{}, workflowTaskStartError(err)
	}
	if preflight.unsatisfiedDependencyCount > 0 {
		count := preflight.unsatisfiedDependencyCount
		return serverapi.WorkflowTaskStartResponse{
			Outcome:                    serverapi.WorkflowTaskActionOutcomeDependencyConfirmationRequired,
			UnsatisfiedDependencyCount: &count,
		}, nil
	}
	target := preflight.target
	if target.context.Task.ExecutionTarget == nil && !target.explicit &&
		target.context.Policy.Mode == workflow.ExecutionTargetModeAskOnFirstExecution {
		return serverapi.WorkflowTaskStartResponse{
			Outcome: serverapi.WorkflowTaskActionOutcomeSelectionRequired,
			SelectionRequired: &serverapi.WorkflowExecutionTargetSelectionRequirement{
				Reason: serverapi.WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection,
			},
		}, nil
	}
	observation, err := newTaskSetupObservation(req.SetupOperationID, target.selection, s.setupEvents)
	if err != nil {
		return serverapi.WorkflowTaskStartResponse{}, err
	}
	target.unavailable = initiatingActionTargetInterrupt
	preparation := s.initiatingActionPreparation(
		workflow.TaskID(req.TaskID),
		req.SetupOperationID,
		target,
		observation,
		func(preparationCtx context.Context) (preparedInitiatingActionTarget, error) {
			decision, preparationErr := s.initiatingActionTarget(
				preparationCtx,
				workflow.TaskID(req.TaskID),
				&req.SetupOperationID,
				target,
			)
			if decision.prepared == nil {
				return preparedInitiatingActionTarget{}, preparationErr
			}
			return *decision.prepared, preparationErr
		},
	)
	started, err := s.currentNodeExecution.StartTask(
		ctx,
		workflow.TaskID(req.TaskID),
		preparation,
		observation.finalize,
	)
	if err != nil {
		return serverapi.WorkflowTaskStartResponse{}, workflowTaskStartError(err)
	}
	if len(started.Mutation.Created) != 1 {
		return serverapi.WorkflowTaskStartResponse{}, errors.New("task start did not create exactly one current node")
	}
	if detail, detailErr := s.readModels.TaskDetail.GetTask(ctx, req.TaskID); detailErr == nil {
		s.publishProjectWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionStarted, req.TaskID)
	}
	return serverapi.WorkflowTaskStartResponse{
		Outcome: serverapi.WorkflowTaskActionOutcomeApplied,
		Applied: &serverapi.WorkflowTaskStartApplied{
			CurrentNodes: workflowview.ProjectCurrentNodes(started.Mutation.Created),
		},
	}, nil
}

func (s *Service) initiatingActionPreparation(
	taskID workflow.TaskID,
	setupOperationID serverapi.WorktreeSetupOperationID,
	target initiatingActionTargetPreflight,
	observation *taskSetupObservation,
	materialize func(context.Context) (preparedInitiatingActionTarget, error),
) workflowexecution.TaskStartPreparation {
	var preparedTarget preparedInitiatingActionTarget
	return workflowexecution.TaskStartPreparation{
		Prepare: func(ctx context.Context) error {
			var preparationErr error
			preparedTarget, preparationErr = materialize(ctx)
			if preparationErr == nil && preparedTarget.candidate == nil {
				preparationErr = errors.New("Task target preparation produced no lock candidate")
			}
			if preparationErr != nil {
				preparationErr = taskPreparationError(
					setupOperationID, target, preparedTarget.setupResult, preparedTarget.retainedWorktree,
					preparedTarget.retainedPreviousWorktree, preparationErr,
				)
			}
			observation.record(preparedTarget, preparationErr)
			return preparationErr
		},
		Commit: func(ctx context.Context) error {
			lockErr := s.store.LockTaskExecutionTarget(ctx, taskID, preparedTarget.candidate)
			lockErr = taskPreparationError(
				setupOperationID, target, preparedTarget.setupResult, preparedTarget.retainedWorktree,
				preparedTarget.retainedPreviousWorktree, lockErr,
			)
			observation.record(preparedTarget, lockErr)
			return lockErr
		},
	}
}

func coordinateInitiatingAction[T any](ctx context.Context, service *Service, req initiatingActionRequest, apply func(*workflowstore.ExecutionTargetCandidate) (*T, error)) (initiatingActionResult[T], error) {
	var candidate *workflowstore.ExecutionTargetCandidate
	var retainedPreviousWorktree *serverapi.RetainedPreviousWorktree
	if req.requiresExecutionTarget {
		targetDecision, err := service.initiatingActionTarget(ctx, req.taskID, req.setupOperationID, req.targetPreflight)
		if err != nil {
			if targetDecision.prepared != nil {
				retainedPreviousWorktree = targetDecision.prepared.retainedPreviousWorktree
			}
			return initiatingActionResult[T]{
				retainedPreviousWorktree: retainedPreviousWorktree,
			}, err
		}
		if targetDecision.selectionRequired != nil {
			return initiatingActionResult[T]{selectionRequired: targetDecision.selectionRequired}, nil
		}
		if targetDecision.prepared != nil {
			candidate = targetDecision.prepared.candidate
			retainedPreviousWorktree = targetDecision.prepared.retainedPreviousWorktree
		}
	}
	if req.afterTargetResolution != nil {
		if err := req.afterTargetResolution(); err != nil {
			return initiatingActionResult[T]{retainedPreviousWorktree: retainedPreviousWorktree}, err
		}
	}
	applied, err := apply(candidate)
	if err != nil {
		if applied != nil {
			return initiatingActionResult[T]{applied: applied, retainedPreviousWorktree: retainedPreviousWorktree}, err
		}
		return initiatingActionResult[T]{retainedPreviousWorktree: retainedPreviousWorktree}, err
	}
	return initiatingActionResult[T]{applied: applied, retainedPreviousWorktree: retainedPreviousWorktree}, nil
}

func (s *Service) preflightInitiatingActionTarget(
	ctx context.Context,
	taskID workflow.TaskID,
	explicit *serverapi.WorkflowExecutionTargetSelection,
	requestedBranchName *string,
) (initiatingActionTargetPreflight, error) {
	targetContext, err := s.store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		return initiatingActionTargetPreflight{}, err
	}
	if targetContext.Task.ExecutionTarget != nil {
		if explicit != nil {
			return initiatingActionTargetPreflight{}, workflowstore.ErrExecutionTargetAlreadyLocked
		}
		selection := workflow.ExecutionTargetSelection{Mode: targetContext.Task.ExecutionTarget.Mode}
		if selection.Mode == workflow.ExecutionTargetModeCustomRef {
			selection.CustomRef = targetContext.Task.ExecutionTarget.RequestedRef
		}
		if targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone {
			if s.executionTargets == nil {
				return initiatingActionTargetPreflight{}, errExecutionTargetInfrastructureRequired
			}
		}
		branchAssertion, pendingBranchReplaced, err := s.preflightInitialTaskBranch(ctx, targetContext, selection, requestedBranchName)
		if err != nil {
			return initiatingActionTargetPreflight{}, err
		}
		return initiatingActionTargetPreflight{
			context:                targetContext,
			selection:              selection,
			initialBranchAssertion: branchAssertion,
			pendingBranchReplaced:  pendingBranchReplaced,
		}, nil
	}
	selection := workflow.ExecutionTargetSelection{
		Mode:      targetContext.Policy.Mode,
		CustomRef: targetContext.Policy.CustomRef,
	}
	if explicit == nil && targetContext.Policy.Mode == workflow.ExecutionTargetModeAskOnFirstExecution {
		return initiatingActionTargetPreflight{
			context:   targetContext,
			selection: selection,
		}, nil
	}
	if explicit != nil {
		selection = workflow.ExecutionTargetSelection{
			Mode:      workflow.ExecutionTargetMode(explicit.Mode),
			CustomRef: explicit.CustomRef,
		}
	}
	if selection.Mode != workflow.ExecutionTargetModeNone || targetContext.Task.ManagedWorktreeID != nil {
		if s.executionTargets == nil {
			return initiatingActionTargetPreflight{}, errExecutionTargetInfrastructureRequired
		}
	}
	branchAssertion, pendingBranchReplaced, err := s.preflightInitialTaskBranch(ctx, targetContext, selection, requestedBranchName)
	if err != nil {
		return initiatingActionTargetPreflight{}, err
	}
	return initiatingActionTargetPreflight{
		context:                targetContext,
		selection:              selection,
		explicit:               explicit != nil,
		initialBranchAssertion: branchAssertion,
		pendingBranchReplaced:  pendingBranchReplaced,
	}, nil
}

func (s *Service) preflightInitialTaskBranch(
	ctx context.Context,
	targetContext workflowstore.TaskExecutionTargetContext,
	selection workflow.ExecutionTargetSelection,
	requestedBranchName *string,
) (*string, bool, error) {
	if selection.Mode == workflow.ExecutionTargetModeNone {
		if requestedBranchName != nil {
			return nil, false, &serverapi.WorkflowTaskInitialBranchError{
				Reason:     serverapi.WorkflowTaskInitialBranchErrorReasonNoManagedTarget,
				BranchName: *requestedBranchName,
			}
		}
		return nil, false, nil
	}
	if targetContext.Task.ManagedWorktreeID != nil {
		if requestedBranchName != nil {
			if err := s.executionTargets.AssertInitialTaskBranch(ctx, InitialTaskBranchAssertionRequest{
				TaskID:     targetContext.Task.ID,
				BranchName: *requestedBranchName,
			}); err != nil {
				return nil, false, err
			}
		}
		return requestedBranchName, false, nil
	}
	if targetContext.Task.ExecutionTarget != nil {
		if requestedBranchName != nil {
			return nil, false, operationCannotCreateInitialWorktreeError(*requestedBranchName)
		}
		return nil, false, nil
	}
	branchName := requestedBranchName
	if branchName == nil {
		branchName = targetContext.Task.PendingInitialManagedBranchName
	}
	if branchName == nil {
		return nil, false, fmt.Errorf("task %q has no pending initial managed branch", targetContext.Task.ID)
	}
	if err := s.executionTargets.InspectProspectiveInitialTaskBranch(ctx, InitialTaskBranchInspectionRequest{
		SourceWorkspaceRoot: targetContext.SourceWorkspaceRoot,
		BranchName:          *branchName,
	}); err != nil {
		return nil, false, err
	}
	if requestedBranchName != nil && targetContext.Task.ExecutionTarget == nil {
		if err := s.store.ReplacePendingInitialManagedBranchName(ctx, targetContext.Task.ID, *requestedBranchName); err != nil {
			return nil, false, err
		}
		return requestedBranchName, true, nil
	}
	return requestedBranchName, false, nil
}

func operationCannotCreateInitialWorktreeError(branchName string) *serverapi.WorkflowTaskInitialBranchError {
	return &serverapi.WorkflowTaskInitialBranchError{
		Reason:     serverapi.WorkflowTaskInitialBranchErrorReasonOperationCannotCreateWorktree,
		BranchName: branchName,
	}
}

func (s *Service) initiatingActionTarget(ctx context.Context, taskID workflow.TaskID, setupOperationID *serverapi.WorktreeSetupOperationID, preflight initiatingActionTargetPreflight) (initiatingActionTargetDecision, error) {
	targetContext := preflight.context
	if targetContext.Task.ExecutionTarget != nil {
		if targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone {
			if err := s.executionTargets.RestoreExecutionTarget(ctx, ExecutionTargetRestoreRequest{
				TaskID:                 taskID,
				SetupOperationID:       setupOperationID,
				InitialBranchAssertion: preflight.initialBranchAssertion,
			}); err != nil {
				return initiatingActionTargetDecision{}, workflowLockedExecutionTargetError(err)
			}
		}
		return initiatingActionTargetDecision{}, nil
	}
	snapshot, selectionRequired, err := s.resolveInitiatingActionTarget(ctx, preflight)
	if err != nil || selectionRequired != nil {
		return initiatingActionTargetDecision{selectionRequired: selectionRequired}, err
	}
	prepared, err := s.materializeInitiatingActionTarget(ctx, taskID, setupOperationID, preflight, snapshot, worktreecontract.SetupRequirementRequired)
	return initiatingActionTargetDecision{prepared: &prepared}, err
}

func (s *Service) resolveInitiatingActionTarget(
	ctx context.Context,
	preflight initiatingActionTargetPreflight,
) (*workflowstore.ExecutionTargetSnapshot, *serverapi.WorkflowExecutionTargetSelectionRequirement, error) {
	targetContext := preflight.context
	if !preflight.explicit && targetContext.Policy.Mode == workflow.ExecutionTargetModeAskOnFirstExecution {
		return nil, &serverapi.WorkflowExecutionTargetSelectionRequirement{
			Reason: serverapi.WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection,
		}, nil
	}
	selection := preflight.selection
	if selection.Mode == workflow.ExecutionTargetModeNone {
		if targetContext.Task.ManagedWorktreeID == nil {
			return nil, nil, nil
		}
		return &workflowstore.ExecutionTargetSnapshot{
			Mode: selection.Mode, Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		}, nil, nil
	}
	snapshot, err := s.executionTargets.ResolveExecutionTarget(ctx, ExecutionTargetResolveRequest{
		SourceWorkspaceRoot: targetContext.SourceWorkspaceRoot,
		Selection:           selection,
	})
	if err != nil {
		if !preflight.explicit && preflight.unavailable == initiatingActionTargetRequestSelection {
			if requirement, ok := configuredTargetSelectionRequirement(selection, err); ok {
				return nil, requirement, nil
			}
		}
		if preflight.explicit && selection.Mode == workflow.ExecutionTargetModeCustomRef {
			if resolutionErr, ok := explicitExecutionTargetResolutionError(err); ok {
				return nil, nil, resolutionErr
			}
		}
		return nil, nil, err
	}
	if snapshot.Mode != selection.Mode {
		return nil, nil, errors.New("resolved execution target mode does not match selection")
	}
	if err := snapshot.Validate(); err != nil {
		return nil, nil, err
	}
	return &snapshot, nil, nil
}

func (s *Service) materializeInitiatingActionTarget(
	ctx context.Context,
	taskID workflow.TaskID,
	setupOperationID *serverapi.WorktreeSetupOperationID,
	preflight initiatingActionTargetPreflight,
	snapshot *workflowstore.ExecutionTargetSnapshot,
	setupRequirement worktreecontract.SetupRequirement,
) (preparedInitiatingActionTarget, error) {
	targetContext := preflight.context
	if snapshot != nil {
		materialization, materializationErr := s.executionTargets.MaterializeExecutionTarget(ctx, ExecutionTargetMaterializeRequest{
			TaskID:                 taskID,
			SetupOperationID:       setupOperationID,
			Snapshot:               *snapshot,
			SetupRequirement:       setupRequirement,
			InitialBranchAssertion: preflight.initialBranchAssertion,
		})
		prepared := preparedInitiatingActionTarget{
			retainedWorktree:         materialization.RetainedWorktree,
			retainedPreviousWorktree: materialization.RetainedPreviousWorktree,
			setupResult:              materialization.SetupResult,
		}
		if snapshot.Mode == workflow.ExecutionTargetModeNone {
			prepared.candidate = &workflowstore.ExecutionTargetCandidate{
				Snapshot: *snapshot,
				Root:     workflowstore.ExecutionRoot{SourceWorkspaceID: targetContext.SourceWorkspaceID, SourceWorkspaceRoot: targetContext.SourceWorkspaceRoot},
			}
			return prepared, materializationErr
		}
		if materialization.RetainedRoot == nil {
			if materializationErr == nil {
				return prepared, errors.New("execution target materialization returned no managed root")
			}
			return prepared, materializationErr
		}
		prepared.candidate = &workflowstore.ExecutionTargetCandidate{
			Snapshot: *snapshot,
			Root: workflowstore.ExecutionRoot{
				SourceWorkspaceID:   targetContext.SourceWorkspaceID,
				SourceWorkspaceRoot: targetContext.SourceWorkspaceRoot,
				Managed:             materialization.RetainedRoot,
			},
		}
		if err := prepared.candidate.Validate(); err != nil {
			return prepared, errors.Join(materializationErr, err)
		}
		return prepared, materializationErr
	}
	candidate := &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   targetContext.SourceWorkspaceID,
			SourceWorkspaceRoot: targetContext.SourceWorkspaceRoot,
		},
	}
	return preparedInitiatingActionTarget{candidate: candidate}, nil
}

func configuredTargetSelectionRequirement(selection workflow.ExecutionTargetSelection, err error) (*serverapi.WorkflowExecutionTargetSelectionRequirement, bool) {
	unavailable, ok := configuredExecutionTargetUnavailable(selection, err)
	if !ok {
		return nil, false
	}
	return configuredTargetSelectionRequirementFromUnavailable(*unavailable), true
}

func configuredExecutionTargetUnavailable(
	selection workflow.ExecutionTargetSelection,
	err error,
) (*workflow.ConfiguredExecutionTargetUnavailable, bool) {
	cause, ok := executionTargetUnavailableCause(err)
	if !ok {
		return nil, false
	}
	unavailable := &workflow.ConfiguredExecutionTargetUnavailable{
		Mode:  selection.Mode,
		Cause: cause,
	}
	if selection.Mode == workflow.ExecutionTargetModeCustomRef {
		requestedRef := *selection.CustomRef
		unavailable.RequestedRef = &requestedRef
	}
	return unavailable, true
}

func configuredTargetSelectionRequirementFromUnavailable(
	unavailable workflow.ConfiguredExecutionTargetUnavailable,
) *serverapi.WorkflowExecutionTargetSelectionRequirement {
	configured := &serverapi.WorkflowExecutionTargetConfiguredTarget{
		Mode: serverapi.WorkflowExecutionTargetMode(unavailable.Mode),
	}
	if unavailable.RequestedRef != nil {
		requestedRef := *unavailable.RequestedRef
		configured.RequestedRef = &requestedRef
	}
	return &serverapi.WorkflowExecutionTargetSelectionRequirement{
		Reason:           serverapi.WorkflowExecutionTargetSelectionReasonConfiguredTargetUnavailable,
		ConfiguredTarget: configured,
		UnavailableCause: serverapi.WorkflowExecutionTargetUnavailableCause(unavailable.Cause),
	}
}

const configuredTargetPreparationFailureCode = "workflow_configured_execution_target_unavailable"

func configuredTargetPreparationError(preflight initiatingActionTargetPreflight, err error) error {
	if err == nil || preflight.explicit {
		return err
	}
	unavailable, ok := configuredExecutionTargetUnavailable(preflight.selection, err)
	if !ok {
		return err
	}
	requirement := configuredTargetSelectionRequirementFromUnavailable(*unavailable)
	fields := map[string]string{
		"error": err.Error(),
		"mode":  string(requirement.ConfiguredTarget.Mode),
		"cause": string(requirement.UnavailableCause),
	}
	if requirement.ConfiguredTarget.RequestedRef != nil {
		fields["requested_ref"] = *requirement.ConfiguredTarget.RequestedRef
	}
	return workflowexecution.NewTaskStartPreparationError(err, workflow.CurrentNodeInterruptionDetail{
		Code:                                 configuredTargetPreparationFailureCode,
		Fields:                               fields,
		ConfiguredExecutionTargetUnavailable: unavailable,
	})
}

func configuredTargetResumeSelection(nodes []workflow.CurrentNode) (*serverapi.WorkflowExecutionTargetSelectionRequirement, error) {
	for _, node := range nodes {
		if node.Scheduling == nil || node.Scheduling.Interruption == nil {
			continue
		}
		unavailable := node.Scheduling.Interruption.Detail.ConfiguredExecutionTargetUnavailable
		if unavailable == nil {
			continue
		}
		if err := unavailable.Validate(); err != nil {
			return nil, fmt.Errorf("invalid configured execution target interruption: %w", err)
		}
		return configuredTargetSelectionRequirementFromUnavailable(*unavailable), nil
	}
	return nil, nil
}

func resumeSetupRequirement(nodes []workflow.CurrentNode, selection workflow.ExecutionTargetSelection) (worktreecontract.SetupRequirement, error) {
	for _, node := range nodes {
		if node.Scheduling == nil || node.Scheduling.Interruption == nil {
			continue
		}
		recovery := node.Scheduling.Interruption.Detail.SetupRecovery
		if recovery == nil {
			continue
		}
		if err := recovery.Validate(); err != nil {
			return worktreecontract.SetupRequirementRequired, fmt.Errorf("invalid setup recovery interruption: %w", err)
		}
		if recovery.ExecutionTarget.Equal(selection) {
			return recovery.SetupRequirement, nil
		}
	}
	return worktreecontract.SetupRequirementRequired, nil
}

func executionTargetUnavailableCause(err error) (workflow.ExecutionTargetUnavailableCause, bool) {
	var revisionErr *worktree.GitRevisionResolutionError
	if errors.As(err, &revisionErr) {
		switch revisionErr.Kind {
		case worktree.GitRevisionResolutionErrorInvalidRevision:
			return workflow.ExecutionTargetUnavailableCauseInvalidRevision, true
		case worktree.GitRevisionResolutionErrorNonCommit:
			return workflow.ExecutionTargetUnavailableCauseNonCommit, true
		case worktree.GitRevisionResolutionErrorGitFailure:
			return workflow.ExecutionTargetUnavailableCauseGitFailure, true
		}
	}
	var defaultBranchErr *worktree.GitDefaultBranchResolutionError
	if errors.As(err, &defaultBranchErr) {
		switch defaultBranchErr.Kind {
		case worktree.GitDefaultBranchResolutionErrorMissing:
			return workflow.ExecutionTargetUnavailableCauseDefaultBranchMissing, true
		case worktree.GitDefaultBranchResolutionErrorAmbiguous:
			return workflow.ExecutionTargetUnavailableCauseDefaultBranchAmbiguous, true
		case worktree.GitDefaultBranchResolutionErrorGitFailure:
			return workflow.ExecutionTargetUnavailableCauseGitFailure, true
		}
	}
	return "", false
}

func explicitExecutionTargetResolutionError(err error) (*serverapi.WorkflowExecutionTargetResolutionError, bool) {
	var revisionErr *worktree.GitRevisionResolutionError
	if !errors.As(err, &revisionErr) {
		return nil, false
	}
	code := serverapi.WorkflowExecutionTargetResolutionErrorCode("")
	switch revisionErr.Kind {
	case worktree.GitRevisionResolutionErrorInvalidRevision:
		code = serverapi.WorkflowExecutionTargetResolutionErrorInvalidRevision
	case worktree.GitRevisionResolutionErrorNonCommit:
		code = serverapi.WorkflowExecutionTargetResolutionErrorNonCommit
	case worktree.GitRevisionResolutionErrorGitFailure:
		code = serverapi.WorkflowExecutionTargetResolutionErrorGitFailure
	default:
		return nil, false
	}
	return &serverapi.WorkflowExecutionTargetResolutionError{
		Code:         code,
		RequestedRef: revisionErr.RequestedRef,
	}, true
}

func workflowLockedExecutionTargetError(err error) error {
	var lockedErr *worktree.LockedTaskWorktreeError
	if !errors.As(err, &lockedErr) {
		return err
	}
	return &serverapi.WorkflowLockedExecutionTargetError{
		Cause: serverapi.WorkflowLockedExecutionTargetCause(lockedErr.Cause),
	}
}

func (s *Service) InterruptWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskInterruptRequest) (serverapi.WorkflowTaskInterruptResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskInterruptResponse{}, err
	}
	if err := s.authorizeWorkflowTaskMutation(ctx, workflow.TaskID(req.TaskID), req.InvokingSessionID); err != nil {
		return serverapi.WorkflowTaskInterruptResponse{}, err
	}
	selector := workflowexecution.InterruptSelector{TaskID: workflow.TaskID(req.TaskID)}
	if rawSessionID := strings.TrimSpace(req.SessionID); rawSessionID != "" {
		sessionID, err := runtimeids.ParseSessionID(rawSessionID)
		if err != nil {
			return serverapi.WorkflowTaskInterruptResponse{}, err
		}
		selector.SessionID = &sessionID
	}
	if s.currentNodeExecution == nil {
		return serverapi.WorkflowTaskInterruptResponse{}, workflowexecution.ErrNoInterruptibleExecution
	}
	if err := s.currentNodeExecution.Interrupt(ctx, selector); err != nil {
		return serverapi.WorkflowTaskInterruptResponse{}, err
	}
	return serverapi.WorkflowTaskInterruptResponse{}, nil
}

func (s *Service) ResumeWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error) {
	return s.resumeWorkflowTask(ctx, req)
}

func (s *Service) resumeWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskResumeResponse{}, err
	}
	if err := s.authorizeWorkflowTaskMutation(ctx, workflow.TaskID(req.TaskID), req.InvokingSessionID); err != nil {
		return serverapi.WorkflowTaskResumeResponse{}, err
	}
	if s.currentNodeExecution == nil {
		return serverapi.WorkflowTaskResumeResponse{}, errors.New("current node workflow execution is required")
	}
	taskID := workflow.TaskID(req.TaskID)
	return workflowexecution.RunTaskMutation(ctx, s.taskMutations, taskID, func(ctx context.Context) (serverapi.WorkflowTaskResumeResponse, error) {
		return s.resumeWorkflowTaskAuthorized(ctx, req, taskID)
	})
}

func (s *Service) resumeWorkflowTaskAuthorized(
	ctx context.Context,
	req serverapi.WorkflowTaskResumeRequest,
	taskID workflow.TaskID,
) (serverapi.WorkflowTaskResumeResponse, error) {
	promoted, handled, err := s.currentNodeExecution.PromoteConcurrencyQueuedTask(ctx, taskID)
	if err != nil {
		return serverapi.WorkflowTaskResumeResponse{}, err
	}
	if handled {
		if detail, detailErr := s.readModels.TaskDetail.GetTask(ctx, req.TaskID); detailErr == nil {
			s.publishProjectWorkflowEvent(
				ctx,
				detail.Summary.ProjectID,
				detail.Summary.WorkflowID,
				serverapi.WorkflowProjectEventResourceTask,
				serverapi.WorkflowProjectEventActionResumed,
				req.TaskID,
			)
		}
		return serverapi.WorkflowTaskResumeResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
			Applied: &serverapi.WorkflowTaskResumeApplied{
				CurrentNodes: workflowview.ProjectCurrentNodes(promoted),
			},
		}, nil
	}
	preflight, err := s.currentNodeExecution.PreflightTaskResume(ctx, taskID)
	if err != nil {
		return serverapi.WorkflowTaskResumeResponse{}, err
	}
	switch preflight.Outcome {
	case workflowexecution.TaskResumePreflightNoOp:
		return serverapi.WorkflowTaskResumeResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeNoOp,
			NoOp: &serverapi.WorkflowTaskResumeNoOp{
				CurrentNodes: workflowview.ProjectCurrentNodes(preflight.CurrentNodes),
			},
		}, nil
	case workflowexecution.TaskResumePreflightResumable:
	default:
		return serverapi.WorkflowTaskResumeResponse{}, fmt.Errorf(
			"task resume preflight returned invalid outcome %q",
			preflight.Outcome,
		)
	}
	interrupted := preflight.CurrentNodes
	if req.ExecutionTarget == nil {
		selectionRequired, err := configuredTargetResumeSelection(interrupted)
		if err != nil {
			return serverapi.WorkflowTaskResumeResponse{}, err
		}
		if selectionRequired != nil {
			return serverapi.WorkflowTaskResumeResponse{
				Outcome:           serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired,
				SelectionRequired: selectionRequired,
			}, nil
		}
	}
	target, err := s.preflightInitiatingActionTarget(ctx, taskID, req.ExecutionTarget, req.BranchName)
	if err != nil {
		return serverapi.WorkflowTaskResumeResponse{}, err
	}
	observation, err := newTaskSetupObservation(req.SetupOperationID, target.selection, s.setupEvents)
	if err != nil {
		return serverapi.WorkflowTaskResumeResponse{}, err
	}
	setupRequirement, err := resumeSetupRequirement(interrupted, target.selection)
	if err != nil {
		return serverapi.WorkflowTaskResumeResponse{}, err
	}
	var preparation *workflowexecution.TaskStartPreparation
	if target.context.Task.ExecutionTarget == nil {
		target.unavailable = initiatingActionTargetRequestSelection
		snapshot, selectionRequired, err := s.resolveInitiatingActionTarget(ctx, target)
		if err != nil {
			return serverapi.WorkflowTaskResumeResponse{}, err
		}
		if selectionRequired != nil {
			return serverapi.WorkflowTaskResumeResponse{
				Outcome:           serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired,
				SelectionRequired: selectionRequired,
			}, nil
		}
		prepared := s.initiatingActionPreparation(
			taskID,
			req.SetupOperationID,
			target,
			observation,
			func(preparationCtx context.Context) (preparedInitiatingActionTarget, error) {
				return s.materializeInitiatingActionTarget(
					preparationCtx,
					taskID,
					&req.SetupOperationID,
					target,
					snapshot,
					setupRequirement,
				)
			},
		)
		preparation = &prepared
	} else if target.context.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone {
		prepared := workflowexecution.TaskStartPreparation{
			Prepare: func(preparationCtx context.Context) error {
				decision, preparationErr := s.initiatingActionTarget(
					preparationCtx,
					taskID,
					&req.SetupOperationID,
					target,
				)
				if preparationErr == nil && (decision.prepared != nil || decision.selectionRequired != nil) {
					preparationErr = errors.New("locked Resume target returned an initial target decision")
				}
				preparationErr = taskPreparationError(
					req.SetupOperationID, target, nil, nil, nil, preparationErr,
				)
				observation.record(preparedInitiatingActionTarget{}, preparationErr)
				return preparationErr
			},
			Commit: func(context.Context) error { return nil },
		}
		preparation = &prepared
	}
	var resumeResult workflowexecution.TaskResumeResult
	if preparation == nil {
		resumeResult, err = s.currentNodeExecution.ResumeTask(ctx, taskID)
	} else {
		resumeResult, err = s.currentNodeExecution.ResumeTaskWithPreparation(ctx, taskID, *preparation, observation.finalize)
	}
	if err != nil {
		return serverapi.WorkflowTaskResumeResponse{}, err
	}
	if resumeResult.Outcome == workflowexecution.TaskResumeNoOp {
		return serverapi.WorkflowTaskResumeResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeNoOp,
			NoOp: &serverapi.WorkflowTaskResumeNoOp{
				CurrentNodes: workflowview.ProjectCurrentNodes(resumeResult.CurrentNodes),
			},
		}, nil
	}
	resumed := resumeResult.CurrentNodes
	if len(resumed) == 0 {
		return serverapi.WorkflowTaskResumeResponse{}, &workflowexecution.TaskResumeConflictError{TaskID: taskID}
	}
	if preparation == nil {
		observation.finalize(workflowexecution.TaskPreparationFinalization{
			Kind: workflowexecution.TaskPreparationHandedOff,
		})
	}
	if detail, detailErr := s.readModels.TaskDetail.GetTask(ctx, req.TaskID); detailErr == nil {
		s.publishProjectWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionResumed, req.TaskID)
	}
	return serverapi.WorkflowTaskResumeResponse{
		Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
		Applied: &serverapi.WorkflowTaskResumeApplied{
			CurrentNodes: workflowview.ProjectCurrentNodes(resumed),
		},
	}, nil
}

func (s *Service) ApproveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskApproveRequest) (serverapi.WorkflowTaskApproveResponse, error) {
	return s.approveWorkflowTask(ctx, req)
}

func (s *Service) approveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskApproveRequest) (serverapi.WorkflowTaskApproveResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskApproveResponse{}, err
	}
	if s.currentNodeExecution == nil {
		return serverapi.WorkflowTaskApproveResponse{}, errors.New("current node workflow execution is required")
	}
	approvalID, err := workflow.ParseApprovalID(req.ApprovalID)
	if err != nil {
		return serverapi.WorkflowTaskApproveResponse{}, err
	}
	if req.InvokingSessionID != nil {
		approval, err := s.store.PendingApproval(ctx, approvalID)
		if err != nil {
			return serverapi.WorkflowTaskApproveResponse{}, err
		}
		if err := s.authorizeWorkflowTaskMutation(ctx, approval.Source.TaskID, req.InvokingSessionID); err != nil {
			return serverapi.WorkflowTaskApproveResponse{}, err
		}
	}
	approved, err := s.currentNodeExecution.ApplyPendingApproval(ctx, approvalID)
	if err != nil && !approved.Committed() {
		return serverapi.WorkflowTaskApproveResponse{}, err
	}
	s.finalizeTaskAttentionResolution(approved.TaskAttentionResolution)
	taskID := string(approved.ResolvedApproval.Source.TaskID)
	if detail, detailErr := s.readModels.TaskDetail.GetTask(ctx, taskID); detailErr == nil {
		s.publishProjectWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionApproved, taskID, req.ApprovalID)
	}
	return serverapi.WorkflowTaskApproveResponse{
		Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
		Applied: &serverapi.WorkflowTaskApproveApplied{
			TaskID:       taskID,
			CurrentNodes: workflowview.ProjectCurrentNodes(approved.Mutation.Created),
		},
	}, err
}

func (s *Service) MoveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskMoveResponse, error) {
	return s.moveWorkflowTask(ctx, req)
}

func (s *Service) PreviewWorkflowTaskMove(ctx context.Context, req serverapi.WorkflowTaskMovePreviewRequest) (serverapi.WorkflowTaskMovePreviewResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskMovePreviewResponse{}, err
	}
	if s.currentNodeExecution == nil {
		return serverapi.WorkflowTaskMovePreviewResponse{}, errors.New("current node workflow execution is required")
	}
	preview, err := s.store.PreviewManualMove(ctx, workflowstore.ManualMoveRequest{
		TaskID:       workflow.TaskID(req.TaskID),
		TargetNodeID: workflow.NodeID(req.TargetNodeID),
	})
	if err != nil {
		return serverapi.WorkflowTaskMovePreviewResponse{}, err
	}
	if preview.Outcome == workflowstore.ManualMovePreviewOutcomeNoOp {
		return serverapi.WorkflowTaskMovePreviewResponse{
			Outcome: serverapi.WorkflowTaskMovePreviewOutcomeNoOp,
			NoOp: &serverapi.WorkflowTaskMovePreviewNoOp{
				CurrentNodes: workflowview.ProjectCurrentNodes(preview.CurrentNodes),
			},
		}, nil
	}
	if preview.Outcome == workflowstore.ManualMovePreviewOutcomeBlocked {
		reason, err := manualMovePreviewBlocker(preview.Blocker)
		if err != nil {
			return serverapi.WorkflowTaskMovePreviewResponse{}, err
		}
		return serverapi.WorkflowTaskMovePreviewResponse{
			Outcome: serverapi.WorkflowTaskMovePreviewOutcomeBlocked,
			Blocked: &serverapi.WorkflowTaskMovePreviewBlocked{
				Reason: reason,
			},
		}, nil
	}
	disposition, err := s.currentNodeExecution.ManualMoveDisposition(workflow.TaskID(req.TaskID))
	if err != nil {
		return serverapi.WorkflowTaskMovePreviewResponse{}, err
	}
	switch disposition {
	case workflowexecution.ManualMoveDispositionWaitingQuestion:
		return serverapi.WorkflowTaskMovePreviewResponse{
			Outcome: serverapi.WorkflowTaskMovePreviewOutcomeBlocked,
			Blocked: &serverapi.WorkflowTaskMovePreviewBlocked{
				Reason: serverapi.WorkflowTaskMovePreviewBlockerWaitingQuestion,
			},
		}, nil
	case workflowexecution.ManualMoveDispositionLifecycleConflict:
		return serverapi.WorkflowTaskMovePreviewResponse{
			Outcome: serverapi.WorkflowTaskMovePreviewOutcomeBlocked,
			Blocked: &serverapi.WorkflowTaskMovePreviewBlocked{
				Reason: serverapi.WorkflowTaskMovePreviewBlockerLifecycleConflict,
			},
		}, nil
	case workflowexecution.ManualMoveDispositionQuiescent, workflowexecution.ManualMoveDispositionAutoInterruptible:
	default:
		return serverapi.WorkflowTaskMovePreviewResponse{}, fmt.Errorf("manual move disposition %q is invalid", disposition)
	}
	switch preview.Outcome {
	case workflowstore.ManualMovePreviewOutcomeDirect:
		return serverapi.WorkflowTaskMovePreviewResponse{
			Outcome: serverapi.WorkflowTaskMovePreviewOutcomeDirect,
			Direct:  &serverapi.WorkflowTaskMovePreviewDirect{},
		}, nil
	case workflowstore.ManualMovePreviewOutcomeTransition:
		choices := make([]serverapi.WorkflowTaskMovePreviewTransitionChoice, 0, len(preview.Choices))
		for _, choice := range preview.Choices {
			requiredValues := make([]serverapi.WorkflowTaskMoveRequiredValue, 0, len(choice.RequiredValues))
			for _, value := range choice.RequiredValues {
				requiredValues = append(requiredValues, serverapi.WorkflowTaskMoveRequiredValue{
					NodeKey:       string(value.NodeKey),
					OutputName:    value.OutputName,
					Description:   value.Description,
					ResolvedValue: value.ResolvedValue,
				})
			}
			choices = append(choices, serverapi.WorkflowTaskMovePreviewTransitionChoice{
				TransitionKey:         string(choice.TransitionKey),
				Label:                 choice.Label,
				SourceNodeDisplayName: workflow.NodeDisplayName(choice.SourceNode),
				RequiredValues:        requiredValues,
			})
		}
		return serverapi.WorkflowTaskMovePreviewResponse{
			Outcome:    serverapi.WorkflowTaskMovePreviewOutcomeTransition,
			Transition: &serverapi.WorkflowTaskMovePreviewTransition{Choices: choices},
		}, nil
	default:
		return serverapi.WorkflowTaskMovePreviewResponse{}, fmt.Errorf("manual move preview outcome %q is invalid", preview.Outcome)
	}
}

func (s *Service) moveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskMoveResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskMoveResponse{}, err
	}
	if err := s.authorizeWorkflowTaskMutation(ctx, workflow.TaskID(req.TaskID), req.InvokingSessionID); err != nil {
		return serverapi.WorkflowTaskMoveResponse{}, err
	}
	if s.currentNodeExecution == nil {
		return serverapi.WorkflowTaskMoveResponse{}, errors.New("current node workflow execution is required")
	}
	values := make(map[workflow.ModelKey]map[string]string, len(req.Values))
	for nodeKey, outputs := range req.Values {
		converted := make(map[string]string, len(outputs))
		for outputName, value := range outputs {
			converted[outputName] = value
		}
		values[workflow.ModelKey(nodeKey)] = converted
	}
	var transitionKey *workflow.TransitionID
	if req.TransitionKey != nil {
		value := workflow.TransitionID(*req.TransitionKey)
		transitionKey = &value
	}
	moveRequest := workflowstore.ManualMoveRequest{
		TaskID:        workflow.TaskID(req.TaskID),
		TargetNodeID:  workflow.NodeID(req.TargetNodeID),
		TransitionKey: transitionKey,
		Values:        values,
		Commentary:    req.Commentary,
	}
	prepared, err := s.store.PrepareManualMove(ctx, moveRequest)
	if err != nil {
		return serverapi.WorkflowTaskMoveResponse{}, err
	}
	if req.BranchName != nil && (prepared.IsNoOp() || !prepared.RequiresExecutionTarget()) {
		return serverapi.WorkflowTaskMoveResponse{}, operationCannotCreateInitialWorktreeError(*req.BranchName)
	}
	if prepared.IsNoOp() {
		return serverapi.WorkflowTaskMoveResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeNoOp,
			NoOp: &serverapi.WorkflowTaskMoveNoOp{
				CurrentNodes: workflowview.ProjectCurrentNodes(prepared.CurrentNodes()),
			},
		}, nil
	}
	var targetPreflight initiatingActionTargetPreflight
	if prepared.RequiresExecutionTarget() {
		if !req.ProceedDespiteDependencies {
			count, countErr := s.readModels.TaskDependencies.CountUnsatisfiedBlockers(ctx, req.TaskID)
			if countErr != nil {
				return serverapi.WorkflowTaskMoveResponse{}, countErr
			}
			if count > 0 {
				return serverapi.WorkflowTaskMoveResponse{
					Outcome:                    serverapi.WorkflowExecutionTargetActionOutcomeDependencyConfirmationRequired,
					UnsatisfiedDependencyCount: &count,
				}, nil
			}
		}
		targetPreflight, err = s.preflightInitiatingActionTarget(ctx, moveRequest.TaskID, req.ExecutionTarget, req.BranchName)
		if err != nil {
			return serverapi.WorkflowTaskMoveResponse{}, err
		}
	}
	coordinated, err := coordinateInitiatingAction(ctx, s, initiatingActionRequest{
		taskID:                  moveRequest.TaskID,
		requiresExecutionTarget: prepared.RequiresExecutionTarget(),
		targetPreflight:         targetPreflight,
		afterTargetResolution: func() error {
			return s.currentNodeExecution.InterruptForManualMove(ctx, moveRequest.TaskID, func() error {
				preview, err := s.store.PreviewManualMove(ctx, moveRequest)
				if err != nil {
					return err
				}
				if preview.Outcome == workflowstore.ManualMovePreviewOutcomeNoOp {
					return &manualMoveNoOpBeforeInterruptError{
						currentNodes: append([]workflow.CurrentNode(nil), preview.CurrentNodes...),
					}
				}
				return nil
			})
		},
	}, func(candidate *workflowstore.ExecutionTargetCandidate) (*workflowstore.ManualMoveResult, error) {
		moved, err := s.currentNodeExecution.ApplyManualMove(ctx, prepared, candidate)
		if err != nil && moved.Outcome != workflowstore.ManualMoveResultOutcomeApplied &&
			moved.Outcome != workflowstore.ManualMoveResultOutcomeNoOp {
			return nil, err
		}
		return &moved, err
	})
	var noOpBeforeInterrupt *manualMoveNoOpBeforeInterruptError
	if errors.As(err, &noOpBeforeInterrupt) {
		if targetPreflight.pendingBranchReplaced {
			return serverapi.WorkflowTaskMoveResponse{}, workflowexecution.ErrManualMoveLifecycleConflict
		}
		return serverapi.WorkflowTaskMoveResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeNoOp,
			NoOp: &serverapi.WorkflowTaskMoveNoOp{
				CurrentNodes:             workflowview.ProjectCurrentNodes(noOpBeforeInterrupt.currentNodes),
				RetainedPreviousWorktree: coordinated.retainedPreviousWorktree,
			},
		}, nil
	}
	if err != nil && coordinated.applied == nil {
		return serverapi.WorkflowTaskMoveResponse{}, err
	}
	if coordinated.selectionRequired != nil {
		return serverapi.WorkflowTaskMoveResponse{
			Outcome:           serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired,
			SelectionRequired: coordinated.selectionRequired,
		}, nil
	}
	if coordinated.applied == nil {
		return serverapi.WorkflowTaskMoveResponse{}, errors.New("coordinated task move returned no applied result")
	}
	if err != nil {
		slog.Error(
			"manual move committed with post-commit lifecycle error",
			"task_id", req.TaskID,
			"target_node_id", req.TargetNodeID,
			"error", err,
		)
	}
	moved := *coordinated.applied
	if err := moved.Validate(); err != nil {
		return serverapi.WorkflowTaskMoveResponse{}, err
	}
	if moved.Outcome == workflowstore.ManualMoveResultOutcomeNoOp {
		if targetPreflight.pendingBranchReplaced {
			return serverapi.WorkflowTaskMoveResponse{}, workflowexecution.ErrManualMoveLifecycleConflict
		}
		return serverapi.WorkflowTaskMoveResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeNoOp,
			NoOp: &serverapi.WorkflowTaskMoveNoOp{
				CurrentNodes:             workflowview.ProjectCurrentNodes(moved.CurrentNodes),
				RetainedPreviousWorktree: coordinated.retainedPreviousWorktree,
			},
		}, nil
	}
	s.finalizeTaskAttentionResolution(moved.TaskAttentionResolution)
	if detail, detailErr := s.readModels.TaskDetail.GetTask(ctx, req.TaskID); detailErr == nil {
		s.publishProjectWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionMoved, req.TaskID)
	}
	return serverapi.WorkflowTaskMoveResponse{
		Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
		Applied: &serverapi.WorkflowTaskMoveApplied{
			CurrentNodes:             workflowview.ProjectCurrentNodes(moved.Mutation.Created),
			RetainedPreviousWorktree: coordinated.retainedPreviousWorktree,
		},
	}, nil
}

func manualMovePreviewBlocker(blocker workflowstore.ManualMoveBlocker) (serverapi.WorkflowTaskMovePreviewBlocker, error) {
	switch blocker {
	case workflowstore.ManualMoveBlockerInvalidWorkflow:
		return serverapi.WorkflowTaskMovePreviewBlockerInvalidWorkflow, nil
	case workflowstore.ManualMoveBlockerNoSourcePosition:
		return serverapi.WorkflowTaskMovePreviewBlockerNoSourcePosition, nil
	case workflowstore.ManualMoveBlockerUnsupportedDestination:
		return serverapi.WorkflowTaskMovePreviewBlockerUnsupportedDestination, nil
	case workflowstore.ManualMoveBlockerWaitingQuestion:
		return serverapi.WorkflowTaskMovePreviewBlockerWaitingQuestion, nil
	case workflowstore.ManualMoveBlockerLifecycleConflict:
		return serverapi.WorkflowTaskMovePreviewBlockerLifecycleConflict, nil
	case workflowstore.ManualMoveBlockerContextSessionUnavailable:
		return serverapi.WorkflowTaskMovePreviewBlockerContextSessionUnavailable, nil
	case workflowstore.ManualMoveBlockerNoUsableTransition:
		return serverapi.WorkflowTaskMovePreviewBlockerNoUsableTransition, nil
	case workflowstore.ManualMoveBlockerParallelBranchRequiresFanOut:
		return serverapi.WorkflowTaskMovePreviewBlockerParallelBranchRequiresFanOut, nil
	default:
		return "", fmt.Errorf("manual move blocker %q is invalid", blocker)
	}
}

func (s *Service) authorizeWorkflowTaskMutation(
	ctx context.Context,
	targetTaskID workflow.TaskID,
	invokingSessionID *runtimeids.SessionID,
) error {
	if invokingSessionID == nil {
		return nil
	}
	invokingTaskID, err := s.store.TaskIDForSession(ctx, *invokingSessionID)
	if err != nil {
		return err
	}
	if invokingTaskID != nil && *invokingTaskID == targetTaskID {
		return &serverapi.WorkflowTaskMutationSelfTargetError{TaskID: string(targetTaskID)}
	}
	return nil
}

func (s *Service) CompleteWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCompleteRequest) (serverapi.WorkflowTaskCompleteResponse, error) {
	return s.completeWorkflowTask(ctx, req)
}

func (s *Service) completeWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCompleteRequest) (serverapi.WorkflowTaskCompleteResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskCompleteResponse{}, err
	}
	if s.currentNodeExecution == nil {
		return serverapi.WorkflowTaskCompleteResponse{}, errors.New("current node workflow execution is required")
	}
	var (
		completed workflowstore.CurrentNodeCompletionResult
		taskID    workflow.TaskID
		err       error
	)
	if req.ActorKind == serverapi.WorkflowTaskCompleteActorAgent {
		sessionID, parseErr := runtimeids.ParseSessionID(req.AgentSessionID)
		if parseErr != nil {
			return serverapi.WorkflowTaskCompleteResponse{}, parseErr
		}
		completed, err = s.currentNodeExecution.CompleteSessionCurrentNode(ctx, sessionID, req.TransitionID, req.OutputValues, req.Commentary)
	} else {
		selector := workflowstore.IdleCurrentNodeSelector{}
		if strings.TrimSpace(req.SessionID) != "" {
			sessionID, parseErr := runtimeids.ParseSessionID(req.SessionID)
			if parseErr != nil {
				return serverapi.WorkflowTaskCompleteResponse{}, parseErr
			}
			selector.SessionID = &sessionID
		} else {
			value := workflow.TaskID(req.TaskID)
			selector.TaskID = &value
		}
		completed, err = s.currentNodeExecution.CompleteIdleCurrentNode(ctx, selector, req.TransitionID, req.OutputValues, req.Commentary)
	}
	if err != nil && !completed.Committed() {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) {
			return serverapi.WorkflowTaskCompleteResponse{}, serverapi.ErrWorkflowTaskCompleteTargetNotFound
		}
		if errors.Is(err, workflowstore.ErrCurrentNodeCompletionSelectorAmbiguous) {
			return serverapi.WorkflowTaskCompleteResponse{}, serverapi.WorkflowTaskCompleteSelectorAmbiguousError{}
		}
		return serverapi.WorkflowTaskCompleteResponse{}, err
	}
	if completed.PendingApproval != nil {
		taskID = completed.PendingApproval.Source.TaskID
	} else {
		if len(completed.Mutation.Removed) != 1 {
			return serverapi.WorkflowTaskCompleteResponse{}, errors.New("current node completion did not remove exactly one source")
		}
		taskID = completed.Mutation.Removed[0].TaskID
	}
	response := serverapi.WorkflowTaskCompleteResponse{
		TaskID:       string(taskID),
		CurrentNodes: workflowview.ProjectCurrentNodes(completed.Mutation.Created),
		Handoff: serverapi.WorkflowTaskCompletionHandoff{
			SourceNodeDisplayName:  completed.Handoff.SourceNodeDisplayName,
			DestinationDisplayName: completed.Handoff.DestinationDisplayName,
		},
	}
	if completed.PendingApproval != nil {
		approvalID := completed.PendingApproval.ID.String()
		response.PendingApprovalID = &approvalID
		if s.attentionFinalizer != nil {
			finalizeCtx, cancel := workflowAttentionContext(ctx)
			defer cancel()
			s.attentionFinalizer.PublishPendingApproval(finalizeCtx, completed.PendingApproval.ID)
		}
	}
	return response, err
}

func (s *Service) DeleteWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskDeleteRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if s.taskWorktreeCleanup != nil {
		if err := s.taskWorktreeCleanup.EnsureTaskWorktreeDeletable(ctx, req.TaskID); err != nil {
			return err
		}
	}
	return s.taskMutations.Run(ctx, workflow.TaskID(req.TaskID), func(ctx context.Context) error {
		if s.currentNodeExecution == nil {
			return errors.New("current node workflow execution is required")
		}
		if err := s.currentNodeExecution.EnsureTaskQuiescent(workflow.TaskID(req.TaskID)); err != nil {
			return err
		}
		if s.taskWorktreeCleanup != nil {
			if err := s.taskWorktreeCleanup.DeleteTaskWorktree(ctx, req.TaskID); err != nil {
				return err
			}
		}
		result, err := s.store.DeleteTask(ctx, workflow.TaskID(req.TaskID))
		if err != nil {
			return err
		}
		s.finalizeTaskAttentionResolution(result.TaskAttentionResolution)
		s.publishProjectWorkflowEvent(ctx, result.ProjectID, result.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionDeleted, req.TaskID)
		return nil
	})
}

func (s *Service) finalizeWorkflowAttentionResolution(ctx context.Context, result workflowstore.WorkflowDeleteResult) {
	s.finalizeTaskAttentionResolution(result.TaskAttentionResolution)
}

func (s *Service) finalizeTaskAttentionResolution(resolution workflowstore.TaskAttentionResolution) {
	if s == nil || s.attentionFinalizer == nil {
		return
	}
	s.attentionFinalizer.FinalizeTaskResolution(resolution)
}

func workflowAttentionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), workflowAttentionFinalizationTimeout)
	}
	return context.WithTimeout(context.WithoutCancel(ctx), workflowAttentionFinalizationTimeout)
}

func (s *Service) ListWorkflowAttention(ctx context.Context, req serverapi.WorkflowAttentionListRequest) (serverapi.WorkflowAttentionListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowAttentionListResponse{}, err
	}
	response, err := s.readModels.Attention.List(ctx, req)
	if err != nil {
		return serverapi.WorkflowAttentionListResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return serverapi.WorkflowAttentionListResponse{}, err
	}
	return response, nil
}

func (s *Service) ListWorkflowTaskAttention(ctx context.Context, req serverapi.WorkflowTaskAttentionListRequest) (serverapi.WorkflowTaskAttentionListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
	response, err := s.readModels.Attention.ListTask(ctx, req)
	if err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
	if err := response.ValidateForTask(strings.TrimSpace(req.TaskID)); err != nil {
		return serverapi.WorkflowTaskAttentionListResponse{}, err
	}
	return response, nil
}

func (s *Service) AddWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentAddRequest) (serverapi.WorkflowTaskCommentAddResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskCommentAddResponse{}, err
	}
	comment, err := s.store.AddComment(ctx, workflow.TaskID(req.TaskID), req.Body, req.Author, req.AuthorID)
	if err != nil {
		return serverapi.WorkflowTaskCommentAddResponse{}, err
	}
	if detail, detailErr := s.readModels.TaskDetail.GetTask(ctx, req.TaskID); detailErr == nil {
		s.publishProjectWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionCommentAdded, req.TaskID, comment.ID)
	}
	return serverapi.WorkflowTaskCommentAddResponse{Comment: commentRecord(comment)}, nil
}

func (s *Service) ListWorkflowTaskComments(ctx context.Context, req serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskCommentListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskCommentListResponse{}, err
	}
	window, err := serverapi.ResolveWorkflowOffsetWindow(req.Offset, req.Limit)
	if err != nil {
		return serverapi.WorkflowTaskCommentListResponse{}, err
	}
	totalCount, err := s.store.CountTaskComments(ctx, workflow.TaskID(req.TaskID))
	if err != nil {
		return serverapi.WorkflowTaskCommentListResponse{}, err
	}
	comments, err := s.store.ListCommentsPage(ctx, workflow.TaskID(req.TaskID), window.Offset, window.Limit+1)
	if err != nil {
		return serverapi.WorkflowTaskCommentListResponse{}, err
	}
	out := make([]serverapi.WorkflowTaskComment, 0, len(comments))
	for _, comment := range comments {
		out = append(out, commentRecord(comment))
	}
	return serverapi.WorkflowTaskCommentListResponse{
		WorkflowOffsetPage: serverapi.FinalizeWorkflowOffsetPage(window, out),
		TotalCount:         totalCount,
	}, nil
}

func (s *Service) ReplaceWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentReplaceRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	taskID, projectID, workflowID, err := s.store.TaskIdentityForComment(ctx, strings.TrimSpace(req.CommentID))
	if err != nil {
		return err
	}
	if err := s.store.ReplaceComment(ctx, req.CommentID, req.Body); err != nil {
		return err
	}
	s.publishProjectWorkflowEvent(ctx, projectID, workflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionCommentUpdated, taskID, req.CommentID)
	return nil
}

func (s *Service) DeleteWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentDeleteRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	taskID, projectID, workflowID, err := s.store.TaskIdentityForComment(ctx, strings.TrimSpace(req.CommentID))
	if err != nil {
		return err
	}
	if err := s.store.DeleteComment(ctx, req.CommentID); err != nil {
		return err
	}
	s.publishProjectWorkflowEvent(ctx, projectID, workflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionCommentDeleted, taskID, req.CommentID)
	return nil
}

func (s *Service) ListWorkflowTaskActivity(ctx context.Context, req serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskActivityListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	response, err := s.readModels.Activity.List(ctx, req)
	if err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	if err := response.ValidateForTask(strings.TrimSpace(req.TaskID)); err != nil {
		return serverapi.WorkflowTaskActivityListResponse{}, err
	}
	return response, nil
}

func (s *Service) ListWorkflowTaskSessions(ctx context.Context, req serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskSessionListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskSessionListResponse{}, err
	}
	response, err := s.readModels.TaskSessions.List(ctx, req)
	if err != nil {
		return serverapi.WorkflowTaskSessionListResponse{}, err
	}
	return response, nil
}

func (s *Service) ListWorkflowTasks(ctx context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskListResponse{}, err
	}
	return s.readModels.TaskList.List(ctx, req)
}

func (s *Service) SearchWorkflowTasks(ctx context.Context, req serverapi.TaskSearchRequest) (serverapi.TaskSearchResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.TaskSearchResponse{}, err
	}
	return s.readModels.TaskSearch.Search(ctx, req)
}

func (s *Service) GetWorkflowBoard(ctx context.Context, req serverapi.WorkflowBoardRequest) (serverapi.WorkflowBoardResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowBoardResponse{}, err
	}
	board, err := s.readModels.Board.Get(ctx, req)
	if err != nil {
		return serverapi.WorkflowBoardResponse{}, err
	}
	return serverapi.WorkflowBoardResponse{Board: board}, nil
}

func (s *Service) ListWorkflowBoardNodeCards(ctx context.Context, req serverapi.WorkflowBoardNodeCardsListRequest) (serverapi.WorkflowBoardNodeCardsListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowBoardNodeCardsListResponse{}, err
	}
	return s.readModels.Board.ListNodeCards(ctx, req)
}

func (s *Service) SubscribeWorkflowProject(ctx context.Context, req serverapi.WorkflowProjectSubscribeRequest) (serverapi.WorkflowProjectSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return s.events.subscribe(strings.TrimSpace(req.ProjectID), nil)
}

func (s *Service) SubscribeWorkflow(ctx context.Context, req serverapi.WorkflowSubscribeRequest) (serverapi.WorkflowSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if _, err := s.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: req.WorkflowID}); err != nil {
		return nil, err
	}
	return s.events.subscribe("", &req.WorkflowID)
}

func (s *Service) GetWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskGetResponse{}, err
	}
	var (
		detail serverapi.WorkflowTaskDetail
		err    error
	)
	if strings.TrimSpace(req.TaskID) != "" {
		detail, err = s.readModels.TaskDetail.GetTask(ctx, req.TaskID)
	} else if strings.TrimSpace(req.ProjectID) != "" {
		detail, err = s.readModels.TaskDetail.GetTaskByProjectShortID(ctx, req.ProjectID, req.ShortID)
	} else {
		detail, err = s.readModels.TaskDetail.GetTaskByShortID(ctx, req.ShortID)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return serverapi.WorkflowTaskGetResponse{}, errors.Join(serverapi.ErrWorkflowTaskNotFound, err)
		}
		return serverapi.WorkflowTaskGetResponse{}, err
	}
	return serverapi.WorkflowTaskGetResponse{Task: detail}, nil
}

func workflowRecord(row workflowstore.WorkflowRecord) serverapi.WorkflowRecord {
	record := serverapi.WorkflowRecord{
		ID:                    row.ID,
		Name:                  row.Name,
		Description:           row.Description,
		Version:               row.Version,
		ExecutionTargetPolicy: workflowExecutionTargetPolicyToAPI(row.ExecutionTargetPolicy),
	}
	if row.ProjectLink != nil {
		record.ProjectLink = &serverapi.WorkflowListProjectLink{Default: row.ProjectLink.Default}
	}
	return record
}

func projectWorkflowLink(row workflowstore.ProjectWorkflowLinkRecord) serverapi.ProjectWorkflowLink {
	return serverapi.ProjectWorkflowLink{ID: row.ID, ProjectID: row.ProjectID, WorkflowID: row.WorkflowID, Default: row.IsDefault}
}

func workflowStoreDefaultPolicy(policy serverapi.WorkflowProjectLinkDefaultMode) workflowstore.WorkflowLinkDefaultPolicy {
	switch policy {
	case serverapi.WorkflowProjectLinkDefaultAlways:
		return workflowstore.WorkflowLinkDefaultAlways
	case serverapi.WorkflowProjectLinkDefaultIfProjectHasNone:
		return workflowstore.WorkflowLinkDefaultIfProjectHasNone
	default:
		return workflowstore.WorkflowLinkDefaultNever
	}
}

func workflowUnlinkProjectResponse(result workflowstore.ProjectWorkflowUnlinkResult) serverapi.WorkflowUnlinkProjectResponse {
	resp := serverapi.WorkflowUnlinkProjectResponse{LinkID: result.LinkID, Unlinked: result.Unlinked}
	for _, blocker := range result.Blockers {
		dto := serverapi.WorkflowUnlinkProjectBlocker{Code: blocker.Code, Message: blocker.Message, Count: blocker.Count}
		for _, task := range blocker.Tasks {
			dto.Tasks = append(dto.Tasks, serverapi.WorkflowUnlinkTaskReference{TaskID: string(task.TaskID), ShortID: task.ShortID, Title: task.Title})
		}
		resp.Blockers = append(resp.Blockers, dto)
	}
	return resp
}

func workflowDeleteResponse(result workflowstore.WorkflowDeleteResult) serverapi.WorkflowDeleteResponse {
	resp := serverapi.WorkflowDeleteResponse{Deleted: result.Deleted, Impact: workflowDeleteImpact(result.Impact)}
	for _, blocker := range result.Blockers {
		resp.Blockers = append(resp.Blockers, serverapi.WorkflowDeleteBlocker{Code: blocker.Code, Message: blocker.Message, Count: blocker.Count})
	}
	return resp
}

func workflowDeleteImpact(impact workflowstore.WorkflowDeleteImpact) serverapi.WorkflowDeleteImpact {
	return serverapi.WorkflowDeleteImpact{
		WorkflowID:                     impact.WorkflowID,
		Version:                        impact.Version,
		ProjectCount:                   impact.ProjectCount,
		LinkCount:                      impact.LinkCount,
		DefaultReplacementProjectCount: impact.DefaultReplacementProjectCount,
		TaskCount:                      impact.TaskCount,
		CurrentNodeCount:               impact.CurrentNodeCount,
		PendingApprovalCount:           impact.PendingApprovalCount,
		BlockedTaskCount:               impact.BlockedTaskCount,
	}
}

func (s *Service) workflowGraphValidationResults(ctx context.Context, workflowID runtimeids.WorkflowID, metadata *serverapi.WorkflowGraphMetadata, graph serverapi.WorkflowGraphDraft, modes []serverapi.WorkflowValidationMode) (map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse, error) {
	def, err := s.workflowGraphDraftDefinition(ctx, workflowID, metadata, graph)
	if err != nil {
		return nil, err
	}
	return s.workflowGraphValidationResultsForDefinition(def, modes), nil
}

func (s *Service) workflowGraphValidationResultsForDefinition(def workflow.Definition, modes []serverapi.WorkflowValidationMode) map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse {
	out := make(map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse, len(modes))
	contexts := make([]workflow.ValidationContext, 0, len(modes))
	for _, mode := range modes {
		contexts = append(contexts, workflow.ValidationContext(mode))
	}
	results := workflowscript.EvaluateDefinition(def, contexts, s.roleResolver, nil)
	for _, mode := range modes {
		out[mode] = workflowValidationResponse(def.ID, results[workflow.ValidationContext(mode)])
	}
	return out
}

func scriptPathValidationError(workflowID runtimeids.WorkflowID, nodeID workflow.NodeID, diagnostic workflowscript.Diagnostic) serverapi.WorkflowValidationError {
	workflowIDValue := workflowID
	nodeIDValue := string(nodeID)
	return serverapi.WorkflowValidationError{
		Code:          diagnostic.Code,
		Message:       diagnostic.Message,
		WorkflowID:    &workflowIDValue,
		NodeID:        &nodeIDValue,
		BlocksContext: diagnostic.Blocking,
	}
}

func workflowValidationErrorsValid(errors []serverapi.WorkflowValidationError) bool {
	for _, err := range errors {
		if err.BlocksContext {
			return false
		}
	}
	return true
}

func (s *Service) workflowGraphDraftDefinition(ctx context.Context, workflowID runtimeids.WorkflowID, metadata *serverapi.WorkflowGraphMetadata, graph serverapi.WorkflowGraphDraft) (workflow.Definition, error) {
	current, _, err := s.store.GetDefinition(ctx, workflowID)
	if err != nil {
		return workflow.Definition{}, err
	}
	def, err := workflowDefinitionFromGraphDraft(workflowID, graph)
	if err != nil {
		return workflow.Definition{}, err
	}
	displayName := current.DisplayName
	if metadata != nil {
		displayName = metadata.Name
	}
	targetPolicy := current.ExecutionTargetPolicy
	if metadata != nil && metadata.ExecutionTargetPolicy != nil {
		targetPolicy = workflowExecutionTargetPolicyFromAPI(*metadata.ExecutionTargetPolicy)
	}
	def.DisplayName = displayName
	def.ExecutionTargetPolicy = targetPolicy
	return def, nil
}

func workflowDefinitionFromGraphDraft(workflowID runtimeids.WorkflowID, graph serverapi.WorkflowGraphDraft) (workflow.Definition, error) {
	def := workflow.Definition{ID: workflowID}
	groupMemberIDs := map[string][]workflow.NodeID{}
	for _, group := range graph.NodeGroups {
		def.NodeGroups = append(def.NodeGroups, workflow.NodeGroup{
			WorkflowID:  workflowID,
			ID:          group.ID,
			Key:         workflow.ModelKey(group.Key),
			DisplayName: group.DisplayName,
		})
	}
	for _, node := range graph.Nodes {
		if node.GroupID != nil {
			groupMemberIDs[*node.GroupID] = append(groupMemberIDs[*node.GroupID], workflow.NodeID(node.ID))
		}
		workflowNode, err := workflow.NewNode(
			workflow.NodeIdentity{
				WorkflowID:  workflowID,
				ID:          workflow.NodeID(node.ID),
				Key:         workflow.ModelKey(node.Key),
				DisplayName: node.DisplayName,
				GroupID:     textutil.Pointer(node.GroupID),
			},
			workflow.NodeKind(node.Kind),
			workflow.NodeFields{
				SubagentRole:   node.SubagentRole,
				CompletionMode: node.CompletionMode,
				ScriptPath: func() workflow.OptionalScriptPath {
					if scriptPath, ok := workflow.PresentScriptPath(optionalStringValue(node.ScriptPath)); ok {
						return scriptPath
					}
					return workflow.AbsentScriptPath()
				}(),
				JoinInputProviders: joinInputProviders(node.JoinInputProviders),
			},
		)
		if err != nil {
			return workflow.Definition{}, err
		}
		def.Nodes = append(def.Nodes, workflowNode)
	}
	for index := range def.NodeGroups {
		def.NodeGroups[index].MemberNodeIDs = groupMemberIDs[def.NodeGroups[index].ID]
	}
	for _, group := range graph.TransitionGroups {
		def.TransitionGroups = append(def.TransitionGroups, workflow.TransitionGroup{
			WorkflowID:   workflowID,
			ID:           workflow.TransitionGroupID(group.ID),
			SourceNodeID: workflow.NodeID(group.SourceNodeID),
			TransitionID: workflow.TransitionID(group.TransitionID),
			DisplayName:  group.DisplayName,
			Description:  group.Description,
		})
	}
	for _, edge := range graph.Edges {
		def.Edges = append(def.Edges, workflow.Edge{
			WorkflowID:        workflowID,
			ID:                workflow.EdgeID(edge.ID),
			Key:               workflow.ModelKey(edge.Key),
			TransitionGroupID: workflow.TransitionGroupID(edge.TransitionGroupID),
			TargetNodeID:      workflow.NodeID(edge.TargetNodeID),
			AssigneeSelection: workflow.AssigneeSelection(edge.AssigneeSelection),
			ThinkingSelection: workflow.ThinkingSelection(edge.ThinkingSelection),
			ContextMode:       workflow.ContextMode(edge.ContextMode),
			ContextSource:     workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(edge.ContextSource.Kind), NodeKey: workflow.ModelKey(edge.ContextSource.NodeKey)}),
			RequiresApproval:  edge.RequiresApproval,
			PromptTemplate:    edge.PromptTemplate,
			Parameters:        domainParameters(edge.Parameters),
		})
	}
	return def, nil
}

func workflowValidationResponse(workflowID runtimeids.WorkflowID, result workflow.ValidationResult) serverapi.WorkflowValidateResponse {
	resp := serverapi.WorkflowValidateResponse{Valid: !result.HasBlockingErrors()}
	resp.Errors = workflowview.ValidationErrors(workflow.WorkflowIDPointer(workflowID), result.Errors)
	return resp
}

func workflowGraphSaveValidationModes() []serverapi.WorkflowValidationMode {
	return []serverapi.WorkflowValidationMode{serverapi.WorkflowValidationModeDraft, serverapi.WorkflowValidationModeExecution}
}

func workflowGraphSaveValidationResponses(result workflowstore.WorkflowGraphSaveResult) map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse {
	out := make(map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse, 2)
	for _, mode := range workflowGraphSaveValidationModes() {
		context := workflow.ValidationContext(mode)
		out[mode] = workflowValidationResponse(result.Definition.ID, result.ValidationResults[context])
	}
	return out
}

func workflowGraphStoreSaveRequest(workflowID runtimeids.WorkflowID, expectedVersion int64, metadata *serverapi.WorkflowGraphMetadata, graph serverapi.WorkflowGraphDraft, confirmation *serverapi.WorkflowGraphSaveConfirmation) (workflowstore.WorkflowGraphSaveRequest, error) {
	definition, err := workflowDefinitionFromGraphDraft(workflowID, graph)
	if err != nil {
		return workflowstore.WorkflowGraphSaveRequest{}, err
	}
	req := workflowstore.NewWorkflowGraphSaveRequest(definition, expectedVersion)
	if metadata != nil {
		req.Metadata = &workflowstore.WorkflowGraphSaveMetadata{Name: metadata.Name, Description: metadata.Description}
		if metadata.ExecutionTargetPolicy != nil {
			policy := workflowExecutionTargetPolicyFromAPI(*metadata.ExecutionTargetPolicy)
			req.Metadata.ExecutionTargetPolicy = &policy
		}
	}
	if confirmation != nil {
		req.Confirmed = true
		req.ExpectedRemovedNodeGroupCount = confirmation.ExpectedRemovedNodeGroupCount
		req.ExpectedRemovedNodeCount = confirmation.ExpectedRemovedNodeCount
		req.ExpectedRemovedTransitionGroupCount = confirmation.ExpectedRemovedTransitionGroupCount
		req.ExpectedRemovedEdgeCount = confirmation.ExpectedRemovedEdgeCount
		req.ExpectedNodeTaskReferenceCount = confirmation.ExpectedNodeTaskReferenceCount
		req.ExpectedEdgeTaskReferenceCount = confirmation.ExpectedEdgeTaskReferenceCount
	}
	return req, nil
}

func workflowExecutionTargetPolicyToAPI(policy workflow.ExecutionTargetPolicy) serverapi.WorkflowExecutionTargetConfiguration {
	policy = policy.Canonical()
	var customRef *string
	if policy.CustomRef != nil {
		value := *policy.CustomRef
		customRef = &value
	}
	return serverapi.WorkflowExecutionTargetConfiguration{
		Mode:      serverapi.WorkflowExecutionTargetMode(policy.Mode),
		CustomRef: customRef,
	}
}

func workflowExecutionTargetPolicyFromAPI(policy serverapi.WorkflowExecutionTargetConfiguration) workflow.ExecutionTargetPolicy {
	var customRef *string
	if policy.CustomRef != nil {
		value := *policy.CustomRef
		customRef = &value
	}
	return workflow.ExecutionTargetPolicy{
		Mode:      workflow.ExecutionTargetMode(policy.Mode),
		CustomRef: customRef,
	}.Canonical()
}

func workflowGraphSavePreviewResponse(result workflowstore.WorkflowGraphSaveResult, validationResults map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse) serverapi.WorkflowGraphSavePreviewResponse {
	return serverapi.WorkflowGraphSavePreviewResponse{
		CurrentVersion:       result.Version,
		Changed:              result.Changed,
		ValidationResults:    validationResults,
		Impact:               workflowGraphSaveImpact(result),
		Blockers:             workflowGraphSaveBlockers(result.Blockers),
		CanSave:              result.CanSave,
		ConfirmationRequired: result.ConfirmationRequired,
	}
}

func workflowGraphSaveResponse(result workflowstore.WorkflowGraphSaveResult, validationResults map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse) serverapi.WorkflowGraphSaveResponse {
	return serverapi.WorkflowGraphSaveResponse{
		Saved:                result.Saved,
		Changed:              result.Changed,
		CurrentVersion:       result.Version,
		ValidationResults:    validationResults,
		Impact:               workflowGraphSaveImpact(result),
		Blockers:             workflowGraphSaveBlockers(result.Blockers),
		CanSave:              result.CanSave,
		ConfirmationRequired: result.ConfirmationRequired,
	}
}

func workflowGraphSaveImpact(result workflowstore.WorkflowGraphSaveResult) serverapi.WorkflowGraphSaveImpact {
	return serverapi.WorkflowGraphSaveImpact{
		RemovedNodeGroupCount:             result.Impact.RemovedNodeGroupCount,
		RemovedNodeCount:                  result.Impact.RemovedNodeCount,
		RemovedTransitionGroupCount:       result.Impact.RemovedTransitionGroupCount,
		RemovedEdgeCount:                  result.Impact.RemovedEdgeCount,
		RemovedEntities:                   workflowGraphEntityReferences(result.Impact.RemovedEntities),
		NodeTaskReferenceCount:            result.Impact.NodeTaskReferenceCount,
		EdgeTaskReferenceCount:            result.Impact.EdgeTaskReferenceCount,
		ActiveCurrentNodeCount:            result.EditPolicyImpact.ActiveCurrentNodeCount,
		PendingApprovalCount:              result.EditPolicyImpact.PendingApprovalCount,
		StartNodeChangeCount:              result.EditPolicyImpact.StartNodeChangeCount,
		LastTerminalChangeCount:           result.EditPolicyImpact.LastTerminalChangeCount,
		TaskReferencedNodeKindChangeCount: result.EditPolicyImpact.TaskReferencedNodeKindChangeCount,
	}
}

func workflowGraphSaveBlockers(blockers []workflowstore.WorkflowGraphSaveBlocker) []serverapi.WorkflowGraphSaveBlocker {
	out := make([]serverapi.WorkflowGraphSaveBlocker, 0, len(blockers))
	for _, blocker := range blockers {
		out = append(out, serverapi.WorkflowGraphSaveBlocker{
			Code:             blocker.Code,
			Message:          blocker.Message,
			Count:            blocker.Count,
			AffectedEntities: workflowGraphEntityReferences(blocker.AffectedEntities),
		})
	}
	return out
}

func workflowGraphEntityReferences(references []workflowstore.WorkflowGraphEntityReference) []serverapi.WorkflowGraphEntityReference {
	return append([]serverapi.WorkflowGraphEntityReference{}, references...)
}

func commentRecord(row workflowstore.CommentRecord) serverapi.WorkflowTaskComment {
	return serverapi.WorkflowTaskComment{ID: row.ID, TaskID: string(row.TaskID), Body: row.Body, Author: row.Author, AuthorID: row.AuthorID, CreatedAtUnixMs: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func joinInputProviders(in []serverapi.WorkflowJoinInputProvider) []workflow.JoinInputProvider {
	out := make([]workflow.JoinInputProvider, 0, len(in))
	for _, provider := range in {
		out = append(out, workflow.JoinInputProvider{InputName: provider.InputName, ProviderEdgeID: workflow.EdgeID(provider.ProviderEdgeID)})
	}
	return out
}

func domainParameters(in []serverapi.WorkflowParameter) []workflow.Parameter {
	out := make([]workflow.Parameter, 0, len(in))
	for _, parameter := range in {
		out = append(out, workflow.Parameter{Key: parameter.Key, Description: parameter.Description, Purpose: workflow.ParameterPurpose(parameter.Purpose)})
	}
	return out
}
