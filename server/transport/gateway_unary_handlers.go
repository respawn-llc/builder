package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/shared/apicontract"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func gatewayTrustedCall[Raw any, Trusted any, Req any, Resp any](
	getClient func(GatewayDependencies) Raw,
	call func(Trusted, context.Context, apicontract.Validated[Req]) (Resp, error),
) gatewayUnaryHandler {
	return func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[Req]) (Resp, error) {
			var zero Resp
			if err := g.authorizeValidatedRouteRequest(ctx, state, req.Method, validated.Value()); err != nil {
				return zero, err
			}
			trusted, ok := any(getClient(g.deps)).(Trusted)
			if !ok {
				return zero, fmt.Errorf("%T does not implement the trusted route owner", getClient(g.deps))
			}
			return call(trusted, ctx, validated)
		})
	}
}

func gatewayTrustedCallNoResponse[Raw any, Trusted any, Req any](
	getClient func(GatewayDependencies) Raw,
	call func(Trusted, context.Context, apicontract.Validated[Req]) error,
) gatewayUnaryHandler {
	return func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[Req]) (struct{}, error) {
			if err := g.authorizeValidatedRouteRequest(ctx, state, req.Method, validated.Value()); err != nil {
				return struct{}{}, err
			}
			trusted, ok := any(getClient(g.deps)).(Trusted)
			if !ok {
				return struct{}{}, fmt.Errorf("%T does not implement the trusted route owner", getClient(g.deps))
			}
			return struct{}{}, call(trusted, ctx, validated)
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

func gatewaySessionMembershipTrustedCall[Raw any, Trusted any, Req any, Resp any](
	getClient func(GatewayDependencies) Raw,
	sessionID func(Req) string,
	call func(Trusted, context.Context, apicontract.Validated[Req]) (Resp, error),
) gatewayUnaryHandler {
	return func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[Req]) (Resp, error) {
			var zero Resp
			if _, err := authorizeSessionActiveProject(sessionID)(ctx, g, state, validated); err != nil {
				return zero, err
			}
			trusted, ok := any(getClient(g.deps)).(Trusted)
			if !ok {
				return zero, fmt.Errorf("%T does not implement the trusted Session membership route owner", getClient(g.deps))
			}
			return call(trusted, ctx, validated)
		})
	}
}

func gatewayRuntimeLiveTrustedCall[Req any, Resp any](
	identity func(apicontract.Validated[Req]) apicontract.RuntimeLiveRequestIdentity,
	call func(apicontract.RuntimeLiveControlTrustedService, context.Context, apicontract.Validated[Req], apicontract.RuntimeLiveRequestIdentity) (Resp, error),
) gatewayUnaryHandler {
	return func(g *Gateway, ctx context.Context, _ *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[Req]) (Resp, error) {
			var zero Resp
			trusted, ok := g.deps.RuntimeLiveControlClient().(apicontract.RuntimeLiveControlTrustedService)
			if !ok {
				return zero, errors.New("Runtime Live Control trusted service is required")
			}
			return call(trusted, ctx, validated, identity(validated))
		})
	}
}

func gatewayRuntimeGoalTrustedCall[Req any, Resp any](
	sessionID func(Req) string,
	call func(apicontract.RuntimeGoalTrustedService, context.Context, apicontract.Validated[Req]) (Resp, error),
) gatewayUnaryHandler {
	return func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[Req]) (Resp, error) {
			var zero Resp
			if err := g.requireGoalSessionAccess(ctx, state, sessionID(validated.Value())); err != nil {
				return zero, err
			}
			trusted, ok := g.deps.RuntimeControlClient().(apicontract.RuntimeGoalTrustedService)
			if !ok {
				return zero, errors.New("Runtime Goal trusted service is required")
			}
			return call(trusted, ctx, validated)
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
		return decodeNoSemanticValidationAndHandle(req, func(validated apicontract.Validated[serverapi.AuthGetBootstrapStatusRequest]) (serverapi.AuthGetBootstrapStatusResponse, error) {
			bootstrapClient := g.deps.AuthBootstrapClient()
			if bootstrapClient == nil {
				return serverapi.AuthGetBootstrapStatusResponse{}, serverapi.ErrServerAuthRequired
			}
			trusted, ok := bootstrapClient.(apicontract.AuthBootstrapTrustedService)
			if !ok {
				return serverapi.AuthGetBootstrapStatusResponse{}, errors.New("Auth Bootstrap trusted service is required")
			}
			return trusted.GetAuthBootstrapStatusValidated(ctx, validated)
		})
	},
	protocol.MethodServerReadinessGet: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeNoSemanticValidationAndHandle(req, func(validated apicontract.Validated[serverapi.ServerReadinessRequest]) (serverapi.ServerReadinessResponse, error) {
			statusClient := g.deps.ServerStatusClient()
			if statusClient == nil {
				return serverapi.ServerReadinessResponse{}, errors.New("server status client is required")
			}
			trusted, ok := statusClient.(apicontract.ServerStatusTrustedService)
			if !ok {
				return serverapi.ServerReadinessResponse{}, errors.New("Server Status trusted service is required")
			}
			response, err := trusted.GetServerReadinessValidated(ctx, validated)
			if err != nil {
				return serverapi.ServerReadinessResponse{}, err
			}
			response.ServerID = g.identity.ServerID
			response.ProtocolVersion = g.identity.ProtocolVersion
			return response, nil
		})
	},
	protocol.MethodServerUpdateStatusGet: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.UpdateStatusRequest]) (serverapi.UpdateStatusResponse, error) {
			statusClient := g.deps.ServerStatusClient()
			if statusClient == nil {
				return serverapi.UpdateStatusResponse{}, errors.New("server status client is required")
			}
			trusted, ok := statusClient.(apicontract.ServerStatusTrustedService)
			if !ok {
				return serverapi.UpdateStatusResponse{}, errors.New("Server Status trusted service is required")
			}
			return trusted.GetUpdateStatusValidated(ctx, validated)
		})
	},
	protocol.MethodAuthCompleteBootstrap: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.AuthCompleteBootstrapRequest]) (serverapi.AuthCompleteBootstrapResponse, error) {
			bootstrapClient := g.deps.AuthBootstrapClient()
			if bootstrapClient == nil {
				return serverapi.AuthCompleteBootstrapResponse{}, serverapi.ErrServerAuthRequired
			}
			trusted, ok := bootstrapClient.(apicontract.AuthBootstrapTrustedService)
			if !ok {
				return serverapi.AuthCompleteBootstrapResponse{}, errors.New("Auth Bootstrap trusted service is required")
			}
			resp, err := trusted.CompleteAuthBootstrapValidated(ctx, validated)
			if err == nil {
				state.noAuthAccepted = resp.NoAuthSelected
			}
			return resp, err
		})
	},
	protocol.MethodAuthAcknowledgeNoAuth: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeNoSemanticValidationAndHandle(req, func(validated apicontract.Validated[serverapi.AuthAcknowledgeNoAuthRequest]) (serverapi.AuthAcknowledgeNoAuthResponse, error) {
			bootstrapClient := g.deps.AuthBootstrapClient()
			if bootstrapClient == nil {
				return serverapi.AuthAcknowledgeNoAuthResponse{}, serverapi.ErrServerAuthRequired
			}
			trusted, ok := bootstrapClient.(apicontract.AuthBootstrapTrustedService)
			if !ok {
				return serverapi.AuthAcknowledgeNoAuthResponse{}, errors.New("Auth Bootstrap trusted service is required")
			}
			resp, err := trusted.AcknowledgeNoAuthValidated(ctx, validated)
			if err == nil {
				state.noAuthAccepted = resp.NoAuthSelected
			}
			return resp, err
		})
	},
	protocol.MethodAuthGetStatus: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.AuthStatusRequest]) (serverapi.AuthStatusResponse, error) {
			statusClient := g.deps.AuthStatusClient()
			if statusClient == nil {
				return serverapi.AuthStatusResponse{}, serverapi.ErrServerAuthRequired
			}
			trusted, ok := statusClient.(apicontract.AuthStatusTrustedService)
			if !ok {
				return serverapi.AuthStatusResponse{}, errors.New("Auth Status trusted service is required")
			}
			return trusted.GetAuthStatusValidated(ctx, validated)
		})
	},
	protocol.MethodCapabilityFactsGet: gatewayTrustedCall[apicontract.CapabilityFactsService, apicontract.CapabilityFactsTrustedService, serverapi.CapabilityFactsRequest, serverapi.CapabilityFactsResponse](GatewayDependencies.CapabilityFactsClient, apicontract.CapabilityFactsTrustedService.GetCapabilityFactsValidated),
	protocol.MethodPromptCommandCatalogGet: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.PromptCommandCatalogRequest]) (serverapi.PromptCommandCatalogResponse, error) {
			params := validated.Value()
			projectID, err := g.activeProjectID(ctx, state)
			if err != nil {
				return serverapi.PromptCommandCatalogResponse{}, err
			}
			workspaceRoot, err := g.promptCommandWorkspaceRootForCatalog(ctx, state, params.SessionID)
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
		response, err := apicontract.WithValidated(params, apicontract.SemanticValidationRequired, func(validated apicontract.Validated[serverapi.OnboardingFinalizeRequest]) (serverapi.OnboardingFinalizeResponse, error) {
			finalizeClient := g.deps.OnboardingFinalizeClient()
			if finalizeClient == nil {
				return serverapi.OnboardingFinalizeResponse{}, validatedOwnerError{cause: serverapi.NewServerNotReadyError(serverapi.ServerNotReadyOnboardingRequired, nil, nil)}
			}
			trusted, ok := finalizeClient.(apicontract.OnboardingFinalizeTrustedService)
			if !ok {
				return serverapi.OnboardingFinalizeResponse{}, validatedOwnerError{cause: errors.New("Onboarding Finalize trusted service is required")}
			}
			response, ownerErr := trusted.FinalizeOnboardingValidated(ctx, validated)
			if ownerErr != nil {
				return serverapi.OnboardingFinalizeResponse{}, validatedOwnerError{cause: ownerErr}
			}
			return response, nil
		})
		if err != nil {
			return responseForValidationOrOwnerError(req.ID, err)
		}
		return protocol.NewSuccessResponse(req.ID, response)
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
		return decodeAndHandle(req, func(params protocol.AttachSessionRequest) (protocol.AttachResponse, error) {
			binding, err := g.resolveSessionAttachment(ctx, state, params.SessionID)
			if err != nil {
				return protocol.AttachResponse{}, err
			}
			state.attachedProject = binding.ProjectID
			state.attachedWorkspaceID = binding.WorkspaceID
			state.attachedWorkspaceRoot = binding.CanonicalRoot
			parsedSessionID, parseErr := runtimeids.ParseSessionID(params.SessionID)
			if parseErr != nil {
				return protocol.AttachResponse{}, parseErr
			}
			state.attachedSession = &parsedSessionID
			return protocol.SessionAttachResponse(
				binding.ProjectID,
				binding.WorkspaceID,
				binding.CanonicalRoot,
				params.SessionID,
			)
		})
	},
	protocol.MethodProjectList: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeNoSemanticValidationAndHandle(req, func(validated apicontract.Validated[serverapi.ProjectListRequest]) (serverapi.ProjectListResponse, error) {
			if err := g.authorizeValidatedRouteRequest(ctx, state, req.Method, validated.Value()); err != nil {
				return serverapi.ProjectListResponse{}, err
			}
			trusted, ok := g.deps.ProjectViewClient().(apicontract.ProjectListTrustedService)
			if !ok {
				return serverapi.ProjectListResponse{}, errors.New("Project View trusted service is required")
			}
			return trusted.ListProjectsValidated(ctx, validated)
		})
	},
	protocol.MethodProjectHomeList:               gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectListTrustedService, serverapi.ProjectHomeListRequest, serverapi.ProjectHomeListResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectListTrustedService.ListProjectHomeValidated),
	protocol.MethodProjectResolvePath:            gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectWorkspaceResolutionTrustedService, serverapi.ProjectResolvePathRequest, serverapi.ProjectResolvePathResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectWorkspaceResolutionTrustedService.ResolveProjectPathValidated),
	protocol.MethodProjectPlanWorkspaceBinding:   gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectWorkspaceResolutionTrustedService, serverapi.ProjectBindingPlanRequest, serverapi.ProjectBindingPlanResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectWorkspaceResolutionTrustedService.PlanWorkspaceBindingValidated),
	protocol.MethodProjectCreate:                 gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectCatalogMutationTrustedService, serverapi.ProjectCreateRequest, serverapi.ProjectCreateResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectCatalogMutationTrustedService.CreateProjectValidated),
	protocol.MethodProjectEditGet:                gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectCatalogMutationTrustedService, serverapi.ProjectEditGetRequest, serverapi.ProjectEditGetResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectCatalogMutationTrustedService.GetProjectEditValidated),
	protocol.MethodProjectUpdate:                 gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectCatalogMutationTrustedService, serverapi.ProjectUpdateRequest, serverapi.ProjectUpdateResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectCatalogMutationTrustedService.UpdateProjectValidated),
	protocol.MethodProjectSetDefaultWorkspace:    gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectWorkspaceCatalogTrustedService, serverapi.ProjectDefaultWorkspaceSetRequest, serverapi.ProjectDefaultWorkspaceSetResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectWorkspaceCatalogTrustedService.SetDefaultWorkspaceValidated),
	protocol.MethodProjectWorkspaceList:          gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectWorkspaceCatalogTrustedService, serverapi.ProjectWorkspaceListRequest, serverapi.ProjectWorkspaceListResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectWorkspaceCatalogTrustedService.ListProjectWorkspacesValidated),
	protocol.MethodProjectWorkspaceGet:           gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectWorkspaceCatalogTrustedService, serverapi.ProjectWorkspaceGetRequest, serverapi.ProjectWorkspaceGetResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectWorkspaceCatalogTrustedService.GetProjectWorkspaceValidated),
	protocol.MethodProjectUnlinkWorkspace:        gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectWorkspaceMutationTrustedService, serverapi.ProjectWorkspaceUnlinkRequest, serverapi.ProjectWorkspaceUnlinkResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectWorkspaceMutationTrustedService.UnlinkWorkspaceFromProjectValidated),
	protocol.MethodProjectDelete:                 gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectCatalogMutationTrustedService, serverapi.ProjectDeleteRequest, serverapi.ProjectDeleteResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectCatalogMutationTrustedService.DeleteProjectValidated),
	protocol.MethodProjectAttachWorkspace:        gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectWorkspaceMutationTrustedService, serverapi.ProjectAttachWorkspaceRequest, serverapi.ProjectAttachWorkspaceResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectWorkspaceMutationTrustedService.AttachWorkspaceToProjectValidated),
	protocol.MethodProjectRebindWorkspace:        gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectWorkspaceMutationTrustedService, serverapi.ProjectRebindWorkspaceRequest, serverapi.ProjectRebindWorkspaceResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectWorkspaceMutationTrustedService.RebindWorkspaceValidated),
	protocol.MethodProjectGetOverview:            gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectOverviewTrustedService, serverapi.ProjectGetOverviewRequest, serverapi.ProjectGetOverviewResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectOverviewTrustedService.GetProjectOverviewValidated),
	protocol.MethodSessionPage:                   gatewayTrustedCall[apicontract.ProjectViewService, apicontract.ProjectOverviewTrustedService, serverapi.SessionPageRequest, serverapi.SessionPageResponse](GatewayDependencies.ProjectViewClient, apicontract.ProjectOverviewTrustedService.ListSessionPageValidated),
	protocol.MethodWorkflowCreate:                gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowCatalogTrustedService, serverapi.WorkflowCreateRequest, serverapi.WorkflowCreateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowCatalogTrustedService.CreateWorkflowValidated),
	protocol.MethodWorkflowCreateAndLinkProject:  gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowCatalogTrustedService, serverapi.WorkflowCreateAndLinkProjectRequest, serverapi.WorkflowCreateAndLinkProjectResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowCatalogTrustedService.CreateAndLinkWorkflowToProjectValidated),
	protocol.MethodWorkflowUpdate:                gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowCatalogTrustedService, serverapi.WorkflowUpdateRequest, serverapi.WorkflowGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowCatalogTrustedService.UpdateWorkflowValidated),
	protocol.MethodWorkflowList:                  gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowCatalogTrustedService, serverapi.WorkflowListRequest, serverapi.WorkflowListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowCatalogTrustedService.ListWorkflowsValidated),
	protocol.MethodWorkflowGet:                   gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowCatalogTrustedService, serverapi.WorkflowGetRequest, serverapi.WorkflowGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowCatalogTrustedService.GetWorkflowValidated),
	protocol.MethodWorkflowLinkProject:           gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowProjectLinkTrustedService, serverapi.WorkflowLinkProjectRequest, serverapi.WorkflowLinkProjectResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowProjectLinkTrustedService.LinkWorkflowToProjectValidated),
	protocol.MethodWorkflowListProjectLinks:      gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowProjectLinkTrustedService, serverapi.WorkflowListProjectLinksRequest, serverapi.WorkflowListProjectLinksResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowProjectLinkTrustedService.ListProjectWorkflowLinksValidated),
	protocol.MethodWorkflowSetDefaultProjectLink: gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowProjectLinkTrustedService, serverapi.WorkflowSetDefaultProjectLinkRequest, serverapi.WorkflowSetDefaultProjectLinkResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowProjectLinkTrustedService.SetDefaultProjectWorkflowLinkValidated),
	protocol.MethodWorkflowUnlinkProject:         gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowProjectLinkTrustedService, serverapi.WorkflowUnlinkProjectRequest, serverapi.WorkflowUnlinkProjectResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowProjectLinkTrustedService.UnlinkWorkflowFromProjectValidated),
	protocol.MethodWorkflowDeletePreview:         gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowDeleteTrustedService, serverapi.WorkflowDeletePreviewRequest, serverapi.WorkflowDeletePreviewResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowDeleteTrustedService.PreviewWorkflowDeleteValidated),
	protocol.MethodWorkflowDelete:                gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowDeleteTrustedService, serverapi.WorkflowDeleteRequest, serverapi.WorkflowDeleteResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowDeleteTrustedService.DeleteWorkflowValidated),
	protocol.MethodWorkflowValidate:              gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowValidationTrustedService, serverapi.WorkflowValidateRequest, serverapi.WorkflowValidateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowValidationTrustedService.ValidateWorkflowValidated),
	protocol.MethodWorkflowScriptPathValidate:    gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowValidationTrustedService, serverapi.WorkflowScriptPathValidateRequest, serverapi.WorkflowValidateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowValidationTrustedService.ValidateWorkflowScriptPathValidated),
	protocol.MethodWorkflowGraphValidateDraft:    gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowValidationTrustedService, serverapi.WorkflowGraphValidateDraftRequest, serverapi.WorkflowGraphValidateDraftResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowValidationTrustedService.ValidateWorkflowGraphDraftValidated),
	protocol.MethodWorkflowGraphDeriveWiring:     gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowValidationTrustedService, serverapi.WorkflowGraphDeriveWiringRequest, serverapi.WorkflowGraphDeriveWiringResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowValidationTrustedService.DeriveWorkflowGraphWiringValidated),
	protocol.MethodWorkflowGraphSavePreview:      gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowGraphSaveTrustedService, serverapi.WorkflowGraphSavePreviewRequest, serverapi.WorkflowGraphSavePreviewResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowGraphSaveTrustedService.PreviewWorkflowGraphSaveValidated),
	protocol.MethodWorkflowGraphSave:             gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowGraphSaveTrustedService, serverapi.WorkflowGraphSaveRequest, serverapi.WorkflowGraphSaveResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowGraphSaveTrustedService.SaveWorkflowGraphValidated),
	protocol.MethodWorkflowProjectLabelCreate:    gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowProjectLabelTrustedService, serverapi.WorkflowProjectLabelCreateRequest, serverapi.WorkflowProjectLabelCreateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowProjectLabelTrustedService.CreateWorkflowProjectLabelValidated),
	protocol.MethodWorkflowProjectLabelList:      gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowProjectLabelTrustedService, serverapi.WorkflowProjectLabelCatalogRequest, serverapi.WorkflowProjectLabelCatalogResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowProjectLabelTrustedService.ListWorkflowProjectLabelsValidated),
	protocol.MethodWorkflowProjectLabelRename:    gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowProjectLabelTrustedService, serverapi.WorkflowProjectLabelRenameRequest, serverapi.WorkflowProjectLabelRenameResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowProjectLabelTrustedService.RenameWorkflowProjectLabelValidated),
	protocol.MethodWorkflowProjectLabelDelete:    gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowProjectLabelTrustedService, serverapi.WorkflowProjectLabelDeleteRequest, serverapi.WorkflowProjectLabelDeleteResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowProjectLabelTrustedService.DeleteWorkflowProjectLabelValidated),
	protocol.MethodWorkflowProjectLabelReorder:   gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowProjectLabelTrustedService, serverapi.WorkflowProjectLabelReorderRequest, serverapi.WorkflowProjectLabelReorderResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowProjectLabelTrustedService.ReorderWorkflowProjectLabelsValidated),
	protocol.MethodWorkflowTaskLabelsGet:         gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskLabelTrustedService, serverapi.WorkflowTaskLabelsGetRequest, serverapi.WorkflowTaskLabelsGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskLabelTrustedService.GetWorkflowTaskLabelsValidated),
	protocol.MethodWorkflowTaskLabelsUpdate:      gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskLabelTrustedService, serverapi.WorkflowTaskLabelsUpdateRequest, serverapi.WorkflowTaskLabelsUpdateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskLabelTrustedService.UpdateWorkflowTaskLabelsValidated),
	protocol.MethodWorkflowTaskCreate:            gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskMutationTrustedService, serverapi.WorkflowTaskCreateRequest, serverapi.WorkflowTaskCreateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskMutationTrustedService.CreateWorkflowTaskValidated),
	protocol.MethodWorkflowTaskDependencyAdd:     gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskDependencyTrustedService, serverapi.WorkflowTaskDependencyAddRequest, serverapi.WorkflowTaskDependencyAddResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskDependencyTrustedService.AddWorkflowTaskDependencyValidated),
	protocol.MethodWorkflowTaskDependencyRemove:  gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskDependencyTrustedService, serverapi.WorkflowTaskDependencyRemoveRequest, serverapi.WorkflowTaskDependencyRemoveResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskDependencyTrustedService.RemoveWorkflowTaskDependencyValidated),
	protocol.MethodWorkflowTaskDependencyList:    gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskDependencyTrustedService, serverapi.WorkflowTaskDependencyListRequest, serverapi.WorkflowTaskDependencyListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskDependencyTrustedService.ListWorkflowTaskDependenciesValidated),
	protocol.MethodWorkflowTaskUpdate:            gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskMutationTrustedService, serverapi.WorkflowTaskUpdateRequest, serverapi.WorkflowTaskUpdateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskMutationTrustedService.UpdateWorkflowTaskValidated),
	protocol.MethodWorkflowTaskStart:             gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskRunTrustedService, serverapi.WorkflowTaskStartRequest, serverapi.WorkflowTaskStartResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskRunTrustedService.StartWorkflowTaskValidated),
	protocol.MethodWorkflowTaskInterrupt:         gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskInterruptTrustedService, serverapi.WorkflowTaskInterruptRequest, serverapi.WorkflowTaskInterruptResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskInterruptTrustedService.InterruptWorkflowTaskValidated),
	protocol.MethodWorkflowTaskResume:            gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskRunTrustedService, serverapi.WorkflowTaskResumeRequest, serverapi.WorkflowTaskResumeResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskRunTrustedService.ResumeWorkflowTaskValidated),
	protocol.MethodWorkflowTaskApprove:           gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskApproveTrustedService, serverapi.WorkflowTaskApproveRequest, serverapi.WorkflowTaskApproveResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskApproveTrustedService.ApproveWorkflowTaskValidated),
	protocol.MethodWorkflowTaskMovePreview:       gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskMoveTrustedService, serverapi.WorkflowTaskMovePreviewRequest, serverapi.WorkflowTaskMovePreviewResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskMoveTrustedService.PreviewWorkflowTaskMoveValidated),
	protocol.MethodWorkflowTaskMove:              gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskMoveTrustedService, serverapi.WorkflowTaskMoveRequest, serverapi.WorkflowTaskMoveResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskMoveTrustedService.MoveWorkflowTaskValidated),
	protocol.MethodWorkflowTaskComplete:          gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskRunTrustedService, serverapi.WorkflowTaskCompleteRequest, serverapi.WorkflowTaskCompleteResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskRunTrustedService.CompleteWorkflowTaskValidated),
	protocol.MethodWorkflowTaskDelete:            gatewayTrustedCallNoResponse[apicontract.WorkflowService, apicontract.WorkflowTaskMutationTrustedService, serverapi.WorkflowTaskDeleteRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskMutationTrustedService.DeleteWorkflowTaskValidated),
	protocol.MethodWorkflowAttentionList:         gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowAttentionTrustedService, serverapi.WorkflowAttentionListRequest, serverapi.WorkflowAttentionListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowAttentionTrustedService.ListWorkflowAttentionValidated),
	protocol.MethodWorkflowTaskAttentionList:     gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowAttentionTrustedService, serverapi.WorkflowTaskAttentionListRequest, serverapi.WorkflowTaskAttentionListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowAttentionTrustedService.ListWorkflowTaskAttentionValidated),
	protocol.MethodWorkflowTaskCommentAdd:        gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskCommentTrustedService, serverapi.WorkflowTaskCommentAddRequest, serverapi.WorkflowTaskCommentAddResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskCommentTrustedService.AddWorkflowTaskCommentValidated),
	protocol.MethodWorkflowTaskCommentList:       gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskCommentTrustedService, serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskCommentListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskCommentTrustedService.ListWorkflowTaskCommentsValidated),
	protocol.MethodWorkflowTaskCommentReplace:    gatewayTrustedCallNoResponse[apicontract.WorkflowService, apicontract.WorkflowTaskCommentTrustedService, serverapi.WorkflowTaskCommentReplaceRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskCommentTrustedService.ReplaceWorkflowTaskCommentValidated),
	protocol.MethodWorkflowTaskCommentDelete:     gatewayTrustedCallNoResponse[apicontract.WorkflowService, apicontract.WorkflowTaskCommentTrustedService, serverapi.WorkflowTaskCommentDeleteRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskCommentTrustedService.DeleteWorkflowTaskCommentValidated),
	protocol.MethodWorkflowTaskActivityList:      gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskListTrustedService, serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskActivityListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskListTrustedService.ListWorkflowTaskActivityValidated),
	protocol.MethodWorkflowTaskSessionList:       gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskListTrustedService, serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskSessionListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskListTrustedService.ListWorkflowTaskSessionsValidated),
	protocol.MethodWorkflowTaskList:              gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskListTrustedService, serverapi.WorkflowTaskListRequest, serverapi.WorkflowTaskListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskListTrustedService.ListWorkflowTasksValidated),
	protocol.MethodWorkflowTaskSearch:            gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskListTrustedService, serverapi.TaskSearchRequest, serverapi.TaskSearchResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskListTrustedService.SearchWorkflowTasksValidated),
	protocol.MethodWorkflowBoardGet:              gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowBoardTrustedService, serverapi.WorkflowBoardRequest, serverapi.WorkflowBoardResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowBoardTrustedService.GetWorkflowBoardValidated),
	protocol.MethodWorkflowBoardNodeCardsList:    gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowBoardTrustedService, serverapi.WorkflowBoardNodeCardsListRequest, serverapi.WorkflowBoardNodeCardsListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowBoardTrustedService.ListWorkflowBoardNodeCardsValidated),
	protocol.MethodWorkflowTaskGet:               gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskGetTrustedService, serverapi.WorkflowTaskGetRequest, serverapi.WorkflowTaskGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskGetTrustedService.GetWorkflowTaskValidated),
	protocol.MethodWorkflowTaskObserve:           gatewayTrustedCall[apicontract.WorkflowService, apicontract.WorkflowTaskObservationTrustedService, serverapi.WorkflowTaskObservationRequest, serverapi.WorkflowTaskObservationResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowTaskObservationTrustedService.ObserveWorkflowTaskValidated),
	protocol.MethodSessionPlan: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeValidatedAndHandle(req, func(validated apicontract.Validated[serverapi.SessionPlanRequest]) (serverapi.SessionPlanResponse, error) {
			if err := g.authorizeValidatedRouteRequest(ctx, state, req.Method, validated.Value()); err != nil {
				return serverapi.SessionPlanResponse{}, err
			}
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
			if err := g.authorizeValidatedRouteRequest(ctx, state, req.Method, validated.Value()); err != nil {
				return serverapi.WorkspaceChatDraftResponse{}, err
			}
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
			if err := g.authorizeValidatedRouteRequest(ctx, state, req.Method, validated.Value()); err != nil {
				return serverapi.WorkspaceChatMaterializeResponse{}, err
			}
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
				authorization, err := authorizeSessionActiveProject(func(request serverapi.SessionRuntimeActivateRequest) string {
					return request.SessionID
				})(ctx, g, state, validated)
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
				authorization, err := authorizeSessionActiveProject(func(request serverapi.SessionRuntimeReleaseRequest) string {
					return request.Attachment.SessionID
				})(ctx, g, state, validated)
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
	protocol.MethodRuntimeSetSessionName: gatewaySessionTrustedCallNoResponse[apicontract.RuntimeControlService, apicontract.RuntimeSessionIdentityTrustedService, serverapi.RuntimeSetSessionNameRequest](
		GatewayDependencies.RuntimeControlClient,
		func(request serverapi.RuntimeSetSessionNameRequest) string { return request.SessionID },
		apicontract.RuntimeSessionIdentityTrustedService.SetSessionNameValidated,
	),
	protocol.MethodRuntimeSetThinkingLevel: gatewaySessionTrustedCallNoResponse[apicontract.RuntimeControlService, apicontract.RuntimeChatSettingsTrustedService, serverapi.RuntimeSetThinkingLevelRequest](
		GatewayDependencies.RuntimeControlClient,
		func(request serverapi.RuntimeSetThinkingLevelRequest) string { return request.SessionID },
		apicontract.RuntimeChatSettingsTrustedService.SetThinkingLevelValidated,
	),
	protocol.MethodRuntimeSetFastModeEnabled: gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeChatSettingsTrustedService, serverapi.RuntimeSetFastModeEnabledRequest, serverapi.RuntimeSetFastModeEnabledResponse](
		GatewayDependencies.RuntimeControlClient,
		func(request serverapi.RuntimeSetFastModeEnabledRequest) string { return request.SessionID },
		apicontract.RuntimeChatSettingsTrustedService.SetFastModeEnabledValidated,
	),
	protocol.MethodRuntimeSetReviewerEnabled: gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeChatSettingsTrustedService, serverapi.RuntimeSetReviewerEnabledRequest, serverapi.RuntimeSetReviewerEnabledResponse](
		GatewayDependencies.RuntimeControlClient,
		func(request serverapi.RuntimeSetReviewerEnabledRequest) string { return request.SessionID },
		apicontract.RuntimeChatSettingsTrustedService.SetReviewerEnabledValidated,
	),
	protocol.MethodRuntimeSetAutoCompactionEnabled: gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeChatSettingsTrustedService, serverapi.RuntimeSetAutoCompactionEnabledRequest, serverapi.RuntimeSetAutoCompactionEnabledResponse](
		GatewayDependencies.RuntimeControlClient,
		func(request serverapi.RuntimeSetAutoCompactionEnabledRequest) string { return request.SessionID },
		apicontract.RuntimeChatSettingsTrustedService.SetAutoCompactionEnabledValidated,
	),
	protocol.MethodRuntimeSetQuestionsEnabled: gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeChatSettingsTrustedService, serverapi.RuntimeSetQuestionsEnabledRequest, serverapi.RuntimeSetQuestionsEnabledResponse](
		GatewayDependencies.RuntimeControlClient,
		func(request serverapi.RuntimeSetQuestionsEnabledRequest) string { return request.SessionID },
		apicontract.RuntimeChatSettingsTrustedService.SetQuestionsEnabledValidated,
	),
	protocol.MethodRuntimeAppendCommittedEntry: gatewaySessionTrustedCallNoResponse[apicontract.RuntimeControlService, apicontract.RuntimeTranscriptMutationTrustedService, serverapi.RuntimeAppendCommittedEntryRequest](
		GatewayDependencies.RuntimeControlClient,
		func(request serverapi.RuntimeAppendCommittedEntryRequest) string { return request.SessionID },
		apicontract.RuntimeTranscriptMutationTrustedService.AppendCommittedEntryValidated,
	),
	protocol.MethodRuntimeShouldCompactBeforeUserMessage: gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeCompactionTrustedService, serverapi.RuntimeShouldCompactBeforeUserMessageRequest, serverapi.RuntimeShouldCompactBeforeUserMessageResponse](
		GatewayDependencies.RuntimeControlClient,
		func(request serverapi.RuntimeShouldCompactBeforeUserMessageRequest) string { return request.SessionID },
		apicontract.RuntimeCompactionTrustedService.ShouldCompactBeforeUserMessageValidated,
	),
	protocol.MethodRuntimeSubmitUserTurn: gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeUserInputTrustedService, serverapi.RuntimeSubmitUserTurnRequest, serverapi.RuntimeSubmitUserTurnResponse](
		GatewayDependencies.RuntimeControlClient,
		func(request serverapi.RuntimeSubmitUserTurnRequest) string { return request.SessionID },
		apicontract.RuntimeUserInputTrustedService.SubmitUserTurnValidated,
	),
	protocol.MethodRuntimeSubmitUserShellCommand: gatewaySessionTrustedCallNoResponse[apicontract.RuntimeControlService, apicontract.RuntimeUserInputTrustedService, serverapi.RuntimeSubmitUserShellCommandRequest](
		GatewayDependencies.RuntimeControlClient,
		func(request serverapi.RuntimeSubmitUserShellCommandRequest) string { return request.SessionID },
		apicontract.RuntimeUserInputTrustedService.SubmitUserShellCommandValidated,
	),
	protocol.MethodRuntimeCompactContext: gatewaySessionTrustedCallNoResponse[apicontract.RuntimeControlService, apicontract.RuntimeCompactionTrustedService, serverapi.RuntimeCompactContextRequest](
		GatewayDependencies.RuntimeControlClient,
		func(request serverapi.RuntimeCompactContextRequest) string { return request.SessionID },
		apicontract.RuntimeCompactionTrustedService.CompactContextValidated,
	),
	protocol.MethodRuntimeInterrupt: gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeInterruptTrustedService, serverapi.RuntimeInterruptRequest, serverapi.RuntimeInterruptResponse](
		GatewayDependencies.RuntimeControlClient,
		func(request serverapi.RuntimeInterruptRequest) string { return request.SessionID },
		apicontract.RuntimeInterruptTrustedService.InterruptValidated,
	),
	protocol.MethodRuntimeDiscardQueuedUserMessage: gatewaySessionTrustedCall[apicontract.RuntimeControlService, apicontract.RuntimeUserInputTrustedService, serverapi.RuntimeDiscardQueuedUserMessageRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse](
		GatewayDependencies.RuntimeControlClient,
		func(request serverapi.RuntimeDiscardQueuedUserMessageRequest) string { return request.SessionID },
		apicontract.RuntimeUserInputTrustedService.DiscardQueuedUserMessageValidated,
	),
	protocol.MethodRuntimeRecordPromptHistory: gatewaySessionTrustedCallNoResponse[apicontract.RuntimeControlService, apicontract.RuntimeTranscriptMutationTrustedService, serverapi.RuntimeRecordPromptHistoryRequest](
		GatewayDependencies.RuntimeControlClient,
		func(request serverapi.RuntimeRecordPromptHistoryRequest) string { return request.SessionID },
		apicontract.RuntimeTranscriptMutationTrustedService.RecordPromptHistoryValidated,
	),
	protocol.MethodRuntimeLiveSteer: gatewayRuntimeLiveTrustedCall(
		func(validated apicontract.Validated[serverapi.RuntimeLiveSteerRequest]) apicontract.RuntimeLiveRequestIdentity {
			request := validated.Value()
			identity := apicontract.RuntimeLiveRequestIdentity{
				SessionID:       validated.SessionID(request.SessionID),
				ClientRequestID: validated.RuntimeClientRequestID(request.ClientRequestID),
			}
			if request.CallerSessionID != nil {
				caller := validated.SessionID(*request.CallerSessionID)
				identity.CallerSessionID = &caller
			}
			return identity
		},
		apicontract.RuntimeLiveControlTrustedService.LiveSteerValidated,
	),
	protocol.MethodRuntimeLiveStop: gatewayRuntimeLiveTrustedCall(
		func(validated apicontract.Validated[serverapi.RuntimeLiveStopRequest]) apicontract.RuntimeLiveRequestIdentity {
			request := validated.Value()
			return apicontract.RuntimeLiveRequestIdentity{
				SessionID:       validated.SessionID(request.SessionID),
				ClientRequestID: validated.RuntimeClientRequestID(request.ClientRequestID),
			}
		},
		apicontract.RuntimeLiveControlTrustedService.LiveStopValidated,
	),
	protocol.MethodRuntimeLiveWait: gatewayRuntimeLiveTrustedCall(
		func(validated apicontract.Validated[serverapi.RuntimeLiveWaitRequest]) apicontract.RuntimeLiveRequestIdentity {
			return apicontract.RuntimeLiveRequestIdentity{SessionID: validated.SessionID(validated.Value().SessionID)}
		},
		apicontract.RuntimeLiveControlTrustedService.LiveWaitValidated,
	),
	protocol.MethodRuntimeLiveWatch: gatewayRuntimeLiveTrustedCall(
		func(validated apicontract.Validated[serverapi.RuntimeLiveWatchRequest]) apicontract.RuntimeLiveRequestIdentity {
			return apicontract.RuntimeLiveRequestIdentity{SessionID: validated.SessionID(validated.Value().SessionID)}
		},
		apicontract.RuntimeLiveControlTrustedService.LiveWatchValidated,
	),
	protocol.MethodRuntimeGoalShow: gatewayRuntimeGoalTrustedCall(
		func(request serverapi.RuntimeGoalShowRequest) string { return request.SessionID },
		apicontract.RuntimeGoalTrustedService.ShowGoalValidated,
	),
	protocol.MethodRuntimeGoalSet: gatewayRuntimeGoalTrustedCall(
		func(request serverapi.RuntimeGoalSetRequest) string { return request.SessionID },
		apicontract.RuntimeGoalTrustedService.SetGoalValidated,
	),
	protocol.MethodRuntimeGoalPause: gatewayRuntimeGoalTrustedCall(
		func(request serverapi.RuntimeGoalStatusRequest) string { return request.SessionID },
		apicontract.RuntimeGoalTrustedService.PauseGoalValidated,
	),
	protocol.MethodRuntimeGoalResume: gatewayRuntimeGoalTrustedCall(
		func(request serverapi.RuntimeGoalStatusRequest) string { return request.SessionID },
		apicontract.RuntimeGoalTrustedService.ResumeGoalValidated,
	),
	protocol.MethodRuntimeGoalComplete: gatewayRuntimeGoalTrustedCall(
		func(request serverapi.RuntimeGoalStatusRequest) string { return request.SessionID },
		apicontract.RuntimeGoalTrustedService.CompleteGoalValidated,
	),
	protocol.MethodRuntimeGoalClear: gatewayRuntimeGoalTrustedCall(
		func(request serverapi.RuntimeGoalClearRequest) string { return request.SessionID },
		apicontract.RuntimeGoalTrustedService.ClearGoalValidated,
	),
	protocol.MethodProcessList: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAuthorizeAndHandle(g, ctx, state, req, func(params serverapi.ProcessListRequest) (serverapi.ProcessListResponse, error) {
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
	protocol.MethodAskListPending: gatewaySessionMembershipTrustedCall[apicontract.AskViewService, apicontract.AskViewTrustedService, serverapi.AskListPendingBySessionRequest, serverapi.AskListPendingBySessionResponse](
		GatewayDependencies.AskViewClient,
		func(request serverapi.AskListPendingBySessionRequest) string { return request.SessionID },
		apicontract.AskViewTrustedService.ListPendingAsksBySessionValidated,
	),
	protocol.MethodPromptAnswerBatch: gatewaySessionMembershipTrustedCall[apicontract.PromptControlService, apicontract.PromptControlTrustedService, serverapi.PromptAnswerBatchRequest, serverapi.PromptAnswerBatchResponse](
		GatewayDependencies.PromptControlClient,
		func(request serverapi.PromptAnswerBatchRequest) string { return request.SessionID.String() },
		apicontract.PromptControlTrustedService.AnswerPromptBatchValidated,
	),
	protocol.MethodApprovalListPending: gatewaySessionMembershipTrustedCall[apicontract.ApprovalViewService, apicontract.ApprovalViewTrustedService, serverapi.ApprovalListPendingBySessionRequest, serverapi.ApprovalListPendingBySessionResponse](
		GatewayDependencies.ApprovalViewClient,
		func(request serverapi.ApprovalListPendingBySessionRequest) string { return request.SessionID },
		apicontract.ApprovalViewTrustedService.ListPendingApprovalsBySessionValidated,
	),
}
