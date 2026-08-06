package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestExecuteToolCallsPropagatesContextCancellation(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	started := make(chan struct{})
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID: toolspec.ToolExecCommand,
			Handler: cancellationAwareTool{
				started: started,
			},
		}),
		Config{Model: "gpt-5"},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := engine.executeToolCalls(ctx, "step", []llm.ToolCall{{
			ID:    "canceled-call",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{}`),
		}})
		done <- err
	}()

	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("execute tool calls error = %v, want context cancellation", err)
	}
	if _, ok := engine.transcriptRuntimeState().ToolCompletionSnapshot("canceled-call"); !ok {
		t.Fatal("canceled tool completion was not persisted before returning")
	}
}

func TestExecuteToolCallsClosesCompletedAndInterruptedResultsInRosterOrder(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	secondStarted := make(chan struct{})
	handler := &orderedCancellationTool{secondStarted: secondStarted}
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolPatch,
			Handler: handler,
		}),
		Config{
			Model: "gpt-5",
			CurrentNodeExecution: testWorkflowConfig(
				&fakeWorkflowController{},
				config.WorkflowCompletionModeTool,
			),
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		results []tools.Result
		err     error
	}, 1)
	go func() {
		results, err := engine.executeToolCalls(ctx, "step", []llm.ToolCall{
			{
				ID:          "completed",
				Name:        string(toolspec.ToolPatch),
				Custom:      true,
				CustomInput: textutil.Value("first"),
			},
			{
				ID:          "interrupted",
				Name:        string(toolspec.ToolPatch),
				Custom:      true,
				CustomInput: textutil.Value("second"),
			},
		})
		done <- struct {
			results []tools.Result
			err     error
		}{results: results, err: err}
	}()

	<-secondStarted
	cancel()
	outcome := <-done
	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("execute error = %v, want cancellation after semantic close", outcome.err)
	}
	if len(outcome.results) != 2 ||
		outcome.results[0].CallID != "completed" ||
		outcome.results[1].CallID != "interrupted" ||
		!outcome.results[1].IsError ||
		!bytes.Equal(outcome.results[1].Output, missingToolOutputInterruptedOutput) {
		t.Fatalf("semantic close results = %+v", outcome.results)
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read semantic close records: %v", err)
	}
	var completionIDs []string
	for _, record := range window.Records {
		completion, ok := mustSessionEventPayload(record).(session.ToolCompletionRecord)
		if ok && (completion.CallID == "completed" || completion.CallID == "interrupted") {
			completionIDs = append(completionIDs, completion.CallID)
		}
	}
	if len(completionIDs) != 2 ||
		completionIDs[0] != "completed" ||
		completionIDs[1] != "interrupted" {
		t.Fatalf("semantic close completion order = %v, want completed then interrupted once", completionIDs)
	}
	foundCustomOutput := false
	for _, item := range engine.transcriptRuntimeState().SnapshotItems() {
		if item.CallID != nil &&
			*item.CallID == "interrupted" &&
			item.Type == llm.ResponseItemTypeCustomToolOutput {
			foundCustomOutput = bytes.Equal(item.Output, missingToolOutputInterruptedOutput)
		}
	}
	if !foundCustomOutput {
		t.Fatal("interrupted patch did not retain its custom output kind and typed interruption output")
	}
}

type cancellationAwareTool struct {
	started chan struct{}
}

func (t cancellationAwareTool) Call(ctx context.Context, call tools.Call) (tools.Result, error) {
	select {
	case <-t.started:
	default:
		close(t.started)
	}
	<-ctx.Done()
	return tools.Result{
		CallID:  call.ID,
		Name:    call.Name,
		IsError: true,
		Output:  json.RawMessage(`{"error":"canceled"}`),
	}, ctx.Err()
}

type orderedCancellationTool struct {
	calls         atomic.Int32
	secondStarted chan struct{}
}

func (t *orderedCancellationTool) Call(ctx context.Context, call tools.Call) (tools.Result, error) {
	if t.calls.Add(1) == 1 {
		return tools.Result{
			CallID:  call.ID,
			Name:    call.Name,
			Output:  json.RawMessage(`{"ok":true}`),
			Summary: textutil.Value("complete"),
		}, nil
	}
	close(t.secondStarted)
	<-ctx.Done()
	return tools.Result{}, ctx.Err()
}
