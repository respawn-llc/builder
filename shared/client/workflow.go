package client

import (
	"context"

	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type WorkflowClient = servicecontract.WorkflowService
type loopbackWorkflowClient struct {
	loopbackClient[servicecontract.WorkflowService]
}

func NewLoopbackWorkflowClient(service servicecontract.WorkflowService) WorkflowClient {
	return &loopbackWorkflowClient{loopbackClient: newLoopbackClient(service)}
}

func (c *loopbackWorkflowClient) CreateWorkflow(ctx context.Context, req serverapi.WorkflowCreateRequest) (serverapi.WorkflowCreateResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.CreateWorkflow)
}

func (c *loopbackWorkflowClient) CreateAndLinkWorkflowToProject(ctx context.Context, req serverapi.WorkflowCreateAndLinkProjectRequest) (serverapi.WorkflowCreateAndLinkProjectResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.CreateAndLinkWorkflowToProject)
}

func (c *loopbackWorkflowClient) UpdateWorkflow(ctx context.Context, req serverapi.WorkflowUpdateRequest) (serverapi.WorkflowGetResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.UpdateWorkflow)
}

func (c *loopbackWorkflowClient) ListWorkflows(ctx context.Context, req serverapi.WorkflowListRequest) (serverapi.WorkflowListResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.ListWorkflows)
}

func (c *loopbackWorkflowClient) GetWorkflow(ctx context.Context, req serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.GetWorkflow)
}

func (c *loopbackWorkflowClient) AddWorkflowNodeGroup(ctx context.Context, req serverapi.WorkflowNodeGroupAddRequest) (serverapi.WorkflowNodeGroupResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.AddWorkflowNodeGroup)
}

func (c *loopbackWorkflowClient) UpdateWorkflowNodeGroup(ctx context.Context, req serverapi.WorkflowNodeGroupUpdateRequest) (serverapi.WorkflowNodeGroupResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.UpdateWorkflowNodeGroup)
}

func (c *loopbackWorkflowClient) DeleteWorkflowNodeGroup(ctx context.Context, req serverapi.WorkflowNodeGroupDeleteRequest) error {
	return callLoopbackClientNoResponse(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.DeleteWorkflowNodeGroup)
}

func (c *loopbackWorkflowClient) AddWorkflowNode(ctx context.Context, req serverapi.WorkflowNodeAddRequest) (serverapi.WorkflowNodeAddResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.AddWorkflowNode)
}

func (c *loopbackWorkflowClient) UpdateWorkflowNode(ctx context.Context, req serverapi.WorkflowNodeUpdateRequest) (serverapi.WorkflowNodeUpdateResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.UpdateWorkflowNode)
}

func (c *loopbackWorkflowClient) AddWorkflowTransitionGroup(ctx context.Context, req serverapi.WorkflowTransitionGroupAddRequest) (serverapi.WorkflowTransitionGroupAddResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.AddWorkflowTransitionGroup)
}

func (c *loopbackWorkflowClient) UpdateWorkflowTransitionGroup(ctx context.Context, req serverapi.WorkflowTransitionGroupUpdateRequest) (serverapi.WorkflowTransitionGroupUpdateResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.UpdateWorkflowTransitionGroup)
}

func (c *loopbackWorkflowClient) AddWorkflowEdge(ctx context.Context, req serverapi.WorkflowEdgeAddRequest) (serverapi.WorkflowEdgeAddResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.AddWorkflowEdge)
}

func (c *loopbackWorkflowClient) UpdateWorkflowEdge(ctx context.Context, req serverapi.WorkflowEdgeUpdateRequest) (serverapi.WorkflowEdgeUpdateResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.UpdateWorkflowEdge)
}

func (c *loopbackWorkflowClient) LinkWorkflowToProject(ctx context.Context, req serverapi.WorkflowLinkProjectRequest) (serverapi.WorkflowLinkProjectResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.LinkWorkflowToProject)
}

func (c *loopbackWorkflowClient) ListProjectWorkflowLinks(ctx context.Context, req serverapi.WorkflowListProjectLinksRequest) (serverapi.WorkflowListProjectLinksResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.ListProjectWorkflowLinks)
}

func (c *loopbackWorkflowClient) SetDefaultProjectWorkflowLink(ctx context.Context, req serverapi.WorkflowSetDefaultProjectLinkRequest) (serverapi.WorkflowSetDefaultProjectLinkResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.SetDefaultProjectWorkflowLink)
}

func (c *loopbackWorkflowClient) UnlinkWorkflowFromProject(ctx context.Context, req serverapi.WorkflowUnlinkProjectRequest) (serverapi.WorkflowUnlinkProjectResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.UnlinkWorkflowFromProject)
}

func (c *loopbackWorkflowClient) PreviewWorkflowDelete(ctx context.Context, req serverapi.WorkflowDeletePreviewRequest) (serverapi.WorkflowDeletePreviewResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.PreviewWorkflowDelete)
}

func (c *loopbackWorkflowClient) DeleteWorkflow(ctx context.Context, req serverapi.WorkflowDeleteRequest) (serverapi.WorkflowDeleteResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.DeleteWorkflow)
}

func (c *loopbackWorkflowClient) ValidateWorkflow(ctx context.Context, req serverapi.WorkflowValidateRequest) (serverapi.WorkflowValidateResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.ValidateWorkflow)
}

func (c *loopbackWorkflowClient) ValidateWorkflowScriptPath(ctx context.Context, req serverapi.WorkflowScriptPathValidateRequest) (serverapi.WorkflowValidateResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.ValidateWorkflowScriptPath)
}

func (c *loopbackWorkflowClient) ValidateWorkflowGraphDraft(ctx context.Context, req serverapi.WorkflowGraphValidateDraftRequest) (serverapi.WorkflowGraphValidateDraftResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.ValidateWorkflowGraphDraft)
}

func (c *loopbackWorkflowClient) DeriveWorkflowGraphWiring(ctx context.Context, req serverapi.WorkflowGraphDeriveWiringRequest) (serverapi.WorkflowGraphDeriveWiringResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.DeriveWorkflowGraphWiring)
}

func (c *loopbackWorkflowClient) PreviewWorkflowGraphSave(ctx context.Context, req serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.PreviewWorkflowGraphSave)
}

func (c *loopbackWorkflowClient) SaveWorkflowGraph(ctx context.Context, req serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.SaveWorkflowGraph)
}

func (c *loopbackWorkflowClient) CreateWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCreateRequest) (serverapi.WorkflowTaskCreateResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.CreateWorkflowTask)
}

func (c *loopbackWorkflowClient) UpdateWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskUpdateRequest) (serverapi.WorkflowTaskUpdateResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.UpdateWorkflowTask)
}

func (c *loopbackWorkflowClient) StartWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.StartWorkflowTask)
}

func (c *loopbackWorkflowClient) InterruptWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskInterruptRequest) (serverapi.WorkflowTaskInterruptResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.InterruptWorkflowTask)
}

func (c *loopbackWorkflowClient) ResumeWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.ResumeWorkflowTask)
}

func (c *loopbackWorkflowClient) ApproveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskApproveRequest) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.ApproveWorkflowTask)
}

func (c *loopbackWorkflowClient) MoveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.MoveWorkflowTask)
}

func (c *loopbackWorkflowClient) CompleteWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCompleteRequest) (serverapi.WorkflowTaskCompleteResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.CompleteWorkflowTask)
}

func (c *loopbackWorkflowClient) CancelWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCancelRequest) error {
	return callLoopbackClientNoResponse(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.CancelWorkflowTask)
}

func (c *loopbackWorkflowClient) DeleteWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskDeleteRequest) error {
	return callLoopbackClientNoResponse(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.DeleteWorkflowTask)
}

func (c *loopbackWorkflowClient) ListWorkflowAttention(ctx context.Context, req serverapi.WorkflowAttentionListRequest) (serverapi.WorkflowAttentionListResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.ListWorkflowAttention)
}

func (c *loopbackWorkflowClient) ListWorkflowTaskAttention(ctx context.Context, req serverapi.WorkflowTaskAttentionListRequest) (serverapi.WorkflowTaskAttentionListResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.ListWorkflowTaskAttention)
}

func (c *loopbackWorkflowClient) AnswerWorkflowTaskQuestion(ctx context.Context, req serverapi.WorkflowTaskQuestionAnswerRequest) error {
	return callLoopbackClientNoResponse(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.AnswerWorkflowTaskQuestion)
}

func (c *loopbackWorkflowClient) AddWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentAddRequest) (serverapi.WorkflowTaskCommentAddResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.AddWorkflowTaskComment)
}

func (c *loopbackWorkflowClient) ListWorkflowTaskComments(ctx context.Context, req serverapi.WorkflowTaskCommentListRequest) (serverapi.WorkflowTaskCommentListResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.ListWorkflowTaskComments)
}

func (c *loopbackWorkflowClient) ReplaceWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentReplaceRequest) error {
	return callLoopbackClientNoResponse(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.ReplaceWorkflowTaskComment)
}

func (c *loopbackWorkflowClient) DeleteWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentDeleteRequest) error {
	return callLoopbackClientNoResponse(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.DeleteWorkflowTaskComment)
}

func (c *loopbackWorkflowClient) ListWorkflowTaskActivity(ctx context.Context, req serverapi.WorkflowTaskActivityListRequest) (serverapi.WorkflowTaskActivityListResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.ListWorkflowTaskActivity)
}

func (c *loopbackWorkflowClient) ListWorkflowTasks(ctx context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.ListWorkflowTasks)
}

func (c *loopbackWorkflowClient) SubscribeWorkflowProject(ctx context.Context, req serverapi.WorkflowProjectSubscribeRequest) (serverapi.WorkflowProjectSubscription, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.SubscribeWorkflowProject)
}

func (c *loopbackWorkflowClient) SubscribeWorkflow(ctx context.Context, req serverapi.WorkflowSubscribeRequest) (serverapi.WorkflowSubscription, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.SubscribeWorkflow)
}

func (c *loopbackWorkflowClient) GetWorkflowBoard(ctx context.Context, req serverapi.WorkflowBoardRequest) (serverapi.WorkflowBoardResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.GetWorkflowBoard)
}

func (c *loopbackWorkflowClient) ListWorkflowBoardNodeCards(ctx context.Context, req serverapi.WorkflowBoardNodeCardsListRequest) (serverapi.WorkflowBoardNodeCardsListResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.ListWorkflowBoardNodeCards)
}

func (c *loopbackWorkflowClient) GetWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return callLoopbackClient(c, "workflow service is required", ctx, req, servicecontract.WorkflowService.GetWorkflowTask)
}
