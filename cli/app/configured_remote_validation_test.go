package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

type configuredRemoteValidatorStub struct {
	identity       protocol.ServerIdentity
	rootPinCalls   int
	operations     []string
	requireRootErr error
	readinessErr   error
	authErr        error
	factsErr       error
}

func (s *configuredRemoteValidatorStub) Identity() protocol.ServerIdentity {
	s.operations = append(s.operations, "identity")
	return s.identity
}

func (s *configuredRemoteValidatorStub) RequireRoot(string) error {
	s.rootPinCalls++
	s.operations = append(s.operations, "root_pin")
	return s.requireRootErr
}

func (s *configuredRemoteValidatorStub) GetServerReadiness(context.Context, serverapi.ServerReadinessRequest) (serverapi.ServerReadinessResponse, error) {
	s.operations = append(s.operations, "readiness")
	return serverapi.ServerReadinessResponse{}, s.readinessErr
}

func (s *configuredRemoteValidatorStub) GetAuthBootstrapStatus(context.Context, serverapi.AuthGetBootstrapStatusRequest) (serverapi.AuthGetBootstrapStatusResponse, error) {
	s.operations = append(s.operations, "auth_bootstrap")
	return serverapi.AuthGetBootstrapStatusResponse{}, s.authErr
}

func (s *configuredRemoteValidatorStub) GetCapabilityFacts(context.Context, serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error) {
	s.operations = append(s.operations, "capability_facts")
	return serverapi.CapabilityFactsResponse{}, s.factsErr
}

func compatibleConfiguredRemoteValidatorStub() *configuredRemoteValidatorStub {
	return &configuredRemoteValidatorStub{identity: protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		Capabilities: protocol.CapabilityFlags{
			AuthBootstrap:      true,
			OnboardingFinalize: true,
		},
	}}
}

func TestConfiguredRemoteValidationPinsOnceAndCompletesRequiredProbes(t *testing.T) {
	t.Parallel()

	remote := compatibleConfiguredRemoteValidatorStub()
	if err := validateConfiguredRemoteWithWorkspace(context.Background(), remote, "root-id", nil); err != nil {
		t.Fatalf("validate configured remote: %v", err)
	}
	if remote.rootPinCalls != 1 {
		t.Fatalf("root pin calls = %d, want exactly one", remote.rootPinCalls)
	}
	wantOperations := []string{"root_pin", "identity", "readiness", "auth_bootstrap", "capability_facts"}
	if !reflect.DeepEqual(remote.operations, wantOperations) {
		t.Fatalf("validation operations = %v, want %v", remote.operations, wantOperations)
	}
}

func TestConfiguredRemoteValidationReportsRequiredProbeFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		configure func(*configuredRemoteValidatorStub, error)
		operation configuredRemoteValidationOperation
	}{
		{name: "root pin", configure: func(s *configuredRemoteValidatorStub, err error) { s.requireRootErr = err }, operation: configuredRemoteValidationRootPin},
		{name: "readiness", configure: func(s *configuredRemoteValidatorStub, err error) { s.readinessErr = err }, operation: configuredRemoteValidationReadiness},
		{name: "auth bootstrap", configure: func(s *configuredRemoteValidatorStub, err error) { s.authErr = err }, operation: configuredRemoteValidationAuthBootstrap},
		{name: "capability facts", configure: func(s *configuredRemoteValidatorStub, err error) { s.factsErr = err }, operation: configuredRemoteValidationCapabilityFacts},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := errors.New(test.name)
			remote := compatibleConfiguredRemoteValidatorStub()
			test.configure(remote, want)

			err := validateConfiguredRemoteWithWorkspace(context.Background(), remote, "root-id", nil)
			var validation *configuredRemoteValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("validation error = %v, want configuredRemoteValidationError", err)
			}
			if validation.Operation != test.operation {
				t.Fatalf("operation = %q, want %q", validation.Operation, test.operation)
			}
			if !errors.Is(err, want) {
				t.Fatalf("validation error = %v, want %v", err, want)
			}
		})
	}
}
