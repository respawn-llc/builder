package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrAuthNotConfigured     = errors.New("auth is not configured")
	ErrInvalidAuthMethod     = errors.New("invalid auth method")
	ErrSwitchRequiresIdle    = errors.New("auth method switch requires idle session")
	ErrOAuthRefreshFailed    = errors.New("oauth token refresh failed")
	ErrInvalidAuthScope      = errors.New("invalid auth scope")
	ErrDeviceCodeUnsupported = errors.New("device code login is not enabled")
)

type Scope string

const (
	ScopeGlobal Scope = "global"
)

type MethodType string

const (
	MethodNone   MethodType = ""
	MethodAPIKey MethodType = "api_key"
	MethodOAuth  MethodType = "oauth"
)

type EnvAPIKeyPreference string

const (
	EnvAPIKeyPreferenceUnspecified EnvAPIKeyPreference = ""
	EnvAPIKeyPreferencePreferSaved EnvAPIKeyPreference = "prefer_saved_auth"
	EnvAPIKeyPreferencePreferEnv   EnvAPIKeyPreference = "prefer_env_api_key"
)

type State struct {
	Scope               Scope               `json:"scope"`
	Method              Method              `json:"method"`
	EnvAPIKeyPreference EnvAPIKeyPreference `json:"env_api_key_preference,omitempty"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

func EmptyState() State {
	return State{Scope: ScopeGlobal}
}

func (s State) IsConfigured() bool {
	return s.Method.Type != MethodNone
}

func (s State) IsNoAuthSelected() bool {
	return s.Method.Type == MethodNone && s.EnvAPIKeyPreference == EnvAPIKeyPreferencePreferSaved
}

func (s State) Validate() error {
	if s.Scope == "" {
		return fmt.Errorf("%w: empty", ErrInvalidAuthScope)
	}
	if s.Scope != ScopeGlobal {
		return fmt.Errorf("%w: %q", ErrInvalidAuthScope, s.Scope)
	}
	if err := s.EnvAPIKeyPreference.Validate(); err != nil {
		return err
	}
	if s.Method.Type == MethodNone {
		return nil
	}
	if err := s.Method.Validate(); err != nil {
		return err
	}
	return nil
}

func (p EnvAPIKeyPreference) Validate() error {
	switch p {
	case EnvAPIKeyPreferenceUnspecified, EnvAPIKeyPreferencePreferSaved, EnvAPIKeyPreferencePreferEnv:
		return nil
	default:
		return fmt.Errorf("invalid env api key preference: %q", p)
	}
}

type Method struct {
	Type   MethodType    `json:"type"`
	APIKey *APIKeyMethod `json:"api_key,omitempty"`
	OAuth  *OAuthMethod  `json:"oauth,omitempty"`
}

type APIKeyMethod struct {
	Key string `json:"key"`
}

func MaskedAPIKeySummary(apiKey *APIKeyMethod) string {
	key := ""
	if apiKey != nil {
		key = strings.TrimSpace(apiKey.Key)
	}
	if key == "" {
		return "API Key"
	}
	runes := []rune(key)
	start := len(runes) - 4
	if start < 0 {
		start = 0
	}
	return "API Key ..." + string(runes[start:])
}

type OAuthMethod struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Expiry       time.Time `json:"expiry"`
	AccountID    string    `json:"account_id,omitempty"`
	Email        string    `json:"email,omitempty"`
}

func (m Method) Validate() error {
	switch m.Type {
	case MethodAPIKey:
		if m.APIKey == nil {
			return fmt.Errorf("%w: api key payload is missing", ErrInvalidAuthMethod)
		}
		if strings.TrimSpace(m.APIKey.Key) == "" {
			return fmt.Errorf("%w: api key is empty", ErrInvalidAuthMethod)
		}
		if m.OAuth != nil {
			return fmt.Errorf("%w: unexpected oauth payload for api key", ErrInvalidAuthMethod)
		}
	case MethodOAuth:
		if m.OAuth == nil {
			return fmt.Errorf("%w: oauth payload is missing", ErrInvalidAuthMethod)
		}
		if strings.TrimSpace(m.OAuth.AccessToken) == "" {
			return fmt.Errorf("%w: oauth access token is empty", ErrInvalidAuthMethod)
		}
		if m.APIKey != nil {
			return fmt.Errorf("%w: unexpected api key payload for oauth", ErrInvalidAuthMethod)
		}
	case MethodNone:
		if m.APIKey != nil || m.OAuth != nil {
			return fmt.Errorf("%w: credentials present for unset type", ErrInvalidAuthMethod)
		}
	default:
		return fmt.Errorf("%w: unknown type %q", ErrInvalidAuthMethod, m.Type)
	}
	return nil
}

func (m Method) AuthHeaderValue() (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	var token string
	switch m.Type {
	case MethodAPIKey:
		token = m.APIKey.Key
	case MethodOAuth:
		token = m.OAuth.AccessToken
	default:
		return "", ErrAuthNotConfigured
	}
	return "Bearer " + token, nil
}

type StartupGate struct {
	Ready  bool
	Reason string
}

func EvaluateStartupGate(state State) StartupGate {
	if err := state.Validate(); err != nil {
		return StartupGate{Ready: false, Reason: err.Error()}
	}
	if !state.IsConfigured() {
		return StartupGate{Ready: false, Reason: ErrAuthNotConfigured.Error()}
	}
	if _, err := state.Method.AuthHeaderValue(); err != nil {
		return StartupGate{Ready: false, Reason: err.Error()}
	}
	return StartupGate{Ready: true}
}

func EnsureStartupReady(state State) error {
	gate := EvaluateStartupGate(state)
	if gate.Ready {
		return nil
	}
	if gate.Reason == ErrAuthNotConfigured.Error() {
		return ErrAuthNotConfigured
	}
	return fmt.Errorf("startup auth gate: %s", gate.Reason)
}

func EnsureIdleForMethodSwitch(isIdle bool) error {
	if isIdle {
		return nil
	}
	return ErrSwitchRequiresIdle
}
