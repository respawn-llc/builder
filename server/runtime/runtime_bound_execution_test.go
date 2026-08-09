package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/runtimecommand"
	"core/server/tools"
)

func TestRuntimeBoundHumanLaunchFailureSettlesAcceptedAgendaItem(t *testing.T) {
	launchErr := errors.New("runtime-bound launch failed")
	statuses := make(chan QueuedUserMessageStatusEvent, 2)
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:         "gpt-5",
			StepLifecycle: failingRuntimeBoundLauncher{err: launchErr},
			OnEvent: func(event Event) {
				if event.QueuedUserMessageStatus != nil {
					statuses <- *event.QueuedUserMessageStatus
				}
			},
		},
	)
	if _, err := engine.QueueUserMessage("accepted human input"); !errors.Is(err, launchErr) {
		t.Fatalf("runtime-bound human error = %v, want %v", err, launchErr)
	}
	var observed []QueuedUserMessageStatusEvent
	for len(observed) < 2 {
		select {
		case status := <-statuses:
			observed = append(observed, status)
		case <-time.After(3 * time.Second):
			t.Fatalf("runtime-bound human statuses = %+v, want accepted then failed", observed)
		}
	}
	if observed[0].Status != QueuedUserMessageAccepted ||
		observed[1].Status != QueuedUserMessageFailed ||
		observed[1].FailureReason != QueuedUserMessageFailureRuntimeUnavailable {
		t.Fatalf("runtime-bound human statuses = %+v", observed)
	}
}

type failingRuntimeBoundLauncher struct {
	err error
}

func (failingRuntimeBoundLauncher) StepBegan(
	context.Context,
	StepLifecycleSnapshot,
) error {
	return nil
}

func (failingRuntimeBoundLauncher) StepEnded(
	context.Context,
	StepLifecycleSnapshot,
) error {
	return nil
}

func (l failingRuntimeBoundLauncher) LaunchRuntimeBoundExecution(
	_ runtimecommand.Admission,
	_ func(context.Context, *Engine) error,
	_ func(error),
) error {
	return l.err
}
