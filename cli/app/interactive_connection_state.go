package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"sync"

	"core/shared/llmerrors"
	"core/shared/rpcwire"
	"core/shared/serverapi"
)

// interactiveConnectionOperation identifies whether an operation is unary or streaming.
type interactiveConnectionOperation uint8

const (
	interactiveConnectionOperationUnary interactiveConnectionOperation = iota + 1
	interactiveConnectionOperationStream
)

// interactiveConnectionOutcomeKind describes the reachability evidence supplied by an operation.
type interactiveConnectionOutcomeKind uint8

const (
	interactiveConnectionOutcomeSuccess interactiveConnectionOutcomeKind = iota + 1
	interactiveConnectionOutcomeSurfaceCanceled
	interactiveConnectionOutcomeConnectionLoss
	interactiveConnectionOutcomeReachableOperationFailure
	interactiveConnectionOutcomeInconclusiveOperationFailure
	interactiveConnectionOutcomeInvalidContract
)

// interactiveConnectionOutcome is the typed result of observing one interactive server operation.
type interactiveConnectionOutcome struct {
	kind interactiveConnectionOutcomeKind
	err  error
}

func (o interactiveConnectionOutcome) Kind() interactiveConnectionOutcomeKind { return o.kind }
func (o interactiveConnectionOutcome) Err() error                             { return o.err }

func (o interactiveConnectionOutcome) ConfirmsReachability() bool {
	return o.kind == interactiveConnectionOutcomeSuccess || o.kind == interactiveConnectionOutcomeReachableOperationFailure
}

func (o interactiveConnectionOutcome) IsConnectionLoss() bool {
	return o.kind == interactiveConnectionOutcomeConnectionLoss
}

// invalidInteractiveConnectionContract reports a caller-side contract violation
// without changing connectivity state.
func invalidInteractiveConnectionContract(err error) interactiveConnectionOutcome {
	if err == nil {
		err = errors.New("invalid connection operation contract")
	}
	return interactiveConnectionOutcome{kind: interactiveConnectionOutcomeInvalidContract, err: err}
}

// classifyInteractiveConnection determines reachability from typed transport and
// operation errors. It deliberately does not inspect rendered error text.
func classifyInteractiveConnection(operation interactiveConnectionOperation, err error) interactiveConnectionOutcome {
	switch operation {
	case interactiveConnectionOperationUnary, interactiveConnectionOperationStream:
	default:
		return invalidInteractiveConnectionContract(errors.New("invalid connection operation kind"))
	}
	if err == nil {
		return interactiveConnectionOutcome{kind: interactiveConnectionOutcomeSuccess}
	}
	if errors.Is(err, context.Canceled) {
		return interactiveConnectionOutcome{kind: interactiveConnectionOutcomeSurfaceCanceled, err: err}
	}
	if isInteractiveConnectionTimeout(err) {
		return interactiveConnectionOutcome{kind: interactiveConnectionOutcomeInconclusiveOperationFailure, err: err}
	}
	if isInteractiveConnectionTransportLoss(err) {
		if operation == interactiveConnectionOperationStream &&
			errors.Is(err, io.EOF) &&
			!errors.Is(err, rpcwire.ErrTransportClosed) {
			return interactiveConnectionOutcome{kind: interactiveConnectionOutcomeInconclusiveOperationFailure, err: err}
		}
		return interactiveConnectionOutcome{kind: interactiveConnectionOutcomeConnectionLoss, err: err}
	}
	if isInteractiveConnectionStreamTermination(err) {
		return interactiveConnectionOutcome{kind: interactiveConnectionOutcomeInconclusiveOperationFailure, err: err}
	}
	return interactiveConnectionOutcome{kind: interactiveConnectionOutcomeReachableOperationFailure, err: err}
}

func isInteractiveConnectionStreamTermination(err error) bool {
	return errors.Is(err, serverapi.ErrStreamGap) ||
		errors.Is(err, serverapi.ErrStreamUnavailable) ||
		errors.Is(err, serverapi.ErrStreamFailed)
}

func isInteractiveConnectionTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isInteractiveConnectionTransportLoss(err error) bool {
	var statusErr *llmerrors.APIStatusError
	if errors.As(err, &statusErr) {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	if errors.Is(err, rpcwire.ErrTransportClosed) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// interactiveConnectionOwner is the sole mutable connection lifecycle for one
// interactive run. It is app-owned because every interactive surface shares it.
type interactiveConnectionOwner struct {
	mu           sync.RWMutex
	disconnected bool
}

func newInteractiveConnectionOwner() *interactiveConnectionOwner {
	return &interactiveConnectionOwner{}
}

func (o *interactiveConnectionOwner) Observe(operation interactiveConnectionOperation, err error) interactiveConnectionOutcome {
	return o.ObserveOutcome(classifyInteractiveConnection(operation, err))
}

func (o *interactiveConnectionOwner) ObserveOutcome(outcome interactiveConnectionOutcome) interactiveConnectionOutcome {
	if o == nil {
		return outcome
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	switch outcome.kind {
	case interactiveConnectionOutcomeConnectionLoss:
		o.disconnected = true
	case interactiveConnectionOutcomeSuccess, interactiveConnectionOutcomeReachableOperationFailure:
		o.disconnected = false
	}
	return outcome
}

func (o *interactiveConnectionOwner) ObserveUnary(err error) interactiveConnectionOutcome {
	return o.Observe(interactiveConnectionOperationUnary, err)
}

func (o *interactiveConnectionOwner) ObserveStream(err error) interactiveConnectionOutcome {
	return o.Observe(interactiveConnectionOperationStream, err)
}

func (o *interactiveConnectionOwner) IsDisconnected() bool {
	if o == nil {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.disconnected
}
