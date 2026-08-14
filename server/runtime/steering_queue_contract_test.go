package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/textutil"
)

func TestSteeringOutputProvenanceIsTypedAndExactIDsAreValidated(t *testing.T) {
	intent := steerEventIntent(Event{Kind: EventStreamingErrorUpdated})

	runtimeEntry := newRuntimeOutputSteeringQueueEntry(false, intent)
	if err := runtimeEntry.validate(); err != nil {
		t.Fatalf("validate Runtime output: %v", err)
	}
	if _, ok := runtimeEntry.output.provenance.(runtimeOutputProvenance); !ok {
		t.Fatalf("Runtime output provenance = %+v", runtimeEntry.output.provenance)
	}

	for _, stepID := range []string{"", "   "} {
		entry := newExactOutputSteeringQueueEntry(stepID, false, intent)
		if err := entry.validate(); err == nil {
			t.Fatalf("exact output with Step ID %q validated", stepID)
		}
	}

	human := newHumanSteeringQueueEntry(QueuedUserMessage{
		ID: "queue-item",
		Message: llm.Message{
			Role:    llm.RoleUser,
			Content: textutil.Value("hello"),
		},
	}, true)
	queue := newSteeringQueue()
	if _, err := queue.appendHuman(human, nil, false); err != nil {
		t.Fatalf("append deferred human output: %v", err)
	}
	if err := queue.bindDeferredHumanProvenance(exactSteeringOutputProvenance("step")); err != nil {
		t.Fatalf("bind deferred human output: %v", err)
	}
	exact, ok := human.output.provenance.(exactOutputProvenance)
	if !ok || exact.stepID != "step" {
		t.Fatalf("bound human output provenance = %+v", human.output.provenance)
	}
}

func TestSteeringPersistenceRetainsRuntimeAndExactStepPresence(t *testing.T) {
	for _, test := range []struct {
		name       string
		stepID     *string
		wantStepID *string
	}{
		{name: "Runtime"},
		{
			name:       "exact",
			stepID:     textutil.OptionalExactString("11111111-1111-4111-8111-111111111111"),
			wantStepID: textutil.OptionalExactString("11111111-1111-4111-8111-111111111111"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
			intent := steerMessagesWithPersistenceIntent(
				steeringMessageEventNone,
				true,
				[]llm.Message{{
					Role:    llm.RoleDeveloper,
					Content: textutil.Value("provenance"),
				}},
			)
			var (
				receipt session.CommitReceipt
				err     error
			)
			if test.stepID == nil {
				receipt, err = engine.steerRuntimeWithCommitReceipt(intent)
			} else {
				restore := setTestActiveStep(engine, *test.stepID)
				defer restore()
				receipt, err = engine.steerWithCommitReceipt(*test.stepID, intent)
			}
			if err != nil || !receipt.Committed {
				t.Fatalf("persist Steering output = %+v, %v", receipt, err)
			}
			window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(1)
			if err != nil {
				t.Fatalf("read persisted Steering output: %v", err)
			}
			if len(window.Records) != 1 {
				t.Fatalf("persisted Steering records = %d, want 1", len(window.Records))
			}
			gotStepID := window.Records[0].StepID()
			if !equalOptionalString(gotStepID, test.wantStepID) {
				t.Fatalf("persisted Step ID = %v, want %v", gotStepID, test.wantStepID)
			}
		})
	}
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func TestSteeringQueueFIFOAndDrainingHeadContract(t *testing.T) {
	queue := newSteeringQueue()
	first := newUserShellQueueEntry("first", nil)
	second := newUserShellQueueEntry("second", nil)
	third := newUserShellQueueEntry("third", nil)
	_, _ = queue.append(first)
	_, _ = queue.append(second)
	head, ok := queue.beginNext(true)
	if !ok || head != first || !queue.pendingWork() || queue.finishDrain(nil) {
		t.Fatal("first dequeued head did not retain FIFO Draining ownership")
	}
	_, _ = queue.append(third)
	_ = queue.finishCurrent(first)
	for _, want := range []*steeringQueueEntry{second, third} {
		head, ok = queue.beginNext(true)
		if !ok || head != want {
			t.Fatal("arrival during drain changed accepted FIFO order")
		}
		_ = queue.finishCurrent(want)
	}
	if !queue.finishDrain(nil) || queue.pendingWork() {
		t.Fatal("queue did not become Idle after finalizing the accepted FIFO")
	}
}

func TestSteeringQueuePanicsOnTenThousandthPendingIntent(t *testing.T) {
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseProvider:
		default:
			close(releaseProvider)
		}
	})
	client := &hookClient{
		response: commentaryResponse(
			"complete",
			completeNodeCall("complete", json.RawMessage(`{"commentary":"done","summary":"done"}`)),
		),
		beforeReturn: func() error {
			close(providerStarted)
			<-releaseProvider
			return nil
		},
	}
	engine := mustNewWorkflowTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		testWorkflowConfig(&fakeWorkflowController{}, config.WorkflowCompletionModeTool),
		Config{},
	)
	submitDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitWorkflowTurn(context.Background())
		submitDone <- err
	}()
	select {
	case <-providerStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("protected Workflow Step did not start")
	}

	var (
		appliedMu sync.Mutex
		applied   []int
	)
	for index := 0; index < maxPendingSteeringIntents; index++ {
		index := index
		if err := engine.SubmitWorktreeTransition(func(
			context.Context,
			func(clientui.SessionExecutionTarget, *session.WorktreeReminderState) error,
		) error {
			appliedMu.Lock()
			applied = append(applied, index)
			appliedMu.Unlock()
			return nil
		}); err != nil {
			t.Fatalf("append Steering Intent %d: %v", index+1, err)
		}
	}
	engine.steering.mu.Lock()
	pending := len(engine.steering.pending)
	engine.steering.mu.Unlock()
	if pending != maxPendingSteeringIntents {
		t.Fatalf("pending Steering Intents = %d, want %d", pending, maxPendingSteeringIntents)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("10,000th pending Steering Intent did not panic")
			}
		}()
		_ = engine.SubmitWorktreeTransition(func(
			context.Context,
			func(clientui.SessionExecutionTarget, *session.WorktreeReminderState) error,
		) error {
			return nil
		})
	}()
	engine.steering.mu.Lock()
	pending = len(engine.steering.pending)
	engine.steering.mu.Unlock()
	if pending != maxPendingSteeringIntents {
		t.Fatalf("panic changed pending Steering count to %d", pending)
	}

	close(releaseProvider)
	select {
	case err := <-submitDone:
		if err != nil {
			t.Fatalf("SubmitWorkflowTurn: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("Workflow turn did not finish after releasing the provider")
	}
	waitEngineLifecycleTasks(t, engine)
	appliedMu.Lock()
	defer appliedMu.Unlock()
	if len(applied) != maxPendingSteeringIntents {
		t.Fatalf("applied Steering Intents = %d, want %d", len(applied), maxPendingSteeringIntents)
	}
	for index, value := range applied {
		if value != index {
			t.Fatalf("applied Steering order[%d] = %d", index, value)
		}
	}
}
