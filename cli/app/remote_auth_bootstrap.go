package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/cli/app/internal/authui"
	serverauth "core/server/auth"
	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"

	"github.com/charmbracelet/lipgloss"
)

var (
	ErrAuthCanceledByUser = errors.New("auth canceled by user")
	ErrOAuthStateMismatch = errors.New("oauth state mismatch")
)

func ensureRemoteAuthReady(ctx context.Context, remote apicontract.AuthBootstrapService, settings config.Settings, interactor authInteractor, interactive bool) error {
	if remote == nil {
		return errors.New("auth bootstrap client is required")
	}
	status, err := remote.GetAuthBootstrapStatus(ctx, serverapi.AuthGetBootstrapStatusRequest{})
	if err != nil {
		return err
	}
	if status.AuthReady {
		disableRemoteNoAuth(remote)
		return nil
	}
	if interactor == nil {
		return serverapi.ErrServerAuthRequired
	}
	if !status.AuthRequired && !interactive {
		return nil
	}
	if interactive, ok := interactor.(*interactiveAuthInteractor); ok {
		if status.NoAuthSelected {
			return enableRemoteNoAuth(ctx, remote)
		}
		return interactive.completeRemoteAuthBootstrap(ctx, remote, settings, status, false)
	}
	apiKey := strings.TrimSpace(interactor.LookupEnv("OPENAI_API_KEY"))
	if apiKey == "" {
		return serverapi.ErrServerAuthRequired
	}
	resp, err := remote.CompleteAuthBootstrap(ctx, serverapi.AuthCompleteBootstrapRequest{
		Mode:   serverapi.AuthBootstrapModeAPIKey,
		APIKey: apiKey,
	})
	if err != nil {
		return err
	}
	if !resp.AuthReady {
		return serverapi.ErrServerAuthRequired
	}
	disableRemoteNoAuth(remote)
	return nil
}

func (i *interactiveAuthInteractor) completeRemoteAuthBootstrap(ctx context.Context, remote apicontract.AuthBootstrapService, settings config.Settings, status serverapi.AuthGetBootstrapStatusResponse, force bool) error {
	if i == nil {
		return errors.New("interactive auth interactor is required")
	}
	req := authInteraction{
		Theme:          string(settings.Theme),
		AuthRequired:   status.AuthRequired,
		PromptOptional: !status.AuthRequired,
		HasEnvAPIKey:   strings.TrimSpace(i.LookupEnv("OPENAI_API_KEY")) != "",
	}
	for {
		choice, err := i.chooseMethod(req)
		if err != nil {
			return err
		}
		completeReq, err := i.collectRemoteBootstrapRequest(ctx, req.Theme, choice, status)
		if err != nil {
			req.FlowErr = err
			continue
		}
		completeReq.Force = force
		resp, err := remote.CompleteAuthBootstrap(ctx, completeReq)
		if err != nil {
			req.FlowErr = err
			continue
		}
		if completeReq.Mode == serverapi.AuthBootstrapModeNone && resp.NoAuthSelected {
			if err := enableRemoteNoAuth(ctx, remote); err != nil {
				return err
			}
			i.printAuthSection(req.Theme, "Server Auth Skipped", []string{lipgloss.NewStyle().Foreground(uiPalette(req.Theme).muted).Faint(true).Render("Kent will proceed without configured server auth.")})
			return nil
		}
		if !resp.AuthReady {
			req.FlowErr = serverapi.ErrServerAuthRequired
			continue
		}
		disableRemoteNoAuth(remote)
		i.printAuthSection(req.Theme, "Server Auth Ready", []string{lipgloss.NewStyle().Foreground(uiPalette(req.Theme).muted).Faint(true).Render("Kent configured auth on the server.")})
		return nil
	}
}

type remoteNoAuthAcknowledgementEnabler interface {
	EnableNoAuthBootstrapAcknowledgement(context.Context) error
}

type remoteNoAuthAcknowledgementDisabler interface {
	DisableNoAuthBootstrapAcknowledgement()
}

func enableRemoteNoAuth(ctx context.Context, remote apicontract.AuthBootstrapService) error {
	if enabler, ok := remote.(remoteNoAuthAcknowledgementEnabler); ok {
		return enabler.EnableNoAuthBootstrapAcknowledgement(ctx)
	}
	resp, err := remote.AcknowledgeNoAuth(ctx, serverapi.AuthAcknowledgeNoAuthRequest{})
	if err != nil {
		return err
	}
	if resp.NoAuthSelected || resp.AuthReady {
		return nil
	}
	return serverapi.ErrServerAuthRequired
}

func disableRemoteNoAuth(remote apicontract.AuthBootstrapService) {
	if disabler, ok := remote.(remoteNoAuthAcknowledgementDisabler); ok {
		disabler.DisableNoAuthBootstrapAcknowledgement()
	}
}

func (i *interactiveAuthInteractor) collectRemoteBootstrapRequest(ctx context.Context, theme string, choice authMethodChoice, status serverapi.AuthGetBootstrapStatusResponse) (serverapi.AuthCompleteBootstrapRequest, error) {
	if !supportsBootstrapMode(status.SupportedModes, choice) {
		return serverapi.AuthCompleteBootstrapRequest{}, fmt.Errorf("auth method %q is not supported by this server", choice)
	}
	oauthOpts := authui.OAuthOptions{Issuer: status.OAuth.Issuer, ClientID: status.OAuth.ClientID}
	switch choice {
	case authMethodChoiceSkip:
		return serverapi.AuthCompleteBootstrapRequest{Mode: serverapi.AuthBootstrapModeNone}, nil
	case authMethodChoiceEnvAPIKey:
		apiKey := strings.TrimSpace(i.LookupEnv("OPENAI_API_KEY"))
		if apiKey == "" {
			return serverapi.AuthCompleteBootstrapRequest{}, errors.New("OPENAI_API_KEY is not available")
		}
		return serverapi.AuthCompleteBootstrapRequest{Mode: serverapi.AuthBootstrapModeAPIKey, APIKey: apiKey}, nil
	case authMethodChoiceBrowserAuto:
		return i.collectRemoteBrowserAuto(ctx, oauthOpts, theme)
	case authMethodChoiceDevice:
		return i.collectRemoteDevice(ctx, oauthOpts, theme)
	default:
		return serverapi.AuthCompleteBootstrapRequest{}, fmt.Errorf("unsupported auth method %q", choice)
	}
}

func (i *interactiveAuthInteractor) collectRemoteBrowserAuto(ctx context.Context, opts authui.OAuthOptions, theme string) (serverapi.AuthCompleteBootstrapRequest, error) {
	startListener := i.startCallbackListener
	if startListener == nil {
		startListener = func() (oauthCallbackListener, error) {
			return serverauth.StartOAuthCallbackListener()
		}
	}
	openBrowser := i.openBrowser
	if openBrowser == nil {
		openBrowser = serverauth.OpenBrowser
	}
	listener, err := startListener()
	if err != nil {
		return serverapi.AuthCompleteBootstrapRequest{}, err
	}
	defer func() { _ = listener.Close() }()
	session, err := serverauth.BeginOpenAIBrowserFlow(opts, listener.RedirectURI())
	if err != nil {
		return serverapi.AuthCompleteBootstrapRequest{}, err
	}
	openErr := openBrowser(session.AuthorizeURL)
	runPage := i.runCallbackPage
	if runPage == nil {
		runPage = runAuthCallbackPage
	}
	result, err := runPage(ctx, authCallbackPageData{
		Theme:        theme,
		AuthorizeURL: session.AuthorizeURL,
		OpenErr:      openErr,
	}, func(waitCtx context.Context) (authui.OAuthBrowserCallback, error) {
		return listener.Wait(waitCtx, opts.PollTimeout)
	}, func(_ context.Context, input string) (authui.AuthMethod, error) {
		parsed, err := serverauth.ParseOAuthCallbackInput(input)
		if err != nil {
			return authui.AuthMethod{}, err
		}
		sessionState := strings.TrimSpace(session.State)
		parsedState := strings.TrimSpace(parsed.State)
		if sessionState != "" && parsedState != "" && parsedState != sessionState {
			return authui.AuthMethod{}, ErrOAuthStateMismatch
		}
		if strings.TrimSpace(parsed.Code) == "" {
			return authui.AuthMethod{}, errors.New("oauth callback is missing code")
		}
		return authui.AuthMethod{Type: "oauth"}, nil
	})
	if err != nil {
		return serverapi.AuthCompleteBootstrapRequest{}, err
	}
	if result.Canceled {
		return serverapi.AuthCompleteBootstrapRequest{}, ErrAuthCanceledByUser
	}
	if result.Err != nil {
		return serverapi.AuthCompleteBootstrapRequest{}, result.Err
	}
	return serverapi.AuthCompleteBootstrapRequest{
		Mode:              serverapi.AuthBootstrapModeBrowserCallbackURL,
		CallbackInput:     result.CallbackInput,
		RedirectURI:       session.RedirectURI,
		OAuthState:        session.State,
		OAuthCodeVerifier: session.CodeVerifier,
	}, nil
}

func (i *interactiveAuthInteractor) collectRemoteDevice(ctx context.Context, opts authui.OAuthOptions, theme string) (serverapi.AuthCompleteBootstrapRequest, error) {
	grant, err := serverauth.CollectOpenAIDeviceAuthorizationGrant(ctx, opts, func(code authui.OAuthDeviceCode) {
		i.printAuthSection(theme, authMethodDisplayTitle(authMethodChoiceDevice), []string{
			lipgloss.NewStyle().Foreground(uiPalette(theme).primary).Underline(true).Render(code.VerificationURL),
			lipgloss.NewStyle().Foreground(uiPalette(theme).foreground).Render("Code: ") + lipgloss.NewStyle().Foreground(uiPalette(theme).secondary).Bold(true).Render(code.UserCode),
			lipgloss.NewStyle().Foreground(uiPalette(theme).muted).Faint(true).Render("Waiting for authorization..."),
		})
	})
	if err != nil {
		return serverapi.AuthCompleteBootstrapRequest{}, err
	}
	return serverapi.AuthCompleteBootstrapRequest{
		Mode:                    serverapi.AuthBootstrapModeDeviceCode,
		DeviceAuthorizationCode: grant.AuthorizationCode,
		DeviceCodeVerifier:      grant.CodeVerifier,
	}, nil
}

func supportsBootstrapMode(modes []serverapi.AuthBootstrapMode, choice authMethodChoice) bool {
	need := serverapi.AuthBootstrapMode("")
	switch choice {
	case authMethodChoiceSkip:
		need = serverapi.AuthBootstrapModeNone
	case authMethodChoiceEnvAPIKey:
		need = serverapi.AuthBootstrapModeAPIKey
	case authMethodChoiceBrowserAuto:
		need = serverapi.AuthBootstrapModeBrowserCallbackURL
	case authMethodChoiceBrowserPaste:
		need = serverapi.AuthBootstrapModeBrowserCallbackCode
	case authMethodChoiceDevice:
		need = serverapi.AuthBootstrapModeDeviceCode
	default:
		return false
	}
	for _, mode := range modes {
		if mode == need {
			return true
		}
	}
	return false
}
