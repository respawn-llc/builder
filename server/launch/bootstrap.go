package launch

import (
	"errors"
	"strings"

	"core/server/metadata"
	"core/server/session"
)

type BootstrapRequest struct {
	WorkspaceRoot         string
	WorkspaceRootExplicit bool
	SessionID             string
	OpenAIBaseURL         string
	OpenAIBaseURLExplicit bool
}

type BootstrapPlan struct {
	WorkspaceRoot    string
	OpenAIBaseURL    string
	UseOpenAIBaseURL bool
	SessionContext   *SessionCallerContext
}

type SessionCallerContext struct {
	WorkflowSession bool
	AgentRole       *string
}

func ResolveSessionCallerContext(persistenceRoot string, sessionID string) (SessionCallerContext, error) {
	store, err := openSessionByID(persistenceRoot, sessionID)
	if err != nil {
		return SessionCallerContext{}, err
	}
	meta := store.Meta()
	context := SessionCallerContext{WorkflowSession: meta.WorkflowSession != nil}
	if meta.Continuation != nil && meta.Continuation.AgentRole != nil {
		role := *meta.Continuation.AgentRole
		context.AgentRole = &role
	}
	return context, nil
}

func ResolveBootstrapPlan(persistenceRoot string, req BootstrapRequest) (BootstrapPlan, error) {
	plan := BootstrapPlan{
		WorkspaceRoot:    strings.TrimSpace(req.WorkspaceRoot),
		OpenAIBaseURL:    strings.TrimSpace(req.OpenAIBaseURL),
		UseOpenAIBaseURL: req.OpenAIBaseURLExplicit,
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return plan, nil
	}
	if strings.TrimSpace(persistenceRoot) == "" {
		return BootstrapPlan{}, errors.New("launch planner persistence root is required")
	}
	store, err := openSessionByID(persistenceRoot, req.SessionID)
	if err != nil {
		return BootstrapPlan{}, err
	}
	meta := store.Meta()
	plan.SessionContext = &SessionCallerContext{WorkflowSession: meta.WorkflowSession != nil}
	if meta.Continuation != nil && meta.Continuation.AgentRole != nil {
		plan.SessionContext.AgentRole = cloneContinuationRole(meta.Continuation.AgentRole)
	}
	if !req.WorkspaceRootExplicit && strings.TrimSpace(meta.WorkspaceRoot) != "" {
		plan.WorkspaceRoot = strings.TrimSpace(meta.WorkspaceRoot)
	}
	if req.OpenAIBaseURLExplicit {
		return plan, nil
	}
	if meta.Continuation != nil && strings.TrimSpace(meta.Continuation.OpenAIBaseURL) != "" {
		plan.OpenAIBaseURL = strings.TrimSpace(meta.Continuation.OpenAIBaseURL)
		plan.UseOpenAIBaseURL = true
	}
	return plan, nil
}

func openSessionByID(persistenceRoot string, sessionID string) (*session.Store, error) {
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = metadataStore.Close() }()
	store, err := session.OpenByID(persistenceRoot, sessionID, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		return nil, err
	}
	return store, nil
}
