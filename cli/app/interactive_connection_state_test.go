package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"testing"

	"core/shared/llmerrors"
	"core/shared/rpcwire"
	"core/shared/serverapi"
)

type interactiveConnectionTimeoutError struct{}

func (interactiveConnectionTimeoutError) Error() string   { return "timeout" }
func (interactiveConnectionTimeoutError) Timeout() bool   { return true }
func (interactiveConnectionTimeoutError) Temporary() bool { return true }

func TestInteractiveConnectionClassifierUsesTypedEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		operation interactiveConnectionOperation
		err       error
		want      interactiveConnectionOutcomeKind
	}{
		{name: "success", operation: interactiveConnectionOperationUnary, want: interactiveConnectionOutcomeSuccess},
		{name: "request transport loss", operation: interactiveConnectionOperationUnary, err: &url.Error{Op: "Get", URL: "http://127.0.0.1", Err: io.EOF}, want: interactiveConnectionOutcomeConnectionLoss},
		{name: "stream transport loss", operation: interactiveConnectionOperationStream, err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("reset")}, want: interactiveConnectionOutcomeConnectionLoss},
		{name: "normalized stream transport loss", operation: interactiveConnectionOperationStream, err: errors.Join(serverapi.ErrStreamFailed, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("reset")}), want: interactiveConnectionOutcomeConnectionLoss},
		{name: "unexpected websocket close", operation: interactiveConnectionOperationStream, err: errors.Join(serverapi.ErrStreamFailed, rpcwire.ErrTransportClosed, io.EOF), want: interactiveConnectionOutcomeConnectionLoss},
		{name: "server operation error confirms reachability", operation: interactiveConnectionOperationUnary, err: &llmerrors.APIStatusError{StatusCode: 500}, want: interactiveConnectionOutcomeReachableOperationFailure},
		{name: "surface cancellation is inconclusive", operation: interactiveConnectionOperationUnary, err: context.Canceled, want: interactiveConnectionOutcomeSurfaceCanceled},
		{name: "timeout is inconclusive", operation: interactiveConnectionOperationUnary, err: interactiveConnectionTimeoutError{}, want: interactiveConnectionOutcomeInconclusiveOperationFailure},
		{name: "stream termination is inconclusive", operation: interactiveConnectionOperationStream, err: serverapi.ErrStreamUnavailable, want: interactiveConnectionOutcomeInconclusiveOperationFailure},
		{name: "graceful stream eof is inconclusive", operation: interactiveConnectionOperationStream, err: io.EOF, want: interactiveConnectionOutcomeInconclusiveOperationFailure},
		{name: "normalized graceful stream eof is inconclusive", operation: interactiveConnectionOperationStream, err: errors.Join(serverapi.ErrStreamFailed, io.EOF), want: interactiveConnectionOutcomeInconclusiveOperationFailure},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyInteractiveConnection(tc.operation, tc.err).Kind(); got != tc.want {
				t.Fatalf("classifyInteractiveConnection(%v, %T) = %v, want %v", tc.operation, tc.err, got, tc.want)
			}
		})
	}
}

func TestInteractiveConnectionOwnerIgnoresGracefulStreamClose(t *testing.T) {
	t.Parallel()

	owner := newInteractiveConnectionOwner()
	owner.ObserveStream(errors.Join(serverapi.ErrStreamFailed, io.EOF))

	if owner.IsDisconnected() {
		t.Fatal("graceful stream close persisted a disconnect")
	}
}

func TestInteractiveConnectionOwnerOnlyClearsOnReachabilityConfirmation(t *testing.T) {
	t.Parallel()

	owner := newInteractiveConnectionOwner()
	owner.ObserveUnary(&url.Error{Op: "Get", URL: "http://127.0.0.1", Err: io.EOF})
	if !owner.IsDisconnected() {
		t.Fatal("transport loss did not persist disconnect")
	}
	for _, err := range []error{context.Canceled, interactiveConnectionTimeoutError{}, serverapi.ErrStreamGap} {
		owner.ObserveUnary(err)
		if !owner.IsDisconnected() {
			t.Fatalf("%T unexpectedly cleared disconnect", err)
		}
	}
	owner.ObserveUnary(&llmerrors.APIStatusError{StatusCode: 500})
	if owner.IsDisconnected() {
		t.Fatal("reachability-confirming operation failure did not clear disconnect")
	}
}
