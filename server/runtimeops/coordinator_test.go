package runtimeops

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

func TestCoordinatorProjectsExactInputOutcomes(t *testing.T) {
	coord := NewCoordinator()
	refs := []clientui.RuntimeOperationRef{
		testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit),
		testRuntimeOperationRef(clientui.RuntimeOperationKindQueuedMessage),
		testRuntimeOperationRef(clientui.RuntimeOperationKindUserShell),
		testRuntimeOperationRef(clientui.RuntimeOperationKindCompact),
	}

	coord.RecordCommitted("session-1", refs[0])
	coord.RecordSubmitted("session-1", refs[1])
	coord.RecordFailedWithRestore("session-1", refs[2])
	coord.RecordCanceledNotCommitted("session-1", refs[3])

	snapshot := mustFeedSnapshot(t, coord, "session-1", refs)
	assertState(t, snapshot, refs[0], clientui.RuntimeInputReconciliationCommitted)
	assertState(t, snapshot, refs[1], clientui.RuntimeInputReconciliationSubmitted)
	assertState(t, snapshot, refs[2], clientui.RuntimeInputReconciliationFailedWithRestore)
	assertState(t, snapshot, refs[3], clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestCoordinatorBindsQueuedClientAndServerIdentities(t *testing.T) {
	coord := NewCoordinator()
	clientRequestID := runtimeids.NewRuntimeClientRequestID()
	queueItemID := runtimeids.NewQueueItemID()
	ref := clientui.RuntimeOperationRef{
		Kind:            clientui.RuntimeOperationKindQueuedMessage,
		ClientRequestID: clientRequestID,
		QueueItemID:     &queueItemID,
	}

	if err := coord.RecordQueuedMessageStatus(
		"session-1",
		ref,
		clientui.RuntimeInputReconciliationSubmitted,
	); err != nil {
		t.Fatalf("RecordQueuedMessageStatus: %v", err)
	}

	byClient := mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{{
		Kind:            clientui.RuntimeOperationKindQueuedMessage,
		ClientRequestID: clientRequestID,
	}})
	if got := byClient.Operations[0].Operation; got.Kind != ref.Kind ||
		got.ClientRequestID != ref.ClientRequestID ||
		got.QueueItemID == nil ||
		*got.QueueItemID != queueItemID {
		t.Fatalf("client identity projection = %+v, want %+v", got, ref)
	}
	assertState(t, byClient, ref, clientui.RuntimeInputReconciliationSubmitted)
}

func TestTrackRetainsNoReplayAuthority(t *testing.T) {
	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	failure := errors.New("pre-acceptance failure")
	var calls atomic.Int32

	for range 2 {
		_, err := Track(coord, context.Background(), "session-1", ref, func(context.Context, Attempt) (struct{}, error) {
			calls.Add(1)
			coord.RecordFailedWithRestore("session-1", ref)
			return struct{}{}, failure
		})
		if !errors.Is(err, failure) {
			t.Fatalf("Track error = %v, want %v", err, failure)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("tracked attempts = %d, want 2 independent executions", got)
	}
	assertState(
		t,
		mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}),
		ref,
		clientui.RuntimeInputReconciliationFailedWithRestore,
	)
}

func TestTrackRejectsTargetCanceledBeforeAttempt(t *testing.T) {
	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	result, err := coord.CancelOperationTarget("session-1", ref)
	if err != nil {
		t.Fatalf("CancelOperationTarget: %v", err)
	}
	if err := result.Commit(); err != nil {
		t.Fatalf("commit cancellation: %v", err)
	}

	ran := false
	_, err = Track(coord, context.Background(), "session-1", ref, func(context.Context, Attempt) (struct{}, error) {
		ran = true
		return struct{}{}, nil
	})
	if !errors.Is(err, ErrOperationCanceled) || ran {
		t.Fatalf("canceled Track = ran:%t err:%v, want no execution and operation canceled", ran, err)
	}
}

func TestCoordinatorCancelInFlightAttempt(t *testing.T) {
	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := Track(coord, context.Background(), "session-1", ref, func(_ context.Context, attempt Attempt) (struct{}, error) {
			coord.MarkOperationActive("session-1", ref)
			close(started)
			<-attempt.Context().Done()
			return struct{}{}, attempt.Context().Err()
		})
		done <- err
	}()
	<-started

	result, err := coord.CancelOperationTarget("session-1", ref)
	if err != nil {
		t.Fatalf("CancelOperationTarget: %v", err)
	}
	if !result.InterruptActive {
		t.Fatal("active cancellation did not require exact Agent-Turn interruption")
	}
	select {
	case err := <-done:
		t.Fatalf("active attempt canceled before interrupt acceptance: %v", err)
	default:
	}
	assertState(
		t,
		mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}),
		ref,
		clientui.RuntimeInputReconciliationAccepted,
	)
	if err := result.Commit(); err != nil {
		t.Fatalf("commit active cancellation: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("tracked attempt error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for targeted cancellation")
	}
	assertState(
		t,
		mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}),
		ref,
		clientui.RuntimeInputReconciliationCanceledNotCommitted,
	)
}

func TestCoordinatorCancellationWaitsForExactMutation(t *testing.T) {
	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	mutationStarted := make(chan struct{})
	releaseMutation := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		_, err := coord.TryRecordOperationMutation(
			"session-1",
			ref,
			clientui.RuntimeInputReconciliationCommitted,
			func() (bool, error) {
				close(mutationStarted)
				<-releaseMutation
				return true, nil
			},
		)
		mutationDone <- err
	}()
	<-mutationStarted

	cancelDone := make(chan error, 1)
	go func() {
		_, err := coord.CancelOperationTarget("session-1", ref)
		cancelDone <- err
	}()
	select {
	case err := <-cancelDone:
		t.Fatalf("cancellation crossed exact mutation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseMutation)
	if err := <-mutationDone; err != nil {
		t.Fatalf("TryRecordOperationMutation: %v", err)
	}
	if err := <-cancelDone; err != nil {
		t.Fatalf("CancelOperationTarget after mutation: %v", err)
	}
	assertState(
		t,
		mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}),
		ref,
		clientui.RuntimeInputReconciliationCommitted,
	)
}

func TestCoordinatorRecordsCommittedMutationError(t *testing.T) {
	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindUserShell)
	acceptedErr := errors.New("observer failed after commit")

	committed, err := coord.TryRecordOperationMutation(
		"session-1",
		ref,
		clientui.RuntimeInputReconciliationCommitted,
		func() (bool, error) { return true, acceptedErr },
	)
	if !committed || !errors.Is(err, acceptedErr) {
		t.Fatalf("committed mutation = (%t, %v), want true/%v", committed, err, acceptedErr)
	}
	assertState(
		t,
		mustFeedSnapshot(t, coord, "session-1", []clientui.RuntimeOperationRef{ref}),
		ref,
		clientui.RuntimeInputReconciliationCommitted,
	)
}

func assertState(
	t *testing.T,
	snapshot clientui.RuntimeInputReconciliationSnapshot,
	ref clientui.RuntimeOperationRef,
	want clientui.RuntimeInputReconciliationState,
) {
	t.Helper()
	for _, record := range snapshot.Operations {
		if record.Operation.Key() == ref.Key() {
			if record.State != want {
				t.Fatalf("state for %+v = %q, want %q", ref, record.State, want)
			}
			return
		}
	}
	t.Fatalf("missing record for %+v in %+v", ref, snapshot.Operations)
}

func mustFeedSnapshot(
	t *testing.T,
	coord *Coordinator,
	sessionID string,
	refs []clientui.RuntimeOperationRef,
) clientui.RuntimeInputReconciliationSnapshot {
	t.Helper()
	snapshot, err := coord.FeedSnapshot(sessionID, refs)
	if err != nil {
		t.Fatalf("FeedSnapshot: %v", err)
	}
	return snapshot
}

func testRuntimeOperationRef(kind clientui.RuntimeOperationKind) clientui.RuntimeOperationRef {
	return clientui.RuntimeOperationRef{
		Kind:            kind,
		ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
	}
}
