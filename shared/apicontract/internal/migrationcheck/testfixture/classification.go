package testfixture

import "errors"

type OpenText string

type EntityID string

type DeliveryMode string

const (
	DeliveryModeImmediate DeliveryMode = "immediate"
	DeliveryModeScheduled DeliveryMode = "scheduled"
	DeliveryModeDeferred  DeliveryMode = "deferred"
)

type Request struct {
	Label        OpenText
	EntityID     EntityID
	DeliveryMode DeliveryMode
	BatchSize    int32
	Inline       *OpenText
	Queued       *OpenText
	AccountID    EntityID
}

func (r Request) Validate() error {
	if err := r.validateDeliveryChoice(); err != nil {
		return err
	}
	if r.DeliveryMode == DeliveryModeDeferred && r.BatchSize < 2 {
		return errors.New("deferred delivery requires a batch")
	}
	return validateAccountOwnership(r.AccountID)
}

func (r Request) validateDeliveryChoice() error {
	return validateDeliveryChoice(r.Inline, r.Queued)
}

type MessageLocalRequest struct {
	Label        OpenText
	DeliveryMode DeliveryMode
	BatchSize    int32
	Inline       *OpenText
	Queued       *OpenText
}

func (r MessageLocalRequest) Validate() error {
	if len(r.Label) > 24 {
		return errors.New("label exceeds its message-local bound")
	}
	if err := validateDeliveryChoice(r.Inline, r.Queued); err != nil {
		return err
	}
	if r.DeliveryMode == DeliveryModeDeferred && r.BatchSize < 2 {
		return errors.New("deferred delivery requires a batch")
	}
	return nil
}

func validateDeliveryChoice(inline *OpenText, queued *OpenText) error {
	if (inline == nil) == (queued == nil) {
		return errors.New("exactly one delivery choice is required")
	}
	return nil
}

func validateAccountOwnership(accountID EntityID) error {
	if accountID == "" {
		return errors.New("account ownership requires server state")
	}
	return nil
}
