package app

import (
	"reflect"
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/clientui"
)

func TestOngoingFrameInputUsesOperatorLocalSectionsAndCursor(t *testing.T) {
	m := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIPromptHistory([]string{"older", "newer"}),
	), 48, 10)
	m.input = "hello"
	m.inputCursor = 2
	m.promptHistorySelection = 1
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
	m.runtimeActivityProjection = clientui.RuntimeActivity{RunID: "runtime-copy"}
	m.runtimeContextUsage = clientui.RuntimeContextUsage{UsedTokens: 123, WindowTokens: 456}

	frame := m.ongoingFrameInput()

	for _, kind := range frameSectionKinds(frame) {
		switch kind {
		case ongoing.FrameSectionRuntimeActivity, ongoing.FrameSectionContextUsage, ongoing.FrameSectionGoal, ongoing.FrameSectionPendingPrompt:
			t.Fatalf("frame section %s came from non-transcript runtime-main-view/session-activity state", kind)
		}
	}
}

func TestOngoingTranscriptControllerPlacesCursorAfterPrependedLiveSections(t *testing.T) {
	m := sizedTestUIModel(newProjectedStaticUIModel(), 48, 10)
	m.input = "hello"
	m.inputCursor = 2
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, m.ongoingFrameInput)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}

	if _, err := controller.Accept(clientui.TranscriptMessage{
		Sequence: 2,
		Kind:     clientui.TranscriptMessageQueuedOrSteeredMessageState,
		QueuedOrSteeredMessageState: &clientui.TranscriptQueuedOrSteeredMessageState{
			QueueItemID: "11111111-1111-4111-8111-111111111111",
			Status:      clientui.QueuedUserMessageAccepted,
			UserText:    "queued prompt",
		},
	}); err != nil {
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
}

func frameSectionKinds(frame ongoing.FrameInput) []ongoing.FrameSectionKind {
	kinds := make([]ongoing.FrameSectionKind, 0, len(frame.Sections))
	for _, section := range frame.Sections {
		kinds = append(kinds, section.Kind)
	}
	return kinds
}
