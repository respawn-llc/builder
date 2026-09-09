package client

import (
	"context"
	"errors"
	"net"
	"time"
)

const GoalRequestTimeout = 15 * time.Second

type GoalRequestTimeoutError struct {
	cause error
}

func (e GoalRequestTimeoutError) Error() string {
	return "Kent timed out waiting for the goal request. Check the goal before retrying; the server may still finish the request."
}

func (e GoalRequestTimeoutError) Unwrap() error { return e.cause }

func PresentGoalRequestError(err error) error {
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
		return GoalRequestTimeoutError{cause: err}
	}
	return err
}
