package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestMissingToolOutputRepairAppendsSyntheticOutputAndRetries(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{
		errors: []error{&llm.APIStatusError{StatusCode: 400, Body: "tool call without output"}},
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("repaired")},
			Usage:     llm.Usage{InputTokens: 10, OutputTokens: 2, WindowTokens: 100},
		}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	steerDanglingToolCall(t, eng, "step", llm.ToolCall{ID: "missing", Name: "exec_command", Input: json.RawMessage(`{}`)})

	message, err := eng.SubmitUserMessage(context.Background(), "continue")
	if err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if content, ok := textutil.OptionalExact(message.Content); !ok || content != "repaired" {
		t.Fatalf("assistant content = %q present=%t, want repaired", content, ok)
	}
	if len(client.calls) != 2 {
		t.Fatalf("model calls = %d, want initial 400 plus repaired retry", len(client.calls))
	}
	if !repairRequestHasToolCall(client.calls[0].Items, "missing") ||
		!repairRequestHasToolCall(client.calls[1].Items, "missing") ||
		!repairRequestHasToolOutput(client.calls[1].Items, "missing") {
		t.Fatalf("repair must append an output without removing the original call")
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded repair records: %v", err)
	}
	var completion *storedToolCompletion
	var warning *storedLocalEntry
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.ToolCompletionRecord:
			got, err := storedToolCompletionFromSessionRecord(payload)
			if err != nil {
				t.Fatalf("restore completion: %v", err)
			}
			if got.CallID == "missing" {
				completion = &got
			}
		case session.LocalEntryRecord:
			got, err := storedLocalEntryFromSessionRecord(payload)
			if err != nil {
				t.Fatalf("restore local entry: %v", err)
			}
			if got.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
				warning = &got
			}
		}
	}
	if completion == nil || !completion.IsError {
		t.Fatalf("missing synthetic error completion: %+v", completion)
	}
	if !bytes.Equal(completion.Output, missingToolOutputInterruptedOutput) {
		t.Fatalf("live generation repair selected the wrong typed disposition: %s", completion.Output)
	}
	if warning == nil ||
		warning.ToolOutputRepair == nil ||
		warning.ToolOutputRepair.Kind != transcript.ToolOutputRepairLiveProviderRejection ||
		warning.ToolOutputRepair.Count != 1 ||
		warning.Text != "" {
		t.Fatalf("operator repair warning facts = %+v", warning)
	}
}

func TestNormalGenerationLive400RepairWaitsForMatchingStartThenRetriesOnce(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{
		errors: []error{
			&llm.APIStatusError{StatusCode: 400},
			&llm.APIStatusError{StatusCode: 400},
		},
		responses: []llm.Response{finalTextResponse("repaired")},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	customInput := "custom input"
	call := llm.ToolCall{
		ID:          "normal-live-custom",
		Name:        "custom_tool",
		Custom:      true,
		CustomInput: &customInput,
	}
	const originalStepID = "normal-live-original-step"
	steerDanglingToolCall(t, eng, originalStepID, call)
	eng.rememberPendingToolCallStarts(map[string]int{call.ID: 1})

	if _, err := eng.SubmitUserMessage(context.Background(), "blocked repair"); !llm.HasHTTPStatus(err, 400) {
		t.Fatalf("generation with matching live start error = %v, want HTTP 400", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("generation calls with matching live start = %d, want one and no retry", len(client.calls))
	}
	if repairRequestHasToolOutput(eng.transcriptRuntimeState().SnapshotItems(), call.ID) {
		t.Fatal("normal generation repaired while the matching live start remained")
	}
	if warnings := typedLiveRepairWarnings(t, store); len(warnings) != 0 {
		t.Fatalf("normal generation warnings while live start remained = %d, want none", len(warnings))
	}

	eng.forgetPendingToolCallStart(call.ID)
	if _, err := eng.SubmitUserMessage(context.Background(), "repair now"); err != nil {
		t.Fatalf("normal generation after live start retirement: %v", err)
	}
	if len(client.calls) != 3 {
		t.Fatalf("normal generation calls = %d, want blocked send plus one repaired retry pair", len(client.calls))
	}
	firstRepairAttempt := client.calls[1]
	retry := client.calls[2]
	firstMetadata := firstRepairAttempt
	firstMetadata.Items = nil
	retryMetadata := retry
	retryMetadata.Items = nil
	if !reflect.DeepEqual(firstMetadata, retryMetadata) {
		t.Fatalf(
			"normal generation retry changed provider operation identity: first=%+v retry=%+v",
			firstMetadata,
			retryMetadata,
		)
	}
	if !repairRequestHasToolCall(firstRepairAttempt.Items, call.ID) ||
		repairRequestHasToolOutput(firstRepairAttempt.Items, call.ID) {
		t.Fatal("normal generation first repair attempt did not retain the dangling custom call")
	}
	if !repairRequestHasToolCall(retry.Items, call.ID) ||
		!repairRequestHasToolOutputType(retry.Items, call.ID, llm.ResponseItemTypeCustomToolOutput) {
		t.Fatal("normal generation retry did not rebuild with the original custom call and custom output")
	}
	stepID, outputKind, completion := repairCompletionTypedFacts(t, store, call.ID)
	if stepID == nil || *stepID != originalStepID {
		t.Fatalf("normal generation repair step = %v, want original %q", stepID, originalStepID)
	}
	if outputKind != session.ToolOutputKindCustom {
		t.Fatalf("normal generation output kind = %q, want custom", outputKind)
	}
	if !completion.IsError || !bytes.Equal(completion.Output, missingToolOutputInterruptedOutput) {
		t.Fatalf("normal generation live disposition = error:%t output:%s", completion.IsError, completion.Output)
	}
	if warnings := typedLiveRepairWarnings(t, store); len(warnings) != 1 {
		t.Fatalf("normal generation typed repair warnings = %d, want one", len(warnings))
	}
}

func TestMissingToolOutputRepairRetryPreservesQueuedSteeringBoundary(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	queued := false
	var eng *Engine
	client := &hookClient{
		errors:   []error{&llm.APIStatusError{StatusCode: 400}},
		response: finalTextResponse("result"),
		beforeReturn: func() error {
			if queued {
				return nil
			}
			queued = true
			if _, err := eng.QueueUserMessage("queued steering"); err != nil {
				return err
			}
			return nil
		},
	}
	eng = mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	steerDanglingToolCall(t, eng, "step", llm.ToolCall{ID: "missing", Name: "exec_command", Input: json.RawMessage(`{}`)})

	if _, err := eng.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if len(client.calls) != 2 {
		t.Fatalf("model calls = %d, want initial 400 plus repaired retry", len(client.calls))
	}
	countUserItems := func(items []llm.ResponseItem) int {
		count := 0
		for _, item := range items {
			if item.Type == llm.ResponseItemTypeMessage && item.Role != nil && *item.Role == llm.RoleUser {
				count++
			}
		}
		return count
	}
	if got, want := countUserItems(client.calls[1].Items), countUserItems(client.calls[0].Items); got != want {
		t.Fatalf("retry user-message count = %d, want protected repair retry to retain %d", got, want)
	}
}

func TestLiveMissingToolOutputRepairWaitsForOutputSteeringBoundary(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	steerDanglingToolCall(t, engine, "step", llm.ToolCall{
		ID: "serialized-repair", Name: "exec_command", Input: json.RawMessage(`{}`),
	})
	restoreStep := setTestActiveStep(engine, "step")
	defer restoreStep()

	type repairOutcome struct {
		count int
		err   error
	}
	started := make(chan struct{})
	done := make(chan repairOutcome, 1)
	go func() {
		close(started)
		count, err := engine.repairMissingToolOutputsByAppending(
			textutil.Value("step"),
			missingToolOutputRepairLiveProvider400,
		)
		done <- repairOutcome{count: count, err: err}
	}()
	<-started
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.count != 1 {
			t.Fatalf("serialized live repair = %+v, want count one", outcome)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("serialized live repair did not finish")
	}
}

func TestMissingToolOutputRepairLeavesUnrelated400Unrepaired(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{
		errors: []error{&llm.APIStatusError{StatusCode: 400, Body: "malformed request"}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	if _, err := eng.SubmitUserMessage(context.Background(), "continue"); err == nil {
		t.Fatal("expected unrelated provider 400 to surface")
	}
	if len(client.calls) != 1 {
		t.Fatalf("model calls = %d, want no repair retry", len(client.calls))
	}
}

func TestRequiredToolChoiceRepairsDanglingOutputAndRebuildsRequest(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{
		errors: []error{
			&llm.APIStatusError{StatusCode: 400},
			&llm.APIStatusError{StatusCode: 401},
		},
	}
	eng := mustNewWorkflowTestEngine(
		t,
		store,
		client,
		&workflowruntime.CurrentNodeExecutionConfig{
			ScopeID:        runtimeids.NewExecutionScopeID(),
			CompletionMode: workflowruntime.CompletionModeTool,
		},
		Config{},
	)
	steerDanglingToolCall(t, eng, "step", llm.ToolCall{ID: "missing", Name: "exec_command", Input: json.RawMessage(`{}`)})

	if _, err := eng.SubmitUserMessage(context.Background(), "continue"); !llm.HasHTTPStatus(err, 401) {
		t.Fatalf("submit error = %v, want unrepaired HTTP 401 after repaired retry", err)
	}
	if len(client.calls) != 2 {
		t.Fatalf("model calls = %d, want initial 400 plus repaired retry", len(client.calls))
	}
	for index, call := range client.calls {
		if call.ToolChoiceMode != llm.ToolChoiceModeRequired {
			t.Fatalf("model call %d tool choice = %q, want required", index, call.ToolChoiceMode)
		}
	}
	if !repairRequestHasToolOutput(client.calls[1].Items, "missing") {
		t.Fatal("required-tool retry omitted the synthetic output")
	}
}

func repairCompletionRecord(
	t *testing.T,
	store *session.Store,
	callID string,
) (session.EventRecord, storedToolCompletion) {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded repair records: %v", err)
	}
	for _, record := range window.Records {
		completion, ok := mustSessionEventPayload(record).(session.ToolCompletionRecord)
		if !ok {
			continue
		}
		stored, err := storedToolCompletionFromSessionRecord(completion)
		if err != nil {
			t.Fatalf("restore completion: %v", err)
		}
		if stored.CallID == callID {
			return record, stored
		}
	}
	t.Fatalf("missing synthetic completion for call %q", callID)
	return session.EventRecord{}, storedToolCompletion{}
}

func steerDanglingToolCall(t *testing.T, engine *Engine, stepID string, call llm.ToolCall) {
	t.Helper()
	restoreStep := setTestActiveStep(engine, stepID)
	defer restoreStep()
	if err := engine.steer(runtimeTestStepID(stepID), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}})); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}
}

func TestRepairMissingToolOutputsPersistSyntheticErrorPresentation(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	steerDanglingToolCall(t, eng, "step", llm.ToolCall{
		ID: "missing", Name: "exec_command", Input: json.RawMessage(`{"cmd":"true"}`),
	})
	restoreStep := setTestActiveStep(eng, "step")
	defer restoreStep()

	if _, err := eng.repairMissingToolOutputsByAppending(textutil.Value("step"), missingToolOutputRepairLiveProvider400); err != nil {
		t.Fatalf("repair: %v", err)
	}
	_, completion := repairCompletionRecord(t, store, "missing")
	if completion.Presentation == nil || !completion.Presentation.IsShell || completion.Presentation.Command == "" {
		t.Fatalf("synthetic completion presentation = %+v, want typed shell presentation", completion.Presentation)
	}
}

func TestCompactionMissingToolOutputRepairAppendsAndRetries(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		compactionErrors: []error{&llm.APIStatusError{StatusCode: 400}, nil},
		compactionResponses: []llm.CompactionResponse{{
			Usage: llm.Usage{WindowTokens: 100},
		}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	steerDanglingToolCall(t, eng, "step", llm.ToolCall{ID: "missing", Name: "exec_command", Input: json.RawMessage(`{}`)})
	request := llm.CompactionRequest{
		Model:      "gpt-5",
		SessionID:  textutil.Value(store.Meta().SessionID),
		InputItems: eng.transcriptRuntimeState().SnapshotItems(),
	}

	restoreStep := setTestActiveStep(eng, "step")
	defer restoreStep()
	dispatchFactory, err := eng.activeDispatchRequestFactory(runtimeTestStepID("step"), nil)
	if err != nil {
		t.Fatalf("create compaction dispatch: %v", err)
	}
	if _, _, _, err := eng.compactWithContextRepairRetry(context.Background(), runtimeTestStepID("step"), client, request, dispatchFactory); err != nil {
		t.Fatalf("compact with repair retry: %v", err)
	}
	if len(client.compactionCalls) != 2 {
		t.Fatalf("compaction calls = %d, want initial 400 plus repaired retry", len(client.compactionCalls))
	}
	if !repairRequestHasToolCall(client.compactionCalls[1].InputItems, "missing") ||
		!repairRequestHasToolOutput(client.compactionCalls[1].InputItems, "missing") {
		t.Fatal("repaired compaction retry did not preserve the call with its synthetic output")
	}
	_, completion := repairCompletionRecord(t, store, "missing")
	if !bytes.Equal(completion.Output, missingToolOutputInterruptedOutput) {
		t.Fatalf("live compaction repair selected the wrong typed disposition: %s", completion.Output)
	}
}

func TestCompactionMissingOutputAfterCollapsePanics(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		compactionErrors: []error{
			&llm.ProviderAPIError{
				ProviderID:   "openai",
				StatusCode:   400,
				Code:         llm.UnifiedErrorCodeContextLengthOverflow,
				ProviderCode: "context_length_exceeded",
			},
			&llm.APIStatusError{StatusCode: 400},
		},
	}
	eng := mustNewExecTestEngine(t, store, client, Config{Model: "gpt-5"})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
		ID:    "call-shell",
		Name:  "exec_command",
		Input: json.RawMessage(`{}`),
	}}}})); err != nil {
		t.Fatalf("append shell tool call: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:       llm.RoleTool,
		ToolCallID: textutil.Value("call-shell"),
		Name:       textutil.Value("exec_command"),
		Content:    textutil.Value(`{"output":"` + strings.Repeat("x", 120_000) + `"}`),
	}})); err != nil {
		t.Fatalf("append shell tool output: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
		ID:    "call-missing",
		Name:  "exec_command",
		Input: json.RawMessage(`{}`),
	}}}})); err != nil {
		t.Fatalf("append dangling tool call: %v", err)
	}
	request := llm.CompactionRequest{
		Model:      "gpt-5",
		SessionID:  textutil.Value(store.Meta().SessionID),
		InputItems: eng.transcriptRuntimeState().SnapshotItems(),
	}

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("expected a missing-output provider error after collapse to violate the invariant")
		}
	}()
	dispatchFactory, err := eng.activeDispatchRequestFactory(runtimeTestStepID("step"), nil)
	if err != nil {
		t.Fatalf("create compaction dispatch: %v", err)
	}
	_, _, _, _ = eng.compactWithContextRepairRetry(context.Background(), runtimeTestStepID("step"), client, request, dispatchFactory)
}

func repairRequestHasToolCall(items []llm.ResponseItem, callID string) bool {
	for _, item := range items {
		if !isToolCallItem(item.Type) {
			continue
		}
		if got, ok := textutil.FirstOptionalTrimmed(item.CallID, item.ID); ok && got == callID {
			return true
		}
	}
	return false
}

func repairRequestHasToolOutput(items []llm.ResponseItem, callID string) bool {
	for _, item := range items {
		if !isToolOutputItem(item.Type) {
			continue
		}
		if got, ok := textutil.OptionalTrimmed(item.CallID); ok && got == callID {
			return true
		}
	}
	return false
}

func repairRequestHasToolOutputType(
	items []llm.ResponseItem,
	callID string,
	outputType llm.ResponseItemType,
) bool {
	for _, item := range items {
		if item.Type != outputType {
			continue
		}
		if got, ok := textutil.OptionalTrimmed(item.CallID); ok && got == callID {
			return true
		}
	}
	return false
}

func repairCompletionTypedFacts(
	t *testing.T,
	store *session.Store,
	callID string,
) (*string, session.ToolOutputKind, storedToolCompletion) {
	t.Helper()
	record, completion := repairCompletionRecord(t, store, callID)
	payload, ok := mustSessionEventPayload(record).(session.ToolCompletionRecord)
	if !ok {
		t.Fatalf("repair record for call %q has payload %T", callID, mustSessionEventPayload(record))
	}
	return record.StepID(), payload.OutputKind, completion
}

func typedLiveRepairWarnings(
	t *testing.T,
	store *session.Store,
) []storedLocalEntry {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(32)
	if err != nil {
		t.Fatalf("read bounded live-repair warnings: %v", err)
	}
	var warnings []storedLocalEntry
	for _, record := range window.Records {
		entry, ok := mustSessionEventPayload(record).(session.LocalEntryRecord)
		if !ok {
			continue
		}
		stored, err := storedLocalEntryFromSessionRecord(entry)
		if err != nil {
			t.Fatalf("restore live-repair warning: %v", err)
		}
		if stored.Visibility == transcript.EntryVisibilityOngoing &&
			stored.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
			warnings = append(warnings, stored)
		}
	}
	return warnings
}
