package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/clientui"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type worktreeTechnicalTestError struct {
	error
}

func (worktreeTechnicalTestError) WorktreeTechnicalFailure() {}

type worktreeIndeterminateTestError struct {
	error
}

func (worktreeIndeterminateTestError) WorktreeTransitionIndeterminate() {}

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
	enterID := clientui.NewWorktreeTransitionID()
	enterAck, err := engine.ScheduleWorktreeTransition(
		context.Background(),
		enterID,
		runtimeinput.PendingWorkWorktreeTransition{
			Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
			Selector:   &enterSelector,
		},
		func(context.Context) error {
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if enterAck.OperationId != enterID.String() {
		t.Fatalf("enter acknowledgement ID = %s, want %s", enterAck.OperationId, enterID)
	}
	secondSteer := pendingWorkTestMust(t, func() (QueuedUserMessage, error) {
		return engine.QueueUserMessageForAutoDrain(context.Background(), "second steer")
	})
	leaveID := clientui.NewWorktreeTransitionID()
	leaveAck, err := engine.ScheduleWorktreeTransition(
		context.Background(),
		leaveID,
		runtimeinput.PendingWorkWorktreeTransition{
			Transition: runtimeinput.PendingWorkWorktreeTransitionLeave,
		},
		func(context.Context) error {
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if leaveAck.OperationId != leaveID.String() {
		t.Fatalf("leave acknowledgement ID = %s, want %s", leaveAck.OperationId, leaveID)
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
			operationID := clientui.NewWorktreeTransitionID()
			started := make(chan struct{}, 1)

			ack, err := engine.ScheduleWorktreeTransition(
				t.Context(),
				operationID,
				testCase.transition,
				func(context.Context) error {
					started <- struct{}{}
					return nil
				},
			)
			if err != nil {
				t.Fatalf("schedule Worktree transition: %v", err)
			}
			if ack.OperationId != operationID.String() {
				t.Fatalf("acknowledgement ID = %s, want %s", ack.OperationId, operationID)
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
			operationID := clientui.NewWorktreeTransitionID()
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
				func(ctx context.Context) error {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
						t.Errorf("started Worktree transition was canceled: %v", context.Cause(ctx))
					}
					close(finished)
					return nil
				},
			)
			if err != nil {
				t.Fatalf("schedule Worktree transition: %v", err)
			}
			if ack.OperationId != operationID.String() {
				t.Fatalf("acknowledgement ID = %s, want %s", ack.OperationId, operationID)
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
			clientui.NewWorktreeTransitionID(),
			runtimeinput.PendingWorkWorktreeTransition{
				Transition: runtimeinput.PendingWorkWorktreeTransitionLeave,
			},
			func(context.Context) error {
				return nil
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
			clientui.NewWorktreeTransitionID(),
			runtimeinput.PendingWorkWorktreeTransition{
				Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
				Selector:   textutil.Value("feature/rejected"),
			},
			func(context.Context) error {
				return nil
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
			clientui.NewWorktreeTransitionID(),
			runtimeinput.PendingWorkWorktreeTransition{
				Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
				Selector:   textutil.Value("feature/admitted"),
			},
			func(context.Context) error {
				return nil
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
			operationID clientui.WorktreeTransitionID
			ack         *worktreepb.ScheduledAcknowledgement
			err         error
		}
		results := make(chan admissionResult, 2)
		for index := range 2 {
			index := index
			go func() {
				operationID := clientui.NewWorktreeTransitionID()
				ack, err := engine.ScheduleWorktreeTransitionWithAcceptance(
					t.Context(),
					operationID,
					runtimeinput.PendingWorkWorktreeTransition{
						Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
						Selector:   textutil.Value(fmt.Sprintf("feature/concurrent-%d", index)),
					},
					accept,
					func(context.Context) error {
						return nil
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
			if result.ack.OperationId != result.operationID.String() {
				t.Fatalf("concurrent acknowledgement ID = %s, want %s", result.ack.OperationId, result.operationID)
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
	var changes atomic.Int32
	engine.cfg.OnEvent = func(event Event) {
		if event.Kind == EventPendingWorkChanged {
			changes.Add(1)
		}
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
	if pendingWorkTestContains(pendingWorkTestSnapshot(t, engine), messageID) {
		t.Fatal("removed message remained in list projection")
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
	restoration, err = engine.RemovePendingWork(context.Background(), compaction.ID)
	if err != nil || restoration.Kind != runtimeinput.PendingWorkItemKindManualCompaction ||
		restoration.CanonicalInput != "/compact tighten spacing" {
		t.Fatalf("compaction removal = %+v/%v", restoration, err)
	}
	if pendingWorkTestContains(pendingWorkTestSnapshot(t, engine), compaction.ID) {
		t.Fatal("removed compaction remained in list projection")
	}
	if _, err := engine.RemovePendingWork(context.Background(), compaction.ID); !errors.Is(err, runtimeinput.ErrPendingWorkNotPending) {
		t.Fatalf("repeated removal = %v", err)
	}

	releaseMaintenance()
	if changes.Load() < 4 {
		t.Fatalf("Pending Work Changed notifications = %d, want admission and removal notifications", changes.Load())
	}
}

func TestPendingOperationalWorkLeavesProjectionBeforeDomainExecution(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)
	requestID := runtimeids.NewCompactionRequestID()
	itemID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
		return serverapi.PendingWorkItemIDFromCompactionRequest(requestID)
	})
	domainStarted := make(chan bool, 1)
	var changes atomic.Int32
	engine.cfg.OnEvent = func(event Event) {
		switch event.Kind {
		case EventPendingWorkChanged:
			changes.Add(1)
		case EventCompactionStarted, EventCompactionFailed:
			if event.Compaction == nil || event.Compaction.RequestID == nil || *event.Compaction.RequestID != requestID {
				return
			}
			pending := pendingWorkTestContains(pendingWorkTestSnapshot(t, engine), itemID)
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
	if changes.Load() < 2 {
		t.Fatalf("Pending Work Changed notifications = %d, want admission and start", changes.Load())
	}
}

func TestPendingOperationalWorkTechnicalRestoration(t *testing.T) {
	technicalFailure := errors.New("technical application failure")
	tests := []struct {
		name            string
		run             func(*testing.T, func(Event), func() error) (runtimeids.QueueItemID, runtimeinput.PendingWorkItemKind, string)
		wantRestoration bool
		wantAbort       bool
	}{
		{
			name: "Worktree definitely unapplied technical failure",
			run: func(t *testing.T, observe func(Event), _ func() error) (runtimeids.QueueItemID, runtimeinput.PendingWorkItemKind, string) {
				t.Helper()
				engine := pendingWorkTestEngine(t, Config{Model: "gpt-5", OnEvent: observe})
				operationID := clientui.NewWorktreeTransitionID()
				itemID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
					return serverapi.PendingWorkItemIDFromWorktreeOperation(operationID)
				})
				if _, err := engine.ScheduleWorktreeTransition(
					t.Context(),
					operationID,
					runtimeinput.PendingWorkWorktreeTransition{
						Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
						Selector:   textutil.Value("feature/technical"),
					},
					func(context.Context) error {
						return worktreeTechnicalTestError{error: technicalFailure}
					},
				); err != nil {
					t.Fatalf("schedule Worktree transition: %v", err)
				}
				waitEngineLifecycleTasks(t, engine)
				return itemID, runtimeinput.PendingWorkItemKindWorktreeTransition, "/wt switch feature/technical"
			},
			wantRestoration: true,
		},
		{
			name: "manual compaction definitely unapplied technical failure",
			run: func(t *testing.T, observe func(Event), _ func() error) (runtimeids.QueueItemID, runtimeinput.PendingWorkItemKind, string) {
				t.Helper()
				client := &fakeCompactionClient{compactionErrors: []error{technicalFailure}}
				engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
					Model:          "gpt-5",
					CompactionMode: "native",
					OnEvent:        observe,
				})
				if err := steerTestActiveStep(
					engine,
					"seed-restoration",
					steerMessagesWithPersistenceIntent(
						steeringPriorityNormal,
						steeringMessageEventNone,
						true,
						[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}},
					),
				); err != nil {
					t.Fatalf("seed compaction input: %v", err)
				}
				requestID := runtimeids.NewCompactionRequestID()
				itemID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
					return serverapi.PendingWorkItemIDFromCompactionRequest(requestID)
				})
				guidance := "preserve facts"
				if _, err := engine.CompactContextAdmissionForRequestWithAcceptance(
					t.Context(),
					requestID,
					runtimeinput.ManualCompactionAdmission{Guidance: &guidance},
					nil,
				); err != nil {
					t.Fatalf("schedule manual compaction: %v", err)
				}
				waitEngineLifecycleTasks(t, engine)
				return itemID, runtimeinput.PendingWorkItemKindManualCompaction, "/compact preserve facts"
			},
			wantRestoration: true,
		},
		{
			name: "user-correctable Worktree failure",
			run: func(t *testing.T, observe func(Event), _ func() error) (runtimeids.QueueItemID, runtimeinput.PendingWorkItemKind, string) {
				t.Helper()
				engine := pendingWorkTestEngine(t, Config{Model: "gpt-5", OnEvent: observe})
				operationID := clientui.NewWorktreeTransitionID()
				itemID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
					return serverapi.PendingWorkItemIDFromWorktreeOperation(operationID)
				})
				if _, err := engine.ScheduleWorktreeTransition(
					t.Context(),
					operationID,
					runtimeinput.PendingWorkWorktreeTransition{
						Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
						Selector:   textutil.Value("missing"),
					},
					func(context.Context) error {
						return errors.New("selector not found")
					},
				); err != nil {
					t.Fatalf("schedule Worktree transition: %v", err)
				}
				waitEngineLifecycleTasks(t, engine)
				return itemID, runtimeinput.PendingWorkItemKindWorktreeTransition, "/wt switch missing"
			},
		},
		{
			name: "user-correctable manual compaction failure",
			run: func(t *testing.T, observe func(Event), _ func() error) (runtimeids.QueueItemID, runtimeinput.PendingWorkItemKind, string) {
				t.Helper()
				engine := pendingWorkTestEngine(t, Config{
					Model:          "gpt-5",
					CompactionMode: "local",
					OnEvent:        observe,
				})
				releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)
				requestID := runtimeids.NewCompactionRequestID()
				itemID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
					return serverapi.PendingWorkItemIDFromCompactionRequest(requestID)
				})
				if _, err := engine.CompactContextAdmissionForRequestWithAcceptance(
					t.Context(),
					requestID,
					runtimeinput.ManualCompactionAdmission{},
					nil,
				); err != nil {
					t.Fatalf("schedule manual compaction: %v", err)
				}
				engine.compactionRuntimeState().SetManualCompactionEligible(false)
				releaseMaintenance()
				waitEngineLifecycleTasks(t, engine)
				return itemID, runtimeinput.PendingWorkItemKindManualCompaction, "/compact"
			},
		},
		{
			name: "ordinary Worktree failure without technical marker",
			run: func(t *testing.T, observe func(Event), _ func() error) (runtimeids.QueueItemID, runtimeinput.PendingWorkItemKind, string) {
				t.Helper()
				engine := pendingWorkTestEngine(t, Config{Model: "gpt-5", OnEvent: observe})
				operationID := clientui.NewWorktreeTransitionID()
				itemID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
					return serverapi.PendingWorkItemIDFromWorktreeOperation(operationID)
				})
				if _, err := engine.ScheduleWorktreeTransition(
					t.Context(),
					operationID,
					runtimeinput.PendingWorkWorktreeTransition{
						Transition: runtimeinput.PendingWorkWorktreeTransitionLeave,
					},
					func(context.Context) error {
						return technicalFailure
					},
				); err != nil {
					t.Fatalf("schedule Worktree transition: %v", err)
				}
				waitEngineLifecycleTasks(t, engine)
				return itemID, runtimeinput.PendingWorkItemKindWorktreeTransition, "/wt leave"
			},
		},
		{
			name: "indeterminate Worktree failure does not restore",
			run: func(t *testing.T, observe func(Event), _ func() error) (runtimeids.QueueItemID, runtimeinput.PendingWorkItemKind, string) {
				t.Helper()
				engine := pendingWorkTestEngine(t, Config{Model: "gpt-5", OnEvent: observe})
				operationID := clientui.NewWorktreeTransitionID()
				itemID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
					return serverapi.PendingWorkItemIDFromWorktreeOperation(operationID)
				})
				if _, err := engine.ScheduleWorktreeTransition(
					t.Context(),
					operationID,
					runtimeinput.PendingWorkWorktreeTransition{
						Transition: runtimeinput.PendingWorkWorktreeTransitionLeave,
					},
					func(context.Context) error {
						return worktreeIndeterminateTestError{
							error: worktreeTechnicalTestError{error: technicalFailure},
						}
					},
				); err != nil {
					t.Fatalf("schedule Worktree transition: %v", err)
				}
				waitEngineLifecycleTasks(t, engine)
				return itemID, runtimeinput.PendingWorkItemKindWorktreeTransition, "/wt leave"
			},
		},
		{
			name: "explicit discard",
			run: func(t *testing.T, observe func(Event), _ func() error) (runtimeids.QueueItemID, runtimeinput.PendingWorkItemKind, string) {
				t.Helper()
				engine := pendingWorkTestEngine(t, Config{Model: "gpt-5", OnEvent: observe})
				releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)
				operationID := clientui.NewWorktreeTransitionID()
				itemID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
					return serverapi.PendingWorkItemIDFromWorktreeOperation(operationID)
				})
				if _, err := engine.ScheduleWorktreeTransition(
					t.Context(),
					operationID,
					runtimeinput.PendingWorkWorktreeTransition{
						Transition: runtimeinput.PendingWorkWorktreeTransitionLeave,
					},
					func(context.Context) error {
						return worktreeTechnicalTestError{error: technicalFailure}
					},
				); err != nil {
					t.Fatalf("schedule Worktree transition: %v", err)
				}
				if _, err := engine.RemovePendingWork(t.Context(), itemID); err != nil {
					t.Fatalf("discard Worktree transition: %v", err)
				}
				releaseMaintenance()
				waitEngineLifecycleTasks(t, engine)
				return itemID, runtimeinput.PendingWorkItemKindWorktreeTransition, "/wt leave"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			var restorations []runtimeinput.PendingWorkTechnicalRestoration
			aborts := 0
			observe := func(event Event) {
				if event.Kind != EventPendingWorkRestored || event.PendingWorkRestoration == nil {
					return
				}
				mu.Lock()
				restorations = append(restorations, *event.PendingWorkRestoration)
				mu.Unlock()
			}
			itemID, kind, canonical := test.run(t, observe, func() error {
				mu.Lock()
				aborts++
				mu.Unlock()
				return nil
			})

			mu.Lock()
			gotRestorations := append([]runtimeinput.PendingWorkTechnicalRestoration(nil), restorations...)
			gotAborts := aborts
			mu.Unlock()
			if test.wantRestoration {
				if len(gotRestorations) != 1 {
					t.Fatalf("technical restorations = %+v, want one", gotRestorations)
				}
				got := gotRestorations[0]
				if got.ItemID != itemID || got.Kind != kind || got.CanonicalInput != canonical {
					t.Fatalf("technical restoration = %+v, want id=%s kind=%s canonical=%q", got, itemID, kind, canonical)
				}
			} else if len(gotRestorations) != 0 {
				t.Fatalf("technical restorations = %+v, want none", gotRestorations)
			}
			if gotAborts != 0 != test.wantAbort {
				t.Fatalf("runtime abort count = %d, want abort=%v", gotAborts, test.wantAbort)
			}
		})
	}
}

func TestPendingWorkChangedDeliveryDoesNotBlockReadsOrIndependentMutations(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5"})
	firstChangedStarted := make(chan struct{})
	releaseFirstChanged := make(chan struct{})
	var releaseChanged sync.Once
	defer releaseChanged.Do(func() { close(releaseFirstChanged) })
	var changes atomic.Int32
	engine.cfg.OnEvent = func(event Event) {
		if event.Kind != EventPendingWorkChanged {
			return
		}
		if changes.Add(1) == 1 {
			close(firstChangedStarted)
			<-releaseFirstChanged
		}
	}

	type admissionResult struct {
		item QueuedUserMessage
		err  error
	}
	firstDone := make(chan admissionResult, 1)
	go func() {
		item, err := engine.queueUserMessageRaw("first", false, nil)
		firstDone <- admissionResult{item: item, err: err}
	}()
	pendingWorkTestWait(t, firstChangedStarted, "first Pending Work Changed")

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
		item, err := engine.queueUserMessageRaw("second", false, nil)
		secondDone <- admissionResult{item: item, err: err}
	}()
	var secondResult admissionResult
	select {
	case secondResult = <-secondDone:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("second admission blocked on Changed delivery")
	}
	releaseChanged.Do(func() { close(releaseFirstChanged) })
	firstResult := <-firstDone
	if firstResult.err != nil || secondResult.err != nil {
		t.Fatalf("admissions = %v/%v", firstResult.err, secondResult.err)
	}

	current := pendingWorkTestSnapshot(t, engine)
	if len(current.Items) != 2 || changes.Load() != 2 {
		t.Fatalf("list/notifications = %+v/%d", current.Items, changes.Load())
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
