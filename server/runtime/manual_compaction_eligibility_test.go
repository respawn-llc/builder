package runtime

import (
	"errors"
	"testing"

	"core/server/llm"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/textutil"
)

func completeManualEligibilityAgentStep(t *testing.T, engine *Engine) {
	t.Helper()
	engine.compactionRuntimeState().SetManualCompactionEligible(true)
}

func TestManualCompactionRejectsTooSoonBeforeScheduling(t *testing.T) {
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

	requestID := runtimeids.NewCompactionRequestID()
	if _, err := engine.CompactContextAdmissionForRequestWithAcceptance(
		t.Context(),
		requestID,
		runtimeinput.ManualCompactionAdmission{},
		nil,
	); !errors.Is(err, ErrManualCompactionTooSoon) {
		t.Fatalf("fresh-session compaction error = %v, want too soon", err)
	}
	pending, err := engine.PendingWorkSnapshot()
	if err != nil {
		t.Fatalf("PendingWorkSnapshot: %v", err)
	}
	if len(pending.Items) != 0 {
		t.Fatalf("fresh-session compaction changed Pending Work: %+v", pending.Items)
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

func TestManualCompactionAdmissionReturnsDuringAgentStep(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5", CompactionMode: "local"})
	release := pendingWorkTestHoldStep(t, engine, ActiveKindUserTurn)
	_, firstID := schedulePendingManualCompaction(t, engine)
	_, secondID := schedulePendingManualCompaction(t, engine)
	pending := pendingWorkTestSnapshot(t, engine)
	if !pendingWorkTestContains(pending, firstID) || !pendingWorkTestContains(pending, secondID) {
		t.Fatalf("repeated manual compactions were coalesced: %+v", pending.Items)
	}
	_, err := engine.RemovePendingWork(t.Context(), firstID)
	pendingWorkTestNoError(t, err)
	release()
	waitEngineLifecycleTasks(t, engine)
	if pendingWorkTestContains(pendingWorkTestSnapshot(t, engine), secondID) {
		t.Fatal("manual compaction remained in Pending Work after its boundary started")
	}
}

func TestManualCompactionRevalidatesMutableConditionsAtBoundary(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Engine)
	}{
		{"disabled policy", func(engine *Engine) {
			engine.contextPolicy.CompactionMode = serverapi.ChatContextCompactionModeDisabled
		}},
		{
			"active compaction",
			func(engine *Engine) {
				engine.compactionRuntimeState().SetActive(runtimeTestStepID("other-compaction"), nil, string(compactionModeManual), 1, ActiveKindCompaction)
			},
		},
		{"too soon", func(engine *Engine) { engine.compactionRuntimeState().SetManualCompactionEligible(false) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := pendingWorkTestEngine(t, Config{Model: "gpt-5", CompactionMode: "local"})
			engine.compactionRuntimeState().SetManualCompactionEligible(true)
			release := pendingWorkTestHoldMaintenance(t, engine)
			var terminal *CompactionStatus
			engine.cfg.OnEvent = func(event Event) {
				if event.Kind == EventCompactionFailed && event.Compaction != nil {
					terminal = event.Compaction
				}
			}

			requestID, itemID := schedulePendingManualCompaction(t, engine)
			test.mutate(engine)
			release()
			waitEngineLifecycleTasks(t, engine)
			if terminal == nil || terminal.RequestID == nil || *terminal.RequestID != requestID {
				t.Fatalf("terminal compaction status = %+v, want request %s", terminal, requestID)
			}
			if pendingWorkTestContains(pendingWorkTestSnapshot(t, engine), itemID) {
				t.Fatal("started manual compaction remained in Pending Work")
			}
		})
	}
}

func schedulePendingManualCompaction(t *testing.T, engine *Engine) (runtimeids.CompactionRequestID, runtimeids.QueueItemID) {
	t.Helper()
	requestID := runtimeids.NewCompactionRequestID()
	_, err := engine.CompactContextAdmissionForRequestWithAcceptance(
		t.Context(), requestID, runtimeinput.ManualCompactionAdmission{}, nil)
	pendingWorkTestNoError(t, err)
	itemID, err := serverapi.PendingWorkItemIDFromCompactionRequest(requestID)
	pendingWorkTestNoError(t, err)
	return requestID, itemID
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
