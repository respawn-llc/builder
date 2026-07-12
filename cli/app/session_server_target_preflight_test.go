package app

import (
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

	for _, identity := range []protocol.ServerIdentity{
		{ProtocolVersion: "different", Capabilities: compatible.Capabilities},
		{ProtocolVersion: protocol.Version, Capabilities: protocol.CapabilityFlags{OnboardingFinalize: true}},
		{ProtocolVersion: protocol.Version, Capabilities: protocol.CapabilityFlags{AuthBootstrap: true}},
	} {
		if err := validateStartupRemoteIdentity(identity); err == nil {
			t.Fatalf("expected identity %+v to be incompatible", identity)
		}
	}
}
