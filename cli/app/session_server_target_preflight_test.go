package app

import (
	"context"
	"errors"
	"testing"

	"core/cli/app/internal/startupremote"
	"core/shared/config"
	"core/shared/protocol"
)

func TestValidateStartupRemoteIdentityRequiresExactOnboardingControlSurface(t *testing.T) {
	compatible := protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		Capabilities: protocol.CapabilityFlags{
			AuthBootstrap:      true,
			OnboardingFinalize: true,
		},
	}
	if err := startupremote.ValidateIdentity(compatible); err != nil {
		t.Fatalf("compatible identity: %v", err)
	}

	for _, test := range []struct {
		identity protocol.ServerIdentity
		issue    startupremote.CompatibilityIssue
	}{
		{
			identity: protocol.ServerIdentity{ProtocolVersion: "different", Capabilities: compatible.Capabilities},
			issue:    startupremote.ProtocolVersionMismatch,
		},
		{
			identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, Capabilities: protocol.CapabilityFlags{OnboardingFinalize: true}},
			issue:    startupremote.AuthBootstrapUnavailable,
		},
		{
			identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, Capabilities: protocol.CapabilityFlags{AuthBootstrap: true}},
			issue:    startupremote.OnboardingFinalizeUnavailable,
		},
	} {
		err := startupremote.ValidateIdentity(test.identity)
		var compatibility *startupremote.CompatibilityError
		if !errors.As(err, &compatibility) {
			t.Fatalf("identity %+v error = %v, want typed compatibility error", test.identity, err)
		}
		if compatibility.Issue != test.issue {
			t.Fatalf("identity %+v compatibility issue = %q, want %q", test.identity, compatibility.Issue, test.issue)
		}
	}
}

func TestConfiguredStartupUnavailableEndpointReturnsAttachContext(t *testing.T) {
	workspace := t.TempDir()
	cfgRoot := t.TempDir()
	t.Setenv(config.PersistenceRootEnvName, cfgRoot)
	t.Setenv("KENT_SERVER_HOST", "127.0.0.1")
	t.Setenv("KENT_SERVER_PORT", "1")

	_, err := startSessionServer(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		ConfigRoot:            cfgRoot,
	}, newHeadlessAuthInteractor(), false)
	if err == nil {
		t.Fatal("configured startup unexpectedly succeeded without a reachable server")
	}
	var preflight *configuredServerPreflightError
	if !errors.As(err, &preflight) {
		t.Fatalf("error = %v, want configuredServerPreflightError", err)
	}
	if preflight.operation != "attach" || preflight.endpoint == "" {
		t.Fatalf("preflight = %+v, want endpoint-scoped attach failure", preflight)
	}
}
