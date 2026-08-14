package runtime

import (
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
