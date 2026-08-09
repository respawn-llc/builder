package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/server/workflowruntime"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"

	"github.com/google/uuid"
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

func TestCompleteNodeKeepsCommittedResultAfterCancelingSiblingTools(t *testing.T) {
	store := mustCreateTestSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	siblingStarted := make(chan struct{})
	baseController := &fakeWorkflowController{}
	controller := &cancelingCompletionController{
		fakeWorkflowController: baseController,
		siblingStarted:         siblingStarted,
		cancel:                 cancel,
	}
	engine := mustNewWorkflowTestEngine(
		t,
		store,
		&fakeClient{},
		testWorkflowConfig(controller, config.WorkflowCompletionModeTool),
		Config{
			EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
		},
	)
	engine.registry = tools.NewRegistry(tools.HandlerRegistration{
		ID: toolspec.ToolExecCommand,
		Handler: cancellationAwareTool{
			started: siblingStarted,
		},
	})
	origin := serverapi.RuntimeStepOrigin{
		RunID:  uuid.NewString(),
		StepID: uuid.NewString(),
	}
	engine.agentSteps.current = &activeAgentStep{
		scopeID: engine.agentSteps.scopeID,
		origin:  origin,
		phase:   agentStepProviderRunning,
	}

	results, err := engine.executeToolCalls(ctx, origin.StepID, []llm.ToolCall{
		{
			ID:    "sibling-tool",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"sleep"}`),
		},
		completeNodeCall(
			"complete-node",
			json.RawMessage(`{"commentary":"complete","summary":"done"}`),
		),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execute completion batch error = %v, want sibling cancellation", err)
	}
	if len(results) != 2 ||
		results[1].CallID != "complete-node" ||
		results[1].IsError ||
		!results[1].Terminal {
		t.Fatalf("committed complete_node result = %+v", results)
	}
	if baseController.completed.Load() != 1 {
		t.Fatalf("workflow completions = %d, want one", baseController.completed.Load())
	}
	if completion, ok := engine.transcriptRuntimeState().ToolCompletionSnapshot("complete-node"); !ok ||
		completion.IsError {
		t.Fatalf("persisted complete_node completion = %+v, found=%t", completion, ok)
	}
	if completion, ok := engine.transcriptRuntimeState().ToolCompletionSnapshot("sibling-tool"); !ok ||
		!completion.IsError {
		t.Fatalf("persisted sibling completion = %+v, found=%t", completion, ok)
	}
}

type cancelingCompletionController struct {
	*fakeWorkflowController
	siblingStarted <-chan struct{}
	cancel         context.CancelFunc
}

func (c *cancelingCompletionController) CompleteCurrentNode(
	ctx context.Context,
	request workflowruntime.CompletionRequest,
) (workflowruntime.CompletionResult, error) {
	select {
	case <-c.siblingStarted:
	case <-ctx.Done():
		return workflowruntime.CompletionResult{}, context.Cause(ctx)
	}
	result, err := c.fakeWorkflowController.CompleteCurrentNode(ctx, request)
	if err == nil {
		c.cancel()
	}
	return result, err
}

type toolExecutionProbe struct {
	called   bool
	calls    atomic.Int32
	warnings []tools.ModelWarning
}

func testResultGroupRosterFromPreparedCalls(calls []executorToolCall) []resultGroupCallIdentity {
	accepted := acceptedResponseCalls{
		local: make([]llm.ToolCall, len(calls)),
		order: make([]acceptedResponseCallRef, len(calls)),
	}
	for index, call := range calls {
		accepted.local[index] = call.call
		accepted.order[index] = acceptedResponseCallRef{
			source: acceptedResponseCallLocal,
			index:  index,
		}
	}
	return resultGroupRosterFromAcceptedCalls(accepted)
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
