package serverapi

import "errors"

var ErrRuntimeUnavailable = errors.New("session runtime is unavailable")

var ErrSessionRunStarting = errors.New("session runtime is being recreated; try again once it is ready")
