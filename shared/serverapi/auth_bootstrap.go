package serverapi

import (
	"errors"
)

var ErrServerAuthRequired = errors.New("server auth is not configured")
