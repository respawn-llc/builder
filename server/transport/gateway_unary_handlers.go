package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/shared/client"
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
			return bootstrapClient.CompleteAuthBootstrap(ctx, params)
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
			state.attachedWorkspaceID = ""
			state.attachedWorkspaceRoot = ""
			state.attachedSession = params.SessionID
			return protocol.AttachResponse{Kind: "session", SessionID: params.SessionID}, nil
		})
	},
	protocol.MethodProjectList:                   gatewayClientCall[client.ProjectViewClient, serverapi.ProjectListRequest, serverapi.ProjectListResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.ListProjects),
	protocol.MethodProjectHomeList:               gatewayClientCall[client.ProjectViewClient, serverapi.ProjectHomeListRequest, serverapi.ProjectHomeListResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.ListProjectHome),
	protocol.MethodProjectResolvePath:            gatewayClientCall[client.ProjectViewClient, serverapi.ProjectResolvePathRequest, serverapi.ProjectResolvePathResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.ResolveProjectPath),
	protocol.MethodProjectPlanWorkspaceBinding:   gatewayClientCall[client.ProjectViewClient, serverapi.ProjectBindingPlanRequest, serverapi.ProjectBindingPlanResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.PlanWorkspaceBinding),
	protocol.MethodProjectCreate:                 gatewayClientCall[client.ProjectViewClient, serverapi.ProjectCreateRequest, serverapi.ProjectCreateResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.CreateProject),
	protocol.MethodProjectEditGet:                gatewayClientCall[client.ProjectViewClient, serverapi.ProjectEditGetRequest, serverapi.ProjectEditGetResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.GetProjectEdit),
	protocol.MethodProjectUpdate:                 gatewayClientCall[client.ProjectViewClient, serverapi.ProjectUpdateRequest, serverapi.ProjectUpdateResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.UpdateProject),
	protocol.MethodProjectSetDefaultWorkspace:    gatewayClientCall[client.ProjectViewClient, serverapi.ProjectDefaultWorkspaceSetRequest, serverapi.ProjectDefaultWorkspaceSetResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.SetDefaultWorkspace),
	protocol.MethodProjectWorkspaceList:          gatewayClientCall[client.ProjectViewClient, serverapi.ProjectWorkspaceListRequest, serverapi.ProjectWorkspaceListResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.ListProjectWorkspaces),
	protocol.MethodProjectUnlinkWorkspace:        gatewayClientCall[client.ProjectViewClient, serverapi.ProjectWorkspaceUnlinkRequest, serverapi.ProjectWorkspaceUnlinkResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.UnlinkWorkspaceFromProject),
	protocol.MethodProjectDelete:                 gatewayClientCall[client.ProjectViewClient, serverapi.ProjectDeleteRequest, serverapi.ProjectDeleteResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.DeleteProject),
	protocol.MethodProjectAttachWorkspace:        gatewayClientCall[client.ProjectViewClient, serverapi.ProjectAttachWorkspaceRequest, serverapi.ProjectAttachWorkspaceResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.AttachWorkspaceToProject),
	protocol.MethodProjectRebindWorkspace:        gatewayClientCall[client.ProjectViewClient, serverapi.ProjectRebindWorkspaceRequest, serverapi.ProjectRebindWorkspaceResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.RebindWorkspace),
	protocol.MethodProjectGetOverview:            gatewayClientCall[client.ProjectViewClient, serverapi.ProjectGetOverviewRequest, serverapi.ProjectGetOverviewResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.GetProjectOverview),
	protocol.MethodSessionListByProject:          gatewayClientCall[client.ProjectViewClient, serverapi.SessionListByProjectRequest, serverapi.SessionListByProjectResponse](GatewayDependencies.ProjectViewClient, client.ProjectViewClient.ListSessionsByProject),
	protocol.MethodWorkflowCreate:                gatewayClientCall[client.WorkflowClient, serverapi.WorkflowCreateRequest, serverapi.WorkflowCreateResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.CreateWorkflow),
	protocol.MethodWorkflowCreateAndLinkProject:  gatewayClientCall[client.WorkflowClient, serverapi.WorkflowCreateAndLinkProjectRequest, serverapi.WorkflowCreateAndLinkProjectResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.CreateAndLinkWorkflowToProject),
	protocol.MethodWorkflowUpdate:                gatewayClientCall[client.WorkflowClient, serverapi.WorkflowUpdateRequest, serverapi.WorkflowGetResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.UpdateWorkflow),
	protocol.MethodWorkflowList:                  gatewayClientCall[client.WorkflowClient, serverapi.WorkflowListRequest, serverapi.WorkflowListResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.ListWorkflows),
	protocol.MethodWorkflowGet:                   gatewayClientCall[client.WorkflowClient, serverapi.WorkflowGetRequest, serverapi.WorkflowGetResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.GetWorkflow),
	protocol.MethodWorkflowNodeGroupAdd:          gatewayClientCall[client.WorkflowClient, serverapi.WorkflowNodeGroupAddRequest, serverapi.WorkflowNodeGroupResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.AddWorkflowNodeGroup),
	protocol.MethodWorkflowNodeGroupUpdate:       gatewayClientCall[client.WorkflowClient, serverapi.WorkflowNodeGroupUpdateRequest, serverapi.WorkflowNodeGroupResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.UpdateWorkflowNodeGroup),
	protocol.MethodWorkflowNodeGroupDelete:       gatewayClientCallNoResponse[client.WorkflowClient, serverapi.WorkflowNodeGroupDeleteRequest](GatewayDependencies.WorkflowClient, client.WorkflowClient.DeleteWorkflowNodeGroup),
	protocol.MethodWorkflowAddNode:               gatewayClientCall[client.WorkflowClient, serverapi.WorkflowNodeAddRequest, serverapi.WorkflowNodeAddResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.AddWorkflowNode),
	protocol.MethodWorkflowUpdateNode:            gatewayClientCall[client.WorkflowClient, serverapi.WorkflowNodeUpdateRequest, serverapi.WorkflowNodeUpdateResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.UpdateWorkflowNode),
	protocol.MethodWorkflowAddTransitionGroup:    gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTransitionGroupAddRequest, serverapi.WorkflowTransitionGroupAddResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.AddWorkflowTransitionGroup),
	protocol.MethodWorkflowUpdateTransitionGroup: gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTransitionGroupUpdateRequest, serverapi.WorkflowTransitionGroupUpdateResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.UpdateWorkflowTransitionGroup),
	protocol.MethodWorkflowAddEdge:               gatewayClientCall[client.WorkflowClient, serverapi.WorkflowEdgeAddRequest, serverapi.WorkflowEdgeAddResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.AddWorkflowEdge),
	protocol.MethodWorkflowUpdateEdge:            gatewayClientCall[client.WorkflowClient, serverapi.WorkflowEdgeUpdateRequest, serverapi.WorkflowEdgeUpdateResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.UpdateWorkflowEdge),
	protocol.MethodWorkflowLinkProject:           gatewayClientCall[client.WorkflowClient, serverapi.WorkflowLinkProjectRequest, serverapi.WorkflowLinkProjectResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.LinkWorkflowToProject),
	protocol.MethodWorkflowListProjectLinks:      gatewayClientCall[client.WorkflowClient, serverapi.WorkflowListProjectLinksRequest, serverapi.WorkflowListProjectLinksResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.ListProjectWorkflowLinks),
	protocol.MethodWorkflowSetDefaultProjectLink: gatewayClientCall[client.WorkflowClient, serverapi.WorkflowSetDefaultProjectLinkRequest, serverapi.WorkflowSetDefaultProjectLinkResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.SetDefaultProjectWorkflowLink),
	protocol.MethodWorkflowUnlinkProject:         gatewayClientCall[client.WorkflowClient, serverapi.WorkflowUnlinkProjectRequest, serverapi.WorkflowUnlinkProjectResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.UnlinkWorkflowFromProject),
	protocol.MethodWorkflowDeletePreview:         gatewayClientCall[client.WorkflowClient, serverapi.WorkflowDeletePreviewRequest, serverapi.WorkflowDeletePreviewResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.PreviewWorkflowDelete),
	protocol.MethodWorkflowDelete:                gatewayClientCall[client.WorkflowClient, serverapi.WorkflowDeleteRequest, serverapi.WorkflowDeleteResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.DeleteWorkflow),
	protocol.MethodWorkflowValidate:              gatewayClientCall[client.WorkflowClient, serverapi.WorkflowValidateRequest, serverapi.WorkflowValidateResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.ValidateWorkflow),
	protocol.MethodWorkflowGraphValidateDraft:    gatewayClientCall[client.WorkflowClient, serverapi.WorkflowGraphValidateDraftRequest, serverapi.WorkflowGraphValidateDraftResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.ValidateWorkflowGraphDraft),
	protocol.MethodWorkflowGraphDeriveWiring:     gatewayClientCall[client.WorkflowClient, serverapi.WorkflowGraphDeriveWiringRequest, serverapi.WorkflowGraphDeriveWiringResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.DeriveWorkflowGraphWiring),
	protocol.MethodWorkflowGraphSavePreview:      gatewayClientCall[client.WorkflowClient, serverapi.WorkflowGraphSavePreviewRequest, serverapi.WorkflowGraphSavePreviewResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.PreviewWorkflowGraphSave),
	protocol.MethodWorkflowGraphSave:             gatewayClientCall[client.WorkflowClient, serverapi.WorkflowGraphSaveRequest, serverapi.WorkflowGraphSaveResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.SaveWorkflowGraph),
	protocol.MethodWorkflowTaskCreate:            gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTaskCreateRequest, serverapi.WorkflowTaskCreateResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.CreateWorkflowTask),
	protocol.MethodWorkflowTaskUpdate:            gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTaskUpdateRequest, serverapi.WorkflowTaskUpdateResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.UpdateWorkflowTask),
	protocol.MethodWorkflowTaskStart:             gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTaskStartRequest, serverapi.WorkflowTaskStartResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.StartWorkflowTask),
	protocol.MethodWorkflowTaskInterrupt:         gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTaskInterruptRequest, serverapi.WorkflowTaskInterruptResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.InterruptWorkflowTask),
	protocol.MethodWorkflowTaskResume:            gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTaskResumeRequest, serverapi.WorkflowTaskResumeResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.ResumeWorkflowTask),
	protocol.MethodWorkflowTaskApprove:           gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTaskApproveRequest, serverapi.WorkflowTaskApproveResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.ApproveWorkflowTask),
	protocol.MethodWorkflowTaskMove:              gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTaskMoveRequest, serverapi.WorkflowTaskMoveResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.MoveWorkflowTask),
	protocol.MethodWorkflowTaskComplete:          gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTaskCompleteRequest, serverapi.WorkflowTaskCompleteResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.CompleteWorkflowTask),
	protocol.MethodWorkflowTaskCancel:            gatewayClientCallNoResponse[client.WorkflowClient, serverapi.WorkflowTaskCancelRequest](GatewayDependencies.WorkflowClient, client.WorkflowClient.CancelWorkflowTask),
	protocol.MethodWorkflowTaskDelete:            gatewayClientCallNoResponse[client.WorkflowClient, serverapi.WorkflowTaskDeleteRequest](GatewayDependencies.WorkflowClient, client.WorkflowClient.DeleteWorkflowTask),
	protocol.MethodWorkflowAttentionList:         gatewayClientCall[client.WorkflowClient, serverapi.WorkflowAttentionListRequest, serverapi.WorkflowAttentionListResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.ListWorkflowAttention),
	protocol.MethodWorkflowTaskAttentionList:     gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTaskAttentionListRequest, serverapi.WorkflowTaskAttentionListResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.ListWorkflowTaskAttention),
	protocol.MethodWorkflowTaskQuestionAnswer:    gatewayClientCallNoResponse[client.WorkflowClient, serverapi.WorkflowTaskQuestionAnswerRequest](GatewayDependencies.WorkflowClient, client.WorkflowClient.AnswerWorkflowTaskQuestion),
	protocol.MethodWorkflowTaskCommentAdd:        gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTaskCommentAddRequest, serverapi.WorkflowTaskCommentAddResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.AddWorkflowTaskComment),
	protocol.MethodWorkflowTaskCommentList:       gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTaskCommentListRequest, serverapi.WorkflowTaskCommentListResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.ListWorkflowTaskComments),
	protocol.MethodWorkflowTaskCommentReplace:    gatewayClientCallNoResponse[client.WorkflowClient, serverapi.WorkflowTaskCommentReplaceRequest](GatewayDependencies.WorkflowClient, client.WorkflowClient.ReplaceWorkflowTaskComment),
	protocol.MethodWorkflowTaskCommentDelete:     gatewayClientCallNoResponse[client.WorkflowClient, serverapi.WorkflowTaskCommentDeleteRequest](GatewayDependencies.WorkflowClient, client.WorkflowClient.DeleteWorkflowTaskComment),
	protocol.MethodWorkflowTaskActivityList:      gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTaskActivityListRequest, serverapi.WorkflowTaskActivityListResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.ListWorkflowTaskActivity),
	protocol.MethodWorkflowTaskList:              gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTaskListRequest, serverapi.WorkflowTaskListResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.ListWorkflowTasks),
	protocol.MethodWorkflowBoardGet:              gatewayClientCall[client.WorkflowClient, serverapi.WorkflowBoardRequest, serverapi.WorkflowBoardResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.GetWorkflowBoard),
	protocol.MethodWorkflowBoardNodeCardsList:    gatewayClientCall[client.WorkflowClient, serverapi.WorkflowBoardNodeCardsListRequest, serverapi.WorkflowBoardNodeCardsListResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.ListWorkflowBoardNodeCards),
	protocol.MethodWorkflowTaskGet:               gatewayClientCall[client.WorkflowClient, serverapi.WorkflowTaskGetRequest, serverapi.WorkflowTaskGetResponse](GatewayDependencies.WorkflowClient, client.WorkflowClient.GetWorkflowTask),
	protocol.MethodSessionPlan: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
			launchClient, err := g.sessionLaunchClientForState(ctx, state)
			if err != nil {
				return serverapi.SessionPlanResponse{}, err
			}
			return launchClient.PlanSession(ctx, params)
		})
	},
	protocol.MethodSessionGetMainView:       gatewayClientCall[client.SessionViewClient, serverapi.SessionMainViewRequest, serverapi.SessionMainViewResponse](GatewayDependencies.SessionViewClient, client.SessionViewClient.GetSessionMainView),
	protocol.MethodSessionGetTranscriptPage: gatewayClientCall[client.SessionViewClient, serverapi.SessionTranscriptPageRequest, serverapi.SessionTranscriptPageResponse](GatewayDependencies.SessionViewClient, client.SessionViewClient.GetSessionTranscriptPage),
	protocol.MethodSessionGetCommittedTranscriptSuffix: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.SessionCommittedTranscriptSuffixRequest) (serverapi.SessionCommittedTranscriptSuffixResponse, error) {
			suffixClient, ok := g.deps.SessionViewClient().(client.SessionCommittedTranscriptSuffixClient)
			if !ok {
				return serverapi.SessionCommittedTranscriptSuffixResponse{}, errors.New("session committed transcript suffix client is required")
			}
			return suffixClient.GetSessionCommittedTranscriptSuffix(ctx, params)
		})
	},
	protocol.MethodSessionGetInitialInput:      gatewayClientCall[client.SessionLifecycleClient, serverapi.SessionInitialInputRequest, serverapi.SessionInitialInputResponse](GatewayDependencies.SessionLifecycleClient, client.SessionLifecycleClient.GetInitialInput),
	protocol.MethodSessionPersistInputDraft:    gatewayClientCall[client.SessionLifecycleClient, serverapi.SessionPersistInputDraftRequest, serverapi.SessionPersistInputDraftResponse](GatewayDependencies.SessionLifecycleClient, client.SessionLifecycleClient.PersistInputDraft),
	protocol.MethodSessionRetargetWorkspace:    gatewayClientCall[client.SessionLifecycleClient, serverapi.SessionRetargetWorkspaceRequest, serverapi.SessionRetargetWorkspaceResponse](GatewayDependencies.SessionLifecycleClient, client.SessionLifecycleClient.RetargetSessionWorkspace),
	protocol.MethodSessionResolveTransition:    gatewayClientCall[client.SessionLifecycleClient, serverapi.SessionResolveTransitionRequest, serverapi.SessionResolveTransitionResponse](GatewayDependencies.SessionLifecycleClient, client.SessionLifecycleClient.ResolveTransition),
	protocol.MethodWorktreeList:                gatewayClientCall[client.WorktreeClient, serverapi.WorktreeListRequest, serverapi.WorktreeListResponse](GatewayDependencies.WorktreeClient, client.WorktreeClient.ListWorktrees),
	protocol.MethodWorktreeCreateTargetResolve: gatewayClientCall[client.WorktreeClient, serverapi.WorktreeCreateTargetResolveRequest, serverapi.WorktreeCreateTargetResolveResponse](GatewayDependencies.WorktreeClient, client.WorktreeClient.ResolveWorktreeCreateTarget),
	protocol.MethodWorktreeCreate:              gatewayClientCall[client.WorktreeClient, serverapi.WorktreeCreateRequest, serverapi.WorktreeCreateResponse](GatewayDependencies.WorktreeClient, client.WorktreeClient.CreateWorktree),
	protocol.MethodWorktreeSwitch:              gatewayClientCall[client.WorktreeClient, serverapi.WorktreeSwitchRequest, serverapi.WorktreeSwitchResponse](GatewayDependencies.WorktreeClient, client.WorktreeClient.SwitchWorktree),
	protocol.MethodWorktreeDelete:              gatewayClientCall[client.WorktreeClient, serverapi.WorktreeDeleteRequest, serverapi.WorktreeDeleteResponse](GatewayDependencies.WorktreeClient, client.WorktreeClient.DeleteWorktree),
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
			if err == nil {
				state.removeOwnedRuntime(params.SessionID)
			}
			return resp, err
		})
	},
	protocol.MethodRuntimeSetSessionName:                 gatewayClientCallNoResponse[client.RuntimeControlClient, serverapi.RuntimeSetSessionNameRequest](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.SetSessionName),
	protocol.MethodRuntimeSetThinkingLevel:               gatewayClientCallNoResponse[client.RuntimeControlClient, serverapi.RuntimeSetThinkingLevelRequest](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.SetThinkingLevel),
	protocol.MethodRuntimeSetFastModeEnabled:             gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeSetFastModeEnabledRequest, serverapi.RuntimeSetFastModeEnabledResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.SetFastModeEnabled),
	protocol.MethodRuntimeSetReviewerEnabled:             gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeSetReviewerEnabledRequest, serverapi.RuntimeSetReviewerEnabledResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.SetReviewerEnabled),
	protocol.MethodRuntimeSetAutoCompactionEnabled:       gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeSetAutoCompactionEnabledRequest, serverapi.RuntimeSetAutoCompactionEnabledResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.SetAutoCompactionEnabled),
	protocol.MethodRuntimeSetQuestionsEnabled:            gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeSetQuestionsEnabledRequest, serverapi.RuntimeSetQuestionsEnabledResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.SetQuestionsEnabled),
	protocol.MethodRuntimeAppendCommittedEntry:           gatewayClientCallNoResponse[client.RuntimeControlClient, serverapi.RuntimeAppendCommittedEntryRequest](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.AppendCommittedEntry),
	protocol.MethodRuntimeShouldCompactBeforeUserMessage: gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeShouldCompactBeforeUserMessageRequest, serverapi.RuntimeShouldCompactBeforeUserMessageResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.ShouldCompactBeforeUserMessage),
	protocol.MethodRuntimeSubmitUserMessage:              gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeSubmitUserMessageRequest, serverapi.RuntimeSubmitUserMessageResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.SubmitUserMessage),
	protocol.MethodRuntimeSubmitUserTurn:                 gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeSubmitUserTurnRequest, serverapi.RuntimeSubmitUserTurnResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.SubmitUserTurn),
	protocol.MethodRuntimeSubmitUserShellCommand:         gatewayClientCallNoResponse[client.RuntimeControlClient, serverapi.RuntimeSubmitUserShellCommandRequest](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.SubmitUserShellCommand),
	protocol.MethodRuntimeCompactContext:                 gatewayClientCallNoResponse[client.RuntimeControlClient, serverapi.RuntimeCompactContextRequest](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.CompactContext),
	protocol.MethodRuntimeCompactContextForPreSubmit:     gatewayClientCallNoResponse[client.RuntimeControlClient, serverapi.RuntimeCompactContextForPreSubmitRequest](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.CompactContextForPreSubmit),
	protocol.MethodRuntimeHasQueuedUserWork:              gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeHasQueuedUserWorkRequest, serverapi.RuntimeHasQueuedUserWorkResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.HasQueuedUserWork),
	protocol.MethodRuntimeSubmitQueuedUserMessages:       gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeSubmitQueuedUserMessagesRequest, serverapi.RuntimeSubmitQueuedUserMessagesResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.SubmitQueuedUserMessages),
	protocol.MethodRuntimeInterrupt:                      gatewayClientCallNoResponse[client.RuntimeControlClient, serverapi.RuntimeInterruptRequest](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.Interrupt),
	protocol.MethodRuntimeQueueUserMessage:               gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeQueueUserMessageRequest, serverapi.RuntimeQueueUserMessageResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.QueueUserMessage),
	protocol.MethodRuntimeDiscardQueuedUserMessage:       gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeDiscardQueuedUserMessageRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.DiscardQueuedUserMessage),
	protocol.MethodRuntimeRecordPromptHistory:            gatewayClientCallNoResponse[client.RuntimeControlClient, serverapi.RuntimeRecordPromptHistoryRequest](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.RecordPromptHistory),
	protocol.MethodRuntimeGoalShow:                       gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeGoalShowRequest, serverapi.RuntimeGoalShowResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.ShowGoal),
	protocol.MethodRuntimeGoalSet:                        gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeGoalSetRequest, serverapi.RuntimeGoalShowResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.SetGoal),
	protocol.MethodRuntimeGoalPause:                      gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.PauseGoal),
	protocol.MethodRuntimeGoalResume:                     gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.ResumeGoal),
	protocol.MethodRuntimeGoalComplete:                   gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.CompleteGoal),
	protocol.MethodRuntimeGoalClear:                      gatewayClientCall[client.RuntimeControlClient, serverapi.RuntimeGoalClearRequest, serverapi.RuntimeGoalShowResponse](GatewayDependencies.RuntimeControlClient, client.RuntimeControlClient.ClearGoal),
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
	protocol.MethodProcessGet:          gatewayClientCall[client.ProcessViewClient, serverapi.ProcessGetRequest, serverapi.ProcessGetResponse](GatewayDependencies.ProcessViewClient, client.ProcessViewClient.GetProcess),
	protocol.MethodProcessKill:         gatewayClientCall[client.ProcessControlClient, serverapi.ProcessKillRequest, serverapi.ProcessKillResponse](GatewayDependencies.ProcessControlClient, client.ProcessControlClient.KillProcess),
	protocol.MethodProcessInlineOutput: gatewayClientCall[client.ProcessControlClient, serverapi.ProcessInlineOutputRequest, serverapi.ProcessInlineOutputResponse](GatewayDependencies.ProcessControlClient, client.ProcessControlClient.GetInlineOutput),
	protocol.MethodAskListPending:      gatewayClientCall[client.AskViewClient, serverapi.AskListPendingBySessionRequest, serverapi.AskListPendingBySessionResponse](GatewayDependencies.AskViewClient, client.AskViewClient.ListPendingAsksBySession),
	protocol.MethodAskAnswer:           gatewayClientCallNoResponse[client.PromptControlClient, serverapi.AskAnswerRequest](GatewayDependencies.PromptControlClient, client.PromptControlClient.AnswerAsk),
	protocol.MethodApprovalListPending: gatewayClientCall[client.ApprovalViewClient, serverapi.ApprovalListPendingBySessionRequest, serverapi.ApprovalListPendingBySessionResponse](GatewayDependencies.ApprovalViewClient, client.ApprovalViewClient.ListPendingApprovalsBySession),
	protocol.MethodApprovalAnswer:      gatewayClientCallNoResponse[client.PromptControlClient, serverapi.ApprovalAnswerRequest](GatewayDependencies.PromptControlClient, client.PromptControlClient.AnswerApproval),
}
