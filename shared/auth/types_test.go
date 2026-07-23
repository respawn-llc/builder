package auth

import "testing"

func TestEvaluateStartupGateDoesNotTreatNoAuthPreferenceAsReady(t *testing.T) {
	gate := EvaluateStartupGate(State{
		Scope:               ScopeGlobal,
		Method:              Method{Type: MethodNone},
		EnvAPIKeyPreference: EnvAPIKeyPreferencePreferSaved,
	})
	if gate.Ready {
		t.Fatal("no-auth preference must not satisfy auth-required startup gate")
	}
	if gate.Reason != ErrAuthNotConfigured.Error() {
		t.Fatalf("reason = %q, want %q", gate.Reason, ErrAuthNotConfigured.Error())
	}
}
