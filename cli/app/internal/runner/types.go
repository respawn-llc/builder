package runner

import (
	"context"

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
	AgentRole                 string
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
	ForceNewSession bool
	Overrides       serverapi.RunPromptOverrides
}

type Dependencies[S SessionServer, A any, SO any] struct {
	NewAuthInteractor   func() A
	StartSessionServer  func(context.Context, Request[SO], A, bool) (S, error)
	RunSessionLifecycle func(context.Context, S, A, string, SessionLifecycleOptions) error
}
