package serverapi

import "errors"

var ErrRuntimeUnavailable = errors.New("session runtime is unavailable")

var ErrRuntimeNoActiveRun = errors.New("session runtime has no active live run")

var ErrRuntimeNoFinalAnswer = errors.New("session runtime live run completed without a final answer")

var ErrSessionRunStarting = errors.New("session runtime is being recreated; try again once it is ready")
