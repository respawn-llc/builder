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
		route := mustGatewayRoute(req.Method, apicontract.KindUnary)
		if _, response, failed := g.preflightRouteRequest(ctx, state, route, req); failed {
			return response
		}
		return decodeAndHandle(req, func(params Req) (Resp, error) {
			return call(getClient(g.deps), ctx, params)
		})
	}
}

func gatewayClientCallNoResponse[C any, Req any](getClient func(GatewayDependencies) C, call func(C, context.Context, Req) error) gatewayUnaryHandler {
	return func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		route := mustGatewayRoute(req.Method, apicontract.KindUnary)
		if _, response, failed := g.preflightRouteRequest(ctx, state, route, req); failed {
			return response
		}
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
		if params.ClientCapabilities != nil {
			state.clientCapabilities = *params.ClientCapabilities
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
	protocol.MethodServerUpdateStatusGet: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.UpdateStatusRequest) (serverapi.UpdateStatusResponse, error) {
			statusClient := g.deps.ServerStatusClient()
			if statusClient == nil {
				return serverapi.UpdateStatusResponse{}, errors.New("server status client is required")
			}
			return statusClient.GetUpdateStatus(ctx, params)
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
	protocol.MethodPromptCommandCatalogGet: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		params, err := decodeParams[serverapi.PromptCommandCatalogRequest](req.Params)
		if err != nil {
			return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, err.Error())
		}
		if err := params.Validate(); err != nil {
			return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, err.Error())
		}
		projectID, err := g.activeProjectID(ctx, state)
		if err != nil {
			return responseForError(req.ID, err)
		}
		workspaceRoot, err := g.promptCommandWorkspaceRootForCatalog(ctx, state, params.SessionID)
		if err != nil {
			return responseForError(req.ID, err)
		}
		catalog, err := g.deps.PromptCommandCatalogClientForProjectWorkspace(ctx, projectID, workspaceRoot)
		if err != nil {
			return responseForError(req.ID, err)
		}
		resp, err := catalog.GetPromptCommandCatalog(ctx, params)
		if err != nil {
			return responseForError(req.ID, err)
		}
		return protocol.NewSuccessResponse(req.ID, resp)
	},
	protocol.MethodOnboardingFinalize: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return executeOnboardingFinalize(g, ctx, state, req)
	},
	protocol.MethodAttachProject: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return executeAttachProject(g, ctx, state, req)
	},
	protocol.MethodAttachSession: validatedUnaryHandler(
		protocol.MethodAttachSession,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeSessionAttachment,
		handleAttachSession,
	),
	protocol.MethodProjectList:                   gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectListRequest, serverapi.ProjectListResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ListProjects),
	protocol.MethodProjectHomeList:               gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectHomeListRequest, serverapi.ProjectHomeListResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ListProjectHome),
	protocol.MethodProjectResolvePath:            gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectResolvePathRequest, serverapi.ProjectResolvePathResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ResolveProjectPath),
	protocol.MethodProjectPlanWorkspaceBinding:   gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectBindingPlanRequest, serverapi.ProjectBindingPlanResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.PlanWorkspaceBinding),
	protocol.MethodProjectCreate:                 gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectCreateRequest, serverapi.ProjectCreateResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.CreateProject),
	protocol.MethodProjectEditGet:                gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectEditGetRequest, serverapi.ProjectEditGetResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.GetProjectEdit),
	protocol.MethodProjectUpdate:                 gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectUpdateRequest, serverapi.ProjectUpdateResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.UpdateProject),
	protocol.MethodProjectSetDefaultWorkspace:    gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectDefaultWorkspaceSetRequest, serverapi.ProjectDefaultWorkspaceSetResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.SetDefaultWorkspace),
	protocol.MethodProjectWorkspaceList:          gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectWorkspaceListRequest, serverapi.ProjectWorkspaceListResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ListProjectWorkspaces),
	protocol.MethodProjectWorkspaceGet:           gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectWorkspaceGetRequest, serverapi.ProjectWorkspaceGetResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.GetProjectWorkspace),
	protocol.MethodProjectUnlinkWorkspace:        gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectWorkspaceUnlinkRequest, serverapi.ProjectWorkspaceUnlinkResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.UnlinkWorkspaceFromProject),
	protocol.MethodProjectDelete:                 gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectDeleteRequest, serverapi.ProjectDeleteResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.DeleteProject),
	protocol.MethodProjectAttachWorkspace:        gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectAttachWorkspaceRequest, serverapi.ProjectAttachWorkspaceResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.AttachWorkspaceToProject),
	protocol.MethodProjectRebindWorkspace:        gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectRebindWorkspaceRequest, serverapi.ProjectRebindWorkspaceResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.RebindWorkspace),
	protocol.MethodProjectGetOverview:            gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectGetOverviewRequest, serverapi.ProjectGetOverviewResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.GetProjectOverview),
	protocol.MethodSessionPage:                   gatewayClientCall[apicontract.ProjectViewService, serverapi.SessionPageRequest, serverapi.SessionPageResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ListSessionPage),
	protocol.MethodWorkflowCreate:                gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowCreateRequest, serverapi.WorkflowCreateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CreateWorkflow),
	protocol.MethodWorkflowCreateAndLinkProject:  gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowCreateAndLinkProjectRequest, serverapi.WorkflowCreateAndLinkProjectResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CreateAndLinkWorkflowToProject),
	protocol.MethodWorkflowUpdate:                gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowUpdateRequest, serverapi.WorkflowGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.UpdateWorkflow),
	protocol.MethodWorkflowList:                  gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowListRequest, serverapi.WorkflowListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflows),
	protocol.MethodWorkflowGet:                   gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowGetRequest, serverapi.WorkflowGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.GetWorkflow),
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
	protocol.MethodWorkflowProjectLabelCreate:    gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowProjectLabelCreateRequest, serverapi.WorkflowProjectLabelCreateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CreateWorkflowProjectLabel),
	protocol.MethodWorkflowProjectLabelList:      gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowProjectLabelCatalogRequest, serverapi.WorkflowProjectLabelCatalogResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowProjectLabels),
	protocol.MethodWorkflowProjectLabelRename:    gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowProjectLabelRenameRequest, serverapi.WorkflowProjectLabelRenameResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.RenameWorkflowProjectLabel),
	protocol.MethodWorkflowProjectLabelDelete:    gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowProjectLabelDeleteRequest, serverapi.WorkflowProjectLabelDeleteResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.DeleteWorkflowProjectLabel),
	protocol.MethodWorkflowProjectLabelReorder:   gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowProjectLabelReorderRequest, serverapi.WorkflowProjectLabelReorderResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ReorderWorkflowProjectLabels),
	protocol.MethodWorkflowTaskLabelsGet:         gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskLabelsGetRequest, serverapi.WorkflowTaskLabelsGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.GetWorkflowTaskLabels),
	protocol.MethodWorkflowTaskLabelsUpdate:      gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskLabelsUpdateRequest, serverapi.WorkflowTaskLabelsUpdateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.UpdateWorkflowTaskLabels),
	protocol.MethodWorkflowTaskCreate:            gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskCreateRequest, serverapi.WorkflowTaskCreateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CreateWorkflowTask),
	protocol.MethodWorkflowTaskDependencyAdd:     gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskDependencyAddRequest, serverapi.WorkflowTaskDependencyAddResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.AddWorkflowTaskDependency),
	protocol.MethodWorkflowTaskDependencyRemove:  gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskDependencyRemoveRequest, serverapi.WorkflowTaskDependencyRemoveResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.RemoveWorkflowTaskDependency),
	protocol.MethodWorkflowTaskDependencyList:    gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskDependencyListRequest, serverapi.WorkflowTaskDependencyListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTaskDependencies),
	protocol.MethodWorkflowTaskUpdate:            gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskUpdateRequest, serverapi.WorkflowTaskUpdateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.UpdateWorkflowTask),
	protocol.MethodWorkflowTaskStart:             gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskStartRequest, serverapi.WorkflowTaskStartResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.StartWorkflowTask),
	protocol.MethodWorkflowTaskInterrupt:         gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskInterruptRequest, serverapi.WorkflowTaskInterruptResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.InterruptWorkflowTask),
	protocol.MethodWorkflowTaskResume:            gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskResumeRequest, serverapi.WorkflowTaskResumeResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ResumeWorkflowTask),
	protocol.MethodWorkflowTaskApprove:           gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskApproveRequest, serverapi.WorkflowTaskApproveResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ApproveWorkflowTask),
	protocol.MethodWorkflowTaskMovePreview:       gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskMovePreviewRequest, serverapi.WorkflowTaskMovePreviewResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.PreviewWorkflowTaskMove),
	protocol.MethodWorkflowTaskMove:              gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskMoveRequest, serverapi.WorkflowTaskMoveResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.MoveWorkflowTask),
	protocol.MethodWorkflowTaskComplete:          gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskCompleteRequest, serverapi.WorkflowTaskCompleteResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CompleteWorkflowTask),
	protocol.MethodWorkflowTaskDelete:            gatewayClientCallNoResponse[apicontract.WorkflowService, serverapi.WorkflowTaskDeleteRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.DeleteWorkflowTask),
	protocol.MethodWorkflowAttentionList:         validatedGatewayClientCall[serverapi.WorkflowAttentionListRequest, serverapi.WorkflowAttentionListResponse, apicontract.WorkflowTrustedService](func(deps GatewayDependencies) any { return deps.WorkflowClient() }, apicontract.WorkflowTrustedService.ListWorkflowAttentionValidated),
	protocol.MethodWorkflowTaskAttentionList:     validatedGatewayClientCall[serverapi.WorkflowTaskAttentionListRequest, serverapi.WorkflowTaskAttentionListResponse, apicontract.WorkflowTrustedService](func(deps GatewayDependencies) any { return deps.WorkflowClient() }, apicontract.WorkflowTrustedService.ListWorkflowTaskAttentionValidated),
	protocol.MethodWorkflowTaskCommentAdd:        gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskCommentAddRequest, serverapi.WorkflowTaskCommentAddResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.AddWorkflowTaskComment),
	protocol.MethodWorkflowTaskCommentList:       validatedGatewayClientCall[serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskCommentListResponse, apicontract.WorkflowTrustedService](func(deps GatewayDependencies) any { return deps.WorkflowClient() }, apicontract.WorkflowTrustedService.ListWorkflowTaskCommentsValidated),
	protocol.MethodWorkflowTaskCommentReplace:    gatewayClientCallNoResponse[apicontract.WorkflowService, serverapi.WorkflowTaskCommentReplaceRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ReplaceWorkflowTaskComment),
	protocol.MethodWorkflowTaskCommentDelete:     gatewayClientCallNoResponse[apicontract.WorkflowService, serverapi.WorkflowTaskCommentDeleteRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.DeleteWorkflowTaskComment),
	protocol.MethodWorkflowTaskActivityList:      validatedGatewayClientCall[serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskActivityListResponse, apicontract.WorkflowTrustedService](func(deps GatewayDependencies) any { return deps.WorkflowClient() }, apicontract.WorkflowTrustedService.ListWorkflowTaskActivityValidated),
	protocol.MethodWorkflowTaskSessionList:       validatedGatewayClientCall[serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskSessionListResponse, apicontract.WorkflowTrustedService](func(deps GatewayDependencies) any { return deps.WorkflowClient() }, apicontract.WorkflowTrustedService.ListWorkflowTaskSessionsValidated),
	protocol.MethodWorkflowTaskList:              validatedGatewayClientCall[serverapi.WorkflowTaskListRequest, serverapi.WorkflowTaskListResponse, apicontract.WorkflowTrustedService](func(deps GatewayDependencies) any { return deps.WorkflowClient() }, apicontract.WorkflowTrustedService.ListWorkflowTasksValidated),
	protocol.MethodWorkflowTaskSearch:            validatedGatewayClientCall[serverapi.TaskSearchRequest, serverapi.TaskSearchResponse, apicontract.WorkflowTrustedService](func(deps GatewayDependencies) any { return deps.WorkflowClient() }, apicontract.WorkflowTrustedService.SearchWorkflowTasksValidated),
	protocol.MethodWorkflowBoardGet:              validatedGatewayClientCall[serverapi.WorkflowBoardRequest, serverapi.WorkflowBoardResponse, apicontract.WorkflowTrustedService](func(deps GatewayDependencies) any { return deps.WorkflowClient() }, apicontract.WorkflowTrustedService.GetWorkflowBoardValidated),
	protocol.MethodWorkflowBoardNodeCardsList:    validatedGatewayClientCall[serverapi.WorkflowBoardNodeCardsListRequest, serverapi.WorkflowBoardNodeCardsListResponse, apicontract.WorkflowTrustedService](func(deps GatewayDependencies) any { return deps.WorkflowClient() }, apicontract.WorkflowTrustedService.ListWorkflowBoardNodeCardsValidated),
	protocol.MethodWorkflowTaskGet:               validatedGatewayClientCall[serverapi.WorkflowTaskGetRequest, serverapi.WorkflowTaskGetResponse, apicontract.WorkflowTrustedService](func(deps GatewayDependencies) any { return deps.WorkflowClient() }, apicontract.WorkflowTrustedService.GetWorkflowTaskValidated),
	protocol.MethodWorkflowTaskObserve:           validatedGatewayClientCall[serverapi.WorkflowTaskObservationRequest, serverapi.WorkflowTaskObservationResponse, apicontract.WorkflowTrustedService](func(deps GatewayDependencies) any { return deps.WorkflowClient() }, apicontract.WorkflowTrustedService.ObserveWorkflowTaskValidated),
	protocol.MethodSessionPlan: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return executeSessionPlan(g, ctx, state, req)
	},
	protocol.MethodSessionWorkspaceChatDraft: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return executeWorkspaceChatDraft(g, ctx, state, req)
	},
	protocol.MethodSessionWorkspaceChatMaterialize: validatedUnaryHandler(
		protocol.MethodSessionWorkspaceChatMaterialize,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeRouteScope[serverapi.WorkspaceChatMaterializeRequest](mustGatewayRoute(protocol.MethodSessionWorkspaceChatMaterialize, apicontract.KindUnary)),
		handleWorkspaceChatMaterialize,
	),
	protocol.MethodSessionGetMainView: validatedUnaryHandler(
		protocol.MethodSessionGetMainView, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeSessionActiveProject(func(req serverapi.SessionMainViewRequest) string { return req.SessionID }),
		handleSessionMainView,
	),
	protocol.MethodSessionGetTranscriptPage: validatedUnaryHandler(
		protocol.MethodSessionGetTranscriptPage, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeSessionActiveProject(func(req serverapi.SessionTranscriptPageRequest) string { return req.SessionID }),
		handleSessionTranscriptPage,
	),
	protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer: validatedUnaryHandler(
		protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeSessionActiveProject(func(req serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) string { return req.SessionID }),
		handleLatestCommittedAssistantFinalAnswer,
	),
	protocol.MethodSessionGetExecutionEnvironment: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return executeSessionExecutionEnvironment(g, ctx, state, req)
	},
	protocol.MethodSessionGetInitialInput: validatedUnaryHandler(
		protocol.MethodSessionGetInitialInput, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeOptionalSessionActiveProject(func(req serverapi.SessionInitialInputRequest) string { return req.SessionID }),
		handleSessionInitialInput,
	),
	protocol.MethodSessionPersistInputDraft: validatedUnaryHandler(
		protocol.MethodSessionPersistInputDraft, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeSessionActiveProject(func(req serverapi.SessionPersistInputDraftRequest) string { return req.SessionID }),
		handleSessionPersistInputDraft,
	),
	protocol.MethodSessionRetargetWorkspace: validatedUnaryHandler(
		protocol.MethodSessionRetargetWorkspace, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		attachedProjectConstraint,
		handleSessionWorkspaceRetarget,
	),
	protocol.MethodSessionResolveTransition: validatedUnaryHandler(
		protocol.MethodSessionResolveTransition, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeOptionalSessionActiveProject(func(req serverapi.SessionResolveTransitionRequest) string { return req.SessionID }),
		handleSessionResolveTransition,
	),
	protocol.MethodWorktreeStatus: gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeStatusRequest, serverapi.WorktreeStatusResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.GetWorktreeStatus),
	protocol.MethodWorktreeList:   gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeListRequest, serverapi.WorktreeListResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.ListWorktrees),
	protocol.MethodWorktreeWorkspaceList: validatedUnaryHandler(
		protocol.MethodWorktreeWorkspaceList, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeProjectWorkspaceBinding,
		handleWorktreeWorkspaceList,
	),
	protocol.MethodWorktreeSelectorResolve:     gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeSelectorPreviewRequest, serverapi.WorktreeSelectorPreviewResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.ResolveWorktreeSelector),
	protocol.MethodWorktreeDeletePreview:       gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeDeletePreviewRequest, serverapi.WorktreeDeletePreviewResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.PreviewWorktreeDelete),
	protocol.MethodWorktreeCreateTargetResolve: gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeCreateTargetResolveRequest, serverapi.WorktreeCreateTargetResolveResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.ResolveWorktreeCreateTarget),
	protocol.MethodWorktreeCreate: validatedUnaryHandler(
		protocol.MethodWorktreeCreate, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeSessionActiveProject(func(req serverapi.WorktreeCreateRequest) string { return req.SessionID }),
		handleWorktreeCreate,
	),
	protocol.MethodWorktreeEnter:  gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeEnterRequest, serverapi.WorktreeScheduledAcknowledgement](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.EnterWorktree),
	protocol.MethodWorktreeLeave:  gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeLeaveRequest, serverapi.WorktreeScheduledAcknowledgement](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.LeaveWorktree),
	protocol.MethodWorktreeDelete: gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeDeleteRequest, serverapi.WorktreeDeleteResult](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.DeleteWorktree),
	protocol.MethodSessionRuntimeActivate: validatedUnaryHandler(
		protocol.MethodSessionRuntimeActivate, apicontract.SemanticValidationRequired, requestDecoderDefault, prepareSessionRuntimeActivate,
		authorizeSessionActiveProject(func(req serverapi.SessionRuntimeActivateRequest) string { return req.SessionID }),
		handleSessionRuntimeActivate,
	),
	protocol.MethodSessionRuntimeRelease: validatedUnaryHandler(
		protocol.MethodSessionRuntimeRelease, apicontract.SemanticValidationRequired, requestDecoderDefault, prepareSessionRuntimeRelease,
		authorizeSessionActiveProject(func(req serverapi.SessionRuntimeReleaseRequest) string { return req.Attachment.SessionID }),
		handleSessionRuntimeRelease,
	),
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
	protocol.MethodRuntimeInterrupt:                      gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeInterruptRequest, serverapi.RuntimeInterruptResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.Interrupt),
	protocol.MethodRuntimeLiveSteer: validatedUnaryHandler(
		protocol.MethodRuntimeLiveSteer, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		func(context.Context, *Gateway, *connectionState, apicontract.Validated[serverapi.RuntimeLiveSteerRequest]) (noAuthorizationFacts, error) {
			return noAuthorizationFacts{}, nil
		},
		handleRuntimeLiveSteer,
	),
	protocol.MethodRuntimeLiveStop: validatedUnaryHandler(
		protocol.MethodRuntimeLiveStop, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		func(context.Context, *Gateway, *connectionState, apicontract.Validated[serverapi.RuntimeLiveStopRequest]) (noAuthorizationFacts, error) {
			return noAuthorizationFacts{}, nil
		},
		handleRuntimeLiveStop,
	),
	protocol.MethodRuntimeLiveWait: validatedUnaryHandler(
		protocol.MethodRuntimeLiveWait, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		func(context.Context, *Gateway, *connectionState, apicontract.Validated[serverapi.RuntimeLiveWaitRequest]) (noAuthorizationFacts, error) {
			return noAuthorizationFacts{}, nil
		},
		handleRuntimeLiveWait,
	),
	protocol.MethodRuntimeLiveWatch: validatedUnaryHandler(
		protocol.MethodRuntimeLiveWatch, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		func(context.Context, *Gateway, *connectionState, apicontract.Validated[serverapi.RuntimeLiveWatchRequest]) (noAuthorizationFacts, error) {
			return noAuthorizationFacts{}, nil
		},
		handleRuntimeLiveWatch,
	),
	protocol.MethodRuntimeDiscardQueuedUserMessage: gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeDiscardQueuedUserMessageRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.DiscardQueuedUserMessage),
	protocol.MethodRuntimeRecordPromptHistory:      gatewayClientCallNoResponse[apicontract.RuntimeControlService, serverapi.RuntimeRecordPromptHistoryRequest](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.RecordPromptHistory),
	protocol.MethodRuntimeGoalShow: validatedUnaryHandler(
		protocol.MethodRuntimeGoalShow, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeGoalSession(func(req serverapi.RuntimeGoalShowRequest) string { return req.SessionID }),
		handleRuntimeGoalShow,
	),
	protocol.MethodRuntimeGoalSet: validatedUnaryHandler(
		protocol.MethodRuntimeGoalSet, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeGoalSession(func(req serverapi.RuntimeGoalSetRequest) string { return req.SessionID }),
		handleRuntimeGoalSet,
	),
	protocol.MethodRuntimeGoalPause: validatedUnaryHandler(
		protocol.MethodRuntimeGoalPause, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeGoalSession(func(req serverapi.RuntimeGoalStatusRequest) string { return req.SessionID }),
		handleRuntimeGoalPause,
	),
	protocol.MethodRuntimeGoalResume: validatedUnaryHandler(
		protocol.MethodRuntimeGoalResume, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeGoalSession(func(req serverapi.RuntimeGoalStatusRequest) string { return req.SessionID }),
		handleRuntimeGoalResume,
	),
	protocol.MethodRuntimeGoalComplete: validatedUnaryHandler(
		protocol.MethodRuntimeGoalComplete, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeGoalSession(func(req serverapi.RuntimeGoalStatusRequest) string { return req.SessionID }),
		handleRuntimeGoalComplete,
	),
	protocol.MethodRuntimeGoalClear: validatedUnaryHandler(
		protocol.MethodRuntimeGoalClear, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeGoalSession(func(req serverapi.RuntimeGoalClearRequest) string { return req.SessionID }),
		handleRuntimeGoalClear,
	),
	protocol.MethodProcessList: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.ProcessListRequest) (serverapi.ProcessListResponse, error) {
			if strings.TrimSpace(params.OwnerSessionID) != "" {
				if err := g.requireSessionInActiveProject(ctx, state, params.OwnerSessionID); err != nil {
					return serverapi.ProcessListResponse{}, err
				}
			}
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
	protocol.MethodProcessGet: validatedUnaryHandler(
		protocol.MethodProcessGet,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeProcessActiveProject(func(req serverapi.ProcessGetRequest) string { return req.ProcessID }),
		handleProcessGet,
	),
	protocol.MethodProcessKill: validatedUnaryHandler(
		protocol.MethodProcessKill,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeProcessActiveProject(func(req serverapi.ProcessKillRequest) string { return req.ProcessID }),
		handleProcessKill,
	),
	protocol.MethodProcessInlineOutput: validatedUnaryHandler(
		protocol.MethodProcessInlineOutput,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeProcessActiveProject(func(req serverapi.ProcessInlineOutputRequest) string { return req.ProcessID }),
		handleProcessInlineOutput,
	),
	protocol.MethodAskListPending: validatedUnaryHandler(
		protocol.MethodAskListPending,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeSessionActiveProject(func(req serverapi.AskListPendingBySessionRequest) string { return req.SessionID }),
		handleAskListPending,
	),
	protocol.MethodPromptAnswerBatch: validatedUnaryHandler(
		protocol.MethodPromptAnswerBatch,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeSessionActiveProject(func(req serverapi.PromptAnswerBatchRequest) string { return req.SessionID.String() }),
		handlePromptAnswerBatch,
	),
	protocol.MethodApprovalListPending: validatedUnaryHandler(
		protocol.MethodApprovalListPending,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeSessionActiveProject(func(req serverapi.ApprovalListPendingBySessionRequest) string { return req.SessionID }),
		handleApprovalListPending,
	),
}
