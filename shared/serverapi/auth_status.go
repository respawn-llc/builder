package serverapi

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type AuthStatusRequest struct {
	Provider *AuthProviderFacts `json:"provider,omitempty"`
}

type AuthStatusResponse struct {
	Resolution   AuthStatusResolution  `json:"resolution"`
	Subscription AuthSubscriptionFacts `json:"subscription"`
}

type AuthStatusResolutionKind string

const (
	AuthStatusResolutionKnown       AuthStatusResolutionKind = "known"
	AuthStatusResolutionUnavailable AuthStatusResolutionKind = "unavailable"
)

type AuthStatusResolution struct {
	Kind    AuthStatusResolutionKind `json:"kind"`
	Facts   *AuthStatusFacts         `json:"facts,omitempty"`
	Failure *AuthStatusFailure       `json:"failure,omitempty"`
}

type AuthStatusFailure struct {
	Cause string `json:"cause"`
}

type AuthStatusMethod string

const (
	AuthStatusMethodNone   AuthStatusMethod = "none"
	AuthStatusMethodAPIKey AuthStatusMethod = "api_key"
	AuthStatusMethodOAuth  AuthStatusMethod = "oauth"
)

type AuthStatusEnvPreference string

const (
	AuthStatusEnvPreferenceUnspecified AuthStatusEnvPreference = "unspecified"
	AuthStatusEnvPreferencePreferSaved AuthStatusEnvPreference = "prefer_saved_auth"
	AuthStatusEnvPreferencePreferEnv   AuthStatusEnvPreference = "prefer_env_api_key"
)

type AuthStatusFacts struct {
	Method        AuthStatusMethod        `json:"method"`
	Provider      AuthProviderFacts       `json:"provider"`
	EnvPreference AuthStatusEnvPreference `json:"env_preference"`
	OAuth         *AuthOAuthFacts         `json:"oauth,omitempty"`
	APIKey        *AuthAPIKeyFacts        `json:"api_key,omitempty"`
}

type AuthOAuthFacts struct {
	AccountID *string `json:"account_id,omitempty"`
	Email     *string `json:"email,omitempty"`
}

type AuthAPIKeyFacts struct {
	Suffix *string `json:"suffix,omitempty"`
}

type AuthProviderKind string

const (
	AuthProviderKindOpenAI             AuthProviderKind = "openai"
	AuthProviderKindOpenAICompatible   AuthProviderKind = "openai_compatible"
	AuthProviderKindConfiguredProvider AuthProviderKind = "configured_provider"
)

type AuthProviderFacts struct {
	Kind          AuthProviderKind           `json:"kind"`
	Identifier    string                     `json:"identifier"`
	DisplayOrigin *AuthProviderDisplayOrigin `json:"display_origin,omitempty"`
}

type AuthProviderDisplayOrigin struct {
	Scheme   string  `json:"scheme"`
	Hostname string  `json:"hostname"`
	Port     *string `json:"port,omitempty"`
}

type AuthSubscriptionFacts struct {
	Applicable bool                          `json:"applicable"`
	Plan       *string                       `json:"plan,omitempty"`
	Windows    []AuthSubscriptionWindowFacts `json:"windows,omitempty"`
	Failure    *AuthStatusFailure            `json:"failure,omitempty"`
}

type AuthSubscriptionWindowBucket string

const (
	AuthSubscriptionWindowBucketDefault    AuthSubscriptionWindowBucket = "default"
	AuthSubscriptionWindowBucketAdditional AuthSubscriptionWindowBucket = "additional"
)

type AuthSubscriptionWindowFacts struct {
	Bucket         AuthSubscriptionWindowBucket `json:"bucket"`
	DurationSecs   int                          `json:"duration_seconds"`
	UsedPercent    float64                      `json:"used_percent"`
	ResetAt        *time.Time                   `json:"reset_at,omitempty"`
	LimitName      *string                      `json:"limit_name,omitempty"`
	MeteredFeature *string                      `json:"metered_feature,omitempty"`
}

func KnownAuthStatusResolution(facts AuthStatusFacts, failure *AuthStatusFailure) AuthStatusResolution {
	return AuthStatusResolution{
		Kind:    AuthStatusResolutionKnown,
		Facts:   &facts,
		Failure: failure,
	}
}

func UnavailableAuthStatusResolution(failure AuthStatusFailure) AuthStatusResolution {
	return AuthStatusResolution{
		Kind:    AuthStatusResolutionUnavailable,
		Failure: &failure,
	}
}

func OpenAIAuthProviderFacts() AuthProviderFacts {
	return AuthProviderFacts{
		Kind:       AuthProviderKindOpenAI,
		Identifier: "openai",
	}
}

func (r AuthStatusRequest) Validate() error {
	if r.Provider == nil {
		return nil
	}
	if err := r.Provider.validate(); err != nil {
		return fmt.Errorf("auth status provider: %w", err)
	}
	return nil
}

func (r AuthStatusResponse) Validate() error {
	if err := r.Resolution.validate(); err != nil {
		return fmt.Errorf("auth resolution: %w", err)
	}
	if err := r.Subscription.validate(); err != nil {
		return fmt.Errorf("auth subscription: %w", err)
	}
	if r.Resolution.Kind == AuthStatusResolutionUnavailable && r.Subscription.Applicable {
		return errors.New("subscription cannot be applicable when auth resolution is unavailable")
	}
	return nil
}

func (r AuthStatusResolution) validate() error {
	switch r.Kind {
	case AuthStatusResolutionKnown:
		if r.Facts == nil {
			return errors.New("known resolution requires facts")
		}
		if err := r.Facts.validate(); err != nil {
			return err
		}
	case AuthStatusResolutionUnavailable:
		if r.Facts != nil || r.Failure == nil {
			return errors.New("unavailable resolution requires only failure")
		}
	default:
		return fmt.Errorf("kind %q is invalid", r.Kind)
	}
	if r.Failure == nil {
		return nil
	}
	return r.Failure.validate()
}

func (f AuthStatusFacts) validate() error {
	if err := f.Provider.validate(); err != nil {
		return fmt.Errorf("provider: %w", err)
	}
	switch f.EnvPreference {
	case AuthStatusEnvPreferenceUnspecified,
		AuthStatusEnvPreferencePreferSaved,
		AuthStatusEnvPreferencePreferEnv:
	default:
		return fmt.Errorf("env preference %q is invalid", f.EnvPreference)
	}
	switch f.Method {
	case AuthStatusMethodNone:
		if f.OAuth == nil && f.APIKey == nil {
			return nil
		}
	case AuthStatusMethodOAuth:
		if f.OAuth != nil && f.APIKey == nil {
			return f.OAuth.validate()
		}
	case AuthStatusMethodAPIKey:
		if f.APIKey != nil && f.OAuth == nil {
			return f.APIKey.validate()
		}
	default:
		return fmt.Errorf("method %q is invalid", f.Method)
	}
	return fmt.Errorf("method %q payload is invalid", f.Method)
}

func (f AuthOAuthFacts) validate() error {
	return errors.Join(
		validateOptionalAuthString("account_id", f.AccountID),
		validateOptionalAuthString("email", f.Email),
	)
}

func (f AuthAPIKeyFacts) validate() error {
	if f.Suffix == nil {
		return nil
	}
	if err := validateOptionalAuthString("suffix", f.Suffix); err != nil {
		return err
	}
	if utf8.RuneCountInString(*f.Suffix) != 4 {
		return errors.New("api-key suffix must contain exactly four runes")
	}
	return nil
}

func (f AuthProviderFacts) validate() error {
	switch f.Kind {
	case AuthProviderKindOpenAI:
		if f.Identifier == "openai" && f.DisplayOrigin == nil {
			return nil
		}
	case AuthProviderKindOpenAICompatible:
		if f.Identifier == "openai-compatible" {
			if f.DisplayOrigin == nil {
				return nil
			}
			return f.DisplayOrigin.Validate()
		}
	case AuthProviderKindConfiguredProvider:
		if strings.TrimSpace(f.Identifier) != "" && f.DisplayOrigin == nil {
			return nil
		}
	}
	return fmt.Errorf("provider kind %q identifier %q has invalid facts", f.Kind, f.Identifier)
}

func (o AuthProviderDisplayOrigin) Validate() error {
	if o.Scheme != "http" && o.Scheme != "https" {
		return fmt.Errorf("display origin scheme %q is invalid", o.Scheme)
	}
	if strings.TrimSpace(o.Hostname) == "" || o.Hostname != strings.TrimSpace(o.Hostname) {
		return errors.New("display origin hostname is required")
	}
	address, addressErr := netip.ParseAddr(o.Hostname)
	if addressErr == nil && !validDisplayOriginZone(address.Zone()) {
		return errors.New("display origin hostname contains invalid IP zone syntax")
	}
	if addressErr != nil {
		parsed, err := url.Parse(o.Scheme + "://" + o.Hostname)
		if err != nil ||
			parsed.Scheme != o.Scheme ||
			parsed.User != nil ||
			parsed.Host != o.Hostname ||
			parsed.Hostname() != o.Hostname ||
			parsed.Port() != "" ||
			parsed.Path != "" ||
			parsed.RawQuery != "" ||
			parsed.Fragment != "" ||
			parsed.Opaque != "" {
			return errors.New("display origin hostname contains URL syntax")
		}
	}
	if o.Port == nil {
		return nil
	}
	if err := validateOptionalAuthString("display origin port", o.Port); err != nil {
		return err
	}
	port, err := strconv.Atoi(*o.Port)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("display origin port %q is invalid", *o.Port)
	}
	return nil
}

func validDisplayOriginZone(zone string) bool {
	for index := range len(zone) {
		character := zone[index]
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '-' ||
			character == '.' ||
			character == '_' ||
			character == '~' {
			continue
		}
		return false
	}
	return true
}

func (s AuthSubscriptionFacts) validate() error {
	if !s.Applicable {
		if s.Plan != nil || len(s.Windows) != 0 || s.Failure != nil {
			return errors.New("non-applicable subscription cannot contain facts")
		}
		return nil
	}
	if err := validateOptionalAuthString("plan", s.Plan); err != nil {
		return err
	}
	if s.Failure != nil {
		if s.Plan != nil || len(s.Windows) != 0 {
			return errors.New("failed subscription cannot contain plan or windows")
		}
		return s.Failure.validate()
	}
	for index, window := range s.Windows {
		if err := window.validate(); err != nil {
			return fmt.Errorf("window %d: %w", index, err)
		}
	}
	return nil
}

func (w AuthSubscriptionWindowFacts) validate() error {
	if w.Bucket != AuthSubscriptionWindowBucketDefault && w.Bucket != AuthSubscriptionWindowBucketAdditional {
		return fmt.Errorf("bucket %q is invalid", w.Bucket)
	}
	if w.Bucket == AuthSubscriptionWindowBucketDefault && (w.LimitName != nil || w.MeteredFeature != nil) {
		return errors.New("default window cannot contain additional-bucket identifiers")
	}
	if w.DurationSecs <= 0 {
		return errors.New("duration_seconds must be positive")
	}
	if math.IsNaN(w.UsedPercent) || math.IsInf(w.UsedPercent, 0) {
		return errors.New("used_percent must be finite")
	}
	if w.ResetAt != nil && w.ResetAt.IsZero() {
		return errors.New("reset_at cannot be zero")
	}
	return errors.Join(
		validateOptionalAuthString("limit_name", w.LimitName),
		validateOptionalAuthString("metered_feature", w.MeteredFeature),
	)
}

func (f AuthStatusFailure) validate() error {
	if strings.TrimSpace(f.Cause) == "" {
		return errors.New("failure cause is required")
	}
	return nil
}

func validateOptionalAuthString(label string, value *string) error {
	if value != nil && (strings.TrimSpace(*value) == "" || *value != strings.TrimSpace(*value)) {
		return fmt.Errorf("%s must be nonblank and trimmed", label)
	}
	return nil
}
