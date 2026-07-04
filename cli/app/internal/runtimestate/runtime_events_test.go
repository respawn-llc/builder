package runtimestate

import (
	"testing"

	"core/shared/clientui"
)

const (
	testQueueItemID       = "11111111-1111-4111-8111-111111111111"
	testSecondQueueItemID = "22222222-2222-4222-8222-222222222222"
	testServerQueueItemID = "33333333-3333-4333-8333-333333333333"
	testClientRequestID   = "44444444-4444-4444-8444-444444444444"
	testOtherRequestID    = "55555555-5555-4555-8555-555555555555"
	testRunID             = "66666666-6666-4666-8666-666666666666"
	testStepID            = "77777777-7777-4777-8777-777777777777"
	testBackgroundID      = "88888888-8888-4888-8888-888888888888"
)

func TestReduceRuntimeEvent_UserMessageFlushedProducesPendingInputAndConversationUpdates(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{Freshness: clientui.ConversationFreshnessFresh},
		PendingInputState{
			PendingInjected: []clientui.QueuedUserMessage{
				{ID: testQueueItemID, Text: "steered message"},
				{ID: testSecondQueueItemID, Text: "follow-up"},
			},
		},
		RuntimeReasoningState{},
		false,
		clientui.Event{
			Kind:                         clientui.EventUserMessageFlushed,
			UserMessage:                  "steered message",
			UserMessageBatchQueueItemIDs: []string{testQueueItemID},
		},
	)

	if len(update.PendingInput.ConsumedQueueItemIDs) != 1 || update.PendingInput.ConsumedQueueItemIDs[0] != testQueueItemID {
		t.Fatalf("consumed queue item ids = %+v, want %s", update.PendingInput.ConsumedQueueItemIDs, testQueueItemID)
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
			PendingInjected: []clientui.QueuedUserMessage{
				{ID: testQueueItemID, Text: "steered message"},
				{ID: testSecondQueueItemID, Text: "follow-up"},
			},
		},
		RuntimeReasoningState{},
		false,
		clientui.Event{
			Kind: clientui.EventQueuedUserMessageStatus,
			QueuedUserMessageStatus: &clientui.QueuedUserMessageStatusEvent{
				QueueItemID: testQueueItemID,
				Status:      clientui.QueuedUserMessageSubmitted,
			},
		},
	)

	if len(update.PendingInput.ConsumedQueueItemIDs) != 1 || update.PendingInput.ConsumedQueueItemIDs[0] != testQueueItemID {
		t.Fatalf("consumed queue item ids = %+v, want %s", update.PendingInput.ConsumedQueueItemIDs, testQueueItemID)
	}
	if len(update.PendingInput.State.PendingInjected) != 1 || update.PendingInput.State.PendingInjected[0].ID != testSecondQueueItemID {
		t.Fatalf("pending injected = %+v, want only %s", update.PendingInput.State.PendingInjected, testSecondQueueItemID)
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
			PendingInjected: []clientui.QueuedUserMessage{{ID: testQueueItemID, Text: "steered message", ClientRequestID: testClientRequestID}},
		},
		RuntimeReasoningState{},
		false,
		clientui.Event{
			Kind: clientui.EventQueuedUserMessageStatus,
			QueuedUserMessageStatus: &clientui.QueuedUserMessageStatusEvent{
				QueueItemID:     testQueueItemID,
				ClientRequestID: testClientRequestID,
				Status:          clientui.QueuedUserMessageFailed,
				RestoreText:     " steered message ",
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
			PendingInjected: []clientui.QueuedUserMessage{{ID: testQueueItemID, Text: "steered message", ClientRequestID: testClientRequestID}},
		},
		RuntimeReasoningState{},
		false,
		clientui.Event{
			Kind: clientui.EventQueuedUserMessageStatus,
			QueuedUserMessageStatus: &clientui.QueuedUserMessageStatusEvent{
				QueueItemID:     testServerQueueItemID,
				ClientRequestID: testClientRequestID,
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
	if len(update.PendingInput.ConsumedQueueItemIDs) != 2 ||
		update.PendingInput.ConsumedQueueItemIDs[0] != testServerQueueItemID ||
		update.PendingInput.ConsumedQueueItemIDs[1] != testClientRequestID {
		t.Fatalf("consumed ids = %+v, want server queue id and client request id", update.PendingInput.ConsumedQueueItemIDs)
	}
}

func TestReduceRuntimeEvent_QueuedStatusFailedIgnoresOtherClientRestoreText(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{
			PendingInjected: []clientui.QueuedUserMessage{{ID: testQueueItemID, Text: "local message", ClientRequestID: testClientRequestID}},
		},
		RuntimeReasoningState{},
		false,
		clientui.Event{
			Kind: clientui.EventQueuedUserMessageStatus,
			QueuedUserMessageStatus: &clientui.QueuedUserMessageStatusEvent{
				QueueItemID:     testQueueItemID,
				ClientRequestID: testOtherRequestID,
				Status:          clientui.QueuedUserMessageFailed,
				RestoreText:     "other client message",
				FailureReason:   clientui.QueuedUserMessageFailureTerminalWorkflowCompletion,
			},
		},
	)

	if update.PendingInput.RestoredText != "" {
		t.Fatalf("restored text = %q, want empty for other client failure", update.PendingInput.RestoredText)
	}
	if len(update.PendingInput.State.PendingInjected) != 1 || update.PendingInput.State.PendingInjected[0].ClientRequestID != testClientRequestID {
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
				QueueItemID:     testServerQueueItemID,
				ClientRequestID: testOtherRequestID,
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
		clientui.Event{
			Kind:     clientui.EventRunStateChanged,
			RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)},
		},
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
		RunID:      testRunID,
		StepID:     testStepID,
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
		clientui.Event{
			Kind:     clientui.EventRunStateChanged,
			RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeGoalLoop)},
		},
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
		clientui.Event{
			Kind:     clientui.EventRunStateChanged,
			RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleFinished, clientui.RunModeGoalLoop)},
		},
	)
	if !stopped.RunState.State.Run.IsGoalLoopRunning() {
		t.Fatalf("raw goal loop stop changed run state: %+v", stopped.RunState.State)
	}
}

func TestReduceRuntimeEvent_ReasoningDeltaTracksStatusAndResetClearsStream(t *testing.T) {
	delta := &clientui.ReasoningDelta{Key: "reasoning", Role: "assistant", Text: "**Checking tests**\nmore"}
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
		clientui.Event{
			Kind: clientui.EventBackgroundUpdated,
			Background: &clientui.BackgroundShellEvent{
				Type:        "completed",
				ID:          testBackgroundID,
				State:       "completed",
				CompactText: "Background shell completed",
			},
		},
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
	if notice.Message != "Background shell completed" {
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
		clientui.Event{
			Kind:       clientui.EventBackgroundUpdated,
			Background: &clientui.BackgroundShellEvent{Type: "completed", ID: testBackgroundID, State: "completed"},
		},
	)

	notice := update.Notices.BackgroundNotice
	if notice == nil {
		t.Fatal("expected completion notice")
	}
	want := "background shell " + testBackgroundID + " completed"
	if notice.Message != want {
		t.Fatalf("notice message = %q, want %q", notice.Message, want)
	}
}

func TestReduceRuntimeEvent_BackgroundKilledByRuntimeProducesErrorNotice(t *testing.T) {
	update := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{
			Kind: clientui.EventBackgroundUpdated,
			Background: &clientui.BackgroundShellEvent{
				Type:        "killed",
				ID:          testBackgroundID,
				State:       "killed",
				CompactText: "Background shell killed",
			},
		},
	)

	notice := update.Notices.BackgroundNotice
	if notice == nil {
		t.Fatal("expected killed notice")
	}
	if notice.Kind != BackgroundNoticeError {
		t.Fatalf("notice kind = %v, want error", notice.Kind)
	}
}

func TestReduceRuntimeEvent_CompactionLifecycle(t *testing.T) {
	started := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventCompactionStarted},
	)
	if !started.RunState.State.Compaction.IsRunning() {
		t.Fatal("expected compaction started to set compacting state")
	}

	completed := ReduceRuntimeEvent(
		RuntimeRunState{Compaction: clientui.NewCompactionLifecycle(true)},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventCompactionCompleted, Compaction: &clientui.CompactionStatus{Mode: "auto", Count: 2}},
	)
	if completed.RunState.State.Compaction.IsRunning() {
		t.Fatal("expected compaction completed to clear compacting state")
	}

	failed := ReduceRuntimeEvent(
		RuntimeRunState{Compaction: clientui.NewCompactionLifecycle(true)},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventCompactionFailed, Compaction: &clientui.CompactionStatus{Mode: "auto", Count: 2, Error: "failed"}},
	)
	if failed.RunState.State.Compaction.IsRunning() {
		t.Fatal("expected compaction failed to clear compacting state")
	}
}

func TestReduceRuntimeEvent_ReviewerLifecycle(t *testing.T) {
	started := ReduceRuntimeEvent(
		RuntimeRunState{},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventReviewerStarted},
	)
	if !started.RunState.State.Reviewer.IsRunning() || !started.RunState.State.Reviewer.IsBlocking() {
		t.Fatalf("reviewer lifecycle = %v, want blocking running", started.RunState.State.Reviewer)
	}

	completed := ReduceRuntimeEvent(
		RuntimeRunState{Reviewer: clientui.ReviewerLifecycleRunningBlocking},
		RuntimeConversationState{},
		PendingInputState{},
		RuntimeReasoningState{},
		false,
		clientui.Event{Kind: clientui.EventReviewerCompleted},
	)
	if completed.RunState.State.Reviewer.IsRunning() {
		t.Fatalf("reviewer lifecycle = %v, want idle", completed.RunState.State.Reviewer)
	}
}

func TestReduceRuntimeRunStateEventRejectsInvalidLifecycleAtReducerBoundary(t *testing.T) {
	initial := RuntimeRunState{Run: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)}
	reduction := ReduceRuntimeRunStateEvent(
		initial,
		true,
		clientui.Event{
			Kind:     clientui.EventRunStateChanged,
			RunState: &clientui.RunState{Lifecycle: clientui.RunLifecycle{Phase: clientui.RunLifecycleIdle, Mode: clientui.RunModeGoalLoop}},
		},
	)
	if reduction.State.Run != initial.Run {
		t.Fatalf("invalid run transition changed state: %+v", reduction.State)
	}
	if reduction.Err == nil {
		t.Fatal("expected invalid run transition to surface an error")
	}
}

func TestDomainReducersIgnoreUnownedEventConcerns(t *testing.T) {
	evt := clientui.Event{
		Kind:       clientui.EventBackgroundUpdated,
		Background: &clientui.BackgroundShellEvent{Type: "completed", ID: testBackgroundID, State: "completed"},
	}

	if reasoning := ReduceRuntimeReasoningEvent(RuntimeReasoningState{StatusHeader: "thinking"}, evt); reasoning.State.StatusHeader != "thinking" || len(reasoning.Stream) != 0 {
		t.Fatalf("reasoning reducer handled background event: %+v", reasoning)
	}
	if pending := ReduceRuntimePendingInputEvent(PendingInputState{PendingInjected: []clientui.QueuedUserMessage{{ID: testQueueItemID}}}, evt); len(pending.State.PendingInjected) != 1 || pending.State.PendingInjected[0].ID != testQueueItemID {
		t.Fatalf("pending input reducer handled background event: %+v", pending)
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

func TestReduceRuntimeNoticeEvent_DiagnosticFailuresProduceNotices(t *testing.T) {
	sleepGuard := ReduceRuntimeNoticeEvent(clientui.Event{Kind: clientui.EventSleepGuardFailed, Error: "disabled by OS"})
	if sleepGuard.DiagnosticNotice == nil || sleepGuard.DiagnosticNotice.Kind != BackgroundNoticeError {
		t.Fatalf("sleep guard notice = %+v, want diagnostic error", sleepGuard.DiagnosticNotice)
	}

	promptHistory := ReduceRuntimeNoticeEvent(clientui.Event{Kind: clientui.EventPromptHistoryPersistFailed, Error: "disk full"})
	if promptHistory.TransientDiagnostic == nil || promptHistory.TransientDiagnostic.Kind != BackgroundNoticeError {
		t.Fatalf("prompt history notice = %+v, want transient diagnostic error", promptHistory.TransientDiagnostic)
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
