package runtime

import (
	"context"
	"strings"

	"core/server/llm"
)

type CompletionAdapter interface {
	Evaluate(ctx context.Context, final llm.Message) (objectiveOutcome, error)
}

type objectiveOutcome struct {
	Applicable bool
	Done       bool
	Complete   func(context.Context) error
	Continue   error
}

type workflowCompletionAdapter struct {
	executor *defaultStepExecutor
}

func (s *defaultStepExecutor) workflowCompletionAdapter() workflowCompletionAdapter {
	return workflowCompletionAdapter{executor: s}
}

func (a workflowCompletionAdapter) Evaluate(ctx context.Context, final llm.Message) (objectiveOutcome, error) {
	e := a.executor.engine
	if final.Phase != llm.MessagePhaseFinal {
		return objectiveOutcome{}, nil
	}
	mode, err := e.workflowCompletionMode(ctx)
	if err != nil {
		return objectiveOutcome{}, err
	}
	eval, ok := evaluateWorkflowOutputCompletion(mode, e.cfg.WorkflowRun.Contract, strings.TrimSpace(final.Content))
	if !ok {
		return objectiveOutcome{}, nil
	}
	if eval.Invalid != nil {
		return objectiveOutcome{Applicable: true, Continue: eval.Invalid}, nil
	}
	return objectiveOutcome{Applicable: true, Done: true, Complete: func(ctx context.Context) error {
		if completeErr := a.executor.completeWorkflowRunFromParsed(ctx, eval.Parsed); completeErr != nil {
			return completeErr
		}
		e.setWorkflowTerminalState(eval.Source)
		return nil
	}}, nil
}

var _ CompletionAdapter = workflowCompletionAdapter{}
