package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"core/shared/serverapi"
)

type worktreeSetupProgressSubscriber interface {
	SubscribeWorktreeSetup(context.Context, serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error)
}

func runWorkflowMutationWithSetupProgress[T any](ctx context.Context, remote workflowCommandRemote, stderr io.Writer, mutate func(context.Context, serverapi.WorktreeSetupOperationID) (T, error)) (T, error) {
	setupOperationID := serverapi.NewWorktreeSetupOperationID()
	stopSetupProgress, err := subscribeWorktreeSetupProgress(ctx, remote, setupOperationID, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "warning: worktree setup progress subscription unavailable: %v\n", err)
		stopSetupProgress = func() error { return nil }
	}
	resp, mutateErr := mutate(ctx, setupOperationID)
	if setupProgressErr := stopSetupProgress(); setupProgressErr != nil {
		fmt.Fprintf(stderr, "warning: worktree setup progress stream ended unexpectedly: %v\n", setupProgressErr)
	}
	return resp, mutateErr
}

func subscribeWorktreeSetupProgress(ctx context.Context, remote workflowCommandRemote, setupOperationID serverapi.WorktreeSetupOperationID, stderr io.Writer) (func() error, error) {
	subscriber, ok := remote.(worktreeSetupProgressSubscriber)
	if !ok {
		return nil, errors.New("worktree setup progress subscription is unavailable")
	}
	subscription, err := subscriber.SubscribeWorktreeSetup(ctx, serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupOperationID})
	if err != nil {
		return nil, err
	}
	progressCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		defer func() { _ = subscription.Close() }()
		for {
			event, err := subscription.Next(progressCtx)
			if err != nil {
				if errors.Is(err, io.EOF) {
					done <- io.ErrUnexpectedEOF
					return
				}
				if errors.Is(err, context.Canceled) || errors.Is(progressCtx.Err(), context.Canceled) {
					done <- nil
					return
				}
				done <- err
				return
			}
			writeWorktreeSetupProgress(stderr, event)
			if event.Phase == serverapi.WorktreeSetupPhaseCompleted || event.Phase == serverapi.WorktreeSetupPhaseFailed {
				done <- nil
				return
			}
		}
	}()
	return func() error {
		cancel()
		return <-done
	}, nil
}

func writeWorktreeSetupProgress(stderr io.Writer, event serverapi.WorktreeSetupEvent) {
	if event.Phase != serverapi.WorktreeSetupPhaseStarted {
		return
	}
	fmt.Fprintf(stderr, "Waiting for worktree setup script %s in %s.\n", event.ScriptPath, event.WorktreeRoot)
}
