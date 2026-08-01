package sessionruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/runtime"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestExactLiveExecutionRejectsRetiredCaptureWithoutCallback(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "owner-a", &plan)
	_ = attachment

	handle, entered, release := startExactLiveTestExecution(t, fixture.authority, sessionID, &plan)
	<-entered
	capture, err := fixture.authority.captureLiveExecution(sessionID)
	if err != nil {
		t.Fatalf("capture live execution: %v", err)
	}

	close(release)
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait retired execution: %v", err)
	}

	callbackCalled := false
	err = fixture.authority.admitLiveExecution(context.Background(), capture, func(context.Context, *runtime.Engine) error {
		callbackCalled = true
		return nil
	})
	if !errors.Is(err, ErrExecutionNoLongerLive) {
		t.Fatalf("stale live execution admission error = %v, want exact liveness error", err)
	}
	if callbackCalled {
		t.Fatal("stale live execution token invoked Engine callback")
	}
}

func TestExactLiveExecutionReportsNoActiveRunWithoutCallback(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "owner-a", &plan)
	_ = attachment

	_, err := fixture.authority.captureLiveExecution(sessionID)
	if !errors.Is(err, serverapi.ErrRuntimeNoActiveRun) {
		t.Fatalf("idle live execution capture error = %v, want no-active-run", err)
	}
}

func TestExactLiveExecutionRejectsReplacementExecutionFromStaleCapture(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "owner-a", &plan)
	_ = attachment

	first, entered, release := startExactLiveTestExecution(t, fixture.authority, sessionID, &plan)
	<-entered
	capture, err := fixture.authority.captureLiveExecution(sessionID)
	if err != nil {
		t.Fatalf("capture first live execution: %v", err)
	}
	close(release)
	if _, err := first.Wait(context.Background()); err != nil {
		t.Fatalf("wait first execution: %v", err)
	}

	second, secondEntered, secondRelease := startExactLiveTestExecution(t, fixture.authority, sessionID, &plan)
	<-secondEntered
	t.Cleanup(func() {
		close(secondRelease)
		if _, err := second.Wait(context.Background()); err != nil {
			t.Errorf("wait replacement execution: %v", err)
		}
	})

	callbackCalled := false
	err = fixture.authority.admitLiveExecution(context.Background(), capture, func(context.Context, *runtime.Engine) error {
		callbackCalled = true
		return nil
	})
	if !errors.Is(err, ErrExecutionNoLongerLive) {
		t.Fatalf("stale replacement admission error = %v, want exact liveness error", err)
	}
	if callbackCalled {
		t.Fatal("stale token attached to replacement execution")
	}
}

func TestExactLiveExecutionCleanupWaitsForAcquiredLeaseOnly(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "owner-a", &plan)
	_ = attachment

	handle, entered, release := startExactLiveTestExecution(t, fixture.authority, sessionID, &plan)
	<-entered
	capture, err := fixture.authority.captureLiveExecution(sessionID)
	if err != nil {
		t.Fatalf("capture live execution: %v", err)
	}

	leaseEntered := make(chan struct{})
	leaseRelease := make(chan struct{})
	leaseDone := make(chan error, 1)
	go func() {
		leaseDone <- fixture.authority.admitLiveExecution(context.Background(), capture, func(context.Context, *runtime.Engine) error {
			close(leaseEntered)
			<-leaseRelease
			return nil
		})
	}()
	<-leaseEntered

	close(release)
	waitDone := make(chan error, 1)
	go func() {
		_, err := handle.Wait(context.Background())
		waitDone <- err
	}()
	select {
	case err := <-waitDone:
		t.Fatalf("execution retired before exact lease release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(leaseRelease)
	if err := <-leaseDone; err != nil {
		t.Fatalf("exact lease callback: %v", err)
	}
	if err := <-waitDone; err != nil {
		t.Fatalf("execution cleanup after exact lease release: %v", err)
	}
}

func TestExactLiveExecutionCleanupDoesNotWaitForGenericRuntimeCallback(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "owner-a", &plan)
	_ = attachment

	handle, entered, release := startExactLiveTestExecution(t, fixture.authority, sessionID, &plan)
	<-entered
	callbackEntered := make(chan struct{})
	callbackRelease := make(chan struct{})
	callbackDone := make(chan error, 1)
	go func() {
		callbackDone <- fixture.authority.WithRuntime(context.Background(), attachment.Resource(), func(context.Context, *runtime.Engine) error {
			close(callbackEntered)
			<-callbackRelease
			return nil
		})
	}()
	<-callbackEntered

	close(release)
	select {
	case err := <-waitExecution(handle):
		if err != nil {
			t.Fatalf("generic callback execution result: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("execution cleanup waited for generic runtime callback")
	}
	close(callbackRelease)
	if err := <-callbackDone; err != nil {
		t.Fatalf("generic runtime callback: %v", err)
	}
}

func startExactLiveTestExecution(
	t *testing.T,
	authority *Authority,
	sessionID runtimeids.SessionID,
	plan *AgentRuntimePlan,
) (ExecutionHandle, <-chan struct{}, chan<- struct{}) {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Resource:   CurrentAgentResource{},
		Runtime:    plan,
		Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
			close(entered)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start exact live test execution: %v", err)
	}
	return handle, entered, release
}

func waitExecution(handle ExecutionHandle) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := handle.Wait(context.Background())
		done <- err
	}()
	return done
}
