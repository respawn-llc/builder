package runtime

import (
	"reflect"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestHistoryReplacementLiveProjectionMatchesPersistedActiveSegment(t *testing.T) {
	var events []Event
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)
	items := llm.ItemsFromMessages([]llm.Message{
		{Role: llm.RoleUser, Content: textutil.Value("input")},
		{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("response"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			ToolCalls: []llm.ToolCall{{
				ID:   "call-1",
				Name: string(toolspec.ToolExecCommand),
			}},
		},
		{
			Role:       llm.RoleTool,
			ToolCallID: textutil.Value("call-1"),
			Name:       textutil.Value(string(toolspec.ToolExecCommand)),
			Content:    textutil.Value(`{"output":"done"}`),
		},
	})

	if err := engine.steer(
		"compaction",
		steerHistoryReplacementIntent("local", compactionModeAuto, "", 1, "", "", items),
	); err != nil {
		t.Fatalf("persist history replacement: %v", err)
	}

	live := make([]TranscriptCommittedRowFact, 0)
	projectedRows := 0
	for index := range events {
		event := events[index]
		if event.Kind != EventLocalEntryAdded || !event.LocalEntryProjected {
			continue
		}
		if !event.CommittedEntryStartSet ||
			event.CommittedEntryStart != projectedRows ||
			event.CommittedEntryCount != projectedRows+1 {
			t.Fatalf("projected row coordinates = %+v at index %d", event, projectedRows)
		}
		projectedRows++
		live = append(live, TranscriptCommittedRowFactsFromEvent(event)...)
	}
	if projectedRows == 0 || len(live) == 0 {
		t.Fatalf("history replacement emitted no projected transcript facts: %+v", events)
	}

	page := mustEngineNewestSegmentPage(t, engine)
	hydrated := TranscriptCommittedRowFactsFromSnapshot(page.Snapshot)
	if !reflect.DeepEqual(hydrated, live) {
		t.Fatalf("persisted active segment facts = %+v, live facts = %+v", hydrated, live)
	}
}
