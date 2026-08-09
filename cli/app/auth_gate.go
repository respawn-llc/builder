package app

import (
	"context"
	"io"
	"os"
	"time"

	"core/cli/app/internal/authui"
	serverauth "core/server/auth"
)

type authInteraction struct {
	Theme        string
	FlowErr      error
	HasEnvAPIKey bool
}

type authInteractor interface {
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
	stderr                io.Writer
	lookupEnv             func(string) string
	openBrowser           func(string) error
	startCallbackListener func() (oauthCallbackListener, error)
	runCallbackPage       func(context.Context, authCallbackPageData, func(context.Context) (authui.OAuthBrowserCallback, error), func(context.Context, string) (authui.AuthMethod, error)) (authCallbackPageResult, error)
	pickMethod            func(authInteraction) (authMethodPickerResult, error)
}

func newInteractiveAuthInteractor() authInteractor {
	return &interactiveAuthInteractor{
		stderr:      os.Stderr,
		lookupEnv:   os.Getenv,
		openBrowser: serverauth.OpenBrowser,
		startCallbackListener: func() (oauthCallbackListener, error) {
			return serverauth.StartOAuthCallbackListener()
		},
		runCallbackPage: runAuthCallbackPage,
	}
}

func newHeadlessAuthInteractor() authInteractor {
	return &headlessAuthInteractor{lookupEnv: os.Getenv}
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
