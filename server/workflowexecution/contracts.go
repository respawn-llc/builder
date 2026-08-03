package workflowexecution

import (
	"errors"
)

var (
	ErrNoInterruptibleExecution = errors.New("task has no actively executing workflow scope to interrupt")
	// Deprecated: use a typed operation-specific lifecycle conflict error.
	ErrTaskExecutionNotQuiescent = errors.New("workflow task execution is not quiescent")
)
