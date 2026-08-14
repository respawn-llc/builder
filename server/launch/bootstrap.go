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
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		return subagentpolicy.Caller{}, err
	}
	defer func() { _ = metadataStore.Close() }()
	return ResolveSessionCallerWithStore(persistenceRoot, sessionID, metadataStore)
}

func ResolveSessionCallerWithStore(
	persistenceRoot string,
	sessionID string,
	metadataStore interface {
		AuthoritativeSessionStoreOptions() []session.StoreOption
		SessionHasWorkflowTask(context.Context, string) (bool, error)
	},
) (subagentpolicy.Caller, error) {
	if metadataStore == nil {
		return subagentpolicy.Caller{}, errors.New("metadata store is required")
	}
	if _, err := session.OpenByID(
		persistenceRoot,
		sessionID,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	); err != nil {
		return subagentpolicy.Caller{}, err
	}
	workflow, err := metadataStore.SessionHasWorkflowTask(context.Background(), sessionID)
	if err != nil {
		return subagentpolicy.Caller{}, err
	}
	return subagentpolicy.Caller{Workflow: workflow}, nil
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
