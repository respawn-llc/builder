package runtime

import (
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"
)

func TestToolCompletionDeletionMismatchPanicsBeforePersistenceInDebug(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		Debug: true,
	})
	result := mismatchedDeletionCompletion(t, engine)

	defer func() {
		recovered := recover()
		failure, ok := recovered.(toolCompletionPresentationPanic)
		if !ok {
			t.Fatalf("panic = %#v, want typed tool completion presentation panic", recovered)
		}
		if failure.CallID != result.CallID ||
			failure.ToolName != result.Name ||
			failure.Mismatch == nil ||
			failure.Mismatch.Kind != patchformat.WholeFileDeletionFactMismatchUnexpectedOperation {
			t.Fatalf("typed panic context = %+v", failure)
		}

		window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
		if err != nil {
			t.Fatalf("read bounded mismatch records: %v", err)
		}
		for _, record := range window.Records {
			switch mustSessionEventPayload(record).(type) {
			case session.ToolCompletionRecord, session.LocalEntryRecord:
				t.Fatalf("debug mismatch persisted a completion or fallback entry: %+v", record)
			}
		}
		if _, ok := engine.transcriptRuntimeState().liveToolLedger().Lookup(result.CallID); !ok {
			t.Fatal("debug mismatch removed the live tool before persistence")
		}
	}()

	_, _ = engine.steerWithCommitReceipt("step-delete", steerToolCompletionIntent(result))
}

func mismatchedDeletionCompletion(t *testing.T, engine *Engine) tools.Result {
	t.Helper()
	call := llm.ToolCall{
		ID:          "deletion-call",
		Name:        string(toolspec.ToolPatch),
		Custom:      true,
		CustomInput: textutil.Value("*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n"),
	}
	normalized := normalizeToolCallForTranscript(call, engine.transcriptWorkingDir())
	if err := engine.steer(
		"step-delete",
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventNone,
			true,
			[]llm.Message{{
				Role:      llm.RoleAssistant,
				ToolCalls: []llm.ToolCall{normalized},
			}},
		),
	); err != nil {
		t.Fatalf("persist deletion call: %v", err)
	}
	if err := engine.transcriptRuntimeState().RecordLiveToolStart("step-delete", normalized); err != nil {
		t.Fatalf("record live deletion call: %v", err)
	}
	received := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 9}
	group := patchformat.WholeFileDeletionGroupID{FirstOperation: received}
	return tools.Result{
		CallID: call.ID,
		Name:   toolspec.ToolPatch,
		Output: json.RawMessage(`{"ok":true}`),
		PresentationDelta: &transcript.ToolResultPresentationDelta{
			WholeFileDeletionFacts: []patchformat.WholeFileDeletionFact{{
				PhysicalGroup: group,
				OperationIDs:  []patchformat.WholeFileDeletionOperationID{received},
				Removed:       1,
			}},
		},
	}
}
