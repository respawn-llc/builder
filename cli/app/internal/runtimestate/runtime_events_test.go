package runtimestate

import (
	"testing"

	"core/shared/clientui"
)

func TestReduceRuntimeEvent_UserMessageFlushedProducesPendingInputAndConversationUpdates(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{Freshness: clientui.ConversationFreshnessFresh},
		PendingInputState{
			PendingInjected: []clientui.QueuedUserMessage{{ID: "queue-1", Text: "steered message"}, {ID: "queue-2", Text: "follow-up"}},
		},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventUserMessageFlushed, UserMessage: "steered message", UserMessageBatchQueueItemIDs: []string{"queue-1"}},
	)

	if len(update.PendingInput.ConsumedQueueItemIDs) != 1 || update.PendingInput.ConsumedQueueItemIDs[0] != "queue-1" {
		t.Fatalf("consumed queue item ids = %+v, want queue-1", update.PendingInput.ConsumedQueueItemIDs)
	}
	if len(update.PendingInput.State.PendingInjected) != 1 || update.PendingInput.State.PendingInjected[0].Text != "follow-up" {
		t.Fatalf("expected first injected item consumed, got %+v", update.PendingInput.State.PendingInjected)
	}
	if update.Conversation.State.Freshness != clientui.ConversationFreshnessEstablished {
		t.Fatalf("conversation freshness = %v, want established", update.Conversation.State.Freshness)
	}
}

func TestReduceRuntimeEvent_QueuedStatusSubmittedRemovesPendingInputByID(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{
			PendingInjected: []clientui.QueuedUserMessage{{ID: "queue-1", Text: "steered message"}, {ID: "queue-2", Text: "follow-up"}},
		},
		RuntimeReasoningState{},
		false,
		clientui.Event{
			Kind: clientui.EventQueuedUserMessageStatus,
			QueuedUserMessageStatus: &clientui.QueuedUserMessageStatusEvent{
				QueueItemID: "queue-1",
				Status:      clientui.QueuedUserMessageSubmitted,
			},
		},
	)

	if len(update.PendingInput.ConsumedQueueItemIDs) != 1 || update.PendingInput.ConsumedQueueItemIDs[0] != "queue-1" {
		t.Fatalf("consumed queue item ids = %+v, want queue-1", update.PendingInput.ConsumedQueueItemIDs)
	}
	if len(update.PendingInput.State.PendingInjected) != 1 || update.PendingInput.State.PendingInjected[0].ID != "queue-2" {
		t.Fatalf("pending injected = %+v, want only queue-2", update.PendingInput.State.PendingInjected)
	}
	if update.PendingInput.RestoredText != "" {
		t.Fatalf("restored text = %q, want empty", update.PendingInput.RestoredText)
	}
}

func TestReduceRuntimeEvent_QueuedStatusFailedRemovesPendingInputAndRestoresText(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{
			PendingInjected: []clientui.QueuedUserMessage{{ID: "queue-1", Text: "steered message", ClientRequestID: "req-local"}},
		},
		RuntimeReasoningState{},
		false,
		clientui.Event{
			Kind: clientui.EventQueuedUserMessageStatus,
			QueuedUserMessageStatus: &clientui.QueuedUserMessageStatusEvent{
				QueueItemID:     "queue-1",
				ClientRequestID: "req-local",
				Status:          clientui.QueuedUserMessageFailed,
				RestoreText:     "steered message",
				FailureReason:   clientui.QueuedUserMessageFailureTerminalWorkflowCompletion,
			},
		},
	)

	if len(update.PendingInput.State.PendingInjected) != 0 {
		t.Fatalf("pending injected = %+v, want empty", update.PendingInput.State.PendingInjected)
	}
	if update.PendingInput.RestoredText != "steered message" {
		t.Fatalf("restored text = %q, want queued text", update.PendingInput.RestoredText)
	}
}

func TestReduceRuntimeEvent_QueuedStatusFailedMatchesProvisionalPendingInputByClientRequestID(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{
			PendingInjected: []clientui.QueuedUserMessage{{ID: "local-provisional", Text: "steered message", ClientRequestID: "req-local"}},
		},
		RuntimeReasoningState{},
		false,
		clientui.Event{
			Kind: clientui.EventQueuedUserMessageStatus,
			QueuedUserMessageStatus: &clientui.QueuedUserMessageStatusEvent{
				QueueItemID:     "server-queue-1",
				ClientRequestID: "req-local",
				Status:          clientui.QueuedUserMessageFailed,
				RestoreText:     "steered message",
				FailureReason:   clientui.QueuedUserMessageFailureClosing,
			},
		},
	)

	if len(update.PendingInput.State.PendingInjected) != 0 {
		t.Fatalf("pending injected = %+v, want provisional item removed by client request id", update.PendingInput.State.PendingInjected)
	}
	if update.PendingInput.RestoredText != "steered message" {
		t.Fatalf("restored text = %q, want queued text", update.PendingInput.RestoredText)
	}
	if len(update.PendingInput.ConsumedQueueItemIDs) != 2 || update.PendingInput.ConsumedQueueItemIDs[0] != "server-queue-1" || update.PendingInput.ConsumedQueueItemIDs[1] != "req-local" {
		t.Fatalf("consumed ids = %+v, want server queue id and client request id", update.PendingInput.ConsumedQueueItemIDs)
	}
}

func TestReduceRuntimeEvent_QueuedStatusFailedIgnoresOtherClientRestoreText(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{
			PendingInjected: []clientui.QueuedUserMessage{{ID: "queue-1", Text: "local message", ClientRequestID: "req-local"}},
		},
		RuntimeReasoningState{},
		false,
		clientui.Event{
			Kind: clientui.EventQueuedUserMessageStatus,
			QueuedUserMessageStatus: &clientui.QueuedUserMessageStatusEvent{
				QueueItemID:     "queue-1",
				ClientRequestID: "req-other",
				Status:          clientui.QueuedUserMessageFailed,
				RestoreText:     "other client message",
				FailureReason:   clientui.QueuedUserMessageFailureTerminalWorkflowCompletion,
			},
		},
	)

	if update.PendingInput.RestoredText != "" {
		t.Fatalf("restored text = %q, want empty for other client failure", update.PendingInput.RestoredText)
	}
	if len(update.PendingInput.State.PendingInjected) != 1 || update.PendingInput.State.PendingInjected[0].ClientRequestID != "req-local" {
		t.Fatalf("pending injected = %+v, want local queued item preserved", update.PendingInput.State.PendingInjected)
	}
}

func TestReduceRuntimeEvent_QueuedStatusFailedWithoutLocalPendingInputDoesNotRestoreText(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{
			Kind: clientui.EventQueuedUserMessageStatus,
			QueuedUserMessageStatus: &clientui.QueuedUserMessageStatusEvent{
				QueueItemID:     "queue-other",
				ClientRequestID: "req-other",
				Status:          clientui.QueuedUserMessageFailed,
				RestoreText:     "other client message",
				FailureReason:   clientui.QueuedUserMessageFailureTerminalWorkflowCompletion,
			},
		},
	)

	if update.PendingInput.RestoredText != "" {
		t.Fatalf("restored text = %q, want empty without local queued item", update.PendingInput.RestoredText)
	}
}

func TestReduceRuntimeEvent_RunStateStoppedClearsReasoningWithoutChangingLiveness(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{Run: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{StatusHeader: "Running checks"},
		true,
		clientui.Event{Kind: clientui.EventRunStateChanged, RunState: &clientui.RunState{Lifecycle: clientui.IdleRunLifecycle()}},
	)

	if !update.RunState.State.Run.IsRunning() {
		t.Fatal("expected raw stopped run not to clear liveness")
	}
	if update.RunState.Activity != RuntimeActivityUnchanged {
		t.Fatal("expected raw stopped run to leave runtime activity unchanged")
	}
	if update.Reasoning.State.StatusHeader != "" {
		t.Fatalf("expected reasoning status header cleared, got %q", update.Reasoning.State.StatusHeader)
	}
	if !hasReasoningStreamCommand(update.Reasoning.Stream, RuntimeReasoningStreamClear) {
		t.Fatal("expected reasoning stream cleared when run stops")
	}
}

func TestReduceRuntimeEvent_RunStateStartedDoesNotDriveLiveness(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{Run: clientui.IdleRunLifecycle()},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventRunStateChanged, RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)}},
	)

	if update.RunState.State.Run.IsRunning() {
		t.Fatal("expected raw started run not to set liveness")
	}
	if update.RunState.Activity != RuntimeActivityUnchanged {
		t.Fatal("expected raw started run to leave runtime activity unchanged")
	}
}

func TestReduceRuntimeEvent_RuntimeActivityChangedDrivesBusyState(t *testing.T) {
	runningActivity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
		ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
		RunID:      "run-1",
		StepID:     "step-1",
	})
	running := ReduceRuntimeEvent(
		RuntimeRunState{Run: clientui.IdleRunLifecycle()},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{
			Kind:            clientui.EventRuntimeActivityChanged,
			RuntimeActivity: &runningActivity,
		},
	)
	if !running.RunState.State.Run.IsGoalLoopRunning() || running.RunState.Activity != RuntimeActivityRunning {
		t.Fatalf("running activity reduction = %+v, want goal loop running", running.RunState)
	}

	idleActivity := clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: true})
	idle := ReduceRuntimeEvent(
		running.RunState.State,
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		true,
		clientui.Event{
			Kind:            clientui.EventRuntimeActivityChanged,
			RuntimeActivity: &idleActivity,
		},
	)
	if idle.RunState.State.Run.IsRunning() || idle.RunState.Activity != RuntimeActivityIdle {
		t.Fatalf("idle activity reduction = %+v, want idle", idle.RunState)
	}
}

func TestReduceRuntimeEvent_RawGoalRunStateDoesNotDriveGoalLiveness(t *testing.T) {
	started := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventRunStateChanged, RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeGoalLoop)}},
	)
	if started.RunState.State.Run.IsGoalLoopRunning() {
		t.Fatalf("raw goal loop start changed run state: %+v", started.RunState.State)
	}

	stopped := ReduceRuntimeEvent(
		RuntimeRunState{Run: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeGoalLoop)},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		true,
		clientui.Event{Kind: clientui.EventRunStateChanged, RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleFinished, clientui.RunModeGoalLoop)}},
	)
	if !stopped.RunState.State.Run.IsGoalLoopRunning() {
		t.Fatalf("raw goal loop stop changed run state: %+v", stopped.RunState.State)
	}
}

func TestReduceRuntimeEvent_ConversationUpdatedDoesNotRebuildTranscript(t *testing.T) {
	plain := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventConversationUpdated},
	)
	if len(plain.Transcript.AssistantStream) != 0 {
		t.Fatalf("plain conversation_updated changed transcript: %+v", plain.Transcript)
	}
	committed := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventConversationUpdated, CommittedTranscriptChanged: true},
	)
	if len(committed.Transcript.AssistantStream) != 0 {
		t.Fatalf("committed conversation_updated changed transcript: %+v", committed.Transcript)
	}
	recovery := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventConversationUpdated, RecoveryCause: clientui.TranscriptRecoveryCauseStreamGap},
	)
	if len(recovery.Transcript.AssistantStream) != 0 {
		t.Fatalf("recovery conversation_updated changed transcript: %+v", recovery.Transcript)
	}
	gap := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventStreamGap, RecoveryCause: clientui.TranscriptRecoveryCauseStreamGap},
	)
	if len(gap.Transcript.AssistantStream) != 0 {
		t.Fatalf("stream gap changed transcript: %+v", gap.Transcript)
	}
}

func TestReduceRuntimeEvent_AssistantDeltaStreamsAppendAndReset(t *testing.T) {
	appended := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{
			Kind:                    clientui.EventAssistantDelta,
			AssistantDelta:          "hello",
			AssistantDeltaPhase:     clientui.MessagePhaseFinal,
			StepID:                  "step-1",
			AssistantStreamMetadata: &clientui.AssistantStreamMetadata{StepID: "step-1"},
		},
	)
	if len(appended.Transcript.AssistantStream) != 1 {
		t.Fatalf("expected assistant append command, got %+v", appended.Transcript.AssistantStream)
	}
	if got := appended.Transcript.AssistantStream[0]; got.Kind != RuntimeAssistantStreamAppend || got.Delta != "hello" || got.Phase != clientui.MessagePhaseFinal || got.StepID != "step-1" || got.AssistantStreamMetadata == nil || got.AssistantStreamMetadata.StepID != "step-1" {
		t.Fatalf("assistant append command = %+v", appended.Transcript.AssistantStream[0])
	}

	reset := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventAssistantDeltaReset, StepID: "step-1", AssistantStreamMetadata: &clientui.AssistantStreamMetadata{StepID: "step-1"}},
	)
	if len(reset.Transcript.AssistantStream) != 1 {
		t.Fatalf("expected assistant clear command, got %+v", reset.Transcript.AssistantStream)
	}
	if got := reset.Transcript.AssistantStream[0]; got.Kind != RuntimeAssistantStreamClear || got.StepID != "step-1" || got.AssistantStreamMetadata == nil || got.AssistantStreamMetadata.StepID != "step-1" {
		t.Fatalf("assistant clear command = %+v", reset.Transcript.AssistantStream[0])
	}
}

func TestReduceRuntimeEvent_StreamingErrorUpdatedDoesNotRebuildTranscript(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventStreamingErrorUpdated},
	)
	if len(update.Transcript.AssistantStream) != 0 {
		t.Fatalf("streaming_error_updated changed transcript: %+v", update.Transcript)
	}
}

func TestReduceRuntimeEvent_ReasoningDeltaTracksStatusAndResetClearsStream(t *testing.T) {
	delta := &clientui.ReasoningDelta{Key: "reasoning-1", Role: "assistant", Text: "**Checking tests**\nmore"}
	update := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventReasoningDelta, ReasoningDelta: delta},
	)
	if update.Reasoning.State.StatusHeader != "Checking tests" {
		t.Fatalf("reasoning header = %q", update.Reasoning.State.StatusHeader)
	}
	if len(update.Reasoning.Stream) != 1 || update.Reasoning.Stream[0].Kind != RuntimeReasoningStreamUpsert {
		t.Fatalf("expected reasoning upsert command, got %+v", update.Reasoning.Stream)
	}
	if update.Reasoning.Stream[0].Delta == delta {
		t.Fatal("expected reasoning delta to be cloned")
	}
	if *update.Reasoning.Stream[0].Delta != *delta {
		t.Fatalf("reasoning delta clone = %+v, want %+v", update.Reasoning.Stream[0].Delta, delta)
	}

	reset := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{StatusHeader: "Checking tests"},
		false,
		clientui.Event{Kind: clientui.EventReasoningDeltaReset},
	)
	if reset.Reasoning.State.StatusHeader != "Checking tests" {
		t.Fatalf("reasoning reset header = %q, want unchanged", reset.Reasoning.State.StatusHeader)
	}
	if !hasReasoningStreamCommand(reset.Reasoning.Stream, RuntimeReasoningStreamClear) {
		t.Fatal("expected reasoning reset to clear stream")
	}
}

func TestReduceRuntimeEvent_BackgroundCompletionProducesNotice(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventBackgroundUpdated, Background: &clientui.BackgroundShellEvent{Type: "completed", ID: "1000", State: "completed", CompactText: "Background shell 1000 completed (exit 0)"}},
	)

	if update.BackgroundProcesses.Command != RuntimeBackgroundProcessRefresh {
		t.Fatal("expected background update to refresh process snapshots")
	}
	notice := update.Notices.BackgroundNotice
	if notice == nil {
		t.Fatal("expected completion notice")
	}
	if notice.Kind != BackgroundNoticeSuccess {
		t.Fatalf("notice kind = %v, want success", notice.Kind)
	}
	if notice.Message != "Background shell 1000 completed (exit 0)" {
		t.Fatalf("notice message = %q", notice.Message)
	}
}

func TestReduceRuntimeEvent_BackgroundCompletionFallsBackWithoutCompactText(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventBackgroundUpdated, Background: &clientui.BackgroundShellEvent{Type: "completed", ID: "1000", State: "completed"}},
	)

	notice := update.Notices.BackgroundNotice
	if notice == nil {
		t.Fatal("expected completion notice")
	}
	if notice.Message != "background shell 1000 completed" {
		t.Fatalf("notice message = %q", notice.Message)
	}
}

func TestReduceRuntimeEvent_CompactionCompletedClearsCompacting(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{Compaction: clientui.NewCompactionLifecycle(true)},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventCompactionCompleted, Compaction: &clientui.CompactionStatus{Mode: "auto", Count: 2}},
	)

	if update.RunState.State.Compaction.IsRunning() {
		t.Fatal("expected compaction completed to clear compacting state")
	}
	if len(update.Transcript.AssistantStream) != 0 {
		t.Fatalf("expected compaction completed to leave transcript unchanged, got %+v", update.Transcript)
	}
}

func TestReduceRuntimeRunStateEventRejectsInvalidLifecycleAtReducerBoundary(t *testing.T) {
	initial := RuntimeRunState{Run: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)}
	reduction := ReduceRuntimeRunStateEvent(
		initial,
		true,
		clientui.Event{Kind: clientui.EventRunStateChanged, RunState: &clientui.RunState{Lifecycle: clientui.RunLifecycle{Phase: clientui.RunLifecycleIdle, Mode: clientui.RunModeGoalLoop}}},
	)
	if reduction.State.Run != initial.Run {
		t.Fatalf("invalid run transition changed state: %+v", reduction.State)
	}
	if reduction.Err == nil {
		t.Fatal("expected invalid run transition to surface an error")
	}
}

func TestDomainReducersIgnoreUnownedEventConcerns(t *testing.T) {
	evt := clientui.Event{Kind: clientui.EventBackgroundUpdated, Background: &clientui.BackgroundShellEvent{Type: "completed", ID: "1000", State: "completed"}}

	if transcript := ReduceRuntimeTranscriptEvent(evt); len(transcript.AssistantStream) != 0 {
		t.Fatalf("transcript reducer handled background event: %+v", transcript)
	}
	if reasoning := ReduceRuntimeReasoningEvent(RuntimeReasoningState{StatusHeader: "thinking"}, evt); reasoning.State.StatusHeader != "thinking" || len(reasoning.Stream) != 0 {
		t.Fatalf("reasoning reducer handled background event: %+v", reasoning)
	}
	background := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		evt,
	).BackgroundProcesses
	if background.Command != RuntimeBackgroundProcessRefresh {
		t.Fatalf("background process reducer did not own background refresh: %+v", background)
	}
}

func hasReasoningStreamCommand(commands []RuntimeReasoningStreamCommand, kind RuntimeReasoningStreamCommandKind) bool {
	for _, command := range commands {
		if command.Kind == kind {
			return true
		}
	}
	return false
}

func TestExtractReasoningStatusHeaderAcceptsWhitespaceWrappedBoldOnly(t *testing.T) {
	got := ExtractReasoningStatusHeader("  **Summarizing fix and investigation**  ")
	if got != "Summarizing fix and investigation" {
		t.Fatalf("expected bold-only header extracted, got %q", got)
	}
}

func TestExtractReasoningStatusHeaderUsesFirstBoldSpanInMixedContent(t *testing.T) {
	tests := map[string]string{
		"**Header**\nextra":                 "Header",
		"prefix **Header**":                 "Header",
		"**Header** suffix":                 "Header",
		"prefix **Header** suffix":          "Header",
		"before **First** after **Second**": "First",
	}
	for input, want := range tests {
		if got := ExtractReasoningStatusHeader(input); got != want {
			t.Fatalf("expected %q -> %q, got %q", input, want, got)
		}
	}
}

func TestExtractReasoningStatusHeaderRejectsInvalidContent(t *testing.T) {
	tests := []string{"****", "**   **", "**Header*", "*Header**", "plain text", "prefix **Header"}
	for _, input := range tests {
		if got := ExtractReasoningStatusHeader(input); got != "" {
			t.Fatalf("expected %q to be rejected, got %q", input, got)
		}
	}
}
