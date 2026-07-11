package bootstrap

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"core/prompts"
	"core/server/auth"
	"core/server/launch"
	"core/server/runtime"
	"core/server/runtimewire"
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
	LookupEnv             func(string) string
	Now                   func() time.Time
}

type ConfigPlan struct {
	Config         config.App
	ContainerDir   string
	SessionContext *SessionCallerContext
}

type SessionCallerContext struct {
	WorkflowSession bool
	AgentRole       *string
}

func ResolveSessionCallerContext(persistenceRoot string, sessionID string) (SessionCallerContext, error) {
	context, err := launch.ResolveSessionCallerContext(persistenceRoot, sessionID)
	if err != nil {
		return SessionCallerContext{}, err
	}
	return SessionCallerContext{WorkflowSession: context.WorkflowSession, AgentRole: context.AgentRole}, nil
}

type AuthSupport struct {
	OAuthOptions auth.OpenAIOAuthOptions
	AuthManager  *auth.Manager
}

type RuntimeSupport struct {
	FastModeState    *runtime.FastModeState
	Background       *shelltool.Manager
	BackgroundRouter *runtimewire.BackgroundEventRouter
	Generated        prompts.GeneratedSyncResult
}

func ResolveConfig(req Request) (ConfigPlan, error) {
	now := req.Now
	if now == nil {
		now = time.Now
	}
	bootstrapPlan := launch.BootstrapPlan{
		WorkspaceRoot:    strings.TrimSpace(req.WorkspaceRoot),
		OpenAIBaseURL:    strings.TrimSpace(req.OpenAIBaseURL),
		UseOpenAIBaseURL: req.OpenAIBaseURLExplicit,
	}
	cfg, err := loadConfig(req.LoadOptions, bootstrapPlan.WorkspaceRoot, bootstrapPlan.OpenAIBaseURL, bootstrapPlan.UseOpenAIBaseURL)
	if err != nil {
		return ConfigPlan{}, err
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
	cfg, err = loadConfig(req.LoadOptions, bootstrapPlan.WorkspaceRoot, bootstrapPlan.OpenAIBaseURL, bootstrapPlan.UseOpenAIBaseURL)
	if err != nil {
		return ConfigPlan{}, err
	}
	var sessionContext *SessionCallerContext
	if bootstrapPlan.SessionContext != nil {
		sessionContext = &SessionCallerContext{
			WorkflowSession: bootstrapPlan.SessionContext.WorkflowSession,
			AgentRole:       bootstrapPlan.SessionContext.AgentRole,
		}
	}
	return ConfigPlan{Config: cfg, SessionContext: sessionContext}, nil
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
	background, err := shelltool.NewManager(
		shelltool.WithMinimumExecToBgTime(time.Duration(cfg.Settings.MinimumExecToBgSeconds)*time.Second),
		shelltool.WithPostprocessor(postprocess.NewRunner(postprocess.Settings{
			Mode:     cfg.Settings.Shell.PostprocessingMode,
			HookPath: cfg.Settings.Shell.PostprocessHook,
		})),
	)
	if err != nil {
		return RuntimeSupport{}, err
	}
	return RuntimeSupport{
		FastModeState:    runtime.NewFastModeState(cfg.Settings.PriorityRequestMode),
		Background:       background,
		BackgroundRouter: runtimewire.NewBackgroundEventRouter(background, cfg.Settings.ShellOutputMaxChars, shelltool.NormalizeBackgroundOutputMode(string(cfg.Settings.BGShellsOutput))),
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
