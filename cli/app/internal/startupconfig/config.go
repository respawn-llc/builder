package startupconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"core/server/bootstrap"
	"core/shared/config"
	"core/shared/sessioncontract"
	"core/shared/sessionenv"
)

type Request struct {
	WorkspaceRoot             string
	WorkspaceRootExplicit     bool
	SessionID                 string
	WorkspaceContextSessionID string
	OpenAIBaseURL             string
	OpenAIBaseURLExplicit     bool
	LoadOptions               config.LoadOptions
}

type RunPromptResult struct {
	Config                config.App
	ResolvedWorkspaceRoot string
	CallerContext         CallerContext
}

type CallerKind string

const (
	CallerKindHuman       CallerKind = "human"
	CallerKindKentSession CallerKind = "kent_session"
)

type CallerContext struct {
	Kind            CallerKind
	WorkflowSession bool
	AgentRole       *string
}

func humanCallerContext() CallerContext {
	return CallerContext{Kind: CallerKindHuman}
}

func ResolveSessionConfig(req Request) (config.App, error) {
	workspaceRoot, err := ResolveWorkspaceRoot(req.WorkspaceRoot)
	if err != nil {
		return config.App{}, err
	}
	plan, err := bootstrap.ResolveConfig(bootstrap.Request{
		WorkspaceRoot:         workspaceRoot,
		WorkspaceRootExplicit: req.WorkspaceRootExplicit,
		SessionID:             req.SessionID,
		OpenAIBaseURL:         req.OpenAIBaseURL,
		OpenAIBaseURLExplicit: req.OpenAIBaseURLExplicit,
		LoadOptions:           req.LoadOptions,
	})
	if err != nil {
		return config.App{}, err
	}
	return plan.Config, nil
}

func ResolveRunPromptConfig(req Request) (RunPromptResult, error) {
	workspaceRoot, err := ResolveWorkspaceRoot(req.WorkspaceRoot)
	if err != nil {
		return RunPromptResult{}, err
	}
	sessionID := strings.TrimSpace(req.SessionID)
	contextSessionID := strings.TrimSpace(req.WorkspaceContextSessionID)
	if sessionID == "" && !req.WorkspaceRootExplicit {
		sessionID = contextSessionID
	}
	plan, err := bootstrap.ResolveConfig(bootstrap.Request{
		WorkspaceRoot:         workspaceRoot,
		WorkspaceRootExplicit: req.WorkspaceRootExplicit,
		SessionID:             sessionID,
		OpenAIBaseURL:         req.OpenAIBaseURL,
		OpenAIBaseURLExplicit: req.OpenAIBaseURLExplicit,
		LoadOptions:           req.LoadOptions,
	})
	if err != nil {
		if sessionID != "" && sessionID == contextSessionID {
			return RunPromptResult{}, workspaceContextSessionError(contextSessionID, err)
		}
		return RunPromptResult{}, err
	}
	caller := humanCallerContext()
	if contextSessionID != "" {
		sessionContext := plan.SessionContext
		if sessionContext == nil || contextSessionID != sessionID {
			resolved, err := bootstrap.ResolveSessionCallerContext(plan.Config.PersistenceRoot, contextSessionID)
			if err != nil {
				return RunPromptResult{}, workspaceContextSessionError(contextSessionID, err)
			}
			sessionContext = &resolved
		}
		caller = CallerContext{
			Kind:            CallerKindKentSession,
			WorkflowSession: sessionContext.WorkflowSession,
			AgentRole:       sessionContext.AgentRole,
		}
	}
	resolvedRoot := workspaceRoot
	if strings.TrimSpace(plan.Config.WorkspaceRoot) != "" && plan.Config.WorkspaceRoot != workspaceRoot {
		resolvedRoot = plan.Config.WorkspaceRoot
	}
	return RunPromptResult{Config: plan.Config, ResolvedWorkspaceRoot: resolvedRoot, CallerContext: caller}, nil
}

func ResolveWorkspaceRoot(workspaceRoot string) (string, error) {
	trimmed := strings.TrimSpace(workspaceRoot)
	if trimmed == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		trimmed = cwd
	}
	return filepath.Abs(trimmed)
}

// ErrWorkspaceContextSessionMissing marks the implicit workspace-context
// session lookup that failed because the referenced session no longer exists.
// It wraps sessioncontract.ErrSessionNotFound so callers and tests can
// distinguish the workspace-context guidance path from a strict explicit
// session lookup with errors.Is rather than matching rendered message text.
var ErrWorkspaceContextSessionMissing = errors.New("workspace context session is missing")

func workspaceContextSessionError(sessionID string, err error) error {
	if errors.Is(err, sessioncontract.ErrSessionNotFound) {
		return fmt.Errorf("%s points to missing Kent session %q; unset %s or run from a live Kent shell: %w: %w", sessionenv.SessionIDEnv, strings.TrimSpace(sessionID), sessionenv.SessionIDEnv, ErrWorkspaceContextSessionMissing, err)
	}
	return fmt.Errorf("resolve %s workspace context %q: %w", sessionenv.SessionIDEnv, strings.TrimSpace(sessionID), err)
}
