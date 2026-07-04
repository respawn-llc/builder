package serverapi

import (
	"errors"

	"core/shared/clientui"
)

// ErrLimitNegative is returned when a request supplies a negative limit.
var ErrLimitNegative = errors.New("limit must be >= 0")

type SessionMainViewRequest struct {
	SessionID            string
	PendingOperationRefs []clientui.RuntimeOperationRef
}

type SessionMainViewResponse struct {
	MainView clientui.RuntimeMainView
}

func (r SessionMainViewRequest) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	for _, ref := range r.PendingOperationRefs {
		if err := ref.Validate(); err != nil {
			return err
		}
	}
	return nil
}
