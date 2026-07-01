package app

import (
	"context"
	"testing"

	"core/shared/client"
	"core/shared/clientui"
	"core/shared/serverapi"
)

type transcriptDiagTestSessionViewClient struct{}

func (transcriptDiagTestSessionViewClient) GetSessionMainView(context.Context, serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	return serverapi.SessionMainViewResponse{}, nil
}

func (transcriptDiagTestSessionViewClient) GetSessionTranscriptPage(context.Context, serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	return serverapi.SessionTranscriptPageResponse{}, nil
}

type transcriptDiagTestRuntimeControlClient struct{}

func (transcriptDiagTestRuntimeControlClient) SetSessionName(context.Context, serverapi.RuntimeSetSessionNameRequest) error {
	return nil
}
func (transcriptDiagTestRuntimeControlClient) SetThinkingLevel(context.Context, serverapi.RuntimeSetThinkingLevelRequest) error {
	return nil
}
func (transcriptDiagTestRuntimeControlClient) SetFastModeEnabled(context.Context, serverapi.RuntimeSetFastModeEnabledRequest) (serverapi.RuntimeSetFastModeEnabledResponse, error) {
	return serverapi.RuntimeSetFastModeEnabledResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) SetReviewerEnabled(context.Context, serverapi.RuntimeSetReviewerEnabledRequest) (serverapi.RuntimeSetReviewerEnabledResponse, error) {
	return serverapi.RuntimeSetReviewerEnabledResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) SetAutoCompactionEnabled(context.Context, serverapi.RuntimeSetAutoCompactionEnabledRequest) (serverapi.RuntimeSetAutoCompactionEnabledResponse, error) {
	return serverapi.RuntimeSetAutoCompactionEnabledResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) SetQuestionsEnabled(context.Context, serverapi.RuntimeSetQuestionsEnabledRequest) (serverapi.RuntimeSetQuestionsEnabledResponse, error) {
	return serverapi.RuntimeSetQuestionsEnabledResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) AppendCommittedEntry(context.Context, serverapi.RuntimeAppendCommittedEntryRequest) error {
	return nil
}
func (transcriptDiagTestRuntimeControlClient) ShouldCompactBeforeUserMessage(context.Context, serverapi.RuntimeShouldCompactBeforeUserMessageRequest) (serverapi.RuntimeShouldCompactBeforeUserMessageResponse, error) {
	return serverapi.RuntimeShouldCompactBeforeUserMessageResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) SubmitUserTurn(context.Context, serverapi.RuntimeSubmitUserTurnRequest) (serverapi.RuntimeSubmitUserTurnResponse, error) {
	return serverapi.RuntimeSubmitUserTurnResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) SubmitUserShellCommand(context.Context, serverapi.RuntimeSubmitUserShellCommandRequest) error {
	return nil
}
func (transcriptDiagTestRuntimeControlClient) CompactContext(context.Context, serverapi.RuntimeCompactContextRequest) error {
	return nil
}
func (transcriptDiagTestRuntimeControlClient) CompactContextForPreSubmit(context.Context, serverapi.RuntimeCompactContextForPreSubmitRequest) error {
	return nil
}
func (transcriptDiagTestRuntimeControlClient) HasQueuedUserWork(context.Context, serverapi.RuntimeHasQueuedUserWorkRequest) (serverapi.RuntimeHasQueuedUserWorkResponse, error) {
	return serverapi.RuntimeHasQueuedUserWorkResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) SubmitQueuedUserMessages(context.Context, serverapi.RuntimeSubmitQueuedUserMessagesRequest) (serverapi.RuntimeSubmitQueuedUserMessagesResponse, error) {
	return serverapi.RuntimeSubmitQueuedUserMessagesResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) Interrupt(context.Context, serverapi.RuntimeInterruptRequest) error {
	return nil
}
func (transcriptDiagTestRuntimeControlClient) QueueUserMessage(context.Context, serverapi.RuntimeQueueUserMessageRequest) (serverapi.RuntimeQueueUserMessageResponse, error) {
	return serverapi.RuntimeQueueUserMessageResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) DiscardQueuedUserMessage(context.Context, serverapi.RuntimeDiscardQueuedUserMessageRequest) (serverapi.RuntimeDiscardQueuedUserMessageResponse, error) {
	return serverapi.RuntimeDiscardQueuedUserMessageResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) RecordPromptHistory(context.Context, serverapi.RuntimeRecordPromptHistoryRequest) error {
	return nil
}
func (transcriptDiagTestRuntimeControlClient) ShowGoal(context.Context, serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) SetGoal(context.Context, serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) PauseGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) ResumeGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) CompleteGoal(context.Context, serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, nil
}
func (transcriptDiagTestRuntimeControlClient) ClearGoal(context.Context, serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return serverapi.RuntimeGoalShowResponse{}, nil
}

var _ client.SessionViewClient = transcriptDiagTestSessionViewClient{}
var _ client.RuntimeControlClient = transcriptDiagTestRuntimeControlClient{}

func TestRuntimeCarryQueueResumesInOrder(t *testing.T) {
	logger := &testUILogger{}
	m := newProjectedStaticUIModel(
		WithUILogger(logger),
		WithUITranscriptDiagnostics(true),
		WithUISessionID("session-1"),
	)
	m.waitRuntimeEventAfterHydration = true
	m.pendingRuntimeEvents = []clientui.Event{{Kind: clientui.EventAssistantDelta, AssistantDelta: "older"}}
	carry := clientui.Event{Kind: clientui.EventLocalEntryAdded, CommittedTranscriptChanged: true, CommittedEntryStart: 1, CommittedEntryStartSet: true, CommittedEntryCount: 2, TranscriptEntries: []clientui.ChatEntry{{Role: "reviewer_status", Text: "Supervisor ran: no changes."}}}

	next, _ := m.Update(runtimeEventBatchMsg{
		events: []clientui.Event{{Kind: clientui.EventConversationUpdated}},
		carry:  &carry,
	})
	updated := next.(*uiModel)
	if got := len(updated.pendingRuntimeEvents); got != 2 {
		t.Fatalf("expected carry prepended to pending runtime events, got %d", got)
	}
	if updated.pendingRuntimeEvents[0].Kind != clientui.EventLocalEntryAdded {
		t.Fatalf("expected carry first in pending runtime events, got %+v", updated.pendingRuntimeEvents)
	}
	updated.waitRuntimeEventAfterHydration = false

	resumeCmd := updated.waitRuntimeEventCmd()
	if resumeCmd == nil {
		t.Fatal("expected pending runtime event resume command")
	}
	batch, ok := resumeCmd().(runtimeEventBatchMsg)
	if !ok {
		t.Fatalf("expected runtimeEventBatchMsg, got %T", resumeCmd())
	}
	if len(batch.events) != 1 || batch.events[0].Kind != clientui.EventLocalEntryAdded {
		t.Fatalf("expected carry resumed before older pending event, got %+v", batch.events)
	}
}

func TestRuntimeClientTranscriptDiagnosticsEnablePaths(t *testing.T) {
	t.Setenv("KENT_TRANSCRIPT_DIAGNOSTICS", "")
	client := newUIRuntimeClientWithReads("session-1", transcriptDiagTestSessionViewClient{}, transcriptDiagTestRuntimeControlClient{}).(*sessionRuntimeClient)
	if client.transcriptDiagnosticsEnabled() {
		t.Fatal("expected transcript diagnostics disabled by default")
	}
	client.SetTranscriptDiagnosticsEnabled(true)
	if !client.transcriptDiagnosticsEnabled() {
		t.Fatal("expected explicit runtime-client diagnostics enable to take effect")
	}
	client.SetTranscriptDiagnosticsEnabled(false)
	if client.transcriptDiagnosticsEnabled() {
		t.Fatal("expected runtime-client diagnostics disable to take effect when env is unset")
	}
	t.Setenv("KENT_TRANSCRIPT_DIAGNOSTICS", "1")
	if !client.transcriptDiagnosticsEnabled() {
		t.Fatal("expected transcript diagnostics env to enable runtime-client diagnostics")
	}
}
