package invariant

import (
	"testing"
)

func TestPolicyDefaultDiagnosticModeRecordsStructuredDiagnostic(t *testing.T) {
	var recorded []Diagnostic
	policy := NewPolicy(WithSink(SinkFunc(func(d Diagnostic) {
		recorded = append(recorded, d)
	})))

	policy.Check(false, TUIProjectionDiagnostic(TUIProjectionDiagnosticInput{
		Operation:            "ctrl_c",
		SessionID:            "session-1",
		CachedServerActivity: "registered_idle",
		LocalProjection:      "running",
		PendingInterrupt:     "pending",
		ConnectionState:      "connected",
	}))

	if len(recorded) != 1 {
		t.Fatalf("recorded diagnostics = %d, want 1", len(recorded))
	}
	got := recorded[0]
	if got.Scope != ScopeTUIProjection {
		t.Fatalf("scope = %q, want %q", got.Scope, ScopeTUIProjection)
	}
	assertDiagnosticField(t, got, FieldOperation, "ctrl_c")
	assertDiagnosticField(t, got, FieldSessionID, "session-1")
	assertDiagnosticField(t, got, FieldCachedServerActivity, "registered_idle")
	assertDiagnosticField(t, got, FieldLocalProjection, "running")
	assertDiagnosticField(t, got, FieldPendingInterrupt, "pending")
	assertDiagnosticField(t, got, FieldConnectionState, "connected")
	if got.Stack == "" {
		t.Fatal("expected diagnostic stack")
	}
}

func TestPolicyPanicModePanicsWithDiagnostic(t *testing.T) {
	policy := NewPolicy(WithMode(ModePanic))

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected panic")
		}
		diagnostic, ok := recovered.(Diagnostic)
		if !ok {
			t.Fatalf("panic payload = %T, want Diagnostic", recovered)
		}
		assertDiagnosticField(t, diagnostic, FieldOperation, "publish_activity")
		assertDiagnosticField(t, diagnostic, FieldPublicationCause, "step_ended")
		assertDiagnosticField(t, diagnostic, FieldCurrentReadModelVersion, "epoch-1/1/40")
		assertDiagnosticField(t, diagnostic, FieldProposedReadModelVersion, "epoch-1/1/41")
		assertDiagnosticField(t, diagnostic, FieldResolverInputs, "entry=present")
		assertDiagnosticField(t, diagnostic, FieldOwnerSnapshots, "active=cleared")
		assertDiagnosticField(t, diagnostic, FieldCachedActivity, "running")
		assertDiagnosticField(t, diagnostic, FieldResolvedActivity, "registered_idle")
		assertDiagnosticField(t, diagnostic, FieldProviderError, "invalid dto")
	}()

	policy.Check(false, ReadModelPublicationDiagnostic(ReadModelPublicationDiagnosticInput{
		Operation:                   "publish_activity",
		SessionID:                   "session-1",
		PublicationCause:            "step_ended",
		CurrentReadModelVersion:     "epoch-1/1/40",
		ProposedReadModelVersion:    "epoch-1/1/41",
		ResolverInputs:              "entry=present",
		OwnerSnapshots:              "active=cleared",
		CachedLastPublishedActivity: "running",
		ResolvedProposedActivity:    "registered_idle",
		ProviderError:               "invalid dto",
	}))
}

func TestPolicyModeFromEnvironment(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want Mode
	}{
		{name: "default diagnostic", want: ModeDiagnostic},
		{name: "debug selects panic", env: map[string]string{"KENT_DEBUG": "1"}, want: ModePanic},
		{name: "explicit diagnostic overrides debug", env: map[string]string{"KENT_DEBUG": "1", "KENT_INVARIANT_MODE": "diagnostic"}, want: ModeDiagnostic},
		{name: "explicit panic", env: map[string]string{"KENT_INVARIANT_MODE": "panic"}, want: ModePanic},
		{name: "unknown explicit mode falls back to diagnostic", env: map[string]string{"KENT_INVARIANT_MODE": "warn"}, want: ModeDiagnostic},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := NewPolicy(WithEnvironment(func(key string) string {
				return tt.env[key]
			}))
			if got := policy.Mode(); got != tt.want {
				t.Fatalf("mode = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPolicyCheckTrueDoesNotRecord(t *testing.T) {
	policy := NewPolicy(WithSink(SinkFunc(func(Diagnostic) {
		t.Fatal("sink must not be called for satisfied invariant")
	})))

	policy.Check(true, Diagnostic{Scope: ScopeTUIProjection})
}

func assertDiagnosticField(t *testing.T, diagnostic Diagnostic, key Field, want string) {
	t.Helper()
	if got := diagnostic.Fields[key]; got != want {
		t.Fatalf("diagnostic field %q = %q, want %q", key, got, want)
	}
}
