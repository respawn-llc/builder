package app

import (
	"reflect"
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/clientui"
)

func TestPendingToolStartAndAbortUseAppComposedFrameOnly(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	surface.calls = nil

	if _, err := controller.Accept(ongoingTranscriptMessage(2, clientui.TranscriptMessageToolStart)); err != nil {
		t.Fatalf("accept tool start: %v", err)
	}
	if got, want := surface.callKinds(), []string{"render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool start calls = %v, want %v", got, want)
	}
	if got, want := surface.lastFrameSectionKinds(), []ongoing.FrameSectionKind{ongoing.FrameSectionPendingTools}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool start sections = %v, want %v", got, want)
	}
	surface.calls = nil

	if _, err := controller.Accept(ongoingTranscriptMessage(3, clientui.TranscriptMessageToolAbort)); err != nil {
		t.Fatalf("accept tool abort: %v", err)
	}
	if got, want := surface.callKinds(), []string{"render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool abort calls = %v, want %v", got, want)
	}
	if got := surface.lastFrameSectionKinds(); len(got) != 0 {
		t.Fatalf("tool abort sections = %v, want no pending tool section", got)
	}
}

func TestCommittedToolRowAppendsImmediatelyAndRemovesPendingToolInSameEvent(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	if _, err := controller.Accept(ongoingTranscriptMessage(2, clientui.TranscriptMessageToolStart)); err != nil {
		t.Fatalf("accept tool start: %v", err)
	}
	surface.calls = nil

	row := clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowTool,
		Tool: &clientui.TranscriptToolRow{ToolCallID: "tool-1", ToolName: "shell", Text: "done"},
	}
	if _, err := controller.Accept(clientui.TranscriptMessage{
		Sequence:     3,
		Kind:         clientui.TranscriptMessageCommittedRow,
		CommittedRow: &row,
	}); err != nil {
		t.Fatalf("accept committed tool row: %v", err)
	}

	if got, want := surface.callKinds(), []string{"apply"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("committed tool calls = %v, want %v", got, want)
	}
	if got, want := surface.appliedKinds(), []clientui.TranscriptMessageKind{clientui.TranscriptMessageCommittedRow}; !reflect.DeepEqual(got, want) {
		t.Fatalf("applied kinds = %v, want %v", got, want)
	}
	if got := surface.lastFrameSectionKinds(); len(got) != 0 {
		t.Fatalf("committed tool frame sections = %v, want pending tool removed before append", got)
	}
}

func TestPendingToolsRenderInServerArrivalOrder(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}

	starts := []clientui.TranscriptMessage{
		{
			Sequence: 2,
			Kind:     clientui.TranscriptMessageToolStart,
			ToolStart: &clientui.TranscriptToolStart{
				ToolCallID: "tool-a",
				ToolName:   "alpha",
			},
		},
		{
			Sequence: 3,
			Kind:     clientui.TranscriptMessageToolStart,
			ToolStart: &clientui.TranscriptToolStart{
				ToolCallID: "tool-b",
				ToolName:   "beta",
			},
		},
	}
	for _, message := range starts {
		if _, err := controller.Accept(message); err != nil {
			t.Fatalf("accept tool start: %v", err)
		}
	}
	if got, want := surface.lastFrameSectionLines(ongoing.FrameSectionPendingTools), []string{"⢎  alpha", "⢎  beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pending tool lines = %v, want %v", got, want)
	}

	if _, err := controller.Accept(clientui.TranscriptMessage{
		Sequence: 4,
		Kind:     clientui.TranscriptMessageToolAbort,
		ToolAbort: &clientui.TranscriptToolAbort{
			ToolCallID: "tool-a",
		},
	}); err != nil {
		t.Fatalf("accept tool abort: %v", err)
	}
	if got, want := surface.lastFrameSectionLines(ongoing.FrameSectionPendingTools), []string{"⢎  beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pending tool lines after abort = %v, want %v", got, want)
	}
}

func TestPendingToolStartUsesPresentationMetadata(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}

	if _, err := controller.Accept(clientui.TranscriptMessage{
		Sequence: 2,
		Kind:     clientui.TranscriptMessageToolStart,
		ToolStart: &clientui.TranscriptToolStart{
			ToolCallID: "77777777-7777-4777-8777-777777777777",
			ToolName:   "exec_command",
			ToolPresentation: &clientui.ToolCallMeta{
				ToolName:     "exec_command",
				Presentation: clientui.ToolPresentationShell,
				Command:      "go test ./cli/app",
			},
		},
	}); err != nil {
		t.Fatalf("accept tool start: %v", err)
	}

	if got, want := surface.lastFrameSectionLines(ongoing.FrameSectionPendingTools), []string{"⢎  go test ./cli/app"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pending tool lines = %v, want %v", got, want)
	}
}
