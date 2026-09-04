package launch

import (
	"context"
	"errors"
	"strings"

	"core/server/metadata"
	"core/server/session"
	"core/server/subagentpolicy"
	"core/shared/textutil"
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
}

func ResolveSessionCaller(persistenceRoot string, sessionID string) (subagentpolicy.Caller, error) {
	if _, err := openSessionByID(persistenceRoot, sessionID); err != nil {
		return subagentpolicy.Caller{}, err
	}
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		return subagentpolicy.Caller{}, err
	}
	defer func() { _ = metadataStore.Close() }()
	taskID, err := metadataStore.WorkflowTaskIDForSession(context.Background(), sessionID)
	if err != nil {
		return subagentpolicy.Caller{}, err
	}
	return subagentpolicy.Caller{Workflow: taskID != nil}, nil
}

// ValidateSessionExists verifies a session reference without exposing its
// persisted metadata to callers that only need provenance validation.
func ValidateSessionExists(persistenceRoot string, sessionID string) error {
	_, err := openSessionByID(persistenceRoot, sessionID)
	return err
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
	if !req.WorkspaceRootExplicit && strings.TrimSpace(meta.WorkspaceRoot) != "" {
		plan.WorkspaceRoot = strings.TrimSpace(meta.WorkspaceRoot)
	}
	if req.OpenAIBaseURLExplicit {
		return plan, nil
	}
	if meta.Continuation != nil {
		baseURL, present := textutil.OptionalTrimmed(meta.Continuation.OpenAIBaseURL)
		if !present {
			return plan, nil
		}
		plan.OpenAIBaseURL = baseURL
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
