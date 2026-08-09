package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"core/shared/serverapi"
)

const workflowTaskSetupObservationTimeout = 2 * time.Minute

type worktreeSetupProgressSubscriber interface {
	SubscribeWorktreeSetup(context.Context, serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error)
}

type worktreeSetupObservation struct {
	ctx         context.Context
	cancelCause context.CancelCauseFunc
	done        <-chan worktreeSetupObservationResult
}

type worktreeSetupObservationResult struct {
	terminal *worktreeSetupTerminalObservation
	err      error
}

type worktreeSetupTerminalObservation struct {
	Event serverapi.WorktreeSetupEvent
}

func (o worktreeSetupObservation) cancel() {
	o.cancelCause(context.Canceled)
}

func (o worktreeSetupObservation) startTimeout(timeout time.Duration) {
	if timeout <= 0 {
		o.cancelCause(context.DeadlineExceeded)
		return
	}
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			o.cancelCause(context.DeadlineExceeded)
		case <-o.ctx.Done():
		}
	}()
}

type worktreeSetupObservationError struct {
	cause error
}

func (e *worktreeSetupObservationError) Error() string {
	return e.cause.Error()
}

func (e *worktreeSetupObservationError) Unwrap() error {
	return e.cause
}

func writeTaskLifecycleObservationError(
	stderr io.Writer,
	operation taskLifecycleOperation,
	command taskLifecycleCommandContext,
	err error,
) bool {
	var observationErr *worktreeSetupObservationError
	if !errors.As(err, &observationErr) {
		return false
	}
	outcome, projectionErr := projectTaskLifecycleSetupOutcome(operation, command, nil, observationErr)
	if projectionErr != nil {
		fmt.Fprintln(stderr, projectionErr)
		return true
	}
	renderTaskLifecyclePresentation(stderr, *outcome.Presentation)
	return true
}

func finishObservedTaskLifecycle(
	stderr io.Writer,
	operation taskLifecycleOperation,
	command taskLifecycleCommandContext,
	terminal *worktreeSetupTerminalObservation,
) bool {
	outcome, err := projectTaskLifecycleSetupOutcome(operation, command, terminal, nil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return false
	}
	if outcome.Presentation != nil {
		renderTaskLifecyclePresentation(stderr, *outcome.Presentation)
	}
	return outcome.Success
}

func runWorkflowMutationWithSetupProgress[T any](
	ctx context.Context,
	remote workflowCommandRemote,
	stderr io.Writer,
	mutate func(context.Context, serverapi.WorktreeSetupOperationID) (T, error),
	shouldWait func(T) bool,
) (T, *worktreeSetupTerminalObservation, error) {
	setupOperationID := serverapi.NewWorktreeSetupOperationID()
	observation, err := subscribeWorktreeSetupProgress(ctx, remote, setupOperationID, stderr)
	if err != nil {
		var zero T
		return zero, nil, &worktreeSetupObservationError{
			cause: fmt.Errorf("subscribe to Worktree Setup operation: %w", err),
		}
	}
	resp, mutateErr := mutate(ctx, setupOperationID)
	if mutateErr != nil || !shouldWait(resp) {
		observation.cancel()
		<-observation.done
		return resp, nil, mutateErr
	}
	observation.startTimeout(workflowTaskSetupObservationTimeout)
	result := <-observation.done
	if result.err != nil {
		return resp, nil, &worktreeSetupObservationError{cause: result.err}
	}
	if result.terminal == nil {
		return resp, nil, errors.New("Worktree Setup observation ended without a terminal result")
	}
	return resp, result.terminal, nil
}

func subscribeWorktreeSetupProgress(
	ctx context.Context,
	remote workflowCommandRemote,
	setupOperationID serverapi.WorktreeSetupOperationID,
	stderr io.Writer,
) (worktreeSetupObservation, error) {
	subscriber, ok := remote.(worktreeSetupProgressSubscriber)
	if !ok {
		return worktreeSetupObservation{}, errors.New("worktree setup progress subscription is unavailable")
	}
	observationCtx, cancelCause := context.WithCancelCause(ctx)
	subscription, err := subscriber.SubscribeWorktreeSetup(
		observationCtx,
		serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupOperationID},
	)
	if err != nil {
		cancelCause(context.Canceled)
		return worktreeSetupObservation{}, err
	}
	done := make(chan worktreeSetupObservationResult, 1)
	go func() {
		defer cancelCause(context.Canceled)
		defer func() { _ = subscription.Close() }()
		for {
			event, err := subscription.Next(observationCtx)
			if err != nil {
				if errors.Is(context.Cause(observationCtx), context.DeadlineExceeded) {
					done <- worktreeSetupObservationResult{err: context.DeadlineExceeded}
					return
				}
				if errors.Is(err, io.EOF) {
					done <- worktreeSetupObservationResult{err: io.ErrUnexpectedEOF}
					return
				}
				if errors.Is(err, context.Canceled) && errors.Is(observationCtx.Err(), context.Canceled) {
					done <- worktreeSetupObservationResult{}
					return
				}
				done <- worktreeSetupObservationResult{err: err}
				return
			}
			if event.SetupOperationID != setupOperationID {
				done <- worktreeSetupObservationResult{
					err: errors.New("worktree setup event operation ID does not match subscription"),
				}
				return
			}
			if err := event.Validate(); err != nil {
				done <- worktreeSetupObservationResult{err: fmt.Errorf("invalid worktree setup event: %w", err)}
				return
			}
			writeWorktreeSetupProgress(stderr, event)
			if event.Phase == serverapi.WorktreeSetupPhaseStarted {
				continue
			}
			if event.Phase == serverapi.WorktreeSetupPhaseCompleted ||
				event.Phase == serverapi.WorktreeSetupPhaseNotRequired ||
				event.Phase == serverapi.WorktreeSetupPhaseFailed {
				done <- worktreeSetupObservationResult{
					terminal: &worktreeSetupTerminalObservation{
						Event: event,
					},
				}
				return
			}
		}
	}()
	return worktreeSetupObservation{
		ctx:         observationCtx,
		cancelCause: cancelCause,
		done:        done,
	}, nil
}

func writeWorktreeSetupProgress(stderr io.Writer, event serverapi.WorktreeSetupEvent) {
	if event.Phase != serverapi.WorktreeSetupPhaseStarted {
		return
	}
	fmt.Fprintf(
		stderr,
		"Waiting for worktree setup script %s in %s.\n",
		event.Started.ScriptPath,
		event.Started.WorktreeRoot,
	)
}

func waitForWorkflowTaskRunSession(
	ctx context.Context,
	remote workflowCommandRemote,
	taskID string,
	_ string,
	timeout time.Duration,
	interval time.Duration,
) (serverapi.WorkflowTaskDetail, error) {
	if strings.TrimSpace(taskID) == "" {
		return serverapi.WorkflowTaskDetail{}, errors.New("task id is required")
	}
	if interval <= 0 {
		interval = taskStartSessionPollInterval
	}
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		detail, err := getWorkflowTaskByID(pollCtx, remote, taskID)
		if err != nil {
			if pollCtx.Err() != nil {
				return serverapi.WorkflowTaskDetail{}, fmt.Errorf(
					"started task %s but session id was not assigned within %s",
					taskID,
					timeout,
				)
			}
			return serverapi.WorkflowTaskDetail{}, fmt.Errorf(
				"started task %s but failed to load task detail while waiting for session id: %w",
				taskID,
				err,
			)
		}
		if len(detail.CurrentScripts) > 0 || len(detail.LiveSessions) > 0 {
			return detail, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-pollCtx.Done():
			timer.Stop()
			return serverapi.WorkflowTaskDetail{}, fmt.Errorf(
				"started task %s but session id was not assigned within %s",
				taskDisplayID(detail),
				timeout,
			)
		case <-timer.C:
		}
	}
}
