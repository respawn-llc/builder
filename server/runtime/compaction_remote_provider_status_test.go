package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func captureCompactionLogs(t *testing.T) *testsetup.SlogRecords {
	t.Helper()
	return testsetup.CaptureSlogRecords(t)
}

func checkpointRecords(logs *testsetup.SlogRecords) []testsetup.CapturedSlogRecord {
	var checkpointRecords []testsetup.CapturedSlogRecord
	for _, record := range logs.Records() {
		if _, ok := record.Fields["checkpoint_reason"]; ok {
			checkpointRecords = append(checkpointRecords, record)
		}
	}
	return checkpointRecords
}

func malformedCompactionCheckpointError() *llm.CompactionCheckpointContractError {
	return &llm.CompactionCheckpointContractError{
		Reason:           llm.CompactionCheckpointReasonMultiple,
		CompactionCount:  2,
		OutputCount:      3,
		OutputTypeCounts: map[llm.ResponseItemType]int{llm.ResponseItemTypeCompaction: 2, llm.ResponseItemTypeMessage: 1},
	}
}

func malformedCompactionProviderError() *llm.ProviderAPIError {
	checkpointErr := malformedCompactionCheckpointError()
	providerErr := &llm.ProviderAPIError{
		ProviderID: "openai",
		StatusCode: 502,
		Code:       llm.UnifiedErrorCodeProviderContract,
		Message:    checkpointErr.Error(),
		Raw:        checkpointErr.Error(),
		Err:        checkpointErr,
	}
	providerErr.ProviderRequestID = textutil.Value("provider-request")
	return providerErr
}

func TestRemoteCompactionRetries413OverflowByCollapsingToolOutput(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		compactionErrors: []error{
			&llm.ProviderAPIError{
				ProviderID: "openai",
				StatusCode: 413,
				Code:       llm.UnifiedErrorCodeContextLengthOverflow,
			},
			nil,
		},
		compactionResponses: []llm.CompactionResponse{
			remoteCompactionReplacement(1_000, 100, 2_500),
		},
	}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:               "gpt-5",
		CompactionMode:      "native",
		ContextWindowTokens: 2_500,
	})
	restoreStep := setTestActiveStep(engine, "input")
	if err := engine.steer(runtimeTestStepID("input"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	if err := engine.steer(runtimeTestStepID("input"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
		ID:    "call-1",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"command":"pwd"}`),
	}}}})); err != nil {
		t.Fatalf("persist tool call: %v", err)
	}
	if err := engine.steer(runtimeTestStepID("input"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{
		Role:       llm.RoleTool,
		ToolCallID: textutil.Value("call-1"),
		Name:       textutil.Value(string(toolspec.ToolExecCommand)),
		Content:    textutil.Value(`{"output":"` + strings.Repeat("x", 4_000) + `"}`),
	}})); err != nil {
		t.Fatalf("persist tool output: %v", err)
	}
	restoreStep()

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("schedule compaction after overflow: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if len(client.compactionCalls) != 2 || len(client.calls) != 0 {
		t.Fatalf(
			"remote/local compaction calls = %d/%d, want two/zero",
			len(client.compactionCalls),
			len(client.calls),
		)
	}
	for _, item := range client.compactionCalls[1].Items {
		if item.Type == llm.ResponseItemTypeFunctionCallOutput &&
			item.CallID != nil &&
			*item.CallID == "call-1" &&
			isCollapsedCompactionOverflowShellOutput(item.Output) {
			return
		}
	}
	t.Fatal("overflow retry omitted collapsed typed tool output")
}

func TestMalformedRemoteCompactionFallbackUsesOverflowRepairedInput(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("local summary")},
		}},
		compactionErrors: []error{
			&llm.ProviderAPIError{
				ProviderID: "openai",
				StatusCode: 413,
				Code:       llm.UnifiedErrorCodeContextLengthOverflow,
			},
			malformedCompactionProviderError(),
		},
	}
	engine := mustNewTestEngine(t, store, client, newTestToolRegistry(t), Config{
		Model:               "gpt-5",
		CompactionMode:      "native",
		ContextWindowTokens: 2_500,
	})
	if err := steerTestActiveStep(engine, "overflow-input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	if err := steerTestActiveStep(engine, "overflow-tool-call", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:    "call-1",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"command":"pwd"}`),
		}}}},
	)); err != nil {
		t.Fatalf("persist tool call: %v", err)
	}
	if err := steerTestActiveStep(engine, "overflow-tool-output", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{
			Role:       llm.RoleTool,
			ToolCallID: textutil.Value("call-1"),
			Name:       textutil.Value(string(toolspec.ToolExecCommand)),
			Content:    textutil.Value(`{"output":"` + strings.Repeat("x", 4_000) + `"}`),
		}},
	)); err != nil {
		t.Fatalf("persist tool output: %v", err)
	}

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact context: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if len(client.compactionCalls) != 2 || len(client.calls) != 1 {
		t.Fatalf("remote/local compaction calls = %d/%d, want two/one", len(client.compactionCalls), len(client.calls))
	}
	foundCollapsedOutput := false
	for _, item := range client.calls[0].Items {
		if item.Type == llm.ResponseItemTypeFunctionCallOutput &&
			item.CallID != nil &&
			*item.CallID == "call-1" &&
			isCollapsedCompactionOverflowShellOutput(item.Output) {
			foundCollapsedOutput = true
			break
		}
	}
	if !foundCollapsedOutput {
		t.Fatal("local fallback did not receive the exact overflow-repaired input")
	}
}

func TestMalformedRemoteCompactionCombinesRemoteAndLocalOverflowRepairFacts(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		errors: []error{
			&llm.ProviderAPIError{
				ProviderID: "openai",
				StatusCode: 413,
				Code:       llm.UnifiedErrorCodeContextLengthOverflow,
			},
			nil,
		},
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("local summary")},
		}},
		compactionErrors: []error{
			&llm.ProviderAPIError{
				ProviderID: "openai",
				StatusCode: 413,
				Code:       llm.UnifiedErrorCodeContextLengthOverflow,
			},
			malformedCompactionProviderError(),
		},
	}
	engine := mustNewTestEngine(t, store, client, newTestToolRegistry(t), Config{
		Model:               "gpt-5",
		CompactionMode:      "native",
		ContextWindowTokens: 2_500,
	})
	for _, callID := range []string{"call-1", "call-2"} {
		steerDanglingToolCall(t, engine, "step", llm.ToolCall{
			ID:    callID,
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"command":"pwd"}`),
		})
		if err := steerTestActiveStep(engine, "combined-overflow-tool-output", steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventNone,
			true,
			[]llm.Message{{
				Role:       llm.RoleTool,
				ToolCallID: textutil.Value(callID),
				Name:       textutil.Value(string(toolspec.ToolExecCommand)),
				Content:    textutil.Value(`{"output":"` + strings.Repeat("x", 4_000) + `"}`),
			}},
		)); err != nil {
			t.Fatalf("persist tool output %s: %v", callID, err)
		}
	}

	result, receipt, err := compactNowInActiveTestRun(
		t,
		engine,
		compactionModeManual,
		compactionInstructionsInput{},
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("compact context: receipt=%+v error=%v", receipt, err)
	}
	if len(client.compactionCalls) != 2 || len(client.calls) != 2 {
		t.Fatalf("remote/local request calls = %d/%d, want two/two", len(client.compactionCalls), len(client.calls))
	}
	if got, want := result.overflowRepair.ShellOutputsCollapsed, 2; got != want {
		t.Fatalf("combined shell-output repair count = %d, want %d without loss or double counting", got, want)
	}
	if result.overflowRepair.EstimatedSavedTokens <= 0 {
		t.Fatalf("combined overflow repair facts omitted saved-token total: %+v", result.overflowRepair)
	}
}

func TestRemoteCompactionInheritsEffectiveFastMode(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		compactionResponses: []llm.CompactionResponse{remoteCompactionReplacement(1_000, 100, 2_500)},
		caps: llm.ProviderCapabilities{
			ProviderID:               "chatgpt-codex",
			SupportsResponsesAPI:     true,
			SupportsResponsesCompact: true,
			IsOpenAIFirstParty:       true,
		},
	}
	engine := mustNewTestEngine(t, store, client, newTestToolRegistry(t), Config{
		Model:           "gpt-5.6-sol",
		CompactionMode:  "native",
		FastModeEnabled: true,
	})
	if err := steerTestActiveStep(engine, "fast-mode-input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact context: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if len(client.compactionCalls) != 1 {
		t.Fatalf("compaction calls = %d, want one", len(client.compactionCalls))
	}
	if !client.compactionCalls[0].FastMode {
		t.Fatal("remote compaction did not inherit effective Fast Mode")
	}
}

func TestRemoteCompactionTransientRetryReusesUnchangedDispatchState(t *testing.T) {
	withCompactionRetryDelays(t, []time.Duration{0})
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		compactionErrors: []error{
			errors.New("temporary provider failure"),
			nil,
		},
		compactionResponses: []llm.CompactionResponse{remoteCompactionReplacement(1_000, 100, 2_500)},
	}
	engine := mustNewTestEngine(t, store, client, newTestToolRegistry(t), Config{
		Model:          "gpt-5",
		CompactionMode: "native",
	})
	if err := steerTestActiveStep(engine, "transient-retry-input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact context: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if len(client.compactionCalls) != 2 {
		t.Fatalf("compaction calls = %d, want transient retry", len(client.compactionCalls))
	}
	first := client.compactionCalls[0].CodexDispatch
	second := client.compactionCalls[1].CodexDispatch
	if first == nil || second == nil {
		t.Fatal("transient retry omitted Codex dispatch state")
	}
	if first != second {
		t.Fatal("unchanged transient retry allocated a fresh dispatch-state handle")
	}
}

func TestRemoteCompactionFailureAndCheckpointFallback(t *testing.T) {
	t.Run("non-overflow provider 400 fails without replacement or fallback", func(t *testing.T) {
		store, client, engine := newRemoteCompactionFixture(t, &llm.ProviderAPIError{
			ProviderID: "openai",
			StatusCode: 400,
			Code:       llm.UnifiedErrorCodeUnknown,
		})
		var events []Event
		engine.cfg.OnEvent = func(event Event) {
			events = append(events, event)
		}
		if err := engine.CompactContext(context.Background(), ""); err != nil {
			t.Fatalf("schedule compaction: %v", err)
		}
		waitEngineLifecycleTasks(t, engine)
		assertRemoteCompactionFailureWithoutReplacementOrFallback(
			t,
			store,
			client,
			events,
			1,
		)
	})

	t.Run("404 fails without replacement or fallback", func(t *testing.T) {
		store, client, engine := newRemoteCompactionFixture(t, &llm.ProviderAPIError{
			ProviderID: "openai",
			StatusCode: 404,
			Code:       llm.UnifiedErrorCodeUnknown,
		})
		var events []Event
		engine.cfg.OnEvent = func(event Event) {
			events = append(events, event)
		}
		if err := engine.CompactContext(context.Background(), ""); err != nil {
			t.Fatalf("schedule compaction: %v", err)
		}
		waitEngineLifecycleTasks(t, engine)
		assertRemoteCompactionFailureWithoutReplacementOrFallback(t, store, client, events, 1)
	})

	t.Run("missing checkpoint falls back to local", func(t *testing.T) {
		_, client, engine := newRemoteCompactionFixture(t, malformedCompactionProviderError())
		if err := engine.CompactContext(context.Background(), ""); err != nil {
			t.Fatalf("schedule compaction: %v", err)
		}
		waitEngineLifecycleTasks(t, engine)
		summaries := 0
		for _, item := range engine.transcriptRuntimeState().SnapshotItems() {
			if item.Type == llm.ResponseItemTypeMessage &&
				item.MessageType != nil &&
				*item.MessageType == llm.MessageTypeCompactionSummary {
				summaries++
			}
		}
		if len(client.compactionCalls) != 1 || len(client.calls) != 1 || summaries != 1 {
			t.Fatalf(
				"remote/local/summary calls = %d/%d/%d, want one/one/one",
				len(client.compactionCalls),
				len(client.calls),
				summaries,
			)
		}
	})
}

func TestRemoteCompactionOrdinaryProviderFailuresDoNotFallbackToLocal(t *testing.T) {
	withCompactionRetryDelays(t, []time.Duration{0})
	tests := []struct {
		name string
		err  error
		ctx  func() context.Context
	}{
		{
			name: "authentication",
			err: &llm.ProviderAPIError{
				ProviderID: "openai",
				StatusCode: 401,
				Code:       llm.UnifiedErrorCodeAuthentication,
			},
			ctx: context.Background,
		},
		{
			name: "transport",
			err:  errors.New("transport connection failed"),
			ctx:  context.Background,
		},
		{
			name: "timeout",
			err:  context.DeadlineExceeded,
			ctx:  context.Background,
		},
		{
			name: "cancellation",
			err:  context.Canceled,
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
		},
		{
			name: "unrelated provider contract",
			err: &llm.ProviderAPIError{
				ProviderID: "openai",
				StatusCode: 502,
				Code:       llm.UnifiedErrorCodeProviderContract,
				Err:        errors.New("non-checkpoint contract failure"),
			},
			ctx: context.Background,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, client, engine := newRemoteCompactionFixture(t, test.err)
			client.compactionErrors = nil
			client.compactionErr = test.err
			stepID := runtimeTestStepID("ordinary-provider-failure-" + test.name)
			err := runTestActiveStep(engine, stepID, func() error {
				_, _, compactErr := engine.compactNow(test.ctx(), stepID, compactionModeManual, compactionInstructionsInput{}, true)
				return compactErr
			})
			if err == nil {
				t.Fatal("compaction succeeded after ordinary provider failure")
			}
			if len(client.calls) != 0 {
				t.Fatalf("local Generate calls = %d, want zero", len(client.calls))
			}
			if !errors.Is(err, test.err) &&
				!(errors.Is(test.err, context.Canceled) && errors.Is(err, context.Canceled)) {
				t.Fatalf("compaction error = %v, want original provider failure %v", err, test.err)
			}
		})
	}
}

func TestMalformedRemoteCompactionPanicsInDebugBeforeLocalFallback(t *testing.T) {
	t.Parallel()
	_, client, engine := newRemoteCompactionFixture(t, malformedCompactionProviderError())
	engine.cfg.Debug = true

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("malformed checkpoint did not panic in debug mode")
		}
		err, ok := recovered.(error)
		if !ok {
			t.Fatalf("panic = %T %v, want checkpoint contract error", recovered, recovered)
		}
		var checkpointErr *llm.CompactionCheckpointContractError
		if !errors.As(err, &checkpointErr) {
			t.Fatalf("panic = %T %v, want checkpoint contract error", err, err)
		}
		if len(client.calls) != 0 {
			t.Fatalf("local compaction calls = %d, want zero in debug mode", len(client.calls))
		}
	}()

	stepID := runtimeTestStepID("debug-malformed-checkpoint")
	_ = runTestActiveStep(engine, stepID, func() error {
		_, _, compactErr := engine.compactNow(context.Background(), stepID, compactionModeManual, compactionInstructionsInput{}, true)
		return compactErr
	})
}

func TestMalformedRemoteCompactionLogsSafeDiagnosticAndFallsBackOnce(t *testing.T) {
	logs := captureCompactionLogs(t)
	store, client, engine := newRemoteCompactionFixture(t, malformedCompactionProviderError())
	client.responses = []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("local summary")},
	}}

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact context: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if len(client.compactionCalls) != 1 || len(client.calls) != 1 {
		t.Fatalf("remote/local compaction calls = %d/%d, want one/one", len(client.compactionCalls), len(client.calls))
	}
	if _, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(4); err != nil {
		t.Fatalf("read committed compaction records: %v", err)
	}
	records := checkpointRecords(logs)
	if len(records) != 1 {
		t.Fatalf("structured diagnostics = %d, want one", len(records))
	}
	record := records[0]
	if record.Level != slog.LevelError {
		t.Fatalf("diagnostic level = %s, want error", record.Level)
	}
	wantKeys := map[string]bool{
		"provider_id": true, "status_code": true, "request_id": true,
		"checkpoint_reason": true, "compaction_count": true, "output_count": true,
		"output_type_counts": true,
	}
	if len(record.Fields) != len(wantKeys) {
		t.Fatalf("diagnostic keys = %v, want %v", record.Fields, wantKeys)
	}
	for key := range wantKeys {
		if _, ok := record.Fields[key]; !ok {
			t.Fatalf("diagnostic omitted approved key %q", key)
		}
	}
	if got, ok := record.Fields["provider_id"].(string); !ok || got != "openai" {
		t.Fatalf("provider_id = %q, want openai", got)
	}
	if got, ok := record.Fields["status_code"].(int64); !ok || got != 502 {
		t.Fatalf("status_code = %d, want 502", got)
	}
	if got, ok := record.Fields["request_id"].(string); !ok || got != "provider-request" {
		t.Fatalf("request_id = %q, want provider-request", got)
	}
	if got, ok := record.Fields["checkpoint_reason"].(string); !ok || got != string(llm.CompactionCheckpointReasonMultiple) {
		t.Fatalf("checkpoint_reason = %q, want %q", got, llm.CompactionCheckpointReasonMultiple)
	}
	if got, ok := record.Fields["compaction_count"].(int64); !ok || got != 2 {
		t.Fatalf("compaction_count = %d, want 2", got)
	}
	if got, ok := record.Fields["output_count"].(int64); !ok || got != 3 {
		t.Fatalf("output_count = %d, want 3", got)
	}
	if got, ok := record.Fields["output_type_counts"].(map[llm.ResponseItemType]int); !ok || !reflect.DeepEqual(got, map[llm.ResponseItemType]int{llm.ResponseItemTypeCompaction: 2, llm.ResponseItemTypeMessage: 1}) {
		t.Fatalf("output_type_counts = %#v, want typed counts", record.Fields["output_type_counts"])
	}
}

func TestMalformedRemoteCompactionJoinsLocalFailureAndKeepsOneDiagnostic(t *testing.T) {
	logs := captureCompactionLogs(t)
	_, client, engine := newRemoteCompactionFixture(t, malformedCompactionProviderError())
	client.responses = []llm.Response{{ToolCalls: []llm.ToolCall{{ID: "local-failure", Name: "exec_command"}}}}

	stepID := runtimeTestStepID("compact")
	err := runTestActiveStep(engine, stepID, func() error {
		_, _, compactErr := engine.compactNow(context.Background(), stepID, compactionModeManual, compactionInstructionsInput{}, false)
		return compactErr
	})
	var checkpointErr *llm.CompactionCheckpointContractError
	if !errors.As(err, &checkpointErr) {
		t.Fatalf("compaction error = %T %v, want original checkpoint contract error", err, err)
	}
	if len(client.compactionCalls) != 1 || len(client.calls) == 0 {
		t.Fatalf("remote/local compaction calls = %d/%d, want one/at least one", len(client.compactionCalls), len(client.calls))
	}
	if records := checkpointRecords(logs); len(records) != 1 {
		t.Fatalf("structured diagnostics = %d, want one", len(records))
	}
}

func TestMalformedRemoteCompactionJoinsLocalDispatchFailure(t *testing.T) {
	logs := captureCompactionLogs(t)
	base := &fakeCompactionClient{
		compactionErrors: []error{malformedCompactionProviderError()},
	}
	client := &compactionCallbackClient{fakeCompactionClient: base}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model:          "gpt-5",
		CompactionMode: "native",
	})
	client.onCompact = func() {
		engine.stepLifecycle = &stubExclusiveStepLifecycle{}
	}
	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("schedule compaction: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if len(base.compactionCalls) != 1 || len(base.calls) != 0 {
		t.Fatalf("remote/local compaction calls = %d/%d, want one/zero", len(base.compactionCalls), len(base.calls))
	}
	if records := checkpointRecords(logs); len(records) != 1 {
		t.Fatalf("structured diagnostics = %d, want one", len(records))
	}
}

func newRemoteCompactionFixture(
	t *testing.T,
	compactionError error,
) (*session.Store, *fakeCompactionClient, *Engine) {
	t.Helper()

	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
		}},
		compactionErrors: []error{compactionError},
		compactionResponses: []llm.CompactionResponse{{
			Checkpoint: llm.ResponseItem{
				Type:             llm.ResponseItemTypeCompaction,
				EncryptedContent: textutil.Value("checkpoint"),
			},
			Usage: llm.Usage{InputTokens: 1_000, OutputTokens: 100, WindowTokens: 200_000},
		}},
	}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:          "gpt-5",
		CompactionMode: "native",
	})
	restoreStep := setTestActiveStep(engine, "input")
	if err := engine.steer(runtimeTestStepID("input"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	restoreStep()
	return store, client, engine
}

type compactionCallbackClient struct {
	*fakeCompactionClient
	onCompact func()
}

func (c *compactionCallbackClient) Compact(ctx context.Context, req llm.CompactionRequest) (llm.CompactionResponse, error) {
	if callback := c.onCompact; callback != nil {
		c.onCompact = nil
		callback()
	}
	return c.fakeCompactionClient.Compact(ctx, req)
}

func assertRemoteCompactionFailureWithoutReplacementOrFallback(
	t *testing.T,
	store *session.Store,
	client *fakeCompactionClient,
	events []Event,
	wantRemoteCalls int,
) {
	t.Helper()
	if !hasEventKind(events, EventCompactionFailed) {
		t.Fatalf("compaction events = %+v, want failed event", events)
	}
	if len(client.compactionCalls) != wantRemoteCalls || len(client.calls) != 0 {
		t.Fatalf(
			"remote/local compaction calls = %d/%d, want %d/zero",
			len(client.compactionCalls),
			len(client.calls),
			wantRemoteCalls,
		)
	}
	recent, readErr := mustMaterializeTestEventLog(t, store).ReadRecentRecords(4)
	if readErr != nil {
		t.Fatalf("read bounded compaction records: %v", readErr)
	}
	for _, record := range recent.Records {
		if _, ok := mustSessionEventPayload(record).(session.HistoryReplacementRecord); ok {
			t.Fatalf("provider failure committed history replacement: %+v", record)
		}
	}
}
