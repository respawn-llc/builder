package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/chatcontext"
	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func gatewayClientCall[C any, Req any, Resp any](getClient func(GatewayDependencies) C, call func(C, context.Context, Req) (Resp, error)) gatewayUnaryHandler {
	return func(g *Gateway, ctx context.Context, _ *connectionState, req protocol.Request) protocol.Response {
		return decodeOwnerAndHandle(req, func(params Req) (Resp, error) {
			return call(getClient(g.deps), ctx, params)
		})
	}
}

func gatewayClientCallNoResponse[C any, Req any](getClient func(GatewayDependencies) C, call func(C, context.Context, Req) error) gatewayUnaryHandler {
	return func(g *Gateway, ctx context.Context, _ *connectionState, req protocol.Request) protocol.Response {
		return decodeOwnerAndHandle(req, func(params Req) (struct{}, error) {
			return struct{}{}, call(getClient(g.deps), ctx, params)
		})
	}
}

func gatewaySessionTrustedCall[Raw any, Trusted any, Req any, Resp any](
	getClient func(GatewayDependencies) Raw,
	sessionID func(Req) string,
	call func(Trusted, context.Context, apicontract.Validated[Req], apicontract.AuthorizedSessionInActiveProject) (Resp, error),
) gatewayUnaryHandler {
	return func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[Req]) (Resp, error) {
			var zero Resp
			authorization, err := authorizeSessionActiveProject(sessionID)(ctx, g, state, validated)
			if err != nil {
				return zero, err
			}
			trusted, ok := any(getClient(g.deps)).(Trusted)
			if !ok {
				return zero, fmt.Errorf("%T does not implement the trusted Session route owner", getClient(g.deps))
			}
			return call(trusted, ctx, validated, authorization)
		})
	}
}

func gatewaySessionTrustedCallNoResponse[Raw any, Trusted any, Req any](
	getClient func(GatewayDependencies) Raw,
	sessionID func(Req) string,
	call func(Trusted, context.Context, apicontract.Validated[Req], apicontract.AuthorizedSessionInActiveProject) error,
) gatewayUnaryHandler {
	return gatewaySessionTrustedCall(
		getClient,
		sessionID,
		func(trusted Trusted, ctx context.Context, validated apicontract.Validated[Req], authorization apicontract.AuthorizedSessionInActiveProject) (struct{}, error) {
			return struct{}{}, call(trusted, ctx, validated, authorization)
		},
	)
}

func gatewaySessionIdentityTrustedCall[Raw any, Trusted any, Req any, Resp any](
	getClient func(GatewayDependencies) Raw,
	sessionID func(Req) string,
	call func(Trusted, context.Context, apicontract.Validated[Req], runtimeids.SessionID) (Resp, error),
) gatewayUnaryHandler {
	return func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[Req]) (Resp, error) {
			var zero Resp
			authorization, err := authorizeSessionActiveProject(sessionID)(ctx, g, state, validated)
			if err != nil {
				return zero, err
			}
			trusted, ok := any(getClient(g.deps)).(Trusted)
			if !ok {
				return zero, fmt.Errorf("%T does not implement the trusted Session identity owner", getClient(g.deps))
			}
			return call(trusted, ctx, validated, authorization.SessionID)
		})
	}
}

func gatewayGoalTrustedCall[Req any, Resp any](
	sessionID func(Req) runtimeids.SessionID,
	call func(apicontract.RuntimeGoalTrustedService, context.Context, apicontract.Validated[Req], runtimeids.SessionID) (Resp, error),
) gatewayUnaryHandler {
	return func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[Req]) (Resp, error) {
			var zero Resp
			authorizedSessionID, err := authorizeGoalSession(ctx, g, state, sessionID(validated.Value()))
			if err != nil {
				return zero, err
			}
			trusted, ok := g.deps.RuntimeControlClient().(apicontract.RuntimeGoalTrustedService)
			if !ok {
				return zero, errors.New("Runtime Goal trusted service is required")
			}
			return call(trusted, ctx, validated, authorizedSessionID)
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
		return decodeOwnerAndHandle(req, func(params serverapi.AuthGetBootstrapStatusRequest) (serverapi.AuthGetBootstrapStatusResponse, error) {
			bootstrapClient := g.deps.AuthBootstrapClient()
			if bootstrapClient == nil {
				return serverapi.AuthGetBootstrapStatusResponse{}, serverapi.ErrServerAuthRequired
			}
			return bootstrapClient.GetAuthBootstrapStatus(ctx, params)
		})
	},
	protocol.MethodServerReadinessGet: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeOwnerAndHandle(req, func(params serverapi.ServerReadinessRequest) (serverapi.ServerReadinessResponse, error) {
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
		return decodeOwnerAndHandle(req, func(params serverapi.UpdateStatusRequest) (serverapi.UpdateStatusResponse, error) {
			statusClient := g.deps.ServerStatusClient()
			if statusClient == nil {
				return serverapi.UpdateStatusResponse{}, errors.New("server status client is required")
			}
			return statusClient.GetUpdateStatus(ctx, params)
		})
	},
	protocol.MethodAuthCompleteBootstrap: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeOwnerAndHandle(req, func(params serverapi.AuthCompleteBootstrapRequest) (serverapi.AuthCompleteBootstrapResponse, error) {
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
		return decodeOwnerAndHandle(req, func(params serverapi.AuthAcknowledgeNoAuthRequest) (serverapi.AuthAcknowledgeNoAuthResponse, error) {
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
		return decodeOwnerAndHandle(req, func(params serverapi.AuthStatusRequest) (serverapi.AuthStatusResponse, error) {
			statusClient := g.deps.AuthStatusClient()
			if statusClient == nil {
				return serverapi.AuthStatusResponse{}, serverapi.ErrServerAuthRequired
			}
			return statusClient.GetAuthStatus(ctx, params)
		})
	},
	protocol.MethodCapabilityFactsGet: gatewayClientCall[apicontract.CapabilityFactsService, serverapi.CapabilityFactsRequest, serverapi.CapabilityFactsResponse](GatewayDependencies.CapabilityFactsClient, apicontract.CapabilityFactsService.GetCapabilityFacts),
	protocol.MethodChatContextGet: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.ChatContextRequest) (serverapi.ChatContextResponse, error) {
			if params.Target.IsWorkspaceChat() {
				authReady, err := newRoutePolicyExecutor(g).serverAuthReady(ctx, state)
				if err != nil {
					return serverapi.ChatContextResponse{}, err
				}
				if !authReady {
					return serverapi.ChatContextResponse{}, serverapi.ErrServerAuthRequired
				}
				projectID, err := g.activeProjectID(ctx, state)
				if err != nil {
					return serverapi.ChatContextResponse{}, err
				}
				var owner chatcontext.WorkspaceOwner
				if strings.TrimSpace(state.attachedWorkspaceID) == "" {
					owner, err = g.deps.WorkspaceChatContextOwnerForProjectWorkspace(ctx, projectID, state.attachedWorkspaceRoot)
				} else {
					owner, err = g.deps.WorkspaceChatContextOwnerForProjectWorkspaceID(ctx, projectID, state.attachedWorkspaceID)
				}
				if err != nil {
					return serverapi.ChatContextResponse{}, err
				}
				if owner == nil {
					return serverapi.ChatContextResponse{}, errors.New("workspace Chat Context owner is required")
				}
				contextFacts, err := owner.ReadWorkspaceChatContext(ctx)
				return serverapi.ChatContextResponse{Context: contextFacts}, err
			}
			sessionID, selected := params.Target.SessionID()
			if !selected {
				return serverapi.ChatContextResponse{}, errors.New("validated Chat Context target is required")
			}
			if err := g.requireSessionInActiveProject(ctx, state, sessionID.String()); err != nil {
				return serverapi.ChatContextResponse{}, err
			}
			owner := g.deps.SessionChatContextOwner()
			if owner == nil {
				return serverapi.ChatContextResponse{}, errors.New("Session Chat Context owner is required")
			}
			contextFacts, err := owner.ReadSessionChatContext(ctx, sessionID)
			return serverapi.ChatContextResponse{Context: contextFacts}, err
		})
	},
	protocol.MethodPromptCommandCatalogGet: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.PromptCommandCatalogRequest]) (serverapi.PromptCommandCatalogResponse, error) {
			projectID, err := g.activeProjectID(ctx, state)
			if err != nil {
				return serverapi.PromptCommandCatalogResponse{}, err
			}
			workspaceRoot, err := g.promptCommandWorkspaceRootForCatalog(ctx, state, validated.Value().SessionID)
			if err != nil {
				return serverapi.PromptCommandCatalogResponse{}, err
			}
			catalog, err := g.deps.PromptCommandCatalogClientForProjectWorkspace(ctx, projectID, workspaceRoot)
			if err != nil {
				return serverapi.PromptCommandCatalogResponse{}, err
			}
			trusted, ok := catalog.(apicontract.PromptCommandCatalogTrustedService)
			if !ok {
				return serverapi.PromptCommandCatalogResponse{}, errors.New("Prompt Command Catalog trusted service is required")
			}
			return trusted.GetPromptCommandCatalogValidated(ctx, validated)
		})
	},
	protocol.MethodOnboardingFinalize: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		params, err := g.onboardingFinalizeRequestContract.Decode(req.Params)
		if err != nil {
			return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, fmt.Sprintf("decode params: %v", err))
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
			if err := g.deps.ProjectExists(ctx, params.ProjectID); err != nil {
				return protocol.AttachResponse{}, err
			}
			attachedWorkspaceID, attachedRoot, err := g.resolveAttachedProjectWorkspace(ctx, params)
			if err != nil {
				return protocol.AttachResponse{}, err
			}
			state.attachedProject = params.ProjectID
			state.attachedWorkspaceID = attachedWorkspaceID
			state.attachedWorkspaceRoot = attachedRoot
			state.attachedSession = nil
			return protocol.ProjectAttachResponseForRequest(params, attachedWorkspaceID, attachedRoot)
		})
	},
	protocol.MethodAttachSession: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[protocol.AttachSessionRequest]) (protocol.AttachResponse, error) {
			attachment, err := g.authorizeSessionAttachment(ctx, state, validated.Value().SessionID)
			if err != nil {
				return protocol.AttachResponse{}, err
			}
			state.attachedProject = attachment.ProjectID
			state.attachedWorkspaceID = attachment.WorkspaceID
			state.attachedWorkspaceRoot = attachment.CanonicalRoot
			state.attachedSession = &attachment.SessionID
			return protocol.SessionAttachResponse(
				attachment.ProjectID,
				attachment.WorkspaceID,
				attachment.CanonicalRoot,
				attachment.SessionID.String(),
			)
		})
	},
	protocol.MethodProjectList:                 gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectListRequest, serverapi.ProjectListResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ListProjects),
	protocol.MethodProjectHomeList:             gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectHomeListRequest, serverapi.ProjectHomeListResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ListProjectHome),
	protocol.MethodProjectResolvePath:          gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectResolvePathRequest, serverapi.ProjectResolvePathResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ResolveProjectPath),
	protocol.MethodProjectPlanWorkspaceBinding: gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectBindingPlanRequest, serverapi.ProjectBindingPlanResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.PlanWorkspaceBinding),
	protocol.MethodProjectCreate:               gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectCreateRequest, serverapi.ProjectCreateResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.CreateProject),
	protocol.MethodProjectEditGet:              gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectEditGetRequest, serverapi.ProjectEditGetResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.GetProjectEdit),
	protocol.MethodProjectUpdate:               gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectUpdateRequest, serverapi.ProjectUpdateResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.UpdateProject),
	protocol.MethodProjectSetDefaultWorkspace:  gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectDefaultWorkspaceSetRequest, serverapi.ProjectDefaultWorkspaceSetResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.SetDefaultWorkspace),
	protocol.MethodProjectWorkspaceList:        gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectWorkspaceListRequest, serverapi.ProjectWorkspaceListResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ListProjectWorkspaces),
	protocol.MethodProjectWorkspaceGet:         gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectWorkspaceGetRequest, serverapi.ProjectWorkspaceGetResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.GetProjectWorkspace),
	protocol.MethodProjectUnlinkWorkspace:      gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectWorkspaceUnlinkRequest, serverapi.ProjectWorkspaceUnlinkResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.UnlinkWorkspaceFromProject),
	protocol.MethodProjectDelete:               gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectDeleteRequest, serverapi.ProjectDeleteResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.DeleteProject),
	protocol.MethodProjectAttachWorkspace:      gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectAttachWorkspaceRequest, serverapi.ProjectAttachWorkspaceResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.AttachWorkspaceToProject),
	protocol.MethodProjectRebindWorkspace:      gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectRebindWorkspaceRequest, serverapi.ProjectRebindWorkspaceResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.RebindWorkspace),
	protocol.MethodProjectGetOverview:          gatewayClientCall[apicontract.ProjectViewService, serverapi.ProjectGetOverviewRequest, serverapi.ProjectGetOverviewResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.GetProjectOverview),
	protocol.MethodSessionPage:                 gatewayClientCall[apicontract.ProjectViewService, serverapi.SessionPageRequest, serverapi.SessionPageResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectViewService.ListSessionPage),
	protocol.MethodChatSettingsRead: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.ChatSettingsReadRequest) (serverapi.ChatSettingsReadResponse, error) {
			switch params.Target.TargetKind {
			case serverapi.ChatSettingsReadTargetLazy:
				activeProjectID, err := g.activeProjectID(ctx, state)
				if err != nil {
					return serverapi.ChatSettingsReadResponse{}, err
				}
				if strings.TrimSpace(*params.Target.ProjectID) != strings.TrimSpace(activeProjectID) {
					return serverapi.ChatSettingsReadResponse{}, serverapi.ErrWorkspaceNotRegistered
				}
			case serverapi.ChatSettingsReadTargetSession:
				if err := g.requireSessionInActiveProject(ctx, state, params.Target.Session.String()); err != nil {
					return serverapi.ChatSettingsReadResponse{}, err
				}
			default:
				return serverapi.ChatSettingsReadResponse{}, errors.New("Chat settings target kind is invalid")
			}
			response, err := g.deps.ChatSettingsClient().ReadChatSettings(ctx, params)
			if err != nil {
				return serverapi.ChatSettingsReadResponse{}, err
			}
			if err := response.ValidateForTarget(params.Target); err != nil {
				return serverapi.ChatSettingsReadResponse{}, err
			}
			return response, nil
		})
	},
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
	protocol.MethodWorkflowAttentionList:         gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowAttentionListRequest, serverapi.WorkflowAttentionListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowAttention),
	protocol.MethodWorkflowTaskAttentionList:     gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskAttentionListRequest, serverapi.WorkflowTaskAttentionListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTaskAttention),
	protocol.MethodWorkflowTaskCommentAdd:        gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskCommentAddRequest, serverapi.WorkflowTaskCommentAddResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.AddWorkflowTaskComment),
	protocol.MethodWorkflowTaskCommentList:       gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskCommentListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTaskComments),
	protocol.MethodWorkflowTaskCommentReplace:    gatewayClientCallNoResponse[apicontract.WorkflowService, serverapi.WorkflowTaskCommentReplaceRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ReplaceWorkflowTaskComment),
	protocol.MethodWorkflowTaskCommentDelete:     gatewayClientCallNoResponse[apicontract.WorkflowService, serverapi.WorkflowTaskCommentDeleteRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.DeleteWorkflowTaskComment),
	protocol.MethodWorkflowTaskActivityList:      gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskActivityListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTaskActivity),
	protocol.MethodWorkflowTaskSessionList:       gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskSessionListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTaskSessions),
	protocol.MethodWorkflowTaskList:              gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskListRequest, serverapi.WorkflowTaskListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTasks),
	protocol.MethodWorkflowTaskSearch:            gatewayClientCall[apicontract.WorkflowService, serverapi.TaskSearchRequest, serverapi.TaskSearchResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.SearchWorkflowTasks),
	protocol.MethodWorkflowBoardGet:              gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowBoardRequest, serverapi.WorkflowBoardResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.GetWorkflowBoard),
	protocol.MethodWorkflowBoardNodeCardsList:    gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowBoardNodeCardsListRequest, serverapi.WorkflowBoardNodeCardsListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowBoardNodeCards),
	protocol.MethodWorkflowTaskGet:               gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskGetRequest, serverapi.WorkflowTaskGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.GetWorkflowTask),
	protocol.MethodWorkflowTaskObserve:           gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskObservationRequest, serverapi.WorkflowTaskObservationResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ObserveWorkflowTask),
	protocol.MethodSessionPlan: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.SessionPlanRequest]) (serverapi.SessionPlanResponse, error) {
			launchClient, err := g.sessionLaunchClientForState(ctx, state)
			if err != nil {
				return serverapi.SessionPlanResponse{}, err
			}
			trusted, ok := launchClient.(apicontract.SessionLaunchTrustedService)
			if !ok {
				return serverapi.SessionPlanResponse{}, errors.New("Session Launch trusted service is required")
			}
			return trusted.PlanSessionValidated(ctx, validated)
		})
	},
	protocol.MethodSessionWorkspaceChatDraft: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.WorkspaceChatDraftRequest]) (serverapi.WorkspaceChatDraftResponse, error) {
			launchClient, err := g.sessionLaunchClientForState(ctx, state)
			if err != nil {
				return serverapi.WorkspaceChatDraftResponse{}, err
			}
			trusted, ok := launchClient.(apicontract.SessionLaunchTrustedService)
			if !ok {
				return serverapi.WorkspaceChatDraftResponse{}, errors.New("Session Launch trusted service is required")
			}
			return trusted.WorkspaceChatDraftValidated(ctx, validated)
		})
	},
	protocol.MethodSessionWorkspaceChatMaterialize: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.WorkspaceChatMaterializeRequest]) (serverapi.WorkspaceChatMaterializeResponse, error) {
			launchClient, err := g.sessionLaunchClientForState(ctx, state)
			if err != nil {
				return serverapi.WorkspaceChatMaterializeResponse{}, err
			}
			trusted, ok := launchClient.(apicontract.SessionLaunchTrustedService)
			if !ok {
				return serverapi.WorkspaceChatMaterializeResponse{}, errors.New("Session Launch trusted service is required")
			}
			return trusted.MaterializeWorkspaceChatValidated(ctx, validated)
		})
	},
	protocol.MethodSessionGetMainView: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.SessionMainViewRequest]) (serverapi.SessionMainViewResponse, error) {
			authorization, err := authorizeSessionActiveProject(func(request serverapi.SessionMainViewRequest) string { return request.SessionID })(ctx, g, state, validated)
			if err != nil {
				return serverapi.SessionMainViewResponse{}, err
			}
			trusted, ok := g.deps.SessionViewClient().(apicontract.SessionMainViewTrustedService)
			if !ok {
				return serverapi.SessionMainViewResponse{}, errors.New("Session View trusted service is required")
			}
			return trusted.GetSessionMainViewValidated(ctx, validated, authorization)
		})
	},
	protocol.MethodSessionGetTranscriptPage: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.SessionTranscriptPageRequest]) (serverapi.SessionTranscriptPageResponse, error) {
			authorization, err := authorizeSessionActiveProject(func(request serverapi.SessionTranscriptPageRequest) string { return request.SessionID })(ctx, g, state, validated)
			if err != nil {
				return serverapi.SessionTranscriptPageResponse{}, err
			}
			trusted, ok := g.deps.SessionViewClient().(apicontract.SessionTranscriptPageTrustedService)
			if !ok {
				return serverapi.SessionTranscriptPageResponse{}, errors.New("Session View trusted service is required")
			}
			return trusted.GetSessionTranscriptPageValidated(ctx, validated, authorization)
		})
	},
	protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.SessionLatestCommittedAssistantFinalAnswerRequest]) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
			authorization, err := authorizeSessionActiveProject(func(request serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) string {
				return request.SessionID
			})(ctx, g, state, validated)
			if err != nil {
				return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, err
			}
			trusted, ok := g.deps.SessionViewClient().(apicontract.SessionFinalAnswerTrustedService)
			if !ok {
				return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, errors.New("Session View trusted service is required")
			}
			return trusted.GetLatestCommittedAssistantFinalAnswerValidated(ctx, validated, authorization)
		})
	},
	protocol.MethodSessionGetExecutionEnvironment: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		params, err := g.sessionExecutionRequestContract.Decode(req.Params)
		if err != nil {
			return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, fmt.Sprintf("decode params: %v", err))
		}
		response, err := apicontract.WithValidated(params, apicontract.SemanticValidationRequired, func(validated apicontract.Validated[serverapi.SessionExecutionEnvironmentRequest]) (serverapi.SessionExecutionEnvironmentResponse, error) {
			authorization, err := authorizeSessionActiveProject(func(request serverapi.SessionExecutionEnvironmentRequest) string { return request.SessionID.String() })(ctx, g, state, validated)
			if err != nil {
				return serverapi.SessionExecutionEnvironmentResponse{}, validatedOwnerError{cause: err}
			}
			trusted, ok := g.deps.SessionViewClient().(apicontract.SessionExecutionEnvironmentTrustedService)
			if !ok {
				return serverapi.SessionExecutionEnvironmentResponse{}, validatedOwnerError{cause: errors.New("Session View trusted service is required")}
			}
			response, err := trusted.GetSessionExecutionEnvironmentValidated(ctx, validated, authorization)
			if err != nil {
				return serverapi.SessionExecutionEnvironmentResponse{}, validatedOwnerError{cause: err}
			}
			return response, nil
		})
		if err != nil {
			return responseForValidationOrOwnerError(req.ID, err)
		}
		return protocol.NewSuccessResponse(req.ID, response)
	},
	protocol.MethodSessionGetInitialInput: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.SessionInitialInputRequest]) (serverapi.SessionInitialInputResponse, error) {
			authorization, err := authorizeOptionalSessionActiveProject(func(request serverapi.SessionInitialInputRequest) string { return request.SessionID })(ctx, g, state, validated)
			if err != nil {
				return serverapi.SessionInitialInputResponse{}, err
			}
			trusted, ok := g.deps.SessionLifecycleClient().(apicontract.SessionInitialInputTrustedService)
			if !ok {
				return serverapi.SessionInitialInputResponse{}, errors.New("Session Lifecycle trusted service is required")
			}
			return trusted.GetInitialInputValidated(ctx, validated, authorization)
		})
	},
	protocol.MethodSessionPersistInputDraft: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.SessionPersistInputDraftRequest]) (serverapi.SessionPersistInputDraftResponse, error) {
			authorization, err := authorizeSessionActiveProject(func(request serverapi.SessionPersistInputDraftRequest) string { return request.SessionID })(ctx, g, state, validated)
			if err != nil {
				return serverapi.SessionPersistInputDraftResponse{}, err
			}
			trusted, ok := g.deps.SessionLifecycleClient().(apicontract.SessionPersistInputDraftTrustedService)
			if !ok {
				return serverapi.SessionPersistInputDraftResponse{}, errors.New("Session Lifecycle trusted service is required")
			}
			return trusted.PersistInputDraftValidated(ctx, validated, authorization)
		})
	},
	protocol.MethodSessionRetargetWorkspace: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.SessionRetargetWorkspaceRequest]) (serverapi.SessionRetargetWorkspaceResponse, error) {
			constraint := apicontract.AbsentAttachedProjectConstraint()
			if projectID := strings.TrimSpace(state.attachedProject); projectID != "" {
				constraint = apicontract.PresentAttachedProjectConstraint(projectID)
			}
			trusted, ok := g.deps.SessionLifecycleClient().(apicontract.SessionRetargetWorkspaceTrustedService)
			if !ok {
				return serverapi.SessionRetargetWorkspaceResponse{}, errors.New("Session Lifecycle trusted service is required")
			}
			return trusted.RetargetSessionWorkspaceValidated(ctx, validated, constraint)
		})
	},
	protocol.MethodSessionResolveTransition: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.SessionResolveTransitionRequest]) (serverapi.SessionResolveTransitionResponse, error) {
			authorization, err := authorizeOptionalSessionActiveProject(func(request serverapi.SessionResolveTransitionRequest) string { return request.SessionID })(ctx, g, state, validated)
			if err != nil {
				return serverapi.SessionResolveTransitionResponse{}, err
			}
			trusted, ok := g.deps.SessionLifecycleClient().(apicontract.SessionResolveTransitionTrustedService)
			if !ok {
				return serverapi.SessionResolveTransitionResponse{}, errors.New("Session Lifecycle trusted service is required")
			}
			return trusted.ResolveTransitionValidated(ctx, validated, authorization)
		})
	},
	protocol.MethodWorktreeStatus: gatewaySessionTrustedCall[apicontract.WorktreeService, apicontract.WorktreeQueryTrustedService, serverapi.WorktreeStatusRequest, serverapi.WorktreeStatusResponse](
		GatewayDependencies.WorktreeClient,
		func(request serverapi.WorktreeStatusRequest) string { return request.SessionID },
		apicontract.WorktreeQueryTrustedService.GetWorktreeStatusValidated,
	),
	protocol.MethodWorktreeList: gatewaySessionTrustedCall[apicontract.WorktreeService, apicontract.WorktreeQueryTrustedService, serverapi.WorktreeListRequest, serverapi.WorktreeListResponse](
		GatewayDependencies.WorktreeClient,
		func(request serverapi.WorktreeListRequest) string { return request.SessionID },
		apicontract.WorktreeQueryTrustedService.ListWorktreesValidated,
	),
	protocol.MethodWorktreeWorkspaceList: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.WorktreeWorkspaceListRequest]) (serverapi.WorktreeWorkspaceListResponse, error) {
			authorization, err := g.authorizeProjectWorkspaceBinding(ctx, state, validated.Value())
			if err != nil {
				return serverapi.WorktreeWorkspaceListResponse{}, err
			}
			trusted, ok := g.deps.WorktreeClient().(apicontract.WorktreeWorkspaceListTrustedService)
			if !ok {
				return serverapi.WorktreeWorkspaceListResponse{}, errors.New("Worktree trusted service is required")
			}
			return trusted.ListWorkspaceWorktreesValidated(ctx, validated, authorization)
		})
	},
	protocol.MethodWorktreeSelectorResolve: gatewaySessionTrustedCall[apicontract.WorktreeService, apicontract.WorktreeQueryTrustedService, serverapi.WorktreeSelectorPreviewRequest, serverapi.WorktreeSelectorPreviewResponse](
		GatewayDependencies.WorktreeClient,
		func(request serverapi.WorktreeSelectorPreviewRequest) string { return request.SessionID },
		apicontract.WorktreeQueryTrustedService.ResolveWorktreeSelectorValidated,
	),
	protocol.MethodWorktreeDeletePreview: gatewaySessionTrustedCall[apicontract.WorktreeService, apicontract.WorktreeQueryTrustedService, serverapi.WorktreeDeletePreviewRequest, serverapi.WorktreeDeletePreviewResponse](
		GatewayDependencies.WorktreeClient,
		func(request serverapi.WorktreeDeletePreviewRequest) string { return request.SessionID },
		apicontract.WorktreeQueryTrustedService.PreviewWorktreeDeleteValidated,
	),
	protocol.MethodWorktreeCreateTargetResolve: gatewaySessionTrustedCall[apicontract.WorktreeService, apicontract.WorktreeQueryTrustedService, serverapi.WorktreeCreateTargetResolveRequest, serverapi.WorktreeCreateTargetResolveResponse](
		GatewayDependencies.WorktreeClient,
		func(request serverapi.WorktreeCreateTargetResolveRequest) string { return request.SessionID },
		apicontract.WorktreeQueryTrustedService.ResolveWorktreeCreateTargetValidated,
	),
	protocol.MethodWorktreeCreate: gatewaySessionTrustedCall[apicontract.WorktreeService, apicontract.WorktreeMutationTrustedService, serverapi.WorktreeCreateRequest, serverapi.WorktreeCreateResponse](
		GatewayDependencies.WorktreeClient,
		func(request serverapi.WorktreeCreateRequest) string { return request.SessionID },
		apicontract.WorktreeMutationTrustedService.CreateWorktreeValidated,
	),
	protocol.MethodWorktreeEnter: gatewaySessionTrustedCall[apicontract.WorktreeService, apicontract.WorktreeMutationTrustedService, serverapi.WorktreeEnterRequest, serverapi.WorktreeScheduledAcknowledgement](
		GatewayDependencies.WorktreeClient,
		func(request serverapi.WorktreeEnterRequest) string { return request.SessionID },
		apicontract.WorktreeMutationTrustedService.EnterWorktreeValidated,
	),
	protocol.MethodWorktreeLeave: gatewaySessionTrustedCall[apicontract.WorktreeService, apicontract.WorktreeMutationTrustedService, serverapi.WorktreeLeaveRequest, serverapi.WorktreeScheduledAcknowledgement](
		GatewayDependencies.WorktreeClient,
		func(request serverapi.WorktreeLeaveRequest) string { return request.SessionID },
		apicontract.WorktreeMutationTrustedService.LeaveWorktreeValidated,
	),
	protocol.MethodWorktreeDelete: gatewaySessionTrustedCall[apicontract.WorktreeService, apicontract.WorktreeMutationTrustedService, serverapi.WorktreeDeleteRequest, serverapi.WorktreeDeleteResult](
		GatewayDependencies.WorktreeClient,
		func(request serverapi.WorktreeDeleteRequest) string { return request.SessionID },
		apicontract.WorktreeMutationTrustedService.DeleteWorktreeValidated,
	),
	protocol.MethodSessionRuntimeActivate: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodePreparedValidatedAndHandle(
			req,
			func(request serverapi.SessionRuntimeActivateRequest) serverapi.SessionRuntimeActivateRequest {
				request.OwnerID = strings.TrimSpace(state.runtimeOwnerID)
				return request
			},
			func(validated apicontract.Validated[serverapi.SessionRuntimeActivateRequest]) (serverapi.SessionRuntimeActivateResponse, error) {
				authorization, err := authorizeSessionActiveProject(func(request serverapi.SessionRuntimeActivateRequest) string { return request.SessionID })(ctx, g, state, validated)
				if err != nil {
					return serverapi.SessionRuntimeActivateResponse{}, err
				}
				trusted, ok := g.deps.SessionRuntimeClient().(apicontract.SessionRuntimeTrustedService)
				if !ok {
					return serverapi.SessionRuntimeActivateResponse{}, errors.New("Session Runtime trusted service is required")
				}
				response, err := trusted.ActivateSessionRuntimeValidated(ctx, validated, authorization)
				if err != nil {
					return serverapi.SessionRuntimeActivateResponse{}, err
				}
				if err := response.ValidateForSession(validated.Value().SessionID); err != nil {
					return serverapi.SessionRuntimeActivateResponse{}, err
				}
				state.recordOwnedRuntime(response.Attachment)
				return response, nil
			},
		)
	},
	protocol.MethodSessionRuntimeRelease: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodePreparedValidatedAndHandle(
			req,
			func(request serverapi.SessionRuntimeReleaseRequest) serverapi.SessionRuntimeReleaseRequest {
				request.OwnerID = strings.TrimSpace(state.runtimeOwnerID)
				return request
			},
			func(validated apicontract.Validated[serverapi.SessionRuntimeReleaseRequest]) (serverapi.SessionRuntimeReleaseResponse, error) {
				authorization, err := authorizeSessionActiveProject(func(request serverapi.SessionRuntimeReleaseRequest) string { return request.Attachment.SessionID })(ctx, g, state, validated)
				if err != nil {
					return serverapi.SessionRuntimeReleaseResponse{}, err
				}
				trusted, ok := g.deps.SessionRuntimeClient().(apicontract.SessionRuntimeTrustedService)
				if !ok {
					return serverapi.SessionRuntimeReleaseResponse{}, errors.New("Session Runtime trusted service is required")
				}
				response, err := trusted.ReleaseSessionRuntimeValidated(ctx, validated, authorization)
				if err == nil && (response.Released || validated.Value().DropOwner) {
					state.removeOwnedRuntime(validated.Value().Attachment)
				}
				return response, err
			},
		)
	},
	protocol.MethodRuntimeSetSessionName:                 gatewaySessionTrustedCallNoResponse[apicontract.RuntimeControlService, apicontract.RuntimeSessionIdentityTrustedService, serverapi.RuntimeSetSessionNameRequest](GatewayDependencies.RuntimeControlClient, func(r serverapi.RuntimeSetSessionNameRequest) string { return r.SessionID }, apicontract.RuntimeSessionIdentityTrustedService.SetSessionNameValidated),
	protocol.MethodRuntimeSetThinkingLevel:               gatewaySessionTrustedCallNoResponse[apicontract.RuntimeControlService, apicontract.RuntimeChatSettingsTrustedService, serverapi.RuntimeSetThinkingLevelRequest](GatewayDependencies.RuntimeControlClient, func(r serverapi.RuntimeSetThinkingLevelRequest) string { return r.SessionID }, apicontract.RuntimeChatSettingsTrustedService.SetThinkingLevelValidated),
	protocol.MethodRuntimeSetFastModeEnabled:             gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeChatSettingsTrustedService, serverapi.RuntimeSetFastModeEnabledRequest, serverapi.RuntimeSetFastModeEnabledResponse](GatewayDependencies.RuntimeControlClient, func(r serverapi.RuntimeSetFastModeEnabledRequest) string { return r.SessionID }, apicontract.RuntimeChatSettingsTrustedService.SetFastModeEnabledValidated),
	protocol.MethodRuntimeSetReviewerEnabled:             gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeChatSettingsTrustedService, serverapi.RuntimeSetReviewerEnabledRequest, serverapi.RuntimeSetReviewerEnabledResponse](GatewayDependencies.RuntimeControlClient, func(r serverapi.RuntimeSetReviewerEnabledRequest) string { return r.SessionID }, apicontract.RuntimeChatSettingsTrustedService.SetReviewerEnabledValidated),
	protocol.MethodRuntimeSetAutoCompactionEnabled:       gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeChatSettingsTrustedService, serverapi.RuntimeSetAutoCompactionEnabledRequest, serverapi.RuntimeSetAutoCompactionEnabledResponse](GatewayDependencies.RuntimeControlClient, func(r serverapi.RuntimeSetAutoCompactionEnabledRequest) string { return r.SessionID }, apicontract.RuntimeChatSettingsTrustedService.SetAutoCompactionEnabledValidated),
	protocol.MethodRuntimeSetQuestionsEnabled:            gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeChatSettingsTrustedService, serverapi.RuntimeSetQuestionsEnabledRequest, serverapi.RuntimeSetQuestionsEnabledResponse](GatewayDependencies.RuntimeControlClient, func(r serverapi.RuntimeSetQuestionsEnabledRequest) string { return r.SessionID }, apicontract.RuntimeChatSettingsTrustedService.SetQuestionsEnabledValidated),
	protocol.MethodRuntimeAppendCommittedEntry:           gatewaySessionTrustedCallNoResponse[apicontract.RuntimeControlService, apicontract.RuntimeTranscriptMutationTrustedService, serverapi.RuntimeAppendCommittedEntryRequest](GatewayDependencies.RuntimeControlClient, func(r serverapi.RuntimeAppendCommittedEntryRequest) string { return r.SessionID }, apicontract.RuntimeTranscriptMutationTrustedService.AppendCommittedEntryValidated),
	protocol.MethodRuntimeShouldCompactBeforeUserMessage: gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeCompactionTrustedService, serverapi.RuntimeShouldCompactBeforeUserMessageRequest, serverapi.RuntimeShouldCompactBeforeUserMessageResponse](GatewayDependencies.RuntimeControlClient, func(r serverapi.RuntimeShouldCompactBeforeUserMessageRequest) string { return r.SessionID }, apicontract.RuntimeCompactionTrustedService.ShouldCompactBeforeUserMessageValidated),
	protocol.MethodRuntimeSubmitUserTurn:                 gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeUserInputTrustedService, serverapi.RuntimeSubmitUserTurnRequest, serverapi.RuntimeSubmitUserTurnResponse](GatewayDependencies.RuntimeControlClient, func(r serverapi.RuntimeSubmitUserTurnRequest) string { return r.SessionID }, apicontract.RuntimeUserInputTrustedService.SubmitUserTurnValidated),
	protocol.MethodRuntimeSubmitUserShellCommand:         gatewaySessionTrustedCallNoResponse[apicontract.RuntimeControlService, apicontract.RuntimeUserInputTrustedService, serverapi.RuntimeSubmitUserShellCommandRequest](GatewayDependencies.RuntimeControlClient, func(r serverapi.RuntimeSubmitUserShellCommandRequest) string { return r.SessionID }, apicontract.RuntimeUserInputTrustedService.SubmitUserShellCommandValidated),
	protocol.MethodRuntimeCompactContext:                 gatewaySessionTrustedCallNoResponse[apicontract.RuntimeControlService, apicontract.RuntimeCompactionTrustedService, serverapi.RuntimeCompactContextRequest](GatewayDependencies.RuntimeControlClient, func(r serverapi.RuntimeCompactContextRequest) string { return r.SessionID }, apicontract.RuntimeCompactionTrustedService.CompactContextValidated),
	protocol.MethodRuntimeInterrupt:                      gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeInterruptTrustedService, serverapi.RuntimeInterruptRequest, serverapi.RuntimeInterruptResponse](GatewayDependencies.RuntimeControlClient, func(r serverapi.RuntimeInterruptRequest) string { return r.SessionID }, apicontract.RuntimeInterruptTrustedService.InterruptValidated),
	protocol.MethodRuntimeLiveSteer:                      gatewayClientCall[apicontract.RuntimeLiveControlService, serverapi.RuntimeLiveSteerRequest, serverapi.RuntimeLiveSteerResponse](GatewayDependencies.RuntimeLiveControlClient, apicontract.RuntimeLiveControlService.LiveSteer),
	protocol.MethodRuntimeLiveStop:                       gatewayClientCall[apicontract.RuntimeLiveControlService, serverapi.RuntimeLiveStopRequest, serverapi.RuntimeLiveStopResponse](GatewayDependencies.RuntimeLiveControlClient, apicontract.RuntimeLiveControlService.LiveStop),
	protocol.MethodRuntimeLiveWait:                       gatewayClientCall[apicontract.RuntimeLiveControlService, serverapi.RuntimeLiveWaitRequest, serverapi.RuntimeLiveWaitResponse](GatewayDependencies.RuntimeLiveControlClient, apicontract.RuntimeLiveControlService.LiveWait),
	protocol.MethodRuntimeLiveWatch:                      gatewayClientCall[apicontract.RuntimeLiveControlService, serverapi.RuntimeLiveWatchRequest, serverapi.RuntimeLiveWatchResponse](GatewayDependencies.RuntimeLiveControlClient, apicontract.RuntimeLiveControlService.LiveWatch),
	protocol.MethodRuntimeDiscardQueuedUserMessage:       gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeUserInputTrustedService, serverapi.RuntimeDiscardQueuedUserMessageRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse](GatewayDependencies.RuntimeControlClient, func(r serverapi.RuntimeDiscardQueuedUserMessageRequest) string { return r.SessionID }, apicontract.RuntimeUserInputTrustedService.DiscardQueuedUserMessageValidated),
	protocol.MethodRuntimeRecordPromptHistory:            gatewaySessionTrustedCallNoResponse[apicontract.RuntimeControlService, apicontract.RuntimeTranscriptMutationTrustedService, serverapi.RuntimeRecordPromptHistoryRequest](GatewayDependencies.RuntimeControlClient, func(r serverapi.RuntimeRecordPromptHistoryRequest) string { return r.SessionID }, apicontract.RuntimeTranscriptMutationTrustedService.RecordPromptHistoryValidated),
	protocol.MethodRuntimeGoalShow:                       gatewayGoalTrustedCall[serverapi.RuntimeGoalShowRequest, serverapi.RuntimeGoalShowResponse](func(r serverapi.RuntimeGoalShowRequest) runtimeids.SessionID { return r.SessionID }, apicontract.RuntimeGoalTrustedService.ShowGoalValidated),
	protocol.MethodRuntimeGoalSet:                        gatewayGoalTrustedCall[serverapi.RuntimeGoalSetRequest, serverapi.RuntimeGoalMutationResponse](func(r serverapi.RuntimeGoalSetRequest) runtimeids.SessionID { return r.SessionID }, apicontract.RuntimeGoalTrustedService.SetGoalValidated),
	protocol.MethodRuntimeGoalPause:                      gatewayGoalTrustedCall[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalMutationResponse](func(r serverapi.RuntimeGoalStatusRequest) runtimeids.SessionID { return r.SessionID }, apicontract.RuntimeGoalTrustedService.PauseGoalValidated),
	protocol.MethodRuntimeGoalResume:                     gatewayGoalTrustedCall[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalMutationResponse](func(r serverapi.RuntimeGoalStatusRequest) runtimeids.SessionID { return r.SessionID }, apicontract.RuntimeGoalTrustedService.ResumeGoalValidated),
	protocol.MethodRuntimeGoalComplete:                   gatewayGoalTrustedCall[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalMutationResponse](func(r serverapi.RuntimeGoalStatusRequest) runtimeids.SessionID { return r.SessionID }, apicontract.RuntimeGoalTrustedService.CompleteGoalValidated),
	protocol.MethodRuntimeGoalClear:                      gatewayGoalTrustedCall[serverapi.RuntimeGoalClearRequest, serverapi.RuntimeGoalMutationResponse](func(r serverapi.RuntimeGoalClearRequest) runtimeids.SessionID { return r.SessionID }, apicontract.RuntimeGoalTrustedService.ClearGoalValidated),
	protocol.MethodProcessList: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeOwnerAndHandle(req, func(params serverapi.ProcessListRequest) (serverapi.ProcessListResponse, error) {
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
	protocol.MethodProcessGet: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.ProcessGetRequest]) (serverapi.ProcessGetResponse, error) {
			authorization, err := authorizeProcessActiveProject(func(request serverapi.ProcessGetRequest) string { return request.ProcessID })(ctx, g, state, validated)
			if err != nil {
				return serverapi.ProcessGetResponse{}, err
			}
			trusted, ok := g.deps.ProcessViewClient().(apicontract.ProcessGetTrustedService)
			if !ok {
				return serverapi.ProcessGetResponse{}, errors.New("Process View trusted service is required")
			}
			return trusted.GetProcessValidated(ctx, validated, authorization)
		})
	},
	protocol.MethodProcessKill: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.ProcessKillRequest]) (serverapi.ProcessKillResponse, error) {
			authorization, err := authorizeProcessActiveProject(func(request serverapi.ProcessKillRequest) string { return request.ProcessID })(ctx, g, state, validated)
			if err != nil {
				return serverapi.ProcessKillResponse{}, err
			}
			trusted, ok := g.deps.ProcessControlClient().(apicontract.ProcessKillTrustedService)
			if !ok {
				return serverapi.ProcessKillResponse{}, errors.New("Process Control trusted service is required")
			}
			return trusted.KillProcessValidated(ctx, validated, authorization)
		})
	},
	protocol.MethodProcessInlineOutput: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.ProcessInlineOutputRequest]) (serverapi.ProcessInlineOutputResponse, error) {
			authorization, err := authorizeProcessActiveProject(func(request serverapi.ProcessInlineOutputRequest) string { return request.ProcessID })(ctx, g, state, validated)
			if err != nil {
				return serverapi.ProcessInlineOutputResponse{}, err
			}
			trusted, ok := g.deps.ProcessControlClient().(apicontract.ProcessInlineOutputTrustedService)
			if !ok {
				return serverapi.ProcessInlineOutputResponse{}, errors.New("Process Control trusted service is required")
			}
			return trusted.GetInlineOutputValidated(ctx, validated, authorization)
		})
	},
	protocol.MethodAskListPending: gatewaySessionIdentityTrustedCall[apicontract.AskViewService, apicontract.AskViewTrustedService, serverapi.AskListPendingBySessionRequest, serverapi.AskListPendingBySessionResponse](
		GatewayDependencies.AskViewClient,
		func(r serverapi.AskListPendingBySessionRequest) string { return r.SessionID },
		apicontract.AskViewTrustedService.ListPendingAsksBySessionValidated,
	),
	protocol.MethodPromptAnswerBatch: gatewaySessionIdentityTrustedCall[apicontract.PromptControlService, apicontract.PromptAnswerBatchTrustedService, serverapi.PromptAnswerBatchRequest, serverapi.PromptAnswerBatchResponse](
		GatewayDependencies.PromptControlClient,
		func(r serverapi.PromptAnswerBatchRequest) string { return r.SessionID.String() },
		apicontract.PromptAnswerBatchTrustedService.AnswerPromptBatchValidated,
	),
	protocol.MethodApprovalListPending: gatewaySessionIdentityTrustedCall[apicontract.ApprovalViewService, apicontract.ApprovalViewTrustedService, serverapi.ApprovalListPendingBySessionRequest, serverapi.ApprovalListPendingBySessionResponse](
		GatewayDependencies.ApprovalViewClient,
		func(r serverapi.ApprovalListPendingBySessionRequest) string { return r.SessionID },
		apicontract.ApprovalViewTrustedService.ListPendingApprovalsBySessionValidated,
	),
}
