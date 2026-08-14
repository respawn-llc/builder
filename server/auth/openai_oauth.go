package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"core/server/httpcompression"
)

const (
	DefaultOpenAIIssuer   = "https://auth.openai.com"
	DefaultOpenAIClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	defaultPollTimeout    = 15 * time.Minute
	defaultUserAgent      = "kent"
)

type OpenAIOAuthOptions struct {
	Issuer      string
	ClientID    string
	HTTPClient  *http.Client
	PollTimeout time.Duration
}

type DeviceCode struct {
	VerificationURL string
	UserCode        string
	DeviceAuthID    string
	PollInterval    time.Duration
}

type DeviceAuthorizationGrant struct {
	AuthorizationCode string
	CodeVerifier      string
}

type deviceUserCodeResponse struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
	Interval     any    `json:"interval"`
}

type deviceTokenPollResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeChallenge     string `json:"code_challenge"`
	CodeVerifier      string `json:"code_verifier"`
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	IDToken      string `json:"id_token"`
}

type idTokenClaims struct {
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	Email            string `json:"email"`
	Organizations    []struct {
		ID string `json:"id"`
	} `json:"organizations"`
	OpenAIAuth struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

func normalizeOpenAIOAuthOptions(opts OpenAIOAuthOptions) OpenAIOAuthOptions {
	if strings.TrimSpace(opts.Issuer) == "" {
		opts.Issuer = DefaultOpenAIIssuer
	}
	if strings.TrimSpace(opts.ClientID) == "" {
		opts.ClientID = DefaultOpenAIClientID
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = httpcompression.NewClient(&http.Client{Timeout: 30 * time.Second})
	}
	if opts.PollTimeout <= 0 {
		opts.PollTimeout = defaultPollTimeout
	}
	return opts
}

func RunOpenAIDeviceCodeFlow(ctx context.Context, opts OpenAIOAuthOptions, onCode func(DeviceCode)) (Method, error) {
	opts = normalizeOpenAIOAuthOptions(opts)

	code, err := requestOpenAIDeviceCode(ctx, opts)
	if err != nil {
		return Method{}, err
	}
	if onCode != nil {
		onCode(code)
	}

	poll, err := pollOpenAIDeviceAuthToken(ctx, opts, code)
	if err != nil {
		return Method{}, err
	}

	method, err := exchangeOpenAIAuthorizationCode(ctx, opts, poll.AuthorizationCode, poll.CodeVerifier, issuerRedirectURI(opts))
	if err != nil {
		return Method{}, err
	}
	return method, nil
}

func CollectOpenAIDeviceAuthorizationGrant(ctx context.Context, opts OpenAIOAuthOptions, onCode func(DeviceCode)) (DeviceAuthorizationGrant, error) {
	opts = normalizeOpenAIOAuthOptions(opts)
	code, err := requestOpenAIDeviceCode(ctx, opts)
	if err != nil {
		return DeviceAuthorizationGrant{}, err
	}
	if onCode != nil {
		onCode(code)
	}
	poll, err := pollOpenAIDeviceAuthToken(ctx, opts, code)
	if err != nil {
		return DeviceAuthorizationGrant{}, err
	}
	return DeviceAuthorizationGrant{
		AuthorizationCode: poll.AuthorizationCode,
		CodeVerifier:      poll.CodeVerifier,
	}, nil
}

func CompleteOpenAIDeviceAuthorizationGrant(ctx context.Context, opts OpenAIOAuthOptions, authorizationCode string, codeVerifier string) (Method, error) {
	opts = normalizeOpenAIOAuthOptions(opts)
	authorizationCode = strings.TrimSpace(authorizationCode)
	codeVerifier = strings.TrimSpace(codeVerifier)
	if authorizationCode == "" {
		return Method{}, errors.New("device authorization code is required")
	}
	if codeVerifier == "" {
		return Method{}, errors.New("device code verifier is required")
	}
	return exchangeOpenAIAuthorizationCode(ctx, opts, authorizationCode, codeVerifier, issuerRedirectURI(opts))
}

func requestOpenAIDeviceCode(ctx context.Context, opts OpenAIOAuthOptions) (DeviceCode, error) {
	issuer := strings.TrimSuffix(opts.Issuer, "/")
	endpoint := issuer + "/api/accounts/deviceauth/usercode"
	body, _ := json.Marshal(map[string]string{"client_id": opts.ClientID})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return DeviceCode{}, fmt.Errorf("create device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", defaultUserAgent)

	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return DeviceCode{}, fmt.Errorf("request device code: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return DeviceCode{}, fmt.Errorf("read device code response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return DeviceCode{}, ErrDeviceCodeUnsupported
	}
	if resp.StatusCode/100 != 2 {
		return DeviceCode{}, fmt.Errorf("device code request failed: status %d", resp.StatusCode)
	}

	var parsed deviceUserCodeResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return DeviceCode{}, fmt.Errorf("decode device code response: %w", err)
	}
	if parsed.DeviceAuthID == "" || parsed.UserCode == "" {
		return DeviceCode{}, errors.New("device code response missing required fields")
	}

	intervalSeconds, err := parsePollInterval(parsed.Interval)
	if err != nil {
		return DeviceCode{}, err
	}
	if intervalSeconds <= 0 {
		intervalSeconds = 5
	}

	return DeviceCode{
		VerificationURL: issuer + "/codex/device",
		UserCode:        parsed.UserCode,
		DeviceAuthID:    parsed.DeviceAuthID,
		PollInterval:    time.Duration(intervalSeconds) * time.Second,
	}, nil
}

func pollOpenAIDeviceAuthToken(ctx context.Context, opts OpenAIOAuthOptions, code DeviceCode) (deviceTokenPollResponse, error) {
	issuer := strings.TrimSuffix(opts.Issuer, "/")
	endpoint := issuer + "/api/accounts/deviceauth/token"
	deadline := time.Now().Add(opts.PollTimeout)

	for {
		if time.Now().After(deadline) {
			return deviceTokenPollResponse{}, errors.New("device auth timed out")
		}

		payload, _ := json.Marshal(map[string]string{
			"device_auth_id": code.DeviceAuthID,
			"user_code":      code.UserCode,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return deviceTokenPollResponse{}, fmt.Errorf("create token poll request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", defaultUserAgent)

		resp, err := opts.HTTPClient.Do(req)
		if err != nil {
			return deviceTokenPollResponse{}, fmt.Errorf("poll device auth token: %w", err)
		}
		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return deviceTokenPollResponse{}, fmt.Errorf("read token poll response: %w", readErr)
		}

		if resp.StatusCode/100 == 2 {
			var parsed deviceTokenPollResponse
			if err := json.Unmarshal(respBody, &parsed); err != nil {
				return deviceTokenPollResponse{}, fmt.Errorf("decode token poll response: %w", err)
			}
			if parsed.AuthorizationCode == "" || parsed.CodeVerifier == "" {
				return deviceTokenPollResponse{}, errors.New("token poll response missing required fields")
			}
			return parsed, nil
		}

		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			wait := code.PollInterval
			if wait <= 0 {
				wait = 5 * time.Second
			}
			if remaining := time.Until(deadline); wait > remaining {
				wait = remaining
			}
			if wait <= 0 {
				return deviceTokenPollResponse{}, errors.New("device auth timed out")
			}
			select {
			case <-ctx.Done():
				return deviceTokenPollResponse{}, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		return deviceTokenPollResponse{}, fmt.Errorf("device auth polling failed: status %d", resp.StatusCode)
	}
}

func exchangeOpenAIAuthorizationCode(ctx context.Context, opts OpenAIOAuthOptions, code, codeVerifier, redirectURI string) (Method, error) {
	opts = normalizeOpenAIOAuthOptions(opts)
	issuer := strings.TrimSuffix(opts.Issuer, "/")
	endpoint := issuer + "/oauth/token"
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = issuerRedirectURI(opts)
	}

	values := url.Values{}
	values.Set("grant_type", "authorization_code")
	values.Set("code", code)
	values.Set("redirect_uri", redirectURI)
	values.Set("client_id", opts.ClientID)
	values.Set("code_verifier", codeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return Method{}, fmt.Errorf("create code exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return Method{}, fmt.Errorf("exchange code for tokens: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Method{}, fmt.Errorf("read code exchange response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return Method{}, fmt.Errorf("token exchange failed: status %d", resp.StatusCode)
	}

	var parsed oauthTokenResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Method{}, fmt.Errorf("decode token exchange response: %w", err)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return Method{}, errors.New("token exchange response missing access token")
	}

	tokenType := parsed.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	if parsed.ExpiresIn > 0 {
		expiresAt = time.Now().UTC().Add(time.Duration(parsed.ExpiresIn) * time.Second)
	}

	return Method{
		Type: MethodOAuth,
		OAuth: &OAuthMethod{
			AccessToken:  parsed.AccessToken,
			RefreshToken: parsed.RefreshToken,
			TokenType:    tokenType,
			Expiry:       expiresAt,
			AccountID:    extractAccountID(parsed),
			Email:        extractEmail(parsed),
		},
	}, nil
}

func issuerRedirectURI(opts OpenAIOAuthOptions) string {
	issuer := strings.TrimSuffix(opts.Issuer, "/")
	return issuer + "/deviceauth/callback"
}

func RefreshOpenAIAuthToken(ctx context.Context, opts OpenAIOAuthOptions, method Method) (Method, error) {
	opts = normalizeOpenAIOAuthOptions(opts)
	if method.Type != MethodOAuth || method.OAuth == nil {
		return Method{}, ErrInvalidAuthMethod
	}
	if strings.TrimSpace(method.OAuth.RefreshToken) == "" {
		return Method{}, fmt.Errorf("%w: missing refresh token", ErrOAuthRefreshFailed)
	}

	issuer := strings.TrimSuffix(opts.Issuer, "/")
	endpoint := issuer + "/oauth/token"
	values := url.Values{}
	values.Set("client_id", opts.ClientID)
	values.Set("grant_type", "refresh_token")
	values.Set("refresh_token", method.OAuth.RefreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return Method{}, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return Method{}, fmt.Errorf("%w: %v", ErrOAuthRefreshFailed, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Method{}, fmt.Errorf("%w: %v", ErrOAuthRefreshFailed, err)
	}

	if resp.StatusCode/100 != 2 {
		return Method{}, fmt.Errorf("%w: status %d", ErrOAuthRefreshFailed, resp.StatusCode)
	}

	var parsed oauthTokenResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Method{}, fmt.Errorf("%w: %v", ErrOAuthRefreshFailed, err)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return Method{}, fmt.Errorf("%w: missing access token", ErrOAuthRefreshFailed)
	}

	updated := method
	updated.OAuth.AccessToken = parsed.AccessToken
	if strings.TrimSpace(parsed.RefreshToken) != "" {
		updated.OAuth.RefreshToken = parsed.RefreshToken
	}
	if strings.TrimSpace(parsed.TokenType) != "" {
		updated.OAuth.TokenType = parsed.TokenType
	}
	if strings.TrimSpace(updated.OAuth.TokenType) == "" {
		updated.OAuth.TokenType = "Bearer"
	}
	if accountID := extractAccountID(parsed); strings.TrimSpace(accountID) != "" {
		updated.OAuth.AccountID = accountID
	}
	if email := extractEmail(parsed); strings.TrimSpace(email) != "" {
		updated.OAuth.Email = email
	}
	if parsed.ExpiresIn > 0 {
		updated.OAuth.Expiry = time.Now().UTC().Add(time.Duration(parsed.ExpiresIn) * time.Second)
	} else {
		updated.OAuth.Expiry = time.Now().UTC().Add(time.Hour)
	}
	return updated, nil
}

func NewOpenAIOAuthRefresher(opts OpenAIOAuthOptions, now func() time.Time, refreshBefore time.Duration) *OAuthRefresher {
	opts = normalizeOpenAIOAuthOptions(opts)
	return NewOAuthRefresher(
		now,
		refreshBefore,
		func(ctx context.Context, method Method) (Method, error) {
			return RefreshOpenAIAuthToken(ctx, opts, method)
		},
	)
}

func parsePollInterval(v any) (int64, error) {
	const maxPollIntervalSeconds = int64(math.MaxInt64 / int64(time.Second))
	switch typed := v.(type) {
	case nil:
		return 0, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, fmt.Errorf("invalid poll interval %v", typed)
		}
		if typed != math.Trunc(typed) {
			return 0, fmt.Errorf("invalid poll interval %v", typed)
		}
		interval := int64(typed)
		if interval > maxPollIntervalSeconds {
			return 0, fmt.Errorf("poll interval too large: %d", interval)
		}
		return interval, nil
	case json.Number:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed.String()), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid poll interval %q", typed.String())
		}
		if parsed > maxPollIntervalSeconds {
			return 0, fmt.Errorf("poll interval too large: %d", parsed)
		}
		return parsed, nil
	case string:
		typed = strings.TrimSpace(typed)
		if typed == "" {
			return 0, nil
		}
		n, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid poll interval %q", typed)
		}
		if n > maxPollIntervalSeconds {
			return 0, fmt.Errorf("poll interval too large: %d", n)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("invalid interval type %T", v)
	}
}

func extractAccountID(tokens oauthTokenResponse) string {
	if strings.TrimSpace(tokens.IDToken) != "" {
		if claims, err := parseJWTClaims(tokens.IDToken); err == nil {
			if accountID := extractAccountIDFromClaims(claims); accountID != "" {
				return accountID
			}
		}
	}
	if strings.TrimSpace(tokens.AccessToken) != "" {
		if claims, err := parseJWTClaims(tokens.AccessToken); err == nil {
			return extractAccountIDFromClaims(claims)
		}
	}
	return ""
}

func extractAccountIDFromClaims(claims idTokenClaims) string {
	if v := strings.TrimSpace(claims.ChatGPTAccountID); v != "" {
		return v
	}
	if v := strings.TrimSpace(claims.OpenAIAuth.ChatGPTAccountID); v != "" {
		return v
	}
	if len(claims.Organizations) > 0 {
		return strings.TrimSpace(claims.Organizations[0].ID)
	}
	return ""
}

func extractEmail(tokens oauthTokenResponse) string {
	if strings.TrimSpace(tokens.IDToken) != "" {
		if claims, err := parseJWTClaims(tokens.IDToken); err == nil {
			if email := strings.TrimSpace(claims.Email); email != "" {
				return email
			}
		}
	}
	if strings.TrimSpace(tokens.AccessToken) != "" {
		if claims, err := parseJWTClaims(tokens.AccessToken); err == nil {
			return strings.TrimSpace(claims.Email)
		}
	}
	return ""
}

func parseJWTClaims(token string) (idTokenClaims, error) {
	var out idTokenClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return out, errors.New("invalid token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
