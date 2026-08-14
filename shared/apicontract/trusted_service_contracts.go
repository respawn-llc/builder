package apicontract

import (
	"context"

	"core/shared/serverapi"
)

type ApprovalViewTrustedService interface {
	ListPendingApprovalsBySessionValidated(ctx context.Context, req Validated[serverapi.ApprovalListPendingBySessionRequest]) (serverapi.ApprovalListPendingBySessionResponse, error)
}

type AskViewTrustedService interface {
	ListPendingAsksBySessionValidated(ctx context.Context, req Validated[serverapi.AskListPendingBySessionRequest]) (serverapi.AskListPendingBySessionResponse, error)
}

type AuthBootstrapTrustedService interface {
	GetAuthBootstrapStatusValidated(ctx context.Context, req Validated[serverapi.AuthGetBootstrapStatusRequest]) (serverapi.AuthGetBootstrapStatusResponse, error)
	CompleteAuthBootstrapValidated(ctx context.Context, req Validated[serverapi.AuthCompleteBootstrapRequest]) (serverapi.AuthCompleteBootstrapResponse, error)
	AcknowledgeNoAuthValidated(ctx context.Context, req Validated[serverapi.AuthAcknowledgeNoAuthRequest]) (serverapi.AuthAcknowledgeNoAuthResponse, error)
}

type AuthStatusTrustedService interface {
	GetAuthStatusValidated(ctx context.Context, req Validated[serverapi.AuthStatusRequest]) (serverapi.AuthStatusResponse, error)
}

type CapabilityFactsTrustedService interface {
	GetCapabilityFactsValidated(ctx context.Context, req Validated[serverapi.CapabilityFactsRequest]) (serverapi.CapabilityFactsResponse, error)
}

type PromptCommandCatalogTrustedService interface {
	GetPromptCommandCatalogValidated(ctx context.Context, req Validated[serverapi.PromptCommandCatalogRequest]) (serverapi.PromptCommandCatalogResponse, error)
}

type OnboardingFinalizeTrustedService interface {
	FinalizeOnboardingValidated(ctx context.Context, req Validated[serverapi.OnboardingFinalizeRequest]) (serverapi.OnboardingFinalizeResponse, error)
}

type ProjectListTrustedService interface {
	ListProjectsValidated(ctx context.Context, req Validated[serverapi.ProjectListRequest]) (serverapi.ProjectListResponse, error)
	ListProjectHomeValidated(ctx context.Context, req Validated[serverapi.ProjectHomeListRequest]) (serverapi.ProjectHomeListResponse, error)
}

type ProjectCatalogMutationTrustedService interface {
	CreateProjectValidated(ctx context.Context, req Validated[serverapi.ProjectCreateRequest]) (serverapi.ProjectCreateResponse, error)
	GetProjectEditValidated(ctx context.Context, req Validated[serverapi.ProjectEditGetRequest]) (serverapi.ProjectEditGetResponse, error)
	UpdateProjectValidated(ctx context.Context, req Validated[serverapi.ProjectUpdateRequest]) (serverapi.ProjectUpdateResponse, error)
	DeleteProjectValidated(ctx context.Context, req Validated[serverapi.ProjectDeleteRequest]) (serverapi.ProjectDeleteResponse, error)
}

type ProjectWorkspaceResolutionTrustedService interface {
	ResolveProjectPathValidated(ctx context.Context, req Validated[serverapi.ProjectResolvePathRequest]) (serverapi.ProjectResolvePathResponse, error)
	PlanWorkspaceBindingValidated(ctx context.Context, req Validated[serverapi.ProjectBindingPlanRequest]) (serverapi.ProjectBindingPlanResponse, error)
}

type ProjectWorkspaceCatalogTrustedService interface {
	SetDefaultWorkspaceValidated(ctx context.Context, req Validated[serverapi.ProjectDefaultWorkspaceSetRequest]) (serverapi.ProjectDefaultWorkspaceSetResponse, error)
	ListProjectWorkspacesValidated(ctx context.Context, req Validated[serverapi.ProjectWorkspaceListRequest]) (serverapi.ProjectWorkspaceListResponse, error)
	GetProjectWorkspaceValidated(ctx context.Context, req Validated[serverapi.ProjectWorkspaceGetRequest]) (serverapi.ProjectWorkspaceGetResponse, error)
}

type ProjectWorkspaceMutationTrustedService interface {
	UnlinkWorkspaceFromProjectValidated(ctx context.Context, req Validated[serverapi.ProjectWorkspaceUnlinkRequest]) (serverapi.ProjectWorkspaceUnlinkResponse, error)
	AttachWorkspaceToProjectValidated(ctx context.Context, req Validated[serverapi.ProjectAttachWorkspaceRequest]) (serverapi.ProjectAttachWorkspaceResponse, error)
	RebindWorkspaceValidated(ctx context.Context, req Validated[serverapi.ProjectRebindWorkspaceRequest]) (serverapi.ProjectRebindWorkspaceResponse, error)
}

type ProjectOverviewTrustedService interface {
	GetProjectOverviewValidated(ctx context.Context, req Validated[serverapi.ProjectGetOverviewRequest]) (serverapi.ProjectGetOverviewResponse, error)
	ListSessionPageValidated(ctx context.Context, req Validated[serverapi.SessionPageRequest]) (serverapi.SessionPageResponse, error)
}

type RunPromptTrustedService interface {
	RunPromptValidated(ctx context.Context, req Validated[serverapi.RunPromptRequest], progress serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error)
}

type ServerStatusTrustedService interface {
	GetServerReadinessValidated(ctx context.Context, req Validated[serverapi.ServerReadinessRequest]) (serverapi.ServerReadinessResponse, error)
	GetUpdateStatusValidated(ctx context.Context, req Validated[serverapi.UpdateStatusRequest]) (serverapi.UpdateStatusResponse, error)
}

type PromptControlTrustedService interface {
	AnswerPromptBatchValidated(ctx context.Context, req Validated[serverapi.PromptAnswerBatchRequest]) (serverapi.PromptAnswerBatchResponse, error)
	SubscribeFollowUpValidated(ctx context.Context, req Validated[serverapi.PromptFollowUpWatchRequest]) (serverapi.PromptFollowUpSubscription, error)
}

type AttentionNotificationTrustedService interface {
	SubscribeAttentionNotificationsValidated(ctx context.Context, req Validated[serverapi.AttentionNotificationSubscribeRequest]) (serverapi.AttentionNotificationSubscription, error)
	SubscribeSessionAttentionNotificationsValidated(ctx context.Context, req Validated[serverapi.AttentionSessionNotificationSubscribeRequest]) (serverapi.AttentionNotificationSubscription, error)
}

type SessionTranscriptTrustedService interface {
	SubscribeSessionTranscriptValidated(ctx context.Context, req Validated[serverapi.TranscriptSubscribeRequest]) (serverapi.TranscriptSubscription, error)
}

type ProcessViewTrustedService interface {
	ResolveProcessAuthorization(ctx context.Context, processID string) (ProcessAuthorizationCandidate, error)
}

type ProcessGetTrustedService interface {
	GetProcessValidated(ctx context.Context, req Validated[serverapi.ProcessGetRequest], authorization AuthorizedProcessInActiveProject) (serverapi.ProcessGetResponse, error)
}

type ProcessKillTrustedService interface {
	KillProcessValidated(ctx context.Context, req Validated[serverapi.ProcessKillRequest], authorization AuthorizedProcessInActiveProject) (serverapi.ProcessKillResponse, error)
}

type ProcessInlineOutputTrustedService interface {
	GetInlineOutputValidated(ctx context.Context, req Validated[serverapi.ProcessInlineOutputRequest], authorization AuthorizedProcessInActiveProject) (serverapi.ProcessInlineOutputResponse, error)
}

type SessionMainViewTrustedService interface {
	GetSessionMainViewValidated(ctx context.Context, req Validated[serverapi.SessionMainViewRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionMainViewResponse, error)
}

type SessionTranscriptPageTrustedService interface {
	GetSessionTranscriptPageValidated(ctx context.Context, req Validated[serverapi.SessionTranscriptPageRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionTranscriptPageResponse, error)
}

type SessionFinalAnswerTrustedService interface {
	GetLatestCommittedAssistantFinalAnswerValidated(ctx context.Context, req Validated[serverapi.SessionLatestCommittedAssistantFinalAnswerRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error)
}

type SessionExecutionEnvironmentTrustedService interface {
	GetSessionExecutionEnvironmentValidated(ctx context.Context, req Validated[serverapi.SessionExecutionEnvironmentRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionExecutionEnvironmentResponse, error)
}

type SessionInitialInputTrustedService interface {
	GetInitialInputValidated(ctx context.Context, req Validated[serverapi.SessionInitialInputRequest], authorization OptionalAuthorizedSessionInActiveProject) (serverapi.SessionInitialInputResponse, error)
}

type SessionPersistInputDraftTrustedService interface {
	PersistInputDraftValidated(ctx context.Context, req Validated[serverapi.SessionPersistInputDraftRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionPersistInputDraftResponse, error)
}

type SessionRetargetWorkspaceTrustedService interface {
	RetargetSessionWorkspaceValidated(ctx context.Context, req Validated[serverapi.SessionRetargetWorkspaceRequest], constraint AttachedProjectConstraint) (serverapi.SessionRetargetWorkspaceResponse, error)
}

type SessionResolveTransitionTrustedService interface {
	ResolveTransitionValidated(ctx context.Context, req Validated[serverapi.SessionResolveTransitionRequest], authorization OptionalAuthorizedSessionInActiveProject) (serverapi.SessionResolveTransitionResponse, error)
}

type SessionLaunchTrustedService interface {
	PlanSessionValidated(ctx context.Context, req Validated[serverapi.SessionPlanRequest]) (serverapi.SessionPlanResponse, error)
	WorkspaceChatDraftValidated(ctx context.Context, req Validated[serverapi.WorkspaceChatDraftRequest]) (serverapi.WorkspaceChatDraftResponse, error)
	MaterializeWorkspaceChatValidated(ctx context.Context, req Validated[serverapi.WorkspaceChatMaterializeRequest]) (serverapi.WorkspaceChatMaterializeResponse, error)
}

type SessionRuntimeTrustedService interface {
	ActivateSessionRuntimeValidated(ctx context.Context, req Validated[serverapi.SessionRuntimeActivateRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionRuntimeActivateResponse, error)
	ReleaseSessionRuntimeValidated(ctx context.Context, req Validated[serverapi.SessionRuntimeReleaseRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionRuntimeReleaseResponse, error)
}

type RuntimeGoalTrustedService interface {
	ShowGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalShowRequest]) (serverapi.RuntimeGoalShowResponse, error)
	SetGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalSetRequest]) (serverapi.RuntimeGoalMutationResponse, error)
	PauseGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error)
	ResumeGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error)
	CompleteGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error)
	ClearGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalClearRequest]) (serverapi.RuntimeGoalMutationResponse, error)
}

type RuntimeSessionIdentityTrustedService interface {
	SetSessionNameValidated(ctx context.Context, req Validated[serverapi.RuntimeSetSessionNameRequest], authorization AuthorizedSessionInActiveProject) error
}

type RuntimeChatSettingsTrustedService interface {
	SetThinkingLevelValidated(ctx context.Context, req Validated[serverapi.RuntimeSetThinkingLevelRequest], authorization AuthorizedSessionInActiveProject) error
	SetFastModeEnabledValidated(ctx context.Context, req Validated[serverapi.RuntimeSetFastModeEnabledRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeSetFastModeEnabledResponse, error)
	SetReviewerEnabledValidated(ctx context.Context, req Validated[serverapi.RuntimeSetReviewerEnabledRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeSetReviewerEnabledResponse, error)
	SetAutoCompactionEnabledValidated(ctx context.Context, req Validated[serverapi.RuntimeSetAutoCompactionEnabledRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error)
	SetQuestionsEnabledValidated(ctx context.Context, req Validated[serverapi.RuntimeSetQuestionsEnabledRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeSetQuestionsEnabledResponse, error)
}

type RuntimeTranscriptMutationTrustedService interface {
	AppendCommittedEntryValidated(ctx context.Context, req Validated[serverapi.RuntimeAppendCommittedEntryRequest], authorization AuthorizedSessionInActiveProject) error
	RecordPromptHistoryValidated(ctx context.Context, req Validated[serverapi.RuntimeRecordPromptHistoryRequest], authorization AuthorizedSessionInActiveProject) error
}

type RuntimeCompactionTrustedService interface {
	ShouldCompactBeforeUserMessageValidated(ctx context.Context, req Validated[serverapi.RuntimeShouldCompactBeforeUserMessageRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeShouldCompactBeforeUserMessageResponse, error)
	CompactContextValidated(ctx context.Context, req Validated[serverapi.RuntimeCompactContextRequest], authorization AuthorizedSessionInActiveProject) error
}

type RuntimeUserInputTrustedService interface {
	SubmitUserTurnValidated(ctx context.Context, req Validated[serverapi.RuntimeSubmitUserTurnRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeSubmitUserTurnResponse, error)
	SubmitUserShellCommandValidated(ctx context.Context, req Validated[serverapi.RuntimeSubmitUserShellCommandRequest], authorization AuthorizedSessionInActiveProject) error
	DiscardQueuedUserMessageValidated(ctx context.Context, req Validated[serverapi.RuntimeDiscardQueuedUserMessageRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error)
}

type RuntimeInterruptTrustedService interface {
	InterruptValidated(ctx context.Context, req Validated[serverapi.RuntimeInterruptRequest], authorization AuthorizedSessionInActiveProject) (serverapi.RuntimeInterruptResponse, error)
}

type RuntimeLiveControlTrustedService interface {
	LiveSteerValidated(ctx context.Context, req Validated[serverapi.RuntimeLiveSteerRequest], identity RuntimeLiveRequestIdentity) (serverapi.RuntimeLiveSteerResponse, error)
	LiveStopValidated(ctx context.Context, req Validated[serverapi.RuntimeLiveStopRequest], identity RuntimeLiveRequestIdentity) (serverapi.RuntimeLiveStopResponse, error)
	LiveWaitValidated(ctx context.Context, req Validated[serverapi.RuntimeLiveWaitRequest], identity RuntimeLiveRequestIdentity) (serverapi.RuntimeLiveWaitResponse, error)
	LiveWatchValidated(ctx context.Context, req Validated[serverapi.RuntimeLiveWatchRequest], identity RuntimeLiveRequestIdentity) (serverapi.RuntimeLiveWatchResponse, error)
}

type WorktreeQueryTrustedService interface {
	GetWorktreeStatusValidated(ctx context.Context, req Validated[serverapi.WorktreeStatusRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeStatusResponse, error)
	ListWorktreesValidated(ctx context.Context, req Validated[serverapi.WorktreeListRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeListResponse, error)
	ResolveWorktreeSelectorValidated(ctx context.Context, req Validated[serverapi.WorktreeSelectorPreviewRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeSelectorPreviewResponse, error)
	PreviewWorktreeDeleteValidated(ctx context.Context, req Validated[serverapi.WorktreeDeletePreviewRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeDeletePreviewResponse, error)
	ResolveWorktreeCreateTargetValidated(ctx context.Context, req Validated[serverapi.WorktreeCreateTargetResolveRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeCreateTargetResolveResponse, error)
}

type WorktreeWorkspaceListTrustedService interface {
	ListWorkspaceWorktreesValidated(ctx context.Context, req Validated[serverapi.WorktreeWorkspaceListRequest], binding AuthorizedProjectWorkspaceBinding) (serverapi.WorktreeWorkspaceListResponse, error)
}

type WorktreeMutationTrustedService interface {
	CreateWorktreeValidated(ctx context.Context, req Validated[serverapi.WorktreeCreateRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeCreateResponse, error)
	EnterWorktreeValidated(ctx context.Context, req Validated[serverapi.WorktreeEnterRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeScheduledAcknowledgement, error)
	LeaveWorktreeValidated(ctx context.Context, req Validated[serverapi.WorktreeLeaveRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeScheduledAcknowledgement, error)
	DeleteWorktreeValidated(ctx context.Context, req Validated[serverapi.WorktreeDeleteRequest], authorization AuthorizedSessionInActiveProject) (serverapi.WorktreeDeleteResult, error)
}

type WorktreeSetupTrustedService interface {
	SubscribeWorktreeSetupValidated(ctx context.Context, req Validated[serverapi.WorktreeSetupSubscribeRequest]) (serverapi.WorktreeSetupSubscription, error)
}

type WorkflowCatalogTrustedService interface {
	CreateWorkflowValidated(ctx context.Context, req Validated[serverapi.WorkflowCreateRequest]) (serverapi.WorkflowCreateResponse, error)
	CreateAndLinkWorkflowToProjectValidated(ctx context.Context, req Validated[serverapi.WorkflowCreateAndLinkProjectRequest]) (serverapi.WorkflowCreateAndLinkProjectResponse, error)
	UpdateWorkflowValidated(ctx context.Context, req Validated[serverapi.WorkflowUpdateRequest]) (serverapi.WorkflowGetResponse, error)
	ListWorkflowsValidated(ctx context.Context, req Validated[serverapi.WorkflowListRequest]) (serverapi.WorkflowListResponse, error)
	GetWorkflowValidated(ctx context.Context, req Validated[serverapi.WorkflowGetRequest]) (serverapi.WorkflowGetResponse, error)
}

type WorkflowProjectLinkTrustedService interface {
	LinkWorkflowToProjectValidated(ctx context.Context, req Validated[serverapi.WorkflowLinkProjectRequest]) (serverapi.WorkflowLinkProjectResponse, error)
	ListProjectWorkflowLinksValidated(ctx context.Context, req Validated[serverapi.WorkflowListProjectLinksRequest]) (serverapi.WorkflowListProjectLinksResponse, error)
	SetDefaultProjectWorkflowLinkValidated(ctx context.Context, req Validated[serverapi.WorkflowSetDefaultProjectLinkRequest]) (serverapi.WorkflowSetDefaultProjectLinkResponse, error)
	UnlinkWorkflowFromProjectValidated(ctx context.Context, req Validated[serverapi.WorkflowUnlinkProjectRequest]) (serverapi.WorkflowUnlinkProjectResponse, error)
}

type WorkflowDeleteTrustedService interface {
	PreviewWorkflowDeleteValidated(ctx context.Context, req Validated[serverapi.WorkflowDeletePreviewRequest]) (serverapi.WorkflowDeletePreviewResponse, error)
	DeleteWorkflowValidated(ctx context.Context, req Validated[serverapi.WorkflowDeleteRequest]) (serverapi.WorkflowDeleteResponse, error)
}

type WorkflowValidationTrustedService interface {
	ValidateWorkflowValidated(ctx context.Context, req Validated[serverapi.WorkflowValidateRequest]) (serverapi.WorkflowValidateResponse, error)
	ValidateWorkflowScriptPathValidated(ctx context.Context, req Validated[serverapi.WorkflowScriptPathValidateRequest]) (serverapi.WorkflowValidateResponse, error)
	ValidateWorkflowGraphDraftValidated(ctx context.Context, req Validated[serverapi.WorkflowGraphValidateDraftRequest]) (serverapi.WorkflowGraphValidateDraftResponse, error)
	DeriveWorkflowGraphWiringValidated(ctx context.Context, req Validated[serverapi.WorkflowGraphDeriveWiringRequest]) (serverapi.WorkflowGraphDeriveWiringResponse, error)
}

type WorkflowGraphSaveTrustedService interface {
	PreviewWorkflowGraphSaveValidated(ctx context.Context, req Validated[serverapi.WorkflowGraphSavePreviewRequest]) (serverapi.WorkflowGraphSavePreviewResponse, error)
	SaveWorkflowGraphValidated(ctx context.Context, req Validated[serverapi.WorkflowGraphSaveRequest]) (serverapi.WorkflowGraphSaveResponse, error)
}

type WorkflowProjectLabelTrustedService interface {
	CreateWorkflowProjectLabelValidated(ctx context.Context, req Validated[serverapi.WorkflowProjectLabelCreateRequest]) (serverapi.WorkflowProjectLabelCreateResponse, error)
	ListWorkflowProjectLabelsValidated(ctx context.Context, req Validated[serverapi.WorkflowProjectLabelCatalogRequest]) (serverapi.WorkflowProjectLabelCatalogResponse, error)
	RenameWorkflowProjectLabelValidated(ctx context.Context, req Validated[serverapi.WorkflowProjectLabelRenameRequest]) (serverapi.WorkflowProjectLabelRenameResponse, error)
	DeleteWorkflowProjectLabelValidated(ctx context.Context, req Validated[serverapi.WorkflowProjectLabelDeleteRequest]) (serverapi.WorkflowProjectLabelDeleteResponse, error)
	ReorderWorkflowProjectLabelsValidated(ctx context.Context, req Validated[serverapi.WorkflowProjectLabelReorderRequest]) (serverapi.WorkflowProjectLabelReorderResponse, error)
}

type WorkflowTaskLabelTrustedService interface {
	GetWorkflowTaskLabelsValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskLabelsGetRequest]) (serverapi.WorkflowTaskLabelsGetResponse, error)
	UpdateWorkflowTaskLabelsValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskLabelsUpdateRequest]) (serverapi.WorkflowTaskLabelsUpdateResponse, error)
}

type WorkflowTaskMutationTrustedService interface {
	CreateWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskCreateRequest]) (serverapi.WorkflowTaskCreateResponse, error)
	UpdateWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskUpdateRequest]) (serverapi.WorkflowTaskUpdateResponse, error)
	DeleteWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskDeleteRequest]) error
}

type WorkflowTaskDependencyTrustedService interface {
	AddWorkflowTaskDependencyValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskDependencyAddRequest]) (serverapi.WorkflowTaskDependencyAddResponse, error)
	RemoveWorkflowTaskDependencyValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskDependencyRemoveRequest]) (serverapi.WorkflowTaskDependencyRemoveResponse, error)
	ListWorkflowTaskDependenciesValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskDependencyListRequest]) (serverapi.WorkflowTaskDependencyListResponse, error)
}

type WorkflowTaskRunTrustedService interface {
	StartWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskStartRequest]) (serverapi.WorkflowTaskStartResponse, error)
	ResumeWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskResumeRequest]) (serverapi.WorkflowTaskResumeResponse, error)
	CompleteWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskCompleteRequest]) (serverapi.WorkflowTaskCompleteResponse, error)
}

type WorkflowTaskInterruptTrustedService interface {
	InterruptWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskInterruptRequest]) (serverapi.WorkflowTaskInterruptResponse, error)
}

type WorkflowTaskApproveTrustedService interface {
	ApproveWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskApproveRequest]) (serverapi.WorkflowTaskApproveResponse, error)
}

type WorkflowTaskMoveTrustedService interface {
	PreviewWorkflowTaskMoveValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskMovePreviewRequest]) (serverapi.WorkflowTaskMovePreviewResponse, error)
	MoveWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskMoveRequest]) (serverapi.WorkflowTaskMoveResponse, error)
}

type WorkflowAttentionTrustedService interface {
	ListWorkflowAttentionValidated(ctx context.Context, req Validated[serverapi.WorkflowAttentionListRequest]) (serverapi.WorkflowAttentionListResponse, error)
	ListWorkflowTaskAttentionValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskAttentionListRequest]) (serverapi.WorkflowTaskAttentionListResponse, error)
}

type WorkflowTaskCommentTrustedService interface {
	AddWorkflowTaskCommentValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskCommentAddRequest]) (serverapi.WorkflowTaskCommentAddResponse, error)
	ListWorkflowTaskCommentsValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskOffsetPageRequest]) (serverapi.WorkflowTaskCommentListResponse, error)
	ReplaceWorkflowTaskCommentValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskCommentReplaceRequest]) error
	DeleteWorkflowTaskCommentValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskCommentDeleteRequest]) error
}

type WorkflowTaskListTrustedService interface {
	ListWorkflowTaskActivityValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskOffsetPageRequest]) (serverapi.WorkflowTaskActivityListResponse, error)
	ListWorkflowTaskSessionsValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskOffsetPageRequest]) (serverapi.WorkflowTaskSessionListResponse, error)
	ListWorkflowTasksValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskListRequest]) (serverapi.WorkflowTaskListResponse, error)
	SearchWorkflowTasksValidated(ctx context.Context, req Validated[serverapi.TaskSearchRequest]) (serverapi.TaskSearchResponse, error)
}

type WorkflowBoardTrustedService interface {
	GetWorkflowBoardValidated(ctx context.Context, req Validated[serverapi.WorkflowBoardRequest]) (serverapi.WorkflowBoardResponse, error)
	ListWorkflowBoardNodeCardsValidated(ctx context.Context, req Validated[serverapi.WorkflowBoardNodeCardsListRequest]) (serverapi.WorkflowBoardNodeCardsListResponse, error)
}

type WorkflowTaskGetTrustedService interface {
	GetWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskGetRequest]) (serverapi.WorkflowTaskGetResponse, error)
}

type WorkflowTaskObservationTrustedService interface {
	ObserveWorkflowTaskValidated(ctx context.Context, req Validated[serverapi.WorkflowTaskObservationRequest]) (serverapi.WorkflowTaskObservationResponse, error)
}

type WorkflowSubscriptionTrustedService interface {
	SubscribeWorkflowValidated(ctx context.Context, req Validated[serverapi.WorkflowSubscribeRequest]) (serverapi.WorkflowSubscription, error)
	SubscribeWorkflowProjectValidated(ctx context.Context, req Validated[serverapi.WorkflowProjectSubscribeRequest]) (serverapi.WorkflowProjectSubscription, error)
}
