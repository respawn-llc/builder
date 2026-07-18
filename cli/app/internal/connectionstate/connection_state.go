package connectionstate

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"sync"

	"core/shared/llmerrors"
	"core/shared/serverapi"
)

// OperationKind identifies whether an operation is a unary request or a stream.
type OperationKind uint8

const (
	OperationUnary OperationKind = iota + 1
	OperationStream
)

// OutcomeKind describes the reachability evidence supplied by an operation.
type OutcomeKind uint8

const (
	OutcomeSuccess OutcomeKind = iota + 1
	OutcomeSurfaceCanceled
	OutcomeConnectionLoss
	OutcomeReachableOperationFailure
	OutcomeInconclusiveOperationFailure
	OutcomeInvalidContract
)

// Outcome is the typed result of observing one interactive server operation.
type Outcome struct {
	kind OutcomeKind
	err  error
}

func (o Outcome) Kind() OutcomeKind { return o.kind }
func (o Outcome) Err() error        { return o.err }

func (o Outcome) ConfirmsReachability() bool {
	return o.kind == OutcomeSuccess || o.kind == OutcomeReachableOperationFailure
}

func (o Outcome) IsConnectionLoss() bool {
	return o.kind == OutcomeConnectionLoss
}

// InvalidContract reports a caller-side contract violation without changing
// connectivity state. It is intentionally distinct from an operation failure:
// a malformed response cannot confirm or deny server reachability.
func InvalidContract(err error) Outcome {
	if err == nil {
		err = errors.New("invalid connection operation contract")
	}
	return Outcome{kind: OutcomeInvalidContract, err: err}
}

// Classify determines reachability from typed transport and operation errors.
// It deliberately does not inspect rendered error text.
func Classify(operation OperationKind, err error) Outcome {
	switch operation {
	case OperationUnary, OperationStream:
	default:
		return Outcome{kind: OutcomeInvalidContract, err: errors.New("invalid connection operation kind")}
	}
	if err == nil {
		return Outcome{kind: OutcomeSuccess}
	}
	if errors.Is(err, context.Canceled) {
		return Outcome{kind: OutcomeSurfaceCanceled, err: err}
	}
	if isTimeout(err) || isStreamTermination(err) {
		return Outcome{kind: OutcomeInconclusiveOperationFailure, err: err}
	}
	if isTransportLoss(err) {
		// EOF is evidence of a failed unary response, but a stream can end
		// gracefully and provides no connectivity conclusion by itself.
		if operation == OperationStream && errors.Is(err, io.EOF) {
			return Outcome{kind: OutcomeInconclusiveOperationFailure, err: err}
		}
		return Outcome{kind: OutcomeConnectionLoss, err: err}
	}
	return Outcome{kind: OutcomeReachableOperationFailure, err: err}
}

func isStreamTermination(err error) bool {
	return errors.Is(err, serverapi.ErrStreamGap) ||
		errors.Is(err, serverapi.ErrStreamUnavailable) ||
		errors.Is(err, serverapi.ErrStreamFailed)
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isTransportLoss(err error) bool {
	var statusErr *llmerrors.APIStatusError
	if errors.As(err, &statusErr) {
		return false
	}
	if errors.Is(err, io.EOF) {
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

// Owner is the sole mutable connection lifecycle for one interactive run.
type Owner struct {
	mu           sync.RWMutex
	disconnected bool
}

func NewOwner() *Owner {
	return &Owner{}
}

func (o *Owner) Observe(operation OperationKind, err error) Outcome {
	return o.ObserveOutcome(Classify(operation, err))
}

// ObserveOutcome applies a previously classified operation outcome to the
// shared interactive lifecycle. Completion handlers use this when transport
// work and UI reduction happen on opposite sides of an async boundary.
func (o *Owner) ObserveOutcome(outcome Outcome) Outcome {
	if o == nil {
		return outcome
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	switch outcome.kind {
	case OutcomeConnectionLoss:
		o.disconnected = true
	case OutcomeSuccess, OutcomeReachableOperationFailure:
		o.disconnected = false
	}
	return outcome
}

func (o *Owner) ObserveUnary(err error) Outcome {
	return o.Observe(OperationUnary, err)
}

func (o *Owner) ObserveStream(err error) Outcome {
	return o.Observe(OperationStream, err)
}

func (o *Owner) IsDisconnected() bool {
	if o == nil {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.disconnected
}
