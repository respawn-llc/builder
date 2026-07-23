package authui

import (
	"testing"

	"core/server/auth"
)

func TestNeedsEnvConflictResolution(t *testing.T) {
	if !NeedsAuthEnvConflictResolution(AuthInteractionRequest{
		Gate: auth.StartupGate{Ready: true},
		StoredState: auth.State{
			Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{AccessToken: "x"}},
		},
		HasEnvAPIKey: true,
	}) {
		t.Fatal("expected unresolved env-vs-oauth conflict to require interaction")
	}
	if NeedsAuthEnvConflictResolution(AuthInteractionRequest{
		Gate: auth.StartupGate{Ready: true},
		StoredState: auth.State{
			Method:              auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{AccessToken: "x"}},
			EnvAPIKeyPreference: auth.EnvAPIKeyPreferencePreferSaved,
		},
		HasEnvAPIKey: true,
	}) {
		t.Fatal("did not expect saved preference to reopen conflict picker")
	}
	if NeedsAuthEnvConflictResolution(AuthInteractionRequest{Gate: auth.StartupGate{Ready: false}, StoredState: auth.State{Method: auth.Method{Type: auth.MethodOAuth}}, HasEnvAPIKey: true}) {
		t.Fatal("did not expect unready auth to require env conflict resolution")
	}
}
