package apicontract

import (
	"context"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type RunPromptTrustedService interface {
	RunPromptValidated(ctx context.Context, req Validated[serverapi.RunPromptRequest], progress serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error)
}

type PromptControlTrustedService interface {
	SubscribeFollowUpValidated(ctx context.Context, req Validated[serverapi.PromptFollowUpWatchRequest]) (serverapi.PromptFollowUpSubscription, error)
}

type PromptAnswerBatchTrustedService interface {
	AnswerPromptBatchValidated(ctx context.Context, req Validated[serverapi.PromptAnswerBatchRequest], sessionID runtimeids.SessionID) (serverapi.PromptAnswerBatchResponse, error)
}

type AskViewTrustedService interface {
	ListPendingAsksBySessionValidated(ctx context.Context, req Validated[serverapi.AskListPendingBySessionRequest], sessionID runtimeids.SessionID) (serverapi.AskListPendingBySessionResponse, error)
}

type ApprovalViewTrustedService interface {
	ListPendingApprovalsBySessionValidated(ctx context.Context, req Validated[serverapi.ApprovalListPendingBySessionRequest], sessionID runtimeids.SessionID) (serverapi.ApprovalListPendingBySessionResponse, error)
}

type PromptCommandCatalogTrustedService interface {
	GetPromptCommandCatalogValidated(ctx context.Context, req Validated[serverapi.PromptCommandCatalogRequest]) (serverapi.PromptCommandCatalogResponse, error)
}

type SessionLaunchTrustedService interface {
	PlanSessionValidated(ctx context.Context, req Validated[serverapi.SessionPlanRequest]) (serverapi.SessionPlanResponse, error)
	WorkspaceChatDraftValidated(ctx context.Context, req Validated[serverapi.WorkspaceChatDraftRequest]) (serverapi.WorkspaceChatDraftResponse, error)
	MaterializeWorkspaceChatValidated(ctx context.Context, req Validated[serverapi.WorkspaceChatMaterializeRequest]) (serverapi.WorkspaceChatMaterializeResponse, error)
}

type AttentionNotificationTrustedService interface {
	SubscribeAttentionNotificationsValidated(ctx context.Context, req Validated[serverapi.AttentionNotificationSubscribeRequest]) (serverapi.AttentionNotificationSubscription, error)
	SubscribeSessionAttentionNotificationsValidated(ctx context.Context, req Validated[serverapi.AttentionSessionNotificationSubscribeRequest], sessionID runtimeids.SessionID) (serverapi.AttentionNotificationSubscription, error)
}

type SessionTranscriptTrustedService interface {
	SubscribeSessionTranscriptValidated(ctx context.Context, req Validated[serverapi.TranscriptSubscribeRequest], sessionID runtimeids.SessionID) (serverapi.TranscriptSubscription, error)
}

type WorkflowSubscriptionTrustedService interface {
	SubscribeWorkflowValidated(ctx context.Context, req Validated[serverapi.WorkflowSubscribeRequest]) (serverapi.WorkflowSubscription, error)
	SubscribeWorkflowProjectValidated(ctx context.Context, req Validated[serverapi.WorkflowProjectSubscribeRequest]) (serverapi.WorkflowProjectSubscription, error)
}

type WorktreeSetupTrustedService interface {
	SubscribeWorktreeSetupValidated(ctx context.Context, req Validated[serverapi.WorktreeSetupSubscribeRequest]) (serverapi.WorktreeSetupSubscription, error)
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

type SessionRuntimeTrustedService interface {
	ActivateSessionRuntimeValidated(ctx context.Context, req Validated[serverapi.SessionRuntimeActivateRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionRuntimeActivateResponse, error)
	ReleaseSessionRuntimeValidated(ctx context.Context, req Validated[serverapi.SessionRuntimeReleaseRequest], authorization AuthorizedSessionInActiveProject) (serverapi.SessionRuntimeReleaseResponse, error)
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

type RuntimeGoalTrustedService interface {
	ShowGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalShowRequest], sessionID runtimeids.SessionID) (serverapi.RuntimeGoalShowResponse, error)
	SetGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalSetRequest], sessionID runtimeids.SessionID) (serverapi.RuntimeGoalMutationResponse, error)
	PauseGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalStatusRequest], sessionID runtimeids.SessionID) (serverapi.RuntimeGoalMutationResponse, error)
	ResumeGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalStatusRequest], sessionID runtimeids.SessionID) (serverapi.RuntimeGoalMutationResponse, error)
	CompleteGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalStatusRequest], sessionID runtimeids.SessionID) (serverapi.RuntimeGoalMutationResponse, error)
	ClearGoalValidated(ctx context.Context, req Validated[serverapi.RuntimeGoalClearRequest], sessionID runtimeids.SessionID) (serverapi.RuntimeGoalMutationResponse, error)
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
