package app

import (
	"bytes"
	"context"
	"testing"

	"core/cli/tui/ongoing"
	"core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/appfixture"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

type ptyCheckpointOrderingModel struct {
	output *analyzer.Writer
}

func (model ptyCheckpointOrderingModel) Init() tea.Cmd { return nil }

func (model ptyCheckpointOrderingModel) Update(tea.Msg) (tea.Model, tea.Cmd) {
	if _, err := model.output.Write([]byte("updated|")); err != nil {
		panic(err)
	}
	return model, nil
}

func (model ptyCheckpointOrderingModel) View() string { return "" }

func TestPTYCheckpointModelEmitsInputAppliedAfterInnerUpdate(t *testing.T) {
	var out bytes.Buffer
	writer := analyzer.NewWriter(&out)
	model := newPTYCheckpointModel(
		ptyCheckpointOrderingModel{output: writer},
		writer,
		newPTYCheckpointScenarioState(appfixture.ScriptFinalAssistantOrdinal(1)),
	)

	model.Update(tea.KeyMsg{Type: tea.KeyDown})

	analysis := analyzeCheckpointBytes(t, out.Bytes())
	if got := analysis.Screen.TextInRegion(analyzer.Region{Top: 0, Bottom: 1, Left: 0, Right: 8}); got != "updated|" {
		t.Fatalf("screen text = %q, want update output before checkpoint", got)
	}
	if len(analysis.PhaseEvents) != 1 || analysis.PhaseEvents[0].Phase != analyzer.PhaseInputApplied {
		t.Fatalf("checkpoint events = %#v, want one input-applied event", analysis.PhaseEvents)
	}
	if analysis.PhaseEvents[0].ByteRange.Start < int64(len("updated|")) {
		t.Fatalf("input-applied checkpoint range = %+v, want after inner update output", analysis.PhaseEvents[0].ByteRange)
	}
}

func TestPTYCheckpointModelQueuesInitialDetailApplicationBeforeRendererWrite(t *testing.T) {
	var out bytes.Buffer
	writer := analyzer.NewWriter(&out)
	model, requestID := newPendingPTYDetailCheckpointModel(t)
	wrapped := newPTYCheckpointModel(
		model,
		writer,
		newPTYCheckpointScenarioState(appfixture.ScriptFinalAssistantOrdinal(1)),
	)

	wrapped.Update(detailTranscriptLoadMsg{
		requestID: requestID,
		page: clientui.TranscriptPage{
			SessionID: detailTestSessionID,
			Entries:   []clientui.TranscriptCommittedRow{detailTestAssistantRow("hydrated row")},
		},
	})
	view := wrapped.View()
	if view == "" {
		t.Fatal("detail application produced an empty view")
	}
	if _, err := writer.Write([]byte(view)); err != nil {
		t.Fatalf("write rendered view: %v", err)
	}

	analysis := analyzeCheckpointBytes(t, out.Bytes())
	if len(analysis.PhaseEvents) != 1 || analysis.PhaseEvents[0].Phase != analyzer.PhaseDetailInitialPageApplied {
		t.Fatalf("checkpoint events = %#v, want one detail-initial-page-applied event", analysis.PhaseEvents)
	}
	if analysis.PhaseEvents[0].ByteRange.Start != 0 {
		t.Fatalf("detail checkpoint starts at byte %d, want immediately before renderer output", analysis.PhaseEvents[0].ByteRange.Start)
	}
	if analysis.PhaseEvents[0].ByteRange.End >= int64(len(out.Bytes())) {
		t.Fatal("detail checkpoint was not followed by renderer output")
	}
}

func TestPTYCheckpointModelDoesNotQueueInitialDetailApplicationForMalformedPage(t *testing.T) {
	var out bytes.Buffer
	writer := analyzer.NewWriter(&out)
	model, requestID := newPendingPTYDetailCheckpointModel(t)
	wrapped := newPTYCheckpointModel(
		model,
		writer,
		newPTYCheckpointScenarioState(appfixture.ScriptFinalAssistantOrdinal(1)),
	)

	wrapped.Update(detailTranscriptLoadMsg{
		requestID: requestID,
		page: clientui.TranscriptPage{
			SessionID: detailTestSessionID,
			Entries: []clientui.TranscriptCommittedRow{{
				Visibility: clientui.EntryVisibilityAuto,
				Kind:       clientui.TranscriptRowUser,
				User:       &clientui.TranscriptUserRow{Text: "malformed"},
			}},
		},
	})
	view := wrapped.View()
	if view == "" {
		t.Fatal("rejected detail page produced an empty error-state view")
	}
	if _, err := writer.Write([]byte(view)); err != nil {
		t.Fatalf("write rejected-page view: %v", err)
	}

	analysis := analyzeCheckpointBytes(t, out.Bytes())
	for _, event := range analysis.PhaseEvents {
		if event.Phase == analyzer.PhaseDetailInitialPageApplied {
			t.Fatalf("malformed detail page emitted page-applied checkpoint: %#v", analysis.PhaseEvents)
		}
	}
	if model.pendingDetailTranscript != nil || model.detailTranscript.loaded {
		t.Fatalf(
			"malformed detail page changed request state: pending=%#v loaded=%t",
			model.pendingDetailTranscript,
			model.detailTranscript.loaded,
		)
	}
}

func TestPTYCheckpointModelEmitsScenarioFinalAppliedAfterTerminalTransaction(t *testing.T) {
	var out bytes.Buffer
	writer := analyzer.NewWriter(&out)
	model := newPTYCheckpointOngoingModel(t, writer)
	out.Reset()

	scenario := newPTYCheckpointScenarioState(appfixture.ScriptFinalAssistantOrdinal(1))
	scenario.markScenarioComplete()
	wrapped := newPTYCheckpointModel(model, writer, scenario)
	wrapped.Update(ongoingTranscriptEvent{
		Kind: ongoingTranscriptEventMessage,
		Message: clientui.TranscriptMessage{
			Sequence: 2,
			Kind:     clientui.TranscriptMessageCommittedRow,
			CommittedRow: &clientui.TranscriptCommittedRow{
				Visibility: clientui.EntryVisibilityOngoing,
				Kind:       clientui.TranscriptRowAssistant,
				Assistant: &clientui.TranscriptAssistantRow{
					Text:  "final response",
					Phase: transcript.AssistantPhaseFinal,
				},
			},
		},
	})

	analysis := analyzeCheckpointBytes(t, out.Bytes())
	if len(analysis.PhaseEvents) != 1 ||
		analysis.PhaseEvents[0].Phase != analyzer.PhaseScenarioFinalApplied {
		t.Fatalf("checkpoint events = %#v, want one scenario-final-applied event", analysis.PhaseEvents)
	}
	lastTerminalOperationEnd := ptyLastTerminalOperationEnd(analysis)
	if analysis.PhaseEvents[0].ByteRange.Start < lastTerminalOperationEnd {
		t.Fatalf(
			"scenario-final checkpoint starts at byte %d before terminal transaction ended at %d",
			analysis.PhaseEvents[0].ByteRange.Start,
			lastTerminalOperationEnd,
		)
	}
}

func TestPTYCheckpointModelEmitsScenarioFinalAppliedAfterDeferredDetailTransaction(t *testing.T) {
	var out bytes.Buffer
	writer := analyzer.NewWriter(&out)
	model := newPTYCheckpointOngoingModel(t, writer)
	if _, err := model.ongoingTranscript.SetNormalBufferOwned(false); err != nil {
		t.Fatalf("mark normal buffer unowned: %v", err)
	}
	model.activeSurface = uiSurfaceTranscriptDetail
	_ = model.reconcileOngoingOwnership()
	out.Reset()

	scenario := newPTYCheckpointScenarioState(appfixture.ScriptFinalAssistantOrdinal(1))
	scenario.markScenarioComplete()
	wrapped := newPTYCheckpointModel(model, writer, scenario)
	applyPTYCheckpointAssistantFinal(wrapped, 2, "deferred target final")
	if events := analyzeCheckpointBytes(t, out.Bytes()).PhaseEvents; len(events) != 0 {
		t.Fatalf("phase events before normal-buffer restore = %#v, want none", events)
	}
	if out.Len() != 0 {
		t.Fatalf("deferred target final wrote terminal bytes before restore: %q", out.String())
	}

	model.activeSurface = uiSurfaceOngoingTranscript
	wrapped.Update(ongoingNormalBufferOwnedMsg{owned: true})

	analysis := analyzeCheckpointBytes(t, out.Bytes())
	if len(analysis.PhaseEvents) != 1 ||
		analysis.PhaseEvents[0].Phase != analyzer.PhaseScenarioFinalApplied {
		t.Fatalf("phase events after deferred transaction = %#v, want one scenario-final-applied event", analysis.PhaseEvents)
	}
	lastTerminalOperationEnd := ptyLastTerminalOperationEnd(analysis)
	if analysis.PhaseEvents[0].ByteRange.Start < lastTerminalOperationEnd {
		t.Fatalf(
			"deferred scenario-final checkpoint starts at byte %d before terminal transaction ended at %d",
			analysis.PhaseEvents[0].ByteRange.Start,
			lastTerminalOperationEnd,
		)
	}
}

func TestPTYCheckpointModelCorrelatesScenarioFinalAppliedToTargetScriptFinalExactlyOnce(t *testing.T) {
	var out bytes.Buffer
	writer := analyzer.NewWriter(&out)
	model := newPTYCheckpointOngoingModel(t, writer)
	out.Reset()

	scenario := newPTYCheckpointScenarioState(appfixture.ScriptFinalAssistantOrdinal(2))
	scenario.markScenarioComplete()
	wrapped := newPTYCheckpointModel(model, writer, scenario)

	applyPTYCheckpointAssistantFinal(wrapped, 2, "delayed earlier final")
	if events := analyzeCheckpointBytes(t, out.Bytes()).PhaseEvents; len(events) != 0 {
		t.Fatalf("phase events after earlier final = %#v, want none before target final", events)
	}

	applyPTYCheckpointAssistantFinal(wrapped, 3, "target final")
	analysis := analyzeCheckpointBytes(t, out.Bytes())
	if len(analysis.PhaseEvents) != 1 ||
		analysis.PhaseEvents[0].Phase != analyzer.PhaseScenarioFinalApplied {
		t.Fatalf("phase events after target final = %#v, want one scenario-final-applied event", analysis.PhaseEvents)
	}

	applyPTYCheckpointAssistantFinal(wrapped, 4, "unexpected later final")
	analysis = analyzeCheckpointBytes(t, out.Bytes())
	if len(analysis.PhaseEvents) != 1 ||
		analysis.PhaseEvents[0].Phase != analyzer.PhaseScenarioFinalApplied {
		t.Fatalf("phase events after later final = %#v, want exactly one scenario-final-applied event", analysis.PhaseEvents)
	}
}

func newPTYCheckpointOngoingModel(t *testing.T, writer *analyzer.Writer) *uiModel {
	t.Helper()
	surface := ongoing.NewSurface(writer)
	model := sizedTestUIModel(newProjectedStaticUIModel(
		WithUIOngoingSurface(surface),
	), 40, 10)
	model.ongoingTranscript = newOngoingTranscriptController(surface, model.ongoingFrameInput)
	if _, err := model.ongoingTranscript.Accept(clientui.TranscriptMessage{
		Sequence:  1,
		Kind:      clientui.TranscriptMessageHydration,
		Hydration: &clientui.TranscriptHydration{},
	}); err != nil {
		t.Fatalf("accept initial hydration: %v", err)
	}
	return model
}

func ptyLastTerminalOperationEnd(analysis analyzer.Analysis) int64 {
	var last int64
	for _, operation := range analysis.Operations {
		if operation.ByteRange.End > last {
			last = operation.ByteRange.End
		}
	}
	return last
}

func applyPTYCheckpointAssistantFinal(model *ptyCheckpointModel, sequence uint64, text string) {
	model.Update(ongoingTranscriptEvent{
		Kind: ongoingTranscriptEventMessage,
		Message: clientui.TranscriptMessage{
			Sequence: sequence,
			Kind:     clientui.TranscriptMessageCommittedRow,
			CommittedRow: &clientui.TranscriptCommittedRow{
				Visibility: clientui.EntryVisibilityOngoing,
				Kind:       clientui.TranscriptRowAssistant,
				Assistant: &clientui.TranscriptAssistantRow{
					Text:  text,
					Phase: transcript.AssistantPhaseFinal,
				},
			},
		},
	})
}

func newPendingPTYDetailCheckpointModel(t *testing.T) (*uiModel, uuid.UUID) {
	t.Helper()
	model := newProjectedClosedUIModel(
		&runtimeControlFakeClient{},
		WithUISessionID(detailTestSessionID),
	)
	model.view = mustUpdateTUIModel(t, model.view, tea.KeyMsg{Type: tea.KeyShiftTab})
	model.activeSurface = uiSurfaceTranscriptDetail
	requestID := uuid.New()
	_, cancel := context.WithCancel(context.Background())
	sessionID, err := runtimeids.ParseSessionID(detailTestSessionID)
	if err != nil {
		t.Fatalf("parse detail test session ID: %v", err)
	}
	model.pendingDetailTranscript = &uiPendingDetailTranscriptRequest{
		id:        requestID,
		sessionID: sessionID,
		request:   clientui.TranscriptPageRequest{},
		cancel:    cancel,
	}
	t.Cleanup(model.Close)
	return model, requestID
}

func analyzeCheckpointBytes(t *testing.T, payload []byte) analyzer.Analysis {
	t.Helper()
	capture, err := analyzer.NewCapture(
		analyzer.MustDimensions(24, 80),
		[]analyzer.Chunk{analyzer.NewChunk(0, 0, payload)},
	)
	if err != nil {
		t.Fatalf("new checkpoint capture: %v", err)
	}
	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("analyze checkpoint capture: %v", err)
	}
	return analysis
}
