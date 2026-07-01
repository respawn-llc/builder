package serverapi

import "errors"

var ErrRuntimeOperationCanceled = errors.New("runtime operation canceled before execution")
