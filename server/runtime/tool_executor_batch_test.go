package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	patchtool "core/server/tools/patch"
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

func TestExecuteToolCallsDoesNotRunHandlerBeforeToolStartCommits(t *testing.T) {
	t.Parallel()
	startMutationEntered := make(chan struct{})
	releaseStartMutation := make(chan struct{})
	handlerStarted := make(chan struct{})
	var startOnce sync.Once
	var handlerOnce sync.Once
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID: toolspec.ToolExecCommand,
			Handler: toolExecutionFunc(func(context.Context, tools.Call) (tools.Result, error) {
				handlerOnce.Do(func() { close(handlerStarted) })
				return tools.Result{Output: json.RawMessage(`{"ok":true}`)}, nil
			}),
		}),
		Config{
			Model: "gpt-5",
			OrderedMutation: func(apply func(OrderedMutationTurn) error) error {
				startOnce.Do(func() { close(startMutationEntered) })
				<-releaseStartMutation
				return apply(directOrderedMutationTurn{})
			},
		},
	)

	done := make(chan error, 1)
	go func() {
		_, err := engine.executeToolCalls(context.Background(), "step", []llm.ToolCall{{
			ID:    "gated-tool",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{}`),
		}})
		done <- err
	}()

	select {
	case <-startMutationEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("tool-start mutation did not enter ordered stage")
	}
	select {
	case <-handlerStarted:
		t.Fatal("tool handler ran before tool-start mutation committed")
	default:
	}

	close(releaseStartMutation)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("execute gated tool call: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("gated tool call did not finish")
	}
	select {
	case <-handlerStarted:
	default:
		t.Fatal("tool handler did not run after tool-start mutation committed")
	}
}

func TestExecuteToolCallsMaterializesSuccessfulModelWarningBeforePersistence(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	base := t.TempDir()
	currentRoot := filepath.Join(base, "current")
	foreignRoot := filepath.Join(base, "foreign")
	for _, path := range []string{currentRoot, foreignRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	managedContext, err := tools.NewManagedWorktreePathContext(base, &currentRoot)
	if err != nil {
		t.Fatalf("new managed worktree path context: %v", err)
	}
	patchHandler, err := patchtool.New(currentRoot, true, patchtool.WithManagedWorktreePathContext(managedContext))
	if err != nil {
		t.Fatalf("new patch handler: %v", err)
	}
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: patchHandler}),
		Config{Model: "gpt-5"},
	)

	results, err := engine.executeToolCalls(context.Background(), "step", []llm.ToolCall{{
		ID:    "warned-patch",
		Name:  string(toolspec.ToolPatch),
		Input: json.RawMessage(`{"patch":"*** Begin Patch\n*** Add File: ` + filepath.Join(foreignRoot, "a.txt") + `\n+ok\n*** End Patch\n"}`),
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
		if !errors.Is(err, errPersistToolCompletion) {
			t.Fatalf("uncommitted tool completion error = %v", err)
		}
		if got := probe.calls.Load(); got != 1 {
			t.Fatalf("uncommitted tool handler calls = %d, want one", got)
		}
		if len(results) != 1 || results[0].IsError {
			t.Fatalf("uncommitted tool results = %+v, want successful execution result", results)
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
		if !errors.Is(err, errPersistToolCompletion) || !errors.Is(err, observerErr) {
			t.Fatalf("committed tool completion error = %v", err)
		}
		if got := probe.calls.Load(); got != 1 {
			t.Fatalf("committed tool handler calls = %d, want one", got)
		}
		if len(results) != 1 || results[0].IsError {
			t.Fatalf("committed tool results = %+v, want successful execution result", results)
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
	called bool
	calls  atomic.Int32
}

type toolExecutionFunc func(context.Context, tools.Call) (tools.Result, error)

func (f toolExecutionFunc) Call(ctx context.Context, call tools.Call) (tools.Result, error) {
	return f(ctx, call)
}

func (p *toolExecutionProbe) Call(_ context.Context, call tools.Call) (tools.Result, error) {
	p.called = true
	p.calls.Add(1)
	return tools.Result{
		CallID: call.ID,
		Name:   call.Name,
		Output: json.RawMessage(`{"ok":true}`),
	}, nil
}

type webSearchExecutionProbe struct {
	calls atomic.Int32
}

func (p *webSearchExecutionProbe) Call(context.Context, tools.Call) (tools.Result, error) {
	p.calls.Add(1)
	return tools.Result{}, nil
}
