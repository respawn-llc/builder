package serverattach

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/cli/app/internal/remoteattach"
	"core/shared/client"
	"core/shared/config"
	"core/shared/protocol"
)

var ErrNoServerAvailable = errors.New("no server available to attach to")

var ErrServerIncompatible = errors.New("reachable server is not compatible with this client")

// IncompatibleServerError reports that a reachable server failed the capability
// check, naming why (protocol-version skew or missing capabilities) so the
// operator learns the concrete reason instead of a generic message. It matches
// ErrServerIncompatible via errors.Is and exposes Reason for callers that want
// to compose their own message.
type IncompatibleServerError struct {
	Reason string
}

func (e *IncompatibleServerError) Error() string {
	if strings.TrimSpace(e.Reason) == "" {
		return ErrServerIncompatible.Error()
	}
	return ErrServerIncompatible.Error() + ": " + e.Reason
}

func (e *IncompatibleServerError) Is(target error) bool {
	return target == ErrServerIncompatible
}

var ErrReachableServerRootMismatch = errors.New("reachable server serves a different persistence root than the selected one")

// RootMismatchServerError reports that a reachable server's persistence root did
// not match the selected root, naming the other server so the operator learns
// which instance occupies the endpoint. It matches ErrReachableServerRootMismatch
// via errors.Is and exposes Reason for callers that compose their own message.
type RootMismatchServerError struct {
	Reason string
}

func (e *RootMismatchServerError) Error() string {
	if strings.TrimSpace(e.Reason) == "" {
		return ErrReachableServerRootMismatch.Error()
	}
	return ErrReachableServerRootMismatch.Error() + ": " + e.Reason
}

func (e *RootMismatchServerError) Is(target error) bool {
	return target == ErrReachableServerRootMismatch
}

type capabilityVerdict struct {
	reason             string
	rootMismatchReason string
}

// incompatibilityReason explains why a reachable server failed the capability
// check using the identity it reported during the handshake.
func incompatibilityReason(identity protocol.ServerIdentity) string {
	server := describeIncompatibleServer(identity)
	if reported := strings.TrimSpace(identity.ProtocolVersion); reported != "" && reported != protocol.Version {
		return fmt.Sprintf("%s speaks protocol version %s but this client requires %s", server, reported, protocol.Version)
	}
	return fmt.Sprintf("%s does not advertise the capabilities this client requires (it is likely an older build)", server)
}

func describeIncompatibleServer(identity protocol.ServerIdentity) string {
	id := strings.TrimSpace(identity.ServerID)
	switch {
	case id != "" && identity.PID > 0:
		return fmt.Sprintf("the running server %s (pid %d)", id, identity.PID)
	case id != "":
		return fmt.Sprintf("the running server %s", id)
	case identity.PID > 0:
		return fmt.Sprintf("the running server (pid %d)", identity.PID)
	default:
		return "the running server"
	}
}

func attachRunPromptRemote(ctx context.Context, req AttachRunPromptRequest) (*client.Remote, error) {
	rootID := config.ExplicitPersistenceRootID(req.Config)
	verdict := capabilityVerdict{}
	var identity protocol.ServerIdentity
	remote, reachable, err := remoteattach.DialHeadless(ctx, remoteattach.HeadlessRequest{
		Config:           req.Config,
		AttachTimeout:    req.AttachTimeout,
		DiscoveryTimeout: req.DiscoveryTimeout,
		DialProjectView:  req.DialProjectView,
		DialWorkspace:    req.DialWorkspace,
		Accept: func(candidate protocol.ServerIdentity) bool {
			identity = candidate
			if !rootMatches(rootID, candidate) {
				verdict.rootMismatchReason = rootMismatchReason(candidate)
				return false
			}
			if identity.ProtocolVersion == protocol.Version {
				return true
			}
			verdict.reason = incompatibilityReason(identity)
			return false
		},
		RootID: rootID,
	})
	if err != nil {
		return nil, err
	}
	if remote != nil {
		return remote, nil
	}
	if reachable {
		return nil, errors.New("reachable RunPrompt server returned no remote client")
	}
	if verdict.rootMismatchReason != "" {
		return nil, newRootMismatchServerError(verdict)
	}
	if verdict.reason != "" {
		return nil, newIncompatibleServerError(verdict)
	}
	return nil, ErrNoServerAvailable
}

// rootMatches reports whether identity satisfies the required persistence-root
// id. An empty required id means no root pin (always matches). A non-empty
// required id never matches a server that reports no/different root, since the
// whole point of the pin is to refuse attaching to an instance whose root cannot
// be confirmed (e.g. an older build that reports no root, or a different-root
// server occupying the same endpoint).
func rootMatches(rootID string, identity protocol.ServerIdentity) bool {
	return rootID == "" || identity.PersistenceRootID == rootID
}

// rootMismatchReason explains, for the verdict, why a reachable server was
// rejected for serving a different persistence root than the selected one. It
// names the other server (and pid) so the operator can identify and stop or
// reconfigure the instance occupying the endpoint.
func rootMismatchReason(identity protocol.ServerIdentity) string {
	server := describeIncompatibleServer(identity)
	if strings.TrimSpace(identity.PersistenceRootID) == "" {
		return fmt.Sprintf("%s reports no persistence root, but this client requires the selected root", server)
	}
	return fmt.Sprintf("%s serves a different persistence root than the selected one", server)
}

func newIncompatibleServerError(verdict capabilityVerdict) error {
	return &IncompatibleServerError{Reason: verdict.reason}
}

func newRootMismatchServerError(verdict capabilityVerdict) error {
	return &RootMismatchServerError{Reason: verdict.rootMismatchReason}
}
