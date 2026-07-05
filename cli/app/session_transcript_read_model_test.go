package app

import (
	"reflect"
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/clientui"
)

func TestOngoingTranscriptControllerSeedsHydrationAppOwnedFrameSections(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)

	if _, err := controller.Accept(clientui.TranscriptMessage{
		Sequence: 1,
		Kind:     clientui.TranscriptMessageHydration,
		Hydration: &clientui.TranscriptHydration{
			InFlightTools:           []clientui.TranscriptToolStart{{ToolCallID: "tool-1", ToolName: "shell"}},
			QueuedOrSteeredMessages: []clientui.TranscriptQueuedOrSteeredMessageState{{QueueItemID: "queue-1", Status: clientui.QueuedUserMessageAccepted, UserText: "queued prompt"}},
			RunState:                &clientui.RunState{Status: clientui.RunStatusRunning, ActiveKind: clientui.RuntimeActivityActiveKindUserTurn},
			RuntimeActivity:         &clientui.RuntimeActivity{State: clientui.RuntimeActivityRunning, ActiveKind: clientui.RuntimeActivityActiveKindUserTurn},
			InputReconciliation: &clientui.RuntimeInputReconciliationSnapshot{Operations: []clientui.RuntimeInputReconciliation{{
				State: clientui.RuntimeInputReconciliationUnknown,
			}}},
			SessionStatus: clientui.TranscriptSessionStatus{ReviewerEnabled: true, ThinkingLevel: "high", CompactionMode: "auto"},
			SessionIdentity: clientui.TranscriptSessionIdentity{
				SessionID:   "session-1",
				SessionName: "KENT-196",
				ExecutionTarget: clientui.SessionExecutionTarget{
					WorkspaceName: "Kent",
					Worktree:      &clientui.SessionExecutionWorktreeTarget{Name: "KENT-196"},
				},
			},
			CompactionStatus: &clientui.TranscriptCompactionStatus{Mode: "auto", Count: 2, State: "ready"},
			ContextUsage:     &clientui.RuntimeContextUsage{UsedTokens: 1200, WindowTokens: 2000, CacheHitPercent: 75, HasCacheHitPercentage: true},
			GoalStatus:       &clientui.TranscriptGoalStatus{ID: "goal-1", Objective: "finish review fixes", Status: clientui.RuntimeGoalStatusActive},
			BackgroundActivities: []clientui.TranscriptBackgroundActivity{{
				ID:      "background-1",
				State:   "running",
				Preview: "running tests",
			}},
			PendingSessionPrompts: []clientui.TranscriptPendingSessionPrompt{{
				ID:    "prompt-1",
				State: clientui.TranscriptPromptPending,
				Data:  clientui.TranscriptPendingSessionPromptData{Question: "Approve command?"},
			}},
		},
	}); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}

	if got, want := surface.callKinds(), []string{"apply"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls = %v, want %v", got, want)
	}
	wantSections := []ongoing.FrameSectionKind{
		ongoing.FrameSectionPendingTools,
		ongoing.FrameSectionQueuedOrSteered,
		ongoing.FrameSectionRunState,
		ongoing.FrameSectionRuntimeActivity,
		ongoing.FrameSectionInputReconciliation,
		ongoing.FrameSectionSessionStatus,
		ongoing.FrameSectionSessionIdentity,
		ongoing.FrameSectionCompaction,
		ongoing.FrameSectionContextUsage,
		ongoing.FrameSectionGoal,
		ongoing.FrameSectionBackgroundActivity,
		ongoing.FrameSectionPendingPrompt,
	}
	if got := surface.lastFrameSectionKinds(); !reflect.DeepEqual(got, wantSections) {
		t.Fatalf("hydration frame sections = %v, want %v", got, wantSections)
	}
}

func TestOngoingTranscriptControllerLiveAppOwnedMessageKindsRenderFrameSections(t *testing.T) {
	tests := []struct {
		name    string
		message clientui.TranscriptMessage
		want    ongoing.FrameSectionKind
	}{
		{name: "run state", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageRunState), want: ongoing.FrameSectionRunState},
		{name: "runtime activity", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageRuntimeActivity), want: ongoing.FrameSectionRuntimeActivity},
		{name: "input reconciliation", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageInputReconciliation), want: ongoing.FrameSectionInputReconciliation},
		{name: "queued", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageQueuedOrSteeredMessageState), want: ongoing.FrameSectionQueuedOrSteered},
		{name: "session status", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageSessionStatus), want: ongoing.FrameSectionSessionStatus},
		{name: "session identity", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageSessionIdentity), want: ongoing.FrameSectionSessionIdentity},
		{name: "compaction", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageCompactionStatus), want: ongoing.FrameSectionCompaction},
		{name: "context", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageContextUsage), want: ongoing.FrameSectionContextUsage},
		{name: "goal", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageGoalStatus), want: ongoing.FrameSectionGoal},
		{name: "background", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageBackgroundActivity), want: ongoing.FrameSectionBackgroundActivity},
		{name: "pending prompt", message: ongoingTranscriptMessage(2, clientui.TranscriptMessagePendingSessionPrompt), want: ongoing.FrameSectionPendingPrompt},
		{name: "tool start", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageToolStart), want: ongoing.FrameSectionPendingTools},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			surface := &ongoingSurfaceSpy{}
			controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
			if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
				t.Fatalf("accept hydration: %v", err)
			}
			surface.calls = nil

			if _, err := controller.Accept(tt.message); err != nil {
				t.Fatalf("accept live message: %v", err)
			}

			if got, want := surface.callKinds(), []string{"render"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("surface calls = %v, want %v", got, want)
			}
			if got, want := surface.lastFrameSectionKinds(), []ongoing.FrameSectionKind{tt.want}; !reflect.DeepEqual(got, want) {
				t.Fatalf("frame sections = %v, want %v", got, want)
			}
		})
	}
}

func TestOngoingTranscriptControllerToolAbortUpdatesAppReadModelWithoutSurfaceToolFacts(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	if _, err := controller.Accept(ongoingTranscriptMessage(2, clientui.TranscriptMessageToolStart)); err != nil {
		t.Fatalf("accept tool start: %v", err)
	}
	surface.calls = nil

	if _, err := controller.Accept(ongoingTranscriptMessage(3, clientui.TranscriptMessageToolAbort)); err != nil {
		t.Fatalf("accept tool abort: %v", err)
	}

	if got, want := surface.callKinds(), []string{"render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls = %v, want %v", got, want)
	}
	if got := surface.lastFrameSectionKinds(); len(got) != 0 {
		t.Fatalf("frame sections after abort = %v, want no pending tool section", got)
	}
}

func TestOngoingTranscriptControllerSanitizesTranscriptFactsBeforeFrameInput(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}

	messages := []clientui.TranscriptMessage{
		{
			Sequence: 2,
			Kind:     clientui.TranscriptMessageQueuedOrSteeredMessageState,
			QueuedOrSteeredMessageState: &clientui.TranscriptQueuedOrSteeredMessageState{
				Status:   clientui.QueuedUserMessageAccepted,
				UserText: "queued\x1b[2J\nprompt\x07\r",
			},
		},
		{
			Sequence: 3,
			Kind:     clientui.TranscriptMessageBackgroundActivity,
			BackgroundActivity: &clientui.TranscriptBackgroundActivity{
				State:   "running\x1b[H",
				Preview: "preview\x1b]2;title\x07",
				Command: "cmd\r\nnext",
			},
		},
		{
			Sequence: 4,
			Kind:     clientui.TranscriptMessagePendingSessionPrompt,
			PendingSessionPrompt: &clientui.TranscriptPendingSessionPrompt{
				State: clientui.TranscriptPromptPending,
				Kind:  clientui.TranscriptPromptAsk,
				Data:  clientui.TranscriptPendingSessionPromptData{Question: "question\x1b[3J", ToolName: "tool\x07name"},
			},
		},
		{
			Sequence: 5,
			Kind:     clientui.TranscriptMessageToolStart,
			ToolStart: &clientui.TranscriptToolStart{
				ToolCallID: "tool-1",
				ToolName:   "shell\x1b[2J\x07\r",
			},
		},
	}
	for _, message := range messages {
		if _, err := controller.Accept(message); err != nil {
			t.Fatalf("accept %s: %v", message.Kind, err)
		}
	}

	lastFrame := surface.calls[len(surface.calls)-1].frame
	for _, section := range lastFrame.Sections {
		assertTerminalSafeFrameLines(t, section.Lines)
	}
}

func TestOngoingFrameInputExcludesRawTranscriptFactBucketsAndAssistantTail(t *testing.T) {
	frameType := reflect.TypeOf(ongoing.FrameInput{})
	allowed := map[string]reflect.Type{
		"Size":     reflect.TypeOf(ongoing.Size{}),
		"Sections": reflect.TypeOf([]ongoing.FrameSection{}),
		"Cursor":   reflect.TypeOf(ongoing.Cursor{}),
	}
	if frameType.NumField() != len(allowed) {
		t.Fatalf("FrameInput field count = %d, want %d", frameType.NumField(), len(allowed))
	}
	for index := 0; index < frameType.NumField(); index++ {
		field := frameType.Field(index)
		if want, ok := allowed[field.Name]; !ok || field.Type != want {
			t.Fatalf("FrameInput field %s has type %s, want allowed typed frame fields only", field.Name, field.Type)
		}
	}
}
