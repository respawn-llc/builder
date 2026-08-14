package serverapi

import (
	"errors"
	"fmt"
	"strings"
)

var ErrServerAuthRequired = errors.New("server auth is not configured")

// ErrAuthBootstrapOAuthStateRequired is returned by
// AuthCompleteBootstrapRequest.Validate when a browser-callback bootstrap
// request omits the oauth_state binding the callback to the issued flow.
var ErrAuthBootstrapOAuthStateRequired = errors.New("oauth_state is required")

type AuthBootstrapMode string

const (
	AuthBootstrapModeNone                AuthBootstrapMode = "none"
	AuthBootstrapModeBrowserCallbackURL  AuthBootstrapMode = "browser_callback_url"
	AuthBootstrapModeBrowserCallbackCode AuthBootstrapMode = "browser_callback_code"
	AuthBootstrapModeDeviceCode          AuthBootstrapMode = "device_code"
	AuthBootstrapModeAPIKey              AuthBootstrapMode = "api_key"
)

type AuthBootstrapOAuthConfig struct {
	Issuer   string `json:"issuer,omitempty"`
	ClientID string `json:"client_id,omitempty"`
}

type AuthGetBootstrapStatusRequest struct{}

type AuthGetBootstrapStatusResponse struct {
	AuthReady              bool                     `json:"auth_ready"`
	AuthRequired           bool                     `json:"auth_required"`
	NoAuthSelected         bool                     `json:"no_auth_selected,omitempty"`
	AuthBootstrapSupported bool                     `json:"auth_bootstrap_supported"`
	AllowedPreAuthMethods  []string                 `json:"allowed_pre_auth_methods,omitempty"`
	SupportedModes         []AuthBootstrapMode      `json:"supported_modes,omitempty"`
	OAuth                  AuthBootstrapOAuthConfig `json:"oauth,omitempty"`
}

type AuthAcknowledgeNoAuthRequest struct{}

type AuthAcknowledgeNoAuthResponse struct {
	AuthReady      bool `json:"auth_ready"`
	NoAuthSelected bool `json:"no_auth_selected,omitempty"`
}

type AuthCompleteBootstrapRequest struct {
	Mode                    AuthBootstrapMode `json:"mode"`
	Force                   bool              `json:"force,omitempty"`
	APIKey                  string            `json:"api_key,omitempty"`
	CallbackInput           string            `json:"callback_input,omitempty"`
	RedirectURI             string            `json:"redirect_uri,omitempty"`
	OAuthState              string            `json:"oauth_state,omitempty"`
	OAuthCodeVerifier       string            `json:"oauth_code_verifier,omitempty"`
	DeviceAuthorizationCode string            `json:"device_authorization_code,omitempty"`
	DeviceCodeVerifier      string            `json:"device_code_verifier,omitempty"`
}

type AuthCompleteBootstrapResponse struct {
	AuthReady      bool   `json:"auth_ready"`
	NoAuthSelected bool   `json:"no_auth_selected,omitempty"`
	MethodType     string `json:"method_type,omitempty"`
	AccountID      string `json:"account_id,omitempty"`
	Email          string `json:"email,omitempty"`
}

func (r AuthCompleteBootstrapRequest) Validate() error {
	switch r.Mode {
	case AuthBootstrapModeNone:
		return nil
	case AuthBootstrapModeAPIKey:
		if strings.TrimSpace(r.APIKey) == "" {
			return errors.New("api_key is required")
		}
	case AuthBootstrapModeBrowserCallbackURL, AuthBootstrapModeBrowserCallbackCode:
		if strings.TrimSpace(r.CallbackInput) == "" {
			return errors.New("callback_input is required")
		}
		if strings.TrimSpace(r.RedirectURI) == "" {
			return errors.New("redirect_uri is required")
		}
		if strings.TrimSpace(r.OAuthState) == "" {
			return ErrAuthBootstrapOAuthStateRequired
		}
		if strings.TrimSpace(r.OAuthCodeVerifier) == "" {
			return errors.New("oauth_code_verifier is required")
		}
	case AuthBootstrapModeDeviceCode:
		if strings.TrimSpace(r.DeviceAuthorizationCode) == "" {
			return errors.New("device_authorization_code is required")
		}
		if strings.TrimSpace(r.DeviceCodeVerifier) == "" {
			return errors.New("device_code_verifier is required")
		}
	default:
		return fmt.Errorf("mode %q is unsupported", strings.TrimSpace(string(r.Mode)))
	}
	return nil
}
