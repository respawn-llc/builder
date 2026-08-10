package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
)

func newTranscriptHydrationSnapshotTestEngine(t *testing.T, client llm.Client) *Engine {
	t.Helper()
	return mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{Model: "gpt-5"})
}

func hydrationSnapshot(t *testing.T, engine *Engine) TranscriptHydrationSnapshot {
	t.Helper()
	var snapshot TranscriptHydrationSnapshot
	if err := engine.WithTranscriptHydrationSnapshot(func(value TranscriptHydrationSnapshot) error {
		snapshot = value
		return nil
	}); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	return snapshot
}

func TestTranscriptHydrationSnapshotProjectsAndResetsOwnerLiveFacts(t *testing.T) {
	engine := newTranscriptHydrationSnapshotTestEngine(t, &fakeClient{})
	const stepID = "step-current"
	outputIndex, partIndex := int64(0), int64(0)
	if err := engine.steer(stepID, steerReasoningDeltaIntent(llm.ReasoningSummaryDelta{
		SourceCoordinate: &llm.ReasoningSourceCoordinate{OutputIndex: &outputIndex, PartIndex: &partIndex},
		Text:             "inspect the repository", CurrentStatus: &llm.ReasoningStatus{Text: "Planning"},
	})); err != nil {
		t.Fatalf("reasoning: %v", err)
	}
	for _, call := range []llm.ToolCall{{ID: "call-1", Name: "shell"}, {ID: "call-2", Name: "patch"}} {
		if err := engine.transcriptRuntimeState().RecordLiveToolStart(stepID, call); err != nil {
			t.Fatalf("tool %s: %v", call.ID, err)
		}
	}
	first := mustQueueUserMessage(t, engine, "first")
	second := mustQueueUserMessage(t, engine, "second")
	snapshot := hydrationSnapshot(t, engine)
	if snapshot.ActiveThinkingStatus == nil || snapshot.ActiveThinkingStatus.StepID != stepID ||
		snapshot.ActiveThinkingStatus.Text != "Planning" ||
		len(snapshot.ActiveReasoningTraces) != 1 ||
		snapshot.ActiveReasoningTraces[0].Text != "inspect the repository" {
		t.Fatalf("reasoning = status %+v traces %+v", snapshot.ActiveThinkingStatus, snapshot.ActiveReasoningTraces)
	}
	if len(snapshot.InFlightTools) != 2 || snapshot.InFlightTools[0].ToolCallID != "call-1" ||
		snapshot.InFlightTools[1].ToolCallID != "call-2" ||
		len(snapshot.QueuedMessages) != 2 || snapshot.QueuedMessages[0].ID != first.ID ||
		snapshot.QueuedMessages[1].ID != second.ID {
		t.Fatalf("owner facts = tools %+v queue %+v", snapshot.InFlightTools, snapshot.QueuedMessages)
	}
	if err := engine.steer(stepID, steerClearStreamingStateIntent(), steerResetReasoningStateIntent()); err != nil {
		t.Fatalf("reset reasoning: %v", err)
	}
	afterReset := hydrationSnapshot(t, engine)
	if afterReset.ActiveThinkingStatus == nil || afterReset.ActiveThinkingStatus.Text != "Planning" {
		t.Fatalf("thinking status after reset = %+v", afterReset.ActiveThinkingStatus)
	}
	if len(afterReset.ActiveReasoningTraces) != 0 {
		t.Fatalf("reasoning traces after reset = %+v", afterReset.ActiveReasoningTraces)
	}
}

func TestTranscriptHydrationSnapshotProjectsAndResetsAllRuntimeOwners(t *testing.T) {
	engine := newTranscriptHydrationSnapshotTestEngine(t, &fakeClient{})
	const stepID = "step-owner"
	engine.compactionRuntimeState().SetCount(7)
	if err := engine.steer(stepID,
		steerEventIntent(Event{Kind: EventReviewerStarted, StepID: stepID}),
		steerEventIntent(Event{Kind: EventCompactionStarted, StepID: stepID, Compaction: &CompactionStatus{Mode: "remote", Count: 8}}),
	); err != nil {
		t.Fatalf("steer active owner events: %v", err)
	}
	engine.setLastUsage(llm.Usage{InputTokens: 123, WindowTokens: 1000})
	if _, err := engine.SetGoal("ship the owner snapshot", session.GoalActorUser); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	engine.goalLoopState().Start()
	engine.goalLoopState().Suspend()

	snapshot := hydrationSnapshot(t, engine)
	if snapshot.ActiveReviewer == nil || snapshot.ActiveReviewer.StepID != stepID {
		t.Fatalf("active reviewer = %+v", snapshot.ActiveReviewer)
	}
	if snapshot.ActiveCompaction == nil || snapshot.ActiveCompaction.StepID != stepID ||
		snapshot.ActiveCompaction.Count != 8 || snapshot.CompactionCount != 7 {
		t.Fatalf("compaction = active %+v count %d", snapshot.ActiveCompaction, snapshot.CompactionCount)
	}
	if snapshot.ContextUsage == nil || snapshot.ContextUsage.WindowTokens != 1000 || snapshot.ContextUsage.UsedTokens <= 0 {
		t.Fatalf("context usage = %+v", snapshot.ContextUsage)
	}
	if snapshot.Goal == nil || !snapshot.GoalSuspended {
		t.Fatalf("goal = %+v suspended=%t", snapshot.Goal, snapshot.GoalSuspended)
	}

	if err := engine.steer(stepID,
		steerEventIntent(Event{Kind: EventReviewerCompleted, StepID: stepID}),
		steerEventIntent(Event{Kind: EventCompactionCompleted, StepID: stepID}),
	); err != nil {
		t.Fatalf("steer terminal owner events: %v", err)
	}
	snapshot = hydrationSnapshot(t, engine)
	if snapshot.ActiveReviewer != nil || snapshot.ActiveCompaction != nil || snapshot.CompactionCount != 7 {
		t.Fatalf("terminal owner state = reviewer %+v compaction %+v count %d",
			snapshot.ActiveReviewer, snapshot.ActiveCompaction, snapshot.CompactionCount)
	}
}

func TestFailedQueueFlushRetainsSingleAcceptedStatusAcrossHydrationRace(t *testing.T) {
	store := mustCreateTestSession(t)
	statuses := make(chan QueuedUserMessageStatusEvent, 4)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			if event.QueuedUserMessageStatus != nil {
				statuses <- *event.QueuedUserMessageStatus
			}
		},
	})
	if err := engine.ensureMetaContextForRequest(context.Background(), "queue-flush"); err != nil {
		t.Fatalf("prepare queue flush: %v", err)
	}
	queued := mustQueueUserMessage(t, engine, "queued input")
	blocker := mustBlockTestEventLogAppends(t, store)
	flushDone := make(chan error, 1)
	go func() {
		_, _, err := engine.submitQueuedUserMessagesWithActiveHook(context.Background(), nil)
		flushDone <- err
	}()
	var duringFlush TranscriptHydrationSnapshot
	hydrationDone := make(chan struct{})
	go func() {
		err := engine.WithTranscriptHydrationSnapshot(func(snapshot TranscriptHydrationSnapshot) error {
			duringFlush = snapshot
			close(hydrationDone)
			return nil
		})
		if err != nil {
			t.Errorf("hydrate during queue flush: %v", err)
		}
	}()
	flushErr := <-flushDone
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event-log append: %v", err)
	}
	<-hydrationDone
	if flushErr == nil {
		t.Fatal("failed queue flush returned nil error")
	}
	restored := hydrationSnapshot(t, engine)
	if len(restored.QueuedMessages) != 1 || restored.QueuedMessages[0].ID != queued.ID {
		t.Fatalf("restored queue = %+v", restored.QueuedMessages)
	}
	accepted := 0
	for {
		select {
		case status := <-statuses:
			if status.QueueItemID == queued.ID && status.Status == QueuedUserMessageAccepted {
				accepted++
			}
		default:
			if accepted != 1 {
				t.Fatalf("accepted queue statuses = %d", accepted)
			}
			if len(duringFlush.QueuedMessages) > 0 && duringFlush.QueuedMessages[0].ID != queued.ID {
				t.Fatalf("hydration queue = %+v", duringFlush.QueuedMessages)
			}
			return
		}
	}
}
