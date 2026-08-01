package workflowsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"core/server/requestmemo"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowscript"
	"core/server/workflowstore"
	"core/server/workflowview"
	"core/server/worktree"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type Service struct {
	store                *workflowstore.Store
	readModels           ReadModels
	roleResolver         workflow.RoleResolver
	executionTargets     executionTargetInfrastructure
	taskWorktreeCleanup  taskWorktreeDeleter
	events               *workflowProjectEventBroker
	attentionFinalizer   workflowAttentionFinalizer
	questionMemo         *requestmemo.Memo[taskQuestionAnswerMemoRequest, struct{}]
	mutationPermit       *workflowexecution.MutationPermit
	currentNodeExecution interface {
		StartTaskWithExecutionTarget(context.Context, workflow.TaskID, *workflowstore.ExecutionTargetCandidate) (workflowstore.StartTaskResult, error)
		ResumeTask(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
		ApplyPendingApproval(context.Context, workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error)
		ApplyManualMove(context.Context, workflowstore.ManualMovePreparation, *workflowstore.ExecutionTargetCandidate) (workflowstore.ManualMoveResult, error)
		ManualMoveDisposition(workflow.TaskID) (workflowexecution.ManualMoveDisposition, error)
		InterruptForManualMove(context.Context, workflow.TaskID) error
		Interrupt(context.Context, workflowexecution.InterruptSelector) error
		EnsureTaskQuiescent(workflow.TaskID) error
		CompleteSessionCurrentNode(context.Context, runtimeids.SessionID, string, map[string]string, string) (workflowstore.CurrentNodeCompletionResult, error)
		CompleteIdleCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector, string, map[string]string, string) (workflowstore.CurrentNodeCompletionResult, error)
		AnswerWorkflowQuestion(context.Context, workflow.TaskID, string, askquestion.AskQuestionResponse, error) error
	}
}

type initiatingActionTargetDecision struct {
	candidate         *workflowstore.ExecutionTargetCandidate
	selectionRequired *serverapi.WorkflowExecutionTargetSelectionRequirement
}

type initiatingActionTargetPreflight struct {
	context   workflowstore.TaskExecutionTargetContext
	selection workflow.ExecutionTargetSelection
	explicit  bool
}

type initiatingActionRequest struct {
	taskID                  workflow.TaskID
	setupOperationID        serverapi.WorktreeSetupOperationID
	requiresExecutionTarget bool
	targetPreflight         initiatingActionTargetPreflight
	afterTargetResolution   func() error
}

type initiatingActionResult[T any] struct {
	applied           *T
	selectionRequired *serverapi.WorkflowExecutionTargetSelectionRequirement
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

type executionTargetInfrastructure interface {
	ResolveExecutionTarget(context.Context, ExecutionTargetResolveRequest) (workflowstore.ExecutionTargetSnapshot, error)
	MaterializeExecutionTarget(context.Context, ExecutionTargetMaterializeRequest) (workflowstore.ManagedExecutionRoot, error)
	RestoreExecutionTarget(context.Context, ExecutionTargetRestoreRequest) error
}

type ExecutionTargetResolveRequest struct {
	SourceWorkspaceRoot string
	Selection           workflow.ExecutionTargetSelection
}

type ExecutionTargetMaterializeRequest struct {
	TaskID           workflow.TaskID
	SetupOperationID serverapi.WorktreeSetupOperationID
	Snapshot         workflowstore.ExecutionTargetSnapshot
}

type ExecutionTargetRestoreRequest struct {
	TaskID           workflow.TaskID
	SetupOperationID serverapi.WorktreeSetupOperationID
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

const (
	workflowAttentionFinalizationTimeout = 5 * time.Second
)

type taskQuestionAnswerMemoRequest struct {
	TaskID               string
	AskID                string
	ErrorMessage         string
	Answer               string
	SelectedOptionNumber *int
	FreeformAnswer       string
	ApprovalDecision     clientui.ApprovalDecision
	ApprovalCommentary   string
}

type Option func(*Service)

func WithCurrentNodeExecution(execution interface {
	StartTaskWithExecutionTarget(context.Context, workflow.TaskID, *workflowstore.ExecutionTargetCandidate) (workflowstore.StartTaskResult, error)
	ResumeTask(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error)
	ApplyPendingApproval(context.Context, workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error)
	ApplyManualMove(context.Context, workflowstore.ManualMovePreparation, *workflowstore.ExecutionTargetCandidate) (workflowstore.ManualMoveResult, error)
	ManualMoveDisposition(workflow.TaskID) (workflowexecution.ManualMoveDisposition, error)
	InterruptForManualMove(context.Context, workflow.TaskID) error
	Interrupt(context.Context, workflowexecution.InterruptSelector) error
	EnsureTaskQuiescent(workflow.TaskID) error
	CompleteSessionCurrentNode(context.Context, runtimeids.SessionID, string, map[string]string, string) (workflowstore.CurrentNodeCompletionResult, error)
	CompleteIdleCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector, string, map[string]string, string) (workflowstore.CurrentNodeCompletionResult, error)
	AnswerWorkflowQuestion(context.Context, workflow.TaskID, string, askquestion.AskQuestionResponse, error) error
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

func New(store *workflowstore.Store, readModels ReadModels, roleResolver workflow.RoleResolver, mutationPermit *workflowexecution.MutationPermit, opts ...Option) (*Service, error) {
	if store == nil {
		return nil, errors.New("workflow store is required")
	}
	if mutationPermit == nil {
		return nil, errors.New("workflow mutation permit is required")
	}
	if err := readModels.validate(); err != nil {
		return nil, err
	}
	events := newWorkflowProjectEventBroker()
	store.SetWorkflowEventPublisher(events)
	service := &Service{store: store, readModels: readModels, roleResolver: roleResolver, events: events, questionMemo: requestmemo.New[taskQuestionAnswerMemoRequest, struct{}](), mutationPermit: mutationPermit}
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
	if err := s.store.UpdateWorkflowInfo(ctx, req.WorkflowID, req.Name, req.Description); err != nil {
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

func (s *Service) AddWorkflowNode(ctx context.Context, req serverapi.WorkflowNodeAddRequest) (serverapi.WorkflowNodeAddResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowNodeAddResponse{}, err
	}
	revision, err := runWorkflowGraphMutation(ctx, s, req.WorkflowID, func(ctx context.Context) (int64, error) {
		return s.store.AddNode(ctx, workflowstore.NodeRecord{ID: workflow.NodeID(req.NodeID), WorkflowID: req.WorkflowID, Key: workflow.ModelKey(req.Key), Kind: workflow.NodeKind(req.Kind), DisplayName: req.DisplayName, GroupKey: req.GroupKey, SubagentRole: req.SubagentRole, PromptTemplate: req.PromptTemplate, CompletionMode: req.CompletionMode, ScriptPath: optionalStringValue(req.ScriptPath), InputFields: inputFields(req.InputFields), JoinInputProviders: joinInputProviders(req.JoinInputProviders)})
	})
	if err != nil {
		return serverapi.WorkflowNodeAddResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflow, serverapi.WorkflowProjectEventActionNodeAdded, req.NodeID)
	return serverapi.WorkflowNodeAddResponse{Version: revision}, nil
}

func (s *Service) UpdateWorkflowNode(ctx context.Context, req serverapi.WorkflowNodeUpdateRequest) (serverapi.WorkflowNodeUpdateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowNodeUpdateResponse{}, err
	}
	revision, err := runWorkflowGraphMutation(ctx, s, req.WorkflowID, func(ctx context.Context) (int64, error) {
		return s.store.UpdateNode(ctx, workflowstore.NodeRecord{ID: workflow.NodeID(req.NodeID), WorkflowID: req.WorkflowID, Key: workflow.ModelKey(req.Key), Kind: workflow.NodeKind(req.Kind), DisplayName: req.DisplayName, GroupKey: req.GroupKey, SubagentRole: req.SubagentRole, PromptTemplate: req.PromptTemplate, CompletionMode: req.CompletionMode, ScriptPath: optionalStringValue(req.ScriptPath), InputFields: inputFields(req.InputFields), JoinInputProviders: joinInputProviders(req.JoinInputProviders)})
	})
	if err != nil {
		return serverapi.WorkflowNodeUpdateResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflow, serverapi.WorkflowProjectEventActionNodeUpdated, req.NodeID)
	return serverapi.WorkflowNodeUpdateResponse{Version: revision}, nil
}

func (s *Service) AddWorkflowNodeGroup(ctx context.Context, req serverapi.WorkflowNodeGroupAddRequest) (serverapi.WorkflowNodeGroupResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowNodeGroupResponse{}, err
	}
	var group workflowstore.NodeGroupRecord
	revision, err := runWorkflowGraphMutation(ctx, s, req.WorkflowID, func(ctx context.Context) (int64, error) {
		var storeRevision int64
		var err error
		group, storeRevision, err = s.store.AddNodeGroup(ctx, workflowstore.NodeGroupRecord{ID: req.GroupID, WorkflowID: req.WorkflowID, Key: workflow.ModelKey(req.GroupKey), DisplayName: req.DisplayName, SortOrder: int64(req.SortOrder)})
		return storeRevision, err
	})
	if err != nil {
		return serverapi.WorkflowNodeGroupResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflow, serverapi.WorkflowProjectEventActionNodeGroupAdded, group.ID)
	return serverapi.WorkflowNodeGroupResponse{Group: workflowNodeGroup(group), Version: revision}, nil
}

func (s *Service) UpdateWorkflowNodeGroup(ctx context.Context, req serverapi.WorkflowNodeGroupUpdateRequest) (serverapi.WorkflowNodeGroupResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowNodeGroupResponse{}, err
	}
	var group workflowstore.NodeGroupRecord
	revision, err := runWorkflowGraphMutation(ctx, s, req.WorkflowID, func(ctx context.Context) (int64, error) {
		var storeRevision int64
		var err error
		group, storeRevision, err = s.store.UpdateNodeGroup(ctx, workflowstore.NodeGroupRecord{ID: req.GroupID, WorkflowID: req.WorkflowID, Key: workflow.ModelKey(req.GroupKey), DisplayName: req.DisplayName, SortOrder: int64(req.SortOrder)})
		return storeRevision, err
	})
	if err != nil {
		return serverapi.WorkflowNodeGroupResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflow, serverapi.WorkflowProjectEventActionNodeGroupUpdated, group.ID)
	return serverapi.WorkflowNodeGroupResponse{Group: workflowNodeGroup(group), Version: revision}, nil
}

func (s *Service) DeleteWorkflowNodeGroup(ctx context.Context, req serverapi.WorkflowNodeGroupDeleteRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if _, err := runWorkflowGraphMutation(ctx, s, req.WorkflowID, func(ctx context.Context) (struct{}, error) {
		_, err := s.store.DeleteNodeGroup(ctx, req.WorkflowID, req.GroupID)
		return struct{}{}, err
	}); err != nil {
		return err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflow, serverapi.WorkflowProjectEventActionNodeGroupDeleted, req.GroupID)
	return nil
}

func (s *Service) AddWorkflowTransitionGroup(ctx context.Context, req serverapi.WorkflowTransitionGroupAddRequest) (serverapi.WorkflowTransitionGroupAddResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTransitionGroupAddResponse{}, err
	}
	revision, err := runWorkflowGraphMutation(ctx, s, req.WorkflowID, func(ctx context.Context) (int64, error) {
		return s.store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID(req.GroupID), WorkflowID: req.WorkflowID, SourceNodeID: workflow.NodeID(req.SourceNodeID), TransitionID: workflow.TransitionID(req.TransitionID), DisplayName: req.DisplayName, Description: req.Description})
	})
	if err != nil {
		return serverapi.WorkflowTransitionGroupAddResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflow, serverapi.WorkflowProjectEventActionTransitionGroupAdded, req.GroupID)
	return serverapi.WorkflowTransitionGroupAddResponse{Version: revision}, nil
}

func (s *Service) UpdateWorkflowTransitionGroup(ctx context.Context, req serverapi.WorkflowTransitionGroupUpdateRequest) (serverapi.WorkflowTransitionGroupUpdateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTransitionGroupUpdateResponse{}, err
	}
	revision, err := runWorkflowGraphMutation(ctx, s, req.WorkflowID, func(ctx context.Context) (int64, error) {
		return s.store.UpdateTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID(req.GroupID), WorkflowID: req.WorkflowID, SourceNodeID: workflow.NodeID(req.SourceNodeID), TransitionID: workflow.TransitionID(req.TransitionID), DisplayName: req.DisplayName, Description: req.Description})
	})
	if err != nil {
		return serverapi.WorkflowTransitionGroupUpdateResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflow, serverapi.WorkflowProjectEventActionTransitionGroupUpdated, req.GroupID)
	return serverapi.WorkflowTransitionGroupUpdateResponse{Version: revision}, nil
}

func (s *Service) AddWorkflowEdge(ctx context.Context, req serverapi.WorkflowEdgeAddRequest) (serverapi.WorkflowEdgeAddResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowEdgeAddResponse{}, err
	}
	revision, err := runWorkflowGraphMutation(ctx, s, req.WorkflowID, func(ctx context.Context) (int64, error) {
		return s.store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID(req.EdgeID), WorkflowID: req.WorkflowID, TransitionGroupID: workflow.TransitionGroupID(req.TransitionGroupID), Key: workflow.ModelKey(req.Key), TargetNodeID: workflow.NodeID(req.TargetNodeID), RequiresApproval: req.RequiresApproval, ContextMode: workflow.ContextMode(req.ContextMode), ContextSource: workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(req.ContextSource.Kind), NodeKey: workflow.ModelKey(req.ContextSource.NodeKey)}), PromptTemplate: req.PromptTemplate, Parameters: domainParameters(req.Parameters)})
	})
	if err != nil {
		return serverapi.WorkflowEdgeAddResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflow, serverapi.WorkflowProjectEventActionEdgeAdded, req.EdgeID)
	return serverapi.WorkflowEdgeAddResponse{Version: revision}, nil
}

func (s *Service) UpdateWorkflowEdge(ctx context.Context, req serverapi.WorkflowEdgeUpdateRequest) (serverapi.WorkflowEdgeUpdateResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowEdgeUpdateResponse{}, err
	}
	revision, err := runWorkflowGraphMutation(ctx, s, req.WorkflowID, func(ctx context.Context) (int64, error) {
		return s.store.UpdateEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID(req.EdgeID), WorkflowID: req.WorkflowID, TransitionGroupID: workflow.TransitionGroupID(req.TransitionGroupID), Key: workflow.ModelKey(req.Key), TargetNodeID: workflow.NodeID(req.TargetNodeID), RequiresApproval: req.RequiresApproval, ContextMode: workflow.ContextMode(req.ContextMode), ContextSource: workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(req.ContextSource.Kind), NodeKey: workflow.ModelKey(req.ContextSource.NodeKey)}), PromptTemplate: req.PromptTemplate, Parameters: domainParameters(req.Parameters)})
	})
	if err != nil {
		return serverapi.WorkflowEdgeUpdateResponse{}, err
	}
	s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflow, serverapi.WorkflowProjectEventActionEdgeUpdated, req.EdgeID)
	return serverapi.WorkflowEdgeUpdateResponse{Version: revision}, nil
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
	return workflowexecution.RunMutation(ctx, s.mutationPermit, func(ctx context.Context) (serverapi.WorkflowDeleteResponse, error) {
		if err := s.ensureWorkflowTasksQuiescent(ctx, req.WorkflowID); err != nil {
			return serverapi.WorkflowDeleteResponse{}, err
		}
		return s.deleteWorkflow(ctx, req)
	})
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

func runWorkflowGraphMutation[T any](ctx context.Context, service *Service, workflowID runtimeids.WorkflowID, mutation func(context.Context) (T, error)) (T, error) {
	var result T
	if service == nil {
		return result, errors.New("workflow service is required")
	}
	return workflowexecution.RunMutation(ctx, service.mutationPermit, mutation)
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
		DerivedWiring: workflowview.DerivedWiring(def),
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
		DerivedWiring: workflowview.DerivedWiring(def),
	}, nil
}

func (s *Service) PreviewWorkflowGraphSave(ctx context.Context, req serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowGraphSavePreviewResponse{}, err
	}
	result, err := s.store.PreviewWorkflowGraphSave(ctx, workflowGraphStoreSaveRequest(req.WorkflowID, req.ExpectedVersion, req.Metadata, req.Graph, nil))
	if err != nil {
		return serverapi.WorkflowGraphSavePreviewResponse{}, err
	}
	return workflowGraphSavePreviewResponse(result, workflowGraphSaveValidationResponses(result)), nil
}

func (s *Service) SaveWorkflowGraph(ctx context.Context, req serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowGraphSaveResponse{}, err
	}
	result, err := runWorkflowGraphMutation(ctx, s, req.WorkflowID, func(ctx context.Context) (workflowstore.WorkflowGraphSaveResult, error) {
		return s.store.SaveWorkflowGraph(ctx, workflowGraphStoreSaveRequest(req.WorkflowID, req.ExpectedVersion, req.Metadata, req.Graph, req.Confirmation))
	})
	if err != nil {
		return serverapi.WorkflowGraphSaveResponse{}, err
	}
	resp := workflowGraphSaveResponse(result, workflowGraphSaveValidationResponses(result))
	if !result.Saved {
		return resp, nil
	}
	definition, _ := workflowview.ProjectDefinition(result.Definition, result.Record)
	resp.Definition = &definition
	resp.CurrentVersion = result.Record.Version
	if result.Changed {
		s.publishLinkedWorkflowEvent(ctx, req.WorkflowID, serverapi.WorkflowProjectEventResourceWorkflow, serverapi.WorkflowProjectEventActionGraphSaved, req.WorkflowID.String())
	}
	return resp, nil
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
	if req.DependencyIntent != nil {
		intent := workflow.TaskDependencyCreateIntent{
			RelatedTaskID: workflow.TaskID(req.DependencyIntent.RelatedTaskID),
		}
		switch req.DependencyIntent.NewTaskRole {
		case serverapi.WorkflowTaskDependencyRoleBlocker:
			intent.NewTaskRole = workflow.TaskDependencyRoleBlocker
		case serverapi.WorkflowTaskDependencyRoleBlocked:
			intent.NewTaskRole = workflow.TaskDependencyRoleBlocked
		}
		taskRequest.DependencyIntent = &intent
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
	if req.DependencyIntent != nil {
		relatedID := req.DependencyIntent.RelatedTaskID
		s.publishProjectWorkflowEvent(ctx, task.ProjectID, task.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionDependenciesChanged, string(task.ID), relatedID)
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
	preflight, err := workflowexecution.RunMutation(ctx, s.mutationPermit, func(ctx context.Context) (initiatingActionPreflight, error) {
		taskID := workflow.TaskID(req.TaskID)
		if err := s.currentNodeExecution.EnsureTaskQuiescent(taskID); err != nil {
			return initiatingActionPreflight{}, err
		}
		if err := s.store.ValidateTaskStart(ctx, taskID); err != nil {
			return initiatingActionPreflight{}, err
		}
		target, err := s.preflightInitiatingActionTarget(ctx, taskID, req.ExecutionTarget)
		if err != nil {
			return initiatingActionPreflight{}, err
		}
		if req.ProceedDespiteDependencies {
			return initiatingActionPreflight{target: target}, nil
		}
		count, err := s.readModels.TaskDependencies.CountUnsatisfiedBlockers(ctx, req.TaskID)
		if err != nil {
			return initiatingActionPreflight{}, err
		}
		return initiatingActionPreflight{unsatisfiedDependencyCount: count, target: target}, nil
	})
	if err != nil {
		return serverapi.WorkflowTaskStartResponse{}, err
	}
	if preflight.unsatisfiedDependencyCount > 0 {
		count := preflight.unsatisfiedDependencyCount
		return serverapi.WorkflowTaskStartResponse{
			Outcome:                    serverapi.WorkflowTaskActionOutcomeDependencyConfirmationRequired,
			UnsatisfiedDependencyCount: &count,
		}, nil
	}
	coordinated, err := coordinateInitiatingAction(ctx, s, initiatingActionRequest{
		taskID:                  workflow.TaskID(req.TaskID),
		setupOperationID:        req.SetupOperationID,
		requiresExecutionTarget: true,
		targetPreflight:         preflight.target,
	}, func(candidate *workflowstore.ExecutionTargetCandidate) (*workflowstore.StartTaskResult, error) {
		started, err := s.currentNodeExecution.StartTaskWithExecutionTarget(ctx, workflow.TaskID(req.TaskID), candidate)
		return &started, err
	})
	if err != nil {
		return serverapi.WorkflowTaskStartResponse{}, err
	}
	if coordinated.selectionRequired != nil {
		return serverapi.WorkflowTaskStartResponse{
			Outcome:           serverapi.WorkflowTaskActionOutcomeSelectionRequired,
			SelectionRequired: coordinated.selectionRequired,
		}, nil
	}
	if coordinated.applied == nil {
		return serverapi.WorkflowTaskStartResponse{}, errors.New("coordinated task start returned no applied result")
	}
	started := *coordinated.applied
	if len(started.Mutation.Created) != 1 {
		return serverapi.WorkflowTaskStartResponse{}, errors.New("task start did not create exactly one current node")
	}
	if detail, detailErr := s.readModels.TaskDetail.GetTask(ctx, req.TaskID); detailErr == nil {
		s.publishProjectWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionStarted, req.TaskID)
	}
	return serverapi.WorkflowTaskStartResponse{
		Outcome: serverapi.WorkflowTaskActionOutcomeApplied,
		Applied: &serverapi.WorkflowTaskStartApplied{
			CurrentNodes: workflowCurrentNodes(started.Mutation.Created),
		},
	}, nil
}

func coordinateInitiatingAction[T any](ctx context.Context, service *Service, req initiatingActionRequest, apply func(*workflowstore.ExecutionTargetCandidate) (*T, error)) (initiatingActionResult[T], error) {
	var candidate *workflowstore.ExecutionTargetCandidate
	if req.requiresExecutionTarget {
		targetDecision, err := service.initiatingActionTarget(ctx, req.taskID, req.setupOperationID, req.targetPreflight)
		if err != nil {
			return initiatingActionResult[T]{}, err
		}
		if targetDecision.selectionRequired != nil {
			return initiatingActionResult[T]{selectionRequired: targetDecision.selectionRequired}, nil
		}
		candidate = targetDecision.candidate
	}
	if req.afterTargetResolution != nil {
		if err := req.afterTargetResolution(); err != nil {
			return initiatingActionResult[T]{}, err
		}
	}
	applied, err := apply(candidate)
	if err != nil {
		// A mutation may commit before its post-commit lifecycle work fails.
		// Preserve that result so the operation owner can publish and finalize
		// the durable mutation instead of reporting it as unapplied.
		if applied != nil {
			return initiatingActionResult[T]{applied: applied}, err
		}
		return initiatingActionResult[T]{}, err
	}
	return initiatingActionResult[T]{applied: applied}, nil
}

func (s *Service) preflightInitiatingActionTarget(ctx context.Context, taskID workflow.TaskID, explicit *serverapi.WorkflowExecutionTargetSelection) (initiatingActionTargetPreflight, error) {
	targetContext, err := s.store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		return initiatingActionTargetPreflight{}, err
	}
	if targetContext.Task.ExecutionTarget != nil {
		if explicit != nil {
			return initiatingActionTargetPreflight{}, workflowstore.ErrExecutionTargetAlreadyLocked
		}
		if targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone {
			if s.executionTargets == nil {
				return initiatingActionTargetPreflight{}, errExecutionTargetInfrastructureRequired
			}
		}
		return initiatingActionTargetPreflight{context: targetContext}, nil
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
	if selection.Mode != workflow.ExecutionTargetModeNone {
		if s.executionTargets == nil {
			return initiatingActionTargetPreflight{}, errExecutionTargetInfrastructureRequired
		}
	}
	return initiatingActionTargetPreflight{
		context:   targetContext,
		selection: selection,
		explicit:  explicit != nil,
	}, nil
}

func (s *Service) initiatingActionTarget(ctx context.Context, taskID workflow.TaskID, setupOperationID serverapi.WorktreeSetupOperationID, preflight initiatingActionTargetPreflight) (initiatingActionTargetDecision, error) {
	targetContext := preflight.context
	if targetContext.Task.ExecutionTarget != nil {
		if targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone {
			if err := s.executionTargets.RestoreExecutionTarget(ctx, ExecutionTargetRestoreRequest{
				TaskID:           taskID,
				SetupOperationID: setupOperationID,
			}); err != nil {
				return initiatingActionTargetDecision{}, workflowLockedExecutionTargetError(err)
			}
		}
		return initiatingActionTargetDecision{}, nil
	}
	if !preflight.explicit && targetContext.Policy.Mode == workflow.ExecutionTargetModeAskOnFirstExecution {
		return initiatingActionTargetDecision{
			selectionRequired: &serverapi.WorkflowExecutionTargetSelectionRequirement{
				Reason: serverapi.WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection,
			},
		}, nil
	}
	selection := preflight.selection
	if selection.Mode != workflow.ExecutionTargetModeNone {
		snapshot, err := s.executionTargets.ResolveExecutionTarget(ctx, ExecutionTargetResolveRequest{
			SourceWorkspaceRoot: targetContext.SourceWorkspaceRoot,
			Selection:           selection,
		})
		if err != nil {
			if !preflight.explicit {
				if requirement, ok := configuredTargetSelectionRequirement(selection, err); ok {
					return initiatingActionTargetDecision{selectionRequired: requirement}, nil
				}
			}
			if selection.Mode == workflow.ExecutionTargetModeCustomRef {
				if resolutionErr, ok := explicitExecutionTargetResolutionError(err); ok {
					return initiatingActionTargetDecision{}, resolutionErr
				}
			}
			return initiatingActionTargetDecision{}, err
		}
		if snapshot.Mode != selection.Mode {
			return initiatingActionTargetDecision{}, errors.New("resolved execution target mode does not match selection")
		}
		if err := snapshot.Validate(); err != nil {
			return initiatingActionTargetDecision{}, err
		}
		managedRoot, err := s.executionTargets.MaterializeExecutionTarget(ctx, ExecutionTargetMaterializeRequest{
			TaskID:           taskID,
			SetupOperationID: setupOperationID,
			Snapshot:         snapshot,
		})
		if err != nil {
			return initiatingActionTargetDecision{}, err
		}
		return initiatingActionTargetDecision{
			candidate: &workflowstore.ExecutionTargetCandidate{
				Snapshot: snapshot,
				Root: workflowstore.ExecutionRoot{
					SourceWorkspaceID:   targetContext.SourceWorkspaceID,
					SourceWorkspaceRoot: targetContext.SourceWorkspaceRoot,
					Managed:             &managedRoot,
				},
			},
		}, nil
	}
	return initiatingActionTargetDecision{
		candidate: &workflowstore.ExecutionTargetCandidate{
			Snapshot: workflowstore.ExecutionTargetSnapshot{
				Mode:       workflow.ExecutionTargetModeNone,
				Provenance: workflowstore.ExecutionTargetProvenanceResolved,
			},
			Root: workflowstore.ExecutionRoot{
				SourceWorkspaceID:   targetContext.SourceWorkspaceID,
				SourceWorkspaceRoot: targetContext.SourceWorkspaceRoot,
			},
		},
	}, nil
}

func configuredTargetSelectionRequirement(selection workflow.ExecutionTargetSelection, err error) (*serverapi.WorkflowExecutionTargetSelectionRequirement, bool) {
	cause, ok := executionTargetUnavailableCause(err)
	if !ok {
		return nil, false
	}
	configured := &serverapi.WorkflowExecutionTargetConfiguredTarget{
		Mode: serverapi.WorkflowExecutionTargetMode(selection.Mode),
	}
	if selection.Mode == workflow.ExecutionTargetModeCustomRef {
		configured.RequestedRef = selection.CustomRef
	}
	return &serverapi.WorkflowExecutionTargetSelectionRequirement{
		Reason:           serverapi.WorkflowExecutionTargetSelectionReasonConfiguredTargetUnavailable,
		ConfiguredTarget: configured,
		UnavailableCause: cause,
	}, true
}

func executionTargetUnavailableCause(err error) (serverapi.WorkflowExecutionTargetUnavailableCause, bool) {
	var revisionErr *worktree.GitRevisionResolutionError
	if errors.As(err, &revisionErr) {
		switch revisionErr.Kind {
		case worktree.GitRevisionResolutionErrorInvalidRevision:
			return serverapi.WorkflowExecutionTargetUnavailableCauseInvalidRevision, true
		case worktree.GitRevisionResolutionErrorNonCommit:
			return serverapi.WorkflowExecutionTargetUnavailableCauseNonCommit, true
		case worktree.GitRevisionResolutionErrorGitFailure:
			return serverapi.WorkflowExecutionTargetUnavailableCauseGitFailure, true
		}
	}
	var defaultBranchErr *worktree.GitDefaultBranchResolutionError
	if errors.As(err, &defaultBranchErr) {
		switch defaultBranchErr.Kind {
		case worktree.GitDefaultBranchResolutionErrorMissing:
			return serverapi.WorkflowExecutionTargetUnavailableCauseDefaultBranchMissing, true
		case worktree.GitDefaultBranchResolutionErrorAmbiguous:
			return serverapi.WorkflowExecutionTargetUnavailableCauseDefaultBranchAmbiguous, true
		case worktree.GitDefaultBranchResolutionErrorGitFailure:
			return serverapi.WorkflowExecutionTargetUnavailableCauseGitFailure, true
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
	if detail, detailErr := s.readModels.TaskDetail.GetTask(ctx, req.TaskID); detailErr == nil {
		s.publishProjectWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionInterrupted, req.TaskID)
	}
	return serverapi.WorkflowTaskInterruptResponse{}, nil
}

func workflowCurrentNodes(nodes []workflow.CurrentNode) []serverapi.WorkflowTaskCurrentNode {
	out := make([]serverapi.WorkflowTaskCurrentNode, 0, len(nodes))
	for _, currentNode := range nodes {
		item := serverapi.WorkflowTaskCurrentNode{NodeID: string(currentNode.Reference.NodeID)}
		if branchKey, branchScoped := currentNode.Reference.TransitionBranchKey(); branchScoped {
			value := string(branchKey)
			item.TransitionBranchKey = &value
		}
		if currentNode.SessionID != nil {
			value := currentNode.SessionID.String()
			item.SessionID = &value
		}
		out = append(out, item)
	}
	return out
}

func (s *Service) ResumeWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error) {
	return s.resumeWorkflowTask(ctx, req)
}

func (s *Service) resumeWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskResumeResponse{}, err
	}
	if s.currentNodeExecution == nil {
		return serverapi.WorkflowTaskResumeResponse{}, errors.New("current node workflow execution is required")
	}
	resumed, err := s.currentNodeExecution.ResumeTask(ctx, workflow.TaskID(req.TaskID))
	if err != nil {
		return serverapi.WorkflowTaskResumeResponse{}, err
	}
	if detail, detailErr := s.readModels.TaskDetail.GetTask(ctx, req.TaskID); detailErr == nil {
		s.publishProjectWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionResumed, req.TaskID)
	}
	return serverapi.WorkflowTaskResumeResponse{CurrentNodes: workflowCurrentNodes(resumed)}, nil
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
	approved, err := s.currentNodeExecution.ApplyPendingApproval(ctx, approvalID)
	if err != nil {
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
			CurrentNodes: workflowCurrentNodes(approved.Mutation.Created),
		},
	}, nil
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
				CurrentNodes: workflowCurrentNodes(preview.CurrentNodes),
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
	if prepared.IsNoOp() {
		return serverapi.WorkflowTaskMoveResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeNoOp,
			NoOp: &serverapi.WorkflowTaskMoveNoOp{
				CurrentNodes: workflowCurrentNodes(prepared.CurrentNodes()),
			},
		}, nil
	}
	if prepared.RequiresExecutionTarget() {
		if err := req.SetupOperationID.Validate(); err != nil {
			return serverapi.WorkflowTaskMoveResponse{}, err
		}
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
		targetPreflight, err = s.preflightInitiatingActionTarget(ctx, moveRequest.TaskID, req.ExecutionTarget)
		if err != nil {
			return serverapi.WorkflowTaskMoveResponse{}, err
		}
	}
	coordinated, err := coordinateInitiatingAction(ctx, s, initiatingActionRequest{
		taskID:                  moveRequest.TaskID,
		setupOperationID:        req.SetupOperationID,
		requiresExecutionTarget: prepared.RequiresExecutionTarget(),
		targetPreflight:         targetPreflight,
		afterTargetResolution: func() error {
			return s.currentNodeExecution.InterruptForManualMove(ctx, moveRequest.TaskID)
		},
	}, func(candidate *workflowstore.ExecutionTargetCandidate) (*workflowstore.ManualMoveResult, error) {
		moved, err := s.currentNodeExecution.ApplyManualMove(ctx, prepared, candidate)
		if err != nil && moved.Outcome != workflowstore.ManualMoveResultOutcomeApplied &&
			moved.Outcome != workflowstore.ManualMoveResultOutcomeNoOp {
			return nil, err
		}
		return &moved, err
	})
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
	moved := *coordinated.applied
	if err := moved.Validate(); err != nil {
		return serverapi.WorkflowTaskMoveResponse{}, err
	}
	if moved.Outcome == workflowstore.ManualMoveResultOutcomeNoOp {
		return serverapi.WorkflowTaskMoveResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeNoOp,
			NoOp: &serverapi.WorkflowTaskMoveNoOp{
				CurrentNodes: workflowCurrentNodes(moved.CurrentNodes),
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
			CurrentNodes: workflowCurrentNodes(moved.Mutation.Created),
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
	if err != nil {
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
		CurrentNodes: workflowCurrentNodes(completed.Mutation.Created),
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
	if detail, detailErr := s.readModels.TaskDetail.GetTask(ctx, string(taskID)); detailErr == nil {
		s.publishProjectWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionCompleted, string(taskID))
	}
	return response, nil
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
	return s.mutationPermit.Run(ctx, func(ctx context.Context) error {
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

func (s *Service) AnswerWorkflowTaskQuestion(ctx context.Context, req serverapi.WorkflowTaskQuestionAnswerRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if s == nil || s.currentNodeExecution == nil {
		return errors.New("current node workflow execution is required")
	}
	memoReq := taskQuestionAnswerMemoRequest{TaskID: req.TaskID, AskID: req.AskID, ErrorMessage: req.ErrorMessage, Answer: req.Answer, SelectedOptionNumber: textutil.Pointer(req.SelectedOptionNumber), FreeformAnswer: req.FreeformAnswer}
	if req.Approval != nil {
		memoReq.ApprovalDecision = req.Approval.Decision
		memoReq.ApprovalCommentary = req.Approval.Commentary
	}
	_, err := s.questionMemo.Do(ctx, req.ClientRequestID, memoReq, sameTaskQuestionAnswerMemoRequest, func(ctx context.Context) (struct{}, error) {
		err := s.answerWorkflowTaskQuestion(ctx, req)
		return struct{}{}, err
	})
	return err
}

func (s *Service) answerWorkflowTaskQuestion(ctx context.Context, req serverapi.WorkflowTaskQuestionAnswerRequest) error {
	var (
		response  askquestion.AskQuestionResponse
		submitErr error
	)
	if strings.TrimSpace(req.ErrorMessage) != "" {
		response = askquestion.AskQuestionResponse{RequestID: req.AskID}
		submitErr = errors.New(req.ErrorMessage)
	} else {
		response = askquestion.AskQuestionResponse{RequestID: req.AskID, Answer: req.Answer, SelectedOptionNumber: textutil.Pointer(req.SelectedOptionNumber), FreeformAnswer: req.FreeformAnswer}
		if req.Approval != nil {
			response = askquestion.AskQuestionResponse{
				RequestID: req.AskID,
				Approval: &askquestion.AskQuestionApprovalPayload{
					Decision:   askquestion.AskQuestionApprovalDecision(req.Approval.Decision),
					Commentary: req.Approval.Commentary,
				},
			}
		}
	}
	if err := s.currentNodeExecution.AnswerWorkflowQuestion(ctx, workflow.TaskID(req.TaskID), req.AskID, response, submitErr); err != nil {
		if errors.Is(err, sessionruntime.ErrWorkflowPromptAmbiguous) {
			return serverapi.WorkflowTaskQuestionSelectorAmbiguousError{Message: err.Error()}
		}
		return err
	}
	if detail, err := s.readModels.TaskDetail.GetTask(ctx, req.TaskID); err == nil {
		s.publishProjectWorkflowEvent(ctx, detail.Summary.ProjectID, detail.Summary.WorkflowID, serverapi.WorkflowProjectEventResourceTask, serverapi.WorkflowProjectEventActionQuestionAnswered, req.TaskID, req.AskID)
	}
	return nil
}

func sameTaskQuestionAnswerMemoRequest(a taskQuestionAnswerMemoRequest, b taskQuestionAnswerMemoRequest) bool {
	return a.TaskID == b.TaskID &&
		a.AskID == b.AskID &&
		a.ErrorMessage == b.ErrorMessage &&
		a.Answer == b.Answer &&
		textutil.EqualOptional(a.SelectedOptionNumber, b.SelectedOptionNumber) &&
		a.FreeformAnswer == b.FreeformAnswer &&
		a.ApprovalDecision == b.ApprovalDecision &&
		a.ApprovalCommentary == b.ApprovalCommentary
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

func (s *Service) ListWorkflowTaskComments(ctx context.Context, req serverapi.WorkflowTaskCommentListRequest) (serverapi.WorkflowTaskCommentListResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.WorkflowTaskCommentListResponse{}, err
	}
	window, err := serverapi.ResolveWorkflowOffsetWindow(req.Offset, req.Limit)
	if err != nil {
		return serverapi.WorkflowTaskCommentListResponse{}, err
	}
	comments, err := s.store.ListCommentsPage(ctx, workflow.TaskID(req.TaskID), window.Offset, window.Limit+1)
	if err != nil {
		return serverapi.WorkflowTaskCommentListResponse{}, err
	}
	var nextOffset *int
	if len(comments) > window.Limit {
		comments = comments[:window.Limit]
		value := window.Offset + len(comments)
		nextOffset = &value
	}
	out := make([]serverapi.WorkflowTaskComment, 0, len(comments))
	for _, comment := range comments {
		out = append(out, commentRecord(comment))
	}
	return serverapi.WorkflowTaskCommentListResponse{Comments: out, NextOffset: nextOffset}, nil
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

func (s *Service) ListWorkflowTaskActivity(ctx context.Context, req serverapi.WorkflowTaskActivityListRequest) (serverapi.WorkflowTaskActivityListResponse, error) {
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

func workflowNodeGroup(row workflowstore.NodeGroupRecord) serverapi.WorkflowNodeGroup {
	return serverapi.WorkflowNodeGroup{GroupID: row.ID, WorkflowID: row.WorkflowID, GroupKey: string(row.Key), DisplayName: row.DisplayName, SortOrder: int(row.SortOrder)}
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
	return serverapi.WorkflowValidationError{
		Code:          diagnostic.Code,
		Message:       diagnostic.Message,
		WorkflowID:    &workflowIDValue,
		NodeID:        string(nodeID),
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
	displayName := current.DisplayName
	if metadata != nil {
		displayName = metadata.Name
	}
	targetPolicy := current.ExecutionTargetPolicy
	if metadata != nil && metadata.ExecutionTargetPolicy != nil {
		targetPolicy = workflowExecutionTargetPolicyFromAPI(*metadata.ExecutionTargetPolicy)
	}
	def := workflow.Definition{ID: workflowID, DisplayName: displayName, ExecutionTargetPolicy: targetPolicy}
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
		if strings.TrimSpace(node.GroupID) != "" {
			groupMemberIDs[node.GroupID] = append(groupMemberIDs[node.GroupID], workflow.NodeID(node.ID))
		}
		workflowNode, err := workflow.NewNode(
			workflow.NodeIdentity{
				WorkflowID:  workflowID,
				ID:          workflow.NodeID(node.ID),
				Key:         workflow.ModelKey(node.Key),
				DisplayName: node.DisplayName,
				GroupID:     node.GroupID,
			},
			workflow.NodeKind(node.Kind),
			workflow.NodeFields{
				SubagentRole:   node.SubagentRole,
				PromptTemplate: node.PromptTemplate,
				CompletionMode: node.CompletionMode,
				ScriptPath: func() workflow.OptionalScriptPath {
					if scriptPath, ok := workflow.PresentScriptPath(optionalStringValue(node.ScriptPath)); ok {
						return scriptPath
					}
					return workflow.AbsentScriptPath()
				}(),
				InputFields:        inputFields(node.InputFields),
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

func workflowGraphStoreSaveRequest(workflowID runtimeids.WorkflowID, expectedVersion int64, metadata *serverapi.WorkflowGraphMetadata, graph serverapi.WorkflowGraphDraft, confirmation *serverapi.WorkflowGraphSaveConfirmation) workflowstore.WorkflowGraphSaveRequest {
	req := workflowstore.WorkflowGraphSaveRequest{WorkflowID: workflowID, ExpectedVersion: expectedVersion}
	if metadata != nil {
		req.Metadata = &workflowstore.WorkflowGraphSaveMetadata{Name: metadata.Name, Description: metadata.Description}
		if metadata.ExecutionTargetPolicy != nil {
			policy := workflowExecutionTargetPolicyFromAPI(*metadata.ExecutionTargetPolicy)
			req.Metadata.ExecutionTargetPolicy = &policy
		}
	}
	if confirmation != nil {
		req.Confirmed = true
		req.ExpectedRemovedNodeCount = confirmation.ExpectedRemovedNodeCount
		req.ExpectedRemovedTransitionGroupCount = confirmation.ExpectedRemovedTransitionGroupCount
		req.ExpectedRemovedEdgeCount = confirmation.ExpectedRemovedEdgeCount
		req.ExpectedNodeTaskReferenceCount = confirmation.ExpectedNodeTaskReferenceCount
		req.ExpectedEdgeTaskReferenceCount = confirmation.ExpectedEdgeTaskReferenceCount
	}
	for _, group := range graph.NodeGroups {
		req.NodeGroups = append(req.NodeGroups, workflowstore.NodeGroupRecord{ID: group.ID, WorkflowID: workflowID, Key: workflow.ModelKey(group.Key), DisplayName: group.DisplayName})
	}
	for _, node := range graph.Nodes {
		req.Nodes = append(req.Nodes, workflowstore.NodeRecord{ID: workflow.NodeID(node.ID), WorkflowID: workflowID, Key: workflow.ModelKey(node.Key), Kind: workflow.NodeKind(node.Kind), DisplayName: node.DisplayName, GroupID: node.GroupID, GroupKey: node.GroupKey, SubagentRole: node.SubagentRole, PromptTemplate: node.PromptTemplate, CompletionMode: node.CompletionMode, ScriptPath: optionalStringValue(node.ScriptPath), InputFields: inputFields(node.InputFields), JoinInputProviders: joinInputProviders(node.JoinInputProviders)})
	}
	for _, group := range graph.TransitionGroups {
		req.TransitionGroups = append(req.TransitionGroups, workflowstore.TransitionGroupRecord{ID: workflow.TransitionGroupID(group.ID), WorkflowID: workflowID, SourceNodeID: workflow.NodeID(group.SourceNodeID), TransitionID: workflow.TransitionID(group.TransitionID), DisplayName: group.DisplayName, Description: group.Description})
	}
	for _, edge := range graph.Edges {
		req.Edges = append(req.Edges, workflowstore.EdgeRecord{ID: workflow.EdgeID(edge.ID), WorkflowID: workflowID, TransitionGroupID: workflow.TransitionGroupID(edge.TransitionGroupID), Key: workflow.ModelKey(edge.Key), TargetNodeID: workflow.NodeID(edge.TargetNodeID), RequiresApproval: edge.RequiresApproval, ContextMode: workflow.ContextMode(edge.ContextMode), ContextSource: workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(edge.ContextSource.Kind), NodeKey: workflow.ModelKey(edge.ContextSource.NodeKey)}), PromptTemplate: edge.PromptTemplate, Parameters: domainParameters(edge.Parameters)})
	}
	return req
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
		RemovedNodeCount:                  result.Impact.RemovedNodeCount,
		RemovedTransitionGroupCount:       result.Impact.RemovedTransitionGroupCount,
		RemovedEdgeCount:                  result.Impact.RemovedEdgeCount,
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
		out = append(out, serverapi.WorkflowGraphSaveBlocker{Code: blocker.Code, Message: blocker.Message, Count: blocker.Count})
	}
	return out
}

func commentRecord(row workflowstore.CommentRecord) serverapi.WorkflowTaskComment {
	return serverapi.WorkflowTaskComment{ID: row.ID, TaskID: string(row.TaskID), Body: row.Body, Author: row.Author, AuthorID: row.AuthorID, CreatedAtUnixMs: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func inputFields(in []serverapi.WorkflowInputField) []workflow.InputField {
	out := make([]workflow.InputField, 0, len(in))
	for _, field := range in {
		out = append(out, workflow.InputField{Name: field.Name, Description: field.Description})
	}
	return out
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
		out = append(out, workflow.Parameter{Key: parameter.Key, Description: parameter.Description})
	}
	return out
}
