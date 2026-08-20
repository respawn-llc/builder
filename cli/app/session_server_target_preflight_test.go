package app

import (
	"testing"

	"core/shared/protocol"
)

func TestValidateStartupRemoteIdentityRequiresExactProtocolVersion(t *testing.T) {
	compatible := protocol.ServerIdentity{ProtocolVersion: protocol.Version}
	if err := validateStartupRemoteIdentity(compatible); err != nil {
		t.Fatalf("compatible identity: %v", err)
	}

	if err := validateStartupRemoteIdentity(protocol.ServerIdentity{ProtocolVersion: "different"}); err == nil {
		t.Fatal("mismatched protocol version was accepted")
	}
}
