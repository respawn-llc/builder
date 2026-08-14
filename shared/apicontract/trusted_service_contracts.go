package apicontract

import (
	"context"

	"core/shared/serverapi"
)

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

type WorktreeTrustedService interface {
	ListWorkspaceWorktreesValidated(ctx context.Context, req Validated[serverapi.WorktreeWorkspaceListRequest], binding AuthorizedProjectWorkspaceBinding) (serverapi.WorktreeWorkspaceListResponse, error)
}
