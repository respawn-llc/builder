package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/toolspec"
)

func TestExecuteToolCallsRejectsMissingProviderCallIDBeforeToolExecution(t *testing.T) {
	t.Parallel()
	probe := &toolExecutionProbe{}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: probe,
		}),
		Config{Model: "gpt-5"},
	)

	_, err := engine.executeToolCalls(context.Background(), "step", []llm.ToolCall{{
		Name: string(toolspec.ToolExecCommand),
	}})
	if !errors.Is(err, ErrMissingProviderToolCallID) {
		t.Fatalf("execute tool calls error = %v, want missing provider call ID", err)
	}
	if probe.calls.Load() != 0 {
		t.Fatal("missing provider call ID reached a local tool handler")
	}
}

func TestToolStartOperationalFailureAbortsResultGroupWithoutSyntheticInterruption(t *testing.T) {
	t.Parallel()
	probe := &toolExecutionProbe{}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: probe,
		}),
		Config{Model: "gpt-5"},
	)
	engine.ensureOrchestrationCollaborators()
	collector, err := newResultGroupCollector([]resultGroupCallIdentity{{
		CallID:     "accepted-call",
		Name:       toolspec.ToolExecCommand,
		OutputKind: session.ToolOutputKindFunction,
	}})
	if err != nil {
		t.Fatalf("new result group collector: %v", err)
	}
	prepared := []executorToolCall{{
		call:      llm.ToolCall{Name: string(toolspec.ToolExecCommand)},
		toolID:    toolspec.ToolExecCommand,
		knownTool: true,
	}}

	completed, executeErr := engine.toolFlow.ExecuteToolCalls(
		context.Background(),
		"step",
		prepared,
		collector,
	)
	fatal := collector.fatalSnapshot()
	if fatal == nil ||
		fatal.Committed ||
		!errors.Is(fatal.Cause, ErrMissingProviderToolCallID) ||
		!errors.Is(executeErr, ErrMissingProviderToolCallID) {
		t.Fatalf(
			"start failure = execute:%v fatal:%+v, want uncommitted missing-ID abort",
			executeErr,
			fatal,
		)
	}
	if probe.calls.Load() != 0 {
		t.Fatal("tool handler ran after live-start steering failed")
	}
	if len(completed) != 1 || completed[0] != nil {
		t.Fatalf("completed cells = %+v, want one absent cell", completed)
	}

	if _, err := engine.coordinateAcceptedResponsePostJoin(
		"step",
		prepared,
		completed,
		collector,
		executeErr,
	); !errors.As(err, &fatal) {
		t.Fatalf("post-join error = %v, want Result Group fatal", err)
	}
	if collector.state != resultGroupCollectorClosed ||
		collector.cursor != 0 ||
		collector.slots[0].result != nil {
		t.Fatalf(
			"start-failure collector = state:%d cursor:%d slot:%+v",
			collector.state,
			collector.cursor,
			collector.slots[0],
		)
	}
}

func TestToolReportOperationalFailureAbortsResultGroupWithoutSyntheticInterruption(t *testing.T) {
	t.Parallel()
	handler := &closeEngineBeforeResultReportHandler{}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: handler,
		}),
		Config{Model: "gpt-5"},
	)
	handler.engine = engine
	engine.ensureOrchestrationCollaborators()
	call := llm.ToolCall{
		ID:    "report-failure",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"cmd":"true"}`),
	}
	prepared := []executorToolCall{{
		call:      call,
		toolID:    toolspec.ToolExecCommand,
		knownTool: true,
	}}
	collector, err := newResultGroupCollector([]resultGroupCallIdentity{
		resultGroupIdentityFromToolCall(call),
	})
	if err != nil {
		t.Fatalf("new result group collector: %v", err)
	}

	completed, executeErr := engine.toolFlow.ExecuteToolCalls(
		context.Background(),
		"step",
		prepared,
		collector,
	)
	engine.closed.Store(false)
	fatal := collector.fatalSnapshot()
	if fatal == nil ||
		fatal.Committed ||
		!errors.Is(fatal.Cause, ErrEngineClosed) ||
		!errors.Is(executeErr, ErrEngineClosed) {
		t.Fatalf(
			"report failure = execute:%v fatal:%+v, want uncommitted engine-closed abort",
			executeErr,
			fatal,
		)
	}
	if handler.calls.Load() != 1 {
		t.Fatalf("tool handler calls = %d, want 1", handler.calls.Load())
	}
	if len(completed) != 1 || completed[0] != nil {
		t.Fatalf("completed cells = %+v, want provisional result discarded", completed)
	}

	if _, err := engine.coordinateAcceptedResponsePostJoin(
		"step",
		prepared,
		completed,
		collector,
		executeErr,
	); !errors.As(err, &fatal) {
		t.Fatalf("post-join error = %v, want Result Group fatal", err)
	}
	if collector.state != resultGroupCollectorClosed ||
		collector.cursor != 0 ||
		collector.slots[0].result != nil {
		t.Fatalf(
			"report-failure collector = state:%d cursor:%d slot:%+v",
			collector.state,
			collector.cursor,
			collector.slots[0],
		)
	}
}

func TestSiblingAbortDiscardsProvisionalJoinedResultCells(t *testing.T) {
	t.Parallel()
	handler := &serialSiblingAbortToolHandler{}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolAskQuestion,
			Handler: handler,
		}),
		Config{Model: "gpt-5"},
	)
	handler.engine = engine
	engine.ensureOrchestrationCollaborators()
	prepared := []executorToolCall{
		{
			call: llm.ToolCall{
				ID:   "completed-before-abort",
				Name: string(toolspec.ToolAskQuestion),
			},
			toolID:    toolspec.ToolAskQuestion,
			knownTool: true,
		},
		{
			call: llm.ToolCall{
				ID:   "abort-sibling",
				Name: string(toolspec.ToolAskQuestion),
			},
			toolID:    toolspec.ToolAskQuestion,
			knownTool: true,
		},
	}
	collector, err := newResultGroupCollector(
		resultGroupRosterFromPreparedCalls(prepared),
	)
	if err != nil {
		t.Fatalf("new result group collector: %v", err)
	}

	completed, executeErr := engine.toolFlow.ExecuteToolCalls(
		context.Background(),
		"step",
		prepared,
		collector,
	)
	engine.closed.Store(false)
	fatal := collector.fatalSnapshot()
	if fatal == nil ||
		fatal.Committed ||
		!errors.Is(fatal.Cause, ErrEngineClosed) ||
		!errors.Is(executeErr, ErrEngineClosed) {
		t.Fatalf(
			"sibling abort = execute:%v fatal:%+v, want uncommitted engine-closed abort",
			executeErr,
			fatal,
		)
	}
	outcome, coordinateErr := engine.coordinateAcceptedResponsePostJoin(
		"step",
		prepared,
		completed,
		collector,
		executeErr,
	)
	if !errors.As(coordinateErr, &fatal) {
		t.Fatalf("post-join error = %v, want Result Group fatal", coordinateErr)
	}
	for index, cell := range completed {
		if cell != nil {
			t.Fatalf(
				"completed cell %d = %+v, want absent after sibling abort",
				index,
				cell,
			)
		}
	}
	if outcome.results != nil {
		t.Fatalf(
			"post-join fatal results = %+v, want no materialized result cells",
			outcome.results,
		)
	}
}

func TestHostedStartOperationalFailureAbortsResultGroupWithoutSyntheticInterruption(t *testing.T) {
	t.Parallel()
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	engine.closed.Store(true)

	results, _, err := engine.executeAcceptedToolCallsCoordinated(
		context.Background(),
		"step",
		acceptedHostedExecution("hosted-start-failure"),
	)
	engine.closed.Store(false)
	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) ||
		fatal.Committed ||
		!errors.Is(fatal.Cause, ErrEngineClosed) {
		t.Fatalf(
			"hosted start failure = results:%+v error:%v fatal:%+v",
			results,
			err,
			fatal,
		)
	}
	if len(results) != 0 {
		t.Fatalf("hosted start failure results = %+v, want none", results)
	}
	if _, found := engine.transcriptRuntimeState().
		ToolCompletionSnapshot("hosted-start-failure"); found {
		t.Fatal("hosted start failure produced a synthetic completion")
	}
}

func TestHostedReportOperationalFailureAbortsResultGroupWithoutSyntheticInterruption(t *testing.T) {
	t.Parallel()
	var engine *Engine
	engine = mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model: "gpt-5",
			OnEvent: func(event Event) {
				if engine != nil && event.Kind == EventToolCallStarted {
					engine.closed.Store(true)
				}
			},
		},
	)

	results, _, err := engine.executeAcceptedToolCallsCoordinated(
		context.Background(),
		"step",
		acceptedHostedExecution("hosted-report-failure"),
	)
	engine.closed.Store(false)
	var fatal *resultGroupFatal
	if !errors.As(err, &fatal) ||
		fatal.Committed ||
		!errors.Is(fatal.Cause, ErrEngineClosed) {
		t.Fatalf(
			"hosted report failure = results:%+v error:%v fatal:%+v",
			results,
			err,
			fatal,
		)
	}
	if len(results) != 0 {
		t.Fatalf("hosted report failure results = %+v, want none", results)
	}
	if _, found := engine.transcriptRuntimeState().
		ToolCompletionSnapshot("hosted-report-failure"); found {
		t.Fatal("hosted report failure produced a synthetic completion")
	}
}

func acceptedHostedExecution(callID string) acceptedResponseCalls {
	return acceptedResponseCalls{
		hosted: []hostedToolExecution{{
			Call: llm.ToolCall{
				ID:   callID,
				Name: string(toolspec.ToolWebSearch),
			},
			Result: tools.Result{
				CallID: callID,
				Name:   toolspec.ToolWebSearch,
				Output: json.RawMessage(`{"ok":true}`),
			},
		}},
		order: []acceptedResponseCallRef{{
			source: acceptedResponseCallHosted,
		}},
	}
}

func TestExecuteToolCallsMaterializesSuccessfulModelWarningBeforePersistence(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	handler := &toolExecutionProbe{
		warnings: []tools.ModelWarning{tools.ForeignManagedWorktreeEditWarning()},
	}
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolPatch,
			Handler: handler,
		}),
		Config{Model: "gpt-5"},
	)

	results, err := engine.executeToolCalls(context.Background(), "step", []llm.ToolCall{{
		ID:    "warned-patch",
		Name:  string(toolspec.ToolPatch),
		Input: json.RawMessage(`{}`),
	}})
	if err != nil {
		t.Fatalf("execute warned tool: %v", err)
	}
	if len(results) != 1 || len(results[0].ModelWarnings) != 0 {
		t.Fatalf("materialized result = %+v", results)
	}
	var output map[string]json.RawMessage
	if err := json.Unmarshal(results[0].Output, &output); err != nil {
		t.Fatalf("decode materialized output: %v", err)
	}
	if _, ok := output["ok"]; !ok {
		t.Fatalf("materialized output lost success: %s", results[0].Output)
	}
	if _, ok := output["warning"]; !ok {
		t.Fatalf("materialized output lost warning: %s", results[0].Output)
	}
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(8)
	if err != nil {
		t.Fatalf("read persisted records: %v", err)
	}
	for _, record := range window.Records {
		completion, ok := mustSessionEventPayload(record).(session.ToolCompletionRecord)
		if !ok || completion.CallID != "warned-patch" {
			continue
		}
		var persisted map[string]json.RawMessage
		if err := json.Unmarshal(completion.Output, &persisted); err != nil {
			t.Fatalf("decode persisted output: %v", err)
		}
		if _, ok := persisted["warning"]; ok {
			return
		}
	}
	t.Fatal("persisted completion omitted model warning")
}

func TestExecuteToolCallsRejectsInvalidWebSearchBeforeHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		callID string
		input  json.RawMessage
	}{
		{name: "whitespace query", callID: "web-search-whitespace", input: json.RawMessage(`{"query":"   "}`)},
		{name: "hallucinated query", callID: "web-search-hallucinated", input: json.RawMessage(`{"query":"web search"}`)},
	}
	probe := &webSearchExecutionProbe{}
	var completionMu sync.Mutex
	var completionEvents []Event
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolWebSearch,
			Handler: probe,
		}),
		Config{
			Model: "gpt-5",
			OnEvent: func(event Event) {
				if event.Kind != EventToolCallCompleted || event.ToolResult == nil {
					return
				}
				result := *event.ToolResult
				completionMu.Lock()
				completionEvents = append(completionEvents, Event{
					Kind:                       event.Kind,
					CommittedTranscriptChanged: event.CommittedTranscriptChanged,
					ToolResult:                 &result,
				})
				completionMu.Unlock()
			},
		},
	)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handlerCallsBefore := probe.calls.Load()
			completionMu.Lock()
			completionsBefore := len(completionEvents)
			completionMu.Unlock()

			results, err := engine.executeToolCalls(context.Background(), "step", []llm.ToolCall{{
				ID:    test.callID,
				Name:  string(toolspec.ToolWebSearch),
				Input: test.input,
			}})
			if err != nil {
				t.Fatalf("execute invalid web search tool call: %v", err)
			}
			if got := probe.calls.Load(); got != handlerCallsBefore {
				t.Fatalf("invalid web search reached handler: calls = %d, want %d", got, handlerCallsBefore)
			}
			if len(results) != 1 {
				t.Fatalf("invalid web search results = %+v, want one", results)
			}
			if result := results[0]; result.CallID != test.callID ||
				result.Name != toolspec.ToolWebSearch ||
				!result.IsError {
				t.Fatalf("invalid web search result = %+v", result)
			}
			var output map[string]string
			if err := json.Unmarshal(results[0].Output, &output); err != nil {
				t.Fatalf("decode invalid web search output: %v", err)
			}
			if got := output["error"]; got != tools.InvalidWebSearchQueryMessage {
				t.Fatalf("invalid web search error = %q, want %q", got, tools.InvalidWebSearchQueryMessage)
			}
			completion, found := engine.transcriptRuntimeState().ToolCompletionSnapshot(test.callID)
			if !found || !completion.IsError {
				t.Fatalf("invalid web search runtime completion = %+v, found=%t", completion, found)
			}

			completionMu.Lock()
			defer completionMu.Unlock()
			newCompletions := completionEvents[completionsBefore:]
			if len(newCompletions) != 1 {
				t.Fatalf("persisted invalid web search completions = %+v, want one new completion", newCompletions)
			}
			completionEvent := newCompletions[0]
			if !completionEvent.CommittedTranscriptChanged ||
				completionEvent.ToolResult == nil ||
				completionEvent.ToolResult.CallID != test.callID ||
				completionEvent.ToolResult.Name != toolspec.ToolWebSearch ||
				!completionEvent.ToolResult.IsError {
				t.Fatalf("persisted invalid web search completion = %+v", completionEvent)
			}
		})
	}
}

func TestExecuteToolCallsAppliesNormalCompletionOnlyAfterCommit(t *testing.T) {
	t.Parallel()
	t.Run("uncommitted append", func(t *testing.T) {
		store := mustCreateTestSession(t)
		probe := &toolExecutionProbe{}
		engine := mustNewTestEngine(
			t,
			store,
			&fakeClient{},
			tools.NewRegistry(tools.HandlerRegistration{
				ID:      toolspec.ToolExecCommand,
				Handler: probe,
			}),
			Config{Model: "gpt-5"},
		)
		blocker := mustBlockTestEventLogAppends(t, store)

		results, err := engine.executeToolCalls(context.Background(), "step", []llm.ToolCall{{
			ID:    "uncommitted-tool",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"pwd"}`),
		}})
		var fatal *resultGroupFatal
		if !errors.As(err, &fatal) || fatal.Committed {
			t.Fatalf("uncommitted tool completion error = %v", err)
		}
		if got := probe.calls.Load(); got != 1 {
			t.Fatalf("uncommitted tool handler calls = %d, want one", got)
		}
		if len(results) != 0 {
			t.Fatalf("uncommitted fatal tool results = %+v, want none", results)
		}
		if err := blocker.Restore(); err != nil {
			t.Fatalf("restore event-log append blocker: %v", err)
		}

		window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(8)
		if err != nil {
			t.Fatalf("read bounded uncommitted tool records: %v", err)
		}
		for _, record := range window.Records {
			completion, ok := mustSessionEventPayload(record).(session.ToolCompletionRecord)
			if ok && completion.CallID == "uncommitted-tool" {
				t.Fatalf("uncommitted tool completion persisted: %+v", completion)
			}
		}
	})

	t.Run("committed observer failure", func(t *testing.T) {
		observerErr := errors.New("tool completion observer failure")
		gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
		store := mustCreateTestSessionAt(
			t,
			t.TempDir(),
			session.WithPersistenceObserver(gate),
		)
		probe := &toolExecutionProbe{}
		engine := mustNewTestEngine(
			t,
			store,
			&fakeClient{},
			tools.NewRegistry(tools.HandlerRegistration{
				ID:      toolspec.ToolExecCommand,
				Handler: probe,
			}),
			Config{Model: "gpt-5"},
		)
		gate.FailNext(observerErr)

		results, err := engine.executeToolCalls(context.Background(), "step", []llm.ToolCall{{
			ID:    "committed-tool",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"pwd"}`),
		}})
		var fatal *resultGroupFatal
		if !errors.As(err, &fatal) ||
			!fatal.Committed ||
			!errors.Is(fatal.Cause, observerErr) {
			t.Fatalf("committed tool completion error = %v", err)
		}
		if got := probe.calls.Load(); got != 1 {
			t.Fatalf("committed tool handler calls = %d, want one", got)
		}
		if len(results) != 0 {
			t.Fatalf("committed fatal tool results = %+v, want none", results)
		}

		window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(8)
		if err != nil {
			t.Fatalf("read bounded committed tool records: %v", err)
		}
		completions := 0
		for _, record := range window.Records {
			completion, ok := mustSessionEventPayload(record).(session.ToolCompletionRecord)
			if !ok || completion.CallID != "committed-tool" {
				continue
			}
			completions++
			if completion.Name != string(toolspec.ToolExecCommand) || completion.IsError {
				t.Fatalf("committed tool completion = %+v", completion)
			}
		}
		if completions != 1 {
			t.Fatalf("committed tool completions = %d, want one", completions)
		}
	})
}

type toolExecutionProbe struct {
	called   bool
	calls    atomic.Int32
	warnings []tools.ModelWarning
}

type toolDurabilityObservationRecorder struct {
	mu      sync.Mutex
	appends []session.EventLogAppendObservation
	syncs   []session.EventLogSyncObservation
}

func (r *toolDurabilityObservationRecorder) ObserveEventLogAppend(observation session.EventLogAppendObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appends = append(r.appends, observation)
}

func (r *toolDurabilityObservationRecorder) ObserveEventLogSync(observation session.EventLogSyncObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.syncs = append(r.syncs, observation)
}

func (r *toolDurabilityObservationRecorder) snapshot() ([]session.EventLogAppendObservation, []session.EventLogSyncObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]session.EventLogAppendObservation(nil), r.appends...),
		append([]session.EventLogSyncObservation(nil), r.syncs...)
}

type durabilityToolHandler struct{}

func (durabilityToolHandler) Call(_ context.Context, call tools.Call) (tools.Result, error) {
	return tools.Result{
		CallID: call.ID,
		Name:   call.Name,
		Output: json.RawMessage(`{"ok":true}`),
	}, nil
}

type failingToolHandler struct {
	err error
}

func (h failingToolHandler) Call(_ context.Context, call tools.Call) (tools.Result, error) {
	return tools.Result{CallID: call.ID, Name: call.Name}, h.err
}

type durableIntentObservation struct {
	found bool
	err   error
}

type durableIntentToolHandler struct {
	store    *session.Store
	observed chan<- durableIntentObservation
}

func (h durableIntentToolHandler) Call(_ context.Context, call tools.Call) (tools.Result, error) {
	eventLog, err := h.store.MaterializeEventLog()
	if err != nil {
		h.observed <- durableIntentObservation{err: err}
		return tools.Result{}, err
	}
	window, err := eventLog.ReadRecentRecords(16)
	if err != nil {
		h.observed <- durableIntentObservation{err: err}
		return tools.Result{}, err
	}
	found := false
	for _, record := range window.Records {
		payload, payloadErr := record.Payload()
		if payloadErr != nil {
			h.observed <- durableIntentObservation{err: payloadErr}
			return tools.Result{}, payloadErr
		}
		message, ok := payload.(session.MessageRecord)
		if !ok {
			continue
		}
		for _, persistedCall := range message.ToolCalls {
			if persistedCall.CallID == call.ID {
				found = true
			}
		}
	}
	h.observed <- durableIntentObservation{found: found}
	return tools.Result{
		CallID: call.ID,
		Name:   call.Name,
		Output: json.RawMessage(`{"ok":true}`),
	}, nil
}

func TestLocalToolHandlerStartsAfterAcceptedIntentIsDurable(t *testing.T) {
	store := mustCreateTestSession(t)
	observed := make(chan durableIntentObservation, 1)
	call := llm.ToolCall{
		ID:    "durable-intent",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"cmd":"true"}`),
	}
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("running", call),
		finalTextResponse("done"),
	}}
	engine := mustNewTestEngine(
		t,
		store,
		client,
		tools.NewRegistry(tools.HandlerRegistration{
			ID: toolspec.ToolExecCommand,
			Handler: durableIntentToolHandler{
				store:    store,
				observed: observed,
			},
		}),
		Config{Model: "gpt-5"},
	)

	if _, err := engine.SubmitUserMessage(t.Context(), "run"); err != nil {
		t.Fatalf("submit tool turn: %v", err)
	}
	observation := <-observed
	if observation.err != nil {
		t.Fatalf("inspect durable intent: %v", observation.err)
	}
	if !observation.found {
		t.Fatal("local tool handler started before its accepted assistant intent was durable")
	}
}

func TestExecuteToolCallsCommitsHandlerErrorAsHonestResult(t *testing.T) {
	handlerErr := errors.New("handler failed")
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: failingToolHandler{err: handlerErr},
		}),
		Config{Model: "gpt-5"},
	)

	results, err := engine.executeToolCalls(context.Background(), "step", []llm.ToolCall{{
		ID:    "handler-error",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"cmd":"true"}`),
	}})
	if !errors.Is(err, handlerErr) {
		t.Fatalf("execute handler error = %v, want %v", err, handlerErr)
	}
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("handler error results = %+v, want one honest error result", results)
	}
	snapshot := mustTranscriptHydrationSnapshot(t, engine)
	if rows := countHydratedToolRows(snapshot, "handler-error"); rows != 1 {
		t.Fatalf("handler error projected tool rows = %d, want 1", rows)
	}
	for _, row := range snapshot.CommittedRows {
		if row.Tool != nil && row.Tool.ToolCallID == "handler-error" && !row.Tool.IsError {
			t.Fatalf("handler error row = %+v, want error", row)
		}
	}
}

func TestToolExecutionDurabilityObservationBaseline(t *testing.T) {
	for _, count := range []int{1, 3} {
		t.Run(fmt.Sprintf("%d tools", count), func(t *testing.T) {
			observer := &toolDurabilityObservationRecorder{}
			store := mustCreateTestSessionAt(
				t,
				t.TempDir(),
				session.WithDurabilityObserver(observer),
			)
			engine := mustNewTestEngine(
				t,
				store,
				&fakeClient{},
				tools.NewRegistry(tools.HandlerRegistration{
					ID:      toolspec.ToolExecCommand,
					Handler: durabilityToolHandler{},
				}),
				Config{Model: "gpt-5"},
			)
			calls := make([]llm.ToolCall, count)
			for index := range calls {
				calls[index] = llm.ToolCall{
					ID:    fmt.Sprintf("baseline-%d", index),
					Name:  string(toolspec.ToolExecCommand),
					Input: json.RawMessage(`{"cmd":"true"}`),
				}
			}

			results, err := engine.executeToolCalls(context.Background(), "step", calls)
			if err != nil {
				t.Fatalf("execute tools: %v", err)
			}
			if len(results) != count {
				t.Fatalf("tool results = %d, want %d", len(results), count)
			}
			appends, syncs := observer.snapshot()
			if len(appends) != 1 || len(syncs) != 1 {
				t.Fatalf(
					"durability observations = %d appends/%d syncs, want 1/1",
					len(appends),
					len(syncs),
				)
			}
			for index, appendObservation := range appends {
				if appendObservation.RecordCount != count*2 || !appendObservation.Succeeded {
					t.Fatalf(
						"append observation %d = %+v, want %d successful records",
						index,
						appendObservation,
						count*2,
					)
				}
			}
			for index, syncObservation := range syncs {
				if !syncObservation.Succeeded {
					t.Fatalf("sync observation %d = %+v, want success", index, syncObservation)
				}
			}
			appendLatencies := make([]time.Duration, len(appends))
			for index, observation := range appends {
				appendLatencies[index] = observation.Latency
			}
			syncLatencies := make([]time.Duration, len(syncs))
			for index, observation := range syncs {
				syncLatencies[index] = observation.Latency
			}
			t.Logf(
				"baseline tools=%d append_transactions=%d physical_syncs=%d append_latencies=%v sync_latencies=%v",
				count,
				len(appends),
				len(syncs),
				appendLatencies,
				syncLatencies,
			)
		})
	}
}

func TestExecuteToolCallsCommitsSuccessfulResultsAsOneGroup(t *testing.T) {
	observer := &toolDurabilityObservationRecorder{}
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithDurabilityObserver(observer),
	)
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: durabilityToolHandler{},
		}),
		Config{Model: "gpt-5"},
	)
	calls := []llm.ToolCall{
		{
			ID:    "first",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"true"}`),
		},
		{
			ID:    "second",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"true"}`),
		},
	}

	results, err := engine.executeToolCalls(context.Background(), "step", calls)
	if err != nil {
		t.Fatalf("execute tools: %v", err)
	}
	if len(results) != len(calls) {
		t.Fatalf("tool results = %d, want %d", len(results), len(calls))
	}
	appends, syncs := observer.snapshot()
	if len(appends) != 1 || len(syncs) != 1 {
		t.Fatalf(
			"group durability observations = %d appends/%d syncs, want 1/1",
			len(appends),
			len(syncs),
		)
	}
	if appends[0].RecordCount != len(calls)*2 || !appends[0].Succeeded {
		t.Fatalf(
			"group append observation = %+v, want %d successful records",
			appends[0],
			len(calls)*2,
		)
	}
	snapshot := mustTranscriptHydrationSnapshot(t, engine)
	for _, call := range calls {
		if rows := countHydratedToolRows(snapshot, call.ID); rows != 1 {
			t.Fatalf("projected tool rows for %s = %d, want 1", call.ID, rows)
		}
	}
}

func (p *toolExecutionProbe) Call(_ context.Context, call tools.Call) (tools.Result, error) {
	p.called = true
	p.calls.Add(1)
	return tools.Result{
		CallID:        call.ID,
		Name:          call.Name,
		Output:        json.RawMessage(`{"ok":true}`),
		ModelWarnings: append([]tools.ModelWarning(nil), p.warnings...),
	}, nil
}

type webSearchExecutionProbe struct {
	calls atomic.Int32
}

func (p *webSearchExecutionProbe) Call(context.Context, tools.Call) (tools.Result, error) {
	p.calls.Add(1)
	return tools.Result{}, nil
}

type closeEngineBeforeResultReportHandler struct {
	engine *Engine
	calls  atomic.Int32
}

func (h *closeEngineBeforeResultReportHandler) Call(
	_ context.Context,
	call tools.Call,
) (tools.Result, error) {
	h.calls.Add(1)
	h.engine.closed.Store(true)
	return tools.Result{
		CallID: call.ID,
		Name:   call.Name,
		Output: json.RawMessage(`{"ok":true}`),
	}, nil
}

type serialSiblingAbortToolHandler struct {
	engine *Engine
}

func (h *serialSiblingAbortToolHandler) Call(
	_ context.Context,
	call tools.Call,
) (tools.Result, error) {
	if call.ID == "abort-sibling" {
		h.engine.closed.Store(true)
	}
	return tools.Result{
		CallID: call.ID,
		Name:   call.Name,
		Output: json.RawMessage(`{"ok":true}`),
	}, nil
}
