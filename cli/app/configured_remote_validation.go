package app

import (
	"context"
	"fmt"
	"strings"

	"core/shared/protocol"
	"core/shared/serverapi"
)

type configuredRemoteValidator interface {
	Identity() protocol.ServerIdentity
	RequireRoot(rootID string) error
	GetServerReadiness(context.Context, serverapi.ServerReadinessRequest) (serverapi.ServerReadinessResponse, error)
	GetAuthBootstrapStatus(context.Context, serverapi.AuthGetBootstrapStatusRequest) (serverapi.AuthGetBootstrapStatusResponse, error)
	GetCapabilityFacts(context.Context, serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error)
}

type configuredRemoteValidationOperation string

const (
	configuredRemoteValidationRootPin         configuredRemoteValidationOperation = "validate persistence root"
	configuredRemoteValidationCompatibility   configuredRemoteValidationOperation = "validate compatibility"
	configuredRemoteValidationReadiness       configuredRemoteValidationOperation = "probe server readiness"
	configuredRemoteValidationAuthBootstrap   configuredRemoteValidationOperation = "probe auth bootstrap"
	configuredRemoteValidationCapabilityFacts configuredRemoteValidationOperation = "probe onboarding capability facts"
)

type configuredRemoteValidationError struct {
	Operation configuredRemoteValidationOperation
	Cause     error
}

func (e *configuredRemoteValidationError) Error() string {
	return string(e.Operation) + ": " + e.Cause.Error()
}

func (e *configuredRemoteValidationError) Unwrap() error {
	return e.Cause
}

type configuredRemoteCompatibilityIssue string

const (
	configuredRemoteProtocolVersionMismatch       configuredRemoteCompatibilityIssue = "protocol_version_mismatch"
	configuredRemoteAuthBootstrapUnavailable      configuredRemoteCompatibilityIssue = "auth_bootstrap_unavailable"
	configuredRemoteOnboardingFinalizeUnavailable configuredRemoteCompatibilityIssue = "onboarding_finalize_unavailable"
)

type configuredRemoteCompatibilityError struct {
	Issue         configuredRemoteCompatibilityIssue
	ServerVersion string
}

func (e *configuredRemoteCompatibilityError) Error() string {
	switch e.Issue {
	case configuredRemoteProtocolVersionMismatch:
		return fmt.Sprintf("server protocol version %q is incompatible with client protocol version %q", e.ServerVersion, protocol.Version)
	case configuredRemoteAuthBootstrapUnavailable:
		return "server does not advertise auth bootstrap support"
	case configuredRemoteOnboardingFinalizeUnavailable:
		return "server does not advertise onboarding finalization support"
	default:
		return "server startup control surface is incompatible"
	}
}

// validateConfiguredRemoteWithWorkspace is the sole startup policy for initial
// and successor configured Remotes. It pins the root exactly once before any
// readiness/auth/capability probe and before a predecessor may retire.
func validateConfiguredRemoteWithWorkspace(ctx context.Context, remote configuredRemoteValidator, rootID string, workspaceRoot *string) error {
	if err := remote.RequireRoot(strings.TrimSpace(rootID)); err != nil {
		return &configuredRemoteValidationError{Operation: configuredRemoteValidationRootPin, Cause: err}
	}
	if err := validateConfiguredRemoteIdentity(remote.Identity()); err != nil {
		return &configuredRemoteValidationError{Operation: configuredRemoteValidationCompatibility, Cause: err}
	}
	if _, err := remote.GetServerReadiness(ctx, serverapi.ServerReadinessRequest{}); err != nil {
		return &configuredRemoteValidationError{Operation: configuredRemoteValidationReadiness, Cause: err}
	}
	if _, err := remote.GetAuthBootstrapStatus(ctx, serverapi.AuthGetBootstrapStatusRequest{}); err != nil {
		return &configuredRemoteValidationError{Operation: configuredRemoteValidationAuthBootstrap, Cause: err}
	}
	if _, err := remote.GetCapabilityFacts(ctx, serverapi.CapabilityFactsRequest{WorkspaceRoot: workspaceRoot}); err != nil {
		return &configuredRemoteValidationError{Operation: configuredRemoteValidationCapabilityFacts, Cause: err}
	}
	return nil
}

func validateConfiguredRemoteIdentity(identity protocol.ServerIdentity) error {
	if identity.ProtocolVersion != protocol.Version {
		return &configuredRemoteCompatibilityError{
			Issue:         configuredRemoteProtocolVersionMismatch,
			ServerVersion: identity.ProtocolVersion,
		}
	}
	if !identity.Capabilities.AuthBootstrap {
		return &configuredRemoteCompatibilityError{Issue: configuredRemoteAuthBootstrapUnavailable}
	}
	if !identity.Capabilities.OnboardingFinalize {
		return &configuredRemoteCompatibilityError{Issue: configuredRemoteOnboardingFinalizeUnavailable}
	}
	return nil
}
