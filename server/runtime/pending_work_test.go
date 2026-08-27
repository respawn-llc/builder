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
	secondSteer := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessageForAutoDrain(context.Background(), "second steer")
	})
	queued := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessage(context.Background(), "post-turn queue")
	})

	snapshot := pendingWorkTestSnapshot(t, engine)
	if len(snapshot.Items) != 4 {
		t.Fatalf("Pending Work = %+v", snapshot.Items)
	}
	if snapshot.Items[0].ID.String() != queued.ID ||
		snapshot.Items[1].ID.String() != firstSteer.ID ||
		snapshot.Items[2].Kind != runtimeinput.PendingWorkItemKindManualCompaction ||
		snapshot.Items[2].ID.String() != requestID.String() ||
		snapshot.Items[3].ID.String() != secondSteer.ID {
		t.Fatalf("Pending Work order = %+v", snapshot.Items)
	}
	if snapshot.Items[2].ManualCompaction == nil ||
		snapshot.Items[2].ManualCompaction.Guidance == nil ||
		*snapshot.Items[2].ManualCompaction.Guidance != guidance ||
		snapshot.Items[2].CanonicalInput != "/compact keep details" {
		t.Fatalf("manual compaction = %+v", snapshot.Items[2])
	}
	if snapshot.Items[0].Lane != runtimeinput.PendingWorkLaneQueue {
		t.Fatalf("post-turn item lane = %q", snapshot.Items[0].Lane)
	}

	releaseMaintenance()
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
