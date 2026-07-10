package app

import (
	"reflect"
	"testing"

	"core/cli/tui/ongoing"
	"core/cli/tui/transcriptrender"
	"core/shared/clientui"

	"github.com/charmbracelet/lipgloss"
)

func TestOngoingTranscriptControllerSeedsHydrationAppOwnedFrameSections(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)

	if _, err := controller.Accept(clientui.TranscriptMessage{
		Sequence: 1,
		Kind:     clientui.TranscriptMessageHydration,
		Hydration: &clientui.TranscriptHydration{
			InFlightTools:           []clientui.TranscriptToolStart{{ToolCallID: "tool-1", ToolName: "shell"}},
			QueuedOrSteeredMessages: []clientui.TranscriptQueuedOrSteeredMessageState{{QueueItemID: "11111111-1111-4111-8111-111111111111", Status: clientui.QueuedUserMessageAccepted, UserText: "queued prompt"}},
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
				ID:      "22222222-2222-4222-8222-222222222222",
				State:   "running",
				Preview: "running tests",
			}},
			PendingSessionPrompts: []clientui.TranscriptPendingSessionPrompt{{
				ID:    "ask-1",
				State: clientui.TranscriptPromptPending,
				Data:  clientui.TranscriptPendingSessionPromptData{Question: "Approve command?"},
			}},
		},
	}); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}

	if got, want := surface.callKinds(), []string{"apply", "render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls = %v, want %v", got, want)
	}
	wantSections := []ongoing.FrameSectionKind{
		ongoing.FrameSectionPendingTools,
		ongoing.FrameSectionQueuedOrSteered,
		ongoing.FrameSectionBackgroundActivity,
		ongoing.FrameSectionPendingPrompt,
	}
	if got := surface.lastFrameSectionKinds(); !reflect.DeepEqual(got, wantSections) {
		t.Fatalf("hydration frame sections = %v, want %v", got, wantSections)
	}
}

func TestOngoingTranscriptControllerRendersHydrationLiveFactsWithOnlyNonOngoingRows(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)

	if _, err := controller.Accept(clientui.TranscriptMessage{
		Sequence: 1,
		Kind:     clientui.TranscriptMessageHydration,
		Hydration: &clientui.TranscriptHydration{
			CommittedRows: []clientui.TranscriptCommittedRow{
				{
					Visibility: clientui.EntryVisibilityDetail,
					Kind:       clientui.TranscriptRowUser,
					User:       &clientui.TranscriptUserRow{Text: "detail-only"},
				},
				{
					Visibility: clientui.EntryVisibilityHidden,
					Kind:       clientui.TranscriptRowUser,
					User:       &clientui.TranscriptUserRow{Text: "hidden"},
				},
			},
			PendingSessionPrompts: []clientui.TranscriptPendingSessionPrompt{{
				ID:    "11111111-1111-4111-8111-111111111111",
				State: clientui.TranscriptPromptPending,
				Data:  clientui.TranscriptPendingSessionPromptData{Question: "Approve command?"},
			}},
		},
	}); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}

	if got, want := surface.callKinds(), []string{"apply", "render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls = %v, want %v", got, want)
	}
	if got, want := surface.lastFrameSectionLines(ongoing.FrameSectionPendingPrompt), []string{"Approve command?"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prompt section lines = %v, want %v", got, want)
	}
}

func TestOngoingTranscriptControllerLiveAppOwnedMessageKindsRenderFrameSections(t *testing.T) {
	tests := []struct {
		name    string
		message clientui.TranscriptMessage
		want    []ongoing.FrameSectionKind
	}{
		{name: "run state", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageRunState)},
		{name: "runtime activity", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageRuntimeActivity)},
		{name: "input reconciliation", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageInputReconciliation)},
		{name: "queued", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageQueuedOrSteeredMessageState), want: []ongoing.FrameSectionKind{ongoing.FrameSectionQueuedOrSteered}},
		{name: "session status", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageSessionStatus)},
		{name: "session identity", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageSessionIdentity)},
		{name: "compaction", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageCompactionStatus)},
		{name: "context", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageContextUsage)},
		{name: "goal", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageGoalStatus)},
		{name: "background", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageBackgroundActivity), want: []ongoing.FrameSectionKind{ongoing.FrameSectionBackgroundActivity}},
		{name: "pending prompt", message: ongoingTranscriptMessage(2, clientui.TranscriptMessagePendingSessionPrompt), want: []ongoing.FrameSectionKind{ongoing.FrameSectionPendingPrompt}},
		{name: "tool start", message: ongoingTranscriptMessage(2, clientui.TranscriptMessageToolStart), want: []ongoing.FrameSectionKind{ongoing.FrameSectionPendingTools}},
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
			got := surface.lastFrameSectionKinds()
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("frame sections = %v, want %v", got, tt.want)
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
				QueueItemID: "11111111-1111-4111-8111-111111111111",
				Status:      clientui.QueuedUserMessageAccepted,
				UserText:    "queued\x1b[2J\nprompt\x07\r",
			},
		},
		{
			Sequence: 3,
			Kind:     clientui.TranscriptMessageBackgroundActivity,
			BackgroundActivity: &clientui.TranscriptBackgroundActivity{
				ID:      "22222222-2222-4222-8222-222222222222",
				State:   "running\x1b[H",
				Preview: "preview\x1b]2;title\x07",
				Command: "cmd\r\nnext",
			},
		},
		{
			Sequence: 4,
			Kind:     clientui.TranscriptMessagePendingSessionPrompt,
			PendingSessionPrompt: &clientui.TranscriptPendingSessionPrompt{
				ID:    "ask-1",
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
		"Size":         reflect.TypeOf(ongoing.Size{}),
		"Theme":        reflect.TypeOf(""),
		"SpinnerFrame": reflect.TypeOf(0),
		"Sections":     reflect.TypeOf([]ongoing.FrameSection{}),
		"Cursor":       reflect.TypeOf(ongoing.Cursor{}),
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

func TestOngoingTranscriptControllerScratchResetClearsLiveReadModel(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(clientui.TranscriptMessage{
		Sequence: 1,
		Kind:     clientui.TranscriptMessageHydration,
		Hydration: &clientui.TranscriptHydration{
			InFlightTools:           []clientui.TranscriptToolStart{{ToolCallID: "tool-1", ToolName: "shell"}},
			QueuedOrSteeredMessages: []clientui.TranscriptQueuedOrSteeredMessageState{{QueueItemID: "11111111-1111-4111-8111-111111111111", Status: clientui.QueuedUserMessageAccepted, UserText: "queued prompt"}},
			BackgroundActivities:    []clientui.TranscriptBackgroundActivity{{ID: "22222222-2222-4222-8222-222222222222", State: "running", Preview: "tests"}},
			PendingSessionPrompts:   []clientui.TranscriptPendingSessionPrompt{{ID: "ask-1", State: clientui.TranscriptPromptPending, Data: clientui.TranscriptPendingSessionPromptData{Question: "Approve?"}}},
		},
	}); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	if got := surface.lastFrameSectionKinds(); len(got) == 0 {
		t.Fatal("expected live frame sections before reset")
	}

	controller.ResetForScratchHydration()
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept post-reset hydration: %v", err)
	}

	if got := surface.lastFrameSectionKinds(); len(got) != 0 {
		t.Fatalf("post-reset frame sections = %v, want none", got)
	}
}

func TestOngoingTranscriptControllerTracksPluralLiveSectionsByID(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}

	messages := []clientui.TranscriptMessage{
		{Sequence: 2, Kind: clientui.TranscriptMessageQueuedOrSteeredMessageState, QueuedOrSteeredMessageState: &clientui.TranscriptQueuedOrSteeredMessageState{QueueItemID: "11111111-1111-4111-8111-111111111111", Status: clientui.QueuedUserMessageAccepted, UserText: "first queued"}},
		{Sequence: 3, Kind: clientui.TranscriptMessageQueuedOrSteeredMessageState, QueuedOrSteeredMessageState: &clientui.TranscriptQueuedOrSteeredMessageState{QueueItemID: "44444444-4444-4444-8444-444444444444", Status: clientui.QueuedUserMessageAccepted, UserText: "second queued"}},
		{Sequence: 4, Kind: clientui.TranscriptMessagePendingSessionPrompt, PendingSessionPrompt: &clientui.TranscriptPendingSessionPrompt{ID: "ask-1", State: clientui.TranscriptPromptPending, Data: clientui.TranscriptPendingSessionPromptData{Question: "first prompt"}}},
		{Sequence: 5, Kind: clientui.TranscriptMessagePendingSessionPrompt, PendingSessionPrompt: &clientui.TranscriptPendingSessionPrompt{ID: "approval-1", State: clientui.TranscriptPromptPending, Data: clientui.TranscriptPendingSessionPromptData{Question: "second prompt"}}},
		{Sequence: 6, Kind: clientui.TranscriptMessageBackgroundActivity, BackgroundActivity: &clientui.TranscriptBackgroundActivity{ID: "22222222-2222-4222-8222-222222222222", State: "running", Preview: "first background"}},
		{Sequence: 7, Kind: clientui.TranscriptMessageBackgroundActivity, BackgroundActivity: &clientui.TranscriptBackgroundActivity{ID: "66666666-6666-4666-8666-666666666666", State: "running", Preview: "second background"}},
		{Sequence: 8, Kind: clientui.TranscriptMessageQueuedOrSteeredMessageState, QueuedOrSteeredMessageState: &clientui.TranscriptQueuedOrSteeredMessageState{QueueItemID: "11111111-1111-4111-8111-111111111111", Status: clientui.QueuedUserMessageSubmitted}},
		{Sequence: 9, Kind: clientui.TranscriptMessagePendingSessionPrompt, PendingSessionPrompt: &clientui.TranscriptPendingSessionPrompt{ID: "ask-1", State: clientui.TranscriptPromptResolved}},
		{Sequence: 10, Kind: clientui.TranscriptMessageBackgroundActivity, BackgroundActivity: &clientui.TranscriptBackgroundActivity{ID: "22222222-2222-4222-8222-222222222222", Removed: true}},
	}
	for _, message := range messages {
		if _, err := controller.Accept(message); err != nil {
			t.Fatalf("accept %s sequence %d: %v", message.Kind, message.Sequence, err)
		}
	}

	if got, want := surface.lastFrameSectionLines(ongoing.FrameSectionQueuedOrSteered), []string{"second queued"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("queued section lines = %v, want %v", got, want)
	}
	queuedLines := surface.lastFrameStyledSection(ongoing.FrameSectionQueuedOrSteered)
	if len(queuedLines) != 1 || len(queuedLines[0].Spans) == 0 {
		t.Fatalf("server steering styled lines = %+v, want one typed line", queuedLines)
	}
	queuedSpan := queuedLines[0].Spans[0]
	queuedRole, semantic := queuedSpan.Style.Role()
	if !semantic ||
		queuedRole != transcriptrender.StyleRoleNoticePrimary ||
		queuedSpan.Style.Has(transcriptrender.SpanAttributeFaint) {
		t.Fatalf("server steering span = %+v, want primary/full-strength", queuedSpan)
	}
	if got, want := surface.lastFrameSectionLines(ongoing.FrameSectionPendingPrompt), []string{"second prompt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prompt section lines = %v, want %v", got, want)
	}
	if got, want := surface.lastFrameSectionLines(ongoing.FrameSectionBackgroundActivity), []string{"$ second background • backgrounded"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("background section lines = %v, want %v", got, want)
	}
}

func TestOngoingBackgroundActivityKeepsBackgroundedSuffixAtNarrowWidth(t *testing.T) {
	const width = 24
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, func() ongoing.FrameInput {
		return ongoing.FrameInput{Size: ongoing.Size{Width: width, Height: 24}}
	})
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}

	if _, err := controller.Accept(clientui.TranscriptMessage{
		Sequence: 2,
		Kind:     clientui.TranscriptMessageBackgroundActivity,
		BackgroundActivity: &clientui.TranscriptBackgroundActivity{
			ID:      "22222222-2222-4222-8222-222222222222",
			State:   "running",
			Command: "sleep 2; echo result",
			Preview: "result",
		},
	}); err != nil {
		t.Fatalf("accept background activity: %v", err)
	}

	lines := surface.lastFrameStyledSection(ongoing.FrameSectionBackgroundActivity)
	if len(lines) != 1 || lines[0].Plain() != "$ sleep … • backgrounded" {
		t.Fatalf("background activity lines = %+v", lines)
	}
	if lines[0].LeadingSymbol == nil {
		t.Fatal("background activity line has no symbol")
	}
	if role, semantic := lines[0].LeadingSymbol.Style.Role(); !semantic ||
		role != transcriptrender.StyleRoleToolShellSecondary {
		t.Fatalf("background activity symbol = %+v, want secondary shell symbol", lines[0].LeadingSymbol)
	}
	for _, span := range lines[0].Spans {
		role, semantic := span.Style.Role()
		if !semantic ||
			(role != transcriptrender.StyleRoleToolShell && role != transcriptrender.StyleRoleNoticeForegroundFaint) ||
			!span.Style.Has(transcriptrender.SpanAttributeFaint) {
			t.Fatalf("background activity span = %+v, want faint foreground", span)
		}
	}
}

func TestOngoingTranscriptControllerRejectsInvalidLiveItemIDs(t *testing.T) {
	tests := []struct {
		name    string
		message clientui.TranscriptMessage
	}{
		{
			name: "queued queue item",
			message: clientui.TranscriptMessage{
				Sequence: 2,
				Kind:     clientui.TranscriptMessageQueuedOrSteeredMessageState,
				QueuedOrSteeredMessageState: &clientui.TranscriptQueuedOrSteeredMessageState{
					QueueItemID: "queued-1",
					Status:      clientui.QueuedUserMessageAccepted,
					UserText:    "queued",
				},
			},
		},
		{
			name: "queued client request",
			message: clientui.TranscriptMessage{
				Sequence: 2,
				Kind:     clientui.TranscriptMessageQueuedOrSteeredMessageState,
				QueuedOrSteeredMessageState: &clientui.TranscriptQueuedOrSteeredMessageState{
					ClientRequestID: "11111111-1111-1111-8111-111111111111",
					Status:          clientui.QueuedUserMessageAccepted,
					UserText:        "queued",
				},
			},
		},
		{
			name: "background activity",
			message: clientui.TranscriptMessage{
				Sequence: 2,
				Kind:     clientui.TranscriptMessageBackgroundActivity,
				BackgroundActivity: &clientui.TranscriptBackgroundActivity{
					ID:      "background-1",
					State:   "running",
					Preview: "tests",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			surface := &ongoingSurfaceSpy{}
			controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
			if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
				t.Fatalf("accept hydration: %v", err)
			}

			assertPanic(t, func() {
				_, _ = controller.Accept(tt.message)
			})
		})
	}
}

func TestOngoingTranscriptControllerKeysPendingPromptsByPromptControlID(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}

	messages := []clientui.TranscriptMessage{
		{Sequence: 2, Kind: clientui.TranscriptMessagePendingSessionPrompt, PendingSessionPrompt: &clientui.TranscriptPendingSessionPrompt{ID: "ask-1", State: clientui.TranscriptPromptPending, Data: clientui.TranscriptPendingSessionPromptData{Question: "first prompt"}}},
		{Sequence: 3, Kind: clientui.TranscriptMessagePendingSessionPrompt, PendingSessionPrompt: &clientui.TranscriptPendingSessionPrompt{ID: "approval-1", State: clientui.TranscriptPromptPending, Data: clientui.TranscriptPendingSessionPromptData{Question: "second prompt"}}},
		{Sequence: 4, Kind: clientui.TranscriptMessagePendingSessionPrompt, PendingSessionPrompt: &clientui.TranscriptPendingSessionPrompt{ID: "ask-1", State: clientui.TranscriptPromptResolved}},
	}
	for _, message := range messages {
		if _, err := controller.Accept(message); err != nil {
			t.Fatalf("accept prompt %s: %v", message.PendingSessionPrompt.ID, err)
		}
	}

	if got, want := surface.lastFrameSectionLines(ongoing.FrameSectionPendingPrompt), []string{"second prompt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prompt section lines = %v, want %v", got, want)
	}
}

func TestOngoingTranscriptControllerConstrainsPluralLiveSectionsToFrameWidth(t *testing.T) {
	const width = 12
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, func() ongoing.FrameInput {
		return ongoing.FrameInput{Size: ongoing.Size{Width: width, Height: 24}}
	})
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}

	messages := []clientui.TranscriptMessage{
		{Sequence: 2, Kind: clientui.TranscriptMessageQueuedOrSteeredMessageState, QueuedOrSteeredMessageState: &clientui.TranscriptQueuedOrSteeredMessageState{QueueItemID: "11111111-1111-4111-8111-111111111111", Status: clientui.QueuedUserMessageAccepted, UserText: "queued text that must not wrap in the native live band"}},
		{Sequence: 3, Kind: clientui.TranscriptMessagePendingSessionPrompt, PendingSessionPrompt: &clientui.TranscriptPendingSessionPrompt{ID: "ask-1", State: clientui.TranscriptPromptPending, Data: clientui.TranscriptPendingSessionPromptData{Question: "pending prompt that must not wrap in the native live band"}}},
		{Sequence: 4, Kind: clientui.TranscriptMessageBackgroundActivity, BackgroundActivity: &clientui.TranscriptBackgroundActivity{ID: "22222222-2222-4222-8222-222222222222", State: "running", Preview: "background activity that must not wrap in the native live band"}},
	}
	for _, message := range messages {
		if _, err := controller.Accept(message); err != nil {
			t.Fatalf("accept %s: %v", message.Kind, err)
		}
	}

	for _, kind := range []ongoing.FrameSectionKind{
		ongoing.FrameSectionQueuedOrSteered,
		ongoing.FrameSectionPendingPrompt,
		ongoing.FrameSectionBackgroundActivity,
	} {
		for _, line := range surface.lastFrameSectionLines(kind) {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("section %s line width = %d for %q, want <= %d", kind, got, line, width)
			}
		}
	}
}

func assertPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	fn()
}
