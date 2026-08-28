package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/textutil"
)

func TestPendingWorkProjectsAcceptedMessageAndCompactionOrder(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)

	firstSteer := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessageForAutoDrain(context.Background(), "first steer")
	})
	guidance := "keep details"
	admission := runtimeinput.ManualCompactionAdmission{
		Guidance: &guidance,
	}
	requestID := runtimeids.NewCompactionRequestID()
	if _, err := engine.CompactContextAdmissionForRequestWithAcceptance(
		context.Background(),
		requestID,
		admission,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	enterSelector := "feature/pending-work"
	enterID := serverapi.NewWorktreeOperationID()
	enterAck, err := engine.ScheduleWorktreeTransition(
		context.Background(),
		enterID,
		runtimeinput.PendingWorkWorktreeTransition{
			Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
			Selector:   &enterSelector,
		},
		func(context.Context) WorktreeApplicationResult {
			return CommittedWorktreeApplication(nil)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if enterAck.OperationID != enterID {
		t.Fatalf("enter acknowledgement ID = %s, want %s", enterAck.OperationID, enterID)
	}
	secondSteer := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessageForAutoDrain(context.Background(), "second steer")
	})
	leaveID := serverapi.NewWorktreeOperationID()
	leaveAck, err := engine.ScheduleWorktreeTransition(
		context.Background(),
		leaveID,
		runtimeinput.PendingWorkWorktreeTransition{
			Transition: runtimeinput.PendingWorkWorktreeTransitionLeave,
		},
		func(context.Context) WorktreeApplicationResult {
			return CommittedWorktreeApplication(nil)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if leaveAck.OperationID != leaveID {
		t.Fatalf("leave acknowledgement ID = %s, want %s", leaveAck.OperationID, leaveID)
	}
	queued := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessage(context.Background(), "post-turn queue")
	})

	snapshot := pendingWorkTestSnapshot(t, engine)
	if len(snapshot.Items) != 6 {
		t.Fatalf("Pending Work = %+v", snapshot.Items)
	}
	if snapshot.Items[0].ID.String() != queued.ID ||
		snapshot.Items[1].ID.String() != firstSteer.ID ||
		snapshot.Items[2].Kind != runtimeinput.PendingWorkItemKindManualCompaction ||
		snapshot.Items[2].ID.String() != requestID.String() ||
		snapshot.Items[3].ID.String() != enterID.String() ||
		snapshot.Items[4].ID.String() != secondSteer.ID ||
		snapshot.Items[5].ID.String() != leaveID.String() {
		t.Fatalf("Pending Work order = %+v", snapshot.Items)
	}
	if snapshot.Items[2].ManualCompaction == nil ||
		snapshot.Items[2].ManualCompaction.Guidance == nil ||
		*snapshot.Items[2].ManualCompaction.Guidance != guidance ||
		snapshot.Items[2].CanonicalInput != "/compact keep details" {
		t.Fatalf("manual compaction = %+v", snapshot.Items[2])
	}
	if snapshot.Items[3].CanonicalInput != "/wt switch feature/pending-work" ||
		snapshot.Items[5].CanonicalInput != "/wt leave" {
		t.Fatalf("Worktree canonical inputs = %q/%q", snapshot.Items[3].CanonicalInput, snapshot.Items[5].CanonicalInput)
	}
	if snapshot.Items[0].Lane != runtimeinput.PendingWorkLaneQueue {
		t.Fatalf("post-turn item lane = %q", snapshot.Items[0].Lane)
	}

	releaseMaintenance()
}

func TestWorktreeTransitionPendingWorkLifecycle(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		transition runtimeinput.PendingWorkWorktreeTransition
		canonical  string
	}{
		{
			name: "enter",
			transition: runtimeinput.PendingWorkWorktreeTransition{
				Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
				Selector:   textutil.Value("feature/runtime-owned"),
			},
			canonical: "/wt switch feature/runtime-owned",
		},
		{
			name: "leave",
			transition: runtimeinput.PendingWorkWorktreeTransition{
				Transition: runtimeinput.PendingWorkWorktreeTransitionLeave,
			},
			canonical: "/wt leave",
		},
	} {
		t.Run(testCase.name+"/remove before start", func(t *testing.T) {
			engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
			releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)
			operationID := serverapi.NewWorktreeOperationID()
			started := make(chan struct{}, 1)

			ack, err := engine.ScheduleWorktreeTransition(
				t.Context(),
				operationID,
				testCase.transition,
				func(context.Context) WorktreeApplicationResult {
					started <- struct{}{}
					return CommittedWorktreeApplication(nil)
				},
			)
			if err != nil {
				t.Fatalf("schedule Worktree transition: %v", err)
			}
			if ack.OperationID != operationID {
				t.Fatalf("acknowledgement ID = %s, want %s", ack.OperationID, operationID)
			}
			itemID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
				return serverapi.PendingWorkItemIDFromWorktreeOperation(operationID)
			})
			snapshot := pendingWorkTestSnapshot(t, engine)
			if len(snapshot.Items) != 1 || snapshot.Items[0].ID != itemID ||
				snapshot.Items[0].CanonicalInput != testCase.canonical {
				t.Fatalf("Pending Work = %+v", snapshot.Items)
			}

			restoration, err := engine.RemovePendingWork(t.Context(), itemID)
			if err != nil || restoration.Kind != runtimeinput.PendingWorkItemKindWorktreeTransition ||
				restoration.CanonicalInput != testCase.canonical {
				t.Fatalf("remove Worktree transition = %+v/%v", restoration, err)
			}
			releaseMaintenance()
			waitEngineLifecycleTasks(t, engine)
			select {
			case <-started:
				t.Fatal("removed Worktree transition executed")
			default:
			}
		})

		t.Run(testCase.name+"/not pending after start", func(t *testing.T) {
			engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
			operationID := serverapi.NewWorktreeOperationID()
			itemID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
				return serverapi.PendingWorkItemIDFromWorktreeOperation(operationID)
			})
			started := make(chan struct{})
			release := make(chan struct{})
			finished := make(chan struct{})

			ack, err := engine.ScheduleWorktreeTransition(
				t.Context(),
				operationID,
				testCase.transition,
				func(ctx context.Context) WorktreeApplicationResult {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
						t.Errorf("started Worktree transition was canceled: %v", context.Cause(ctx))
					}
					close(finished)
					return CommittedWorktreeApplication(nil)
				},
			)
			if err != nil {
				t.Fatalf("schedule Worktree transition: %v", err)
			}
			if ack.OperationID != operationID {
				t.Fatalf("acknowledgement ID = %s, want %s", ack.OperationID, operationID)
			}
			pendingWorkTestWait(t, started, "Worktree transition start")
			if pendingWorkTestContains(pendingWorkTestSnapshot(t, engine), itemID) {
				t.Fatal("started Worktree transition remained pending")
			}
			if _, err := engine.RemovePendingWork(t.Context(), itemID); !errors.Is(err, runtimeinput.ErrPendingWorkNotPending) {
				t.Fatalf("remove started Worktree transition = %v", err)
			}
			close(release)
			pendingWorkTestWait(t, finished, "Worktree transition finish")
			waitEngineLifecycleTasks(t, engine)
		})
	}
}

func TestWorktreeTransitionPendingWorkCapacity(t *testing.T) {
	seedMixedProjection := func(t *testing.T, engine *Engine, itemCount int) {
		t.Helper()
		if itemCount < 2 {
			t.Fatalf("mixed Pending Work item count = %d, want at least 2", itemCount)
		}
		for index := range itemCount - 2 {
			if _, err := engine.QueueUserMessage(t.Context(), fmt.Sprintf("queued %d", index)); err != nil {
				t.Fatalf("queue item %d: %v", index, err)
			}
		}
		if _, err := engine.CompactContextAdmissionForRequestWithAcceptance(
			t.Context(),
			runtimeids.NewCompactionRequestID(),
			runtimeinput.ManualCompactionAdmission{},
			nil,
		); err != nil {
			t.Fatalf("schedule manual compaction: %v", err)
		}
		if _, err := engine.ScheduleWorktreeTransition(
			t.Context(),
			serverapi.NewWorktreeOperationID(),
			runtimeinput.PendingWorkWorktreeTransition{
				Transition: runtimeinput.PendingWorkWorktreeTransitionLeave,
			},
			func(context.Context) WorktreeApplicationResult {
				return CommittedWorktreeApplication(nil)
			},
		); err != nil {
			t.Fatalf("schedule Worktree transition: %v", err)
		}
		if got := len(pendingWorkTestSnapshot(t, engine).Items); got != itemCount {
			t.Fatalf("seeded Pending Work count = %d, want %d", got, itemCount)
		}
	}

	t.Run("rejects a completed mixed capacity projection", func(t *testing.T) {
		engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
		releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)
		defer releaseMaintenance()
		seedMixedProjection(t, engine, runtimeinput.PendingWorkCapacity)

		_, err := engine.ScheduleWorktreeTransition(
			t.Context(),
			serverapi.NewWorktreeOperationID(),
			runtimeinput.PendingWorkWorktreeTransition{
				Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
				Selector:   textutil.Value("feature/rejected"),
			},
			func(context.Context) WorktreeApplicationResult {
				return CommittedWorktreeApplication(nil)
			},
		)
		var typed *serverapi.PendingWorkCapacityError
		if !errors.Is(err, runtimeinput.ErrPendingWorkCapacity) || !errors.As(err, &typed) {
			t.Fatalf("capacity error = %T %v", err, err)
		}
		if got := len(pendingWorkTestSnapshot(t, engine).Items); got != runtimeinput.PendingWorkCapacity {
			t.Fatalf("Pending Work count after rejection = %d", got)
		}
	})

	t.Run("admits from a completed mixed 99 item projection", func(t *testing.T) {
		engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
		releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)
		defer releaseMaintenance()
		seedMixedProjection(t, engine, runtimeinput.PendingWorkCapacity-1)

		if _, err := engine.ScheduleWorktreeTransition(
			t.Context(),
			serverapi.NewWorktreeOperationID(),
			runtimeinput.PendingWorkWorktreeTransition{
				Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
				Selector:   textutil.Value("feature/admitted"),
			},
			func(context.Context) WorktreeApplicationResult {
				return CommittedWorktreeApplication(nil)
			},
		); err != nil {
			t.Fatalf("admit Worktree transition: %v", err)
		}
		if got := len(pendingWorkTestSnapshot(t, engine).Items); got != runtimeinput.PendingWorkCapacity {
			t.Fatalf("Pending Work count after admission = %d", got)
		}
	})

	t.Run("preserves concurrent overshoot from a completed mixed 99 item projection", func(t *testing.T) {
		engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
		releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)
		defer releaseMaintenance()
		seedMixedProjection(t, engine, runtimeinput.PendingWorkCapacity-1)

		reachedAcceptance := make(chan struct{}, 2)
		releaseAcceptance := make(chan struct{})
		accept := CommandAcceptance(func(commit func() (bool, error)) (bool, error) {
			reachedAcceptance <- struct{}{}
			<-releaseAcceptance
			return commit()
		})
		type admissionResult struct {
			operationID serverapi.WorktreeOperationID
			ack         serverapi.WorktreeScheduledAcknowledgement
			err         error
		}
		results := make(chan admissionResult, 2)
		for index := range 2 {
			index := index
			go func() {
				operationID := serverapi.NewWorktreeOperationID()
				ack, err := engine.ScheduleWorktreeTransitionWithAcceptance(
					t.Context(),
					operationID,
					runtimeinput.PendingWorkWorktreeTransition{
						Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
						Selector:   textutil.Value(fmt.Sprintf("feature/concurrent-%d", index)),
					},
					accept,
					func(context.Context) WorktreeApplicationResult {
						return CommittedWorktreeApplication(nil)
					},
				)
				results <- admissionResult{operationID: operationID, ack: ack, err: err}
			}()
		}
		pendingWorkTestWait(t, reachedAcceptance, "first Worktree acceptance")
		pendingWorkTestWait(t, reachedAcceptance, "second Worktree acceptance")
		close(releaseAcceptance)
		for range 2 {
			result := <-results
			if result.err != nil {
				t.Fatalf("concurrent Worktree admission: %v", result.err)
			}
			if result.ack.OperationID != result.operationID {
				t.Fatalf("concurrent acknowledgement ID = %s, want %s", result.ack.OperationID, result.operationID)
			}
		}
		if got := len(pendingWorkTestSnapshot(t, engine).Items); got != runtimeinput.PendingWorkCapacity+1 {
			t.Fatalf("Pending Work count after concurrent admission = %d", got)
		}
	})
}

func TestPendingWorkCapacityRejectsWithoutMutation(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	for index := range runtimeinput.PendingWorkCapacity {
		if _, err := engine.QueueUserMessage(context.Background(), fmt.Sprintf("pending %d", index)); err != nil {
			t.Fatal(err)
		}
	}
	before := pendingWorkTestSnapshot(t, engine)

	_, err := engine.QueueUserMessage(context.Background(), "rejected")
	var typed *serverapi.PendingWorkCapacityError
	if !errors.Is(err, runtimeinput.ErrPendingWorkCapacity) || !errors.As(err, &typed) {
		t.Fatalf("capacity error = %T %v", err, err)
	}
	after := pendingWorkTestSnapshot(t, engine)
	if len(after.Items) != len(before.Items) {
		t.Fatalf("Pending Work changed from %d to %d", len(before.Items), len(after.Items))
	}
	for index := range before.Items {
		if after.Items[index].ID != before.Items[index].ID {
			t.Fatalf("item %d changed from %s to %s", index, before.Items[index].ID, after.Items[index].ID)
		}
	}
}

func TestRemovePendingWorkRestoresTypedMessageAndCompactionInput(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)
	var replacementMu sync.Mutex
	var replacement runtimeinput.PendingWork
	engine.cfg.OnEvent = func(event Event) {
		if event.Kind != EventPendingWorkReplaced || event.PendingWork == nil {
			return
		}
		replacementMu.Lock()
		replacement = clonePendingWork(*event.PendingWork)
		replacementMu.Unlock()
	}

	message := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessageForAutoDrain(context.Background(), "restore message")
	})
	messageID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
		return runtimeids.ParseQueueItemID(message.ID)
	})
	restoration, err := engine.RemovePendingWork(context.Background(), messageID)
	if err != nil || restoration.Kind != runtimeinput.PendingWorkItemKindMessage ||
		restoration.CanonicalInput != "restore message" {
		t.Fatalf("message removal = %+v/%v", restoration, err)
	}
	replacementMu.Lock()
	messageReplacement := clonePendingWork(replacement)
	replacementMu.Unlock()
	if pendingWorkTestContains(messageReplacement, messageID) {
		t.Fatalf("message removal replacement = %+v", messageReplacement.Items)
	}

	guidance := "tighten spacing"
	admission := runtimeinput.ManualCompactionAdmission{
		Guidance: &guidance,
	}
	if _, err := engine.CompactContextAdmissionForRequestWithAcceptance(
		context.Background(),
		runtimeids.NewCompactionRequestID(),
		admission,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	var compaction runtimeinput.PendingWorkItem
	for _, item := range pendingWorkTestSnapshot(t, engine).Items {
		if item.Kind == runtimeinput.PendingWorkItemKindManualCompaction {
			compaction = item
		}
	}
	if compaction.ID.IsZero() {
		t.Fatal("manual compaction is absent from Pending Work")
	}
	hydrated := hydrationSnapshot(t, engine).PendingWork
	if len(hydrated.Items) != 1 || hydrated.Items[0].ID != compaction.ID {
		t.Fatalf("hydrated Pending Work = %+v", hydrated.Items)
	}
	restoration, err = engine.RemovePendingWork(context.Background(), compaction.ID)
	if err != nil || restoration.Kind != runtimeinput.PendingWorkItemKindManualCompaction ||
		restoration.CanonicalInput != "/compact tighten spacing" {
		t.Fatalf("compaction removal = %+v/%v", restoration, err)
	}
	replacementMu.Lock()
	compactionReplacement := clonePendingWork(replacement)
	replacementMu.Unlock()
	if pendingWorkTestContains(compactionReplacement, compaction.ID) {
		t.Fatalf("compaction removal replacement = %+v", compactionReplacement.Items)
	}
	if _, err := engine.RemovePendingWork(context.Background(), compaction.ID); !errors.Is(err, runtimeinput.ErrPendingWorkNotPending) {
		t.Fatalf("repeated removal = %v", err)
	}

	releaseMaintenance()
}

func TestPendingOperationalWorkLeavesProjectionBeforeDomainExecution(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)
	requestID := runtimeids.NewCompactionRequestID()
	itemID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
		return serverapi.PendingWorkItemIDFromCompactionRequest(requestID)
	})
	domainStarted := make(chan bool, 1)
	var replacementMu sync.Mutex
	var replacement runtimeinput.PendingWork
	engine.cfg.OnEvent = func(event Event) {
		switch event.Kind {
		case EventPendingWorkReplaced:
			if event.PendingWork != nil {
				replacementMu.Lock()
				replacement = clonePendingWork(*event.PendingWork)
				replacementMu.Unlock()
			}
		case EventCompactionStarted, EventCompactionFailed:
			if event.Compaction == nil || event.Compaction.RequestID == nil || *event.Compaction.RequestID != requestID {
				return
			}
			replacementMu.Lock()
			pending := pendingWorkTestContains(replacement, itemID)
			replacementMu.Unlock()
			select {
			case domainStarted <- pending:
			default:
			}
		}
	}

	if _, err := engine.CompactContextAdmissionForRequestWithAcceptance(
		context.Background(),
		requestID,
		runtimeinput.ManualCompactionAdmission{},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if !pendingWorkTestContains(pendingWorkTestSnapshot(t, engine), itemID) {
		t.Fatal("manual compaction was not pending before the boundary")
	}
	releaseMaintenance()

	select {
	case stillPending := <-domainStarted:
		if stillPending {
			t.Fatal("manual compaction remained in Pending Work after domain execution started")
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("manual compaction domain execution did not start")
	}
}

func TestPendingWorkReplacementDeliveryIsSerializedWithoutBlockingReads(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	firstReplacementStarted := make(chan struct{})
	releaseFirstReplacement := make(chan struct{})
	var firstReplacement sync.Once
	var replacementsMu sync.Mutex
	var replacements []runtimeinput.PendingWork
	engine.cfg.OnEvent = func(event Event) {
		if event.Kind != EventPendingWorkReplaced || event.PendingWork == nil {
			return
		}
		firstReplacement.Do(func() {
			close(firstReplacementStarted)
			<-releaseFirstReplacement
			event.PendingWork.Items[0].CanonicalInput = "mutated event"
		})
		replacementsMu.Lock()
		replacements = append(replacements, clonePendingWork(*event.PendingWork))
		replacementsMu.Unlock()
	}

	type admissionResult struct {
		item QueuedUserMessage
		err  error
	}
	firstDone := make(chan admissionResult, 1)
	go func() {
		item, err := engine.QueueUserMessage(context.Background(), "first")
		firstDone <- admissionResult{item: item, err: err}
	}()
	pendingWorkTestWait(t, firstReplacementStarted, "first Pending Work replacement")

	readDone := make(chan runtimeinput.PendingWork, 1)
	go func() {
		snapshot, _ := engine.PendingWorkSnapshot()
		readDone <- snapshot
	}()
	var snapshot runtimeinput.PendingWork
	select {
	case snapshot = <-readDone:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("Pending Work read blocked on replacement delivery")
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].CanonicalInput != "first" {
		t.Fatalf("latest completed Pending Work = %+v", snapshot.Items)
	}
	snapshot.Items[0].CanonicalInput = "mutated read"

	secondDone := make(chan admissionResult, 1)
	go func() {
		item, err := engine.QueueUserMessage(context.Background(), "second")
		secondDone <- admissionResult{item: item, err: err}
	}()
	close(releaseFirstReplacement)
	firstResult := <-firstDone
	secondResult := <-secondDone
	if firstResult.err != nil || secondResult.err != nil {
		t.Fatalf("admissions = %v/%v", firstResult.err, secondResult.err)
	}

	replacementsMu.Lock()
	gotReplacements := append([]runtimeinput.PendingWork(nil), replacements...)
	replacementsMu.Unlock()
	if len(gotReplacements) != 2 ||
		len(gotReplacements[0].Items) != 1 ||
		len(gotReplacements[1].Items) != 2 ||
		gotReplacements[0].Items[0].ID.String() != firstResult.item.ID ||
		gotReplacements[1].Items[0].ID.String() != firstResult.item.ID ||
		gotReplacements[1].Items[1].ID.String() != secondResult.item.ID {
		t.Fatalf("serialized replacements = %+v", gotReplacements)
	}
	current := pendingWorkTestSnapshot(t, engine)
	if current.Items[0].CanonicalInput != "first" {
		t.Fatalf("snapshot was mutated through read/event payload: %+v", current.Items)
	}
}

func pendingWorkTestEngine(t *testing.T, cfg Config) *Engine {
	t.Helper()
	return mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), cfg)
}

func pendingWorkTestHoldMaintenance(t *testing.T, engine *Engine) func() {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- engine.stepLifecycle.Run(
			context.Background(),
			exclusiveStepOptions{ActiveKind: ActiveKindRuntimeMaintenance},
			func(context.Context, string) error {
				close(started)
				<-release
				return nil
			},
		)
	}()
	pendingWorkTestWait(t, started, "Runtime maintenance")
	return func() {
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func pendingWorkTestSnapshot(t *testing.T, engine *Engine) runtimeinput.PendingWork {
	t.Helper()
	snapshot := pendingWorkTestMust(t, engine.PendingWorkSnapshot)
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func pendingWorkTestContains(pending runtimeinput.PendingWork, id runtimeids.QueueItemID) bool {
	for _, item := range pending.Items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func pendingWorkTestMust[T any](t *testing.T, operation func() (T, error)) T {
	t.Helper()
	value, err := operation()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func pendingWorkTestWait(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatalf("%s did not complete", name)
	}
}
