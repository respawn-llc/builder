package app

import (
	"errors"
	"testing"

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
	if err := validateStartupRemoteIdentity(compatible); err != nil {
		t.Fatalf("compatible identity: %v", err)
	}

	for _, test := range []struct {
		identity protocol.ServerIdentity
		issue    startupRemoteCompatibilityIssue
	}{
		{
			identity: protocol.ServerIdentity{ProtocolVersion: "different", Capabilities: compatible.Capabilities},
			issue:    startupRemoteProtocolVersionMismatch,
		},
		{
			identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, Capabilities: protocol.CapabilityFlags{OnboardingFinalize: true}},
			issue:    startupRemoteAuthBootstrapUnavailable,
		},
		{
			identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, Capabilities: protocol.CapabilityFlags{AuthBootstrap: true}},
			issue:    startupRemoteOnboardingFinalizeUnavailable,
		},
	} {
		err := validateStartupRemoteIdentity(test.identity)
		var compatibility *startupRemoteCompatibilityError
		if !errors.As(err, &compatibility) {
			t.Fatalf("identity %+v error = %v, want typed compatibility error", test.identity, err)
		}
		if compatibility.issue != test.issue {
			t.Fatalf("identity %+v compatibility issue = %d, want %d", test.identity, compatibility.issue, test.issue)
		}
	}
}
