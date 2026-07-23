package serverapi

import (
	"errors"
	"testing"
)

func TestAuthCompleteBootstrapRequestValidateRequiresOAuthStateForBrowserCallback(t *testing.T) {
	err := AuthCompleteBootstrapRequest{
		Mode:              AuthBootstrapModeBrowserCallbackCode,
		CallbackInput:     "code=abc",
		RedirectURI:       "http://localhost/callback",
		OAuthCodeVerifier: "verifier",
	}.Validate()
	if !errors.Is(err, ErrAuthBootstrapOAuthStateRequired) {
		t.Fatalf("Validate error = %v, want ErrAuthBootstrapOAuthStateRequired", err)
	}
}
