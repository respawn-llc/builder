package apicontract

import (
	"reflect"
	"sort"

	"core/shared/protocol"
	"core/shared/serverapi"
)

type Kind string

const (
	KindUnary        Kind = "unary"
	KindSubscription Kind = "subscription"
	KindProgress     Kind = "progress"
	KindNotification Kind = "notification"
)

type AuthPolicy string

const (
	AuthNone          AuthPolicy = "none"
	AuthPreServerAuth AuthPolicy = "pre_server_auth"
	AuthServer        AuthPolicy = "server_auth"
)

type ScopePolicy string

const (
	ScopeNone                       ScopePolicy = "none"
	ScopeAttachProject              ScopePolicy = "attach_project"
	ScopeAttachSession              ScopePolicy = "attach_session"
	ScopeProjectView                ScopePolicy = "project_view"
	ScopeProjectWorkspace           ScopePolicy = "project_workspace"
	ScopeProjectWorkspaceBinding    ScopePolicy = "project_workspace_binding"
	ScopeSessionActiveProject       ScopePolicy = "session_active_project"
	ScopeSessionActiveProjectIfSet  ScopePolicy = "session_active_project_if_set"
	ScopeSessionAttachedProject     ScopePolicy = "session_attached_project"
	ScopeAttachedSession            ScopePolicy = "attached_session"
	ScopeGoalSession                ScopePolicy = "goal_session"
	ScopeRuntimeLiveSessionRequired ScopePolicy = "runtime_live_session_required"
	ScopeRuntimeLiveSessionOptional ScopePolicy = "runtime_live_session_optional"
	ScopeProcessActiveProject       ScopePolicy = "process_active_project"
	ScopeProcessListActiveProject   ScopePolicy = "process_list_active_project"
	ScopeNotification               ScopePolicy = "notification"
)

type ConnectionStrategy string

const (
	ConnectionControl      ConnectionStrategy = "control"
	ConnectionUnscoped     ConnectionStrategy = "unscoped_control"
	ConnectionDedicated    ConnectionStrategy = "dedicated"
	ConnectionSubscription ConnectionStrategy = "subscription"
	ConnectionProgress     ConnectionStrategy = "progress"
	ConnectionNotification ConnectionStrategy = "notification"
)

type Route struct {
	Method             string
	Kind               Kind
	Auth               AuthPolicy
	Scope              ScopePolicy
	Connection         ConnectionStrategy
	RequestType        reflect.Type
	ResponseType       reflect.Type
	EventMethod        string
	EventType          reflect.Type
	CompleteMethod     string
	CompleteType       reflect.Type
	DedicatedRequestID string
	ValidatesRequest   bool
}

const (
	UpdateStatusDedicatedRequestID = "get-update-status"
	TaskSearchDedicatedRequestID   = "workflow-task-search"
)

func unary[Req any, Resp any](method string, auth AuthPolicy, scope ScopePolicy, connection ConnectionStrategy) Route {
	reqType := reflect.TypeOf((*Req)(nil)).Elem()
	return Route{
		Method:           method,
		Kind:             KindUnary,
		Auth:             auth,
		Scope:            scope,
		Connection:       connection,
		RequestType:      reqType,
		ResponseType:     reflect.TypeOf((*Resp)(nil)).Elem(),
		ValidatesRequest: implementsValidator(reqType),
	}
}

func dedicatedUnary[Req any, Resp any](method string, requestID string, scope ScopePolicy) Route {
	route := unary[Req, Resp](method, AuthServer, scope, ConnectionDedicated)
	route.DedicatedRequestID = requestID
	return route
}

func subscription[Req any, Event any](method string, auth AuthPolicy, scope ScopePolicy, eventMethod string, completeMethod string) Route {
	reqType := reflect.TypeOf((*Req)(nil)).Elem()
	return Route{
		Method:           method,
		Kind:             KindSubscription,
		Auth:             auth,
		Scope:            scope,
		Connection:       ConnectionSubscription,
		RequestType:      reqType,
		ResponseType:     reflect.TypeOf((*protocol.SubscribeResponse)(nil)).Elem(),
		EventMethod:      eventMethod,
		EventType:        reflect.TypeOf((*Event)(nil)).Elem(),
		CompleteMethod:   completeMethod,
		CompleteType:     reflect.TypeOf((*protocol.StreamCompleteParams)(nil)).Elem(),
		ValidatesRequest: implementsValidator(reqType),
	}
}

func progress[Req any, Resp any, Event any](method string, scope ScopePolicy, eventMethod string) Route {
	reqType := reflect.TypeOf((*Req)(nil)).Elem()
	return Route{
		Method:           method,
		Kind:             KindProgress,
		Auth:             AuthServer,
		Scope:            scope,
		Connection:       ConnectionProgress,
		RequestType:      reqType,
		ResponseType:     reflect.TypeOf((*Resp)(nil)).Elem(),
		EventMethod:      eventMethod,
		EventType:        reflect.TypeOf((*Event)(nil)).Elem(),
		ValidatesRequest: implementsValidator(reqType),
	}
}

func notification[Event any](method string) Route {
	return Route{
		Method:      method,
		Kind:        KindNotification,
		Auth:        AuthNone,
		Scope:       ScopeNotification,
		Connection:  ConnectionNotification,
		RequestType: reflect.TypeOf((*Event)(nil)).Elem(),
	}
}

func implementsValidator(t reflect.Type) bool {
	validator := reflect.TypeOf((*interface{ Validate() error })(nil)).Elem()
	return t != nil && t.Implements(validator)
}

var routeContracts = []Route{
	unary[serverapi.ChatContextRequest, serverapi.ChatContextResponse](protocol.MethodChatContextGet, AuthPreServerAuth, ScopeSessionActiveProjectIfSet, ConnectionControl),
	unary[serverapi.PromptCommandCatalogRequest, serverapi.PromptCommandCatalogResponse](protocol.MethodPromptCommandCatalogGet, AuthServer, ScopeProjectWorkspace, ConnectionControl),
	unary[serverapi.ChatSettingsReadRequest, serverapi.ChatSettingsReadResponse](protocol.MethodChatSettingsRead, AuthServer, ScopeNone, ConnectionControl),
	unary[serverapi.WorkflowCreateRequest, serverapi.WorkflowCreateResponse](protocol.MethodWorkflowCreate, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowCreateAndLinkProjectRequest, serverapi.WorkflowCreateAndLinkProjectResponse](protocol.MethodWorkflowCreateAndLinkProject, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowUpdateRequest, serverapi.WorkflowGetResponse](protocol.MethodWorkflowUpdate, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowListRequest, serverapi.WorkflowListResponse](protocol.MethodWorkflowList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowGetRequest, serverapi.WorkflowGetResponse](protocol.MethodWorkflowGet, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowLinkProjectRequest, serverapi.WorkflowLinkProjectResponse](protocol.MethodWorkflowLinkProject, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowListProjectLinksRequest, serverapi.WorkflowListProjectLinksResponse](protocol.MethodWorkflowListProjectLinks, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowSetDefaultProjectLinkRequest, serverapi.WorkflowSetDefaultProjectLinkResponse](protocol.MethodWorkflowSetDefaultProjectLink, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowUnlinkProjectRequest, serverapi.WorkflowUnlinkProjectResponse](protocol.MethodWorkflowUnlinkProject, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowDeletePreviewRequest, serverapi.WorkflowDeletePreviewResponse](protocol.MethodWorkflowDeletePreview, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowDeleteRequest, serverapi.WorkflowDeleteResponse](protocol.MethodWorkflowDelete, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowValidateRequest, serverapi.WorkflowValidateResponse](protocol.MethodWorkflowValidate, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowScriptPathValidateRequest, serverapi.WorkflowValidateResponse](protocol.MethodWorkflowScriptPathValidate, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowGraphValidateDraftRequest, serverapi.WorkflowGraphValidateDraftResponse](protocol.MethodWorkflowGraphValidateDraft, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowGraphDeriveWiringRequest, serverapi.WorkflowGraphDeriveWiringResponse](protocol.MethodWorkflowGraphDeriveWiring, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowGraphSavePreviewRequest, serverapi.WorkflowGraphSavePreviewResponse](protocol.MethodWorkflowGraphSavePreview, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowGraphSaveRequest, serverapi.WorkflowGraphSaveResponse](protocol.MethodWorkflowGraphSave, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowProjectLabelCreateRequest, serverapi.WorkflowProjectLabelCreateResponse](protocol.MethodWorkflowProjectLabelCreate, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowProjectLabelCatalogRequest, serverapi.WorkflowProjectLabelCatalogResponse](protocol.MethodWorkflowProjectLabelList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowProjectLabelRenameRequest, serverapi.WorkflowProjectLabelRenameResponse](protocol.MethodWorkflowProjectLabelRename, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowProjectLabelDeleteRequest, serverapi.WorkflowProjectLabelDeleteResponse](protocol.MethodWorkflowProjectLabelDelete, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowProjectLabelReorderRequest, serverapi.WorkflowProjectLabelReorderResponse](protocol.MethodWorkflowProjectLabelReorder, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskLabelsGetRequest, serverapi.WorkflowTaskLabelsGetResponse](protocol.MethodWorkflowTaskLabelsGet, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskLabelsUpdateRequest, serverapi.WorkflowTaskLabelsUpdateResponse](protocol.MethodWorkflowTaskLabelsUpdate, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskCreateRequest, serverapi.WorkflowTaskCreateResponse](protocol.MethodWorkflowTaskCreate, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskDependencyAddRequest, serverapi.WorkflowTaskDependencyAddResponse](protocol.MethodWorkflowTaskDependencyAdd, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskDependencyRemoveRequest, serverapi.WorkflowTaskDependencyRemoveResponse](protocol.MethodWorkflowTaskDependencyRemove, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskDependencyListRequest, serverapi.WorkflowTaskDependencyListResponse](protocol.MethodWorkflowTaskDependencyList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskUpdateRequest, serverapi.WorkflowTaskUpdateResponse](protocol.MethodWorkflowTaskUpdate, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskStartRequest, serverapi.WorkflowTaskStartResponse](protocol.MethodWorkflowTaskStart, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskInterruptRequest, serverapi.WorkflowTaskInterruptResponse](protocol.MethodWorkflowTaskInterrupt, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskResumeRequest, serverapi.WorkflowTaskResumeResponse](protocol.MethodWorkflowTaskResume, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskApproveRequest, serverapi.WorkflowTaskApproveResponse](protocol.MethodWorkflowTaskApprove, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskMovePreviewRequest, serverapi.WorkflowTaskMovePreviewResponse](protocol.MethodWorkflowTaskMovePreview, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskMoveRequest, serverapi.WorkflowTaskMoveResponse](protocol.MethodWorkflowTaskMove, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskCompleteRequest, serverapi.WorkflowTaskCompleteResponse](protocol.MethodWorkflowTaskComplete, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskDeleteRequest, struct{}](protocol.MethodWorkflowTaskDelete, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowAttentionListRequest, serverapi.WorkflowAttentionListResponse](protocol.MethodWorkflowAttentionList, AuthServer, ScopeNone, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskAttentionListRequest, serverapi.WorkflowTaskAttentionListResponse](protocol.MethodWorkflowTaskAttentionList, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskCommentAddRequest, serverapi.WorkflowTaskCommentAddResponse](protocol.MethodWorkflowTaskCommentAdd, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskCommentListResponse](protocol.MethodWorkflowTaskCommentList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskCommentReplaceRequest, struct{}](protocol.MethodWorkflowTaskCommentReplace, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskCommentDeleteRequest, struct{}](protocol.MethodWorkflowTaskCommentDelete, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskActivityListResponse](protocol.MethodWorkflowTaskActivityList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskOffsetPageRequest, serverapi.WorkflowTaskSessionListResponse](protocol.MethodWorkflowTaskSessionList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskListRequest, serverapi.WorkflowTaskListResponse](protocol.MethodWorkflowTaskList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowProjectTaskGroupCountsRequest, serverapi.WorkflowProjectTaskGroupCountsResponse](protocol.MethodWorkflowProjectTaskGroupCounts, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	dedicatedUnary[serverapi.TaskSearchRequest, serverapi.TaskSearchResponse](protocol.MethodWorkflowTaskSearch, TaskSearchDedicatedRequestID, ScopeNone),
	unary[serverapi.WorkflowBoardRequest, serverapi.WorkflowBoardResponse](protocol.MethodWorkflowBoardGet, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowBoardNodeCardsListRequest, serverapi.WorkflowBoardNodeCardsListResponse](protocol.MethodWorkflowBoardNodeCardsList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskGetRequest, serverapi.WorkflowTaskGetResponse](protocol.MethodWorkflowTaskGet, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.WorkflowTaskObservationRequest, serverapi.WorkflowTaskObservationResponse](protocol.MethodWorkflowTaskObserve, AuthServer, ScopeProjectView, ConnectionUnscoped),
	unary[serverapi.SessionMainViewRequest, serverapi.SessionMainViewResponse](protocol.MethodSessionGetMainView, AuthPreServerAuth, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.SessionTranscriptPageRequest, serverapi.SessionTranscriptPageResponse](protocol.MethodSessionGetTranscriptPage, AuthPreServerAuth, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.SessionLatestCommittedAssistantFinalAnswerRequest, serverapi.SessionLatestCommittedAssistantFinalAnswerResponse](protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer, AuthPreServerAuth, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.SessionExecutionEnvironmentRequest, serverapi.SessionExecutionEnvironmentResponse](protocol.MethodSessionGetExecutionEnvironment, AuthPreServerAuth, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.SessionInitialInputRequest, serverapi.SessionInitialInputResponse](protocol.MethodSessionGetInitialInput, AuthPreServerAuth, ScopeSessionActiveProjectIfSet, ConnectionControl),
	unary[serverapi.SessionPersistInputDraftRequest, serverapi.SessionPersistInputDraftResponse](protocol.MethodSessionPersistInputDraft, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.SessionRetargetWorkspaceRequest, serverapi.SessionRetargetWorkspaceResponse](protocol.MethodSessionRetargetWorkspace, AuthServer, ScopeSessionAttachedProject, ConnectionUnscoped),
	unary[serverapi.SessionResolveTransitionRequest, serverapi.SessionResolveTransitionResponse](protocol.MethodSessionResolveTransition, AuthServer, ScopeSessionActiveProjectIfSet, ConnectionControl),
	unary[serverapi.SessionRuntimeActivateRequest, serverapi.SessionRuntimeActivateResponse](protocol.MethodSessionRuntimeActivate, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.SessionRuntimeReleaseRequest, serverapi.SessionRuntimeReleaseResponse](protocol.MethodSessionRuntimeRelease, AuthServer, ScopeNone, ConnectionControl),
	unary[serverapi.WorktreeStatusRequest, serverapi.WorktreeStatusResponse](protocol.MethodWorktreeStatus, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.WorktreeListRequest, serverapi.WorktreeListResponse](protocol.MethodWorktreeList, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.WorktreeWorkspaceListRequest, serverapi.WorktreeWorkspaceListResponse](protocol.MethodWorktreeWorkspaceList, AuthServer, ScopeProjectWorkspaceBinding, ConnectionControl),
	unary[serverapi.WorktreeSelectorPreviewRequest, serverapi.WorktreeSelectorPreviewResponse](protocol.MethodWorktreeSelectorResolve, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.WorktreeDeletePreviewRequest, serverapi.WorktreeDeletePreviewResponse](protocol.MethodWorktreeDeletePreview, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.WorktreeCreateTargetResolveRequest, serverapi.WorktreeCreateTargetResolveResponse](protocol.MethodWorktreeCreateTargetResolve, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.WorktreeCreateRequest, serverapi.WorktreeCreateResponse](protocol.MethodWorktreeCreate, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.WorktreeEnterRequest, serverapi.WorktreeScheduledAcknowledgement](protocol.MethodWorktreeEnter, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.WorktreeLeaveRequest, serverapi.WorktreeScheduledAcknowledgement](protocol.MethodWorktreeLeave, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.WorktreeDeleteRequest, serverapi.WorktreeDeleteResult](protocol.MethodWorktreeDelete, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	subscription[serverapi.WorktreeSetupSubscribeRequest, protocol.WorktreeSetupEventParams](protocol.MethodWorktreeSetupSubscribe, AuthServer, ScopeNone, protocol.MethodWorktreeSetupEvent, protocol.MethodWorktreeSetupComplete),
	unary[serverapi.RuntimeSetSessionNameRequest, struct{}](protocol.MethodRuntimeSetSessionName, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.RuntimeSetThinkingLevelRequest, struct{}](protocol.MethodRuntimeSetThinkingLevel, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.RuntimeSetFastModeEnabledRequest, serverapi.RuntimeSetFastModeEnabledResponse](protocol.MethodRuntimeSetFastModeEnabled, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.RuntimeSetReviewerEnabledRequest, serverapi.RuntimeSetReviewerEnabledResponse](protocol.MethodRuntimeSetReviewerEnabled, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.RuntimeSetAutoCompactionEnabledRequest, serverapi.RuntimeSetAutoCompactionEnabledResponse](protocol.MethodRuntimeSetAutoCompactionEnabled, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.RuntimeSetQuestionsEnabledRequest, serverapi.RuntimeSetQuestionsEnabledResponse](protocol.MethodRuntimeSetQuestionsEnabled, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.RuntimeAppendCommittedEntryRequest, struct{}](protocol.MethodRuntimeAppendCommittedEntry, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.RuntimeShouldCompactBeforeUserMessageRequest, serverapi.RuntimeShouldCompactBeforeUserMessageResponse](protocol.MethodRuntimeShouldCompactBeforeUserMessage, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	dedicatedUnary[serverapi.RuntimeSubmitUserTurnRequest, serverapi.RuntimeSubmitUserTurnResponse](protocol.MethodRuntimeSubmitUserTurn, "runtime-submit-user-turn", ScopeSessionActiveProject),
	dedicatedUnary[serverapi.RuntimeSubmitUserShellCommandRequest, struct{}](protocol.MethodRuntimeSubmitUserShellCommand, "runtime-submit-user-shell-command", ScopeSessionActiveProject),
	dedicatedUnary[serverapi.RuntimeCompactContextRequest, struct{}](protocol.MethodRuntimeCompactContext, "runtime-compact-context", ScopeSessionActiveProject),
	dedicatedUnary[serverapi.RuntimeInterruptRequest, serverapi.RuntimeInterruptResponse](protocol.MethodRuntimeInterrupt, "runtime-interrupt", ScopeSessionActiveProject),
	unary[serverapi.RuntimeLiveSteerRequest, serverapi.RuntimeLiveSteerResponse](protocol.MethodRuntimeLiveSteer, AuthServer, ScopeRuntimeLiveSessionRequired, ConnectionControl),
	dedicatedUnary[serverapi.RuntimeLiveStopRequest, serverapi.RuntimeLiveStopResponse](protocol.MethodRuntimeLiveStop, "runtime-live-stop", ScopeRuntimeLiveSessionOptional),
	dedicatedUnary[serverapi.RuntimeLiveWaitRequest, serverapi.RuntimeLiveWaitResponse](protocol.MethodRuntimeLiveWait, "runtime-live-wait", ScopeRuntimeLiveSessionRequired),
	dedicatedUnary[serverapi.RuntimeLiveWatchRequest, serverapi.RuntimeLiveWatchResponse](protocol.MethodRuntimeLiveWatch, "runtime-live-watch", ScopeRuntimeLiveSessionOptional),
	unary[serverapi.RuntimeDiscardQueuedUserMessageRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse](protocol.MethodRuntimeDiscardQueuedUserMessage, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.RuntimeRecordPromptHistoryRequest, struct{}](protocol.MethodRuntimeRecordPromptHistory, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.RuntimeGoalShowRequest, serverapi.RuntimeGoalShowResponse](protocol.MethodRuntimeGoalShow, AuthServer, ScopeGoalSession, ConnectionControl),
	unary[serverapi.RuntimeGoalSetRequest, serverapi.RuntimeGoalMutationResponse](protocol.MethodRuntimeGoalSet, AuthServer, ScopeGoalSession, ConnectionControl),
	unary[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalMutationResponse](protocol.MethodRuntimeGoalPause, AuthServer, ScopeGoalSession, ConnectionControl),
	unary[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalMutationResponse](protocol.MethodRuntimeGoalResume, AuthServer, ScopeGoalSession, ConnectionControl),
	unary[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalMutationResponse](protocol.MethodRuntimeGoalComplete, AuthServer, ScopeGoalSession, ConnectionControl),
	unary[serverapi.RuntimeGoalClearRequest, serverapi.RuntimeGoalMutationResponse](protocol.MethodRuntimeGoalClear, AuthServer, ScopeGoalSession, ConnectionControl),
	unary[serverapi.ProcessListRequest, serverapi.ProcessListResponse](protocol.MethodProcessList, AuthPreServerAuth, ScopeProcessListActiveProject, ConnectionControl),
	unary[serverapi.ProcessGetRequest, serverapi.ProcessGetResponse](protocol.MethodProcessGet, AuthPreServerAuth, ScopeProcessActiveProject, ConnectionControl),
	unary[serverapi.ProcessKillRequest, serverapi.ProcessKillResponse](protocol.MethodProcessKill, AuthServer, ScopeProcessActiveProject, ConnectionControl),
	unary[serverapi.ProcessInlineOutputRequest, serverapi.ProcessInlineOutputResponse](protocol.MethodProcessInlineOutput, AuthServer, ScopeProcessActiveProject, ConnectionControl),
	unary[serverapi.AskListPendingBySessionRequest, serverapi.AskListPendingBySessionResponse](protocol.MethodAskListPending, AuthPreServerAuth, ScopeSessionActiveProject, ConnectionControl),
	unary[serverapi.PromptAnswerBatchRequest, serverapi.PromptAnswerBatchResponse](protocol.MethodPromptAnswerBatch, AuthServer, ScopeSessionActiveProject, ConnectionControl),
	subscription[serverapi.PromptFollowUpWatchRequest, protocol.PromptFollowUpEventParams](protocol.MethodPromptFollowUpWatch, AuthServer, ScopeSessionActiveProject, protocol.MethodPromptFollowUpEvent, protocol.MethodPromptFollowUpComplete),
	unary[serverapi.ApprovalListPendingBySessionRequest, serverapi.ApprovalListPendingBySessionResponse](protocol.MethodApprovalListPending, AuthPreServerAuth, ScopeSessionActiveProject, ConnectionControl),
	progress[serverapi.RunPromptRequest, serverapi.RunPromptResponse, serverapi.RunPromptProgress](protocol.MethodRunPrompt, ScopeProjectWorkspace, protocol.MethodRunPromptProgress),
	subscription[serverapi.TranscriptSubscribeRequest, protocol.SessionTranscriptEventParams](protocol.MethodSessionSubscribeTranscript, AuthServer, ScopeAttachedSession, protocol.MethodSessionTranscriptEvent, protocol.MethodSessionTranscriptComplete),
	subscription[serverapi.QuestionHistorySubscribeRequest, protocol.SessionQuestionHistoryEventParams](protocol.MethodSessionQuestionHistorySubscribe, AuthServer, ScopeAttachedSession, protocol.MethodSessionQuestionHistoryEvent, protocol.MethodSessionQuestionHistoryComplete),
	subscription[serverapi.AttentionNotificationSubscribeRequest, protocol.AttentionNotificationEventParams](protocol.MethodAttentionNotificationSubscribe, AuthServer, ScopeNone, protocol.MethodAttentionNotificationEvent, protocol.MethodAttentionNotificationComplete),
	subscription[serverapi.AttentionSessionNotificationSubscribeRequest, protocol.AttentionNotificationEventParams](protocol.MethodAttentionSessionNotificationSubscribe, AuthServer, ScopeAttachedSession, protocol.MethodAttentionSessionNotificationEvent, protocol.MethodAttentionSessionNotificationComplete),
	subscription[serverapi.WorkflowSubscribeRequest, protocol.WorkflowProjectEventParams](protocol.MethodWorkflowSubscribe, AuthServer, ScopeNone, protocol.MethodWorkflowEvent, protocol.MethodWorkflowComplete),
	subscription[serverapi.WorkflowProjectSubscribeRequest, protocol.WorkflowProjectEventParams](protocol.MethodWorkflowSubscribeProject, AuthServer, ScopeProjectView, protocol.MethodWorkflowProjectEvent, protocol.MethodWorkflowProjectComplete),
	notification[serverapi.RunPromptProgress](protocol.MethodRunPromptProgress),
	notification[protocol.SessionTranscriptEventParams](protocol.MethodSessionTranscriptEvent),
	notification[protocol.StreamCompleteParams](protocol.MethodSessionTranscriptComplete),
	notification[protocol.SessionQuestionHistoryEventParams](protocol.MethodSessionQuestionHistoryEvent),
	notification[protocol.StreamCompleteParams](protocol.MethodSessionQuestionHistoryComplete),
	notification[protocol.AttentionNotificationEventParams](protocol.MethodAttentionNotificationEvent),
	notification[protocol.StreamCompleteParams](protocol.MethodAttentionNotificationComplete),
	notification[protocol.AttentionNotificationEventParams](protocol.MethodAttentionSessionNotificationEvent),
	notification[protocol.StreamCompleteParams](protocol.MethodAttentionSessionNotificationComplete),
	notification[protocol.PromptFollowUpEventParams](protocol.MethodPromptFollowUpEvent),
	notification[protocol.StreamCompleteParams](protocol.MethodPromptFollowUpComplete),
	notification[protocol.WorkflowProjectEventParams](protocol.MethodWorkflowEvent),
	notification[protocol.StreamCompleteParams](protocol.MethodWorkflowComplete),
	notification[protocol.WorkflowProjectEventParams](protocol.MethodWorkflowProjectEvent),
	notification[protocol.StreamCompleteParams](protocol.MethodWorkflowProjectComplete),
	notification[protocol.WorktreeSetupEventParams](protocol.MethodWorktreeSetupEvent),
	notification[protocol.StreamCompleteParams](protocol.MethodWorktreeSetupComplete),
}

func Routes() []Route {
	routes := append([]Route(nil), routeContracts...)
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].Method < routes[j].Method
	})
	return routes
}

func RouteByMethod(method string) (Route, bool) {
	for _, route := range routeContracts {
		if route.Method == method {
			return route, true
		}
	}
	return Route{}, false
}

func SubscriptionMethods() []string {
	methods := make([]string, 0)
	for _, route := range routeContracts {
		if route.Kind == KindSubscription {
			methods = append(methods, route.Method)
		}
	}
	sort.Strings(methods)
	return methods
}
