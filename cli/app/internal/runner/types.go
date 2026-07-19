package runner

import (
	"context"

	"core/shared/config"
	"core/shared/serverapi"
)

type SessionServer interface {
	Close() error
}

type NoStartupOptions struct{}

type Request[SO any] struct {
	WorkspaceRoot             string
	WorkspaceRootExplicit     bool
	SessionID                 string
	WorkspaceContextSessionID string
	AgentRole                 *string
	Model                     string
	ProviderOverride          string
	ThinkingLevel             string
	Theme                     string
	ModelTimeoutSeconds       int
	Tools                     string
	OpenAIBaseURL             string
	OpenAIBaseURLExplicit     bool
	ConfigRoot                string
	StartupOptions            SO
}

type SessionLifecycleOptions struct {
	Intent    *serverapi.SessionLaunchIntent
	Overrides serverapi.RunPromptOverrides
}

type InteractiveConfig struct {
	Server config.App
	Client config.ClientSettings
}

type Dependencies[S SessionServer, A any, SO any] struct {
	ResolveInteractiveConfig func(Request[SO]) (InteractiveConfig, error)
	NewAuthInteractor        func() A
	StartSessionServer       func(context.Context, Request[SO], A, bool, config.App) (S, error)
	RunSessionLifecycle      func(context.Context, S, A, config.ClientSettings, SessionLifecycleOptions) error
}
