package runtimeattach

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"

	"core/shared/llmerrors"
	"core/shared/serverapi"
)

func IsRuntimeConnectionError(err error) bool {
	if err == nil {
		return false
	}
	var statusErr *llmerrors.APIStatusError
	if errors.As(err, &statusErr) {
		return false
	}
	if errors.Is(err, serverapi.ErrStreamGap) || errors.Is(err, serverapi.ErrStreamUnavailable) || errors.Is(err, serverapi.ErrStreamFailed) {
		return false
	}
	if IsRuntimeTimeoutError(err) {
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

func ConfirmsRuntimeReachability(err error) bool {
	if err == nil {
		return true
	}
	if IsRuntimeConnectionError(err) {
		return false
	}
	if IsRuntimeTimeoutError(err) {
		return false
	}
	if errors.Is(err, serverapi.ErrStreamGap) || errors.Is(err, serverapi.ErrStreamUnavailable) || errors.Is(err, serverapi.ErrStreamFailed) {
		return false
	}
	return true
}

func IsRuntimeTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
