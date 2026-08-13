package apicontract

import (
	"context"

	"core/shared/serverapi"
)

// Interfaces in this package are in-process bindings for shared RPC routes.
// They intentionally describe method shapes only: no runtime handles,
// lifecycle orchestration, logging, timeout, or close policy belongs here.
type ApprovalViewService interface {
	ListPendingApprovalsBySession(ctx context.Context, req serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error)
}

type ApprovalViewTrustedService interface {
	ListPendingApprovalsBySessionValidated(ctx context.Context, req Validated[serverapi.ApprovalListPendingBySessionRequest]) (serverapi.ApprovalListPendingBySessionResponse, error)
}

type AskViewService interface {
	ListPendingAsksBySession(ctx context.Context, req serverapi.AskListPendingBySessionRequest) (serverapi.AskListPendingBySessionResponse, error)
}

type AskViewTrustedService interface {
	ListPendingAsksBySessionValidated(ctx context.Context, req Validated[serverapi.AskListPendingBySessionRequest]) (serverapi.AskListPendingBySessionResponse, error)
}

type AuthBootstrapService interface {
	GetAuthBootstrapStatus(ctx context.Context, req serverapi.AuthGetBootstrapStatusRequest) (serverapi.AuthGetBootstrapStatusResponse, error)
	CompleteAuthBootstrap(ctx context.Context, req serverapi.AuthCompleteBootstrapRequest) (serverapi.AuthCompleteBootstrapResponse, error)
	AcknowledgeNoAuth(ctx context.Context, req serverapi.AuthAcknowledgeNoAuthRequest) (serverapi.AuthAcknowledgeNoAuthResponse, error)
}

type AuthBootstrapTrustedService interface {
	GetAuthBootstrapStatusValidated(ctx context.Context, req Validated[serverapi.AuthGetBootstrapStatusRequest]) (serverapi.AuthGetBootstrapStatusResponse, error)
	CompleteAuthBootstrapValidated(ctx context.Context, req Validated[serverapi.AuthCompleteBootstrapRequest]) (serverapi.AuthCompleteBootstrapResponse, error)
	AcknowledgeNoAuthValidated(ctx context.Context, req Validated[serverapi.AuthAcknowledgeNoAuthRequest]) (serverapi.AuthAcknowledgeNoAuthResponse, error)
}

type AuthStatusService interface {
	GetAuthStatus(ctx context.Context, req serverapi.AuthStatusRequest) (serverapi.AuthStatusResponse, error)
}

type AuthStatusTrustedService interface {
	GetAuthStatusValidated(ctx context.Context, req Validated[serverapi.AuthStatusRequest]) (serverapi.AuthStatusResponse, error)
}

type CapabilityFactsService interface {
	GetCapabilityFacts(ctx context.Context, req serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error)
}

type CapabilityFactsTrustedService interface {
	GetCapabilityFactsValidated(ctx context.Context, req Validated[serverapi.CapabilityFactsRequest]) (serverapi.CapabilityFactsResponse, error)
}

type PromptCommandCatalogService interface {
	GetPromptCommandCatalog(ctx context.Context, req serverapi.PromptCommandCatalogRequest) (serverapi.PromptCommandCatalogResponse, error)
}

type PromptCommandCatalogTrustedService interface {
	GetPromptCommandCatalogValidated(ctx context.Context, req Validated[serverapi.PromptCommandCatalogRequest]) (serverapi.PromptCommandCatalogResponse, error)
}

type OnboardingFinalizeService interface {
	FinalizeOnboarding(ctx context.Context, req serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error)
}

type OnboardingFinalizeTrustedService interface {
	FinalizeOnboardingValidated(ctx context.Context, req Validated[serverapi.OnboardingFinalizeRequest]) (serverapi.OnboardingFinalizeResponse, error)
}

type ProcessControlService interface {
	KillProcess(ctx context.Context, req serverapi.ProcessKillRequest) (serverapi.ProcessKillResponse, error)
	GetInlineOutput(ctx context.Context, req serverapi.ProcessInlineOutputRequest) (serverapi.ProcessInlineOutputResponse, error)
}

type ProcessControlTrustedService interface {
	KillProcessValidated(ctx context.Context, req Validated[serverapi.ProcessKillRequest], authorization AuthorizedProcessInActiveProject) (serverapi.ProcessKillResponse, error)
	GetInlineOutputValidated(ctx context.Context, req Validated[serverapi.ProcessInlineOutputRequest], authorization AuthorizedProcessInActiveProject) (serverapi.ProcessInlineOutputResponse, error)
}

type ProcessViewService interface {
	ListProcesses(ctx context.Context, req serverapi.ProcessListRequest) (serverapi.ProcessListResponse, error)
	GetProcess(ctx context.Context, req serverapi.ProcessGetRequest) (serverapi.ProcessGetResponse, error)
}

type ProcessViewTrustedService interface {
	ResolveProcessAuthorization(ctx context.Context, processID string) (ProcessAuthorizationCandidate, error)
	GetProcessValidated(ctx context.Context, req Validated[serverapi.ProcessGetRequest], authorization AuthorizedProcessInActiveProject) (serverapi.ProcessGetResponse, error)
}

type ProjectViewService interface {
	ListProjects(ctx context.Context, req serverapi.ProjectListRequest) (serverapi.ProjectListResponse, error)
	ListProjectHome(ctx context.Context, req serverapi.ProjectHomeListRequest) (serverapi.ProjectHomeListResponse, error)
	ResolveProjectPath(ctx context.Context, req serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error)
	PlanWorkspaceBinding(ctx context.Context, req serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error)
	CreateProject(ctx context.Context, req serverapi.ProjectCreateRequest) (serverapi.ProjectCreateResponse, error)
	GetProjectEdit(ctx context.Context, req serverapi.ProjectEditGetRequest) (serverapi.ProjectEditGetResponse, error)
	UpdateProject(ctx context.Context, req serverapi.ProjectUpdateRequest) (serverapi.ProjectUpdateResponse, error)
	SetDefaultWorkspace(ctx context.Context, req serverapi.ProjectDefaultWorkspaceSetRequest) (serverapi.ProjectDefaultWorkspaceSetResponse, error)
	ListProjectWorkspaces(ctx context.Context, req serverapi.ProjectWorkspaceListRequest) (serverapi.ProjectWorkspaceListResponse, error)
	GetProjectWorkspace(ctx context.Context, req serverapi.ProjectWorkspaceGetRequest) (serverapi.ProjectWorkspaceGetResponse, error)
	UnlinkWorkspaceFromProject(ctx context.Context, req serverapi.ProjectWorkspaceUnlinkRequest) (serverapi.ProjectWorkspaceUnlinkResponse, error)
	DeleteProject(ctx context.Context, req serverapi.ProjectDeleteRequest) (serverapi.ProjectDeleteResponse, error)
	AttachWorkspaceToProject(ctx context.Context, req serverapi.ProjectAttachWorkspaceRequest) (serverapi.ProjectAttachWorkspaceResponse, error)
	RebindWorkspace(ctx context.Context, req serverapi.ProjectRebindWorkspaceRequest) (serverapi.ProjectRebindWorkspaceResponse, error)
	GetProjectOverview(ctx context.Context, req serverapi.ProjectGetOverviewRequest) (serverapi.ProjectGetOverviewResponse, error)
	ListSessionPage(ctx context.Context, req serverapi.SessionPageRequest) (serverapi.SessionPageResponse, error)
}

type ProjectViewTrustedService interface {
	ListProjectsValidated(ctx context.Context, req Validated[serverapi.ProjectListRequest]) (serverapi.ProjectListResponse, error)
	ListProjectHomeValidated(ctx context.Context, req Validated[serverapi.ProjectHomeListRequest]) (serverapi.ProjectHomeListResponse, error)
	ResolveProjectPathValidated(ctx context.Context, req Validated[serverapi.ProjectResolvePathRequest]) (serverapi.ProjectResolvePathResponse, error)
	PlanWorkspaceBindingValidated(ctx context.Context, req Validated[serverapi.ProjectBindingPlanRequest]) (serverapi.ProjectBindingPlanResponse, error)
	CreateProjectValidated(ctx context.Context, req Validated[serverapi.ProjectCreateRequest]) (serverapi.ProjectCreateResponse, error)
	GetProjectEditValidated(ctx context.Context, req Validated[serverapi.ProjectEditGetRequest]) (serverapi.ProjectEditGetResponse, error)
	UpdateProjectValidated(ctx context.Context, req Validated[serverapi.ProjectUpdateRequest]) (serverapi.ProjectUpdateResponse, error)
	SetDefaultWorkspaceValidated(ctx context.Context, req Validated[serverapi.ProjectDefaultWorkspaceSetRequest]) (serverapi.ProjectDefaultWorkspaceSetResponse, error)
	ListProjectWorkspacesValidated(ctx context.Context, req Validated[serverapi.ProjectWorkspaceListRequest]) (serverapi.ProjectWorkspaceListResponse, error)
	GetProjectWorkspaceValidated(ctx context.Context, req Validated[serverapi.ProjectWorkspaceGetRequest]) (serverapi.ProjectWorkspaceGetResponse, error)
	UnlinkWorkspaceFromProjectValidated(ctx context.Context, req Validated[serverapi.ProjectWorkspaceUnlinkRequest]) (serverapi.ProjectWorkspaceUnlinkResponse, error)
	DeleteProjectValidated(ctx context.Context, req Validated[serverapi.ProjectDeleteRequest]) (serverapi.ProjectDeleteResponse, error)
	AttachWorkspaceToProjectValidated(ctx context.Context, req Validated[serverapi.ProjectAttachWorkspaceRequest]) (serverapi.ProjectAttachWorkspaceResponse, error)
	RebindWorkspaceValidated(ctx context.Context, req Validated[serverapi.ProjectRebindWorkspaceRequest]) (serverapi.ProjectRebindWorkspaceResponse, error)
	GetProjectOverviewValidated(ctx context.Context, req Validated[serverapi.ProjectGetOverviewRequest]) (serverapi.ProjectGetOverviewResponse, error)
	ListSessionPageValidated(ctx context.Context, req Validated[serverapi.SessionPageRequest]) (serverapi.SessionPageResponse, error)
}

type AttentionNotificationService interface {
	SubscribeAttentionNotifications(ctx context.Context, req serverapi.AttentionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error)
	SubscribeSessionAttentionNotifications(ctx context.Context, req serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error)
}

type AttentionNotificationTrustedService interface {
	SubscribeAttentionNotificationsValidated(ctx context.Context, req Validated[serverapi.AttentionNotificationSubscribeRequest]) (serverapi.AttentionNotificationSubscription, error)
}

type PromptControlService interface {
	AnswerPromptBatch(ctx context.Context, req serverapi.PromptAnswerBatchRequest) (serverapi.PromptAnswerBatchResponse, error)
	SubscribeFollowUp(ctx context.Context, req serverapi.PromptFollowUpWatchRequest) (serverapi.PromptFollowUpSubscription, error)
}

type PromptControlTrustedService interface {
	AnswerPromptBatchValidated(ctx context.Context, req Validated[serverapi.PromptAnswerBatchRequest]) (serverapi.PromptAnswerBatchResponse, error)
	SubscribeFollowUpValidated(ctx context.Context, req Validated[serverapi.PromptFollowUpWatchRequest]) (serverapi.PromptFollowUpSubscription, error)
}

type RunPromptService interface {
	RunPrompt(ctx context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error)
}

type RunPromptTrustedService interface {
	RunPromptValidated(ctx context.Context, req Validated[serverapi.RunPromptRequest], progress serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error)
}

type ServerStatusService interface {
	GetServerReadiness(ctx context.Context, req serverapi.ServerReadinessRequest) (serverapi.ServerReadinessResponse, error)
	GetUpdateStatus(ctx context.Context, req serverapi.UpdateStatusRequest) (serverapi.UpdateStatusResponse, error)
}

type ServerStatusTrustedService interface {
	GetServerReadinessValidated(ctx context.Context, req Validated[serverapi.ServerReadinessRequest]) (serverapi.ServerReadinessResponse, error)
	GetUpdateStatusValidated(ctx context.Context, req Validated[serverapi.UpdateStatusRequest]) (serverapi.UpdateStatusResponse, error)
}

type RuntimeControlService interface {
	SetSessionName(ctx context.Context, req serverapi.RuntimeSetSessionNameRequest) error
	SetThinkingLevel(ctx context.Context, req serverapi.RuntimeSetThinkingLevelRequest) error
	SetFastModeEnabled(ctx context.Context, req serverapi.RuntimeSetFastModeEnabledRequest) (serverapi.RuntimeSetFastModeEnabledResponse, error)
	SetReviewerEnabled(ctx context.Context, req serverapi.RuntimeSetReviewerEnabledRequest) (serverapi.RuntimeSetReviewerEnabledResponse, error)
	SetAutoCompactionEnabled(ctx context.Context, req serverapi.RuntimeSetAutoCompactionEnabledRequest) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error)
	SetQuestionsEnabled(ctx context.Context, req serverapi.RuntimeSetQuestionsEnabledRequest) (serverapi.RuntimeSetQuestionsEnabledResponse, error)
	AppendCommittedEntry(ctx context.Context, req serverapi.RuntimeAppendCommittedEntryRequest) error
	ShouldCompactBeforeUserMessage(ctx context.Context, req serverapi.RuntimeShouldCompactBeforeUserMessageRequest) (serverapi.RuntimeShouldCompactBeforeUserMessageResponse, error)
	SubmitUserTurn(ctx context.Context, req serverapi.RuntimeSubmitUserTurnRequest) (serverapi.RuntimeSubmitUserTurnResponse, error)
	SubmitUserShellCommand(ctx context.Context, req serverapi.RuntimeSubmitUserShellCommandRequest) error
	CompactContext(ctx context.Context, req serverapi.RuntimeCompactContextRequest) error
	Interrupt(ctx context.Context, req serverapi.RuntimeInterruptRequest) (serverapi.RuntimeInterruptResponse, error)
	DiscardQueuedUserMessage(ctx context.Context, req serverapi.RuntimeDiscardQueuedUserMessageRequest) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error)
	RecordPromptHistory(ctx context.Context, req serverapi.RuntimeRecordPromptHistoryRequest) error
	ShowGoal(ctx context.Context, req serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error)
	SetGoal(ctx context.Context, req serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalMutationResponse, error)
	PauseGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error)
	ResumeGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error)
	CompleteGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error)
	ClearGoal(ctx context.Context, req serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalMutationResponse, error)
}

type RuntimeGoalTrustedService interface {
	ShowGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalShowRequest]) (serverapi.RuntimeGoalShowResponse, error)
	SetGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalSetRequest]) (serverapi.RuntimeGoalMutationResponse, error)
	PauseGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error)
	ResumeGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error)
	CompleteGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error)
	ClearGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalClearRequest]) (serverapi.RuntimeGoalMutationResponse, error)
}

type RuntimeControlTrustedService interface {
	SetSessionNameValidated(ctx context.Context, req Validated[serverapi.RuntimeSetSessionNameRequest], authorization AuthorizedSessionInActiveProject) error
	SetThinkingLevelValidated(ctx context.Context, req Validated[serverapi.RuntimeSetThinkingLevelRequest], authorization AuthorizedSessionInActiveProject) error
	SetFastModeEnabledValidated(ctx context.Context, req Validated[serverapi.RuntimeSetFastModeEnabledRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeSetFastModeEnabledResponse, error)
	SetReviewerEnabledValidated(ctx context.Context, req Validated[serverapi.RuntimeSetReviewerEnabledRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeSetReviewerEnabledResponse, error)
	SetAutoCompactionEnabledValidated(ctx context.Context, req Validated[serverapi.RuntimeSetAutoCompactionEnabledRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error)
	SetQuestionsEnabledValidated(ctx context.Context, req Validated[serverapi.RuntimeSetQuestionsEnabledRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeSetQuestionsEnabledResponse, error)
	AppendCommittedEntryValidated(ctx context.Context, req Validated[serverapi.RuntimeAppendCommittedEntryRequest], authorization AuthorizedSessionInActiveProject) error
	ShouldCompactBeforeUserMessageValidated(ctx context.Context, req Validated[serverapi.RuntimeShouldCompactBeforeUserMessageRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeShouldCompactBeforeUserMessageResponse, error)
	SubmitUserTurnValidated(ctx context.Context, req Validated[serverapi.RuntimeSubmitUserTurnRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeSubmitUserTurnResponse, error)
	SubmitUserShellCommandValidated(ctx context.Context, req Validated[serverapi.RuntimeSubmitUserShellCommandRequest], authorization AuthorizedSessionInActiveProject) error
	CompactContextValidated(ctx context.Context, req Validated[serverapi.RuntimeCompactContextRequest], authorization AuthorizedSessionInActiveProject) error
	InterruptValidated(ctx context.Context, req Validated[serverapi.RuntimeInterruptRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeInterruptResponse, error)
	DiscardQueuedUserMessageValidated(ctx context.Context, req Validated[serverapi.RuntimeDiscardQueuedUserMessageRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error)
	RecordPromptHistoryValidated(ctx context.Context, req Validated[serverapi.RuntimeRecordPromptHistoryRequest], authorization AuthorizedSessionInActiveProject) error
}

type RuntimeLiveControlService interface {
	LiveSteer(ctx context.Context, req serverapi.RuntimeLiveSteerRequest) (serverapi.RuntimeLiveSteerResponse, error)
	LiveStop(ctx context.Context, req serverapi.RuntimeLiveStopRequest) (serverapi.RuntimeLiveStopResponse, error)
	LiveWait(ctx context.Context, req serverapi.RuntimeLiveWaitRequest) (serverapi.RuntimeLiveWaitResponse, error)
	LiveWatch(ctx context.Context, req serverapi.RuntimeLiveWatchRequest) (serverapi.RuntimeLiveWatchResponse, error)
}

type RuntimeLiveControlTrustedService interface {
	LiveSteerValidated(ctx context.Context, req Validated[serverapi.RuntimeLiveSteerRequest], identity RuntimeLiveRequestIdentity) (serverapi.RuntimeLiveSteerResponse, error)
	LiveStopValidated(ctx context.Context, req Validated[serverapi.RuntimeLiveStopRequest], identity RuntimeLiveRequestIdentity) (serverapi.RuntimeLiveStopResponse, error)
	LiveWaitValidated(ctx context.Context, req Validated[serverapi.RuntimeLiveWaitRequest], identity RuntimeLiveRequestIdentity) (serverapi.RuntimeLiveWaitResponse, error)
	LiveWatchValidated(ctx context.Context, req Validated[serverapi.RuntimeLiveWatchRequest], identity RuntimeLiveRequestIdentity) (serverapi.RuntimeLiveWatchResponse, error)
}

type SessionTranscriptService interface {
	SubscribeSessionTranscript(ctx context.Context, req serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error)
}

type SessionLaunchService interface {
	PlanSession(ctx context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error)
	WorkspaceChatDraft(ctx context.Context, req serverapi.WorkspaceChatDraftRequest) (serverapi.WorkspaceChatDraftResponse, error)
	MaterializeWorkspaceChat(ctx context.Context, req serverapi.WorkspaceChatMaterializeRequest) (serverapi.WorkspaceChatMaterializeResponse, error)
}

type SessionLaunchTrustedService interface {
	PlanSessionValidated(ctx context.Context, req Validated[serverapi.SessionPlanRequest]) (serverapi.SessionPlanResponse, error)
	WorkspaceChatDraftValidated(ctx context.Context, req Validated[serverapi.WorkspaceChatDraftRequest]) (serverapi.WorkspaceChatDraftResponse, error)
	MaterializeWorkspaceChatValidated(ctx context.Context, req Validated[serverapi.WorkspaceChatMaterializeRequest]) (serverapi.WorkspaceChatMaterializeResponse, error)
}

type SessionLifecycleService interface {
	GetInitialInput(ctx context.Context, req serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error)
	PersistInputDraft(ctx context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error)
	RetargetSessionWorkspace(ctx context.Context, req serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error)
	ResolveTransition(ctx context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error)
}

type SessionLifecycleTrustedService interface {
	GetInitialInputValidated(ctx context.Context, req Validated[serverapi.SessionInitialInputRequest], authorization OptionalAuthorizedSessionInActiveProject) (serverapi.SessionInitialInputResponse, error)
	PersistInputDraftValidated(ctx context.Context, req Validated[serverapi.SessionPersistInputDraftRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionPersistInputDraftResponse, error)
	RetargetSessionWorkspaceValidated(
		ctx context.Context,
		req Validated[serverapi.SessionRetargetWorkspaceRequest],
		constraint AttachedProjectConstraint,
	) (serverapi.SessionRetargetWorkspaceResponse, error)
	ResolveTransitionValidated(ctx context.Context, req Validated[serverapi.SessionResolveTransitionRequest], authorization OptionalAuthorizedSessionInActiveProject) (serverapi.SessionResolveTransitionResponse, error)
}

type SessionRuntimeService interface {
	ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error)
	ReleaseSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error)
}

type SessionRuntimeTrustedService interface {
	ActivateSessionRuntimeValidated(ctx context.Context, req Validated[serverapi.SessionRuntimeActivateRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionRuntimeActivateResponse, error)
	ReleaseSessionRuntimeValidated(ctx context.Context, req Validated[serverapi.SessionRuntimeReleaseRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionRuntimeReleaseResponse, error)
}

type SessionViewService interface {
	GetSessionMainView(ctx context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error)
	GetSessionTranscriptPage(ctx context.Context, req serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error)
	GetLatestCommittedAssistantFinalAnswer(ctx context.Context, req serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error)
	GetSessionExecutionEnvironment(ctx context.Context, req serverapi.SessionExecutionEnvironmentRequest) (serverapi.SessionExecutionEnvironmentResponse, error)
}

type SessionViewTrustedService interface {
	GetSessionMainViewValidated(ctx context.Context, req Validated[serverapi.SessionMainViewRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionMainViewResponse, error)
	GetSessionTranscriptPageValidated(ctx context.Context, req Validated[serverapi.SessionTranscriptPageRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionTranscriptPageResponse, error)
	GetLatestCommittedAssistantFinalAnswerValidated(ctx context.Context, req Validated[serverapi.SessionLatestCommittedAssistantFinalAnswerRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error)
	GetSessionExecutionEnvironmentValidated(ctx context.Context, req Validated[serverapi.SessionExecutionEnvironmentRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionExecutionEnvironmentResponse, error)
}

type WorktreeService interface {
	GetWorktreeStatus(ctx context.Context, req serverapi.WorktreeStatusRequest) (serverapi.WorktreeStatusResponse, error)
	ListWorktrees(ctx context.Context, req serverapi.WorktreeListRequest) (serverapi.WorktreeListResponse, error)
	ListWorkspaceWorktrees(ctx context.Context, req serverapi.WorktreeWorkspaceListRequest) (serverapi.WorktreeWorkspaceListResponse, error)
	ResolveWorktreeSelector(ctx context.Context, req serverapi.WorktreeSelectorPreviewRequest) (serverapi.WorktreeSelectorPreviewResponse, error)
	PreviewWorktreeDelete(ctx context.Context, req serverapi.WorktreeDeletePreviewRequest) (serverapi.WorktreeDeletePreviewResponse, error)
	ResolveWorktreeCreateTarget(ctx context.Context, req serverapi.WorktreeCreateTargetResolveRequest) (serverapi.WorktreeCreateTargetResolveResponse, error)
	CreateWorktree(ctx context.Context, req serverapi.WorktreeCreateRequest) (serverapi.WorktreeCreateResponse, error)
	EnterWorktree(ctx context.Context, req serverapi.WorktreeEnterRequest) (serverapi.WorktreeScheduledAcknowledgement, error)
	LeaveWorktree(ctx context.Context, req serverapi.WorktreeLeaveRequest) (serverapi.WorktreeScheduledAcknowledgement, error)
	DeleteWorktree(ctx context.Context, req serverapi.WorktreeDeleteRequest) (serverapi.WorktreeDeleteResult, error)
	SubscribeWorktreeSetup(ctx context.Context, req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error)
}

type WorktreeTrustedService interface {
	GetWorktreeStatusValidated(ctx context.Context, req Validated[serverapi.WorktreeStatusRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeStatusResponse, error)
	ListWorktreesValidated(ctx context.Context, req Validated[serverapi.WorktreeListRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeListResponse, error)
	ListWorkspaceWorktreesValidated(
		ctx context.Context,
		req Validated[serverapi.WorktreeWorkspaceListRequest],
		binding AuthorizedProjectWorkspaceBinding,
	) (serverapi.WorktreeWorkspaceListResponse, error)
	ResolveWorktreeSelectorValidated(ctx context.Context, req Validated[serverapi.WorktreeSelectorPreviewRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeSelectorPreviewResponse, error)
	PreviewWorktreeDeleteValidated(ctx context.Context, req Validated[serverapi.WorktreeDeletePreviewRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeDeletePreviewResponse, error)
	ResolveWorktreeCreateTargetValidated(ctx context.Context, req Validated[serverapi.WorktreeCreateTargetResolveRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeCreateTargetResolveResponse, error)
	CreateWorktreeValidated(ctx context.Context, req Validated[serverapi.WorktreeCreateRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeCreateResponse, error)
	EnterWorktreeValidated(ctx context.Context, req Validated[serverapi.WorktreeEnterRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeScheduledAcknowledgement, error)
	LeaveWorktreeValidated(ctx context.Context, req Validated[serverapi.WorktreeLeaveRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeScheduledAcknowledgement, error)
	DeleteWorktreeValidated(ctx context.Context, req Validated[serverapi.WorktreeDeleteRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeDeleteResult, error)
}

type WorkflowService interface {
	CreateWorkflow(ctx context.Context, req serverapi.WorkflowCreateRequest) (serverapi.WorkflowCreateResponse, error)
	CreateAndLinkWorkflowToProject(ctx context.Context, req serverapi.WorkflowCreateAndLinkProjectRequest) (serverapi.WorkflowCreateAndLinkProjectResponse, error)
	UpdateWorkflow(ctx context.Context, req serverapi.WorkflowUpdateRequest) (serverapi.WorkflowGetResponse, error)
	ListWorkflows(ctx context.Context, req serverapi.WorkflowListRequest) (serverapi.WorkflowListResponse, error)
	GetWorkflow(ctx context.Context, req serverapi.WorkflowGetRequest) (serverapi.WorkflowGetResponse, error)
	LinkWorkflowToProject(ctx context.Context, req serverapi.WorkflowLinkProjectRequest) (serverapi.WorkflowLinkProjectResponse, error)
	ListProjectWorkflowLinks(ctx context.Context, req serverapi.WorkflowListProjectLinksRequest) (serverapi.WorkflowListProjectLinksResponse, error)
	SetDefaultProjectWorkflowLink(ctx context.Context, req serverapi.WorkflowSetDefaultProjectLinkRequest) (serverapi.WorkflowSetDefaultProjectLinkResponse, error)
	UnlinkWorkflowFromProject(ctx context.Context, req serverapi.WorkflowUnlinkProjectRequest) (serverapi.WorkflowUnlinkProjectResponse, error)
	PreviewWorkflowDelete(ctx context.Context, req serverapi.WorkflowDeletePreviewRequest) (serverapi.WorkflowDeletePreviewResponse, error)
	DeleteWorkflow(ctx context.Context, req serverapi.WorkflowDeleteRequest) (serverapi.WorkflowDeleteResponse, error)
	ValidateWorkflow(ctx context.Context, req serverapi.WorkflowValidateRequest) (serverapi.WorkflowValidateResponse, error)
	ValidateWorkflowScriptPath(ctx context.Context, req serverapi.WorkflowScriptPathValidateRequest) (serverapi.WorkflowValidateResponse, error)
	ValidateWorkflowGraphDraft(ctx context.Context, req serverapi.WorkflowGraphValidateDraftRequest) (serverapi.WorkflowGraphValidateDraftResponse, error)
	DeriveWorkflowGraphWiring(ctx context.Context, req serverapi.WorkflowGraphDeriveWiringRequest) (serverapi.WorkflowGraphDeriveWiringResponse, error)
	PreviewWorkflowGraphSave(ctx context.Context, req serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error)
	SaveWorkflowGraph(ctx context.Context, req serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error)
	CreateWorkflowProjectLabel(ctx context.Context, req serverapi.WorkflowProjectLabelCreateRequest) (serverapi.WorkflowProjectLabelCreateResponse, error)
	ListWorkflowProjectLabels(ctx context.Context, req serverapi.WorkflowProjectLabelCatalogRequest) (serverapi.WorkflowProjectLabelCatalogResponse, error)
	RenameWorkflowProjectLabel(ctx context.Context, req serverapi.WorkflowProjectLabelRenameRequest) (serverapi.WorkflowProjectLabelRenameResponse, error)
	DeleteWorkflowProjectLabel(ctx context.Context, req serverapi.WorkflowProjectLabelDeleteRequest) (serverapi.WorkflowProjectLabelDeleteResponse, error)
	ReorderWorkflowProjectLabels(ctx context.Context, req serverapi.WorkflowProjectLabelReorderRequest) (serverapi.WorkflowProjectLabelReorderResponse, error)
	GetWorkflowTaskLabels(ctx context.Context, req serverapi.WorkflowTaskLabelsGetRequest) (serverapi.WorkflowTaskLabelsGetResponse, error)
	UpdateWorkflowTaskLabels(ctx context.Context, req serverapi.WorkflowTaskLabelsUpdateRequest) (serverapi.WorkflowTaskLabelsUpdateResponse, error)
	CreateWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCreateRequest) (serverapi.WorkflowTaskCreateResponse, error)
	AddWorkflowTaskDependency(ctx context.Context, req serverapi.WorkflowTaskDependencyAddRequest) (serverapi.WorkflowTaskDependencyAddResponse, error)
	RemoveWorkflowTaskDependency(ctx context.Context, req serverapi.WorkflowTaskDependencyRemoveRequest) (serverapi.WorkflowTaskDependencyRemoveResponse, error)
	ListWorkflowTaskDependencies(ctx context.Context, req serverapi.WorkflowTaskDependencyListRequest) (serverapi.WorkflowTaskDependencyListResponse, error)
	UpdateWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskUpdateRequest) (serverapi.WorkflowTaskUpdateResponse, error)
	StartWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskStartResponse, error)
	InterruptWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskInterruptRequest) (serverapi.WorkflowTaskInterruptResponse, error)
	ResumeWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error)
	ApproveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskApproveRequest) (serverapi.WorkflowTaskApproveResponse, error)
	PreviewWorkflowTaskMove(ctx context.Context, req serverapi.WorkflowTaskMovePreviewRequest) (serverapi.WorkflowTaskMovePreviewResponse, error)
	MoveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskMoveResponse, error)
	CompleteWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskCompleteRequest) (serverapi.WorkflowTaskCompleteResponse, error)
	DeleteWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskDeleteRequest) error
	ListWorkflowAttention(ctx context.Context, req serverapi.WorkflowAttentionListRequest) (serverapi.WorkflowAttentionListResponse, error)
	ListWorkflowTaskAttention(ctx context.Context, req serverapi.WorkflowTaskAttentionListRequest) (serverapi.WorkflowTaskAttentionListResponse, error)
	AddWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentAddRequest) (serverapi.WorkflowTaskCommentAddResponse, error)
	ListWorkflowTaskComments(ctx context.Context, req serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskCommentListResponse, error)
	ReplaceWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentReplaceRequest) error
	DeleteWorkflowTaskComment(ctx context.Context, req serverapi.WorkflowTaskCommentDeleteRequest) error
	ListWorkflowTaskActivity(ctx context.Context, req serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskActivityListResponse, error)
	ListWorkflowTaskSessions(ctx context.Context, req serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskSessionListResponse, error)
	ListWorkflowTasks(ctx context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error)
	SearchWorkflowTasks(ctx context.Context, req serverapi.TaskSearchRequest) (serverapi.TaskSearchResponse, error)
	SubscribeWorkflow(ctx context.Context, req serverapi.WorkflowSubscribeRequest) (serverapi.WorkflowSubscription, error)
	SubscribeWorkflowProject(ctx context.Context, req serverapi.WorkflowProjectSubscribeRequest) (serverapi.WorkflowProjectSubscription, error)
	GetWorkflowBoard(ctx context.Context, req serverapi.WorkflowBoardRequest) (serverapi.WorkflowBoardResponse, error)
	ListWorkflowBoardNodeCards(ctx context.Context, req serverapi.WorkflowBoardNodeCardsListRequest) (serverapi.WorkflowBoardNodeCardsListResponse, error)
	GetWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error)
	ObserveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskObservationRequest) (serverapi.WorkflowTaskObservationResponse, error)
}

type WorkflowTrustedService interface {
	CreateWorkflowValidated(ctx context.Context, req Validated[serverapi.WorkflowCreateRequest]) (serverapi.WorkflowCreateResponse, error)
	CreateAndLinkWorkflowToProjectValidated(ctx context.Context, req Validated[serverapi.WorkflowCreateAndLinkProjectRequest]) (serverapi.WorkflowCreateAndLinkProjectResponse, error)
	UpdateWorkflowValidated(ctx context.Context, req Validated[serverapi.WorkflowUpdateRequest]) (serverapi.WorkflowGetResponse, error)
	ListWorkflowsValidated(ctx context.Context, req Validated[serverapi.WorkflowListRequest]) (serverapi.WorkflowListResponse, error)
	GetWorkflowValidated(ctx context.Context, req Validated[serverapi.WorkflowGetRequest]) (serverapi.WorkflowGetResponse, error)
	LinkWorkflowToProjectValidated(ctx context.Context, req Validated[serverapi.WorkflowLinkProjectRequest]) (serverapi.WorkflowLinkProjectResponse, error)
	ListProjectWorkflowLinksValidated(ctx context.Context, req Validated[serverapi.WorkflowListProjectLinksRequest]) (serverapi.WorkflowListProjectLinksResponse, error)
	SetDefaultProjectWorkflowLinkValidated(ctx context.Context, req Validated[serverapi.WorkflowSetDefaultProjectLinkRequest]) (serverapi.WorkflowSetDefaultProjectLinkResponse, error)
	UnlinkWorkflowFromProjectValidated(ctx context.Context, req Validated[serverapi.WorkflowUnlinkProjectRequest]) (serverapi.WorkflowUnlinkProjectResponse, error)
	PreviewWorkflowDeleteValidated(ctx context.Context, req Validated[serverapi.WorkflowDeletePreviewRequest]) (serverapi.WorkflowDeletePreviewResponse, error)
	DeleteWorkflowValidated(ctx context.Context, req Validated[serverapi.WorkflowDeleteRequest]) (serverapi.WorkflowDeleteResponse, error)
	ValidateWorkflowValidated(ctx context.Context, req Validated[serverapi.WorkflowValidateRequest]) (serverapi.WorkflowValidateResponse, error)
	ValidateWorkflowScriptPathValidated(ctx context.Context, req Validated[serverapi.WorkflowScriptPathValidateRequest]) (serverapi.WorkflowValidateResponse, error)
	ValidateWorkflowGraphDraftValidated(ctx context.Context, req Validated[serverapi.WorkflowGraphValidateDraftRequest]) (serverapi.WorkflowGraphValidateDraftResponse, error)
	DeriveWorkflowGraphWiringValidated(ctx context.Context, req Validated[serverapi.WorkflowGraphDeriveWiringRequest]) (serverapi.WorkflowGraphDeriveWiringResponse, error)
	PreviewWorkflowGraphSaveValidated(ctx context.Context, req Validated[serverapi.WorkflowGraphSavePreviewRequest]) (serverapi.WorkflowGraphSavePreviewResponse, error)
	SaveWorkflowGraphValidated(ctx context.Context, req Validated[serverapi.WorkflowGraphSaveRequest]) (serverapi.WorkflowGraphSaveResponse, error)
	CreateWorkflowProjectLabelValidated(ctx context.Context, req Validated[serverapi.WorkflowProjectLabelCreateRequest]) (serverapi.WorkflowProjectLabelCreateResponse, error)
	RenameWorkflowProjectLabelValidated(ctx context.Context, req Validated[serverapi.WorkflowProjectLabelRenameRequest]) (serverapi.WorkflowProjectLabelRenameResponse, error)
	DeleteWorkflowProjectLabelValidated(ctx context.Context, req Validated[serverapi.WorkflowProjectLabelDeleteRequest]) (serverapi.WorkflowProjectLabelDeleteResponse, error)
	CreateWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskCreateRequest]) (serverapi.WorkflowTaskCreateResponse, error)
	AddWorkflowTaskDependencyValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskDependencyAddRequest]) (serverapi.WorkflowTaskDependencyAddResponse, error)
	RemoveWorkflowTaskDependencyValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskDependencyRemoveRequest]) (serverapi.WorkflowTaskDependencyRemoveResponse, error)
	UpdateWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskUpdateRequest]) (serverapi.WorkflowTaskUpdateResponse, error)
	StartWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskStartRequest]) (serverapi.WorkflowTaskStartResponse, error)
	InterruptWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskInterruptRequest]) (serverapi.WorkflowTaskInterruptResponse, error)
	ResumeWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskResumeRequest]) (serverapi.WorkflowTaskResumeResponse, error)
	ApproveWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskApproveRequest]) (serverapi.WorkflowTaskApproveResponse, error)
	PreviewWorkflowTaskMoveValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskMovePreviewRequest]) (serverapi.WorkflowTaskMovePreviewResponse, error)
	MoveWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskMoveRequest]) (serverapi.WorkflowTaskMoveResponse, error)
	CompleteWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskCompleteRequest]) (serverapi.WorkflowTaskCompleteResponse, error)
	DeleteWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskDeleteRequest]) error
	ObserveWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskObservationRequest]) (serverapi.WorkflowTaskObservationResponse, error)
	ListWorkflowProjectLabelsValidated(ctx context.Context, req Validated[serverapi.WorkflowProjectLabelCatalogRequest]) (serverapi.WorkflowProjectLabelCatalogResponse, error)
	ReorderWorkflowProjectLabelsValidated(ctx context.Context, req Validated[serverapi.WorkflowProjectLabelReorderRequest]) (serverapi.WorkflowProjectLabelReorderResponse, error)
	GetWorkflowTaskLabelsValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskLabelsGetRequest]) (serverapi.WorkflowTaskLabelsGetResponse, error)
	UpdateWorkflowTaskLabelsValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskLabelsUpdateRequest]) (serverapi.WorkflowTaskLabelsUpdateResponse, error)
	ListWorkflowAttentionValidated(ctx context.Context, req Validated[serverapi.WorkflowAttentionListRequest]) (serverapi.WorkflowAttentionListResponse, error)
	ListWorkflowTaskAttentionValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskAttentionListRequest]) (serverapi.WorkflowTaskAttentionListResponse, error)
	ListWorkflowTaskCommentsValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskOffsetPageRequest]) (serverapi.WorkflowTaskCommentListResponse, error)
	ListWorkflowTaskActivityValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskOffsetPageRequest]) (serverapi.WorkflowTaskActivityListResponse, error)
	ListWorkflowTaskSessionsValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskOffsetPageRequest]) (serverapi.WorkflowTaskSessionListResponse, error)
	ListWorkflowTasksValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskListRequest]) (serverapi.WorkflowTaskListResponse, error)
	SearchWorkflowTasksValidated(ctx context.Context, req Validated[serverapi.TaskSearchRequest]) (serverapi.TaskSearchResponse, error)
	GetWorkflowBoardValidated(ctx context.Context, req Validated[serverapi.WorkflowBoardRequest]) (serverapi.WorkflowBoardResponse, error)
	ListWorkflowBoardNodeCardsValidated(ctx context.Context, req Validated[serverapi.WorkflowBoardNodeCardsListRequest]) (serverapi.WorkflowBoardNodeCardsListResponse, error)
	GetWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskGetRequest]) (serverapi.WorkflowTaskGetResponse, error)
}
