package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
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
	if err := engine.steer(stepID, steerReasoningDeltaIntent(llm.ReasoningSummaryDelta{
		Key: "plan", Text: "inspect the repository", CurrentStatus: &llm.ReasoningStatus{Text: "Planning"},
	})); err != nil {
		t.Fatalf("reasoning: %v", err)
	}
	for _, call := range []llm.ToolCall{{ID: "call-1", Name: "shell"}, {ID: "call-2", Name: "patch"}} {
		if err := engine.transcriptRuntimeState().RecordLiveToolStart(stepID, call); err != nil {
			t.Fatalf("tool %s: %v", call.ID, err)
		}
	}
	first := engine.QueueUserMessageWithClientRequestID("first", "client-1")
	second := engine.QueueUserMessageWithClientRequestID("second", "client-2")
	snapshot := hydrationSnapshot(t, engine)
	if snapshot.ActiveReasoning == nil || snapshot.ActiveReasoning.StepID != stepID ||
		snapshot.ActiveReasoning.Key != "plan" || snapshot.ActiveReasoning.Text != "inspect the repository" ||
		snapshot.ActiveReasoning.CurrentStatus == nil || snapshot.ActiveReasoning.CurrentStatus.Text != "Planning" {
		t.Fatalf("reasoning = %+v", snapshot.ActiveReasoning)
	}
	if len(snapshot.InFlightTools) != 2 || snapshot.InFlightTools[0].ToolCallID != "call-1" ||
		snapshot.InFlightTools[1].ToolCallID != "call-2" {
		t.Fatalf("tools = %+v", snapshot.InFlightTools)
	}
	if len(snapshot.QueuedMessages) != 2 || snapshot.QueuedMessages[0].ID != first.ID ||
		snapshot.QueuedMessages[1].ID != second.ID {
		t.Fatalf("queue = %+v", snapshot.QueuedMessages)
	}
	if err := engine.steer(stepID, steerClearStreamingStateIntent()); err != nil {
		t.Fatalf("reset reasoning: %v", err)
	}
	if got := hydrationSnapshot(t, engine).ActiveReasoning; got != nil {
		t.Fatalf("reasoning after reset = %+v", got)
	}
}

func TestTranscriptHydrationSnapshotProjectsReviewerCompactionAndCompletion(t *testing.T) {
	engine := newTranscriptHydrationSnapshotTestEngine(t, &fakeClient{})
	const stepID = "step-current"
	engine.compactionRuntimeState().SetCount(3)
	if err := engine.steer(stepID,
		steerEventIntent(Event{Kind: EventReviewerStarted, StepID: stepID}),
		steerEventIntent(Event{Kind: EventCompactionStarted, StepID: stepID, Compaction: &CompactionStatus{Mode: "auto", Count: 3}}),
	); err != nil {
		t.Fatalf("start owner state: %v", err)
	}
	snapshot := hydrationSnapshot(t, engine)
	if snapshot.ActiveReviewer == nil || snapshot.ActiveReviewer.StepID != stepID ||
		snapshot.ActiveCompaction == nil || snapshot.ActiveCompaction.StepID != stepID ||
		snapshot.ActiveCompaction.Mode != "auto" || snapshot.ActiveCompaction.Count != 3 {
		t.Fatalf("active owner state = reviewer %+v compaction %+v", snapshot.ActiveReviewer, snapshot.ActiveCompaction)
	}
	if err := engine.steer(stepID,
		steerEventIntent(Event{Kind: EventReviewerCompleted, StepID: stepID}),
		steerEventIntent(Event{Kind: EventCompactionCompleted, StepID: stepID, Compaction: &CompactionStatus{Mode: "auto", Count: 3}}),
	); err != nil {
		t.Fatalf("finish owner state: %v", err)
	}
	snapshot = hydrationSnapshot(t, engine)
	if snapshot.ActiveReviewer != nil || snapshot.ActiveCompaction != nil || snapshot.CompactionCount != 3 {
		t.Fatalf("terminal owner state = reviewer %+v compaction %+v count %d",
			snapshot.ActiveReviewer, snapshot.ActiveCompaction, snapshot.CompactionCount)
	}
}

func TestTranscriptHydrationSnapshotProjectsContextGoalToolsAndQueueTerminalState(t *testing.T) {
	engine := newTranscriptHydrationSnapshotTestEngine(t, &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("done")},
	}}})
	for _, id := range []string{"call-1", "call-2"} {
		if err := engine.transcriptRuntimeState().RecordLiveToolStart("step-current", llm.ToolCall{ID: id, Name: "shell"}); err != nil {
			t.Fatalf("tool %s: %v", id, err)
		}
	}
	engine.transcriptRuntimeState().CompleteLiveTool("call-1")
	discarded := engine.QueueUserMessage("discard me")
	if !engine.DiscardQueuedUserMessage(discarded.ID) {
		t.Fatalf("discard queue item %q", discarded.ID)
	}
	queued := engine.QueueUserMessage("flush me")
	if _, err := engine.SubmitQueuedUserMessages(context.Background()); err != nil {
		t.Fatalf("flush queue item %q: %v", queued.ID, err)
	}
	goalEngine := newTranscriptHydrationSnapshotTestEngine(t, &fakeClient{})
	goalEngine.setLastUsage(llm.Usage{InputTokens: 123, WindowTokens: 4_000})
	if _, err := goalEngine.SetGoal("ship the feature", session.GoalActorUser); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	goalEngine.goalLoopState().Suspend()
	goalSnapshot := hydrationSnapshot(t, goalEngine)
	if goalSnapshot.ContextUsage == nil || *goalSnapshot.ContextUsage != goalEngine.ContextUsage() ||
		goalSnapshot.Goal == nil || goalSnapshot.Goal.Objective != "ship the feature" || !goalSnapshot.GoalSuspended {
		t.Fatalf("context/goal = %+v %+v suspended=%t", goalSnapshot.ContextUsage, goalSnapshot.Goal, goalSnapshot.GoalSuspended)
	}
	snapshot := hydrationSnapshot(t, engine)
	if len(snapshot.InFlightTools) != 1 || snapshot.InFlightTools[0].ToolCallID != "call-2" {
		t.Fatalf("tools after completion = %+v", snapshot.InFlightTools)
	}
	engine.transcriptRuntimeState().AbortLiveTools()
	if snapshot = hydrationSnapshot(t, engine); len(snapshot.InFlightTools) != 0 {
		t.Fatalf("tools after abort = %+v", snapshot.InFlightTools)
	}
	if snapshot = hydrationSnapshot(t, engine); len(snapshot.QueuedMessages) != 0 {
		t.Fatalf("queue after discard/flush = %+v", snapshot.QueuedMessages)
	}
}

func TestFailedQueueFlushRestoresAcceptedStateAcrossHydrationRace(t *testing.T) {
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
	queued := engine.QueueUserMessageWithClientRequestID("queued input", "request-id")
	blocker := mustBlockTestEventLogAppends(t, store)
	flushDone := make(chan error, 1)
	go func() {
		_, _, err := engine.SubmitQueuedUserMessagesWithActiveHook(context.Background(), nil)
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
			if accepted != 2 {
				t.Fatalf("accepted queue statuses = %d", accepted)
			}
			if len(duringFlush.QueuedMessages) > 0 && duringFlush.QueuedMessages[0].ID != queued.ID {
				t.Fatalf("hydration queue = %+v", duringFlush.QueuedMessages)
			}
			return
		}
	}
}
