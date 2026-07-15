package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestStartSessionActivityEventsResubscribesFromLastSequenceAfterStreamGap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{evt: clientui.Event{Sequence: 41, Kind: clientui.EventAssistantDelta, AssistantDelta: "first"}}, {err: serverapi.ErrStreamGap}}}
	resubscribed := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{evt: clientui.Event{Sequence: 42, Kind: clientui.EventRunStateChanged, RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)}}}}}
	remaining := []serverapi.SessionActivitySubscription{resubscribed}
	var requestedAfter uint64

	events, stop := startSessionActivityEvents(ctx, initial, func(_ context.Context, afterSequence uint64) (serverapi.SessionActivitySubscription, error) {
		requestedAfter = afterSequence
		if len(remaining) == 0 {
			return nil, context.Canceled
		}
		next := remaining[0]
		remaining = remaining[1:]
		return next, nil
	}, func() bool { return false }, nil)
	defer stop()

	first := waitSessionActivityEvent(t, events)
	if first.Kind != clientui.EventAssistantDelta || first.AssistantDelta != "first" {
		t.Fatalf("unexpected initial event: %+v", first)
	}

	second := waitSessionActivityEvent(t, events)
	if second.Kind != clientui.EventRunStateChanged || second.RunState == nil || !second.RunState.Lifecycle.IsRunning() {
		t.Fatalf("unexpected resubscribed event: %+v", second)
	}
	if requestedAfter != 41 {
		t.Fatalf("resubscribe cursor = %d, want 41", requestedAfter)
	}
}

func TestStartSessionActivityEventsEmitsExplicitGapWhenCursorReplayUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{evt: clientui.Event{Sequence: 41, Kind: clientui.EventAssistantDelta, AssistantDelta: "first"}}, {err: serverapi.ErrStreamGap}}}
	recovered := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{evt: clientui.Event{Sequence: 1, Kind: clientui.EventAssistantMessage}}}}
	var requestedAfter []uint64
	events, stop := startSessionActivityEvents(ctx, initial, func(_ context.Context, afterSequence uint64) (serverapi.SessionActivitySubscription, error) {
		requestedAfter = append(requestedAfter, afterSequence)
		if afterSequence > 0 {
			return nil, serverapi.ErrStreamGap
		}
		return recovered, nil
	}, func() bool { return false }, nil)
	defer stop()

	first := waitSessionActivityEvent(t, events)
	if first.Kind != clientui.EventAssistantDelta || first.AssistantDelta != "first" {
		t.Fatalf("unexpected initial event: %+v", first)
	}
	gap := waitSessionActivityEvent(t, events)
	if gap.Kind != clientui.EventStreamGap {
		t.Fatalf("expected explicit stream-gap event, got %+v", gap)
	}
	if gap.RecoveryCause != clientui.TranscriptRecoveryCauseStreamGap {
		t.Fatalf("stream-gap recovery cause = %q, want %q", gap.RecoveryCause, clientui.TranscriptRecoveryCauseStreamGap)
	}
	live := waitSessionActivityEvent(t, events)
	if live.Kind != clientui.EventAssistantMessage {
		t.Fatalf("expected live event after cursor reset, got %+v", live)
	}
	if len(requestedAfter) != 2 || requestedAfter[0] != 41 || requestedAfter[1] != 0 {
		t.Fatalf("resubscribe cursors = %+v, want [41 0]", requestedAfter)
	}
}

func TestStartSessionActivityEventsKeepsRetryingFreshSubscribeAfterCursorReplayGap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{evt: clientui.Event{Sequence: 41, Kind: clientui.EventAssistantDelta, AssistantDelta: "first"}}, {err: io.EOF}}}
	recovered := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{evt: clientui.Event{Sequence: 1, Kind: clientui.EventAssistantMessage}}}}
	var requestedAfter []uint64
	freshAttempts := 0
	events, stop := startSessionActivityEvents(ctx, initial, func(_ context.Context, afterSequence uint64) (serverapi.SessionActivitySubscription, error) {
		requestedAfter = append(requestedAfter, afterSequence)
		if afterSequence > 0 {
			return nil, serverapi.ErrStreamGap
		}
		freshAttempts++
		if freshAttempts == 1 {
			return nil, serverapi.ErrStreamGap
		}
		if freshAttempts == 2 {
			return nil, serverapi.ErrStreamUnavailable
		}
		return recovered, nil
	}, func() bool { return false }, nil)
	defer stop()

	first := waitSessionActivityEvent(t, events)
	if first.Kind != clientui.EventAssistantDelta || first.AssistantDelta != "first" {
		t.Fatalf("unexpected initial event: %+v", first)
	}
	gap := waitSessionActivityEvent(t, events)
	if gap.Kind != clientui.EventStreamGap {
		t.Fatalf("expected explicit stream-gap event, got %+v", gap)
	}
	live := waitSessionActivityEvent(t, events)
	if live.Kind != clientui.EventAssistantMessage {
		t.Fatalf("expected live event after fresh subscribe retry, got %+v", live)
	}
	if len(requestedAfter) != 4 || requestedAfter[0] != 41 || requestedAfter[1] != 0 || requestedAfter[2] != 0 || requestedAfter[3] != 0 {
		t.Fatalf("resubscribe cursors = %+v, want [41 0 0 0]", requestedAfter)
	}
}

func TestStartSessionActivityEventsEmitsExplicitGapWhenInitialStreamDropsWithoutCursor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{err: io.EOF}}}
	resubscribed := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{evt: clientui.Event{Sequence: 1, Kind: clientui.EventRunStateChanged, RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)}}}}}
	var requestedAfter []uint64
	events, stop := startSessionActivityEvents(ctx, initial, func(_ context.Context, afterSequence uint64) (serverapi.SessionActivitySubscription, error) {
		requestedAfter = append(requestedAfter, afterSequence)
		return resubscribed, nil
	}, func() bool { return false }, nil)
	defer stop()

	gap := waitSessionActivityEvent(t, events)
	if gap.Kind != clientui.EventStreamGap {
		t.Fatalf("expected explicit stream-gap event after initial stream drop, got %+v", gap)
	}
	if gap.RecoveryCause != clientui.TranscriptRecoveryCauseStreamGap {
		t.Fatalf("stream-gap recovery cause = %q, want %q", gap.RecoveryCause, clientui.TranscriptRecoveryCauseStreamGap)
	}
	if len(requestedAfter) != 1 || requestedAfter[0] != 0 {
		t.Fatalf("resubscribe cursors = %+v, want [0]", requestedAfter)
	}
	live := waitSessionActivityEvent(t, events)
	if live.Kind != clientui.EventRunStateChanged || live.RunState == nil || !live.RunState.Lifecycle.IsRunning() {
		t.Fatalf("expected live event after recovery resubscribe, got %+v", live)
	}
}

func TestStartSessionActivityEventsKeepsResubscribingAfterTransientSubscribeTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{evt: clientui.Event{Sequence: 41, Kind: clientui.EventAssistantDelta, AssistantDelta: "before drop"}}, {err: io.EOF}}}
	recovered := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{evt: clientui.Event{Sequence: 42, Kind: clientui.EventAssistantMessage}}}}
	subscribeCalls := 0
	events, stop := startSessionActivityEvents(ctx, initial, func(context.Context, uint64) (serverapi.SessionActivitySubscription, error) {
		subscribeCalls++
		if subscribeCalls == 1 {
			return nil, context.DeadlineExceeded
		}
		return recovered, nil
	}, func() bool { return false }, nil)
	defer stop()

	first := waitSessionActivityEvent(t, events)
	if first.AssistantDelta != "before drop" {
		t.Fatalf("unexpected initial event: %+v", first)
	}
	reconnected := waitSessionActivityEvent(t, events)
	if reconnected.Kind != clientui.EventAssistantMessage {
		t.Fatalf("unexpected reconnected event: %+v", reconnected)
	}
	if subscribeCalls != 2 {
		t.Fatalf("subscribe calls = %d, want 2", subscribeCalls)
	}
}

func TestStartSessionActivityEventsStopsRetryingWhenParentContextCancelsDuringResubscribe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	initial := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{err: io.EOF}}}
	subscribeCalled := make(chan struct{})
	events, stop := startSessionActivityEvents(ctx, initial, func(context.Context, uint64) (serverapi.SessionActivitySubscription, error) {
		select {
		case <-subscribeCalled:
		default:
			close(subscribeCalled)
		}
		return nil, context.DeadlineExceeded
	}, func() bool { return false }, nil)
	defer stop()

	select {
	case <-subscribeCalled:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resubscribe attempt")
	}
	cancel()

	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("expected session activity channel to close after parent cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session activity retry loop to stop after parent cancellation")
	}
}

func TestStartSessionActivityEventsResubscribeStaysIsolatedAcrossStreams(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initialA := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{evt: clientui.Event{Sequence: 1, Kind: clientui.EventAssistantDelta, AssistantDelta: "a-first"}}, {err: serverapi.ErrStreamGap}}}
	resubA := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{evt: clientui.Event{Sequence: 2, Kind: clientui.EventRunStateChanged, StepID: "step-a", RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)}}}}}
	remainingA := []serverapi.SessionActivitySubscription{resubA}

	initialB := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{{evt: clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "b-first"}}}}

	eventsA, stopA := startSessionActivityEvents(ctx, initialA, func(context.Context, uint64) (serverapi.SessionActivitySubscription, error) {
		if len(remainingA) == 0 {
			return nil, context.Canceled
		}
		next := remainingA[0]
		remainingA = remainingA[1:]
		return next, nil
	}, func() bool { return false }, nil)
	defer stopA()
	eventsB, stopB := startSessionActivityEvents(ctx, initialB, func(context.Context, uint64) (serverapi.SessionActivitySubscription, error) {
		return nil, context.Canceled
	}, func() bool { return false }, nil)
	defer stopB()

	firstA := waitSessionActivityEvent(t, eventsA)
	if firstA.AssistantDelta != "a-first" {
		t.Fatalf("unexpected initial event for stream A: %+v", firstA)
	}
	firstB := waitSessionActivityEvent(t, eventsB)
	if firstB.AssistantDelta != "b-first" {
		t.Fatalf("unexpected initial event for stream B: %+v", firstB)
	}

	secondA := waitSessionActivityEvent(t, eventsA)
	if secondA.Kind != clientui.EventRunStateChanged || secondA.StepID != "step-a" {
		t.Fatalf("unexpected post-resubscribe event for stream A: %+v", secondA)
	}

	select {
	case evt := <-eventsB:
		t.Fatalf("unexpected cross-stream event on stream B: %+v", evt)
	default:
	}
}

func TestStartPendingPromptEventsResubscribesWithoutDuplicatingPendingPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubPromptActivitySubscription{steps: []stubPromptActivityStep{{evt: clientui.PendingPromptEvent{Type: clientui.PendingPromptEventPending, PromptID: "ask-1", SessionID: "session-1", Question: "First?"}}, {err: serverapi.ErrStreamGap}}}
	resubscribed := &stubPromptActivitySubscription{steps: []stubPromptActivityStep{{evt: clientui.PendingPromptEvent{Type: clientui.PendingPromptEventPending, PromptID: "ask-1", SessionID: "session-1", Question: "First?"}}, {evt: clientui.PendingPromptEvent{Type: clientui.PendingPromptEventPending, PromptID: "ask-2", SessionID: "session-1", Question: "Second?"}}}}
	remaining := []serverapi.PromptActivitySubscription{resubscribed}

	events, stop := startPendingPromptEvents(ctx, initial, func(context.Context, clientui.ReadModelVersion) (serverapi.PromptActivitySubscription, error) {
		if len(remaining) == 0 {
			return nil, context.Canceled
		}
		next := remaining[0]
		remaining = remaining[1:]
		return next, nil
	}, stubPromptControlClient{}, nil)
	defer stop()

	first := waitPromptEventWithin(t, events, time.Second)
	if first.req.PromptID != "ask-1" || first.req.Question != "First?" {
		t.Fatalf("unexpected first prompt event: %+v", first.req)
	}

	second := waitPromptEventWithin(t, events, time.Second)
	if second.req.PromptID != "ask-2" || second.req.Question != "Second?" {
		t.Fatalf("unexpected second prompt event: %+v", second.req)
	}

	select {
	case duplicate := <-events:
		t.Fatalf("unexpected duplicate pending prompt after resubscribe: %+v", duplicate.req)
	default:
	}
}

func TestStartPendingPromptEventsResubscribeEmitsResolutionForPromptMissingFromSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initialVersion := mustPromptReadModelVersionForTest(t, 1)
	initial := &stubPromptActivitySubscription{steps: []stubPromptActivityStep{{evt: clientui.PendingPromptEvent{ReadModelVersion: initialVersion, Type: clientui.PendingPromptEventPending, PromptID: "ask-1", SessionID: "session-1", Question: "First?"}}, {err: serverapi.ErrStreamGap}}}
	resubscribed := &stubPromptActivitySubscription{steps: []stubPromptActivityStep{
		{evt: clientui.PendingPromptEvent{Type: clientui.PendingPromptEventPending, PromptID: "ask-2", SessionID: "session-1", Question: "Second?"}},
		{evt: clientui.PendingPromptEvent{Type: clientui.PendingPromptEventSnapshot, SessionID: "session-1"}},
	}}
	events, stop := startPendingPromptEvents(ctx, initial, func(_ context.Context, afterVersion clientui.ReadModelVersion) (serverapi.PromptActivitySubscription, error) {
		if afterVersion != (clientui.ReadModelVersion{}) {
			return nil, serverapi.ErrStreamGap
		}
		return resubscribed, nil
	}, stubPromptControlClient{}, nil)
	defer stop()

	first := waitPromptEventWithin(t, events, time.Second)
	if first.req.PromptID != "ask-1" {
		t.Fatalf("unexpected first prompt event: %+v", first.req)
	}
	resolved := waitPromptEventWithin(t, events, time.Second)
	if !resolved.isResolution() || resolved.promptID() != "ask-1" {
		t.Fatalf("expected resolution event for ask-1 after resubscribe, got %+v", resolved)
	}
	second := waitPromptEventWithin(t, events, time.Second)
	if second.req.PromptID != "ask-2" || second.req.Question != "Second?" {
		t.Fatalf("unexpected second prompt event: %+v", second.req)
	}
}

func TestStartPendingPromptEventsRetriesResubscribeWhenSnapshotStreamFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initialVersion := mustPromptReadModelVersionForTest(t, 1)
	initial := &stubPromptActivitySubscription{steps: []stubPromptActivityStep{{evt: clientui.PendingPromptEvent{ReadModelVersion: initialVersion, Type: clientui.PendingPromptEventPending, PromptID: "ask-1", SessionID: "session-1", Question: "First?"}}, {err: serverapi.ErrStreamGap}}}
	secondResubscribe := &stubPromptActivitySubscription{steps: []stubPromptActivityStep{
		{evt: clientui.PendingPromptEvent{Type: clientui.PendingPromptEventPending, PromptID: "ask-2", SessionID: "session-1", Question: "Second?"}},
		{evt: clientui.PendingPromptEvent{Type: clientui.PendingPromptEventSnapshot, SessionID: "session-1"}},
	}}
	snapshotCalls := 0

	events, stop := startPendingPromptEvents(ctx, initial, func(_ context.Context, afterVersion clientui.ReadModelVersion) (serverapi.PromptActivitySubscription, error) {
		if afterVersion != (clientui.ReadModelVersion{}) {
			return nil, serverapi.ErrStreamGap
		}
		snapshotCalls++
		if snapshotCalls == 1 {
			return nil, errors.New("snapshot stream unavailable")
		}
		return secondResubscribe, nil
	}, stubPromptControlClient{}, nil)
	defer stop()

	first := waitPromptEventWithin(t, events, time.Second)
	if first.req.PromptID != "ask-1" {
		t.Fatalf("unexpected first prompt event: %+v", first.req)
	}
	resolved := waitPromptEventWithin(t, events, 2*time.Second)
	if !resolved.isResolution() || resolved.promptID() != "ask-1" {
		t.Fatalf("expected resolution event for ask-1 after successful retry, got %+v", resolved)
	}
	second := waitPromptEventWithin(t, events, 2*time.Second)
	if second.req.PromptID != "ask-2" || second.req.Question != "Second?" {
		t.Fatalf("unexpected second prompt event: %+v", second.req)
	}
	if snapshotCalls != 2 {
		t.Fatalf("snapshot calls = %d, want 2", snapshotCalls)
	}
}

func TestPendingPromptAnswerRetriesOneImmutableOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubPromptActivitySubscription{steps: []stubPromptActivityStep{{evt: clientui.PendingPromptEvent{Sequence: 1, Type: clientui.PendingPromptEventPending, PromptID: "ask-1", SessionID: "session-1", Question: "First?"}}}}
	control := &retryingPromptControlClient{askErrors: []error{errors.New("transport down"), nil}}

	events, stop := startPendingPromptEvents(ctx, initial, func(context.Context, clientui.ReadModelVersion) (serverapi.PromptActivitySubscription, error) {
		return nil, context.Canceled
	}, control, nil)
	defer stop()

	first := waitPromptEventWithin(t, events, time.Second)
	if first.req.PromptID != "ask-1" {
		t.Fatalf("unexpected first prompt id: %q", first.req.PromptID)
	}
	first.reply <- askReply{response: clientui.PromptAnswer{PromptID: first.req.PromptID, Answer: "handled"}}

	waitForPromptAskCallCount(t, control, 2)
	requests := control.askRequestsSnapshot()
	if len(requests) != 2 {
		t.Fatalf("AnswerAsk requests = %d, want 2", len(requests))
	}
	if requests[0] != requests[1] {
		t.Fatalf("retried request changed: first=%+v second=%+v", requests[0], requests[1])
	}
	if _, err := runtimeids.ParseRuntimeClientRequestID(requests[0].ClientRequestID); err != nil {
		t.Fatalf("client request id is not UUIDv4: %v", err)
	}
	if requests[0].SessionID != "session-1" || requests[0].AskID != "ask-1" || requests[0].Answer != "handled" {
		t.Fatalf("unexpected answer operation: %+v", requests[0])
	}
	select {
	case retried := <-events:
		t.Fatalf("submitted answer must not be re-presented: %+v", retried.req)
	default:
	}
}

func TestPendingApprovalAnswerRetriesOneImmutableOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubPromptActivitySubscription{steps: []stubPromptActivityStep{{evt: clientui.PendingPromptEvent{
		Sequence:  1,
		Type:      clientui.PendingPromptEventPending,
		PromptID:  "approval-1",
		SessionID: "session-1",
		Question:  "Proceed?",
		Approval:  true,
		ApprovalOptions: []clientui.ApprovalOption{
			{Decision: clientui.ApprovalDecisionAllowOnce, Label: "Allow once"},
			{Decision: clientui.ApprovalDecisionDeny, Label: "Deny"},
		},
	}}}}
	control := &retryingPromptControlClient{approvalErrors: []error{errors.New("transport down"), nil}}

	events, stop := startPendingPromptEvents(ctx, initial, func(context.Context, clientui.ReadModelVersion) (serverapi.PromptActivitySubscription, error) {
		return nil, context.Canceled
	}, control, nil)
	defer stop()

	first := waitPromptEventWithin(t, events, time.Second)
	first.reply <- askReply{response: clientui.PromptAnswer{
		PromptID: first.req.PromptID,
		Approval: &clientui.ApprovalPromptAnswer{
			Decision:   clientui.ApprovalDecisionAllowOnce,
			Commentary: "carefully",
		},
	}}

	waitForPromptApprovalCallCount(t, control, 2)
	requests := control.approvalRequestsSnapshot()
	if len(requests) != 2 {
		t.Fatalf("AnswerApproval requests = %d, want 2", len(requests))
	}
	if requests[0] != requests[1] {
		t.Fatalf("retried request changed: first=%+v second=%+v", requests[0], requests[1])
	}
	if _, err := runtimeids.ParseRuntimeClientRequestID(requests[0].ClientRequestID); err != nil {
		t.Fatalf("client request id is not UUIDv4: %v", err)
	}
	if requests[0].SessionID != "session-1" ||
		requests[0].ApprovalID != "approval-1" ||
		requests[0].Decision != clientui.ApprovalDecisionAllowOnce ||
		requests[0].Commentary != "carefully" {
		t.Fatalf("unexpected approval operation: %+v", requests[0])
	}
}

func TestPendingPromptAnswerAfterStopDoesNotPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubPromptActivitySubscription{steps: []stubPromptActivityStep{{evt: clientui.PendingPromptEvent{Type: clientui.PendingPromptEventPending, PromptID: "ask-1", SessionID: "session-1", Question: "First?"}}}}
	control := &retryingPromptControlClient{askErr: errors.New("transport down")}

	events, stop := startPendingPromptEvents(ctx, initial, func(context.Context, clientui.ReadModelVersion) (serverapi.PromptActivitySubscription, error) {
		return nil, context.Canceled
	}, control, nil)

	first := waitPromptEventWithin(t, events, time.Second)
	stop()
	first.reply <- askReply{response: clientui.PromptAnswer{PromptID: first.req.PromptID, Answer: "handled"}}
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("expected prompt channel to close after stop")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for prompt channel to close")
	}
}

func TestStartPendingPromptEventsEmitsResolutionEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubPromptActivitySubscription{steps: []stubPromptActivityStep{
		{evt: clientui.PendingPromptEvent{Type: clientui.PendingPromptEventPending, PromptID: "ask-1", SessionID: "session-1", Question: "First?"}},
		{evt: clientui.PendingPromptEvent{Type: clientui.PendingPromptEventResolved, PromptID: "ask-1", SessionID: "session-1"}},
	}}

	events, stop := startPendingPromptEvents(ctx, initial, func(context.Context, clientui.ReadModelVersion) (serverapi.PromptActivitySubscription, error) {
		return nil, context.Canceled
	}, stubPromptControlClient{}, nil)
	defer stop()

	first := waitPromptEventWithin(t, events, time.Second)
	if first.req.PromptID != "ask-1" {
		t.Fatalf("unexpected first prompt event: %+v", first.req)
	}
	resolved := waitPromptEventWithin(t, events, time.Second)
	if !resolved.isResolution() || resolved.promptID() != "ask-1" {
		t.Fatalf("expected resolution event for ask-1, got %+v", resolved)
	}
}

func TestStartPendingPromptEventsFallbackNotifiesFromPromptActivity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubPromptActivitySubscription{steps: []stubPromptActivityStep{{evt: clientui.PendingPromptEvent{Type: clientui.PendingPromptEventPending, PromptID: "ask-1", SessionID: "session-1", Question: "First?"}}}}
	hook := newRecordingAttentionNotificationHook()
	events, stop := startPendingPromptEvents(ctx, initial, func(context.Context, clientui.ReadModelVersion) (serverapi.PromptActivitySubscription, error) {
		return nil, context.Canceled
	}, stubPromptControlClient{}, hook)
	defer stop()

	first := waitPromptEventWithin(t, events, time.Second)
	if first.req.PromptID != "ask-1" {
		t.Fatalf("unexpected first prompt event: %+v", first.req)
	}
	notification := hook.wait(t, time.Second)
	if notification.Type != clientui.AttentionNotificationEventPending || notification.Pending == nil {
		t.Fatalf("unexpected fallback notification: %+v", notification)
	}
	if notification.Source != clientui.AttentionNotificationSourceLive {
		t.Fatalf("fallback notification source = %q, want live", notification.Source)
	}
	if notification.Pending.ID != (clientui.AttentionNotificationID{Kind: clientui.AttentionNotificationKindQuestion, UUID: "ask-1"}) || notification.Pending.Target.Kind != clientui.AttentionNotificationTargetSessionPrompt {
		t.Fatalf("unexpected fallback pending notification: %+v", notification.Pending)
	}
}

func TestPendingPromptAnswerDoesNotRetryTerminalError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubPromptActivitySubscription{steps: []stubPromptActivityStep{{evt: clientui.PendingPromptEvent{Type: clientui.PendingPromptEventPending, PromptID: "ask-1", SessionID: "session-1", Question: "First?"}}}}
	control := &retryingPromptControlClient{askErr: serverapi.ErrPromptAlreadyResolved}

	events, stop := startPendingPromptEvents(ctx, initial, func(context.Context, clientui.ReadModelVersion) (serverapi.PromptActivitySubscription, error) {
		return nil, context.Canceled
	}, control, nil)
	defer stop()

	first := waitPromptEventWithin(t, events, time.Second)
	first.reply <- askReply{response: clientui.PromptAnswer{PromptID: first.req.PromptID, Answer: "handled"}}
	waitForPromptAskCallCount(t, control, 1)
	select {
	case retried := <-events:
		t.Fatalf("did not expect retry after terminal prompt error: %+v", retried.req)
	default:
	}
	if got := control.askCallCount(); got != 1 {
		t.Fatalf("AnswerAsk call count = %d, want 1", got)
	}
}

func TestPendingPromptAnswerRejectsCompletionAfterPromptResolved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubPromptActivitySubscription{steps: []stubPromptActivityStep{
		{evt: clientui.PendingPromptEvent{Type: clientui.PendingPromptEventPending, PromptID: "ask-1", SessionID: "session-1", Question: "First?"}},
		{evt: clientui.PendingPromptEvent{Type: clientui.PendingPromptEventResolved, PromptID: "ask-1", SessionID: "session-1"}},
	}}
	control := &retryingPromptControlClient{askErr: errors.New("transport down")}

	events, stop := startPendingPromptEvents(ctx, initial, func(context.Context, clientui.ReadModelVersion) (serverapi.PromptActivitySubscription, error) {
		return nil, context.Canceled
	}, control, nil)
	defer stop()

	first := waitPromptEventWithin(t, events, time.Second)
	if first.req.PromptID != "ask-1" {
		t.Fatalf("unexpected first prompt event: %+v", first.req)
	}

	resolved := waitPromptEventWithin(t, events, time.Second)
	if !resolved.isResolution() || resolved.promptID() != "ask-1" {
		t.Fatalf("expected prompt resolution event, got %+v", resolved)
	}
	first.reply <- askReply{response: clientui.PromptAnswer{PromptID: first.req.PromptID, Answer: "handled"}}
	time.Sleep(20 * time.Millisecond)
	select {
	case retried := <-events:
		t.Fatalf("did not expect stale retry after local resolution: %+v", retried.req)
	default:
	}
	if got := control.askCallCount(); got != 0 {
		t.Fatalf("AnswerAsk call count = %d, want 0 for stale completion", got)
	}
}

func TestPendingPromptAnswerDoesNotRetryFailureCompletedAfterResolution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subscription := newChannelPromptActivitySubscription()
	subscription.events <- clientui.PendingPromptEvent{
		Type:      clientui.PendingPromptEventPending,
		PromptID:  "ask-1",
		SessionID: "session-1",
		Question:  "First?",
	}
	control := newBlockingPromptControlClient()
	events, stop := startPendingPromptEvents(ctx, subscription, func(context.Context, clientui.ReadModelVersion) (serverapi.PromptActivitySubscription, error) {
		return nil, context.Canceled
	}, control, nil)
	defer stop()

	first := waitPromptEventWithin(t, events, time.Second)
	first.reply <- askReply{response: clientui.PromptAnswer{PromptID: first.req.PromptID, Answer: "handled"}}
	select {
	case <-control.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first AnswerAsk attempt")
	}

	subscription.events <- clientui.PendingPromptEvent{
		Type:      clientui.PendingPromptEventResolved,
		PromptID:  "ask-1",
		SessionID: "session-1",
	}
	resolved := waitPromptEventWithin(t, events, time.Second)
	if !resolved.isResolution() || resolved.promptID() != "ask-1" {
		t.Fatalf("expected prompt resolution event, got %+v", resolved)
	}

	close(control.release)
	select {
	case <-control.returned:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stale AnswerAsk attempt to return")
	}
	select {
	case req := <-control.started:
		t.Fatalf("stale failure retried after prompt resolution: %+v", req)
	case <-time.After(50 * time.Millisecond):
	}
}

type stubSessionActivityStep struct {
	evt clientui.Event
	err error
}

type stubSessionActivitySubscription struct {
	steps  []stubSessionActivityStep
	closed bool
}

func (s *stubSessionActivitySubscription) Next(ctx context.Context) (clientui.Event, error) {
	if len(s.steps) == 0 {
		<-ctx.Done()
		return clientui.Event{}, ctx.Err()
	}
	step := s.steps[0]
	s.steps = s.steps[1:]
	if step.err != nil {
		return clientui.Event{}, step.err
	}
	return step.evt, nil
}

func (s *stubSessionActivitySubscription) Close() error {
	s.closed = true
	return nil
}

type stubPromptActivityStep struct {
	evt clientui.PendingPromptEvent
	err error
}

type stubPromptActivitySubscription struct {
	steps  []stubPromptActivityStep
	closed bool
}

func (s *stubPromptActivitySubscription) Next(ctx context.Context) (clientui.PendingPromptEvent, error) {
	if len(s.steps) == 0 {
		<-ctx.Done()
		return clientui.PendingPromptEvent{}, ctx.Err()
	}
	step := s.steps[0]
	s.steps = s.steps[1:]
	if step.err != nil {
		return clientui.PendingPromptEvent{}, step.err
	}
	return step.evt, nil
}

func (s *stubPromptActivitySubscription) Close() error {
	s.closed = true
	return nil
}

type channelPromptActivitySubscription struct {
	events chan clientui.PendingPromptEvent
}

func newChannelPromptActivitySubscription() *channelPromptActivitySubscription {
	return &channelPromptActivitySubscription{events: make(chan clientui.PendingPromptEvent, 2)}
}

func (s *channelPromptActivitySubscription) Next(ctx context.Context) (clientui.PendingPromptEvent, error) {
	select {
	case <-ctx.Done():
		return clientui.PendingPromptEvent{}, ctx.Err()
	case evt := <-s.events:
		return evt, nil
	}
}

func (*channelPromptActivitySubscription) Close() error {
	return nil
}

type stubPromptControlClient struct{}

func (stubPromptControlClient) AnswerAsk(context.Context, serverapi.AskAnswerRequest) error {
	return nil
}

func (stubPromptControlClient) AnswerApproval(context.Context, serverapi.ApprovalAnswerRequest) error {
	return nil
}

type recordingAttentionNotificationHook struct {
	events chan clientui.AttentionNotificationEvent
}

func newRecordingAttentionNotificationHook() *recordingAttentionNotificationHook {
	return &recordingAttentionNotificationHook{events: make(chan clientui.AttentionNotificationEvent, 8)}
}

func (h *recordingAttentionNotificationHook) OnAttentionNotification(evt clientui.AttentionNotificationEvent) {
	h.events <- evt
}

func (h *recordingAttentionNotificationHook) wait(t *testing.T, timeout time.Duration) clientui.AttentionNotificationEvent {
	t.Helper()
	select {
	case evt := <-h.events:
		return evt
	case <-time.After(timeout):
		t.Fatal("timed out waiting for attention notification fallback")
		return clientui.AttentionNotificationEvent{}
	}
}

type retryingPromptControlClient struct {
	mu                 sync.Mutex
	askErr             error
	askErrors          []error
	askRequests        []serverapi.AskAnswerRequest
	approvalErr        error
	approvalErrors     []error
	approvalRequests   []serverapi.ApprovalAnswerRequest
	askCalls           int
	approvalCallCountV int
}

type blockingPromptControlClient struct {
	started  chan serverapi.AskAnswerRequest
	release  chan struct{}
	returned chan struct{}
}

func newBlockingPromptControlClient() *blockingPromptControlClient {
	return &blockingPromptControlClient{
		started:  make(chan serverapi.AskAnswerRequest, 2),
		release:  make(chan struct{}),
		returned: make(chan struct{}, 2),
	}
}

func (c *blockingPromptControlClient) AnswerAsk(_ context.Context, req serverapi.AskAnswerRequest) error {
	c.started <- req
	<-c.release
	c.returned <- struct{}{}
	return errors.New("transport down")
}

func (*blockingPromptControlClient) AnswerApproval(context.Context, serverapi.ApprovalAnswerRequest) error {
	return nil
}

func (c *retryingPromptControlClient) askCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.askCalls
}

func (c *retryingPromptControlClient) askRequestsSnapshot() []serverapi.AskAnswerRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]serverapi.AskAnswerRequest(nil), c.askRequests...)
}

func (c *retryingPromptControlClient) approvalCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.approvalCallCountV
}

func (c *retryingPromptControlClient) approvalRequestsSnapshot() []serverapi.ApprovalAnswerRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]serverapi.ApprovalAnswerRequest(nil), c.approvalRequests...)
}

func (c *retryingPromptControlClient) AnswerAsk(_ context.Context, req serverapi.AskAnswerRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.askCalls++
	c.askRequests = append(c.askRequests, req)
	if len(c.askErrors) > 0 {
		err := c.askErrors[0]
		c.askErrors = c.askErrors[1:]
		return err
	}
	return c.askErr
}

func (c *retryingPromptControlClient) AnswerApproval(_ context.Context, req serverapi.ApprovalAnswerRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.approvalCallCountV++
	c.approvalRequests = append(c.approvalRequests, req)
	if len(c.approvalErrors) > 0 {
		err := c.approvalErrors[0]
		c.approvalErrors = c.approvalErrors[1:]
		return err
	}
	return c.approvalErr
}

func waitSessionActivityEvent(t *testing.T, events <-chan clientui.Event) clientui.Event {
	t.Helper()
	select {
	case evt, ok := <-events:
		if !ok {
			t.Fatal("session activity channel closed before next event")
		}
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session activity event")
		return clientui.Event{}
	}
}

func waitPromptEventWithin(t *testing.T, events <-chan askEvent, timeout time.Duration) askEvent {
	t.Helper()
	select {
	case evt := <-events:
		return evt
	case <-time.After(timeout):
		t.Fatal("timed out waiting for prompt event")
		return askEvent{}
	}
}

func waitForPromptApprovalCallCount(t *testing.T, control *retryingPromptControlClient, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if control.approvalCallCount() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("AnswerApproval call count = %d, want %d", control.approvalCallCount(), want)
}

func mustPromptReadModelVersionForTest(t *testing.T, sequence uint64) clientui.ReadModelVersion {
	t.Helper()
	version, err := clientui.NewReadModelVersion("prompt-test", 1, sequence)
	if err != nil {
		t.Fatalf("NewReadModelVersion: %v", err)
	}
	return version
}

func waitForPromptAskCallCount(t *testing.T, control *retryingPromptControlClient, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := control.askCallCount(); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("AnswerAsk call count = %d, want %d", control.askCallCount(), want)
}

var _ serverapi.SessionActivitySubscription = (*stubSessionActivitySubscription)(nil)
var _ serverapi.PromptActivitySubscription = (*stubPromptActivitySubscription)(nil)

func TestStubSubscriptionsSatisfyInterfaces(t *testing.T) {
	if errors.Is(nil, context.Canceled) {
		t.Fatal("unreachable")
	}
}
