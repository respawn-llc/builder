package serverapi

import (
	"context"
	"errors"
	"fmt"
	"io"
)

var ErrStreamGap = errors.New("stream cursor is outside the retained range and client must rehydrate")
var ErrStreamUnavailable = errors.New("stream is unavailable")
var ErrStreamFailed = errors.New("stream failed")

var ErrSessionActivityGap = ErrStreamGap
var ErrProcessOutputGap = ErrStreamGap
var ErrSessionActivityUnavailable = ErrStreamUnavailable
var ErrProcessOutputUnavailable = ErrStreamUnavailable

type TranscriptCloseReason string

const (
	TranscriptCloseReasonSubscriberOverflow TranscriptCloseReason = "subscriber_overflow"
	TranscriptCloseReasonContractViolation  TranscriptCloseReason = "contract_violation"
)

type TranscriptStreamError struct {
	Reason TranscriptCloseReason
	Err    error
}

func NewTranscriptStreamError(reason TranscriptCloseReason, err error) TranscriptStreamError {
	return TranscriptStreamError{Reason: reason, Err: err}
}

func (e TranscriptStreamError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("transcript stream closed: %s", e.Reason)
	}
	return fmt.Sprintf("transcript stream closed: %s: %v", e.Reason, e.Err)
}

func (e TranscriptStreamError) Unwrap() error {
	return e.Err
}

func TranscriptCloseReasonOf(err error) (TranscriptCloseReason, bool) {
	var streamErr TranscriptStreamError
	if errors.As(err, &streamErr) {
		return streamErr.Reason, true
	}
	return "", false
}

func NormalizeStreamError(err error) error {
	if err == nil {
		return nil
	}
	var transcriptErr TranscriptStreamError
	if errors.As(err, &transcriptErr) {
		return err
	}
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrStreamGap) || errors.Is(err, ErrStreamUnavailable) || errors.Is(err, ErrStreamFailed) {
		return err
	}
	return errors.Join(ErrStreamFailed, err)
}
