package runtime

import (
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestToolCompletionLocatorOwnerSurvivesRoleToolMaterializationAndReopen(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	events := make([]Event, 0, 16)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(event Event) { events = append(events, event) },
	})
	restoreStep := setTestActiveStep(engine, "step-1")
	defer restoreStep()
	calls := []llm.ToolCall{
		{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
		{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
	}
	if err := engine.steer(runtimeTestStepID("step-1"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: calls}})); err != nil {
		t.Fatalf("append assistant tool calls: %v", err)
	}
	results := []tools.Result{
		{CallID: "call-2", Name: toolspec.ToolExecCommand, Output: json.RawMessage(`{"output":"two"}`)},
		{CallID: "call-1", Name: toolspec.ToolExecCommand, Output: json.RawMessage(`{"output":"one"}`)},
	}
	liveLocators := make(map[string]transcript.CommittedRowLocator, len(results))
	for _, result := range results {
		before := len(events)
		if err := engine.steer(runtimeTestStepID("step-1"), steerToolCompletionIntent(result)); err != nil {
			t.Fatalf("persist tool completion %s: %v", result.CallID, err)
		}
		for _, event := range events[before:] {
			facts := TranscriptCommittedRowFactsFromEvent(event)
			for _, fact := range facts {
				if fact.Tool != nil && fact.Tool.ToolCallID == result.CallID {
					liveLocators[result.CallID] = fact.Locator
				}
			}
		}
	}
	for _, result := range results {
		mirror := llm.Message{
			Role:       llm.RoleTool,
			ToolCallID: textutil.Value(result.CallID),
			Name:       textutil.Value(string(result.Name)),
			Content:    textutil.Value(string(result.Output)),
		}
		if err := engine.steer(runtimeTestStepID("step-1"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{mirror})); err != nil {
			t.Fatalf("materialize tool mirror %s: %v", result.CallID, err)
		}
	}
	liveToolRows := make(map[string]int, len(results))
	for _, event := range events {
		for _, fact := range TranscriptCommittedRowFactsFromEvent(event) {
			if fact.Tool != nil {
				liveToolRows[fact.Tool.ToolCallID]++
			}
		}
	}
	for _, result := range results {
		if got, want := liveToolRows[result.CallID], 1; got != want {
			t.Fatalf("live tool row count for %s = %d, want %d", result.CallID, got, want)
		}
	}

	page := mustEngineNewestSegmentPage(t, engine)
	pageFacts := TranscriptCommittedRowFactsFromSnapshot(page.Snapshot)
	assertToolFactsFollowLocatorOrder(t, pageFacts, "page")
	hydrationFacts := hydrationSnapshot(t, engine).CommittedRows
	assertToolFactsFollowLocatorOrder(t, hydrationFacts, "hydration")
	for _, callID := range []string{"call-1", "call-2"} {
		pageFact := findToolFact(t, pageFacts, callID)
		liveLocator, ok := liveLocators[callID]
		if !ok {
			t.Fatalf("live locator for %s is absent", callID)
		}
		if err := liveLocator.Validate(); err != nil {
			t.Fatalf("live locator for %s is invalid: %v", callID, err)
		}
		if pageFact.Locator != liveLocator {
			t.Fatalf("tool %s page locator = %+v, live locator = %+v", callID, pageFact.Locator, liveLocator)
		}
	}

	if err := engine.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}
	reopened := mustNewTestEngine(t, mustOpenTestSession(t, store.Dir()), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	reopenedFacts := TranscriptCommittedRowFactsFromSnapshot(mustEngineNewestSegmentPage(t, reopened).Snapshot)
	assertToolFactsFollowLocatorOrder(t, reopenedFacts, "reopened page")
	assertToolFactsFollowLocatorOrder(t, hydrationSnapshot(t, reopened).CommittedRows, "reopened hydration")
	for _, callID := range []string{"call-1", "call-2"} {
		if got := findToolFact(t, reopenedFacts, callID).Locator; got != liveLocators[callID] {
			t.Fatalf("reopened tool %s locator = %+v, live locator = %+v", callID, got, liveLocators[callID])
		}
	}
}

func assertToolFactsFollowLocatorOrder(t *testing.T, facts []TranscriptCommittedRowFact, source string) {
	t.Helper()
	var previous *TranscriptCommittedRowFact
	for index := range facts {
		if facts[index].Tool == nil {
			continue
		}
		if previous != nil && facts[index].Locator.EventSequence < previous.Locator.EventSequence {
			t.Fatalf("%s tool facts regress by locator: previous=%+v current=%+v", source, previous.Locator, facts[index].Locator)
		}
		previous = &facts[index]
	}
}

func findToolFact(t *testing.T, facts []TranscriptCommittedRowFact, callID string) TranscriptCommittedRowFact {
	t.Helper()
	for _, fact := range facts {
		if fact.Tool != nil && fact.Tool.ToolCallID == callID {
			return fact
		}
	}
	t.Fatalf("tool fact %s is absent: %+v", callID, facts)
	return TranscriptCommittedRowFact{}
}
