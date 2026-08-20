package serverapi

import (
	"errors"
	"fmt"
)

var ErrUnsupportedProvider = errors.New("unsupported llm provider")

type UnsupportedProviderError struct {
	ProviderID string
}

func (e *UnsupportedProviderError) Error() string {
	return fmt.Sprintf("%s: %s", ErrUnsupportedProvider, e.ProviderID)
}

func (e *UnsupportedProviderError) Unwrap() error {
	return ErrUnsupportedProvider
}
