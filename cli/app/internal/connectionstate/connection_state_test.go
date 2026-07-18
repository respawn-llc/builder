package connectionstate

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"testing"

	"core/shared/llmerrors"
	"core/shared/serverapi"
)

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestInteractiveConnectionClassifierUsesTypedEvidence(t *testing.T) {
	tests := []struct {
		name      string
		operation OperationKind
		err       error
		want      OutcomeKind
	}{
		{name: "success", operation: OperationUnary, want: OutcomeSuccess},
		{name: "request transport loss", operation: OperationUnary, err: &url.Error{Op: "Get", URL: "http://127.0.0.1", Err: io.EOF}, want: OutcomeConnectionLoss},
		{name: "stream transport loss", operation: OperationStream, err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("reset")}, want: OutcomeConnectionLoss},
		{name: "server operation error confirms reachability", operation: OperationUnary, err: &llmerrors.APIStatusError{StatusCode: 500}, want: OutcomeReachableOperationFailure},
		{name: "surface cancellation is inconclusive", operation: OperationUnary, err: context.Canceled, want: OutcomeSurfaceCanceled},
		{name: "timeout is inconclusive", operation: OperationUnary, err: timeoutError{}, want: OutcomeInconclusiveOperationFailure},
		{name: "stream termination is inconclusive", operation: OperationStream, err: serverapi.ErrStreamUnavailable, want: OutcomeInconclusiveOperationFailure},
		{name: "graceful stream eof is inconclusive", operation: OperationStream, err: io.EOF, want: OutcomeInconclusiveOperationFailure},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.operation, tc.err).Kind(); got != tc.want {
				t.Fatalf("Classify(%v, %T) = %v, want %v", tc.operation, tc.err, got, tc.want)
			}
		})
	}
}

func TestInteractiveConnectionOwnerOnlyClearsOnReachabilityConfirmation(t *testing.T) {
	owner := NewOwner()
	owner.ObserveUnary(&url.Error{Op: "Get", URL: "http://127.0.0.1", Err: io.EOF})
	if !owner.IsDisconnected() {
		t.Fatal("transport loss did not persist disconnect")
	}
	for _, err := range []error{context.Canceled, timeoutError{}, serverapi.ErrStreamGap} {
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
