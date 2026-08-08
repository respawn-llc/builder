package workflowexecution

import (
	"errors"
)

var (
	ErrNoInterruptibleExecution  = errors.New("task has no actively executing workflow scope to interrupt")
	ErrTaskExecutionNotQuiescent = errors.New("workflow task execution is not quiescent")
)
