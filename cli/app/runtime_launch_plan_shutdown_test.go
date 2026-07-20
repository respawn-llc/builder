package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/shared/invariant"
	"core/shared/lifecyclecontract"
)

func TestRuntimeLaunchPlanCloseJoinsAttachmentBeforeRuntimeRelease(t *testing.T) {
	for _, test := range []struct {
		name       string
		closePlan  func(*runtimeLaunchPlan) error
		releaseFor func(*int, func()) (func() error, func() error)
	}{
		{
			name:      "normal",
			closePlan: func(plan *runtimeLaunchPlan) error { return plan.Close() },
			releaseFor: func(calls *int, assertReady func()) (func() error, func() error) {
				return func() error {
						assertReady()
						*calls++
						return nil
					}, func() error {
						t.Fatal("detach release ran for normal close")
						return nil
					}
			},
		},
		{
			name:      "detach_only",
			closePlan: func(plan *runtimeLaunchPlan) error { return plan.DetachOnlyClose() },
			releaseFor: func(calls *int, assertReady func()) (func() error, func() error) {
				return func() error {
						t.Fatal("normal release ran for detach-only close")
						return nil
					}, func() error {
						assertReady()
						*calls++
						return nil
					}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			streamsClosed := false
			ingress := newUIEventDispatcher(make(chan ongoingTranscriptEvent))
			dispatcher, err := newLifecycleHookDispatcher(
				[]string{"unused-hook-command"},
				lifecyclecontract.NewEncoder(invariant.NewPolicy(invariant.WithMode(invariant.ModeDiagnostic))),
			)
			if err != nil {
				t.Fatalf("construct lifecycle dispatcher: %v", err)
			}
			releaseCalls := 0
			assertReady := func() {
				if !streamsClosed {
					t.Fatal("runtime released before event streams joined")
				}
				if !ingress.Closed() {
					t.Fatal("runtime released before accepted-event ingress closed")
				}
				select {
				case <-dispatcher.done:
				default:
					t.Fatal("runtime released before lifecycle dispatcher joined")
				}
				if !dispatcher.Issues().ClosedAndEmpty() {
					t.Fatal("runtime released before lifecycle issue mailbox closed")
				}
			}
			normalRelease, detachRelease := test.releaseFor(&releaseCalls, assertReady)
			plan := &runtimeLaunchPlan{
				Wiring:                  &runtimeWiring{eventDispatcher: ingress},
				lifecycleHookDispatcher: dispatcher,
				stopEventStreams: func() {
					streamsClosed = true
				},
				close:       normalRelease,
				detachClose: detachRelease,
			}

			if err := test.closePlan(plan); err != nil {
				t.Fatalf("close runtime launch plan: %v", err)
			}
			if err := test.closePlan(plan); err != nil {
				t.Fatalf("repeat close runtime launch plan: %v", err)
			}
			if releaseCalls != 1 {
				t.Fatalf("runtime release calls = %d, want one", releaseCalls)
			}
		})
	}
}

func TestRuntimeLaunchPlanCloseCancelsActiveHookBeforeReturning(t *testing.T) {
	tempDir := t.TempDir()
	recordPath := filepath.Join(tempDir, "records.jsonl")
	readyPath := filepath.Join(tempDir, "ready")
	t.Setenv(lifecycleHookHelperEnvironmentName, "1")
	t.Setenv(lifecycleHookHelperReadyPathName, readyPath)
	t.Setenv(lifecycleHookHelperReleasePathName, filepath.Join(tempDir, "never-release"))

	dispatcher, err := newLifecycleHookDispatcher(
		[]string{
			os.Args[0],
			"-test.run=^TestLifecycleHookDispatcherHelper$",
			"--",
			recordPath,
			"hanging-close",
		},
		lifecyclecontract.NewEncoder(invariant.NewPolicy(invariant.WithMode(invariant.ModeDiagnostic))),
	)
	if err != nil {
		t.Fatalf("construct lifecycle dispatcher: %v", err)
	}
	if accepted := dispatcher.EnqueueLifecycleEnvelope(dispatcherTestEnvelope(t, 1)); !accepted {
		t.Fatal("hanging lifecycle event was rejected")
	}
	testsetup.RequireUntil(
		t,
		time.Now().Add(5*time.Second),
		10*time.Millisecond,
		func() bool {
			_, err := os.Stat(readyPath)
			return err == nil
		},
		"hanging lifecycle helper did not become ready",
	)

	ingress := newUIEventDispatcher(make(chan ongoingTranscriptEvent))
	waitResult := make(chan any, 1)
	wait := ingress.wait()
	if wait == nil {
		t.Fatal("accepted-event ingress wait is nil before close")
	}
	go func() {
		waitResult <- wait()
	}()
	released := false
	plan := &runtimeLaunchPlan{
		Wiring:                  &runtimeWiring{eventDispatcher: ingress},
		lifecycleHookDispatcher: dispatcher,
		stopEventStreams:        func() {},
		close: func() error {
			released = true
			return nil
		},
	}

	if err := plan.Close(); err != nil {
		t.Fatalf("close runtime launch plan: %v", err)
	}
	if !released {
		t.Fatal("runtime release did not run")
	}
	select {
	case message := <-waitResult:
		if message != nil {
			t.Fatalf("closed ingress returned message %T, want nil", message)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted-event ingress wait remained blocked after close")
	}
	select {
	case <-dispatcher.done:
	default:
		t.Fatal("lifecycle dispatcher worker remained alive after close")
	}
}
