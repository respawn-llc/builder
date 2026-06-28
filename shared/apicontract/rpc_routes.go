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
	ScopeNone                      ScopePolicy = "none"
	ScopeAttachProject             ScopePolicy = "attach_project"
	ScopeAttachSession             ScopePolicy = "attach_session"
	ScopeProjectView               ScopePolicy = "project_view"
	ScopeProjectWorkspace          ScopePolicy = "project_workspace"
	ScopeSessionActiveProject      ScopePolicy = "session_active_project"
	ScopeSessionActiveProjectIfSet ScopePolicy = "session_active_project_if_set"
	ScopeSessionAttachedProject    ScopePolicy = "session_attached_project"
	ScopeAttachedSession           ScopePolicy = "attached_session"
	ScopeGoalSession               ScopePolicy = "goal_session"
	ScopeProcessActiveProject      ScopePolicy = "process_active_project"
	ScopeProcessListActiveProject  ScopePolicy = "process_list_active_project"
	ScopeNotification              ScopePolicy = "notification"
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

type Dependency string

const (
	DependencyProtocol           Dependency = "protocol"
	DependencyServerStatus       Dependency = "server_status"
	DependencyAuthBootstrap      Dependency = "auth_bootstrap"
	DependencyAuthStatus         Dependency = "auth_status"
	DependencyProjectView        Dependency = "project_view"
	DependencySessionLaunch      Dependency = "session_launch"
	DependencySessionView        Dependency = "session_view"
	DependencySessionLifecycle   Dependency = "session_lifecycle"
	DependencySessionRuntime     Dependency = "session_runtime"
	DependencyWorktree           Dependency = "worktree"
	DependencyRuntimeControl     Dependency = "runtime_control"
	DependencyProcessView        Dependency = "process_view"
	DependencyProcessControl     Dependency = "process_control"
	DependencyProcessOutput      Dependency = "process_output"
	DependencyAskView            Dependency = "ask_view"
	DependencyApprovalView       Dependency = "approval_view"
	DependencyPromptControl      Dependency = "prompt_control"
	DependencyPromptActivity     Dependency = "prompt_activity"
	DependencySessionActivity    Dependency = "session_activity"
	DependencyRunPrompt          Dependency = "run_prompt"
	DependencyStreamNotification Dependency = "stream_notification"
	DependencyWorkflow           Dependency = "workflow"
)

type Route struct {
	Method             string
	Kind               Kind
	Auth               AuthPolicy
	Scope              ScopePolicy
	Connection         ConnectionStrategy
	Dependency         Dependency
	RequestType        reflect.Type
	ResponseType       reflect.Type
	EventMethod        string
	EventType          reflect.Type
	CompleteMethod     string
	CompleteType       reflect.Type
	DedicatedRequestID string
	ValidatesRequest   bool
}

func unary[Req any, Resp any](method string, auth AuthPolicy, scope ScopePolicy, connection ConnectionStrategy, dependency Dependency) Route {
	reqType := reflect.TypeOf((*Req)(nil)).Elem()
	return Route{
		Method:           method,
		Kind:             KindUnary,
		Auth:             auth,
		Scope:            scope,
		Connection:       connection,
		Dependency:       dependency,
		RequestType:      reqType,
		ResponseType:     reflect.TypeOf((*Resp)(nil)).Elem(),
		ValidatesRequest: implementsValidator(reqType),
	}
}

func dedicatedUnary[Req any, Resp any](method string, requestID string, scope ScopePolicy, dependency Dependency) Route {
	route := unary[Req, Resp](method, AuthServer, scope, ConnectionDedicated, dependency)
	route.DedicatedRequestID = requestID
	return route
}

func subscription[Req any, Event any](method string, auth AuthPolicy, scope ScopePolicy, dependency Dependency, eventMethod string, completeMethod string) Route {
	reqType := reflect.TypeOf((*Req)(nil)).Elem()
	return Route{
		Method:           method,
		Kind:             KindSubscription,
		Auth:             auth,
		Scope:            scope,
		Connection:       ConnectionSubscription,
		Dependency:       dependency,
		RequestType:      reqType,
		ResponseType:     reflect.TypeOf((*protocol.SubscribeResponse)(nil)).Elem(),
		EventMethod:      eventMethod,
		EventType:        reflect.TypeOf((*Event)(nil)).Elem(),
		CompleteMethod:   completeMethod,
		CompleteType:     reflect.TypeOf((*protocol.StreamCompleteParams)(nil)).Elem(),
		ValidatesRequest: implementsValidator(reqType),
	}
}

func progress[Req any, Resp any, Event any](method string, scope ScopePolicy, dependency Dependency, eventMethod string) Route {
	reqType := reflect.TypeOf((*Req)(nil)).Elem()
	return Route{
		Method:           method,
		Kind:             KindProgress,
		Auth:             AuthServer,
		Scope:            scope,
		Connection:       ConnectionProgress,
		Dependency:       dependency,
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
		Dependency:  DependencyStreamNotification,
		RequestType: reflect.TypeOf((*Event)(nil)).Elem(),
	}
}

func implementsValidator(t reflect.Type) bool {
	validator := reflect.TypeOf((*interface{ Validate() error })(nil)).Elem()
	return t != nil && t.Implements(validator)
}

var routeContracts = []Route{
	unary[protocol.HandshakeRequest, protocol.HandshakeResponse](protocol.MethodHandshake, AuthNone, ScopeNone, ConnectionControl, DependencyProtocol),
	unary[serverapi.ServerReadinessRequest, serverapi.ServerReadinessResponse](protocol.MethodServerReadinessGet, AuthPreServerAuth, ScopeNone, ConnectionUnscoped, DependencyServerStatus),
	unary[serverapi.AuthGetBootstrapStatusRequest, serverapi.AuthGetBootstrapStatusResponse](protocol.MethodAuthGetBootstrapStatus, AuthPreServerAuth, ScopeNone, ConnectionUnscoped, DependencyAuthBootstrap),
	unary[serverapi.AuthCompleteBootstrapRequest, serverapi.AuthCompleteBootstrapResponse](protocol.MethodAuthCompleteBootstrap, AuthPreServerAuth, ScopeNone, ConnectionUnscoped, DependencyAuthBootstrap),
	unary[serverapi.AuthStatusRequest, serverapi.AuthStatusResponse](protocol.MethodAuthGetStatus, AuthPreServerAuth, ScopeNone, ConnectionUnscoped, DependencyAuthStatus),
	unary[protocol.AttachProjectRequest, protocol.AttachResponse](protocol.MethodAttachProject, AuthPreServerAuth, ScopeAttachProject, ConnectionUnscoped, DependencyProtocol),
	unary[protocol.AttachSessionRequest, protocol.AttachResponse](protocol.MethodAttachSession, AuthPreServerAuth, ScopeAttachSession, ConnectionUnscoped, DependencyProtocol),
	unary[serverapi.ProjectListRequest, serverapi.ProjectListResponse](protocol.MethodProjectList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.ProjectHomeListRequest, serverapi.ProjectHomeListResponse](protocol.MethodProjectHomeList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.ProjectResolvePathRequest, serverapi.ProjectResolvePathResponse](protocol.MethodProjectResolvePath, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.ProjectBindingPlanRequest, serverapi.ProjectBindingPlanResponse](protocol.MethodProjectPlanWorkspaceBinding, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.ProjectCreateRequest, serverapi.ProjectCreateResponse](protocol.MethodProjectCreate, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.ProjectEditGetRequest, serverapi.ProjectEditGetResponse](protocol.MethodProjectEditGet, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.ProjectUpdateRequest, serverapi.ProjectUpdateResponse](protocol.MethodProjectUpdate, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.ProjectDefaultWorkspaceSetRequest, serverapi.ProjectDefaultWorkspaceSetResponse](protocol.MethodProjectSetDefaultWorkspace, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.ProjectWorkspaceListRequest, serverapi.ProjectWorkspaceListResponse](protocol.MethodProjectWorkspaceList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.ProjectWorkspaceUnlinkRequest, serverapi.ProjectWorkspaceUnlinkResponse](protocol.MethodProjectUnlinkWorkspace, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.ProjectDeleteRequest, serverapi.ProjectDeleteResponse](protocol.MethodProjectDelete, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.ProjectAttachWorkspaceRequest, serverapi.ProjectAttachWorkspaceResponse](protocol.MethodProjectAttachWorkspace, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.ProjectRebindWorkspaceRequest, serverapi.ProjectRebindWorkspaceResponse](protocol.MethodProjectRebindWorkspace, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.ProjectGetOverviewRequest, serverapi.ProjectGetOverviewResponse](protocol.MethodProjectGetOverview, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.SessionListByProjectRequest, serverapi.SessionListByProjectResponse](protocol.MethodSessionListByProject, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyProjectView),
	unary[serverapi.WorkflowCreateRequest, serverapi.WorkflowCreateResponse](protocol.MethodWorkflowCreate, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowCreateAndLinkProjectRequest, serverapi.WorkflowCreateAndLinkProjectResponse](protocol.MethodWorkflowCreateAndLinkProject, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowUpdateRequest, serverapi.WorkflowGetResponse](protocol.MethodWorkflowUpdate, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowListRequest, serverapi.WorkflowListResponse](protocol.MethodWorkflowList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowGetRequest, serverapi.WorkflowGetResponse](protocol.MethodWorkflowGet, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowNodeGroupAddRequest, serverapi.WorkflowNodeGroupResponse](protocol.MethodWorkflowNodeGroupAdd, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowNodeGroupUpdateRequest, serverapi.WorkflowNodeGroupResponse](protocol.MethodWorkflowNodeGroupUpdate, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowNodeGroupDeleteRequest, struct{}](protocol.MethodWorkflowNodeGroupDelete, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowNodeAddRequest, serverapi.WorkflowNodeAddResponse](protocol.MethodWorkflowAddNode, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowNodeUpdateRequest, serverapi.WorkflowNodeUpdateResponse](protocol.MethodWorkflowUpdateNode, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTransitionGroupAddRequest, serverapi.WorkflowTransitionGroupAddResponse](protocol.MethodWorkflowAddTransitionGroup, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTransitionGroupUpdateRequest, serverapi.WorkflowTransitionGroupUpdateResponse](protocol.MethodWorkflowUpdateTransitionGroup, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowEdgeAddRequest, serverapi.WorkflowEdgeAddResponse](protocol.MethodWorkflowAddEdge, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowEdgeUpdateRequest, serverapi.WorkflowEdgeUpdateResponse](protocol.MethodWorkflowUpdateEdge, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowLinkProjectRequest, serverapi.WorkflowLinkProjectResponse](protocol.MethodWorkflowLinkProject, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowListProjectLinksRequest, serverapi.WorkflowListProjectLinksResponse](protocol.MethodWorkflowListProjectLinks, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowSetDefaultProjectLinkRequest, serverapi.WorkflowSetDefaultProjectLinkResponse](protocol.MethodWorkflowSetDefaultProjectLink, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowUnlinkProjectRequest, serverapi.WorkflowUnlinkProjectResponse](protocol.MethodWorkflowUnlinkProject, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowDeletePreviewRequest, serverapi.WorkflowDeletePreviewResponse](protocol.MethodWorkflowDeletePreview, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowDeleteRequest, serverapi.WorkflowDeleteResponse](protocol.MethodWorkflowDelete, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowValidateRequest, serverapi.WorkflowValidateResponse](protocol.MethodWorkflowValidate, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowGraphValidateDraftRequest, serverapi.WorkflowGraphValidateDraftResponse](protocol.MethodWorkflowGraphValidateDraft, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowGraphDeriveWiringRequest, serverapi.WorkflowGraphDeriveWiringResponse](protocol.MethodWorkflowGraphDeriveWiring, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowGraphSavePreviewRequest, serverapi.WorkflowGraphSavePreviewResponse](protocol.MethodWorkflowGraphSavePreview, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowGraphSaveRequest, serverapi.WorkflowGraphSaveResponse](protocol.MethodWorkflowGraphSave, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskCreateRequest, serverapi.WorkflowTaskCreateResponse](protocol.MethodWorkflowTaskCreate, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskUpdateRequest, serverapi.WorkflowTaskUpdateResponse](protocol.MethodWorkflowTaskUpdate, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskStartRequest, serverapi.WorkflowTaskStartResponse](protocol.MethodWorkflowTaskStart, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskInterruptRequest, serverapi.WorkflowTaskInterruptResponse](protocol.MethodWorkflowTaskInterrupt, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskResumeRequest, serverapi.WorkflowTaskResumeResponse](protocol.MethodWorkflowTaskResume, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskApproveRequest, serverapi.WorkflowTaskApproveResponse](protocol.MethodWorkflowTaskApprove, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskMoveRequest, serverapi.WorkflowTaskMoveResponse](protocol.MethodWorkflowTaskMove, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskCompleteRequest, serverapi.WorkflowTaskCompleteResponse](protocol.MethodWorkflowTaskComplete, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskCancelRequest, struct{}](protocol.MethodWorkflowTaskCancel, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskDeleteRequest, struct{}](protocol.MethodWorkflowTaskDelete, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowAttentionListRequest, serverapi.WorkflowAttentionListResponse](protocol.MethodWorkflowAttentionList, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskAttentionListRequest, serverapi.WorkflowTaskAttentionListResponse](protocol.MethodWorkflowTaskAttentionList, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskQuestionAnswerRequest, struct{}](protocol.MethodWorkflowTaskQuestionAnswer, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskCommentAddRequest, serverapi.WorkflowTaskCommentAddResponse](protocol.MethodWorkflowTaskCommentAdd, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskCommentListRequest, serverapi.WorkflowTaskCommentListResponse](protocol.MethodWorkflowTaskCommentList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskCommentReplaceRequest, struct{}](protocol.MethodWorkflowTaskCommentReplace, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskCommentDeleteRequest, struct{}](protocol.MethodWorkflowTaskCommentDelete, AuthServer, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskActivityListRequest, serverapi.WorkflowTaskActivityListResponse](protocol.MethodWorkflowTaskActivityList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskListRequest, serverapi.WorkflowTaskListResponse](protocol.MethodWorkflowTaskList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowBoardRequest, serverapi.WorkflowBoardResponse](protocol.MethodWorkflowBoardGet, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowBoardNodeCardsListRequest, serverapi.WorkflowBoardNodeCardsListResponse](protocol.MethodWorkflowBoardNodeCardsList, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.WorkflowTaskGetRequest, serverapi.WorkflowTaskGetResponse](protocol.MethodWorkflowTaskGet, AuthPreServerAuth, ScopeProjectView, ConnectionUnscoped, DependencyWorkflow),
	unary[serverapi.SessionPlanRequest, serverapi.SessionPlanResponse](protocol.MethodSessionPlan, AuthServer, ScopeProjectWorkspace, ConnectionControl, DependencySessionLaunch),
	unary[serverapi.SessionMainViewRequest, serverapi.SessionMainViewResponse](protocol.MethodSessionGetMainView, AuthPreServerAuth, ScopeSessionActiveProject, ConnectionControl, DependencySessionView),
	unary[serverapi.SessionTranscriptPageRequest, serverapi.SessionTranscriptPageResponse](protocol.MethodSessionGetTranscriptPage, AuthPreServerAuth, ScopeSessionActiveProject, ConnectionControl, DependencySessionView),
	unary[serverapi.SessionCommittedTranscriptSuffixRequest, serverapi.SessionCommittedTranscriptSuffixResponse](protocol.MethodSessionGetCommittedTranscriptSuffix, AuthPreServerAuth, ScopeSessionActiveProject, ConnectionControl, DependencySessionView),
	unary[serverapi.SessionInitialInputRequest, serverapi.SessionInitialInputResponse](protocol.MethodSessionGetInitialInput, AuthPreServerAuth, ScopeSessionActiveProjectIfSet, ConnectionControl, DependencySessionLifecycle),
	unary[serverapi.SessionPersistInputDraftRequest, serverapi.SessionPersistInputDraftResponse](protocol.MethodSessionPersistInputDraft, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencySessionLifecycle),
	unary[serverapi.SessionRetargetWorkspaceRequest, serverapi.SessionRetargetWorkspaceResponse](protocol.MethodSessionRetargetWorkspace, AuthServer, ScopeSessionAttachedProject, ConnectionUnscoped, DependencySessionLifecycle),
	unary[serverapi.SessionResolveTransitionRequest, serverapi.SessionResolveTransitionResponse](protocol.MethodSessionResolveTransition, AuthServer, ScopeSessionActiveProjectIfSet, ConnectionControl, DependencySessionLifecycle),
	unary[serverapi.SessionRuntimeActivateRequest, serverapi.SessionRuntimeActivateResponse](protocol.MethodSessionRuntimeActivate, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencySessionRuntime),
	unary[serverapi.SessionRuntimeReleaseRequest, serverapi.SessionRuntimeReleaseResponse](protocol.MethodSessionRuntimeRelease, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencySessionRuntime),
	unary[serverapi.WorktreeListRequest, serverapi.WorktreeListResponse](protocol.MethodWorktreeList, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyWorktree),
	unary[serverapi.WorktreeCreateTargetResolveRequest, serverapi.WorktreeCreateTargetResolveResponse](protocol.MethodWorktreeCreateTargetResolve, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyWorktree),
	unary[serverapi.WorktreeCreateRequest, serverapi.WorktreeCreateResponse](protocol.MethodWorktreeCreate, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyWorktree),
	unary[serverapi.WorktreeSwitchRequest, serverapi.WorktreeSwitchResponse](protocol.MethodWorktreeSwitch, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyWorktree),
	unary[serverapi.WorktreeDeleteRequest, serverapi.WorktreeDeleteResponse](protocol.MethodWorktreeDelete, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyWorktree),
	unary[serverapi.RuntimeSetSessionNameRequest, struct{}](protocol.MethodRuntimeSetSessionName, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeSetThinkingLevelRequest, struct{}](protocol.MethodRuntimeSetThinkingLevel, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeSetFastModeEnabledRequest, serverapi.RuntimeSetFastModeEnabledResponse](protocol.MethodRuntimeSetFastModeEnabled, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeSetReviewerEnabledRequest, serverapi.RuntimeSetReviewerEnabledResponse](protocol.MethodRuntimeSetReviewerEnabled, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeSetAutoCompactionEnabledRequest, serverapi.RuntimeSetAutoCompactionEnabledResponse](protocol.MethodRuntimeSetAutoCompactionEnabled, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeSetQuestionsEnabledRequest, serverapi.RuntimeSetQuestionsEnabledResponse](protocol.MethodRuntimeSetQuestionsEnabled, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeAppendCommittedEntryRequest, struct{}](protocol.MethodRuntimeAppendCommittedEntry, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeShouldCompactBeforeUserMessageRequest, serverapi.RuntimeShouldCompactBeforeUserMessageResponse](protocol.MethodRuntimeShouldCompactBeforeUserMessage, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyRuntimeControl),
	dedicatedUnary[serverapi.RuntimeSubmitUserTurnRequest, serverapi.RuntimeSubmitUserTurnResponse](protocol.MethodRuntimeSubmitUserTurn, "runtime-submit-user-turn", ScopeSessionActiveProject, DependencyRuntimeControl),
	dedicatedUnary[serverapi.RuntimeSubmitUserShellCommandRequest, struct{}](protocol.MethodRuntimeSubmitUserShellCommand, "runtime-submit-user-shell-command", ScopeSessionActiveProject, DependencyRuntimeControl),
	dedicatedUnary[serverapi.RuntimeCompactContextRequest, struct{}](protocol.MethodRuntimeCompactContext, "runtime-compact-context", ScopeSessionActiveProject, DependencyRuntimeControl),
	dedicatedUnary[serverapi.RuntimeCompactContextForPreSubmitRequest, struct{}](protocol.MethodRuntimeCompactContextForPreSubmit, "runtime-compact-context-pre-submit", ScopeSessionActiveProject, DependencyRuntimeControl),
	unary[serverapi.RuntimeHasQueuedUserWorkRequest, serverapi.RuntimeHasQueuedUserWorkResponse](protocol.MethodRuntimeHasQueuedUserWork, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyRuntimeControl),
	dedicatedUnary[serverapi.RuntimeSubmitQueuedUserMessagesRequest, serverapi.RuntimeSubmitQueuedUserMessagesResponse](protocol.MethodRuntimeSubmitQueuedUserMessages, "runtime-submit-queued-user-messages", ScopeSessionActiveProject, DependencyRuntimeControl),
	dedicatedUnary[serverapi.RuntimeInterruptRequest, struct{}](protocol.MethodRuntimeInterrupt, "runtime-interrupt", ScopeSessionActiveProject, DependencyRuntimeControl),
	unary[serverapi.RuntimeQueueUserMessageRequest, serverapi.RuntimeQueueUserMessageResponse](protocol.MethodRuntimeQueueUserMessage, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeDiscardQueuedUserMessageRequest, serverapi.RuntimeDiscardQueuedUserMessageResponse](protocol.MethodRuntimeDiscardQueuedUserMessage, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeRecordPromptHistoryRequest, struct{}](protocol.MethodRuntimeRecordPromptHistory, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeGoalShowRequest, serverapi.RuntimeGoalShowResponse](protocol.MethodRuntimeGoalShow, AuthServer, ScopeGoalSession, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeGoalSetRequest, serverapi.RuntimeGoalShowResponse](protocol.MethodRuntimeGoalSet, AuthServer, ScopeGoalSession, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](protocol.MethodRuntimeGoalPause, AuthServer, ScopeGoalSession, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](protocol.MethodRuntimeGoalResume, AuthServer, ScopeGoalSession, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeGoalStatusRequest, serverapi.RuntimeGoalShowResponse](protocol.MethodRuntimeGoalComplete, AuthServer, ScopeGoalSession, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.RuntimeGoalClearRequest, serverapi.RuntimeGoalShowResponse](protocol.MethodRuntimeGoalClear, AuthServer, ScopeGoalSession, ConnectionControl, DependencyRuntimeControl),
	unary[serverapi.ProcessListRequest, serverapi.ProcessListResponse](protocol.MethodProcessList, AuthPreServerAuth, ScopeProcessListActiveProject, ConnectionControl, DependencyProcessView),
	unary[serverapi.ProcessGetRequest, serverapi.ProcessGetResponse](protocol.MethodProcessGet, AuthPreServerAuth, ScopeProcessActiveProject, ConnectionControl, DependencyProcessView),
	unary[serverapi.ProcessKillRequest, serverapi.ProcessKillResponse](protocol.MethodProcessKill, AuthServer, ScopeProcessActiveProject, ConnectionControl, DependencyProcessControl),
	unary[serverapi.ProcessInlineOutputRequest, serverapi.ProcessInlineOutputResponse](protocol.MethodProcessInlineOutput, AuthServer, ScopeProcessActiveProject, ConnectionControl, DependencyProcessControl),
	unary[serverapi.AskListPendingBySessionRequest, serverapi.AskListPendingBySessionResponse](protocol.MethodAskListPending, AuthPreServerAuth, ScopeSessionActiveProject, ConnectionControl, DependencyAskView),
	unary[serverapi.AskAnswerRequest, struct{}](protocol.MethodAskAnswer, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyPromptControl),
	unary[serverapi.ApprovalListPendingBySessionRequest, serverapi.ApprovalListPendingBySessionResponse](protocol.MethodApprovalListPending, AuthPreServerAuth, ScopeSessionActiveProject, ConnectionControl, DependencyApprovalView),
	unary[serverapi.ApprovalAnswerRequest, struct{}](protocol.MethodApprovalAnswer, AuthServer, ScopeSessionActiveProject, ConnectionControl, DependencyPromptControl),
	progress[serverapi.RunPromptRequest, serverapi.RunPromptResponse, serverapi.RunPromptProgress](protocol.MethodRunPrompt, ScopeProjectWorkspace, DependencyRunPrompt, protocol.MethodRunPromptProgress),
	subscription[serverapi.SessionActivitySubscribeRequest, protocol.SessionActivityEventParams](protocol.MethodSessionSubscribeActivity, AuthServer, ScopeAttachedSession, DependencySessionActivity, protocol.MethodSessionActivityEvent, protocol.MethodSessionActivityComplete),
	subscription[serverapi.ProcessOutputSubscribeRequest, protocol.ProcessOutputEventParams](protocol.MethodProcessSubscribeOutput, AuthServer, ScopeProcessActiveProject, DependencyProcessOutput, protocol.MethodProcessOutputEvent, protocol.MethodProcessOutputComplete),
	subscription[serverapi.PromptActivitySubscribeRequest, protocol.PromptActivityEventParams](protocol.MethodPromptSubscribeActivity, AuthServer, ScopeAttachedSession, DependencyPromptActivity, protocol.MethodPromptActivityEvent, protocol.MethodPromptActivityComplete),
	subscription[serverapi.WorkflowSubscribeRequest, protocol.WorkflowProjectEventParams](protocol.MethodWorkflowSubscribe, AuthServer, ScopeNone, DependencyWorkflow, protocol.MethodWorkflowEvent, protocol.MethodWorkflowComplete),
	subscription[serverapi.WorkflowProjectSubscribeRequest, protocol.WorkflowProjectEventParams](protocol.MethodWorkflowSubscribeProject, AuthServer, ScopeProjectView, DependencyWorkflow, protocol.MethodWorkflowProjectEvent, protocol.MethodWorkflowProjectComplete),
	notification[serverapi.RunPromptProgress](protocol.MethodRunPromptProgress),
	notification[protocol.SessionActivityEventParams](protocol.MethodSessionActivityEvent),
	notification[protocol.StreamCompleteParams](protocol.MethodSessionActivityComplete),
	notification[protocol.ProcessOutputEventParams](protocol.MethodProcessOutputEvent),
	notification[protocol.StreamCompleteParams](protocol.MethodProcessOutputComplete),
	notification[protocol.PromptActivityEventParams](protocol.MethodPromptActivityEvent),
	notification[protocol.StreamCompleteParams](protocol.MethodPromptActivityComplete),
	notification[protocol.WorkflowProjectEventParams](protocol.MethodWorkflowEvent),
	notification[protocol.StreamCompleteParams](protocol.MethodWorkflowComplete),
	notification[protocol.WorkflowProjectEventParams](protocol.MethodWorkflowProjectEvent),
	notification[protocol.StreamCompleteParams](protocol.MethodWorkflowProjectComplete),
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

func AllowedPreAuthMethods() []string {
	methods := make([]string, 0)
	for _, route := range routeContracts {
		if route.Auth == AuthPreServerAuth {
			methods = append(methods, route.Method)
		}
	}
	sort.Strings(methods)
	return methods
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
