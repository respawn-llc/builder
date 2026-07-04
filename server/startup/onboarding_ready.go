package startup

import (
	"context"
	"errors"
	"strings"

	"core/server/auth"
	"core/shared/config"
)

var ErrOnboardingCanceled = errors.New("first-time setup canceled")
var ErrOnboardingRequired = errors.New("first-time setup is required")

func writeHeadlessDefaultSettings(settingsPath string) (string, bool, error) {
	if settingsPath != "" {
		return config.WriteDefaultSettingsFileAt(settingsPath)
	}
	return config.WriteDefaultSettingsFile()
}

// ErrAuthManagerRequired and ErrInteractiveRunnerRequired guard interactive
// onboarding readiness. Callers and tests match these with errors.Is rather
// than comparing rendered message text.
var (
	ErrOnboardingAuthManagerRequired       = errors.New("auth manager is required for onboarding")
	ErrOnboardingInteractiveRunnerRequired = errors.New("interactive onboarding runner is required")
)

type OnboardingResult struct {
	Completed            bool
	CreatedDefaultConfig bool
	SettingsPath         string
}

type OnboardingInteractiveRunner func(ctx context.Context, cfg config.App, authState auth.State) (OnboardingResult, error)

func EnsureOnboardingReady(ctx context.Context, cfg config.App, mgr *auth.Manager, interactive bool, reloadConfig func() (config.App, error), runner OnboardingInteractiveRunner) (config.App, bool, error) {
	if cfg.Source.SettingsFileExists {
		return cfg, false, nil
	}
	if reloadConfig == nil {
		return cfg, false, errors.New("reload config is required")
	}
	if !interactive {
		return cfg, false, ErrOnboardingRequired
	}
	if mgr == nil {
		return cfg, false, ErrOnboardingAuthManagerRequired
	}
	if runner == nil {
		return cfg, false, ErrOnboardingInteractiveRunnerRequired
	}
	state, err := mgr.Load(ctx)
	if err != nil {
		return cfg, false, err
	}
	result, err := runner(ctx, cfg, state)
	if err != nil {
		return cfg, false, err
	}
	if !result.Completed {
		return cfg, false, ErrOnboardingCanceled
	}
	reloaded, err := reloadConfig()
	if err != nil {
		return cfg, false, err
	}
	reloaded.Source.CreatedDefaultConfig = result.CreatedDefaultConfig
	reloaded.Source.SettingsFileExists = true
	if strings.TrimSpace(result.SettingsPath) != "" {
		reloaded.Source.SettingsPath = result.SettingsPath
	}
	return reloaded, true, nil
}
