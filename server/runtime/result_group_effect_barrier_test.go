package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"core/server/attentionnotify"
	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/toolspec"
)

type resultGroupFlushRecorder struct {
	mu           sync.Mutex
	observations []ResultGroupFlushObservation
}

func (r *resultGroupFlushRecorder) ObserveResultGroupFlush(observation ResultGroupFlushObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observations = append(r.observations, observation)
}

func (r *resultGroupFlushRecorder) snapshot() []ResultGroupFlushObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ResultGroupFlushObservation(nil), r.observations...)
}

func questionBarrierAcceptedCalls() acceptedResponseCalls {
	return acceptedResponseCalls{
		hosted: []hostedToolExecution{{
			Call: llm.ToolCall{
				ID:    "hosted",
				Name:  string(toolspec.ToolWebSearch),
				Input: json.RawMessage(`{"query":"kent"}`),
			},
			Result: tools.Result{
				CallID: "hosted",
				Name:   toolspec.ToolWebSearch,
				Output: json.RawMessage(`{"ok":true}`),
			},
		}},
		local: []llm.ToolCall{{
			ID:    "question",
			Name:  string(toolspec.ToolAskQuestion),
			Input: json.RawMessage(`{"question":"Continue?"}`),
		}},
		order: []acceptedResponseCallRef{
			{source: acceptedResponseCallHosted, index: 0},
			{source: acceptedResponseCallLocal, index: 0},
		},
	}
}

func twoQuestionBarrierAcceptedCalls() acceptedResponseCalls {
	calls := questionBarrierAcceptedCalls()
	calls.local = append(calls.local, llm.ToolCall{
		ID:    "question-2",
		Name:  string(toolspec.ToolAskQuestion),
		Input: json.RawMessage(`{"question":"Continue again?"}`),
	})
	calls.order = append(calls.order, acceptedResponseCallRef{
		source: acceptedResponseCallLocal,
		index:  1,
	})
	return calls
}

func persistAcceptedToolCallIntents(
	t *testing.T,
	engine *Engine,
	stepID string,
	calls acceptedResponseCalls,
) {
	t.Helper()
	ordered := make([]llm.ToolCall, 0, len(calls.order))
	for _, ref := range calls.order {
		switch ref.source {
		case acceptedResponseCallHosted:
			ordered = append(ordered, calls.hosted[ref.index].Call)
		case acceptedResponseCallLocal:
			ordered = append(ordered, calls.local[ref.index])
		default:
			t.Fatalf("unsupported accepted response call source %d", ref.source)
		}
	}
	if err := engine.steer(stepID, steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventDefault,
		true,
		[]llm.Message{{Role: llm.RoleAssistant, ToolCalls: ordered}},
	)); err != nil {
		t.Fatalf("persist accepted tool-call intents: %v", err)
	}
}

func TestQuestionBarrierCommitsReadyHostedSiblingBeforeInteraction(t *testing.T) {
	store := mustCreateTestSession(t)
	broker := tools.NewAskQuestionBroker()
	flushes := &resultGroupFlushRecorder{}
	var engine *Engine
	var interactionErr error
	broker.SetAskHandler(func(_ context.Context, req tools.AskQuestionRequest) (tools.AskQuestionResponse, error) {
		if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("hosted"); !found {
			interactionErr = errors.New("Question became visible before the ready hosted sibling committed")
			return tools.AskQuestionResponse{}, interactionErr
		}
		return tools.AskQuestionResponse{RequestID: req.ID, Answer: "continue"}, nil
	})
	engine = mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: tools.NewAskQuestionTool(broker, func() bool { return true }),
		}),
		Config{Model: "gpt-5", DurabilityObserver: flushes},
	)
	results, err := engine.executeAcceptedToolCalls(
		context.Background(),
		"step",
		questionBarrierAcceptedCalls(),
	)
	if err != nil {
		t.Fatalf("execute accepted calls: %v", err)
	}
	if interactionErr != nil {
		t.Fatal(interactionErr)
	}
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("Question results = %+v, want one successful result", results)
	}
	observations := flushes.snapshot()
	if len(observations) != 2 ||
		observations[0].Reason != ResultGroupFlushQuestion ||
		observations[0].ResultCount != 1 ||
		observations[1].Reason != ResultGroupFlushStepBoundary {
		t.Fatalf("result group flushes = %+v, want Question sibling flush then Step Boundary close", observations)
	}
}

type approvalBarrierProbe struct {
	broker *tools.AskQuestionBroker
}

func (p approvalBarrierProbe) Call(
	ctx context.Context,
	call tools.Call,
) (tools.Result, error) {
	_, err := p.broker.Ask(ctx, tools.AskQuestionRequest{
		ID:       call.ID + "-approval",
		Question: "Approve?",
		Approval: true,
		ApprovalOptions: []tools.AskQuestionApprovalOption{{
			Decision: tools.AskQuestionApprovalDecisionAllowOnce,
			Label:    "Allow once",
		}},
	})
	if err != nil {
		return tools.ErrorResult(call, err.Error()), nil
	}
	return tools.Result{
		CallID: call.ID,
		Name:   call.Name,
		Output: json.RawMessage(`{"ok":true}`),
	}, nil
}

func TestApprovalBarrierUsesRuntimeFlushBeforeNestedApprovalVisibility(t *testing.T) {
	store := mustCreateTestSession(t)
	broker := tools.NewAskQuestionBroker()
	flushes := &resultGroupFlushRecorder{}
	var engine *Engine
	var interactionErr error
	broker.SetAskHandler(func(_ context.Context, req tools.AskQuestionRequest) (tools.AskQuestionResponse, error) {
		if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("hosted"); !found {
			interactionErr = errors.New("Approval became visible before the ready hosted sibling committed")
			return tools.AskQuestionResponse{}, interactionErr
		}
		return tools.AskQuestionResponse{
			RequestID: req.ID,
			Approval: &tools.AskQuestionApprovalPayload{
				Decision: tools.AskQuestionApprovalDecisionAllowOnce,
			},
		}, nil
	})
	engine = mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolPatch,
			Handler: approvalBarrierProbe{broker: broker},
		}),
		Config{Model: "gpt-5", DurabilityObserver: flushes},
	)
	calls := questionBarrierAcceptedCalls()
	calls.local[0] = llm.ToolCall{
		ID:    "patch",
		Name:  string(toolspec.ToolPatch),
		Input: json.RawMessage(`{}`),
	}

	results, err := engine.executeAcceptedToolCalls(context.Background(), "step", calls)
	if err != nil {
		t.Fatalf("execute accepted calls: %v", err)
	}
	if interactionErr != nil {
		t.Fatal(interactionErr)
	}
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("Approval results = %+v, want one successful result", results)
	}
	observations := flushes.snapshot()
	if len(observations) != 2 ||
		observations[0].Reason != ResultGroupFlushApproval ||
		observations[0].ResultCount != 1 ||
		observations[1].Reason != ResultGroupFlushStepBoundary {
		t.Fatalf("result group flushes = %+v, want Approval sibling flush then Step Boundary close", observations)
	}
}

func TestResultGroupEffectBarrierRejectsConcurrentFatalAfterSuccessfulFlush(t *testing.T) {
	collector := testResultGroupCollector(t, "first")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cause := errors.New("concurrent sibling durability failure")
	siblingDone := make(chan struct{})

	err := runResultGroupEffectBarrier(
		ctx,
		collector,
		cancel,
		func() error {
			go func() {
				collector.abort(resultGroupFatal{
					Committed: false,
					Cause:     cause,
				})
				close(siblingDone)
			}()
			<-siblingDone
			return nil
		},
	)
	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) ||
		fatal.Committed ||
		!errors.Is(fatal.Cause, cause) {
		t.Fatalf("effect barrier error = %v, want concurrent sibling fatal", err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("effect barrier context error = %v, want canceled", ctx.Err())
	}
}

func TestQuestionBarrierPreCommitFailureBlocksInteractionAndSemanticResult(t *testing.T) {
	store := mustCreateTestSession(t)
	broker := tools.NewAskQuestionBroker()
	handlerCalled := false
	broker.SetAskHandler(func(_ context.Context, req tools.AskQuestionRequest) (tools.AskQuestionResponse, error) {
		handlerCalled = true
		return tools.AskQuestionResponse{RequestID: req.ID, Answer: "continue"}, nil
	})
	skipped := 0
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: tools.NewAskQuestionTool(broker, func() bool { return true }),
		}),
		Config{
			Model: "gpt-5",
			AskQuestionBatchSkipped: func(tools.AskQuestionBatchMetadata) {
				skipped++
			},
		},
	)
	blocker := mustBlockTestEventLogAppends(t, store)

	results, err := engine.executeAcceptedToolCalls(
		context.Background(),
		"step",
		questionBarrierAcceptedCalls(),
	)
	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) || fatal.Committed {
		t.Fatalf("Question barrier error = %v, want uncommitted collector fatal", err)
	}
	if handlerCalled {
		t.Fatal("Question handler ran after uncommitted barrier failure")
	}
	if skipped != 1 {
		t.Fatalf("Question batch skip callbacks = %d, want one", skipped)
	}
	if len(results) != 1 || results[0].CallID != "" {
		t.Fatalf("Question results = %+v, want no provisional semantic result", results)
	}
	if pending := broker.Pending(); len(pending) != 0 {
		t.Fatalf("Question queued after barrier failure: %+v", pending)
	}
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event-log blocker: %v", err)
	}
	window, readErr := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if readErr != nil {
		t.Fatalf("read bounded records: %v", readErr)
	}
	for _, record := range window.Records {
		completion, ok := mustSessionEventPayload(record).(session.ToolCompletionRecord)
		if ok && (completion.CallID == "hosted" || completion.CallID == "question") {
			t.Fatalf("barrier failure persisted tool completion: %+v", completion)
		}
	}
}

func TestQuestionBarrierCommittedObserverFailureRetainsPrefixAndBlocksInteraction(t *testing.T) {
	observerErr := errors.New("Question barrier observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	durability := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithPersistenceObserver(gate),
		session.WithDurabilityObserver(durability),
	)
	broker := tools.NewAskQuestionBroker()
	handlerCalled := false
	broker.SetAskHandler(func(_ context.Context, req tools.AskQuestionRequest) (tools.AskQuestionResponse, error) {
		handlerCalled = true
		return tools.AskQuestionResponse{RequestID: req.ID, Answer: "continue"}, nil
	})
	skipped := 0
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: tools.NewAskQuestionTool(broker, func() bool { return true }),
		}),
		Config{
			Model: "gpt-5",
			AskQuestionBatchSkipped: func(tools.AskQuestionBatchMetadata) {
				skipped++
			},
		},
	)
	persistAcceptedToolCallIntents(t, engine, "step", questionBarrierAcceptedCalls())
	appendsBefore, _ := durability.snapshot()
	gate.FailNext(observerErr)

	results, err := engine.executeAcceptedToolCalls(
		context.Background(),
		"step",
		questionBarrierAcceptedCalls(),
	)
	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) ||
		!fatal.Committed ||
		!errors.Is(fatal.Cause, observerErr) {
		t.Fatalf("Question barrier error = %v, want committed observer fatal", err)
	}
	if handlerCalled {
		t.Fatal("Question handler ran after committed observer barrier failure")
	}
	if skipped != 1 {
		t.Fatalf("Question batch skip callbacks = %d, want one", skipped)
	}
	if len(results) != 1 || results[0].CallID != "" {
		t.Fatalf("Question results = %+v, want no provisional semantic result", results)
	}
	if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("hosted"); !found {
		t.Fatal("committed observer failure did not project the ready sibling")
	}
	if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("question"); found {
		t.Fatal("blocked Question projected a semantic completion")
	}
	appendsAfter, _ := durability.snapshot()
	if len(appendsAfter) != len(appendsBefore)+1 {
		t.Fatalf(
			"committed observer barrier append attempts = %d, want one after %d",
			len(appendsAfter),
			len(appendsBefore),
		)
	}
	assertFreshResourceRepairExactlyOnce(t, store, "question")
}

func TestQuestionBarrierCommittedProjectionFailureBlocksInteractionAndHydratesPrefix(t *testing.T) {
	callbackObserver := newCallbackPersistenceObserver(runtimeTestSessionPersistence)
	durability := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithPersistenceObserver(callbackObserver),
		session.WithDurabilityObserver(durability),
	)
	broker := tools.NewAskQuestionBroker()
	handlerCalled := false
	broker.SetAskHandler(func(_ context.Context, req tools.AskQuestionRequest) (tools.AskQuestionResponse, error) {
		handlerCalled = true
		return tools.AskQuestionResponse{RequestID: req.ID, Answer: "continue"}, nil
	})
	skipped := 0
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: tools.NewAskQuestionTool(broker, func() bool { return true }),
		}),
		Config{
			Model: "gpt-5",
			AskQuestionBatchSkipped: func(tools.AskQuestionBatchMetadata) {
				skipped++
			},
		},
	)
	persistAcceptedToolCallIntents(t, engine, "step", questionBarrierAcceptedCalls())
	appendsBefore, _ := durability.snapshot()
	callbackObserver.Arm(func() {
		engine.transcriptRuntimeState().CompleteLiveTool("hosted")
	})

	results, err := engine.executeAcceptedToolCalls(
		context.Background(),
		"step",
		questionBarrierAcceptedCalls(),
	)
	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) || !fatal.Committed {
		t.Fatalf("Question barrier error = %v, want committed projection fatal", err)
	}
	if handlerCalled {
		t.Fatal("Question handler ran after committed projection barrier failure")
	}
	if skipped != 1 {
		t.Fatalf("Question batch skip callbacks = %d, want one", skipped)
	}
	if len(results) != 1 || results[0].CallID != "" {
		t.Fatalf("Question results = %+v, want no provisional semantic result", results)
	}
	if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("hosted"); found {
		t.Fatal("committed projection failure partially projected the ready sibling")
	}
	appendsAfter, _ := durability.snapshot()
	if len(appendsAfter) != len(appendsBefore)+1 {
		t.Fatalf(
			"committed projection barrier append attempts = %d, want one after %d",
			len(appendsAfter),
			len(appendsBefore),
		)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	restored := mustNewTestEngine(
		t,
		reopened,
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	if rows := countHydratedToolRows(
		mustTranscriptHydrationSnapshot(t, restored),
		"hosted",
	); rows != 1 {
		t.Fatalf("rehydrated committed sibling rows = %d, want one", rows)
	}
	assertFreshResourceRepairOnEngine(t, restored, reopened, "question")
	assertFreshResourceRepairExactlyOnce(t, reopened, "question")
}

func TestQuestionBarrierOrdinaryBrokerErrorRemainsSemantic(t *testing.T) {
	brokerErr := errors.New("broker unavailable")
	broker := tools.NewAskQuestionBroker()
	handlerCalled := false
	broker.SetAskHandler(func(context.Context, tools.AskQuestionRequest) (tools.AskQuestionResponse, error) {
		handlerCalled = true
		return tools.AskQuestionResponse{}, brokerErr
	})
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: tools.NewAskQuestionTool(broker, func() bool { return true }),
		}),
		Config{Model: "gpt-5"},
	)

	results, err := engine.executeAcceptedToolCalls(
		context.Background(),
		"step",
		questionBarrierAcceptedCalls(),
	)
	if err != nil {
		t.Fatalf("ordinary broker error escaped tool semantics: %v", err)
	}
	if !handlerCalled {
		t.Fatal("ordinary broker handler was not called")
	}
	if len(results) != 1 ||
		results[0].CallID != "question" ||
		!results[0].IsError {
		t.Fatalf("Question results = %+v, want one semantic error result", results)
	}
	if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("hosted"); !found {
		t.Fatal("ordinary broker error lost the committed sibling")
	}
	if completion, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("question"); !found || !completion.IsError {
		t.Fatalf("ordinary broker completion = %+v, found=%t", completion, found)
	}
}

func TestQuestionBarrierOrdinaryCancellationRemainsSemantic(t *testing.T) {
	broker := tools.NewAskQuestionBroker()
	handlerCalled := false
	broker.SetAskHandler(func(context.Context, tools.AskQuestionRequest) (tools.AskQuestionResponse, error) {
		handlerCalled = true
		return tools.AskQuestionResponse{}, nil
	})
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: tools.NewAskQuestionTool(broker, func() bool { return true }),
		}),
		Config{Model: "gpt-5"},
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results, err := engine.executeAcceptedToolCalls(
		ctx,
		"step",
		questionBarrierAcceptedCalls(),
	)
	if err != nil {
		t.Fatalf("ordinary cancellation escaped tool semantics: %v", err)
	}
	if handlerCalled {
		t.Fatal("canceled Question entered the broker handler")
	}
	if len(results) != 1 ||
		results[0].CallID != "question" ||
		!results[0].IsError {
		t.Fatalf("Question results = %+v, want one semantic cancellation result", results)
	}
	if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("hosted"); !found {
		t.Fatal("ordinary cancellation lost the committed sibling")
	}
	if completion, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("question"); !found || !completion.IsError {
		t.Fatalf("ordinary cancellation completion = %+v, found=%t", completion, found)
	}
}

func TestSecondQuestionBarrierFatalResolvesAndRemovesMaterializedAttentionBatch(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "uncommitted",
			run: func(t *testing.T) {
				runSecondQuestionBarrierAttentionCase(t, questionBarrierFailureUncommitted)
			},
		},
		{
			name: "committed observer",
			run: func(t *testing.T) {
				runSecondQuestionBarrierAttentionCase(t, questionBarrierFailureObserver)
			},
		},
		{
			name: "committed projection",
			run: func(t *testing.T) {
				runSecondQuestionBarrierAttentionCase(t, questionBarrierFailureProjection)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

type questionBarrierFailure uint8

const (
	questionBarrierFailureUncommitted questionBarrierFailure = iota + 1
	questionBarrierFailureObserver
	questionBarrierFailureProjection
)

func runSecondQuestionBarrierAttentionCase(
	t *testing.T,
	failure questionBarrierFailure,
) {
	t.Helper()
	var (
		store            *session.Store
		gate             *sessiontest.PersistenceGate
		callbackObserver *callbackPersistenceObserver
		restore          func() error
	)
	switch failure {
	case questionBarrierFailureUncommitted:
		store = mustCreateTestSession(t)
	case questionBarrierFailureObserver:
		gate = sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
		store = mustCreateTestSessionAt(
			t,
			t.TempDir(),
			session.WithPersistenceObserver(gate),
		)
	case questionBarrierFailureProjection:
		callbackObserver = newCallbackPersistenceObserver(runtimeTestSessionPersistence)
		store = mustCreateTestSessionAt(
			t,
			t.TempDir(),
			session.WithPersistenceObserver(callbackObserver),
		)
	default:
		t.Fatalf("unknown Question barrier failure %d", failure)
	}

	attentionBroker := attentionnotify.NewBroker()
	t.Cleanup(func() { attentionBroker.Close(nil) })
	attentionSub, err := attentionBroker.SubscribeDesktop()
	if err != nil {
		t.Fatalf("subscribe attention: %v", err)
	}
	t.Cleanup(func() { _ = attentionSub.Close() })
	tracker := attentionnotify.NewQuestionBatchTracker(attentionBroker)
	askBroker := tools.NewAskQuestionBroker()
	var (
		engine       *Engine
		handlerCalls int
		skippedCalls int
		trackerErr   error
		handlerErr   error
		batchID      string
		secondAskID  string
	)
	askBroker.SetAskHandler(func(
		_ context.Context,
		req tools.AskQuestionRequest,
	) (tools.AskQuestionResponse, error) {
		handlerCalls++
		if handlerCalls != 1 {
			handlerErr = fmt.Errorf("blocked second Question entered handler: %+v", req)
			return tools.AskQuestionResponse{}, handlerErr
		}
		if req.QuestionBatch == nil {
			handlerErr = errors.New("first Question has no production batch metadata")
			return tools.AskQuestionResponse{}, handlerErr
		}
		batch := *req.QuestionBatch
		if len(batch.BatchPromptIDs) != 2 {
			handlerErr = fmt.Errorf(
				"first Question batch prompt IDs = %v, want two",
				batch.BatchPromptIDs,
			)
			return tools.AskQuestionResponse{}, handlerErr
		}
		batchID = batch.BatchID
		secondAskID = batch.BatchPromptIDs[1]
		trackerErr = errors.Join(
			trackerErr,
			tracker.Prepare(questionBarrierAttentionBatch(batch, req.Question)),
			tracker.MarkMaterialized(batch.BatchID, req.ID),
			tracker.MarkDurablyCleared(batch.BatchID, req.ID),
		)
		switch failure {
		case questionBarrierFailureUncommitted:
			blocker := mustBlockTestEventLogAppends(t, store)
			restore = blocker.Restore
		case questionBarrierFailureObserver:
			gate.FailNext(errors.New("second Question observer failure"))
		case questionBarrierFailureProjection:
			callbackObserver.Arm(func() {
				engine.transcriptRuntimeState().CompleteLiveTool(req.ID)
			})
		}
		return tools.AskQuestionResponse{RequestID: req.ID, Answer: "continue"}, nil
	})
	engine = mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: tools.NewAskQuestionTool(askBroker, func() bool { return true }),
		}),
		Config{
			Model: "gpt-5",
			AskQuestionBatchSkipped: func(batch tools.AskQuestionBatchMetadata) {
				skippedCalls++
				trackerErr = errors.Join(
					trackerErr,
					tracker.MarkSkipped(batch.BatchID, batch.PromptID),
				)
			},
		},
	)

	results, executeErr := engine.executeAcceptedToolCalls(
		context.Background(),
		"step",
		twoQuestionBarrierAcceptedCalls(),
	)
	if restore != nil {
		if err := restore(); err != nil {
			t.Fatalf("restore event-log blocker: %v", err)
		}
	}
	var fatal *resultGroupFatal
	if !errors.As(executeErr, &fatal) {
		t.Fatalf("second Question barrier error = %v, want collector fatal", executeErr)
	}
	wantCommitted := failure != questionBarrierFailureUncommitted
	if fatal.Committed != wantCommitted {
		t.Fatalf("collector fatal committed = %t, want %t", fatal.Committed, wantCommitted)
	}
	if trackerErr != nil {
		t.Fatalf("attention tracker lifecycle: %v", trackerErr)
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if handlerCalls != 1 || skippedCalls != 1 {
		t.Fatalf(
			"Question handler/skipped calls = %d/%d, want 1/1",
			handlerCalls,
			skippedCalls,
		)
	}
	if len(results) != 2 ||
		results[0].CallID != "question" ||
		results[0].IsError ||
		results[1].CallID != "" {
		t.Fatalf("two-Question results = %+v, want first complete and blocked second absent", results)
	}
	if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("question-2"); found {
		t.Fatal("collector fatal was converted into an interrupted or semantic second-Question result")
	}

	pending := nextQuestionBarrierAttentionEvent(t, attentionSub)
	if pending.Type != clientui.AttentionNotificationEventPending ||
		pending.Pending == nil ||
		pending.Pending.ID.UUID != batchID {
		t.Fatalf("pending Question attention = %+v", pending)
	}
	resolved := nextQuestionBarrierAttentionEvent(t, attentionSub)
	if resolved.Type != clientui.AttentionNotificationEventResolved ||
		resolved.ID == nil ||
		resolved.ID.UUID != batchID {
		t.Fatalf("resolved Question attention = %+v", resolved)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if extra, err := attentionSub.Next(ctx); err == nil {
		t.Fatalf("duplicate Question attention event = %+v", extra)
	}
	if err := tracker.MarkSkipped(batchID, secondAskID); !errors.Is(err, attentionnotify.ErrBatchNotFound) {
		t.Fatalf("resolved Question batch remained registered: %v", err)
	}
}

func TestFirstQuestionBarrierFatalLeavesNoAttentionBatch(t *testing.T) {
	store := mustCreateTestSession(t)
	attentionBroker := attentionnotify.NewBroker()
	t.Cleanup(func() { attentionBroker.Close(nil) })
	attentionSub, err := attentionBroker.SubscribeDesktop()
	if err != nil {
		t.Fatalf("subscribe attention: %v", err)
	}
	t.Cleanup(func() { _ = attentionSub.Close() })
	tracker := attentionnotify.NewQuestionBatchTracker(attentionBroker)
	askBroker := tools.NewAskQuestionBroker()
	handlerCalls := 0
	askBroker.SetAskHandler(func(
		context.Context,
		tools.AskQuestionRequest,
	) (tools.AskQuestionResponse, error) {
		handlerCalls++
		return tools.AskQuestionResponse{}, nil
	})
	skippedCalls := 0
	var trackerErr error
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: tools.NewAskQuestionTool(askBroker, func() bool { return true }),
		}),
		Config{
			Model: "gpt-5",
			AskQuestionBatchSkipped: func(batch tools.AskQuestionBatchMetadata) {
				skippedCalls++
				trackerErr = errors.Join(
					trackerErr,
					tracker.MarkSkipped(batch.BatchID, batch.PromptID),
				)
			},
		},
	)
	engine.ensureOrchestrationCollaborators()
	calls := twoQuestionBarrierAcceptedCalls()
	prepared, err := prepareExecutorToolCalls(
		engine,
		"step",
		"",
		false,
		calls.local,
	)
	if err != nil {
		t.Fatalf("prepare two-Question calls: %v", err)
	}
	if len(prepared) != 2 || prepared[0].askQuestionBatch == nil {
		t.Fatalf("prepared two-Question calls = %+v", prepared)
	}
	batch := *prepared[0].askQuestionBatch
	if err := tracker.Prepare(questionBarrierAttentionBatch(batch, "Continue?")); err != nil {
		t.Fatalf("prepare attention batch: %v", err)
	}
	roster := []resultGroupCallIdentity{
		resultGroupIdentityFromToolCall(calls.hosted[0].Call),
	}
	roster = append(roster, resultGroupRosterFromPreparedCalls(prepared)...)
	collector, err := newResultGroupCollector(roster)
	if err != nil {
		t.Fatalf("new first-Question collector: %v", err)
	}
	hosted := calls.hosted[0]
	normalized := normalizeToolCallForTranscript(
		hosted.Call,
		engine.transcriptWorkingDir(),
	)
	if err := engine.steer("step", steerEventIntent(Event{
		Kind:                       EventToolCallStarted,
		StepID:                     "step",
		ToolCall:                   &normalized,
		CommittedTranscriptChanged: true,
	})); err != nil {
		t.Fatalf("start hosted sibling: %v", err)
	}
	outcome := resultGroupReportOutcome(0)
	if err := engine.steer(
		"step",
		steerResultGroupReportIntent(
			collector,
			hosted.Call.ID,
			resultGroupUnit{result: hosted.Result},
			&outcome,
		),
	); err != nil {
		t.Fatalf("report hosted sibling: %v", err)
	}
	blocker := mustBlockTestEventLogAppends(t, store)

	completed, executeErr := engine.toolFlow.ExecuteToolCalls(
		context.Background(),
		"step",
		prepared,
		collector,
	)
	closeErr := engine.steer("step", steerResultGroupCloseIntent(collector))
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event-log blocker: %v", err)
	}
	if executeErr != nil {
		t.Fatalf("first-Question executor error = %v", executeErr)
	}
	var fatal *resultGroupFatal
	if !errors.As(closeErr, &fatal) || fatal.Committed {
		t.Fatalf("first-Question close error = %v, want uncommitted fatal", closeErr)
	}
	if handlerCalls != 0 || skippedCalls != 2 {
		t.Fatalf(
			"first-Question handler/skipped calls = %d/%d, want 0/2",
			handlerCalls,
			skippedCalls,
		)
	}
	if trackerErr != nil {
		t.Fatalf("first-Question attention tracker lifecycle: %v", trackerErr)
	}
	for index, result := range completed {
		if result != nil {
			t.Fatalf("first-Question completed result %d = %+v, want absent", index, result)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if event, err := attentionSub.Next(ctx); err == nil {
		t.Fatalf("first-Question fatal published attention: %+v", event)
	}
	if err := tracker.MarkSkipped(
		batch.BatchID,
		batch.BatchPromptIDs[0],
	); !errors.Is(err, attentionnotify.ErrBatchNotFound) {
		t.Fatalf("first-Question fatal retained attention batch: %v", err)
	}
}

func questionBarrierAttentionBatch(
	batch tools.AskQuestionBatchMetadata,
	preview string,
) attentionnotify.QuestionBatch {
	workflowID := runtimeids.NewWorkflowID()
	return attentionnotify.QuestionBatch{
		ID:    batch.BatchID,
		Route: attentionnotify.RoutingScope{Kind: attentionnotify.RoutingWorkflowTask, TaskID: "task-1"},
		Target: clientui.AttentionNotificationTarget{
			Kind:        clientui.AttentionNotificationTargetWorkflowTask,
			WorkflowID:  &workflowID,
			TaskID:      "task-1",
			TaskShortID: "KENT-350",
			Focus: &clientui.AttentionNotificationTaskDetailFocus{
				Kind:   clientui.AttentionNotificationFocusQuestion,
				AskIDs: append([]string(nil), batch.BatchPromptIDs...),
			},
		},
		Preview:        preview,
		PreparedAskIDs: append([]string(nil), batch.BatchPromptIDs...),
		OccurredAt:     time.Now().UTC(),
	}
}

func nextQuestionBarrierAttentionEvent(
	t *testing.T,
	sub *attentionnotify.Subscription,
) clientui.AttentionNotificationEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	event, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("next Question attention event: %v", err)
	}
	return event
}
