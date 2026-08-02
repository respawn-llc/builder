package main

import (
	"errors"
	"fmt"

	"core/shared/client"
)

func closeCommandRemote(remote *client.Remote, operation string, cause error) error {
	closeErr := remote.Close()
	if closeErr == nil {
		return cause
	}
	closeFailure := fmt.Errorf("close %s remote: %w", operation, closeErr)
	if cause == nil {
		return closeFailure
	}
	return errors.Join(cause, closeFailure)
}
