package status

import (
	"testing"

	"core/server/auth"
)

func TestAuthIdentityUsesOpaqueFingerprintWhenOAuthAccountAndEmailMissing(t *testing.T) {
	stateA := auth.State{Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{RefreshToken: "refresh-a"}}}
	stateB := auth.State{Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{RefreshToken: "refresh-b"}}}

	identityA := AuthIdentity(stateA)
	identityB := AuthIdentity(stateB)
	if identityA == identityB {
		t.Fatalf("expected opaque token fingerprints to differ, got %q and %q", identityA, identityB)
	}
	if AuthIdentity(stateA) != identityA {
		t.Fatalf("opaque token fingerprint must be deterministic, got %q then %q", identityA, AuthIdentity(stateA))
	}
}

func TestAuthCacheIdentityTreatsTypedNilManagerAsNoAuth(t *testing.T) {
	var manager *auth.Manager
	if got := AuthCacheIdentity(manager); got != "auth:none" {
		t.Fatalf("auth cache identity = %q, want auth:none", got)
	}
	if NormalizeAuthStateResolver(manager) != nil {
		t.Fatal("expected typed nil auth resolver to normalize to nil")
	}
}
