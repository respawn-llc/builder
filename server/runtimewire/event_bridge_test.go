package runtimewire

import (
	"encoding/json"
	"testing"

	"core/server/runtime"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestEventBridgeCoalescesGapSignalsUntilObserved(t *testing.T) {
	bridge := NewEventBridge(1, nil)
	bridge.Publish(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "first"})
	bridge.Publish(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "second"})
	bridge.Publish(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "third"})

	if got := bridge.Dropped.Load(); got != 2 {
		t.Fatalf("dropped count = %d, want 2", got)
	}

	select {
	case <-bridge.GapEvents:
	default:
		t.Fatal("expected dropped publishes to signal a gap")
	}

	select {
	case <-bridge.GapEvents:
		t.Fatal("expected pending gap signal to stay coalesced until observed")
	default:
	}

	bridge.Publish(runtime.Event{Kind: runtime.EventToolCallStarted, StepID: textutil.Value("step-1")})
	if got := bridge.Dropped.Load(); got != 3 {
		t.Fatalf("dropped count after another overflow = %d, want 3", got)
	}

	select {
	case <-bridge.GapEvents:
	default:
		t.Fatal("expected another dropped publish to signal a new gap after observation")
	}
}

func TestEventBridgeDropSignalsGapAndHydrationRestoresAtomicResultGroup(t *testing.T) {
	root := t.TempDir()
	store := newRuntimeWireSession(t, root, "result-group-gap")
	eventLog := materializedRuntimeWireEventLog(t, store)
	stepID := "step-1"
	callIDs := []string{"first", "second"}
	toolCalls := make([]session.MessageToolCallRecord, len(callIDs))
	for index, callID := range callIDs {
		toolCalls[index] = session.MessageToolCallRecord{
			CallID: callID,
			Name:   string(toolspec.ToolExecCommand),
			Kind:   session.ToolCallKindFunction,
			Input:  json.RawMessage(`{"cmd":"true"}`),
		}
	}
	if _, receipt, err := eventLog.AppendRecord(&stepID, session.MessageRecord{
		Role:      session.MessageRoleAssistant,
		ToolCalls: toolCalls,
	}); err != nil || !receipt.Committed {
		t.Fatalf("append assistant tool intent receipt=%+v error=%v", receipt, err)
	}
	payloads := make([]session.EventRecordPayload, 0, len(callIDs)*2)
	for _, callID := range callIDs {
		name := string(toolspec.ToolExecCommand)
		content := `{"ok":true}`
		payloads = append(
			payloads,
			session.ToolCompletionRecord{
				CallID:     callID,
				Name:       name,
				OutputKind: session.ToolOutputKindFunction,
				Output:     json.RawMessage(content),
			},
			session.MessageRecord{
				Role:       session.MessageRoleTool,
				Name:       &name,
				ToolCallID: &callID,
				Content:    &content,
			},
		)
	}
	if records, receipt, err := eventLog.AppendRecordsAtomic(&stepID, payloads); err != nil ||
		!receipt.Committed ||
		len(records) != len(payloads) {
		t.Fatalf(
			"append result group records=%d receipt=%+v error=%v",
			len(records),
			receipt,
			err,
		)
	}

	bridge := NewEventBridge(1, nil)
	for _, callID := range callIDs {
		result := tools.Result{
			CallID: callID,
			Name:   toolspec.ToolExecCommand,
			Output: json.RawMessage(`{"ok":true}`),
		}
		bridge.Publish(runtime.Event{
			Kind:                       runtime.EventToolCallCompleted,
			StepID:                     textutil.Value(stepID),
			ToolResult:                 &result,
			CommittedTranscriptChanged: true,
		})
	}
	if got := bridge.Dropped.Load(); got != 1 {
		t.Fatalf("dropped committed result-group events = %d, want 1", got)
	}
	select {
	case <-bridge.GapEvents:
	default:
		t.Fatal("dropped committed result-group event did not signal a gap")
	}

	assertHydrated := func(store *session.Store) {
		t.Helper()
		engine, err := runtime.New(
			store,
			materializedRuntimeWireEventLog(t, store),
			&runtimewireCaptureClient{},
			tools.NewRegistry(),
			runtime.Config{Model: "gpt-5"},
		)
		if err != nil {
			t.Fatalf("new hydrated runtime: %v", err)
		}
		t.Cleanup(func() { _ = engine.Close() })
		var snapshot runtime.TranscriptHydrationSnapshot
		if err := engine.WithTranscriptHydrationSnapshot(func(value runtime.TranscriptHydrationSnapshot) error {
			snapshot = value
			return nil
		}); err != nil {
			t.Fatalf("hydrate result group: %v", err)
		}
		var hydratedCallIDs []string
		for _, row := range snapshot.CommittedRows {
			if row.Kind == runtime.TranscriptCommittedRowFactTool && row.Tool != nil {
				hydratedCallIDs = append(hydratedCallIDs, row.Tool.ToolCallID)
			}
		}
		if len(hydratedCallIDs) != len(callIDs) {
			t.Fatalf("hydrated result-group calls = %v, want %v", hydratedCallIDs, callIDs)
		}
		for index := range callIDs {
			if hydratedCallIDs[index] != callIDs[index] {
				t.Fatalf("hydrated result-group calls = %v, want %v", hydratedCallIDs, callIDs)
			}
		}
	}

	assertHydrated(store)
	reopened, err := runtimeWireTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen result-group session: %v", err)
	}
	assertHydrated(reopened)
}
