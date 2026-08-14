package runtime

import (
	"context"
	"errors"
	"strings"
)

// CommandAcceptance serializes caller cancellation with a candidate mutation that reports whether it committed.
type CommandAcceptance func(commit func() (bool, error)) (bool, error)

func runCommandAcceptance(accept CommandAcceptance, commit func() (bool, error)) (bool, error) {
	if accept == nil {
		return commit()
	}
	return accept(commit)
}

func commandAcceptanceResult(committed bool, err error) error {
	if committed || err != nil {
		return err
	}
	return context.Canceled
}

func (e *Engine) SubmitUserMessageOrSteerWithAcceptance(ctx context.Context, text string, accept CommandAcceptance) (result UserTurnResult, queued *QueuedUserMessage, err error) {
	if strings.TrimSpace(text) == "" {
		return UserTurnResult{}, nil, errors.New("empty message")
	}
	result, err = e.submitUserMessageWithOutcome(ctx, text, nil, nil, accept)
	if errors.Is(err, ErrAgentBusy) {
		item, queueErr := e.AcceptHumanSteering(text, accept)
		if queueErr != nil {
			return UserTurnResult{}, nil, queueErr
		}
		return UserTurnResult{}, &item, nil
	}
	return result, nil, err
}

func (e *Engine) HasQueuedUserWork() bool {
	e.ensureOrchestrationCollaborators()
	if e.messageFlow.HasPendingUserInjections() {
		return true
	}
	if e.backgroundFlow != nil && e.backgroundFlow.HasPendingNotices() {
		return true
	}
	return false
}
