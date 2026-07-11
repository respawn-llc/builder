package startup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/server/auth"
	"core/shared/config"
)

func TestEnsureReadySkipsWhenSettingsFileExists(t *testing.T) {
	cfg := config.App{Source: config.SourceReport{SettingsFileExists: true, SettingsPath: "/tmp/settings.toml"}}
	reloaded, changed, err := EnsureOnboardingReady(context.Background(), cfg, nil, false, func() (config.App, error) {
		t.Fatal("did not expect reload")
		return config.App{}, nil
	}, nil)
	if err != nil {
		t.Fatalf("ensure ready: %v", err)
	}
	if changed {
		t.Fatal("did not expect onboarding changes")
	}
	if !reloaded.Source.SettingsFileExists || reloaded.Source.SettingsPath != "/tmp/settings.toml" {
		t.Fatalf("unexpected config passthrough: %+v", reloaded.Source)
	}
}

func TestEnsureReadyRequiresAuthManagerForInteractive(t *testing.T) {
	_, _, err := EnsureOnboardingReady(context.Background(), config.App{}, nil, true, func() (config.App, error) {
		return config.App{}, nil
	}, func(context.Context, config.App, auth.State) (OnboardingResult, error) {
		return OnboardingResult{}, nil
	})
	if err == nil || !errors.Is(err, ErrOnboardingAuthManagerRequired) {
		t.Fatalf("expected missing auth manager error, got %v", err)
	}
}

func TestEnsureReadyRequiresRunnerForInteractive(t *testing.T) {
	mgr := auth.NewManager(auth.NewMemoryStore(auth.EmptyState()), nil, time.Now)
	_, _, err := EnsureOnboardingReady(context.Background(), config.App{}, mgr, true, func() (config.App, error) {
		return config.App{}, nil
	}, nil)
	if err == nil || !errors.Is(err, ErrOnboardingInteractiveRunnerRequired) {
		t.Fatalf("expected missing interactive runner error, got %v", err)
	}
}

func TestEnsureReadyReturnsCanceledWhenInteractiveFlowNotCompleted(t *testing.T) {
	mgr := auth.NewManager(auth.NewMemoryStore(auth.EmptyState()), nil, time.Now)
	_, _, err := EnsureOnboardingReady(context.Background(), config.App{}, mgr, true, func() (config.App, error) {
		return config.App{}, nil
	}, func(context.Context, config.App, auth.State) (OnboardingResult, error) {
		return OnboardingResult{Completed: false}, nil
	})
	if !errors.Is(err, ErrOnboardingCanceled) {
		t.Fatalf("expected onboarding canceled, got %v", err)
	}
}

func TestEnsureReadyInteractiveReloadsResultMetadata(t *testing.T) {
	mgr := auth.NewManager(auth.NewMemoryStore(auth.State{Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "sk-test"}}}), nil, time.Now)
	reloadedCfg := config.App{}
	reloaded, changed, err := EnsureOnboardingReady(context.Background(), config.App{}, mgr, true, func() (config.App, error) {
		return reloadedCfg, nil
	}, func(ctx context.Context, cfg config.App, state auth.State) (OnboardingResult, error) {
		if state.Method.APIKey == nil || state.Method.APIKey.Key != "sk-test" {
			t.Fatalf("unexpected auth state: %+v", state.Method)
		}
		return OnboardingResult{Completed: true, CreatedDefaultConfig: true, SettingsPath: "/tmp/settings.toml"}, nil
	})
	if err != nil {
		t.Fatalf("ensure ready: %v", err)
	}
	if !changed {
		t.Fatal("expected onboarding changes")
	}
	if !reloaded.Source.SettingsFileExists || !reloaded.Source.CreatedDefaultConfig || reloaded.Source.SettingsPath != "/tmp/settings.toml" {
		t.Fatalf("unexpected reloaded source: %+v", reloaded.Source)
	}
}

func TestEnsureReadyHeadlessRequiresFinalizeWithoutWritingSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, changed, err := EnsureOnboardingReady(context.Background(), config.App{}, nil, false, func() (config.App, error) {
		return config.App{}, nil
	}, nil)
	if !errors.Is(err, ErrOnboardingRequired) {
		t.Fatalf("ensure ready error = %v, want ErrOnboardingRequired", err)
	}
	if changed {
		t.Fatal("expected no onboarding changes")
	}
	settingsPath := filepath.Join(home, config.ConfigDirName, "config.toml")
	if _, err := os.Stat(settingsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected settings file to remain absent, stat err=%v", err)
	}
}
