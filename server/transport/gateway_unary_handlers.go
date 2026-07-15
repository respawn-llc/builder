package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/serverapi"
)

func gatewayClientCall[C any, Req any, Resp any](getClient func(GatewayDependencies) C, call func(C, context.Context, Req) (Resp, error)) gatewayUnaryHandler {
	return func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params Req) (Resp, error) {
			return call(getClient(g.deps), ctx, params)
		})
	}
}

func gatewayClientCallNoResponse[C any, Req any](getClient func(GatewayDependencies) C, call func(C, context.Context, Req) error) gatewayUnaryHandler {
	return func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params Req) (struct{}, error) {
			return struct{}{}, call(getClient(g.deps), ctx, params)
		})
	}
}

var gatewayUnaryHandlerEntries = map[string]gatewayUnaryHandler{
	protocol.MethodHandshake: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		params, err := decodeParams[protocol.HandshakeRequest](req.Params)
		if err != nil {
			return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, err.Error())
		}
		if err := params.Validate(); err != nil {
			return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, err.Error())
		}
		if params.ProtocolVersion != protocol.Version {
			return protocol.NewErrorResponse(req.ID, protocol.ErrCodeProtocolVersionMismatch, fmt.Sprintf("unsupported protocol version %q; server requires %q, upgrade the older Kent process", params.ProtocolVersion, protocol.Version))
		}
		state.handshakeDone = true
		return protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: g.identity})
	},
	protocol.MethodAuthGetBootstrapStatus: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.AuthGetBootstrapStatusRequest) (serverapi.AuthGetBootstrapStatusResponse, error) {
			bootstrapClient := g.deps.AuthBootstrapClient()
			if bootstrapClient == nil {
				return serverapi.AuthGetBootstrapStatusResponse{}, serverapi.ErrServerAuthRequired
			}
			return bootstrapClient.GetAuthBootstrapStatus(ctx, params)
		})
	},
	protocol.MethodServerReadinessGet: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.ServerReadinessRequest) (serverapi.ServerReadinessResponse, error) {
			statusClient := g.deps.ServerStatusClient()
			if statusClient == nil {
				return serverapi.ServerReadinessResponse{}, errors.New("server status client is required")
			}
			response, err := statusClient.GetServerReadiness(ctx, params)
			if err != nil {
				return serverapi.ServerReadinessResponse{}, err
			}
			response.ServerID = g.identity.ServerID
			response.ProtocolVersion = g.identity.ProtocolVersion
			return response, nil
		})
	},
	protocol.MethodAuthCompleteBootstrap: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.AuthCompleteBootstrapRequest) (serverapi.AuthCompleteBootstrapResponse, error) {
			bootstrapClient := g.deps.AuthBootstrapClient()
			if bootstrapClient == nil {
				return serverapi.AuthCompleteBootstrapResponse{}, serverapi.ErrServerAuthRequired
			}
			resp, err := bootstrapClient.CompleteAuthBootstrap(ctx, params)
			if err == nil {
				state.noAuthAccepted = resp.NoAuthSelected
			}
			return resp, err
		})
	},
	protocol.MethodAuthAcknowledgeNoAuth: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.AuthAcknowledgeNoAuthRequest) (serverapi.AuthAcknowledgeNoAuthResponse, error) {
			bootstrapClient := g.deps.AuthBootstrapClient()
			if bootstrapClient == nil {
				return serverapi.AuthAcknowledgeNoAuthResponse{}, serverapi.ErrServerAuthRequired
			}
			resp, err := bootstrapClient.AcknowledgeNoAuth(ctx, params)
			if err == nil {
				state.noAuthAccepted = resp.NoAuthSelected
			}
			return resp, err
		})
	},
	protocol.MethodAuthGetStatus: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.AuthStatusRequest) (serverapi.AuthStatusResponse, error) {
			statusClient := g.deps.AuthStatusClient()
			if statusClient == nil {
				return serverapi.AuthStatusResponse{}, serverapi.ErrServerAuthRequired
			}
			return statusClient.GetAuthStatus(ctx, params)
		})
	},
	protocol.MethodCapabilityFactsGet: gatewayClientCall[apicontract.CapabilityFactsService, serverapi.CapabilityFactsRequest, serverapi.CapabilityFactsResponse](GatewayDependencies.CapabilityFactsClient, apicontract.CapabilityFactsService.GetCapabilityFacts),
	protocol.MethodOnboardingFinalize: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		params, err := decodeParams[serverapi.OnboardingFinalizeRequest](req.Params)
		if err != nil {
			return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, err.Error())
		}
		finalizeClient := g.deps.OnboardingFinalizeClient()
		if finalizeClient == nil {
			return responseForError(req.ID, serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil))
		}
		resp, err := finalizeClient.FinalizeOnboarding(ctx, params)
		if err != nil {
			return responseForError(req.ID, err)
		}
		return protocol.NewSuccessResponse(req.ID, resp)
	},
	protocol.MethodAttachProject: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params protocol.AttachProjectRequest) (protocol.AttachResponse, error) {
			if err := params.Validate(); err != nil {
				return protocol.AttachResponse{}, err
			}
			if err := g.deps.ProjectExists(ctx, params.ProjectID); err != nil {
				return protocol.AttachResponse{}, err
			}
			attachedWorkspaceID, attachedRoot, err := g.resolveAttachedProjectWorkspace(ctx, params.ProjectID, params.WorkspaceID, params.WorkspaceRoot)
			if err != nil {
				return protocol.AttachResponse{}, err
			}
			state.attachedProject = params.ProjectID
			state.attachedWorkspaceID = attachedWorkspaceID
			state.attachedWorkspaceRoot = attachedRoot
			state.attachedSession = ""
			return protocol.AttachResponse{Kind: "project", ProjectID: params.ProjectID, WorkspaceID: attachedWorkspaceID, WorkspaceRoot: attachedRoot}, nil
		})
	},
	protocol.MethodAttachSession: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params protocol.AttachSessionRequest) (protocol.AttachResponse, error) {
			if err := params.Validate(); err != nil {
				return protocol.AttachResponse{}, err
			}
			binding, err := g.resolveSessionAttachment(ctx, state, params.SessionID)
			if err != nil {
				return protocol.AttachResponse{}, err
			}
			state.attachedProject = binding.ProjectID
			state.attachedWorkspaceID = binding.WorkspaceID
			state.attachedWorkspaceRoot = binding.CanonicalRoot
			state.attachedSession = params.SessionID
			return protocol.AttachResponse{
				Kind:          "session",
				ProjectID:     binding.ProjectID,
				WorkspaceID:   binding.WorkspaceID,
				WorkspaceRoot: binding.CanonicalRoot,
				SessionID:     params.SessionID,
			}, nil
		})
	},
	protocol.MethodProjectList:                   gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectListRequest, serverapi.ProjectListResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ListProjects),
	protocol.MethodProjectHomeList:               gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectHomeListRequest, serverapi.ProjectHomeListResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ListProjectHome),
	protocol.MethodProjectResolvePath:            gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectResolvePathRequest, serverapi.ProjectResolvePathResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ResolveProjectPath),
	protocol.MethodProjectPlanWorkspaceBinding:   gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectBindingPlanRequest, serverapi.ProjectBindingPlanResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.PlanWorkspaceBinding),
	protocol.MethodProjectCreate:                 gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectCreateRequest, serverapi.ProjectCreateResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.CreateProject),
	protocol.MethodProjectEditGet:                gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectEditGetRequest, serverapi.ProjectEditGetResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.GetProjectEdit),
	protocol.MethodProjectUpdate:                 gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectUpdateRequest, serverapi.ProjectUpdateResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.UpdateProject),
	protocol.MethodProjectSetDefaultWorkspace:    gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectDefaultWorkspaceSetRequest, serverapi.ProjectDefaultWorkspaceSetResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.SetDefaultWorkspace),
	protocol.MethodProjectWorkspaceList:          gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectWorkspaceListRequest, serverapi.ProjectWorkspaceListResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ListProjectWorkspaces),
	protocol.MethodProjectUnlinkWorkspace:        gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectWorkspaceUnlinkRequest, serverapi.ProjectWorkspaceUnlinkResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.UnlinkWorkspaceFromProject),
	protocol.MethodProjectDelete:                 gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectDeleteRequest, serverapi.ProjectDeleteResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.DeleteProject),
	protocol.MethodProjectAttachWorkspace:        gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectAttachWorkspaceRequest, serverapi.ProjectAttachWorkspaceResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.AttachWorkspaceToProject),
	protocol.MethodProjectRebindWorkspace:        gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectRebindWorkspaceRequest, serverapi.ProjectRebindWorkspaceResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.RebindWorkspace),
	protocol.MethodProjectGetOverview:            gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectGetOverviewRequest, serverapi.ProjectGetOverviewResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.GetProjectOverview),
	protocol.MethodSessionListByProject:          gatewayClientCall[apicontract.ProjectViewService, serverapi.SessionListByProjectRequest, serverapi.SessionListByProjectResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ListSessionsByProject),
	protocol.MethodWorkflowCreate:                gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowCreateRequest, serverapi.WorkflowCreateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CreateWorkflow),
	protocol.MethodWorkflowCreateAndLinkProject:  gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowCreateAndLinkProjectRequest, serverapi.WorkflowCreateAndLinkProjectResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CreateAndLinkWorkflowToProject),
	protocol.MethodWorkflowUpdate:                gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowUpdateRequest, serverapi.WorkflowGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.UpdateWorkflow),
	protocol.MethodWorkflowList:                  gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowListRequest, serverapi.WorkflowListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflows),
	protocol.MethodWorkflowGet:                   gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowGetRequest, serverapi.WorkflowGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.GetWorkflow),
	protocol.MethodWorkflowNodeGroupAdd:          gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowNodeGroupAddRequest, serverapi.WorkflowNodeGroupResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.AddWorkflowNodeGroup),
	protocol.MethodWorkflowNodeGroupUpdate:       gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowNodeGroupUpdateRequest, serverapi.WorkflowNodeGroupResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.UpdateWorkflowNodeGroup),
	protocol.MethodWorkflowNodeGroupDelete:       gatewayClientCallNoResponse[apicontract.WorkflowService, serverapi.WorkflowNodeGroupDeleteRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.DeleteWorkflowNodeGroup),
	protocol.MethodWorkflowAddNode:               gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowNodeAddRequest, serverapi.WorkflowNodeAddResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.AddWorkflowNode),
	protocol.MethodWorkflowUpdateNode:            gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowNodeUpdateRequest, serverapi.WorkflowNodeUpdateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.UpdateWorkflowNode),
	protocol.MethodWorkflowAddTransitionGroup:    gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTransitionGroupAddRequest, serverapi.WorkflowTransitionGroupAddResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.AddWorkflowTransitionGroup),
	protocol.MethodWorkflowUpdateTransitionGroup: gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTransitionGroupUpdateRequest, serverapi.WorkflowTransitionGroupUpdateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.UpdateWorkflowTransitionGroup),
	protocol.MethodWorkflowAddEdge:               gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowEdgeAddRequest, serverapi.WorkflowEdgeAddResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.AddWorkflowEdge),
	protocol.MethodWorkflowUpdateEdge:            gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowEdgeUpdateRequest, serverapi.WorkflowEdgeUpdateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.UpdateWorkflowEdge),
	protocol.MethodWorkflowLinkProject:           gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowLinkProjectRequest, serverapi.WorkflowLinkProjectResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.LinkWorkflowToProject),
	protocol.MethodWorkflowListProjectLinks:      gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowListProjectLinksRequest, serverapi.WorkflowListProjectLinksResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListProjectWorkflowLinks),
	protocol.MethodWorkflowSetDefaultProjectLink: gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowSetDefaultProjectLinkRequest, serverapi.WorkflowSetDefaultProjectLinkResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.SetDefaultProjectWorkflowLink),
	protocol.MethodWorkflowUnlinkProject:         gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowUnlinkProjectRequest, serverapi.WorkflowUnlinkProjectResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.UnlinkWorkflowFromProject),
	protocol.MethodWorkflowDeletePreview:         gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowDeletePreviewRequest, serverapi.WorkflowDeletePreviewResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.PreviewWorkflowDelete),
	protocol.MethodWorkflowDelete:                gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowDeleteRequest, serverapi.WorkflowDeleteResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.DeleteWorkflow),
	protocol.MethodWorkflowValidate:              gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowValidateRequest, serverapi.WorkflowValidateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ValidateWorkflow),
	protocol.MethodWorkflowScriptPathValidate:    gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowScriptPathValidateRequest, serverapi.WorkflowValidateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ValidateWorkflowScriptPath),
	protocol.MethodWorkflowGraphValidateDraft:    gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowGraphValidateDraftRequest, serverapi.WorkflowGraphValidateDraftResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ValidateWorkflowGraphDraft),
	protocol.MethodWorkflowGraphDeriveWiring:     gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowGraphDeriveWiringRequest, serverapi.WorkflowGraphDeriveWiringResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.DeriveWorkflowGraphWiring),
	protocol.MethodWorkflowGraphSavePreview:      gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowGraphSavePreviewRequest, serverapi.WorkflowGraphSavePreviewResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.PreviewWorkflowGraphSave),
	protocol.MethodWorkflowGraphSave:             gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowGraphSaveRequest, serverapi.WorkflowGraphSaveResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.SaveWorkflowGraph),
	protocol.MethodWorkflowTaskCreate:            gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskCreateRequest, serverapi.WorkflowTaskCreateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CreateWorkflowTask),
	protocol.MethodWorkflowTaskUpdate:            gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskUpdateRequest, serverapi.WorkflowTaskUpdateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.UpdateWorkflowTask),
	protocol.MethodWorkflowTaskStart:             gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskStartRequest, serverapi.WorkflowTaskStartResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.StartWorkflowTask),
	protocol.MethodWorkflowTaskInterrupt:         gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskInterruptRequest, serverapi.WorkflowTaskInterruptResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.InterruptWorkflowTask),
	protocol.MethodWorkflowTaskResume:            gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskResumeRequest, serverapi.WorkflowTaskResumeResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ResumeWorkflowTask),
	protocol.MethodWorkflowTaskApprove:           gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskApproveRequest, serverapi.WorkflowTaskApproveResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ApproveWorkflowTask),
	protocol.MethodWorkflowTaskMove:              gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskMoveRequest, serverapi.WorkflowTaskMoveResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.MoveWorkflowTask),
	protocol.MethodWorkflowTaskComplete:          gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskCompleteRequest, serverapi.WorkflowTaskCompleteResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CompleteWorkflowTask),
	protocol.MethodWorkflowTaskCancel:            gatewayClientCallNoResponse[apicontract.WorkflowService, serverapi.WorkflowTaskCancelRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CancelWorkflowTask),
	protocol.MethodWorkflowTaskDelete:            gatewayClientCallNoResponse[apicontract.WorkflowService, serverapi.WorkflowTaskDeleteRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.DeleteWorkflowTask),
	protocol.MethodWorkflowAttentionList:         gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowAttentionListRequest, serverapi.WorkflowAttentionListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowAttention),
	protocol.MethodWorkflowTaskAttentionList:     gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskAttentionListRequest, serverapi.WorkflowTaskAttentionListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTaskAttention),
	protocol.MethodWorkflowTaskQuestionAnswer:    gatewayClientCallNoResponse[apicontract.WorkflowService, serverapi.WorkflowTaskQuestionAnswerRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.AnswerWorkflowTaskQuestion),
	protocol.MethodWorkflowTaskCommentAdd:        gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskCommentAddRequest, serverapi.WorkflowTaskCommentAddResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.AddWorkflowTaskComment),
	protocol.MethodWorkflowTaskCommentList:       gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskCommentListRequest, serverapi.WorkflowTaskCommentListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTaskComments),
	protocol.MethodWorkflowTaskCommentReplace:    gatewayClientCallNoResponse[apicontract.WorkflowService, serverapi.WorkflowTaskCommentReplaceRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ReplaceWorkflowTaskComment),
	protocol.MethodWorkflowTaskCommentDelete:     gatewayClientCallNoResponse[apicontract.WorkflowService, serverapi.WorkflowTaskCommentDeleteRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.DeleteWorkflowTaskComment),
	protocol.MethodWorkflowTaskActivityList:      gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskActivityListRequest, serverapi.WorkflowTaskActivityListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTaskActivity),
	protocol.MethodWorkflowTaskList:              gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskListRequest, serverapi.WorkflowTaskListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTasks),
	protocol.MethodWorkflowBoardGet:              gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowBoardRequest, serverapi.WorkflowBoardResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.GetWorkflowBoard),
	protocol.MethodWorkflowBoardNodeCardsList:    gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowBoardNodeCardsListRequest, serverapi.WorkflowBoardNodeCardsListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowBoardNodeCards),
	protocol.MethodWorkflowTaskGet:               gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskGetRequest, serverapi.WorkflowTaskGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.GetWorkflowTask),
	protocol.MethodSessionPlan: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
			launchClient, err := g.sessionLaunchClientForState(ctx, state)
			if err != nil {
				return serverapi.SessionPlanResponse{}, err
			}
			return launchClient.PlanSession(ctx, params)
		})
	},
	protocol.MethodSessionGetMainView:                            gatewayClientCall[apicontract.SessionViewService, serverapi.SessionMainViewRequest, serverapi.SessionMainViewResponse](GatewayDependencies.SessionViewClient, apicontract.SessionViewService.GetSessionMainView),
	protocol.MethodSessionGetTranscriptPage:                      gatewayClientCall[apicontract.SessionViewService, serverapi.SessionTranscriptPageRequest, serverapi.SessionTranscriptPageResponse](GatewayDependencies.SessionViewClient, apicontract.SessionViewService.GetSessionTranscriptPage),
	protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer: gatewayClientCall[apicontract.SessionViewService, serverapi.SessionLatestCommittedAssistantFinalAnswerRequest, serverapi.SessionLatestCommittedAssistantFinalAnswerResponse](GatewayDependencies.SessionViewClient, apicontract.SessionViewService.GetLatestCommittedAssistantFinalAnswer),
	protocol.MethodSessionGetInitialInput:                        gatewayClientCall[apicontract.SessionLifecycleService, serverapi.SessionInitialInputRequest, serverapi.SessionInitialInputResponse](GatewayDependencies.SessionLifecycleClient, apicontract.SessionLifecycleService.GetInitialInput),
	protocol.MethodSessionPersistInputDraft:                      gatewayClientCall[apicontract.SessionLifecycleService, serverapi.SessionPersistInputDraftRequest, serverapi.SessionPersistInputDraftResponse](GatewayDependencies.SessionLifecycleClient, apicontract.SessionLifecycleService.PersistInputDraft),
	protocol.MethodSessionRetargetWorkspace:                      gatewayClientCall[apicontract.SessionLifecycleService, serverapi.SessionRetargetWorkspaceRequest, serverapi.SessionRetargetWorkspaceResponse](GatewayDependencies.SessionLifecycleClient, apicontract.SessionLifecycleService.RetargetSessionWorkspace),
	protocol.MethodSessionResolveTransition:                      gatewayClientCall[apicontract.SessionLifecycleService, serverapi.SessionResolveTransitionRequest, serverapi.SessionResolveTransitionResponse](GatewayDependencies.SessionLifecycleClient, apicontract.SessionLifecycleService.ResolveTransition),
	protocol.MethodWorktreeStatus:                                gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeStatusRequest, serverapi.WorktreeStatusResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.GetWorktreeStatus),
	protocol.MethodWorktreeList:                                  gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeListRequest, serverapi.WorktreeListResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.ListWorktrees),
	protocol.MethodWorktreeWorkspaceList:                         gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeWorkspaceListRequest, serverapi.WorktreeWorkspaceListResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.ListWorkspaceWorktrees),
	protocol.MethodWorktreeSelectorResolve:                       gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeSelectorPreviewRequest, serverapi.WorktreeSelectorPreviewResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.ResolveWorktreeSelector),
	protocol.MethodWorktreeCreateTargetResolve:                   gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeCreateTargetResolveRequest, serverapi.WorktreeCreateTargetResolveResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.ResolveWorktreeCreateTarget),
	protocol.MethodWorktreeCreate:                                gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeCreateRequest, serverapi.WorktreeCreateResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.CreateWorktree),
	protocol.MethodWorktreeEnter:                                 gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeEnterRequest, serverapi.WorktreeScheduledAcknowledgement](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.EnterWorktree),
	protocol.MethodWorktreeLeave:                                 gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeLeaveRequest, serverapi.WorktreeScheduledAcknowledgement](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.LeaveWorktree),
	protocol.MethodWorktreeDelete:                                gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeDeleteRequest, serverapi.WorktreeDeleteResult](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.DeleteWorktree),
	protocol.MethodSessionRuntimeActivate: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
			params.OwnerID = state.runtimeOwnerID
			resp, err := g.deps.SessionRuntimeClient().ActivateSessionRuntime(ctx, params)
			if err == nil {
				state.recordOwnedRuntime(params.SessionID)
			}
			return resp, err
		})
	},
	protocol.MethodSessionRuntimeRelease: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
			params.OwnerID = state.runtimeOwnerID
			resp, err := g.deps.SessionRuntimeClient().ReleaseSessionRuntime(ctx, params)
			if err == nil && (resp.Released || params.DropOwner) {
				state.removeOwnedRuntime(params.SessionID)
			}
			return resp, err
		})
	},
	protocol.MethodRuntimeSetSessionName:                 gatewayClientCallNoResponse[apicontract.RuntimeControlService, serverapi.RuntimeSetSessionNameRequest](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.SetSessionName),
	protocol.MethodRuntimeSetThinkingLevel:               gatewayClientCallNoResponse[apicontract.RuntimeControlService, serverapi.RuntimeSetThinkingLevelRequest](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.SetThinkingLevel),
	protocol.MethodRuntimeSetFastModeEnabled:             gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeSetFastModeEnabledRequest, serverapi.RuntimeSetFastModeEnabledResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.SetFastModeEnabled),
	protocol.MethodRuntimeSetReviewerEnabled:             gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeSetReviewerEnabledRequest, serverapi.RuntimeSetReviewerEnabledResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.SetReviewerEnabled),
	protocol.MethodRuntimeSetAutoCompactionEnabled:       gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeSetAutoCompactionEnabledRequest, serverapi.RuntimeSetAutoCompactionEnabledResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.SetAutoCompactionEnabled),
	protocol.MethodRuntimeSetQuestionsEnabled:            gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeSetQuestionsEnabledRequest, serverapi.RuntimeSetQuestionsEnabledResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.SetQuestionsEnabled),
	protocol.MethodRuntimeAppendCommittedEntry:           gatewayClientCallNoResponse[apicontract.RuntimeControlService, serverapi.RuntimeAppendCommittedEntryRequest](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.AppendCommittedEntry),
	protocol.MethodRuntimeShouldCompactBeforeUserMessage: gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeShouldCompactBeforeUserMessageRequest, serverapi.RuntimeShouldCompactBeforeUserMessageResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.ShouldCompactBeforeUserMessage),
	protocol.MethodRuntimeSubmitUserTurn:                 gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeSubmitUserTurnRequest, serverapi.RuntimeSubmitUserTurnResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.SubmitUserTurn),
	protocol.MethodRuntimeSubmitUserShellCommand:         gatewayClientCallNoResponse[apicontract.RuntimeControlService, serverapi.RuntimeSubmitUserShellCommandRequest](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.SubmitUserShellCommand),
	protocol.MethodRuntimeCompactContext:                 gatewayClientCallNoResponse[apicontract.RuntimeControlService, serverapi.RuntimeCompactContextRequest](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.CompactContext),
	protocol.MethodRuntimeCompactContextForPreSubmit:     gatewayClientCallNoResponse[apicontract.RuntimeControlService, serverapi.RuntimeCompactContextForPreSubmitRequest](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.CompactContextForPreSubmit),
	protocol.MethodRuntimeHasQueuedUserWork:              gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeHasQueuedUserWorkRequest, serverapi.RuntimeHasQueuedUserWorkResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.HasQueuedUserWork),
	protocol.MethodRuntimeSubmitQueuedUserMessages:       gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeSubmitQueuedUserMessagesRequest, serverapi.RuntimeSubmitQueuedUserMessagesResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.SubmitQueuedUserMessages),
	protocol.MethodRuntimeInterrupt:                      gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeInterruptRequest, serverapi.RuntimeInterruptResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.Interrupt),
	protocol.MethodRuntimeQueueUserMessage:               gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeQueueUserMessageRequest, serverapi.RuntimeQueueUserMessageResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.QueueUserMessage),
	protocol.MethodRuntimeLiveSteer:                      gatewayClientCall[apicontract.RuntimeLiveControlService, serverapi.RuntimeLiveSteerRequest, serverapi.RuntimeLiveSteerResponse](GatewayDependencies.RuntimeLiveControlClient, apicontract.RuntimeLiveControlService.LiveSteer),
	protocol.MethodRuntimeLiveStop:                       gatewayClientCall[apicontract.RuntimeLiveControlService, serverapi.RuntimeLiveStopRequest, serverapi.RuntimeLiveStopResponse](GatewayDependencies.RuntimeLiveControlClient, apicontract.RuntimeLiveControlService.LiveStop),
	protocol.MethodRuntimeLiveWait:                       gatewayClientCall[apicontract.RuntimeLiveControlService, serverapi.RuntimeLiveWaitRequest, serverapi.RuntimeLiveWaitResponse](GatewayDependencies.RuntimeLiveControlClient, apicontract.RuntimeLiveControlService.LiveWait),
	protocol.MethodRuntimeDiscardQueuedUserMessage:       gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeDiscardQueuedUserMessageRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.DiscardQueuedUserMessage),
	protocol.MethodRuntimeRecordPromptHistory:            gatewayClientCallNoResponse[apicontract.RuntimeControlService, serverapi.RuntimeRecordPromptHistoryRequest](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.RecordPromptHistory),
	protocol.MethodRuntimeGoalShow:                       gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeGoalShowRequest, serverapi.RuntimeGoalShowResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.ShowGoal),
	protocol.MethodRuntimeGoalSet:                        gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeGoalSetRequest, serverapi.RuntimeGoalShowResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.SetGoal),
	protocol.MethodRuntimeGoalPause:                      gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.PauseGoal),
	protocol.MethodRuntimeGoalResume:                     gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.ResumeGoal),
	protocol.MethodRuntimeGoalComplete:                   gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.CompleteGoal),
	protocol.MethodRuntimeGoalClear:                      gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeGoalClearRequest, serverapi.RuntimeGoalShowResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.ClearGoal),
	protocol.MethodProcessList: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.ProcessListRequest) (serverapi.ProcessListResponse, error) {
			resp, err := g.deps.ProcessViewClient().ListProcesses(ctx, params)
			if err != nil {
				return serverapi.ProcessListResponse{}, err
			}
			if strings.TrimSpace(params.OwnerSessionID) != "" {
				return resp, nil
			}
			filtered, err := g.filterProcessesForActiveProject(ctx, state, resp.Processes)
			if err != nil {
				return serverapi.ProcessListResponse{}, err
			}
			resp.Processes = filtered
			return resp, nil
		})
	},
	protocol.MethodProcessGet:          gatewayClientCall[apicontract.ProcessViewService, serverapi.ProcessGetRequest, serverapi.ProcessGetResponse](GatewayDependencies.ProcessViewClient, apicontract.ProcessViewService.GetProcess),
	protocol.MethodProcessKill:         gatewayClientCall[apicontract.ProcessControlService, serverapi.ProcessKillRequest, serverapi.ProcessKillResponse](GatewayDependencies.ProcessControlClient, apicontract.ProcessControlService.KillProcess),
	protocol.MethodProcessInlineOutput: gatewayClientCall[apicontract.ProcessControlService, serverapi.ProcessInlineOutputRequest, serverapi.ProcessInlineOutputResponse](GatewayDependencies.ProcessControlClient, apicontract.ProcessControlService.GetInlineOutput),
	protocol.MethodAskListPending:      gatewayClientCall[apicontract.AskViewService, serverapi.AskListPendingBySessionRequest, serverapi.AskListPendingBySessionResponse](GatewayDependencies.AskViewClient, apicontract.AskViewService.ListPendingAsksBySession),
	protocol.MethodAskAnswer:           gatewayClientCallNoResponse[apicontract.PromptControlService, serverapi.AskAnswerRequest](GatewayDependencies.PromptControlClient, apicontract.PromptControlService.AnswerAsk),
	protocol.MethodApprovalListPending: gatewayClientCall[apicontract.ApprovalViewService, serverapi.ApprovalListPendingBySessionRequest, serverapi.ApprovalListPendingBySessionResponse](GatewayDependencies.ApprovalViewClient, apicontract.ApprovalViewService.ListPendingApprovalsBySession),
	protocol.MethodApprovalAnswer:      gatewayClientCallNoResponse[apicontract.PromptControlService, serverapi.ApprovalAnswerRequest](GatewayDependencies.PromptControlClient, apicontract.PromptControlService.AnswerApproval),
}
