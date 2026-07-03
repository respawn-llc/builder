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
	WorkspaceRoot                   string
	WorkspaceRootExplicit           bool
	SessionID                       string
	WorkspaceContextSessionID       string
	AgentRole                       string
	Model                           string
	ProviderOverride                string
	ThinkingLevel                   string
	Theme                           string
	ModelTimeoutSeconds             int
	Tools                           string
	OpenAIBaseURL                   string
	OpenAIBaseURLExplicit           bool
	ConfigRoot                      string
	StartupOptions                  SO
	TerminalPhaseMarkerEncoder      TerminalPhaseMarkerEncoder
	TerminalPhaseMarkerSinkObserver TerminalPhaseMarkerSinkObserver
}

type SessionLifecycleOptions struct {
	ForceNewSession                 bool
	Overrides                       serverapi.RunPromptOverrides
	TerminalPhaseMarkerEncoder      TerminalPhaseMarkerEncoder
	TerminalPhaseMarkerSinkObserver TerminalPhaseMarkerSinkObserver
}

type Dependencies[S SessionServer, A any, SO any] struct {
	NewAuthInteractor   func() A
	StartSessionServer  func(context.Context, Request[SO], A, bool) (S, error)
	RunSessionLifecycle func(context.Context, S, A, string, SessionLifecycleOptions) error
}

type CompileTimeFixtureDependencies[S SessionServer, A any, SO any] struct {
	Request      Request[SO]
	Dependencies Dependencies[S, A, SO]
}
