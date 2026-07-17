package startup

import (
	"context"
	"os"

	"core/server/auth"
	"core/server/authservice"
	"core/shared/config"
)

type headlessAuthHandler struct {
	lookupEnv func(string) string
}

func NewHeadlessHandlers(lookupEnv func(string) string) (AuthHandler, OnboardingHandler) {
	return headlessAuthHandler{lookupEnv: lookupEnv}, func(_ context.Context, req OnboardingRequest) (config.App, error) {
		if !req.Config.Source.SettingsFileExists {
			return config.App{}, ErrOnboardingRequired
		}
		return req.Config, nil
	}
}

func (h headlessAuthHandler) WrapStore(base auth.Store) auth.Store {
	return authservice.WrapStoreWithEnvAPIKeyOverride(base, h.LookupEnv)
}

func (h headlessAuthHandler) NeedsInteraction(req authservice.FlowInteractionRequest) bool {
	return req.AuthRequired && !req.Gate.Ready
}

func (h headlessAuthHandler) Interact(context.Context, authservice.FlowInteractionRequest) (authservice.FlowInteractionOutcome, error) {
	return authservice.FlowInteractionOutcome{}, auth.ErrAuthNotConfigured
}

func (h headlessAuthHandler) LookupEnv(key string) string {
	if h.lookupEnv == nil {
		return os.Getenv(key)
	}
	return h.lookupEnv(key)
}
