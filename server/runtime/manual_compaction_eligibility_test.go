package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

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

func TestManualCompactionAdmissionReturnsDuringAgentStep(t *testing.T) {
	engine := pendingWorkTestEngine(t, Config{Model: "gpt-5", CompactionMode: "local"})
	stepStarted := make(chan struct{})
	releaseStep := make(chan struct{})
	stepDone := make(chan error, 1)
	go func() {
		stepDone <- engine.stepLifecycle.Run(
			context.Background(),
			exclusiveStepOptions{ActiveKind: ActiveKindUserTurn},
			func(context.Context, string) error {
				close(stepStarted)
				<-releaseStep
				return nil
			},
		)
	}()
	pendingWorkTestWait(t, stepStarted, "Agent Step")

	requestID := runtimeids.NewCompactionRequestID()
	accepted := make(chan error, 1)
	go func() {
		_, err := engine.CompactContextAdmissionForRequestWithAcceptance(
			context.Background(),
			requestID,
			runtimeinput.ManualCompactionAdmission{},
			nil,
		)
		accepted <- err
	}()
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("manual compaction admission: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		close(releaseStep)
		<-stepDone
		t.Fatal("manual compaction admission waited for the Agent Step boundary")
	}

	itemID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
		return runtimeids.ParseQueueItemID(requestID.String())
	})
	if !pendingWorkTestContains(pendingWorkTestSnapshot(t, engine), itemID) {
		t.Fatal("accepted manual compaction is absent from Pending Work")
	}

	secondRequestID := runtimeids.NewCompactionRequestID()
	if _, err := engine.CompactContextAdmissionForRequestWithAcceptance(
		context.Background(),
		secondRequestID,
		runtimeinput.ManualCompactionAdmission{},
		nil,
	); err != nil {
		t.Fatalf("second manual compaction admission: %v", err)
	}
	secondItemID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
		return runtimeids.ParseQueueItemID(secondRequestID.String())
	})
	pending := pendingWorkTestSnapshot(t, engine)
	if !pendingWorkTestContains(pending, itemID) || !pendingWorkTestContains(pending, secondItemID) {
		t.Fatalf("repeated manual compactions were coalesced: %+v", pending.Items)
	}
	if _, err := engine.RemovePendingWork(context.Background(), itemID); err != nil {
		t.Fatalf("remove pending manual compaction: %v", err)
	}
	close(releaseStep)
	if err := <-stepDone; err != nil {
		t.Fatalf("Agent Step: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if pendingWorkTestContains(pendingWorkTestSnapshot(t, engine), secondItemID) {
		t.Fatal("manual compaction remained in Pending Work after its boundary started")
	}
}

func TestManualCompactionRevalidatesMutableConditionsAtBoundary(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Engine)
	}{
		{
			name: "disabled policy",
			mutate: func(engine *Engine) {
				engine.contextPolicy.CompactionMode = serverapi.ChatContextCompactionModeDisabled
			},
		},
		{
			name: "active compaction",
			mutate: func(engine *Engine) {
				engine.compactionRuntimeState().SetActive(
					runtimeTestStepID("other-compaction"),
					nil,
					string(compactionModeManual),
					1,
				)
			},
		},
		{
			name: "too soon",
			mutate: func(engine *Engine) {
				engine.compactionRuntimeState().SetManualCompactionEligible(false)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := pendingWorkTestEngine(t, Config{Model: "gpt-5", CompactionMode: "local"})
			engine.compactionRuntimeState().SetManualCompactionEligible(true)
			releaseMaintenance := pendingWorkTestHoldMaintenance(t, engine)
			requestID := runtimeids.NewCompactionRequestID()
			var eventsMu sync.Mutex
			var terminal *CompactionStatus
			engine.cfg.OnEvent = func(event Event) {
				if event.Kind != EventCompactionFailed || event.Compaction == nil {
					return
				}
				eventsMu.Lock()
				copyStatus := *event.Compaction
				terminal = &copyStatus
				eventsMu.Unlock()
			}

			if _, err := engine.CompactContextAdmissionForRequestWithAcceptance(
				context.Background(),
				requestID,
				runtimeinput.ManualCompactionAdmission{},
				nil,
			); err != nil {
				t.Fatalf("manual compaction admission: %v", err)
			}
			test.mutate(engine)
			releaseMaintenance()
			waitEngineLifecycleTasks(t, engine)

			eventsMu.Lock()
			gotTerminal := terminal
			eventsMu.Unlock()
			if gotTerminal == nil || gotTerminal.RequestID == nil || *gotTerminal.RequestID != requestID {
				t.Fatalf("terminal compaction status = %+v, want request %s", gotTerminal, requestID)
			}
			itemID := pendingWorkTestMust(t, func() (runtimeids.QueueItemID, error) {
				return runtimeids.ParseQueueItemID(requestID.String())
			})
			if pendingWorkTestContains(pendingWorkTestSnapshot(t, engine), itemID) {
				t.Fatal("started manual compaction remained in Pending Work")
			}
		})
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
