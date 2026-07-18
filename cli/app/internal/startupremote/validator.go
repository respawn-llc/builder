// Package startupremote validates a newly attached configured Remote before it
// becomes usable by an interactive client lifecycle.
package startupremote

import (
	"context"
	"fmt"
	"strings"

	"core/shared/protocol"
	"core/shared/serverapi"
)

type Remote interface {
	Identity() protocol.ServerIdentity
	RequireRoot(rootID string) error
	GetServerReadiness(context.Context, serverapi.ServerReadinessRequest) (serverapi.ServerReadinessResponse, error)
	GetAuthBootstrapStatus(context.Context, serverapi.AuthGetBootstrapStatusRequest) (serverapi.AuthGetBootstrapStatusResponse, error)
	GetCapabilityFacts(context.Context, serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error)
}

type Operation string

const (
	OperationRootPin         Operation = "validate persistence root"
	OperationCompatibility   Operation = "validate compatibility"
	OperationReadiness       Operation = "probe server readiness"
	OperationAuthBootstrap   Operation = "probe auth bootstrap"
	OperationCapabilityFacts Operation = "probe onboarding capability facts"
)

type ValidationError struct {
	Operation Operation
	Cause     error
}

func (e *ValidationError) Error() string {
	return string(e.Operation) + ": " + e.Cause.Error()
}

func (e *ValidationError) Unwrap() error {
	return e.Cause
}

type CompatibilityIssue string

const (
	ProtocolVersionMismatch       CompatibilityIssue = "protocol_version_mismatch"
	AuthBootstrapUnavailable      CompatibilityIssue = "auth_bootstrap_unavailable"
	OnboardingFinalizeUnavailable CompatibilityIssue = "onboarding_finalize_unavailable"
)

type CompatibilityError struct {
	Issue         CompatibilityIssue
	ServerVersion string
}

func (e *CompatibilityError) Error() string {
	switch e.Issue {
	case ProtocolVersionMismatch:
		return fmt.Sprintf("server protocol version %q is incompatible with client protocol version %q", e.ServerVersion, protocol.Version)
	case AuthBootstrapUnavailable:
		return "server does not advertise auth bootstrap support"
	case OnboardingFinalizeUnavailable:
		return "server does not advertise onboarding finalization support"
	default:
		return "server startup control surface is incompatible"
	}
}

func Validate(ctx context.Context, remote Remote, rootID string) error {
	return ValidateWithWorkspace(ctx, remote, rootID, nil)
}

func ValidateWithWorkspace(ctx context.Context, remote Remote, rootID string, workspaceRoot *string) error {
	if err := remote.RequireRoot(strings.TrimSpace(rootID)); err != nil {
		return &ValidationError{Operation: OperationRootPin, Cause: err}
	}
	if err := ValidateIdentity(remote.Identity()); err != nil {
		return &ValidationError{Operation: OperationCompatibility, Cause: err}
	}
	if _, err := remote.GetServerReadiness(ctx, serverapi.ServerReadinessRequest{}); err != nil {
		return &ValidationError{Operation: OperationReadiness, Cause: err}
	}
	if _, err := remote.GetAuthBootstrapStatus(ctx, serverapi.AuthGetBootstrapStatusRequest{}); err != nil {
		return &ValidationError{Operation: OperationAuthBootstrap, Cause: err}
	}
	if _, err := remote.GetCapabilityFacts(ctx, serverapi.CapabilityFactsRequest{WorkspaceRoot: workspaceRoot}); err != nil {
		return &ValidationError{Operation: OperationCapabilityFacts, Cause: err}
	}
	return nil
}

func ValidateIdentity(identity protocol.ServerIdentity) error {
	if identity.ProtocolVersion != protocol.Version {
		return &CompatibilityError{
			Issue:         ProtocolVersionMismatch,
			ServerVersion: identity.ProtocolVersion,
		}
	}
	if !identity.Capabilities.AuthBootstrap {
		return &CompatibilityError{Issue: AuthBootstrapUnavailable}
	}
	if !identity.Capabilities.OnboardingFinalize {
		return &CompatibilityError{Issue: OnboardingFinalizeUnavailable}
	}
	return nil
}
