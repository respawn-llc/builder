package app

import (
	"reflect"
	"strings"
	"testing"

	"core/cli/tui/ongoing"
	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/runtimeinput"
)

func TestOngoingFrameInputUsesOperatorLocalSectionsAndCursor(t *testing.T) {
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUITerminalCursorState(newUITerminalCursorState()),
		WithUIPromptHistory([]string{"older", "newer"}),
	), 48, 10)
	testSetMainInputAtRuneCursor(m, "hello", 2)
	testSetPromptHistorySelection(m, 1)
	m.helpVisible = true

	frame := m.ongoingFrameInput()

	if got, want := frame.Size, (ongoing.Size{Width: 48, Height: 10}); got != want {
		t.Fatalf("frame size = %+v, want %+v", got, want)
	}
	inputStart, inputEnd, ok := ongoingFrameSectionTerminalRows(frame, ongoing.FrameSectionInput)
	if !ok {
		t.Fatal("input section missing from ongoing frame")
	}
	statusStart, _, ok := ongoingFrameSectionTerminalRows(frame, ongoing.FrameSectionStatus)
	if !ok {
		t.Fatal("status section missing from ongoing frame")
	}
	if !frame.Cursor.Visible || frame.Cursor.Row < inputStart || frame.Cursor.Row > inputEnd {
		t.Fatalf("frame cursor = %+v, want visible within input rows %d..%d", frame.Cursor, inputStart, inputEnd)
	}
	if frame.Cursor.Target == nil || frame.Cursor.Target.SectionKind != ongoing.FrameSectionInput {
		t.Fatalf("frame cursor target = %+v, want input section target", frame.Cursor.Target)
	}
	if frame.Cursor.Row >= statusStart {
		t.Fatalf("frame cursor row = %d, want before status row %d", frame.Cursor.Row, statusStart)
	}
	if got, want := frame.Cursor.Target.Row, 2; got != want {
		t.Fatalf("input cursor target row = %d, want framed content row %d", got, want)
	}
	if got, want := frame.Cursor.Row, inputStart+1; got != want {
		t.Fatalf("terminal cursor row = %d, want first framed input content row %d", got, want)
	}
	wantKinds := []ongoing.FrameSectionKind{
		ongoing.FrameSectionHelp,
		ongoing.FrameSectionInput,
		ongoing.FrameSectionPromptHistory,
		ongoing.FrameSectionStatus,
	}
	if got := frameSectionKinds(frame); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("frame section kinds = %v, want %v", got, wantKinds)
	}
}

func TestOngoingFrameInputIgnoresRuntimeMainViewCopiesOfTranscriptOwnedFacts(t *testing.T) {
	m := sizedTestUIModel(newProjectedStaticUIModel(), 48, 10)
	m.runtimeActivityProjection = clientui.RuntimeActivity{
		State:    clientui.RuntimeActivityRegisteredIdle,
		Reviewer: clientui.ReviewerActivityInactive,
	}
	m.runtimeContextUsage = clientui.RuntimeContextUsage{UsedTokens: 123, WindowTokens: 456}

	frame := m.ongoingFrameInput()

	for _, kind := range frameSectionKinds(frame) {
		switch kind {
		case ongoing.FrameSectionRuntimeActivity, ongoing.FrameSectionContextUsage, ongoing.FrameSectionGoal, ongoing.FrameSectionPendingPrompt:
			t.Fatalf("frame section %s came from non-transcript runtime-main-view/session-activity state", kind)
		}
	}
}

func TestOngoingFrameInputRendersAvailabilityOnlyGoalProjection(t *testing.T) {
	availability := clientui.GoalAvailabilityAvailable
	client := &runtimeControlFakeClient{
		cachedMainView: clientui.RuntimeMainView{
			Status: clientui.RuntimeStatus{
				Goal: &clientui.RuntimeGoal{Availability: &availability},
			},
		},
		hasCachedMainView: true,
	}
	m := sizedTestUIModel(newProjectedTestUIModel(client), 48, 10)

	m.ongoingFrameInput()
}

func TestOngoingTranscriptControllerPlacesCursorAfterPrependedLiveSections(t *testing.T) {
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUITerminalCursorState(newUITerminalCursorState()),
	), 48, 10)
	testSetMainInputAtRuneCursor(m, "hello", 2)
	surface := &ongoingSurfaceSpy{}
	controller := newTestOngoingTranscriptController(surface, m.ongoingFrameInput)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	if _, err := controller.Accept(ongoingTranscriptMessage(2, clientui.TranscriptMessagePendingWorkReplaced)); err != nil {
		t.Fatalf("accept queued message: %v", err)
	}

	frame := surface.calls[len(surface.calls)-1].frame
	inputStart, inputEnd, ok := ongoingFrameSectionTerminalRows(frame, ongoing.FrameSectionInput)
	if !ok {
		t.Fatal("input section missing from final ongoing frame")
	}
	if !frame.Cursor.Visible || frame.Cursor.Row < inputStart || frame.Cursor.Row > inputEnd {
		t.Fatalf("final frame cursor = %+v, want within input rows %d..%d", frame.Cursor, inputStart, inputEnd)
	}
	if got, want := frame.Cursor.Row, inputStart+1; got != want {
		t.Fatalf("final frame cursor row = %d, want first framed input content row %d", got, want)
	}
}

func TestOngoingTranscriptControllerPreservesWrappedDisplayCursorTargetWithPrependedSections(t *testing.T) {
	const width = 24
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUITerminalCursorState(newUITerminalCursorState()),
	), width, 12)
	testSetMainInput(m, strings.Repeat("界", 20)+" tail")
	projected := m.layout().inputPaneProjection(width, m.layout().effectiveHeight(), uiThemeStyles(m.theme)).Cursor
	if !projected.Visible {
		t.Fatal("shared editor cursor projection is absent")
	}

	surface := &ongoingSurfaceSpy{}
	controller := newTestOngoingTranscriptController(surface, m.ongoingFrameInput)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	if _, err := controller.Accept(ongoingTranscriptMessage(2, clientui.TranscriptMessagePendingWorkReplaced)); err != nil {
		t.Fatalf("accept queued message: %v", err)
	}

	frame := surface.calls[len(surface.calls)-1].frame
	_, queuedEnd, ok := ongoingFrameSectionTerminalRows(frame, ongoing.FrameSectionQueuedOrSteered)
	if !ok {
		t.Fatal("prepended queued section missing from final ongoing frame")
	}
	inputStart, inputEnd, ok := ongoingFrameSectionTerminalRows(frame, ongoing.FrameSectionInput)
	if !ok {
		t.Fatal("input section missing from final ongoing frame")
	}
	if queuedEnd >= inputStart {
		t.Fatalf("queued section ends at row %d, want before input start %d", queuedEnd, inputStart)
	}
	if frame.Cursor.Target == nil || frame.Cursor.Target.SectionKind != ongoing.FrameSectionInput {
		t.Fatalf("frame cursor target = %+v, want input section", frame.Cursor.Target)
	}
	if got, want := frame.Cursor.Target.Row, projected.Row; got != want {
		t.Fatalf("frame cursor target row = %d, want shared editor row %d", got, want)
	}
	if got, want := frame.Cursor.Column, projected.Col+1; got != want {
		t.Fatalf("frame cursor column = %d, want shared editor column %d", got, want)
	}
	if frame.Cursor.Column < 1 || frame.Cursor.Column > frame.Size.Width {
		t.Fatalf("frame cursor column %d outside width %d", frame.Cursor.Column, frame.Size.Width)
	}
	if got, want := frame.Cursor.Row, inputStart+projected.Row-1; got != want {
		t.Fatalf("frame cursor row = %d, want targeted input row %d", got, want)
	}
	if frame.Cursor.Row < inputStart || frame.Cursor.Row > inputEnd {
		t.Fatalf("frame cursor row %d outside input rows %d..%d", frame.Cursor.Row, inputStart, inputEnd)
	}
}

func TestOngoingFrameInputAskViewportAndCursorShareBoundedProjection(t *testing.T) {
	const (
		width  = 24
		height = 10
	)
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUITerminalCursorState(newUITerminalCursorState()),
	), width, height)
	event := testQuestionAskEvent("ask-1", "Question source", "First", "Second")
	testSetActiveAsk(m, &event)
	m.ask.activeProjection.rows = []string{
		"question row one",
		"question row two",
		"question row three",
		"question row four",
		"question row five",
	}
	m.ask.freeform = true
	testSetAskInputAtRuneCursor(m, "draft text", 5)

	pane := m.layout().inputPaneProjection(width, height, uiThemeStyles(m.theme))
	if !pane.Cursor.Visible {
		t.Fatal("bounded ask pane did not expose the editor cursor")
	}
	frame := m.ongoingFrameInput()
	input, ok := frameSection(frame, ongoing.FrameSectionInput)
	if !ok {
		t.Fatal("bounded ask input section missing from ongoing frame")
	}
	if got, want := input.Lines, pane.Lines; !reflect.DeepEqual(got, want) {
		t.Fatalf("frame input rows differ from bounded pane projection: got %d want %d", len(got), len(want))
	}
	if len(input.Lines) > inputContentLineLimit(height)+2 {
		t.Fatalf("framed ask rows = %d, bounded maximum = %d", len(input.Lines), inputContentLineLimit(height)+2)
	}
	if frame.Cursor.Target == nil || frame.Cursor.Target.SectionKind != ongoing.FrameSectionInput {
		t.Fatalf("frame cursor target = %+v, want input section", frame.Cursor.Target)
	}
	if got, want := frame.Cursor.Target.Row, pane.Cursor.Row; got != want {
		t.Fatalf("frame cursor target row = %d, want bounded pane row %d", got, want)
	}
	inputStart, inputEnd, ok := ongoingFrameSectionTerminalRows(frame, ongoing.FrameSectionInput)
	if !ok {
		t.Fatal("bounded ask input section has no terminal rows")
	}
	if got, want := frame.Cursor.Row, inputStart+pane.Cursor.Row-1; got != want {
		t.Fatalf("frame cursor terminal row = %d, want %d", got, want)
	}
	if frame.Cursor.Row < inputStart || frame.Cursor.Row > inputEnd {
		t.Fatalf("frame cursor row %d outside bounded input rows %d..%d", frame.Cursor.Row, inputStart, inputEnd)
	}
}

func TestOngoingFrameInputKeepsServerBackedQueuedStateTranscriptOwned(t *testing.T) {
	m := sizedTestUIModel(newProjectedTestUIModel(&runtimeControlFakeClient{}), 48, 10)
	m.injectedQueue = []injectedRuntimeQueueItem{{
		LocalID:  "22222222-2222-4222-8222-222222222222",
		ServerID: "11111111-1111-4111-8111-111111111111",
		Text:     "server accepted",
		State:    injectedRuntimeQueueEnqueued,
	}}

	frame := m.ongoingFrameInput()

	if section, ok := frameSection(frame, ongoing.FrameSectionQueuedOrSteered); ok {
		t.Fatalf("legacy queued section = %+v, want absent for server-backed accepted items", section)
	}
}

func TestOngoingFrameInputStillRendersClientLocalQueuedMessages(t *testing.T) {
	m := sizedTestUIModel(newProjectedStaticUIModel(), 48, 10)
	m.queueInput("queued before server acceptance")
	m.pendingWork = runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{pendingWorkMessageForTest(runtimeinput.PendingWorkLaneSteer, "server pending")}}

	frame := m.ongoingFrameInput()

	section, ok := frameSection(frame, ongoing.FrameSectionQueuedOrSteered)
	if !ok {
		t.Fatal("local queued section missing")
	}
	if len(section.StyledLines) != 2 || len(section.StyledLines[0].Spans) == 0 {
		t.Fatalf("merged queued styled lines = %+v, want server and local lines", section.StyledLines)
	}
	span := section.StyledLines[0].Spans[0]
	role, semantic := span.Style.Role()
	if !semantic ||
		role != transcriptrender.StyleRoleNoticeSecondary ||
		!span.Style.Has(transcriptrender.SpanAttributeFaint) {
		t.Fatalf("local queued span = %+v, want secondary/faint", span)
	}
}

func TestOngoingFrameInputRendersPendingInjectedMessagesBeforeServerAcceptance(t *testing.T) {
	m := sizedTestUIModel(newProjectedStaticUIModel(), 48, 10)
	m.injectedQueue = []injectedRuntimeQueueItem{{
		LocalID: "11111111-1111-4111-8111-111111111111",
		Text:    "pending injected before server acceptance",
		State:   injectedRuntimeQueuePendingCreate,
	}}
	frame := m.ongoingFrameInput()

	section, ok := frameSection(frame, ongoing.FrameSectionQueuedOrSteered)
	if !ok {
		t.Fatal("pending injected section missing")
	}
	if len(section.StyledLines) != 1 || len(section.StyledLines[0].Spans) == 0 {
		t.Fatalf("pending injected styled lines = %+v, want one typed line", section.StyledLines)
	}
	span := section.StyledLines[0].Spans[0]
	role, semantic := span.Style.Role()
	if !semantic ||
		role != transcriptrender.StyleRoleNoticePrimary ||
		span.Style.Has(transcriptrender.SpanAttributeFaint) {
		t.Fatalf("pending injected span = %+v, want primary/full-strength", span)
	}
}

func TestOngoingFrameInputRendersNoRuntimeInjectedMessages(t *testing.T) {
	m := sizedTestUIModel(newProjectedStaticUIModel(), 48, 10)
	m.injectedQueue = []injectedRuntimeQueueItem{{
		LocalID:  "11111111-1111-4111-8111-111111111111",
		ServerID: "11111111-1111-4111-8111-111111111111",
		Text:     "local injected without runtime client",
		State:    injectedRuntimeQueueEnqueued,
	}}
	frame := m.ongoingFrameInput()

	section, ok := frameSection(frame, ongoing.FrameSectionQueuedOrSteered)
	if !ok {
		t.Fatal("no-runtime injected section missing")
	}
	if len(section.StyledLines) != 1 || len(section.StyledLines[0].Spans) == 0 {
		t.Fatalf("no-runtime injected styled lines = %+v, want one typed line", section.StyledLines)
	}
	span := section.StyledLines[0].Spans[0]
	role, semantic := span.Style.Role()
	if !semantic ||
		role != transcriptrender.StyleRoleNoticePrimary ||
		span.Style.Has(transcriptrender.SpanAttributeFaint) {
		t.Fatalf("no-runtime injected span = %+v, want primary/full-strength", span)
	}
}

func TestOngoingFrameInputSanitizesPromptHistorySection(t *testing.T) {
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIPromptHistory([]string{"alpha\nbeta\tgamma\x1b"}),
	), 48, 10)
	testSetPromptHistorySelection(m, 0)

	frame := m.ongoingFrameInput()

	section, ok := frameSection(frame, ongoing.FrameSectionPromptHistory)
	if !ok {
		t.Fatal("prompt history section missing")
	}
	if got, want := section.Lines, []string{"alpha beta gamma"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prompt history lines = %q, want %q", got, want)
	}
}

func frameSection(frame ongoing.FrameInput, kind ongoing.FrameSectionKind) (ongoing.FrameSection, bool) {
	for _, section := range frame.Sections {
		if section.Kind == kind {
			return section, true
		}
	}
	return ongoing.FrameSection{}, false
}

func frameSectionKinds(frame ongoing.FrameInput) []ongoing.FrameSectionKind {
	kinds := make([]ongoing.FrameSectionKind, 0, len(frame.Sections))
	for _, section := range frame.Sections {
		kinds = append(kinds, section.Kind)
	}
	return kinds
}
