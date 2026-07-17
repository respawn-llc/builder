package auth

import sharedauth "core/shared/auth"

var (
	ErrAuthNotConfigured     = sharedauth.ErrAuthNotConfigured
	ErrInvalidAuthMethod     = sharedauth.ErrInvalidAuthMethod
	ErrSwitchRequiresIdle    = sharedauth.ErrSwitchRequiresIdle
	ErrOAuthRefreshFailed    = sharedauth.ErrOAuthRefreshFailed
	ErrInvalidAuthScope      = sharedauth.ErrInvalidAuthScope
	ErrDeviceCodeUnsupported = sharedauth.ErrDeviceCodeUnsupported
)

type Scope = sharedauth.Scope

const (
	ScopeGlobal = sharedauth.ScopeGlobal
)

type MethodType = sharedauth.MethodType

const (
	MethodNone   = sharedauth.MethodNone
	MethodAPIKey = sharedauth.MethodAPIKey
	MethodOAuth  = sharedauth.MethodOAuth
)

type EnvAPIKeyPreference = sharedauth.EnvAPIKeyPreference

const (
	EnvAPIKeyPreferenceUnspecified = sharedauth.EnvAPIKeyPreferenceUnspecified
	EnvAPIKeyPreferencePreferSaved = sharedauth.EnvAPIKeyPreferencePreferSaved
	EnvAPIKeyPreferencePreferEnv   = sharedauth.EnvAPIKeyPreferencePreferEnv
)

type State = sharedauth.State
type Method = sharedauth.Method
type APIKeyMethod = sharedauth.APIKeyMethod
type OAuthMethod = sharedauth.OAuthMethod
type StartupGate = sharedauth.StartupGate

func EmptyState() State {
	return sharedauth.EmptyState()
}

func MaskedAPIKeySummary(apiKey *APIKeyMethod) string {
	return sharedauth.MaskedAPIKeySummary(apiKey)
}

func EvaluateStartupGate(state State) StartupGate {
	return sharedauth.EvaluateStartupGate(state)
}

func EnsureStartupReady(state State) error {
	return sharedauth.EnsureStartupReady(state)
}
