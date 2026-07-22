package auth

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func rewriteOAuthIssuerClient(server *httptest.Server) *http.Client {
	target, err := url.Parse(server.URL)
	if err != nil {
		panic(err)
	}
	client := server.Client()
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		clone.Host = target.Host
		return base.RoundTrip(clone)
	})
	return client
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func writeOAuthTokenResponse(t testing.TB, w http.ResponseWriter, accessToken string, refreshToken string, expiresIn int) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"token_type":    "Bearer",
		"expires_in":    expiresIn,
	}); err != nil {
		t.Fatalf("write token response: %v", err)
	}
}

func TestParsePollInterval(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    int64
		wantErr bool
	}{
		{name: "nil", input: nil, want: 0},
		{name: "float integer", input: float64(5), want: 5},
		{name: "float zero", input: float64(0), want: 0},
		{name: "float negative", input: float64(-2), want: -2},
		{name: "float fraction invalid", input: float64(1.5), wantErr: true},
		{name: "float nan invalid", input: math.NaN(), wantErr: true},
		{name: "float inf invalid", input: math.Inf(1), wantErr: true},
		{name: "string int", input: "7", want: 7},
		{name: "string empty", input: "   ", want: 0},
		{name: "string fraction invalid", input: "1.5", wantErr: true},
		{name: "string suffix invalid", input: "5s", wantErr: true},
		{name: "string overflow invalid", input: "9223372036854775808", wantErr: true},
		{name: "json number", input: json.Number("9"), want: 9},
		{name: "json number invalid", input: json.Number("2.5"), wantErr: true},
		{name: "type invalid", input: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePollInterval(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (value=%d)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("value=%d want=%d", got, tt.want)
			}
		})
	}
}

func TestRunOpenAIDeviceCodeFlow(t *testing.T) {
	var pollCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_auth_id": "dev-1",
				"user_code":      "ABCD-1234",
				"interval":       "1",
			})
		case "/api/accounts/deviceauth/token":
			if pollCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"status":"pending"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_code": "auth-code-1",
				"code_challenge":     "challenge",
				"code_verifier":      "verifier",
			})
		case "/oauth/token":
			_ = r.ParseForm()
			if got := r.Form.Get("grant_type"); got != "authorization_code" {
				t.Fatalf("unexpected grant_type: %s", got)
			}
			if got := r.Form.Get("code"); got != "auth-code-1" {
				t.Fatalf("unexpected code: %s", got)
			}
			writeOAuthTokenResponse(t, w, "access-1", "refresh-1", 1800)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var shown DeviceCode
	method, err := RunOpenAIDeviceCodeFlow(context.Background(), OpenAIOAuthOptions{
		ClientID:    "client-1",
		HTTPClient:  rewriteOAuthIssuerClient(server),
		PollTimeout: 10 * time.Second,
	}, func(code DeviceCode) {
		shown = code
	})
	if err != nil {
		t.Fatalf("device code flow failed: %v", err)
	}
	if shown.UserCode != "ABCD-1234" {
		t.Fatalf("unexpected shown user code: %+v", shown)
	}
	if method.Type != MethodOAuth || method.OAuth == nil {
		t.Fatalf("unexpected method returned: %+v", method)
	}
	if method.OAuth.AccessToken != "access-1" || method.OAuth.RefreshToken != "refresh-1" {
		t.Fatalf("unexpected oauth tokens: %+v", method.OAuth)
	}
	if method.OAuth.AccountID != "" {
		t.Fatalf("expected empty account id for opaque test tokens, got %q", method.OAuth.AccountID)
	}
	if !method.OAuth.Expiry.After(time.Now().UTC()) {
		t.Fatalf("expected future expiry, got %s", method.OAuth.Expiry)
	}
}

func TestRequestOpenAIDeviceCodeUnsupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := requestOpenAIDeviceCode(context.Background(), OpenAIOAuthOptions{
		ClientID:   "client-1",
		HTTPClient: rewriteOAuthIssuerClient(server),
	})
	if err != ErrDeviceCodeUnsupported {
		t.Fatalf("expected ErrDeviceCodeUnsupported, got %v", err)
	}
}

func TestCompleteOpenAIDeviceAuthorizationGrantNormalizesIssuerBeforeRedirectURI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if got := r.Form.Get("redirect_uri"); got != DefaultOpenAIIssuer+"/deviceauth/callback" {
			t.Fatalf("redirect_uri = %q, want %q", got, DefaultOpenAIIssuer+"/deviceauth/callback")
		}
		writeOAuthTokenResponse(t, w, "access-1", "refresh-1", 1800)
	}))
	defer server.Close()

	method, err := CompleteOpenAIDeviceAuthorizationGrant(context.Background(), OpenAIOAuthOptions{
		ClientID:   "client-1",
		HTTPClient: rewriteOAuthIssuerClient(server),
	}, "auth-code-1", "verifier-1")
	if err != nil {
		t.Fatalf("CompleteOpenAIDeviceAuthorizationGrant: %v", err)
	}
	if method.Type != MethodOAuth || method.OAuth == nil {
		t.Fatalf("unexpected method returned: %+v", method)
	}
}

func TestRefreshOpenAIAuthToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Fatalf("unexpected grant_type: %s", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "old-refresh" {
			t.Fatalf("unexpected refresh token: %s", r.Form.Get("refresh_token"))
		}
		writeOAuthTokenResponse(t, w, "new-access", "new-refresh", 3600)
	}))
	defer server.Close()

	updated, err := RefreshOpenAIAuthToken(context.Background(), OpenAIOAuthOptions{
		ClientID:   "client-1",
		HTTPClient: rewriteOAuthIssuerClient(server),
	}, Method{
		Type: MethodOAuth,
		OAuth: &OAuthMethod{
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			TokenType:    "Bearer",
			Expiry:       time.Now().Add(-time.Minute),
		},
	})
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	if updated.OAuth.AccessToken != "new-access" || updated.OAuth.RefreshToken != "new-refresh" {
		t.Fatalf("unexpected refreshed tokens: %+v", updated.OAuth)
	}
}

func TestExtractAccountID(t *testing.T) {
	jwt := func(payload map[string]any) string {
		raw, _ := json.Marshal(payload)
		return "x." + encodeRawURL(raw) + ".y"
	}

	nested := jwt(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc-nested",
		},
	})
	root := jwt(map[string]any{
		"chatgpt_account_id": "acc-root",
	})
	org := jwt(map[string]any{
		"organizations": []map[string]any{{"id": "org-1"}},
	})

	if got := extractAccountID(oauthTokenResponse{IDToken: nested}); got != "acc-nested" {
		t.Fatalf("expected nested id, got %q", got)
	}
	if got := extractAccountID(oauthTokenResponse{IDToken: root}); got != "acc-root" {
		t.Fatalf("expected root id, got %q", got)
	}
	if got := extractAccountID(oauthTokenResponse{AccessToken: org}); got != "org-1" {
		t.Fatalf("expected org id from access token, got %q", got)
	}
}

func encodeRawURL(b []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, 0, (len(b)*4+2)/3)
	for i := 0; i < len(b); i += 3 {
		var n uint32
		remain := len(b) - i
		n = uint32(b[i]) << 16
		if remain > 1 {
			n |= uint32(b[i+1]) << 8
		}
		if remain > 2 {
			n |= uint32(b[i+2])
		}
		out = append(out, alphabet[(n>>18)&0x3F], alphabet[(n>>12)&0x3F])
		if remain > 1 {
			out = append(out, alphabet[(n>>6)&0x3F])
		}
		if remain > 2 {
			out = append(out, alphabet[n&0x3F])
		}
	}
	return string(out)
}
