package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/chatcontext"
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
			owner := g.deps.SessionChatContextOwner()
			if owner == nil {
				return serverapi.ChatContextResponse{}, errors.New("Session Chat Context owner is required")
			}
			contextFacts, err := owner.ReadSessionChatContext(ctx, sessionID)
			return serverapi.ChatContextResponse{Context: contextFacts}, err
		})
	},
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
	protocol.MethodWorkflowCreate:                 gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowCreateRequest, serverapi.WorkflowCreateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CreateWorkflow),
	protocol.MethodWorkflowCreateAndLinkProject:   gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowCreateAndLinkProjectRequest, serverapi.WorkflowCreateAndLinkProjectResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CreateAndLinkWorkflowToProject),
	protocol.MethodWorkflowUpdate:                 gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowUpdateRequest, serverapi.WorkflowGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.UpdateWorkflow),
	protocol.MethodWorkflowList:                   gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowListRequest, serverapi.WorkflowListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflows),
	protocol.MethodWorkflowGet:                    gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowGetRequest, serverapi.WorkflowGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.GetWorkflow),
	protocol.MethodWorkflowLinkProject:            gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowLinkProjectRequest, serverapi.WorkflowLinkProjectResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.LinkWorkflowToProject),
	protocol.MethodWorkflowListProjectLinks:       gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowListProjectLinksRequest, serverapi.WorkflowListProjectLinksResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListProjectWorkflowLinks),
	protocol.MethodWorkflowSetDefaultProjectLink:  gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowSetDefaultProjectLinkRequest, serverapi.WorkflowSetDefaultProjectLinkResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.SetDefaultProjectWorkflowLink),
	protocol.MethodWorkflowUnlinkProject:          gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowUnlinkProjectRequest, serverapi.WorkflowUnlinkProjectResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.UnlinkWorkflowFromProject),
	protocol.MethodWorkflowDeletePreview:          gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowDeletePreviewRequest, serverapi.WorkflowDeletePreviewResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.PreviewWorkflowDelete),
	protocol.MethodWorkflowDelete:                 gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowDeleteRequest, serverapi.WorkflowDeleteResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.DeleteWorkflow),
	protocol.MethodWorkflowValidate:               gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowValidateRequest, serverapi.WorkflowValidateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ValidateWorkflow),
	protocol.MethodWorkflowScriptPathValidate:     gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowScriptPathValidateRequest, serverapi.WorkflowValidateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ValidateWorkflowScriptPath),
	protocol.MethodWorkflowGraphValidateDraft:     gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowGraphValidateDraftRequest, serverapi.WorkflowGraphValidateDraftResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ValidateWorkflowGraphDraft),
	protocol.MethodWorkflowGraphDeriveWiring:      gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowGraphDeriveWiringRequest, serverapi.WorkflowGraphDeriveWiringResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.DeriveWorkflowGraphWiring),
	protocol.MethodWorkflowGraphSavePreview:       gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowGraphSavePreviewRequest, serverapi.WorkflowGraphSavePreviewResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.PreviewWorkflowGraphSave),
	protocol.MethodWorkflowGraphSave:              gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowGraphSaveRequest, serverapi.WorkflowGraphSaveResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.SaveWorkflowGraph),
	protocol.MethodWorkflowProjectLabelCreate:     gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowProjectLabelCreateRequest, serverapi.WorkflowProjectLabelCreateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CreateWorkflowProjectLabel),
	protocol.MethodWorkflowProjectLabelList:       gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowProjectLabelCatalogRequest, serverapi.WorkflowProjectLabelCatalogResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowProjectLabels),
	protocol.MethodWorkflowProjectLabelRename:     gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowProjectLabelRenameRequest, serverapi.WorkflowProjectLabelRenameResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.RenameWorkflowProjectLabel),
	protocol.MethodWorkflowProjectLabelDelete:     gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowProjectLabelDeleteRequest, serverapi.WorkflowProjectLabelDeleteResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.DeleteWorkflowProjectLabel),
	protocol.MethodWorkflowProjectLabelReorder:    gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowProjectLabelReorderRequest, serverapi.WorkflowProjectLabelReorderResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ReorderWorkflowProjectLabels),
	protocol.MethodWorkflowTaskLabelsGet:          gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskLabelsGetRequest, serverapi.WorkflowTaskLabelsGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.GetWorkflowTaskLabels),
	protocol.MethodWorkflowTaskLabelsUpdate:       gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskLabelsUpdateRequest, serverapi.WorkflowTaskLabelsUpdateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.UpdateWorkflowTaskLabels),
	protocol.MethodWorkflowTaskCreate:             gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskCreateRequest, serverapi.WorkflowTaskCreateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CreateWorkflowTask),
	protocol.MethodWorkflowTaskDependencyAdd:      gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskDependencyAddRequest, serverapi.WorkflowTaskDependencyAddResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.AddWorkflowTaskDependency),
	protocol.MethodWorkflowTaskDependencyRemove:   gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskDependencyRemoveRequest, serverapi.WorkflowTaskDependencyRemoveResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.RemoveWorkflowTaskDependency),
	protocol.MethodWorkflowTaskDependencyList:     gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskDependencyListRequest, serverapi.WorkflowTaskDependencyListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTaskDependencies),
	protocol.MethodWorkflowTaskUpdate:             gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskUpdateRequest, serverapi.WorkflowTaskUpdateResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.UpdateWorkflowTask),
	protocol.MethodWorkflowTaskStart:              gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskStartRequest, serverapi.WorkflowTaskStartResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.StartWorkflowTask),
	protocol.MethodWorkflowTaskInterrupt:          gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskInterruptRequest, serverapi.WorkflowTaskInterruptResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.InterruptWorkflowTask),
	protocol.MethodWorkflowTaskResume:             gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskResumeRequest, serverapi.WorkflowTaskResumeResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ResumeWorkflowTask),
	protocol.MethodWorkflowTaskApprove:            gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskApproveRequest, serverapi.WorkflowTaskApproveResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ApproveWorkflowTask),
	protocol.MethodWorkflowTaskMovePreview:        gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskMovePreviewRequest, serverapi.WorkflowTaskMovePreviewResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.PreviewWorkflowTaskMove),
	protocol.MethodWorkflowTaskMove:               gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskMoveRequest, serverapi.WorkflowTaskMoveResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.MoveWorkflowTask),
	protocol.MethodWorkflowTaskComplete:           gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskCompleteRequest, serverapi.WorkflowTaskCompleteResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.CompleteWorkflowTask),
	protocol.MethodWorkflowTaskDelete:             gatewayClientCallNoResponse[apicontract.WorkflowService, serverapi.WorkflowTaskDeleteRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.DeleteWorkflowTask),
	protocol.MethodWorkflowAttentionList:          gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowAttentionListRequest, serverapi.WorkflowAttentionListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowAttention),
	protocol.MethodWorkflowTaskAttentionList:      gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskAttentionListRequest, serverapi.WorkflowTaskAttentionListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTaskAttention),
	protocol.MethodWorkflowTaskCommentAdd:         gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskCommentAddRequest, serverapi.WorkflowTaskCommentAddResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.AddWorkflowTaskComment),
	protocol.MethodWorkflowTaskCommentList:        gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskCommentListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTaskComments),
	protocol.MethodWorkflowTaskCommentReplace:     gatewayClientCallNoResponse[apicontract.WorkflowService, serverapi.WorkflowTaskCommentReplaceRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ReplaceWorkflowTaskComment),
	protocol.MethodWorkflowTaskCommentDelete:      gatewayClientCallNoResponse[apicontract.WorkflowService, serverapi.WorkflowTaskCommentDeleteRequest](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.DeleteWorkflowTaskComment),
	protocol.MethodWorkflowTaskActivityList:       gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskActivityListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTaskActivity),
	protocol.MethodWorkflowTaskSessionList:        gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskSessionListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTaskSessions),
	protocol.MethodWorkflowTaskList:               gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskListRequest, serverapi.WorkflowTaskListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowTasks),
	protocol.MethodWorkflowProjectTaskGroupCounts: gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowProjectTaskGroupCountsRequest, serverapi.WorkflowProjectTaskGroupCountsResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.GetWorkflowProjectTaskGroupCounts),
	protocol.MethodWorkflowTaskSearch:             gatewayClientCall[apicontract.WorkflowService, serverapi.TaskSearchRequest, serverapi.TaskSearchResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.SearchWorkflowTasks),
	protocol.MethodWorkflowBoardGet:               gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowBoardRequest, serverapi.WorkflowBoardResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.GetWorkflowBoard),
	protocol.MethodWorkflowBoardNodeCardsList:     gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowBoardNodeCardsListRequest, serverapi.WorkflowBoardNodeCardsListResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ListWorkflowBoardNodeCards),
	protocol.MethodWorkflowTaskGet:                gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskGetRequest, serverapi.WorkflowTaskGetResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.GetWorkflowTask),
	protocol.MethodWorkflowTaskObserve:            gatewayClientCall[apicontract.WorkflowService, serverapi.WorkflowTaskObservationRequest, serverapi.WorkflowTaskObservationResponse](GatewayDependencies.WorkflowClient, apicontract.WorkflowService.ObserveWorkflowTask),
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
				if err := g.requireSessionInActiveProject(
					ctx,
					state,
					params.Target.Session.String(),
				); err != nil {
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
	protocol.MethodSessionGetMainView:                            gatewayClientCall[apicontract.SessionViewService, serverapi.SessionMainViewRequest, serverapi.SessionMainViewResponse](GatewayDependencies.SessionViewClient, apicontract.SessionViewService.GetSessionMainView),
	protocol.MethodSessionGetTranscriptPage:                      gatewayClientCall[apicontract.SessionViewService, serverapi.SessionTranscriptPageRequest, serverapi.SessionTranscriptPageResponse](GatewayDependencies.SessionViewClient, apicontract.SessionViewService.GetSessionTranscriptPage),
	protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer: gatewayClientCall[apicontract.SessionViewService, serverapi.SessionLatestCommittedAssistantFinalAnswerRequest, serverapi.SessionLatestCommittedAssistantFinalAnswerResponse](GatewayDependencies.SessionViewClient, apicontract.SessionViewService.GetLatestCommittedAssistantFinalAnswer),
	protocol.MethodSessionGetExecutionEnvironment: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		params, err := g.sessionExecutionRequestContract.Decode(req.Params)
		if err != nil {
			return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, fmt.Sprintf("decode params: %v", err))
		}
		response, err := g.deps.SessionViewClient().GetSessionExecutionEnvironment(ctx, params)
		if err != nil {
			return responseForError(req.ID, err)
		}
		return protocol.NewSuccessResponse(req.ID, response)
	},
	protocol.MethodSessionGetInitialInput:      gatewayClientCall[apicontract.SessionLifecycleService, serverapi.SessionInitialInputRequest, serverapi.SessionInitialInputResponse](GatewayDependencies.SessionLifecycleClient, apicontract.SessionLifecycleService.GetInitialInput),
	protocol.MethodSessionPersistInputDraft:    gatewayClientCall[apicontract.SessionLifecycleService, serverapi.SessionPersistInputDraftRequest, serverapi.SessionPersistInputDraftResponse](GatewayDependencies.SessionLifecycleClient, apicontract.SessionLifecycleService.PersistInputDraft),
	protocol.MethodSessionRetargetWorkspace:    gatewayClientCall[apicontract.SessionLifecycleService, serverapi.SessionRetargetWorkspaceRequest, serverapi.SessionRetargetWorkspaceResponse](GatewayDependencies.SessionLifecycleClient, apicontract.SessionLifecycleService.RetargetSessionWorkspace),
	protocol.MethodSessionResolveTransition:    gatewayClientCall[apicontract.SessionLifecycleService, serverapi.SessionResolveTransitionRequest, serverapi.SessionResolveTransitionResponse](GatewayDependencies.SessionLifecycleClient, apicontract.SessionLifecycleService.ResolveTransition),
	protocol.MethodWorktreeStatus:              gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeStatusRequest, serverapi.WorktreeStatusResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.GetWorktreeStatus),
	protocol.MethodWorktreeList:                gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeListRequest, serverapi.WorktreeListResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.ListWorktrees),
	protocol.MethodWorktreeWorkspaceList:       gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeWorkspaceListRequest, serverapi.WorktreeWorkspaceListResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.ListWorkspaceWorktrees),
	protocol.MethodWorktreeSelectorResolve:     gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeSelectorPreviewRequest, serverapi.WorktreeSelectorPreviewResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.ResolveWorktreeSelector),
	protocol.MethodWorktreeDeletePreview:       gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeDeletePreviewRequest, serverapi.WorktreeDeletePreviewResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.PreviewWorktreeDelete),
	protocol.MethodWorktreeCreateTargetResolve: gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeCreateTargetResolveRequest, serverapi.WorktreeCreateTargetResolveResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.ResolveWorktreeCreateTarget),
	protocol.MethodWorktreeCreate:              gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeCreateRequest, serverapi.WorktreeCreateResponse](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.CreateWorktree),
	protocol.MethodWorktreeEnter:               gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeEnterRequest, serverapi.WorktreeScheduledAcknowledgement](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.EnterWorktree),
	protocol.MethodWorktreeLeave:               gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeLeaveRequest, serverapi.WorktreeScheduledAcknowledgement](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.LeaveWorktree),
	protocol.MethodWorktreeDelete:              gatewayClientCall[apicontract.WorktreeService, serverapi.WorktreeDeleteRequest, serverapi.WorktreeDeleteResult](GatewayDependencies.WorktreeClient, apicontract.WorktreeService.DeleteWorktree),
	protocol.MethodSessionRuntimeActivate: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
			params.OwnerID = state.runtimeOwnerID
			resp, err := g.deps.SessionRuntimeClient().ActivateSessionRuntime(ctx, params)
			if err != nil {
				return serverapi.SessionRuntimeActivateResponse{}, err
			}
			if err := resp.ValidateForSession(params.SessionID); err != nil {
				return serverapi.SessionRuntimeActivateResponse{}, err
			}
			state.recordOwnedRuntime(resp.Attachment)
			return resp, nil
		})
	},
	protocol.MethodSessionRuntimeRelease: func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
		return decodeAndHandle(req, func(params serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
			params.OwnerID = state.runtimeOwnerID
			resp, err := g.deps.SessionRuntimeClient().ReleaseSessionRuntime(ctx, params)
			if err == nil && (resp.Released || params.DropOwner) {
				state.removeOwnedRuntime(params.Attachment)
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
	protocol.MethodRuntimeInterrupt:                      gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeInterruptRequest, serverapi.RuntimeInterruptResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.Interrupt),
	protocol.MethodRuntimeLiveSteer:                      gatewayClientCall[apicontract.RuntimeLiveControlService, serverapi.RuntimeLiveSteerRequest, serverapi.RuntimeLiveSteerResponse](GatewayDependencies.RuntimeLiveControlClient, apicontract.RuntimeLiveControlService.LiveSteer),
	protocol.MethodRuntimeLiveStop:                       gatewayClientCall[apicontract.RuntimeLiveControlService, serverapi.RuntimeLiveStopRequest, serverapi.RuntimeLiveStopResponse](GatewayDependencies.RuntimeLiveControlClient, apicontract.RuntimeLiveControlService.LiveStop),
	protocol.MethodRuntimeLiveWait:                       gatewayClientCall[apicontract.RuntimeLiveControlService, serverapi.RuntimeLiveWaitRequest, serverapi.RuntimeLiveWaitResponse](GatewayDependencies.RuntimeLiveControlClient, apicontract.RuntimeLiveControlService.LiveWait),
	protocol.MethodRuntimeLiveWatch:                      gatewayClientCall[apicontract.RuntimeLiveControlService, serverapi.RuntimeLiveWatchRequest, serverapi.RuntimeLiveWatchResponse](GatewayDependencies.RuntimeLiveControlClient, apicontract.RuntimeLiveControlService.LiveWatch),
	protocol.MethodRuntimeDiscardQueuedUserMessage:       gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeDiscardQueuedUserMessageRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.DiscardQueuedUserMessage),
	protocol.MethodRuntimeRecordPromptHistory:            gatewayClientCallNoResponse[apicontract.RuntimeControlService, serverapi.RuntimeRecordPromptHistoryRequest](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.RecordPromptHistory),
	protocol.MethodRuntimeGoalShow:                       gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeGoalShowRequest, serverapi.RuntimeGoalShowResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.ShowGoal),
	protocol.MethodRuntimeGoalSet:                        gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeGoalSetRequest, serverapi.RuntimeGoalMutationResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.SetGoal),
	protocol.MethodRuntimeGoalPause:                      gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalMutationResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.PauseGoal),
	protocol.MethodRuntimeGoalResume:                     gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalMutationResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.ResumeGoal),
	protocol.MethodRuntimeGoalComplete:                   gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalMutationResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.CompleteGoal),
	protocol.MethodRuntimeGoalClear:                      gatewayClientCall[apicontract.RuntimeControlService, serverapi.RuntimeGoalClearRequest, serverapi.RuntimeGoalMutationResponse](GatewayDependencies.RuntimeControlClient, apicontract.RuntimeControlService.ClearGoal),
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
	protocol.MethodPromptAnswerBatch:   gatewayClientCall[apicontract.PromptControlService, serverapi.PromptAnswerBatchRequest, serverapi.PromptAnswerBatchResponse](GatewayDependencies.PromptControlClient, apicontract.PromptControlService.AnswerPromptBatch),
	protocol.MethodApprovalListPending: gatewayClientCall[apicontract.ApprovalViewService, serverapi.ApprovalListPendingBySessionRequest, serverapi.ApprovalListPendingBySessionResponse](GatewayDependencies.ApprovalViewClient, apicontract.ApprovalViewService.ListPendingApprovalsBySession),
}
