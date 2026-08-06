package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

type recoveryReplayProbe struct {
	calls atomic.Int32
}

func (p *recoveryReplayProbe) Call(_ context.Context, call tools.Call) (tools.Result, error) {
	p.calls.Add(1)
	return tools.Result{
		CallID: call.ID,
		Name:   call.Name,
		Output: json.RawMessage(`{"unexpected":true}`),
	}, nil
}

func TestFreshResourceRepairIsCauseIndependentAndDoesNotReplayTools(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		tool       toolspec.ID
		withMarker bool
		withPrefix bool
	}{
		{name: "process death", tool: toolspec.ToolAskQuestion},
		{name: "pre-commit loss", tool: toolspec.ToolCompleteNode, withMarker: true},
		{name: "committed observer barrier abort", tool: toolspec.ToolAskQuestion, withMarker: true, withPrefix: true},
		{name: "committed projection barrier abort", tool: toolspec.ToolCompleteNode, withMarker: true, withPrefix: true},
	}

	var (
		equivalentOutput  json.RawMessage
		equivalentWarning *string
	)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const stepID = "recovery-step"
			callID := "dangling-" + string(test.tool)
			store := mustCreateTestSession(t)
			calls := []llm.ToolCall{{
				ID:    callID,
				Name:  string(test.tool),
				Input: json.RawMessage(`{}`),
			}}
			if test.withPrefix {
				calls = append([]llm.ToolCall{{
					ID:    "committed-prefix",
					Name:  string(toolspec.ToolWebSearch),
					Input: json.RawMessage(`{}`),
				}}, calls...)
			}
			mustAppendTestEvent(t, store, stepID, llm.Message{
				Role:      llm.RoleAssistant,
				ToolCalls: calls,
			})
			if test.withPrefix {
				record, err := sessionToolCompletionRecordFromRuntime(
					tools.Result{
						CallID: "committed-prefix",
						Name:   toolspec.ToolWebSearch,
						Output: json.RawMessage(`{"ok":true}`),
					},
					[]llm.ResponseItem{{
						Type:   llm.ResponseItemTypeFunctionCallOutput,
						Name:   textutil.Value(string(toolspec.ToolWebSearch)),
						CallID: textutil.Value("committed-prefix"),
						Output: json.RawMessage(`{"ok":true}`),
					}},
				)
				if err != nil {
					t.Fatalf("prepare committed prefix: %v", err)
				}
				if _, _, err := mustMaterializeTestEventLog(t, store).AppendRecord(
					textutil.Value(stepID),
					record,
				); err != nil {
					t.Fatalf("append committed prefix: %v", err)
				}
			}
			if test.withMarker {
				if err := store.SetPendingModelRecovery(session.PendingModelRecovery{
					RecoveryID:             "recovery-" + callID,
					StepID:                 stepID,
					Reason:                 "provider_visible_output_persisted",
					CreatedAt:              time.Unix(0, 0).UTC(),
					OutstandingToolCallIDs: []string{callID},
				}); err != nil {
					t.Fatalf("set pending recovery: %v", err)
				}
			}

			probe := &recoveryReplayProbe{}
			reopened := mustOpenTestSession(t, store.Dir())
			engine := mustNewTestEngine(
				t,
				reopened,
				&fakeClient{},
				tools.NewRegistry(tools.HandlerRegistration{ID: test.tool, Handler: probe}),
				Config{Model: "gpt-5"},
			)
			if calls := probe.calls.Load(); calls != 0 {
				t.Fatalf("fresh recovery replayed %s %d time(s)", test.tool, calls)
			}
			assertFreshResourceRepairOnEngine(t, engine, reopened, callID)
			_, completion := repairCompletionRecord(t, reopened, callID)
			if equivalentOutput == nil {
				equivalentOutput = append(json.RawMessage(nil), completion.Output...)
			}
			if !bytes.Equal(completion.Output, equivalentOutput) {
				t.Fatalf(
					"fresh repair output for %s differs by recovery cause: got %s want %s",
					test.name,
					completion.Output,
					equivalentOutput,
				)
			}
			warning := freshResourceRepairWarning(t, reopened)
			if equivalentWarning == nil {
				equivalentWarning = textutil.Value(warning)
			}
			if warning != *equivalentWarning {
				t.Fatalf(
					"fresh repair warning for %s differs by recovery cause",
					test.name,
				)
			}
		})
	}
}

func TestFreshResourceRepairIgnoresStalePendingStartWhileLiveRepairDefers(t *testing.T) {
	t.Parallel()
	newDanglingEngine := func(t *testing.T, callID string) (*Engine, *session.Store) {
		t.Helper()
		store := mustCreateTestSession(t)
		engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
		steerDanglingToolCall(t, engine, "step", llm.ToolCall{
			ID: callID, Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`),
		})
		engine.rememberPendingToolCallStarts(map[string]int{callID: 0})
		return engine, store
	}

	fresh, freshStore := newDanglingEngine(t, "fresh")
	repaired, err := fresh.repairMissingToolOutputsByAppending(
		textutil.Value("step"),
		missingToolOutputRepairFreshResource,
	)
	if err != nil {
		t.Fatalf("fresh repair: %v", err)
	}
	if repaired != 1 {
		t.Fatalf("fresh repair count = %d, want one despite stale pending start", repaired)
	}
	_, freshCompletion := repairCompletionRecord(t, freshStore, "fresh")
	if !bytes.Equal(freshCompletion.Output, missingToolOutputUnavailableOutput) {
		t.Fatalf("fresh repair selected non-neutral disposition: %s", freshCompletion.Output)
	}

	live, liveStore := newDanglingEngine(t, "live")
	repaired, err = live.repairMissingToolOutputsByAppending(
		textutil.Value("step"),
		missingToolOutputRepairLiveProvider400,
	)
	if err != nil {
		t.Fatalf("live repair: %v", err)
	}
	if repaired != 0 {
		t.Fatalf("live repair count = %d, want defer while a live start may produce output", repaired)
	}
	if repairRequestHasToolOutput(live.transcriptRuntimeState().SnapshotItems(), "live") {
		t.Fatal("live repair pre-empted a pending tool operation")
	}
	if _, err := live.repairMissingToolOutputsByAppending(
		textutil.Value("step"),
		missingToolOutputRepairDisposition(0),
	); err == nil {
		t.Fatal("repair accepted an absent disposition")
	}
	if _, _, found := completionRecordCount(t, liveStore, "live"); found {
		t.Fatal("invalid disposition appended a completion")
	}
}

func assertFreshResourceRepairOnEngine(
	t *testing.T,
	engine *Engine,
	store *session.Store,
	callID string,
) {
	t.Helper()
	completion, found := engine.transcriptRuntimeState().ToolCompletionSnapshot(callID)
	if !found || !completion.IsError {
		t.Fatalf("fresh repair completion for %q = %+v found=%t", callID, completion, found)
	}
	if !bytes.Equal(completion.Output, missingToolOutputUnavailableOutput) {
		t.Fatalf("fresh repair output for %q = %s, want neutral disposition", callID, completion.Output)
	}
	for _, live := range engine.transcriptRuntimeState().LiveToolSnapshot() {
		if live.ToolCallID == callID {
			t.Fatalf("fresh repair retained stale live tool start: %+v", live)
		}
	}
	if store.Meta().PendingModelRecovery != nil {
		t.Fatalf("fresh repair retained pending recovery: %+v", store.Meta().PendingModelRecovery)
	}
}

func assertFreshResourceRepairExactlyOnce(t *testing.T, store *session.Store, callID string) {
	assertFreshResourceRepairExactlyOnceWith(t, store, callID, nil)
}

func assertFreshResourceRepairExactlyOnceWithHydratedPrefix(
	t *testing.T,
	store *session.Store,
	callID string,
	prefixCallID string,
) {
	t.Helper()
	assertFreshResourceRepairExactlyOnceWith(
		t,
		store,
		callID,
		func(restored *Engine, _ *session.Store) {
			assertHydratedToolRowsExactlyOnce(t, restored, prefixCallID)
		},
	)
}

func assertHydratedToolRowsExactlyOnce(
	t *testing.T,
	engine *Engine,
	callID string,
) {
	t.Helper()
	if rows := countHydratedToolRows(
		mustTranscriptHydrationSnapshot(t, engine),
		callID,
	); rows != 1 {
		t.Fatalf("hydrated tool rows for %q = %d, want one", callID, rows)
	}
}

func assertFreshResourceRepairExactlyOnceWith(
	t *testing.T,
	store *session.Store,
	callID string,
	verify func(*Engine, *session.Store),
) {
	t.Helper()
	firstStore := mustOpenTestSession(t, store.Dir())
	first := mustNewTestEngine(t, firstStore, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	assertFreshResourceRepairOnEngine(t, first, firstStore, callID)
	if verify != nil {
		verify(first, firstStore)
	}
	completions, warnings, found := completionRecordCount(t, firstStore, callID)
	if !found || completions != 1 || warnings != 1 {
		t.Fatalf(
			"first fresh open records for %q = completions:%d warnings:%d found:%t, want 1/1/true",
			callID,
			completions,
			warnings,
			found,
		)
	}

	secondStore := mustOpenTestSession(t, store.Dir())
	second := mustNewTestEngine(t, secondStore, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	assertFreshResourceRepairOnEngine(t, second, secondStore, callID)
	if verify != nil {
		verify(second, secondStore)
	}
	completions, warnings, found = completionRecordCount(t, secondStore, callID)
	if !found || completions != 1 || warnings != 1 {
		t.Fatalf(
			"second fresh open records for %q = completions:%d warnings:%d found:%t, want unchanged 1/1/true",
			callID,
			completions,
			warnings,
			found,
		)
	}
}

func completionRecordCount(t *testing.T, store *session.Store, callID string) (int, int, bool) {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(32)
	if err != nil {
		t.Fatalf("read bounded recovery records: %v", err)
	}
	completions := 0
	warnings := 0
	found := false
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.ToolCompletionRecord:
			if payload.CallID == callID {
				completions++
				found = true
			}
		case session.LocalEntryRecord:
			if payload.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
				warnings++
			}
		}
	}
	return completions, warnings, found
}

func freshResourceRepairWarning(t *testing.T, store *session.Store) string {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(32)
	if err != nil {
		t.Fatalf("read bounded fresh-resource warning records: %v", err)
	}
	var warning *string
	for _, record := range window.Records {
		payload, ok := mustSessionEventPayload(record).(session.LocalEntryRecord)
		if !ok ||
			payload.Role != string(transcript.EntryRoleDeveloperErrorFeedback) {
			continue
		}
		if warning != nil {
			t.Fatal("fresh-resource recovery persisted more than one typed warning")
		}
		warning = textutil.Value(payload.Text)
	}
	if warning == nil {
		t.Fatal("fresh-resource recovery persisted no typed warning")
	}
	return *warning
}
