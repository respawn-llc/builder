package sessionruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/runtimecommand"
)

func TestRuntimeEventLifecycle(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "idle resource owns one generation-bound queue", run: testIdleRuntimeEventOwnership},
		{name: "accepted event outlives submitting context", run: testAcceptedRuntimeEventLifetime},
		{name: "resource close settles running and waiting events", run: testRuntimeEventCloseSettlement},
		{name: "resource close joins queue-owned work", run: testRuntimeEventWorkJoin},
		{name: "replacement receives an empty independent queue", run: testRuntimeEventReplacement},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func testIdleRuntimeEventOwnership(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "runtime-events", &plan)
	t.Cleanup(func() {
		if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
			t.Errorf("close runtime: %v", err)
		}
	})

	var first RuntimeEventTarget
	if err := fixture.authority.WithRuntimeEvents(context.Background(), attachment.Resource(), func(_ context.Context, target RuntimeEventTarget) error {
		first = target
		return nil
	}); err != nil {
		t.Fatalf("read first runtime event target: %v", err)
	}
	if first.Resource != attachment.Resource() || first.Engine == nil || first.Events == nil {
		t.Fatalf("runtime event target = %+v, want exact resource, engine, and queue", first)
	}
	if _, active := fixture.authority.SessionExecution(sessionID); active {
		t.Fatal("idle runtime event target created an Exact Execution Scope")
	}

	if err := fixture.authority.WithRuntimeEvents(context.Background(), attachment.Resource(), func(_ context.Context, target RuntimeEventTarget) error {
		if target.Events != first.Events {
			t.Fatal("same Resource Generation exposed more than one Runtime Event queue")
		}
		return nil
	}); err != nil {
		t.Fatalf("read runtime event target again: %v", err)
	}
}

func testAcceptedRuntimeEventLifetime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "runtime-events", &plan)
	t.Cleanup(func() {
		if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
			t.Errorf("close runtime: %v", err)
		}
	})

	submissionCtx, cancelSubmission := context.WithCancel(context.Background())
	release := make(chan struct{})
	var accepted *runtimecommand.Deferred[string]
	if err := fixture.authority.WithRuntimeEvents(submissionCtx, attachment.Resource(), func(callbackCtx context.Context, target RuntimeEventTarget) error {
		var err error
		accepted, err = runtimecommand.Submit(callbackCtx, target.Events, "accepted", func(
			admission runtimecommand.Admission,
			value string,
			complete func(string, error),
		) error {
			select {
			case <-release:
				complete(value, nil)
			case <-admission.Context().Done():
			}
			return nil
		})
		return err
	}); err != nil {
		t.Fatalf("submit runtime event: %v", err)
	}
	cancelSubmission()
	close(release)
	if got := awaitRuntimeEventResult(t, accepted); got != "accepted" {
		t.Fatalf("accepted result = %q, want accepted", got)
	}
}

func testRuntimeEventCloseSettlement(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "runtime-events", &plan)

	started := make(chan struct{})
	var running *runtimecommand.Deferred[int]
	var waiting *runtimecommand.Deferred[int]
	if err := fixture.authority.WithRuntimeEvents(context.Background(), attachment.Resource(), func(callbackCtx context.Context, target RuntimeEventTarget) error {
		var err error
		running, err = runtimecommand.Submit(callbackCtx, target.Events, 1, func(
			admission runtimecommand.Admission,
			_ int,
			_ func(int, error),
		) error {
			close(started)
			<-admission.Context().Done()
			return admission.Context().Err()
		})
		if err != nil {
			return err
		}
		waiting, err = runtimecommand.Submit(callbackCtx, target.Events, 2, func(
			_ runtimecommand.Admission,
			value int,
			complete func(int, error),
		) error {
			complete(value, nil)
			return nil
		})
		return err
	}); err != nil {
		t.Fatalf("submit close-settlement events: %v", err)
	}
	awaitRuntimeEventSignal(t, started)

	closed := make(chan error, 1)
	go func() {
		_, err := attachment.Release(context.Background(), RuntimeReleaseClose)
		closed <- err
	}()
	assertRuntimeEventUnavailable(t, running)
	assertRuntimeEventUnavailable(t, waiting)
	if err := awaitRuntimeEventChannel(t, closed); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
}

func testRuntimeEventWorkJoin(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "runtime-events", &plan)

	workStarted := make(chan struct{})
	workStopped := make(chan struct{})
	var deferred *runtimecommand.Deferred[string]
	if err := fixture.authority.WithRuntimeEvents(context.Background(), attachment.Resource(), func(callbackCtx context.Context, target RuntimeEventTarget) error {
		var err error
		deferred, err = runtimecommand.Submit(callbackCtx, target.Events, "work", func(
			admission runtimecommand.Admission,
			_ string,
			_ func(string, error),
		) error {
			return admission.StartWork(func(ctx context.Context) {
				close(workStarted)
				<-ctx.Done()
				close(workStopped)
			})
		})
		return err
	}); err != nil {
		t.Fatalf("submit queue-owned work: %v", err)
	}
	awaitRuntimeEventSignal(t, workStarted)

	if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	select {
	case <-workStopped:
	default:
		t.Fatal("runtime close returned before queue-owned work stopped")
	}
	assertRuntimeEventUnavailable(t, deferred)
}

func testRuntimeEventReplacement(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	firstAttachment := openLifecycleRuntime(t, fixture.authority, sessionID, "runtime-events-a", &plan)
	var first RuntimeEventTarget
	if err := fixture.authority.WithRuntimeEvents(context.Background(), firstAttachment.Resource(), func(_ context.Context, target RuntimeEventTarget) error {
		first = target
		return nil
	}); err != nil {
		t.Fatalf("read first runtime target: %v", err)
	}
	if _, err := firstAttachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
		t.Fatalf("close first runtime: %v", err)
	}

	secondAttachment := openLifecycleRuntime(t, fixture.authority, sessionID, "runtime-events-b", &plan)
	t.Cleanup(func() {
		if _, err := secondAttachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
			t.Errorf("close second runtime: %v", err)
		}
	})
	var second RuntimeEventTarget
	if err := fixture.authority.WithRuntimeEvents(context.Background(), secondAttachment.Resource(), func(_ context.Context, target RuntimeEventTarget) error {
		second = target
		return nil
	}); err != nil {
		t.Fatalf("read replacement runtime target: %v", err)
	}
	if first.Resource == second.Resource || first.Events == second.Events {
		t.Fatal("replacement reused the prior Resource Generation or Runtime Event queue")
	}
	if deferred, err := runtimecommand.Submit(context.Background(), first.Events, 1, runtimeEventEcho); !errors.Is(err, runtimecommand.ErrUnavailable) || deferred != nil {
		t.Fatalf("submit through closed generation = (%v, %v), want unavailable", deferred, err)
	}
	deferred, err := runtimecommand.Submit(context.Background(), second.Events, 2, runtimeEventEcho)
	if err != nil {
		t.Fatalf("submit through replacement generation: %v", err)
	}
	if got := awaitRuntimeEventResult(t, deferred); got != 2 {
		t.Fatalf("replacement result = %d, want 2", got)
	}
}

func runtimeEventEcho(
	_ runtimecommand.Admission,
	value int,
	complete func(int, error),
) error {
	complete(value, nil)
	return nil
}

func awaitRuntimeEventResult[Result interface{}](t *testing.T, deferred *runtimecommand.Deferred[Result]) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := deferred.Await(ctx)
	if err != nil {
		t.Fatalf("await runtime event: %v", err)
	}
	return result
}

func assertRuntimeEventUnavailable[Result interface{}](t *testing.T, deferred *runtimecommand.Deferred[Result]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := deferred.Await(ctx); !errors.Is(err, runtimecommand.ErrUnavailable) {
		t.Fatalf("runtime event settlement = %v, want unavailable", err)
	}
}

func awaitRuntimeEventSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for runtime event signal")
	}
}

func awaitRuntimeEventChannel[Value interface{}](t *testing.T, values <-chan Value) Value {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for runtime event value")
		var zero Value
		return zero
	}
}
