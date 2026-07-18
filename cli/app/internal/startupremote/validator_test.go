package startupremote

import (
	"context"
	"errors"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

type remoteStub struct {
	identity       protocol.ServerIdentity
	requireRootErr error
	readinessErr   error
	authErr        error
	factsErr       error
}

func (s remoteStub) Identity() protocol.ServerIdentity { return s.identity }

func (s remoteStub) RequireRoot(string) error { return s.requireRootErr }

func (s remoteStub) GetServerReadiness(context.Context, serverapi.ServerReadinessRequest) (serverapi.ServerReadinessResponse, error) {
	return serverapi.ServerReadinessResponse{}, s.readinessErr
}

func (s remoteStub) GetAuthBootstrapStatus(context.Context, serverapi.AuthGetBootstrapStatusRequest) (serverapi.AuthGetBootstrapStatusResponse, error) {
	return serverapi.AuthGetBootstrapStatusResponse{}, s.authErr
}

func (s remoteStub) GetCapabilityFacts(context.Context, serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error) {
	return serverapi.CapabilityFactsResponse{}, s.factsErr
}

func compatibleRemoteStub() remoteStub {
	return remoteStub{identity: protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		Capabilities: protocol.CapabilityFlags{
			AuthBootstrap:      true,
			OnboardingFinalize: true,
		},
	}}
}

func TestValidateRejectsIncompatibleIdentityBeforeRemoteProbes(t *testing.T) {
	remote := compatibleRemoteStub()
	remote.identity.ProtocolVersion = "other"

	err := Validate(context.Background(), remote, "root-id")
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("Validate error = %v, want ValidationError", err)
	}
	if validation.Operation != OperationCompatibility {
		t.Fatalf("operation = %q, want %q", validation.Operation, OperationCompatibility)
	}
	var compatibility *CompatibilityError
	if !errors.As(err, &compatibility) {
		t.Fatalf("Validate error = %v, want CompatibilityError", err)
	}
	if compatibility.Issue != ProtocolVersionMismatch {
		t.Fatalf("issue = %q, want %q", compatibility.Issue, ProtocolVersionMismatch)
	}
}

func TestValidateReportsEachRequiredRemoteProbe(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*remoteStub, error)
		operation Operation
	}{
		{
			name:      "root pin",
			configure: func(s *remoteStub, err error) { s.requireRootErr = err },
			operation: OperationRootPin,
		},
		{
			name:      "readiness",
			configure: func(s *remoteStub, err error) { s.readinessErr = err },
			operation: OperationReadiness,
		},
		{
			name:      "auth bootstrap",
			configure: func(s *remoteStub, err error) { s.authErr = err },
			operation: OperationAuthBootstrap,
		},
		{
			name:      "capability facts",
			configure: func(s *remoteStub, err error) { s.factsErr = err },
			operation: OperationCapabilityFacts,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := errors.New(test.name)
			remote := compatibleRemoteStub()
			test.configure(&remote, want)

			err := Validate(context.Background(), remote, "root-id")
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("Validate error = %v, want ValidationError", err)
			}
			if validation.Operation != test.operation {
				t.Fatalf("operation = %q, want %q", validation.Operation, test.operation)
			}
			if !errors.Is(err, want) {
				t.Fatalf("Validate error = %v, want %v", err, want)
			}
		})
	}
}

func TestValidateAllowsCompatibleRemote(t *testing.T) {
	if err := Validate(context.Background(), compatibleRemoteStub(), "root-id"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
