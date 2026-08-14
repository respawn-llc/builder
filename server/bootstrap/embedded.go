package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"core/prompts"
	"core/server/auth"
	"core/server/launch"
	"core/server/runtime"
	shelltool "core/server/tools/shell"
	"core/server/tools/shell/postprocess"
	"core/shared/config"
	"core/shared/textutil"
)

type Request struct {
	WorkspaceRoot         string
	WorkspaceRootExplicit bool
	SessionID             string
	OpenAIBaseURL         string
	OpenAIBaseURLExplicit bool
	LoadOptions           config.LoadOptions
	InitialConfig         *InitialConfigSnapshot
	LookupEnv             func(string) string
	Now                   func() time.Time
}

type InitialConfigSnapshot struct {
	Config           config.App
	WorkspaceRoot    string
	OpenAIBaseURL    string
	UseOpenAIBaseURL bool
}

type ConfigPlan struct {
	Config config.App
}

func ValidateSessionExists(persistenceRoot string, sessionID string) error {
	return launch.ValidateSessionExists(persistenceRoot, sessionID)
}

type AuthSupport struct {
	OAuthOptions auth.OpenAIOAuthOptions
	AuthManager  *auth.Manager
}

type RuntimeSupport struct {
	FastModeState *runtime.FastModeState
	Background    *shelltool.Manager
	Generated     prompts.GeneratedSyncResult
}

func ResolveConfig(req Request) (ConfigPlan, error) {
	bootstrapPlan := launch.BootstrapPlan{
		WorkspaceRoot:    strings.TrimSpace(req.WorkspaceRoot),
		OpenAIBaseURL:    strings.TrimSpace(req.OpenAIBaseURL),
		UseOpenAIBaseURL: req.OpenAIBaseURLExplicit,
	}
	var cfg config.App
	var err error
	if req.InitialConfig == nil {
		cfg, err = loadConfig(req.LoadOptions, bootstrapPlan.WorkspaceRoot, bootstrapPlan.OpenAIBaseURL, bootstrapPlan.UseOpenAIBaseURL)
		if err != nil {
			return ConfigPlan{}, err
		}
	} else {
		if req.InitialConfig.WorkspaceRoot != bootstrapPlan.WorkspaceRoot ||
			req.InitialConfig.OpenAIBaseURL != bootstrapPlan.OpenAIBaseURL ||
			req.InitialConfig.UseOpenAIBaseURL != bootstrapPlan.UseOpenAIBaseURL {
			return ConfigPlan{}, errors.New("initial config snapshot does not match bootstrap target")
		}
		cfg = req.InitialConfig.Config
	}
	bootstrapPlan, err = launch.ResolveBootstrapPlan(cfg.PersistenceRoot, launch.BootstrapRequest{
		WorkspaceRoot:         strings.TrimSpace(req.WorkspaceRoot),
		WorkspaceRootExplicit: req.WorkspaceRootExplicit,
		SessionID:             strings.TrimSpace(req.SessionID),
		OpenAIBaseURL:         strings.TrimSpace(req.OpenAIBaseURL),
		OpenAIBaseURLExplicit: req.OpenAIBaseURLExplicit,
	})
	if err != nil {
		return ConfigPlan{}, err
	}
	if req.InitialConfig != nil &&
		bootstrapPlan.WorkspaceRoot == strings.TrimSpace(req.WorkspaceRoot) &&
		bootstrapPlan.OpenAIBaseURL == strings.TrimSpace(req.OpenAIBaseURL) &&
		bootstrapPlan.UseOpenAIBaseURL == req.OpenAIBaseURLExplicit {
		return ConfigPlan{Config: cfg}, nil
	}
	cfg, err = loadConfig(req.LoadOptions, bootstrapPlan.WorkspaceRoot, bootstrapPlan.OpenAIBaseURL, bootstrapPlan.UseOpenAIBaseURL)
	if err != nil {
		return ConfigPlan{}, err
	}
	return ConfigPlan{Config: cfg}, nil
}

func BuildAuthSupport(store auth.Store, lookupEnv func(string) string, now func() time.Time) (AuthSupport, error) {
	if store == nil {
		return AuthSupport{}, errors.New("auth store is required")
	}
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	if now == nil {
		now = time.Now
	}
	oauthOpts := auth.OpenAIOAuthOptions{
		Issuer:   auth.DefaultOpenAIIssuer,
		ClientID: textutil.FirstNonEmpty(strings.TrimSpace(lookupEnv("KENT_OAUTH_CLIENT_ID")), auth.DefaultOpenAIClientID),
	}
	return AuthSupport{
		OAuthOptions: oauthOpts,
		AuthManager: auth.NewManager(
			store,
			auth.NewOpenAIOAuthRefresher(oauthOpts, now, 5*time.Minute),
			now,
		),
	}, nil
}

func BuildRuntimeSupport(cfg config.App) (RuntimeSupport, error) {
	runner, err := postprocess.NewRunner(postprocess.Settings{
		Mode:     cfg.Settings.Shell.PostprocessingMode,
		HookPath: cfg.Settings.Shell.PostprocessHook,
	})
	if err != nil {
		return RuntimeSupport{}, fmt.Errorf("compile shell postprocessor: %w", err)
	}
	background, err := shelltool.NewManager(
		shelltool.WithMinimumExecToBgTime(time.Duration(cfg.Settings.MinimumExecToBgSeconds)*time.Second),
		shelltool.WithPostprocessor(runner),
	)
	if err != nil {
		return RuntimeSupport{}, err
	}
	return RuntimeSupport{
		FastModeState: runtime.NewFastModeState(cfg.Settings.PriorityRequestMode),
		Background:    background,
	}, nil
}

func BuildGeneratedSupport(ctx context.Context, persistenceRoot string) (prompts.GeneratedSyncResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return prompts.GeneratedSync(ctx, prompts.GeneratedSyncOptions{ConfigRoot: strings.TrimSpace(persistenceRoot)})
}

func loadConfig(loadOpts config.LoadOptions, workspaceRoot, openAIBaseURL string, useOpenAIBaseURL bool) (config.App, error) {
	if useOpenAIBaseURL {
		loadOpts.OpenAIBaseURL = openAIBaseURL
	} else {
		loadOpts.OpenAIBaseURL = ""
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		return config.LoadGlobal(loadOpts)
	}
	return config.Load(workspaceRoot, loadOpts)
}
