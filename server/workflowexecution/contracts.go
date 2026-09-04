package workflowexecution

import (
	"context"
	"errors"
)

type WorkflowSessionOwnershipReader interface {
	SessionHasWorkflowTask(context.Context, string) (bool, error)
}

var (
	ErrNoInterruptibleExecution  = errors.New("task has no actively executing workflow scope to interrupt")
	ErrTaskExecutionNotQuiescent = errors.New("workflow task execution is not quiescent")
)
