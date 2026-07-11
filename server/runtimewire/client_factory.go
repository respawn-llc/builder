package runtimewire

import (
	"context"
	"errors"
	"fmt"

	"core/server/llm"
	"core/shared/config"
	"core/shared/toolspec"
)

var ErrRuntimeClientFactoryConflict = errors.New("runtime client factory cannot be combined with direct client override")

type RuntimeClientPurpose int

const (
	RuntimeClientPurposeMain RuntimeClientPurpose = iota + 1
	RuntimeClientPurposeReviewer
	RuntimeClientPurposeWorkflow
)

type RuntimeClientRequest struct {
	Purpose          RuntimeClientPurpose
	SessionID        string
	ActiveSettings   config.Settings
	EnabledTools     []toolspec.ID
	WorkspaceRoot    string
	Sources          map[string]string
	ProviderSettings RuntimeClientProviderSettings
}

type RuntimeClientProviderSettings struct {
	Model                        string
	ProviderOverride             string
	OpenAIBaseURL                string
	ModelVerbosity               config.ModelVerbosity
	Store                        bool
	ContextWindowTokens          int
	Auth                         string
	ProviderCapabilitiesOverride *llm.ProviderCapabilities
}

type RuntimeClientFactory interface {
	NewRuntimeClient(context.Context, RuntimeClientRequest) (llm.Client, error)
}

type RuntimeClientFactoryFunc func(context.Context, RuntimeClientRequest) (llm.Client, error)

func (f RuntimeClientFactoryFunc) NewRuntimeClient(ctx context.Context, req RuntimeClientRequest) (llm.Client, error) {
	return f(ctx, req)
}

func runtimeClientProviderSettings(settings providerRuntimeSettings) RuntimeClientProviderSettings {
	return RuntimeClientProviderSettings{
		Model:                        settings.Model,
		ProviderOverride:             settings.ProviderOverride,
		OpenAIBaseURL:                settings.OpenAIBaseURL,
		ModelVerbosity:               settings.ModelVerbosity,
		Store:                        settings.Store,
		ContextWindowTokens:          settings.ContextWindowTokens,
		Auth:                         settings.Auth,
		ProviderCapabilitiesOverride: settings.ProviderCapabilitiesOverride,
	}
}

func newRuntimeClientFromFactory(ctx context.Context, factory RuntimeClientFactory, purpose RuntimeClientPurpose, storeSessionID string, active config.Settings, enabledTools []toolspec.ID, workspaceRoot string, sources map[string]string, provider providerRuntimeSettings) (llm.Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	client, err := factory.NewRuntimeClient(ctx, RuntimeClientRequest{
		Purpose:          purpose,
		SessionID:        storeSessionID,
		ActiveSettings:   active,
		EnabledTools:     append([]toolspec.ID(nil), enabledTools...),
		WorkspaceRoot:    workspaceRoot,
		Sources:          cloneSources(sources),
		ProviderSettings: runtimeClientProviderSettings(provider),
	})
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("runtime client factory returned nil client for purpose %d", purpose)
	}
	return client, nil
}

func cloneSources(sources map[string]string) map[string]string {
	if sources == nil {
		return nil
	}
	cloned := make(map[string]string, len(sources))
	for key, value := range sources {
		cloned[key] = value
	}
	return cloned
}
