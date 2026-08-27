package apicontract

import (
	"context"

	authpb "core/shared/protoapi/gen/kent/api/auth"
	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
	onboardingpb "core/shared/protoapi/gen/kent/api/onboarding"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	serverpb "core/shared/protoapi/gen/kent/api/server"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/serverapi"
	"core/shared/worktreecontract"

	"google.golang.org/protobuf/types/known/emptypb"
)

// Interfaces in this package are in-process bindings for shared RPC routes.
// They intentionally describe method shapes only: no runtime handles,
// lifecycle orchestration, logging, timeout, or close policy belongs here.
type ApprovalViewService interface {
	ListPendingApprovalsBySession(ctx context.Context, req serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error)
}

type AskViewService interface {
	ListPendingAsksBySession(ctx context.Context, req serverapi.AskListPendingBySessionRequest) (serverapi.AskListPendingBySessionResponse, error)
}

type AuthBootstrapService interface {
	GetBootstrapStatus(ctx context.Context, req *emptypb.Empty) (*authpb.BootstrapStatus, error)
	CompleteBootstrap(ctx context.Context, req *authpb.CompleteBootstrapRequest) (*authpb.BootstrapCompletion, error)
	AcknowledgeNoAuth(ctx context.Context, req *emptypb.Empty) (*authpb.NoAuthAcknowledgement, error)
}

type AuthStatusService interface {
	GetStatus(ctx context.Context, req *authpb.GetStatusRequest) (*authpb.Status, error)
}

type CapabilityFactsService interface {
	GetFacts(ctx context.Context, req *capabilitypb.GetFactsRequest) (*capabilitypb.Facts, error)
}

type ChatContextService interface {
	GetChatContext(ctx context.Context, req serverapi.ChatContextRequest) (serverapi.ChatContextResponse, error)
}

type PromptCommandCatalogService interface {
	GetPromptCommandCatalog(ctx context.Context, req serverapi.PromptCommandCatalogRequest) (serverapi.PromptCommandCatalogResponse, error)
}

type OnboardingFinalizeService interface {
	Finalize(ctx context.Context, req *onboardingpb.FinalizeRequest) (*onboardingpb.FinalizeSuccess, error)
}

type ProcessControlService interface {
	KillProcess(ctx context.Context, req serverapi.ProcessKillRequest) (serverapi.ProcessKillResponse, error)
	GetInlineOutput(ctx context.Context, req serverapi.ProcessInlineOutputRequest) (serverapi.ProcessInlineOutputResponse, error)
}

type ProcessViewService interface {
	ListProcesses(ctx context.Context, req serverapi.ProcessListRequest) (serverapi.ProcessListResponse, error)
	GetProcess(ctx context.Context, req serverapi.ProcessGetRequest) (serverapi.ProcessGetResponse, error)
}

type ProjectViewService interface {
	ListProjects(ctx context.Context, req *emptypb.Empty) (*projectpb.ProjectListSuccess, error)
	ListProjectHome(ctx context.Context, req *projectpb.ProjectHomeListRequest) (*projectpb.ProjectHomeListSuccess, error)
	ResolveProjectPath(ctx context.Context, req *projectpb.ResolvePathRequest) (*projectpb.ResolvePathSuccess, error)
	PlanWorkspaceBinding(ctx context.Context, req *projectpb.PlanWorkspaceBindingRequest) (*projectpb.PlanWorkspaceBindingSuccess, error)
	CreateProject(ctx context.Context, req *projectpb.CreateProjectRequest) (*projectpb.CreateProjectSuccess, error)
	GetProjectEdit(ctx context.Context, req *projectpb.ProjectEditGetRequest) (*projectpb.GetProjectEditSuccess, error)
	UpdateProject(ctx context.Context, req *projectpb.UpdateProjectRequest) (*projectpb.UpdateProjectSuccess, error)
	SetDefaultWorkspace(ctx context.Context, req *projectpb.SetDefaultWorkspaceRequest) (*projectpb.SetDefaultWorkspaceSuccess, error)
	ListProjectWorkspaces(ctx context.Context, req *projectpb.ProjectWorkspaceListRequest) (*projectpb.ListProjectWorkspacesSuccess, error)
	GetProjectWorkspace(ctx context.Context, req *projectpb.GetProjectWorkspaceRequest) (*projectpb.GetProjectWorkspaceSuccess, error)
	UnlinkWorkspaceFromProject(ctx context.Context, req *projectpb.UnlinkWorkspaceRequest) (*projectpb.UnlinkWorkspaceSuccess, error)
	DeleteProject(ctx context.Context, req *projectpb.DeleteProjectRequest) (*projectpb.DeleteProjectSuccess, error)
	AttachWorkspaceToProject(ctx context.Context, req *projectpb.AttachWorkspaceRequest) (*projectpb.AttachWorkspaceSuccess, error)
	RebindWorkspace(ctx context.Context, req *projectpb.RebindWorkspaceRequest) (*projectpb.RebindWorkspaceSuccess, error)
	GetProjectOverview(ctx context.Context, req *projectpb.GetOverviewRequest) (*projectpb.GetOverviewSuccess, error)
	ListSessionPage(ctx context.Context, req *projectpb.SessionPageRequest) (*projectpb.SessionPageSuccess, error)
}

type AttentionNotificationService interface {
	SubscribeAttentionNotifications(ctx context.Context, req serverapi.AttentionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error)
	SubscribeSessionAttentionNotifications(ctx context.Context, req serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error)
}

type PromptControlService interface {
	AnswerPromptBatch(ctx context.Context, req serverapi.PromptAnswerBatchRequest) (serverapi.PromptAnswerBatchResponse, error)
	SubscribeFollowUp(ctx context.Context, req serverapi.PromptFollowUpWatchRequest) (serverapi.PromptFollowUpSubscription, error)
}

type RunPromptService interface {
	RunPrompt(ctx context.Context, req serverapi.RunPromptRequest, progress serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error)
}

type ServerStatusService interface {
	GetReadiness(ctx context.Context, req *emptypb.Empty) (*serverpb.GetReadinessSuccess, error)
	GetUpdateStatus(ctx context.Context, req *emptypb.Empty) (*serverpb.GetUpdateStatusSuccess, error)
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
	SetGoal(ctx context.Context, req serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalShowResponse, error)
	PauseGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error)
	ResumeGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error)
	CompleteGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error)
	ClearGoal(ctx context.Context, req serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error)
}

type RuntimeLiveControlService interface {
	LiveSteer(ctx context.Context, req serverapi.RuntimeLiveSteerRequest) (serverapi.RuntimeLiveSteerResponse, error)
	LiveStop(ctx context.Context, req serverapi.RuntimeLiveStopRequest) (serverapi.RuntimeLiveStopResponse, error)
	LiveWait(ctx context.Context, req serverapi.RuntimeLiveWaitRequest) (serverapi.RuntimeLiveWaitResponse, error)
	LiveWatch(ctx context.Context, req serverapi.RuntimeLiveWatchRequest) (serverapi.RuntimeLiveWatchResponse, error)
}

type SessionTranscriptService interface {
	SubscribeSessionTranscript(ctx context.Context, req serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error)
}

type SessionLaunchService interface {
	PlanSession(ctx context.Context, req *sessionlaunchpb.SessionPlanRequest) (*sessionlaunchpb.SessionPlanSuccess, error)
	WorkspaceChatDraft(ctx context.Context, req *sessionlaunchpb.WorkspaceChatDraftRequest) (*sessionlaunchpb.WorkspaceChatDraftSuccess, error)
	MaterializeWorkspaceChat(ctx context.Context, req *emptypb.Empty) (*sessionlaunchpb.MaterializeWorkspaceChatSuccess, error)
}

type ChatSettingsService interface {
	ReadChatSettings(ctx context.Context, req serverapi.ChatSettingsReadRequest) (serverapi.ChatSettingsReadResponse, error)
}

type SessionLifecycleService interface {
	GetInitialInput(ctx context.Context, req serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error)
	PersistInputDraft(ctx context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error)
	RetargetSessionWorkspace(ctx context.Context, req serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error)
	ResolveTransition(ctx context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error)
}

type SessionRuntimeService interface {
	ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error)
	ReleaseSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error)
}

type SessionViewService interface {
	GetSessionMainView(ctx context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error)
	GetSessionTranscriptPage(ctx context.Context, req serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error)
	GetLatestCommittedAssistantFinalAnswer(ctx context.Context, req serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error)
	GetSessionExecutionEnvironment(ctx context.Context, req serverapi.SessionExecutionEnvironmentRequest) (serverapi.SessionExecutionEnvironmentResponse, error)
	SubscribeQuestionHistory(ctx context.Context, req serverapi.QuestionHistorySubscribeRequest) (serverapi.QuestionHistorySubscription, error)
}

type WorktreeService interface {
	GetWorktreeStatus(ctx context.Context, req worktreecontract.StatusRequest) (worktreecontract.StatusResponse, error)
	ListWorktrees(ctx context.Context, req worktreecontract.ListRequest) (worktreecontract.ListResponse, error)
	ListWorkspaceWorktrees(ctx context.Context, req worktreecontract.WorkspaceListRequest) (worktreecontract.WorkspaceListResponse, error)
	ResolveWorktreeSelector(ctx context.Context, req worktreecontract.SelectorResolveRequest) (worktreecontract.SelectorResolveResponse, error)
	PreviewWorktreeDelete(ctx context.Context, req worktreecontract.DeletePreviewRequest) (worktreecontract.DeletePreviewResponse, error)
	ResolveWorktreeCreateTarget(ctx context.Context, req worktreecontract.CreateTargetResolveRequest) (worktreecontract.CreateTargetResolveResponse, error)
	CreateWorktree(ctx context.Context, req worktreecontract.CreateRequest) (worktreecontract.CreateResponse, error)
	EnterWorktree(ctx context.Context, req worktreecontract.EnterRequest) (worktreecontract.ScheduledAcknowledgement, error)
	LeaveWorktree(ctx context.Context, req worktreecontract.LeaveRequest) (worktreecontract.ScheduledAcknowledgement, error)
	DeleteWorktree(ctx context.Context, req worktreecontract.DeleteRequest) (worktreecontract.DeleteResult, error)
	SubscribeWorktreeSetup(ctx context.Context, req worktreecontract.SetupSubscribeRequest) (worktreecontract.SetupSubscription, error)
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
	GetWorkflowProjectTaskGroupCounts(ctx context.Context, req serverapi.WorkflowProjectTaskGroupCountsRequest) (serverapi.WorkflowProjectTaskGroupCountsResponse, error)
	SearchWorkflowTasks(ctx context.Context, req serverapi.TaskSearchRequest) (serverapi.TaskSearchResponse, error)
	SubscribeWorkflow(ctx context.Context, req serverapi.WorkflowSubscribeRequest) (serverapi.WorkflowSubscription, error)
	SubscribeWorkflowProject(ctx context.Context, req serverapi.WorkflowProjectSubscribeRequest) (serverapi.WorkflowProjectSubscription, error)
	GetWorkflowBoard(ctx context.Context, req serverapi.WorkflowBoardRequest) (serverapi.WorkflowBoardResponse, error)
	ListWorkflowBoardNodeCards(ctx context.Context, req serverapi.WorkflowBoardNodeCardsListRequest) (serverapi.WorkflowBoardNodeCardsListResponse, error)
	GetWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error)
	ObserveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskObservationRequest) (serverapi.WorkflowTaskObservationResponse, error)
}
