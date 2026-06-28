package client

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"core/shared/config"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
)

type Remote struct {
	plan           remoteDialPlan
	transport      rpcwire.ClientTransport
	mu             sync.Mutex
	control        *remoteControlConn
	identity       protocol.ServerIdentity
	projectID      string
	workspaceID    string
	workspaceRoot  string
	expectedRootID atomic.Value // string; empty disables root validation
	closed         atomic.Bool
}

func DialRemoteURL(ctx context.Context, rpcURL string) (*Remote, error) {
	return dialRemoteURL(ctx, rpcURL, "", "", "")
}

func DialRemoteURLForProject(ctx context.Context, rpcURL string, projectID string) (*Remote, error) {
	return dialRemoteURL(ctx, rpcURL, projectID, "", "")
}

func DialRemoteURLForProjectWorkspace(ctx context.Context, rpcURL string, projectID string, workspaceRoot string) (*Remote, error) {
	return dialRemoteURL(ctx, rpcURL, projectID, "", workspaceRoot)
}

func DialConfiguredRemote(ctx context.Context, cfg config.App) (*Remote, error) {
	return dialConfiguredRemote(ctx, cfg, "", "", "")
}

func DialConfiguredRemoteForProjectWorkspace(ctx context.Context, cfg config.App, projectID string, workspaceRoot string) (*Remote, error) {
	return dialConfiguredRemote(ctx, cfg, projectID, "", workspaceRoot)
}

func DialConfiguredRemoteForProjectWorkspaceID(ctx context.Context, cfg config.App, projectID string, workspaceID string) (*Remote, error) {
	return dialConfiguredRemote(ctx, cfg, projectID, workspaceID, "")
}

func (c *Remote) Close() error {
	if c == nil {
		return nil
	}
	c.closed.Store(true)
	c.mu.Lock()
	control := c.control
	c.control = nil
	c.mu.Unlock()
	if control == nil {
		return nil
	}
	return control.Close()
}

func (c *Remote) Identity() protocol.ServerIdentity {
	if c == nil {
		return protocol.ServerIdentity{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.identity
}

// RequireRoot pins the persistence-root id that every (re)connect handshake must
// report. It validates the current identity immediately and returns an error on
// mismatch so an initial attach to the wrong instance is rejected; the pinned id
// then guards reconnects, where the dial plan may resolve a different server on
// the fallback TCP endpoint after the original socket disappears. An empty rootID
// disables root validation (default-root behavior is unchanged).
func (c *Remote) RequireRoot(rootID string) error {
	if c == nil {
		return errors.New("remote client is required")
	}
	c.expectedRootID.Store(rootID)
	c.mu.Lock()
	identity := c.identity
	c.mu.Unlock()
	return validateIdentityRoot(rootID, identity)
}

func (c *Remote) rootID() string {
	if c == nil {
		return ""
	}
	if value, ok := c.expectedRootID.Load().(string); ok {
		return value
	}
	return ""
}

func (c *Remote) GetServerReadiness(ctx context.Context, req serverapi.ServerReadinessRequest) (serverapi.ServerReadinessResponse, error) {
	return callUnscopedRPC[serverapi.ServerReadinessRequest, serverapi.ServerReadinessResponse](c, ctx, protocol.MethodServerReadinessGet, req)
}

func (c *Remote) ProjectID() string {
	if c == nil {
		return ""
	}
	return c.projectID
}

func (c *Remote) WorkspaceRoot() string {
	if c == nil {
		return ""
	}
	return c.workspaceRoot
}

func (c *Remote) WorkspaceID() string {
	if c == nil {
		return ""
	}
	return c.workspaceID
}

func callUnscopedRPC[Req any, Resp any](c *Remote, ctx context.Context, method string, req Req) (Resp, error) {
	var resp Resp
	return resp, c.callUnscoped(ctx, method, req, &resp)
}

func callControlRPC[Req any, Resp any](c *Remote, ctx context.Context, method string, req Req) (Resp, error) {
	var resp Resp
	return resp, c.call(ctx, method, req, &resp)
}

func callDedicatedRPC[Req any, Resp any](c *Remote, ctx context.Context, requestID string, method string, req Req) (Resp, error) {
	var resp Resp
	return resp, c.callDedicated(ctx, requestID, method, req, &resp)
}

func (c *Remote) GetAuthBootstrapStatus(ctx context.Context, req serverapi.AuthGetBootstrapStatusRequest) (serverapi.AuthGetBootstrapStatusResponse, error) {
	return callUnscopedRPC[serverapi.AuthGetBootstrapStatusRequest, serverapi.AuthGetBootstrapStatusResponse](c, ctx, protocol.MethodAuthGetBootstrapStatus, req)
}

func (c *Remote) CompleteAuthBootstrap(ctx context.Context, req serverapi.AuthCompleteBootstrapRequest) (serverapi.AuthCompleteBootstrapResponse, error) {
	return callUnscopedRPC[serverapi.AuthCompleteBootstrapRequest, serverapi.AuthCompleteBootstrapResponse](c, ctx, protocol.MethodAuthCompleteBootstrap, req)
}

func (c *Remote) GetAuthStatus(ctx context.Context, req serverapi.AuthStatusRequest) (serverapi.AuthStatusResponse, error) {
	return callUnscopedRPC[serverapi.AuthStatusRequest, serverapi.AuthStatusResponse](c, ctx, protocol.MethodAuthGetStatus, req)
}

func (c *Remote) ListProjects(ctx context.Context, req serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error) {
	return callUnscopedRPC[serverapi.ProjectListRequest, serverapi.ProjectListResponse](c, ctx, protocol.MethodProjectList, req)
}

func (c *Remote) ListProjectHome(ctx context.Context, req serverapi.ProjectHomeListRequest) (serverapi.ProjectHomeListResponse, error) {
	return callUnscopedRPC[serverapi.ProjectHomeListRequest, serverapi.ProjectHomeListResponse](c, ctx, protocol.MethodProjectHomeList, req)
}

func (c *Remote) ResolveProjectPath(ctx context.Context, req serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return callUnscopedRPC[serverapi.ProjectResolvePathRequest, serverapi.ProjectResolvePathResponse](c, ctx, protocol.MethodProjectResolvePath, req)
}

func (c *Remote) PlanWorkspaceBinding(ctx context.Context, req serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
	return callUnscopedRPC[serverapi.ProjectBindingPlanRequest, serverapi.ProjectBindingPlanResponse](c, ctx, protocol.MethodProjectPlanWorkspaceBinding, req)
}

func (c *Remote) CreateProject(ctx context.Context, req serverapi.ProjectCreateRequest) (serverapi.ProjectCreateResponse, error) {
	return callUnscopedRPC[serverapi.ProjectCreateRequest, serverapi.ProjectCreateResponse](c, ctx, protocol.MethodProjectCreate, req)
}

func (c *Remote) GetProjectEdit(ctx context.Context, req serverapi.ProjectEditGetRequest) (serverapi.ProjectEditGetResponse, error) {
	return callUnscopedRPC[serverapi.ProjectEditGetRequest, serverapi.ProjectEditGetResponse](c, ctx, protocol.MethodProjectEditGet, req)
}

func (c *Remote) UpdateProject(ctx context.Context, req serverapi.ProjectUpdateRequest) (serverapi.ProjectUpdateResponse, error) {
	return callUnscopedRPC[serverapi.ProjectUpdateRequest, serverapi.ProjectUpdateResponse](c, ctx, protocol.MethodProjectUpdate, req)
}

func (c *Remote) SetDefaultWorkspace(ctx context.Context, req serverapi.ProjectDefaultWorkspaceSetRequest) (serverapi.ProjectDefaultWorkspaceSetResponse, error) {
	return callUnscopedRPC[serverapi.ProjectDefaultWorkspaceSetRequest, serverapi.ProjectDefaultWorkspaceSetResponse](c, ctx, protocol.MethodProjectSetDefaultWorkspace, req)
}

func (c *Remote) ListProjectWorkspaces(ctx context.Context, req serverapi.ProjectWorkspaceListRequest) (serverapi.ProjectWorkspaceListResponse, error) {
	return callUnscopedRPC[serverapi.ProjectWorkspaceListRequest, serverapi.ProjectWorkspaceListResponse](c, ctx, protocol.MethodProjectWorkspaceList, req)
}

func (c *Remote) UnlinkWorkspaceFromProject(ctx context.Context, req serverapi.ProjectWorkspaceUnlinkRequest) (serverapi.ProjectWorkspaceUnlinkResponse, error) {
	return callUnscopedRPC[serverapi.ProjectWorkspaceUnlinkRequest, serverapi.ProjectWorkspaceUnlinkResponse](c, ctx, protocol.MethodProjectUnlinkWorkspace, req)
}

func (c *Remote) DeleteProject(ctx context.Context, req serverapi.ProjectDeleteRequest) (serverapi.ProjectDeleteResponse, error) {
	return callUnscopedRPC[serverapi.ProjectDeleteRequest, serverapi.ProjectDeleteResponse](c, ctx, protocol.MethodProjectDelete, req)
}

func (c *Remote) AttachWorkspaceToProject(ctx context.Context, req serverapi.ProjectAttachWorkspaceRequest) (serverapi.ProjectAttachWorkspaceResponse, error) {
	return callUnscopedRPC[serverapi.ProjectAttachWorkspaceRequest, serverapi.ProjectAttachWorkspaceResponse](c, ctx, protocol.MethodProjectAttachWorkspace, req)
}

func (c *Remote) RebindWorkspace(ctx context.Context, req serverapi.ProjectRebindWorkspaceRequest) (serverapi.ProjectRebindWorkspaceResponse, error) {
	return callUnscopedRPC[serverapi.ProjectRebindWorkspaceRequest, serverapi.ProjectRebindWorkspaceResponse](c, ctx, protocol.MethodProjectRebindWorkspace, req)
}

func (c *Remote) GetProjectOverview(ctx context.Context, req serverapi.ProjectGetOverviewRequest) (serverapi.ProjectGetOverviewResponse, error) {
	return callUnscopedRPC[serverapi.ProjectGetOverviewRequest, serverapi.ProjectGetOverviewResponse](c, ctx, protocol.MethodProjectGetOverview, req)
}

func (c *Remote) ListSessionsByProject(ctx context.Context, req serverapi.SessionListByProjectRequest) (serverapi.SessionListByProjectResponse, error) {
	return callUnscopedRPC[serverapi.SessionListByProjectRequest, serverapi.SessionListByProjectResponse](c, ctx, protocol.MethodSessionListByProject, req)
}

func (c *Remote) CreateWorkflow(ctx context.Context, req serverapi.WorkflowCreateRequest) (serverapi.WorkflowCreateResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowCreateRequest, serverapi.WorkflowCreateResponse](c, ctx, protocol.MethodWorkflowCreate, req)
}

func (c *Remote) CreateAndLinkWorkflowToProject(ctx context.Context, req serverapi.WorkflowCreateAndLinkProjectRequest) (serverapi.WorkflowCreateAndLinkProjectResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowCreateAndLinkProjectRequest, serverapi.WorkflowCreateAndLinkProjectResponse](c, ctx, protocol.MethodWorkflowCreateAndLinkProject, req)
}

func (c *Remote) UpdateWorkflow(ctx context.Context, req serverapi.WorkflowUpdateRequest) (serverapi.WorkflowGetResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowUpdateRequest, serverapi.WorkflowGetResponse](c, ctx, protocol.MethodWorkflowUpdate, req)
}

func (c *Remote) ListWorkflows(ctx context.Context, req serverapi.WorkflowListRequest) (serverapi.WorkflowListResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowListRequest, serverapi.WorkflowListResponse](c, ctx, protocol.MethodWorkflowList, req)
}

func (c *Remote) GetWorkflow(ctx context.Context, req serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowGetRequest, serverapi.WorkflowGetResponse](c, ctx, protocol.MethodWorkflowGet, req)
}

func (c *Remote) AddWorkflowNodeGroup(ctx context.Context, req serverapi.WorkflowNodeGroupAddRequest) (serverapi.WorkflowNodeGroupResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowNodeGroupAddRequest, serverapi.WorkflowNodeGroupResponse](c, ctx, protocol.MethodWorkflowNodeGroupAdd, req)
}

func (c *Remote) UpdateWorkflowNodeGroup(ctx context.Context, req serverapi.WorkflowNodeGroupUpdateRequest) (serverapi.WorkflowNodeGroupResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowNodeGroupUpdateRequest, serverapi.WorkflowNodeGroupResponse](c, ctx, protocol.MethodWorkflowNodeGroupUpdate, req)
}

func (c *Remote) DeleteWorkflowNodeGroup(ctx context.Context, req serverapi.WorkflowNodeGroupDeleteRequest) error {
	return c.callUnscoped(ctx, protocol.MethodWorkflowNodeGroupDelete, req, &struct{}{})
}

func (c *Remote) AddWorkflowNode(ctx context.Context, req serverapi.WorkflowNodeAddRequest) (serverapi.WorkflowNodeAddResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowNodeAddRequest, serverapi.WorkflowNodeAddResponse](c, ctx, protocol.MethodWorkflowAddNode, req)
}

func (c *Remote) UpdateWorkflowNode(ctx context.Context, req serverapi.WorkflowNodeUpdateRequest) (serverapi.WorkflowNodeUpdateResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowNodeUpdateRequest, serverapi.WorkflowNodeUpdateResponse](c, ctx, protocol.MethodWorkflowUpdateNode, req)
}

func (c *Remote) AddWorkflowTransitionGroup(ctx context.Context, req serverapi.WorkflowTransitionGroupAddRequest) (serverapi.WorkflowTransitionGroupAddResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTransitionGroupAddRequest, serverapi.WorkflowTransitionGroupAddResponse](c, ctx, protocol.MethodWorkflowAddTransitionGroup, req)
}

func (c *Remote) UpdateWorkflowTransitionGroup(ctx context.Context, req serverapi.WorkflowTransitionGroupUpdateRequest) (serverapi.WorkflowTransitionGroupUpdateResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTransitionGroupUpdateRequest, serverapi.WorkflowTransitionGroupUpdateResponse](c, ctx, protocol.MethodWorkflowUpdateTransitionGroup, req)
}

func (c *Remote) AddWorkflowEdge(ctx context.Context, req serverapi.WorkflowEdgeAddRequest) (serverapi.WorkflowEdgeAddResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowEdgeAddRequest, serverapi.WorkflowEdgeAddResponse](c, ctx, protocol.MethodWorkflowAddEdge, req)
}

func (c *Remote) UpdateWorkflowEdge(ctx context.Context, req serverapi.WorkflowEdgeUpdateRequest) (serverapi.WorkflowEdgeUpdateResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowEdgeUpdateRequest, serverapi.WorkflowEdgeUpdateResponse](c, ctx, protocol.MethodWorkflowUpdateEdge, req)
}

func (c *Remote) LinkWorkflowToProject(ctx context.Context, req serverapi.WorkflowLinkProjectRequest) (serverapi.WorkflowLinkProjectResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowLinkProjectRequest, serverapi.WorkflowLinkProjectResponse](c, ctx, protocol.MethodWorkflowLinkProject, req)
}

func (c *Remote) ListProjectWorkflowLinks(ctx context.Context, req serverapi.WorkflowListProjectLinksRequest) (serverapi.WorkflowListProjectLinksResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowListProjectLinksRequest, serverapi.WorkflowListProjectLinksResponse](c, ctx, protocol.MethodWorkflowListProjectLinks, req)
}

func (c *Remote) SetDefaultProjectWorkflowLink(ctx context.Context, req serverapi.WorkflowSetDefaultProjectLinkRequest) (serverapi.WorkflowSetDefaultProjectLinkResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowSetDefaultProjectLinkRequest, serverapi.WorkflowSetDefaultProjectLinkResponse](c, ctx, protocol.MethodWorkflowSetDefaultProjectLink, req)
}

func (c *Remote) UnlinkWorkflowFromProject(ctx context.Context, req serverapi.WorkflowUnlinkProjectRequest) (serverapi.WorkflowUnlinkProjectResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowUnlinkProjectRequest, serverapi.WorkflowUnlinkProjectResponse](c, ctx, protocol.MethodWorkflowUnlinkProject, req)
}

func (c *Remote) PreviewWorkflowDelete(ctx context.Context, req serverapi.WorkflowDeletePreviewRequest) (serverapi.WorkflowDeletePreviewResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowDeletePreviewRequest, serverapi.WorkflowDeletePreviewResponse](c, ctx, protocol.MethodWorkflowDeletePreview, req)
}

func (c *Remote) DeleteWorkflow(ctx context.Context, req serverapi.WorkflowDeleteRequest) (serverapi.WorkflowDeleteResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowDeleteRequest, serverapi.WorkflowDeleteResponse](c, ctx, protocol.MethodWorkflowDelete, req)
}

func (c *Remote) ValidateWorkflow(ctx context.Context, req serverapi.WorkflowValidateRequest) (serverapi.WorkflowValidateResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowValidateRequest, serverapi.WorkflowValidateResponse](c, ctx, protocol.MethodWorkflowValidate, req)
}

func (c *Remote) ValidateWorkflowGraphDraft(ctx context.Context, req serverapi.WorkflowGraphValidateDraftRequest) (serverapi.WorkflowGraphValidateDraftResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowGraphValidateDraftRequest, serverapi.WorkflowGraphValidateDraftResponse](c, ctx, protocol.MethodWorkflowGraphValidateDraft, req)
}

func (c *Remote) DeriveWorkflowGraphWiring(ctx context.Context, req serverapi.WorkflowGraphDeriveWiringRequest) (serverapi.WorkflowGraphDeriveWiringResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowGraphDeriveWiringRequest, serverapi.WorkflowGraphDeriveWiringResponse](c, ctx, protocol.MethodWorkflowGraphDeriveWiring, req)
}

func (c *Remote) PreviewWorkflowGraphSave(ctx context.Context, req serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowGraphSavePreviewRequest, serverapi.WorkflowGraphSavePreviewResponse](c, ctx, protocol.MethodWorkflowGraphSavePreview, req)
}

func (c *Remote) SaveWorkflowGraph(ctx context.Context, req serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowGraphSaveRequest, serverapi.WorkflowGraphSaveResponse](c, ctx, protocol.MethodWorkflowGraphSave, req)
}

func (c *Remote) CreateWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCreateRequest) (serverapi.WorkflowTaskCreateResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskCreateRequest, serverapi.WorkflowTaskCreateResponse](c, ctx, protocol.MethodWorkflowTaskCreate, req)
}

func (c *Remote) UpdateWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskUpdateRequest) (serverapi.WorkflowTaskUpdateResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskUpdateRequest, serverapi.WorkflowTaskUpdateResponse](c, ctx, protocol.MethodWorkflowTaskUpdate, req)
}

func (c *Remote) StartWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskStartResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskStartRequest, serverapi.WorkflowTaskStartResponse](c, ctx, protocol.MethodWorkflowTaskStart, req)
}

func (c *Remote) InterruptWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskInterruptRequest) (serverapi.WorkflowTaskInterruptResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskInterruptRequest, serverapi.WorkflowTaskInterruptResponse](c, ctx, protocol.MethodWorkflowTaskInterrupt, req)
}

func (c *Remote) ResumeWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskResumeRequest, serverapi.WorkflowTaskResumeResponse](c, ctx, protocol.MethodWorkflowTaskResume, req)
}

func (c *Remote) ApproveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskApproveRequest) (serverapi.WorkflowTaskApproveResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskApproveRequest, serverapi.WorkflowTaskApproveResponse](c, ctx, protocol.MethodWorkflowTaskApprove, req)
}

func (c *Remote) MoveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskMoveResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskMoveRequest, serverapi.WorkflowTaskMoveResponse](c, ctx, protocol.MethodWorkflowTaskMove, req)
}

func (c *Remote) CompleteWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCompleteRequest) (serverapi.WorkflowTaskCompleteResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskCompleteRequest, serverapi.WorkflowTaskCompleteResponse](c, ctx, protocol.MethodWorkflowTaskComplete, req)
}

func (c *Remote) CancelWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCancelRequest) error {
	return c.callUnscoped(ctx, protocol.MethodWorkflowTaskCancel, req, &struct{}{})
}

func (c *Remote) DeleteWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskDeleteRequest) error {
	return c.callUnscoped(ctx, protocol.MethodWorkflowTaskDelete, req, &struct{}{})
}

func (c *Remote) ListWorkflowAttention(ctx context.Context, req serverapi.WorkflowAttentionListRequest) (serverapi.WorkflowAttentionListResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowAttentionListRequest, serverapi.WorkflowAttentionListResponse](c, ctx, protocol.MethodWorkflowAttentionList, req)
}

func (c *Remote) ListWorkflowTaskAttention(ctx context.Context, req serverapi.WorkflowTaskAttentionListRequest) (serverapi.WorkflowTaskAttentionListResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskAttentionListRequest, serverapi.WorkflowTaskAttentionListResponse](c, ctx, protocol.MethodWorkflowTaskAttentionList, req)
}

func (c *Remote) AnswerWorkflowTaskQuestion(ctx context.Context, req serverapi.WorkflowTaskQuestionAnswerRequest) error {
	return c.callUnscoped(ctx, protocol.MethodWorkflowTaskQuestionAnswer, req, &struct{}{})
}

func (c *Remote) AddWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentAddRequest) (serverapi.WorkflowTaskCommentAddResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskCommentAddRequest, serverapi.WorkflowTaskCommentAddResponse](c, ctx, protocol.MethodWorkflowTaskCommentAdd, req)
}

func (c *Remote) ListWorkflowTaskComments(ctx context.Context, req serverapi.WorkflowTaskCommentListRequest) (serverapi.WorkflowTaskCommentListResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskCommentListRequest, serverapi.WorkflowTaskCommentListResponse](c, ctx, protocol.MethodWorkflowTaskCommentList, req)
}

func (c *Remote) ReplaceWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentReplaceRequest) error {
	return c.callUnscoped(ctx, protocol.MethodWorkflowTaskCommentReplace, req, &struct{}{})
}

func (c *Remote) DeleteWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentDeleteRequest) error {
	return c.callUnscoped(ctx, protocol.MethodWorkflowTaskCommentDelete, req, &struct{}{})
}

func (c *Remote) ListWorkflowTaskActivity(ctx context.Context, req serverapi.WorkflowTaskActivityListRequest) (serverapi.WorkflowTaskActivityListResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskActivityListRequest, serverapi.WorkflowTaskActivityListResponse](c, ctx, protocol.MethodWorkflowTaskActivityList, req)
}

func (c *Remote) ListWorkflowTasks(ctx context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskListRequest, serverapi.WorkflowTaskListResponse](c, ctx, protocol.MethodWorkflowTaskList, req)
}

func (c *Remote) GetWorkflowBoard(ctx context.Context, req serverapi.WorkflowBoardRequest) (serverapi.WorkflowBoardResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowBoardRequest, serverapi.WorkflowBoardResponse](c, ctx, protocol.MethodWorkflowBoardGet, req)
}

func (c *Remote) ListWorkflowBoardNodeCards(ctx context.Context, req serverapi.WorkflowBoardNodeCardsListRequest) (serverapi.WorkflowBoardNodeCardsListResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowBoardNodeCardsListRequest, serverapi.WorkflowBoardNodeCardsListResponse](c, ctx, protocol.MethodWorkflowBoardNodeCardsList, req)
}

func (c *Remote) GetWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return callUnscopedRPC[serverapi.WorkflowTaskGetRequest, serverapi.WorkflowTaskGetResponse](c, ctx, protocol.MethodWorkflowTaskGet, req)
}

func (c *Remote) PlanSession(ctx context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
	var resp serverapi.SessionPlanResponse
	return resp, c.call(ctx, protocol.MethodSessionPlan, req, &resp)
}

func (c *Remote) GetSessionMainView(ctx context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	var resp serverapi.SessionMainViewResponse
	return resp, c.call(ctx, protocol.MethodSessionGetMainView, req, &resp)
}

func (c *Remote) GetSessionTranscriptPage(ctx context.Context, req serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	var resp serverapi.SessionTranscriptPageResponse
	return resp, c.call(ctx, protocol.MethodSessionGetTranscriptPage, req, &resp)
}

func (c *Remote) GetSessionCommittedTranscriptSuffix(ctx context.Context, req serverapi.SessionCommittedTranscriptSuffixRequest) (serverapi.SessionCommittedTranscriptSuffixResponse, error) {
	var resp serverapi.SessionCommittedTranscriptSuffixResponse
	return resp, c.call(ctx, protocol.MethodSessionGetCommittedTranscriptSuffix, req, &resp)
}

func (c *Remote) GetInitialInput(ctx context.Context, req serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
	var resp serverapi.SessionInitialInputResponse
	return resp, c.call(ctx, protocol.MethodSessionGetInitialInput, req, &resp)
}

func (c *Remote) PersistInputDraft(ctx context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	var resp serverapi.SessionPersistInputDraftResponse
	return resp, c.call(ctx, protocol.MethodSessionPersistInputDraft, req, &resp)
}

func (c *Remote) RetargetSessionWorkspace(ctx context.Context, req serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
	return callUnscopedRPC[serverapi.SessionRetargetWorkspaceRequest, serverapi.SessionRetargetWorkspaceResponse](c, ctx, protocol.MethodSessionRetargetWorkspace, req)
}

func (c *Remote) ResolveTransition(ctx context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	var resp serverapi.SessionResolveTransitionResponse
	return resp, c.call(ctx, protocol.MethodSessionResolveTransition, req, &resp)
}

func (c *Remote) ListWorktrees(ctx context.Context, req serverapi.WorktreeListRequest) (serverapi.WorktreeListResponse, error) {
	var resp serverapi.WorktreeListResponse
	return resp, c.call(ctx, protocol.MethodWorktreeList, req, &resp)
}

func (c *Remote) ResolveWorktreeCreateTarget(ctx context.Context, req serverapi.WorktreeCreateTargetResolveRequest) (serverapi.WorktreeCreateTargetResolveResponse, error) {
	var resp serverapi.WorktreeCreateTargetResolveResponse
	return resp, c.call(ctx, protocol.MethodWorktreeCreateTargetResolve, req, &resp)
}

func (c *Remote) CreateWorktree(ctx context.Context, req serverapi.WorktreeCreateRequest) (serverapi.WorktreeCreateResponse, error) {
	var resp serverapi.WorktreeCreateResponse
	return resp, c.call(ctx, protocol.MethodWorktreeCreate, req, &resp)
}

func (c *Remote) SwitchWorktree(ctx context.Context, req serverapi.WorktreeSwitchRequest) (serverapi.WorktreeSwitchResponse, error) {
	var resp serverapi.WorktreeSwitchResponse
	return resp, c.call(ctx, protocol.MethodWorktreeSwitch, req, &resp)
}

func (c *Remote) DeleteWorktree(ctx context.Context, req serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResponse, error) {
	var resp serverapi.WorktreeDeleteResponse
	return resp, c.call(ctx, protocol.MethodWorktreeDelete, req, &resp)
}

func (c *Remote) ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	var resp serverapi.SessionRuntimeActivateResponse
	return resp, c.call(ctx, protocol.MethodSessionRuntimeActivate, req, &resp)
}

func (c *Remote) ReleaseSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	var resp serverapi.SessionRuntimeReleaseResponse
	return resp, c.call(ctx, protocol.MethodSessionRuntimeRelease, req, &resp)
}

func (c *Remote) SetSessionName(ctx context.Context, req serverapi.RuntimeSetSessionNameRequest) error {
	return c.call(ctx, protocol.MethodRuntimeSetSessionName, req, nil)
}

func (c *Remote) SetThinkingLevel(ctx context.Context, req serverapi.RuntimeSetThinkingLevelRequest) error {
	return c.call(ctx, protocol.MethodRuntimeSetThinkingLevel, req, nil)
}

func (c *Remote) SetFastModeEnabled(ctx context.Context, req serverapi.RuntimeSetFastModeEnabledRequest) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
	return callControlRPC[serverapi.RuntimeSetFastModeEnabledRequest, serverapi.RuntimeSetFastModeEnabledResponse](c, ctx, protocol.MethodRuntimeSetFastModeEnabled, req)
}

func (c *Remote) SetReviewerEnabled(ctx context.Context, req serverapi.RuntimeSetReviewerEnabledRequest) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
	return callControlRPC[serverapi.RuntimeSetReviewerEnabledRequest, serverapi.RuntimeSetReviewerEnabledResponse](c, ctx, protocol.MethodRuntimeSetReviewerEnabled, req)
}

func (c *Remote) SetAutoCompactionEnabled(ctx context.Context, req serverapi.RuntimeSetAutoCompactionEnabledRequest) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
	return callControlRPC[serverapi.RuntimeSetAutoCompactionEnabledRequest, serverapi.RuntimeSetAutoCompactionEnabledResponse](c, ctx, protocol.MethodRuntimeSetAutoCompactionEnabled, req)
}

func (c *Remote) SetQuestionsEnabled(ctx context.Context, req serverapi.RuntimeSetQuestionsEnabledRequest) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
	return callControlRPC[serverapi.RuntimeSetQuestionsEnabledRequest, serverapi.RuntimeSetQuestionsEnabledResponse](c, ctx, protocol.MethodRuntimeSetQuestionsEnabled, req)
}

func (c *Remote) AppendCommittedEntry(ctx context.Context, req serverapi.RuntimeAppendCommittedEntryRequest) error {
	return c.call(ctx, protocol.MethodRuntimeAppendCommittedEntry, req, nil)
}

func (c *Remote) ShouldCompactBeforeUserMessage(ctx context.Context, req serverapi.RuntimeShouldCompactBeforeUserMessageRequest) (serverapi.RuntimeShouldCompactBeforeUserMessageResponse, error) {
	return callControlRPC[serverapi.RuntimeShouldCompactBeforeUserMessageRequest, serverapi.RuntimeShouldCompactBeforeUserMessageResponse](c, ctx, protocol.MethodRuntimeShouldCompactBeforeUserMessage, req)
}

func (c *Remote) SubmitUserTurn(ctx context.Context, req serverapi.RuntimeSubmitUserTurnRequest) (serverapi.RuntimeSubmitUserTurnResponse, error) {
	return callDedicatedRPC[serverapi.RuntimeSubmitUserTurnRequest, serverapi.RuntimeSubmitUserTurnResponse](c, ctx, "runtime-submit-user-turn", protocol.MethodRuntimeSubmitUserTurn, req)
}

func (c *Remote) SubmitUserShellCommand(ctx context.Context, req serverapi.RuntimeSubmitUserShellCommandRequest) error {
	return c.callDedicated(ctx, "runtime-submit-user-shell-command", protocol.MethodRuntimeSubmitUserShellCommand, req, nil)
}

func (c *Remote) CompactContext(ctx context.Context, req serverapi.RuntimeCompactContextRequest) error {
	return c.callDedicated(ctx, "runtime-compact-context", protocol.MethodRuntimeCompactContext, req, nil)
}

func (c *Remote) CompactContextForPreSubmit(ctx context.Context, req serverapi.RuntimeCompactContextForPreSubmitRequest) error {
	return c.callDedicated(ctx, "runtime-compact-context-pre-submit", protocol.MethodRuntimeCompactContextForPreSubmit, req, nil)
}

func (c *Remote) HasQueuedUserWork(ctx context.Context, req serverapi.RuntimeHasQueuedUserWorkRequest) (serverapi.RuntimeHasQueuedUserWorkResponse, error) {
	return callControlRPC[serverapi.RuntimeHasQueuedUserWorkRequest, serverapi.RuntimeHasQueuedUserWorkResponse](c, ctx, protocol.MethodRuntimeHasQueuedUserWork, req)
}

func (c *Remote) SubmitQueuedUserMessages(ctx context.Context, req serverapi.RuntimeSubmitQueuedUserMessagesRequest) (serverapi.RuntimeSubmitQueuedUserMessagesResponse, error) {
	return callDedicatedRPC[serverapi.RuntimeSubmitQueuedUserMessagesRequest, serverapi.RuntimeSubmitQueuedUserMessagesResponse](c, ctx, "runtime-submit-queued-user-messages", protocol.MethodRuntimeSubmitQueuedUserMessages, req)
}

func (c *Remote) Interrupt(ctx context.Context, req serverapi.RuntimeInterruptRequest) error {
	return c.callDedicated(ctx, "runtime-interrupt", protocol.MethodRuntimeInterrupt, req, nil)
}

func (c *Remote) QueueUserMessage(ctx context.Context, req serverapi.RuntimeQueueUserMessageRequest) (serverapi.RuntimeQueueUserMessageResponse, error) {
	return callControlRPC[serverapi.RuntimeQueueUserMessageRequest, serverapi.RuntimeQueueUserMessageResponse](c, ctx, protocol.MethodRuntimeQueueUserMessage, req)
}

func (c *Remote) DiscardQueuedUserMessage(ctx context.Context, req serverapi.RuntimeDiscardQueuedUserMessageRequest) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
	return callControlRPC[serverapi.RuntimeDiscardQueuedUserMessageRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse](c, ctx, protocol.MethodRuntimeDiscardQueuedUserMessage, req)
}

func (c *Remote) RecordPromptHistory(ctx context.Context, req serverapi.RuntimeRecordPromptHistoryRequest) error {
	return c.call(ctx, protocol.MethodRuntimeRecordPromptHistory, req, nil)
}

func (c *Remote) ShowGoal(ctx context.Context, req serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callControlRPC[serverapi.RuntimeGoalShowRequest, serverapi.RuntimeGoalShowResponse](c, ctx, protocol.MethodRuntimeGoalShow, req)
}

func (c *Remote) SetGoal(ctx context.Context, req serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callControlRPC[serverapi.RuntimeGoalSetRequest, serverapi.RuntimeGoalShowResponse](c, ctx, protocol.MethodRuntimeGoalSet, req)
}

func (c *Remote) PauseGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callControlRPC[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](c, ctx, protocol.MethodRuntimeGoalPause, req)
}

func (c *Remote) ResumeGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callControlRPC[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](c, ctx, protocol.MethodRuntimeGoalResume, req)
}

func (c *Remote) CompleteGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callControlRPC[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](c, ctx, protocol.MethodRuntimeGoalComplete, req)
}

func (c *Remote) ClearGoal(ctx context.Context, req serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return callControlRPC[serverapi.RuntimeGoalClearRequest, serverapi.RuntimeGoalShowResponse](c, ctx, protocol.MethodRuntimeGoalClear, req)
}

func (c *Remote) ListProcesses(ctx context.Context, req serverapi.ProcessListRequest) (serverapi.ProcessListResponse, error) {
	var resp serverapi.ProcessListResponse
	return resp, c.call(ctx, protocol.MethodProcessList, req, &resp)
}

func (c *Remote) GetProcess(ctx context.Context, req serverapi.ProcessGetRequest) (serverapi.ProcessGetResponse, error) {
	var resp serverapi.ProcessGetResponse
	return resp, c.call(ctx, protocol.MethodProcessGet, req, &resp)
}

func (c *Remote) KillProcess(ctx context.Context, req serverapi.ProcessKillRequest) (serverapi.ProcessKillResponse, error) {
	var resp serverapi.ProcessKillResponse
	return resp, c.call(ctx, protocol.MethodProcessKill, req, &resp)
}

func (c *Remote) GetInlineOutput(ctx context.Context, req serverapi.ProcessInlineOutputRequest) (serverapi.ProcessInlineOutputResponse, error) {
	var resp serverapi.ProcessInlineOutputResponse
	return resp, c.call(ctx, protocol.MethodProcessInlineOutput, req, &resp)
}

func (c *Remote) ListPendingAsksBySession(ctx context.Context, req serverapi.AskListPendingBySessionRequest) (serverapi.AskListPendingBySessionResponse, error) {
	var resp serverapi.AskListPendingBySessionResponse
	return resp, c.call(ctx, protocol.MethodAskListPending, req, &resp)
}

func (c *Remote) AnswerAsk(ctx context.Context, req serverapi.AskAnswerRequest) error {
	return c.call(ctx, protocol.MethodAskAnswer, req, nil)
}

func (c *Remote) ListPendingApprovalsBySession(ctx context.Context, req serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error) {
	var resp serverapi.ApprovalListPendingBySessionResponse
	return resp, c.call(ctx, protocol.MethodApprovalListPending, req, &resp)
}

func (c *Remote) AnswerApproval(ctx context.Context, req serverapi.ApprovalAnswerRequest) error {
	return c.call(ctx, protocol.MethodApprovalAnswer, req, nil)
}

func (c *Remote) ensureOpen() error {
	if c == nil {
		return errors.New("remote client is required")
	}
	if c.closed.Load() {
		return errors.New("remote client is closed")
	}
	return nil
}

func (c *Remote) call(ctx context.Context, method string, params any, out any) error {
	return c.callUnscoped(ctx, method, params, out)
}

func (c *Remote) callUnscoped(ctx context.Context, method string, params any, out any) error {
	control, err := c.ensureControl(ctx)
	if err != nil {
		return err
	}
	return control.call(ctx, method, params, out)
}

func (c *Remote) ensureControl(ctx context.Context) (*remoteControlConn, error) {
	if err := c.ensureOpen(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return nil, errors.New("remote client is closed")
	}
	if c.control != nil && !c.control.IsDone() {
		return c.control, nil
	}
	if c.control != nil {
		_ = c.control.Close()
		c.control = nil
	}
	conn, identity, err := c.openControlRPCConn(ctx)
	if err != nil {
		return nil, err
	}
	if c.closed.Load() {
		_ = conn.Close()
		return nil, errors.New("remote client is closed")
	}
	control := newRemoteControlConn(conn)
	c.control = control
	c.identity = identity
	return control, nil
}

func (c *Remote) openControlRPCConn(ctx context.Context) (rpcwire.Conn, protocol.ServerIdentity, error) {
	conn, err := c.plan.dial(ctx, c.transport)
	if err != nil {
		return nil, protocol.ServerIdentity{}, err
	}
	cleanup := func() { _ = conn.Close() }
	identity, err := handshakeRPC(ctx, conn)
	if err != nil {
		cleanup()
		return nil, protocol.ServerIdentity{}, err
	}
	if err := validateIdentityRoot(c.rootID(), identity); err != nil {
		cleanup()
		return nil, protocol.ServerIdentity{}, err
	}
	if err := attachProjectRPC(ctx, conn, c.projectID, c.workspaceID, c.workspaceRoot); err != nil {
		cleanup()
		return nil, protocol.ServerIdentity{}, err
	}
	return conn, identity, nil
}

func dialRemoteURL(ctx context.Context, rpcURL string, projectID string, workspaceID string, workspaceRoot string) (*Remote, error) {
	endpoint, err := rpcwire.ParseWebSocketEndpoint(strings.TrimSpace(rpcURL))
	if err != nil {
		return nil, err
	}
	return dialRemoteWithTransport(ctx, remoteDialPlan{endpoints: []rpcwire.Endpoint{endpoint}}, rpcwire.NewWebSocketTransport(), projectID, workspaceID, workspaceRoot)
}

func dialConfiguredRemote(ctx context.Context, cfg config.App, projectID string, workspaceID string, workspaceRoot string) (*Remote, error) {
	plan, err := configuredRemoteDialPlan(cfg)
	if err != nil {
		return nil, err
	}
	return dialRemoteWithTransport(ctx, plan, rpcwire.NewWebSocketTransport(), projectID, workspaceID, workspaceRoot)
}

var _ ProjectViewClient = (*Remote)(nil)
var _ AuthStatusClient = (*Remote)(nil)
var _ SessionLaunchClient = (*Remote)(nil)
var _ SessionViewClient = (*Remote)(nil)
var _ SessionLifecycleClient = (*Remote)(nil)
var _ SessionRuntimeClient = (*Remote)(nil)
var _ RuntimeControlClient = (*Remote)(nil)
var _ ProcessViewClient = (*Remote)(nil)
var _ ProcessControlClient = (*Remote)(nil)
var _ ProcessOutputClient = (*Remote)(nil)
var _ SessionActivityClient = (*Remote)(nil)
var _ RunPromptClient = (*Remote)(nil)
var _ AskViewClient = (*Remote)(nil)
var _ PromptControlClient = (*Remote)(nil)
var _ ApprovalViewClient = (*Remote)(nil)
