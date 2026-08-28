package runtime

import (
	"testing"

	"core/server/llm"
	"core/shared/textutil"
)

func completeManualEligibilityAgentStep(t *testing.T, engine *Engine) {
	t.Helper()
	engine.compactionRuntimeState().SetManualCompactionEligible(true)
}

func TestManualCompactionRequiresToolCallSinceLatestCompaction(t *testing.T) {
	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("summary"),
			},
		}},
	}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model:          "gpt-5",
		CompactionMode: "local",
	})
	engine.compactionRuntimeState().SetManualCompactionEligible(false)

	var events []Event
	engine.cfg.OnEvent = func(event Event) {
		events = append(events, event)
	}
	scheduleManualCompactionAndWait(t, engine)
	if !hasEventKind(events, EventCompactionFailed) {
		t.Fatalf("fresh-session compaction events = %+v, want failed event", events)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.compactionCalls) != 0 || len(client.calls) != 0 {
		t.Fatalf("provider calls after rejected compaction = compaction:%d completion:%d, want zero", len(client.compactionCalls), len(client.calls))
	}
}

func TestManualCompactionAcceptsAfterAgentStepBoundary(t *testing.T) {
	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("summary"),
			},
		}},
	}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model:          "gpt-5",
		CompactionMode: "local",
	})
	completeManualEligibilityAgentStep(t, engine)

	scheduleManualCompactionAndWait(t, engine)
	client.mu.Lock()
	if len(client.calls) != 1 {
		client.mu.Unlock()
		t.Fatalf("provider completion calls = %d, want one", len(client.calls))
	}
	client.mu.Unlock()

	if engine.compactionRuntimeState().ManualCompactionEligible() {
		t.Fatal("successful compaction retained manual eligibility")
	}
}

func TestCompactionCarryoverSelectsNewestOrdinaryUserPrompt(t *testing.T) {
	tests := []struct {
		name  string
		items []llm.ResponseItem
		want  *string
	}{
		{
			name: "typed user prompt is skipped",
			items: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), Content: textutil.Value("ordinary prompt")},
				{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), MessageType: textutil.Value(llm.MessageTypeCompactionPreservedUserMessage), Content: textutil.Value("typed context")},
			},
			want: textutil.Value("ordinary prompt"),
		},
		{
			name: "typed prompts only",
			items: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("typed summary")},
			},
		},
		{
			name: "blank ordinary prompt is skipped",
			items: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), Content: textutil.Value("  \n")},
				{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), Content: textutil.Value("ordinary prompt")},
			},
			want: textutil.Value("ordinary prompt"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := lastVisibleUserMessageSinceLatestCompaction(test.items)
			if (got != nil) != (test.want != nil) {
				t.Fatalf("carryover prompt presence = %t, want %t", got != nil, test.want != nil)
			}
			if got != nil && *got != *test.want {
				t.Fatalf("carryover prompt = %q, want %q", *got, *test.want)
			}
		})
	}
}
