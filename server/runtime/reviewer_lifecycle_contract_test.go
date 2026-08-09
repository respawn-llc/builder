package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestReviewerWorkUsesRuntimeEventStartAndResultBoundaries(t *testing.T) {
	stepID := "11111111-1111-4111-8111-111111111111"
	reviewer := newScriptedGoalLoopClient()
	var events []Event
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:    "gpt-5",
			Reviewer: ReviewerConfig{Model: "gpt-5"},
			OnEvent: func(event Event) {
				events = append(events, event)
			},
		},
	)

	releaseStartAdmission := blockRuntimeEventAdmission(t, engine.runtimeEvents)
	startBlocked := true
	defer func() {
		if startBlocked {
			releaseStartAdmission()
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, err := engine.runReviewerFollowUp(
			context.Background(),
			stepID,
			llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("original"),
			},
			0,
			false,
			reviewer,
		)
		done <- err
	}()
	reviewer.assertNotStarted(t, 1)

	releaseStartAdmission()
	startBlocked = false
	reviewer.waitStarted(t, 1)

	unrelatedApplied := make(chan struct{})
	if _, err := submitRuntimeEvent(
		engine,
		struct{}{},
		func(runtimeEventAdmission, struct{}) (struct{}, error) {
			close(unrelatedApplied)
			return struct{}{}, nil
		},
	); err != nil {
		t.Fatalf("apply unrelated Runtime Event while Reviewer is held: %v", err)
	}
	select {
	case <-unrelatedApplied:
	case <-time.After(3 * time.Second):
		t.Fatal("held Reviewer work blocked unrelated Runtime Event admission")
	}

	releaseResultAdmission := blockRuntimeEventAdmission(t, engine.runtimeEvents)
	resultBlocked := true
	defer func() {
		if resultBlocked {
			releaseResultAdmission()
		}
	}()
	reviewer.releaseCall(1)
	select {
	case err := <-done:
		t.Fatalf("Reviewer settled before its terminal result event: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseResultAdmission()
	resultBlocked = false
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run Reviewer follow-up: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Reviewer did not settle after its terminal result event")
	}

	started, completed := 0, 0
	for _, event := range events {
		switch event.Kind {
		case EventReviewerStarted:
			started++
		case EventReviewerCompleted:
			completed++
		}
	}
	if started != 1 || completed != 1 {
		t.Fatalf(
			"Reviewer lifecycle events = started:%d completed:%d, want one each",
			started,
			completed,
		)
	}
}

func TestDefaultStepExecutorOwnsReviewerLifecycleAndPropagatesFatalError(t *testing.T) {
	fatalErr := errors.New("reviewer application failed")
	var engine *Engine
	reviewer := &reviewerPipelineStub{
		runFollowUp: func(stepID string, original llm.Message) (reviewerFollowUpResult, error) {
			active := engine.reviewerRuntimeState().ActiveStepSnapshot()
			if active == nil || active.StepID != stepID {
				t.Fatalf("Reviewer was not active during pipeline execution: %+v", active)
			}
			return reviewerFollowUpResult{Message: original}, fatalErr
		},
	}
	var events []Event
	engine = mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value("original"),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}}}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})
	engine.stepFlow = &defaultStepExecutor{
		engine:   engine,
		phase:    engine.phaseProtocol,
		reviewer: reviewer,
		messages: engine.messageFlow,
		tools:    engine.toolFlow,
	}

	_, err := engine.runStepLoopWithOptions(
		context.Background(),
		"step-1",
		"all",
		&fakeClient{},
		false,
	)
	if !errors.Is(err, fatalErr) {
		t.Fatalf("outer Agent Step error = %v, want %v", err, fatalErr)
	}
	if active := engine.reviewerRuntimeState().ActiveStepSnapshot(); active != nil {
		t.Fatalf("Reviewer remained active after fatal pipeline return: %+v", active)
	}
	started, completed := 0, 0
	startIndex, completedIndex := -1, -1
	for index, event := range events {
		switch event.Kind {
		case EventReviewerStarted:
			started++
			startIndex = index
		case EventReviewerCompleted:
			completed++
			completedIndex = index
		}
	}
	if started != 1 || completed != 1 {
		t.Fatalf("Reviewer lifecycle counts = started:%d completed:%d events=%+v", started, completed, events)
	}
	if completedIndex <= startIndex {
		t.Fatalf("Reviewer completion did not follow start: events=%+v", events)
	}
}

func TestReviewerStartPublicationFailureDoesNotEmitCompletionOrLeaveState(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	var events []Event
	engine.cfg.OnEvent = func(event Event) {
		events = append(events, event)
	}
	engine.eventLog = session.MaterializedEventLog{}

	err := engine.steer("11111111-1111-4111-8111-111111111111", steerEventIntent(Event{
		Kind: EventReviewerStarted, StepID: "11111111-1111-4111-8111-111111111111",
	}))
	if err == nil {
		t.Fatal("Reviewer start publication unexpectedly succeeded")
	}
	if engine.reviewerRuntimeState().ActiveStepSnapshot() != nil {
		t.Fatal("failed Reviewer start left active runtime state")
	}
	for _, event := range events {
		if event.Kind == EventReviewerCompleted {
			t.Fatalf("failed Reviewer start emitted unmatched completion: %+v", events)
		}
	}
}

func TestReviewerLifecycleCallbacksObserveMatchingState(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	stepID := "11111111-1111-4111-8111-111111111111"
	engine.cfg.OnEvent = func(event Event) {
		switch event.Kind {
		case EventReviewerStarted:
			if active := engine.reviewerRuntimeState().ActiveStepSnapshot(); active == nil || active.StepID != stepID {
				t.Fatalf("Started callback observed Reviewer state %+v", active)
			}
		case EventReviewerCompleted:
			if active := engine.reviewerRuntimeState().ActiveStepSnapshot(); active != nil {
				t.Fatalf("Completed callback observed active Reviewer state %+v", active)
			}
		}
	}
	if err := engine.steer(stepID, steerEventIntent(Event{Kind: EventReviewerStarted, StepID: stepID})); err != nil {
		t.Fatalf("publish Reviewer start: %v", err)
	}
	if err := engine.steer(stepID, steerEventIntent(Event{Kind: EventReviewerCompleted, StepID: stepID})); err != nil {
		t.Fatalf("publish Reviewer completion: %v", err)
	}
}

func TestReviewerCompletionPublicationFailureClearsState(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	stepID := "11111111-1111-4111-8111-111111111111"
	if err := engine.steer(stepID, steerEventIntent(Event{Kind: EventReviewerStarted, StepID: stepID})); err != nil {
		t.Fatalf("publish Reviewer start: %v", err)
	}
	engine.eventLog = session.MaterializedEventLog{}
	if err := engine.steer(
		stepID,
		steerEventIntent(Event{
			Kind:   EventReviewerCompleted,
			StepID: stepID,
		}),
	); err == nil {
		t.Fatal("completion publication unexpectedly succeeded")
	}
	if active := engine.reviewerRuntimeState().ActiveStepSnapshot(); active != nil {
		t.Fatalf("completion publication failure left Reviewer active: %+v", active)
	}
}

func TestReviewerFactCommitFenceRunsThroughCallerLifecycle(t *testing.T) {
	tests := []struct {
		name       string
		intent     func() steeringIntent
		assertFact func(t *testing.T, event Event, rows []TranscriptCommittedRowFact)
	}{
		{
			name: "feedback",
			intent: func() steeringIntent {
				return steerReviewerFeedbackIntent([]string{"preserve source"}, transcript.EntryVisibilityOngoingCollapsed)
			},
			assertFact: func(t *testing.T, event Event, rows []TranscriptCommittedRowFact) {
				if event.LocalEntry == nil || event.LocalEntry.ReviewerFeedback == nil || len(rows) != 1 || rows[0].ReviewerFeedback == nil {
					t.Fatalf("feedback publication = event:%+v rows:%+v", event, rows)
				}
				if event.LocalEntry.ReviewerFeedback.ID != rows[0].ReviewerFeedback.ID {
					t.Fatalf("feedback identity diverged: event=%+v rows=%+v", event.LocalEntry, rows[0])
				}
			},
		},
		{
			name: "error",
			intent: func() steeringIntent {
				return steerReviewerErrorIntent("preserve raw detail")
			},
			assertFact: func(t *testing.T, event Event, rows []TranscriptCommittedRowFact) {
				if event.LocalEntry == nil || event.LocalEntry.ReviewerError == nil || len(rows) != 1 || rows[0].ReviewerError == nil {
					t.Fatalf("error publication = event:%+v rows:%+v", event, rows)
				}
				if event.LocalEntry.ReviewerError.ID != rows[0].ReviewerError.ID {
					t.Fatalf("error identity diverged: event=%+v rows=%+v", event.LocalEntry, rows[0])
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			for _, committed := range []bool{false, true} {
				t.Run(map[bool]string{false: "uncommitted", true: "committed observer error"}[committed], func(t *testing.T) {
					var (
						events  []Event
						blocker *testEventLogAppendBlocker
						gate    *sessiontest.PersistenceGate
					)
					store := mustCreateTestSession(t)
					if committed {
						gate = sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
						store = mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
					}
					engine := mustNewTestEngine(t, store, &fakeClient{responses: []llm.Response{{
						Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("answer")},
						Usage:     llm.Usage{WindowTokens: 200000},
					}}}, tools.NewRegistry(), Config{
						Model:   "gpt-5",
						OnEvent: func(event Event) { events = append(events, event) },
					})
					pipeline := &reviewerFactSteeringStub{
						engine: engine,
						intent: testCase.intent,
						before: func() {
							if committed {
								gate.FailNext(errors.New("observer failure"))
							} else {
								blocker = mustBlockTestEventLogAppends(t, store)
							}
						},
					}
					engine.stepFlow = &defaultStepExecutor{
						engine: engine, phase: engine.phaseProtocol, reviewer: pipeline,
						messages: engine.messageFlow,
						tools:    engine.toolFlow,
					}
					_, runErr := engine.runStepLoopWithOptions(context.Background(), "11111111-1111-4111-8111-111111111111", "all", &fakeClient{}, false)
					if blocker != nil {
						if err := blocker.Restore(); err != nil {
							t.Fatalf("restore append blocker: %v", err)
						}
					}
					started, completed := 0, 0
					factEvents := 0
					var factEvent *Event
					for index := range events {
						switch events[index].Kind {
						case EventReviewerStarted:
							started++
						case EventReviewerCompleted:
							completed++
						case EventLocalEntryAdded:
							if events[index].LocalEntry != nil && (events[index].LocalEntry.ReviewerFeedback != nil || events[index].LocalEntry.ReviewerError != nil) {
								copyEvent := events[index]
								factEvent = &copyEvent
								factEvents++
							}
						}
					}
					if started != 1 || completed != 1 || engine.reviewerRuntimeState().ActiveStepSnapshot() != nil {
						t.Fatalf("lifecycle = started:%d completed:%d active:%+v events:%+v", started, completed, engine.reviewerRuntimeState().ActiveStepSnapshot(), events)
					}
					allRows := TranscriptCommittedRowFactsFromSnapshot(engine.ChatSnapshot())
					rows := make([]TranscriptCommittedRowFact, 0, 1)
					for _, row := range allRows {
						if row.ReviewerFeedback != nil || row.ReviewerError != nil {
							rows = append(rows, row)
						}
					}
					if !committed {
						if runErr == nil || factEvents != 0 || len(rows) != 0 {
							t.Fatalf("uncommitted Reviewer fact = err:%v events:%d rows:%+v", runErr, factEvents, rows)
						}
					} else {
						if runErr == nil || factEvent == nil || factEvents != 1 {
							t.Fatalf("committed observer error lost or duplicated Reviewer fact: err=%v events:%d event=%+v", runErr, factEvents, factEvent)
						}
						testCase.assertFact(t, *factEvent, rows)
						window, readErr := mustMaterializeTestEventLog(t, store).ReadRecentRecords(32)
						if readErr != nil {
							t.Fatalf("read committed Reviewer fact: %v", readErr)
						}
						var persistedFeedback *session.ReviewerFeedbackRecord
						var persistedError *session.ReviewerErrorRecord
						for _, record := range window.Records {
							switch payload := mustSessionEventPayload(record).(type) {
							case session.ReviewerFeedbackRecord:
								copied := payload
								persistedFeedback = &copied
							case session.ReviewerErrorRecord:
								copied := payload
								persistedError = &copied
							}
						}
						if factEvent.LocalEntry.ReviewerFeedback != nil {
							if persistedFeedback == nil || persistedFeedback.ID != factEvent.LocalEntry.ReviewerFeedback.ID {
								t.Fatalf("persisted/live Reviewer feedback identity diverged: persisted=%+v event=%v", persistedFeedback, factEvent.LocalEntry.ReviewerFeedback.ID)
							}
						}
						if factEvent.LocalEntry.ReviewerError != nil {
							if persistedError == nil || persistedError.ID != factEvent.LocalEntry.ReviewerError.ID {
								t.Fatalf("persisted/live Reviewer error identity diverged: persisted=%+v event=%v", persistedError, factEvent.LocalEntry.ReviewerError.ID)
							}
						}
					}
				})
			}
		})
	}
}

type reviewerFactSteeringStub struct {
	engine *Engine
	intent func() steeringIntent
	before func()
}

func (s *reviewerFactSteeringStub) ShouldRunTurn(string, llm.Client, bool) bool { return true }

func (s *reviewerFactSteeringStub) RunFollowUp(
	_ context.Context,
	stepID string,
	original llm.Message,
	_ int,
	_ bool,
	_ llm.Client,
) (reviewerFollowUpResult, error) {
	s.before()
	if err := s.engine.steer(stepID, s.intent()); err != nil {
		return reviewerFollowUpResult{}, err
	}
	return reviewerFollowUpResult{
		Message:    original,
		Completion: &ReviewerStatus{Outcome: "applied"},
	}, nil
}

func (*reviewerFactSteeringStub) RunSuggestions(context.Context, string, llm.Client) (reviewerSuggestionsResult, error) {
	return reviewerSuggestionsResult{}, nil
}

type reviewerPipelineStub struct {
	runFollowUp func(string, llm.Message) (reviewerFollowUpResult, error)
}

func (*reviewerPipelineStub) ShouldRunTurn(string, llm.Client, bool) bool {
	return true
}

func (s *reviewerPipelineStub) RunFollowUp(
	_ context.Context,
	stepID string,
	original llm.Message,
	_ int,
	_ bool,
	_ llm.Client,
) (reviewerFollowUpResult, error) {
	return s.runFollowUp(stepID, original)
}

func (*reviewerPipelineStub) RunSuggestions(context.Context, string, llm.Client) (reviewerSuggestionsResult, error) {
	return reviewerSuggestionsResult{}, nil
}
