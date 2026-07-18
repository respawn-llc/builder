package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"core/cli/app/internal/authui"
	serverauth "core/server/auth"
	"core/server/authservice"

	"github.com/charmbracelet/lipgloss"
)

// errEnvAPIKeyUnavailable is returned when the env-API-key auth method is
// chosen but OPENAI_API_KEY is not present in the environment.
var errEnvAPIKeyUnavailable = errors.New("OPENAI_API_KEY is not available")

// errUnknownAuthMethod is returned when an unrecognized auth-method choice is
// produced by the method picker. The offending choice is attached via %w.
var errUnknownAuthMethod = errors.New("unknown auth method")

type authInteraction = authui.AuthInteractionRequest

type authInteractor interface {
	WrapStore(base authui.AuthStore) authui.AuthStore
	NeedsInteraction(req authInteraction) bool
	Interact(ctx context.Context, req authInteraction) (authui.AuthInteractionOutcome, error)
	LookupEnv(key string) string
}

type headlessAuthInteractor struct {
	lookupEnv func(string) string
}

type oauthCallbackListener interface {
	RedirectURI() string
	Wait(ctx context.Context, timeoutSeconds time.Duration) (authui.OAuthBrowserCallback, error)
	Close() error
}

type interactiveAuthInteractor struct {
	stdin                 io.Reader
	stderr                io.Writer
	lookupEnv             func(string) string
	openBrowser           func(string) error
	startCallbackListener func() (oauthCallbackListener, error)
	runDeviceFlow         func(context.Context, authui.OAuthOptions, func(authui.OAuthDeviceCode)) (authui.AuthMethod, error)
	runCallbackPage       func(context.Context, authCallbackPageData, func(context.Context) (authui.OAuthBrowserCallback, error), func(context.Context, string) (authui.AuthMethod, error)) (authCallbackPageResult, error)
	pickMethod            func(authInteraction) (authMethodPickerResult, error)
	pickConflict          func(authInteraction) (authConflictPickerResult, error)
	showSuccess           func(authSuccessScreenData) error
	promptReader          *bufio.Reader
}

func newInteractiveAuthInteractor() authInteractor {
	return &interactiveAuthInteractor{
		stdin:       os.Stdin,
		stderr:      os.Stderr,
		lookupEnv:   os.Getenv,
		openBrowser: serverauth.OpenBrowser,
		startCallbackListener: func() (oauthCallbackListener, error) {
			return serverauth.StartOAuthCallbackListener()
		},
		runDeviceFlow:   serverauth.RunOpenAIDeviceCodeFlow,
		runCallbackPage: runAuthCallbackPage,
	}
}

func newHeadlessAuthInteractor() authInteractor {
	return &headlessAuthInteractor{lookupEnv: os.Getenv}
}

func (i *interactiveAuthInteractor) WrapStore(base authui.AuthStore) authui.AuthStore {
	return authservice.WrapStoreWithEnvAPIKeyOverride(base, i.lookupEnv)
}

func (i *headlessAuthInteractor) WrapStore(base authui.AuthStore) authui.AuthStore {
	return authservice.WrapStoreWithEnvAPIKeyOverride(base, i.lookupEnv)
}

func (i *interactiveAuthInteractor) LookupEnv(key string) string {
	if i == nil || i.lookupEnv == nil {
		return os.Getenv(key)
	}
	return i.lookupEnv(key)
}

func (i *headlessAuthInteractor) LookupEnv(key string) string {
	if i == nil || i.lookupEnv == nil {
		return os.Getenv(key)
	}
	return i.lookupEnv(key)
}

func (i *headlessAuthInteractor) NeedsInteraction(req authInteraction) bool {
	return req.AuthRequired && !req.Gate.Ready
}

func (i *interactiveAuthInteractor) NeedsInteraction(req authInteraction) bool {
	if !req.AuthRequired && !req.PromptOptional {
		return authui.NeedsAuthEnvConflictResolution(req)
	}
	if req.Gate.Ready {
		return authui.NeedsAuthEnvConflictResolution(req)
	}
	return req.AuthRequired || !req.State.IsNoAuthSelected() || authui.NeedsAuthEnvConflictResolution(req)
}

func (i *headlessAuthInteractor) Interact(ctx context.Context, req authInteraction) (authui.AuthInteractionOutcome, error) {
	if req.StartupErr != nil {
		return authui.AuthInteractionOutcome{}, req.StartupErr
	}
	return authui.AuthInteractionOutcome{}, serverauth.EnsureStartupReady(serverauth.EmptyState())
}

func (i *interactiveAuthInteractor) Interact(ctx context.Context, req authInteraction) (authui.AuthInteractionOutcome, error) {
	if authui.NeedsAuthEnvConflictResolution(req) {
		return authui.AuthInteractionOutcome{}, i.resolveEnvAPIKeyConflict(ctx, req)
	}
	if req.AuthRequired && req.State.IsNoAuthSelected() {
		return authui.AuthInteractionOutcome{ProceedWithoutAuth: true}, nil
	}

	for {
		choice, err := i.chooseMethod(req)
		if err != nil {
			return authui.AuthInteractionOutcome{}, err
		}
		req.FlowErr = nil

		var method authui.AuthMethod
		switch choice {
		case authMethodChoiceSkip:
			if err := persistSkipAuthSelection(ctx, req); err != nil {
				return authui.AuthInteractionOutcome{}, err
			}
			return authui.AuthInteractionOutcome{ProceedWithoutAuth: true}, nil
		case authMethodChoiceEnvAPIKey:
			if !req.HasEnvAPIKey {
				return authui.AuthInteractionOutcome{}, errEnvAPIKeyUnavailable
			}
			_, err = req.Manager.SetEnvAPIKeyPreference(ctx, authui.EnvAPIKeyPreferencePreferEnv, true)
			if err != nil {
				return authui.AuthInteractionOutcome{}, fmt.Errorf("save env api key preference: %w", err)
			}
			if err := i.showAuthSuccess(ctx, req); err != nil {
				return authui.AuthInteractionOutcome{}, err
			}
			return authui.AuthInteractionOutcome{}, nil
		case authMethodChoiceBrowserAuto:
			method, err = i.authOAuthRunner(req.Theme).BrowserAuto(ctx, req.OAuthOptions)
		case authMethodChoiceBrowserPaste:
			method, err = i.authOAuthRunner(req.Theme).BrowserPaste(ctx, req.OAuthOptions)
		case authMethodChoiceDevice:
			method, err = i.authOAuthRunner(req.Theme).Device(ctx, req.OAuthOptions)
		default:
			return authui.AuthInteractionOutcome{}, fmt.Errorf("%w %q", errUnknownAuthMethod, choice)
		}
		if err != nil {
			req.FlowErr = err
			continue
		}
		preference := req.State.EnvAPIKeyPreference
		setPreference := false
		if req.HasEnvAPIKey && preference == authui.EnvAPIKeyPreferenceUnspecified {
			preference = authui.EnvAPIKeyPreferencePreferSaved
			setPreference = true
		}
		if _, err := req.Manager.SwitchMethodAndSetEnvAPIKeyPreference(ctx, method, preference, setPreference, true); err != nil {
			return authui.AuthInteractionOutcome{}, fmt.Errorf("save auth method: %w", err)
		}
		if err := i.showAuthSuccess(ctx, req); err != nil {
			return authui.AuthInteractionOutcome{}, err
		}
		return authui.AuthInteractionOutcome{}, nil
	}
}

func persistSkipAuthSelection(ctx context.Context, req authInteraction) error {
	if _, err := req.Manager.SwitchMethodAndSetEnvAPIKeyPreference(
		ctx,
		authui.AuthMethod{Type: authui.AuthMethodNone},
		authui.EnvAPIKeyPreferencePreferSaved,
		true,
		true,
	); err != nil {
		return fmt.Errorf("save no-auth preference: %w", err)
	}
	return nil
}

func (i *interactiveAuthInteractor) resolveEnvAPIKeyConflict(ctx context.Context, req authInteraction) error {
	run := i.pickConflict
	if run == nil {
		run = runAuthConflictPicker
	}
	picked, err := run(req)
	if err != nil {
		return err
	}
	if picked.Canceled {
		return ErrAuthCanceledByUser
	}
	preference := authui.EnvAPIKeyPreferencePreferSaved
	if picked.Choice == authConflictChoiceEnvAPIKey {
		preference = authui.EnvAPIKeyPreferencePreferEnv
	}
	if _, err := req.Manager.SetEnvAPIKeyPreference(ctx, preference, true); err != nil {
		return fmt.Errorf("save env api key preference: %w", err)
	}
	return nil
}

func (i *interactiveAuthInteractor) showAuthSuccess(ctx context.Context, req authInteraction) error {
	run := i.showSuccess
	if run == nil {
		run = runAuthSuccessScreen
	}
	state, err := req.Manager.Load(ctx)
	if err != nil {
		return fmt.Errorf("load auth state for success screen: %w", err)
	}
	return run(authSuccessScreenData{
		Theme: req.Theme,
		Title: authui.AuthSuccessTitle(state.Method),
	})
}

func (i *interactiveAuthInteractor) chooseMethod(req authInteraction) (authMethodChoice, error) {
	run := i.pickMethod
	if run == nil {
		run = runAuthMethodPicker
	}
	picked, err := run(req)
	if err != nil {
		return "", err
	}
	if picked.Canceled {
		return "", ErrAuthCanceledByUser
	}
	return picked.Choice, nil
}

func (i *interactiveAuthInteractor) authOAuthRunner(theme string) authui.OAuthRunner {
	runDeviceFlow := i.runDeviceFlow
	if runDeviceFlow == nil {
		runDeviceFlow = serverauth.RunOpenAIDeviceCodeFlow
	}
	openBrowser := i.openBrowser
	if openBrowser == nil {
		openBrowser = serverauth.OpenBrowser
	}
	startListener := i.startCallbackListener
	if startListener == nil {
		startListener = func() (oauthCallbackListener, error) {
			return serverauth.StartOAuthCallbackListener()
		}
	}
	runner := authui.OAuthRunner{
		OpenBrowser: openBrowser,
		StartCallbackListener: func() (authui.OAuthCallbackListener, error) {
			return startListener()
		},
		RunDeviceFlow: func(ctx context.Context, opts authui.OAuthOptions, onCode func(authui.OAuthDeviceCode)) (authui.AuthMethod, error) {
			return runDeviceFlow(ctx, opts, onCode)
		},
		Prompt: func(label string) (string, error) {
			return i.prompt(lipgloss.NewStyle().Foreground(uiPalette(theme).primary).Bold(true).Render(label))
		},
		OAuthPresenter: interactiveAuthOAuthPresenter{interactor: i, theme: theme},
	}
	if i.runCallbackPage != nil {
		runner.BrowserCallbackPage = func(ctx context.Context, opts authui.OAuthOptions, session authui.OAuthBrowserSession, openErr error, listener authui.OAuthCallbackListener, complete authui.OAuthCompleteBrowserFlowFunc) (authui.AuthMethod, error) {
			return i.runAuthBrowserHybridPage(ctx, theme, opts, session, openErr, listener, complete)
		}
	}
	return runner
}
