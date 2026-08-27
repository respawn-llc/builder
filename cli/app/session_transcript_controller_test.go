package app

import (
	"errors"
	"reflect"
	"testing"
	"time"
	"unicode"

	"core/cli/tui/ongoing"
	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/transcript"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOngoingTranscriptControllerRequiresHydrationFirst(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newTestOngoingTranscriptController(surface, ongoingTestFrameProvider)

	result, err := controller.Accept(ongoingTranscriptMessage(2, clientui.TranscriptMessageSessionStatus))
	if err != nil {
		t.Fatalf("accept non-hydration first message: %v", err)
	}

	if result.Action != ongoing.ResultRequestScratchRehydration {
		t.Fatalf("result action = %q, want scratch rehydration", result.Action)
	}
	if len(surface.calls) != 0 {
		t.Fatalf("surface calls = %v, want none", surface.calls)
	}
}

func TestOngoingTranscriptControllerSequenceGapRequestsScratchRehydration(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newTestOngoingTranscriptController(surface, ongoingTestFrameProvider)

	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	surface.calls = nil

	result, err := controller.Accept(ongoingTranscriptMessage(3, clientui.TranscriptMessageSessionStatus))
	if err != nil {
		t.Fatalf("accept sequence gap: %v", err)
	}

	if result.Action != ongoing.ResultRequestScratchRehydration {
		t.Fatalf("result action = %q, want scratch rehydration", result.Action)
	}
	if len(surface.calls) != 0 {
		t.Fatalf("surface calls = %v, want none", surface.calls)
	}
}

func TestOngoingTranscriptControllerQueuesOriginalMessagesWhileUnowned(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newTestOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if result, err := controller.SetNormalBufferOwned(false); err != nil || result.Action != ongoing.ResultNoop {
		t.Fatalf("mark unowned result=%+v err=%v", result, err)
	}

	hydration := ongoingHydrationMessage(1)
	live := ongoingTranscriptMessage(2, clientui.TranscriptMessageCommittedRow)
	if _, err := controller.Accept(hydration); err != nil {
		t.Fatalf("accept queued hydration: %v", err)
	}
	if _, err := controller.Accept(live); err != nil {
		t.Fatalf("accept queued live row: %v", err)
	}
	if len(surface.calls) != 0 {
		t.Fatalf("surface calls while unowned = %v, want none", surface.calls)
	}

	result, err := controller.SetNormalBufferOwned(true)
	if err != nil {
		t.Fatalf("restore ownership: %v", err)
	}
	if result.Action != ongoing.ResultNoop {
		t.Fatalf("restore action = %q, want noop", result.Action)
	}
	wantKinds := []clientui.TranscriptMessageKind{clientui.TranscriptMessageHydration, clientui.TranscriptMessageCommittedRow}
	if got := surface.appliedKinds(); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("drained message kinds = %v, want %v", got, wantKinds)
	}
}

func TestOngoingTranscriptControllerHydratesPendingWorkIntoLiveFrame(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newTestOngoingTranscriptController(surface, ongoingTestFrameProvider)
	hydration := ongoingHydrationMessage(1)
	hydrationPayload := hydration.Payload().(clientui.TranscriptHydration)
	hydrationPayload.PendingWork = runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
		pendingWorkMessageForTest(runtimeinput.PendingWorkLaneQueue, "queued"),
	}}
	hydration = clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(hydrationPayload))

	if _, err := controller.Accept(hydration); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}

	if got, want := surface.callKinds(), []string{"apply", "render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls = %v, want %v", got, want)
	}
	lines := surface.lastFrameSectionLines(ongoing.FrameSectionQueuedOrSteered)
	if len(lines) == 0 {
		t.Fatal("hydrated Pending Work did not produce a live frame section")
	}
	assertTerminalSafeFrameLines(t, lines)
}

func TestOngoingTranscriptControllerLeavesUserMessageFlushPresentationToStateObserver(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	observed := make([]clientui.TranscriptMessageKind, 0, 1)
	controller := newOngoingTranscriptController(
		surface,
		ongoingTestFrameProvider,
		noopOngoingTranscriptRuntimeAdmission,
		func(message clientui.TranscriptMessage, _ runtimeTupleMergeResult) tea.Cmd {
			observed = append(observed, message.Kind())
			return nil
		},
	)
	if _, _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	surface.calls = nil
	observed = nil

	if _, command, err := controller.Accept(ongoingTranscriptMessage(2, clientui.TranscriptMessageUserMessageFlushed)); err != nil {
		t.Fatalf("accept user-message flush: %v", err)
	} else if command != nil {
		t.Fatal("user-message flush returned an unexpected state command")
	}

	if got, want := observed, []clientui.TranscriptMessageKind{clientui.TranscriptMessageUserMessageFlushed}; !reflect.DeepEqual(got, want) {
		t.Fatalf("observed message kinds = %v, want %v", got, want)
	}
	if got, want := surface.callKinds(), []string{"render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls = %v, want %v", got, want)
	}
}

func TestOngoingTranscriptControllerDrainsQueuedAssistantFinalizationAfterDetail(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newTestOngoingTranscriptController(surface, ongoingTestFrameProvider)
	streamID := runtimeids.NewAssistantStreamID()
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	if _, err := controller.Accept(clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptAssistantDelta{
		StepID:   ongoingTestStepID(),
		StreamID: streamID,
		Delta:    "roundtrip commentary\n\n",
		Phase:    transcript.AssistantPhaseCommentary,
	})),
	); err != nil {
		t.Fatalf("accept assistant delta: %v", err)
	}
	surface.calls = nil
	if _, err := controller.SetNormalBufferOwned(false); err != nil {
		t.Fatalf("mark unowned: %v", err)
	}

	if _, err := controller.Accept(clientui.NewTranscriptMessage(3, clientui.NewTranscriptEvent(clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowAssistant,
		Locator:    transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1},
		Assistant: &clientui.TranscriptAssistantRow{
			StepID:   ongoingTestStepID(),
			StreamID: &streamID,
			Text:     "roundtrip commentary\n\nroundtrip complete",
			Phase:    transcript.AssistantPhaseFinal,
		},
	})),
	); err != nil {
		t.Fatalf("accept queued assistant finalization: %v", err)
	}
	if len(surface.calls) != 0 {
		t.Fatalf("surface calls while unowned = %v, want none", surface.calls)
	}
	if _, err := controller.SetNormalBufferOwned(true); err != nil {
		t.Fatalf("restore ownership: %v", err)
	}

	if got, want := surface.callKinds(), []string{"apply"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls after restore = %v, want %v", got, want)
	}
	row := surface.calls[0].message.Payload().(clientui.TranscriptCommittedRow)
	if got, want := row.Assistant.Text, "roundtrip commentary\n\nroundtrip complete"; got != want {
		t.Fatalf("drained finalization text = %q, want %q", got, want)
	}
}

func TestOngoingTranscriptControllerQueueOverflowRequestsScratchRehydrationOnRestore(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newTestOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.SetNormalBufferOwned(false); err != nil {
		t.Fatalf("mark unowned: %v", err)
	}

	for seq := uint64(1); seq <= ongoingTranscriptQueueLimit+1; seq++ {
		message := ongoingTranscriptMessage(seq, clientui.TranscriptMessageCommittedRow)
		if seq == 1 {
			message = ongoingHydrationMessage(seq)
		}
		if _, err := controller.Accept(message); err != nil {
			t.Fatalf("accept queued message %d: %v", seq, err)
		}
	}

	result, err := controller.SetNormalBufferOwned(true)
	if err != nil {
		t.Fatalf("restore ownership after overflow: %v", err)
	}
	if result.Action != ongoing.ResultRequestScratchRehydration {
		t.Fatalf("restore action = %q, want scratch rehydration", result.Action)
	}
	if len(surface.calls) != 0 {
		t.Fatalf("surface calls after overflow restore = %v, want no partial drain", surface.calls)
	}
}

func TestOngoingTranscriptControllerQueuesOnlyTerminalWorkWhileUnowned(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newTestOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	surface.calls = nil
	if _, err := controller.SetNormalBufferOwned(false); err != nil {
		t.Fatalf("mark unowned: %v", err)
	}

	for sequence := uint64(2); sequence <= ongoingTranscriptQueueLimit+2; sequence++ {
		message := ongoingTranscriptMessage(sequence, clientui.TranscriptMessageQueuedMessageState)
		payload := message.Payload().(clientui.TranscriptQueuedMessageState)
		*payload.Text = "latest queued prompt"
		message = clientui.NewTranscriptMessage(sequence, clientui.NewTranscriptEvent(payload))
		if _, err := controller.Accept(message); err != nil {
			t.Fatalf("accept app-owned message %d: %v", sequence, err)
		}
	}
	if len(surface.calls) != 0 {
		t.Fatalf("surface calls while unowned = %v, want none", surface.calls)
	}

	result, err := controller.SetNormalBufferOwned(true)
	if err != nil {
		t.Fatalf("restore ownership: %v", err)
	}
	if result.Action != ongoing.ResultNoop {
		t.Fatalf("restore action = %q, want no scratch rehydration", result.Action)
	}
	if got, want := surface.callKinds(), []string{"render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls = %v, want %v", got, want)
	}
}

func TestOngoingTranscriptControllerDrainsQueuedNonRowsInArrivalOrderWithOneRender(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newTestOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	surface.calls = nil

	if _, err := controller.SetNormalBufferOwned(false); err != nil {
		t.Fatalf("mark unowned: %v", err)
	}
	queued := []clientui.TranscriptMessage{
		ongoingTranscriptMessage(2, clientui.TranscriptMessageRuntimeReadModelUpdate),
		ongoingTranscriptMessage(3, clientui.TranscriptMessagePendingWorkReplaced),
		ongoingTranscriptMessage(4, clientui.TranscriptMessagePrompt),
		ongoingTranscriptMessage(5, clientui.TranscriptMessageContextUsage),
		ongoingTranscriptMessage(6, clientui.TranscriptMessageGoalStatus),
	}
	for _, message := range queued {
		if _, err := controller.Accept(message); err != nil {
			t.Fatalf("accept queued %s: %v", message.Kind(), err)
		}
	}
	if len(surface.calls) != 0 {
		t.Fatalf("surface calls while unowned = %v, want none", surface.calls)
	}

	if _, err := controller.SetNormalBufferOwned(true); err != nil {
		t.Fatalf("restore ownership: %v", err)
	}

	if got, want := surface.callKinds(), []string{"render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("surface calls = %v, want %v", got, want)
	}
	wantSections := []ongoing.FrameSectionKind{
		ongoing.FrameSectionQueuedOrSteered,
		ongoing.FrameSectionPendingPrompt,
	}
	if got := surface.lastFrameSectionKinds(); !reflect.DeepEqual(got, wantSections) {
		t.Fatalf("frame sections = %v, want %v", got, wantSections)
	}
}

func TestOngoingTranscriptControllerReturnsSurfaceErrorsSynchronously(t *testing.T) {
	wantErr := errors.New("surface failed")
	surface := &ongoingSurfaceSpy{err: wantErr}
	controller := newTestOngoingTranscriptController(surface, ongoingTestFrameProvider)

	_, err := controller.Accept(ongoingHydrationMessage(1))
	if !errors.Is(err, wantErr) {
		t.Fatalf("accept error = %v, want %v", err, wantErr)
	}
}

type ongoingSurfaceSpy struct {
	calls []ongoingSurfaceCall
	err   error
}

func ongoingTestFrameProvider() ongoing.FrameInput {
	return ongoing.FrameInput{
		Size:   ongoing.Size{Width: 80, Height: 24},
		Cursor: ongoing.Cursor{Visible: true, Row: 24, Column: 1},
	}
}

type testOngoingTranscriptController struct {
	*ongoingTranscriptController
}

func newTestOngoingTranscriptController(surface ongoingTranscriptSurface, frameProvider ongoingFrameProvider) *testOngoingTranscriptController {
	return &testOngoingTranscriptController{
		ongoingTranscriptController: newNoopOngoingTranscriptController(surface, frameProvider),
	}
}

func newNoopOngoingTranscriptController(surface ongoingTranscriptSurface, frameProvider ongoingFrameProvider) *ongoingTranscriptController {
	return newOngoingTranscriptController(
		surface,
		frameProvider,
		noopOngoingTranscriptRuntimeAdmission,
		func(clientui.TranscriptMessage, runtimeTupleMergeResult) tea.Cmd {
			return nil
		},
	)
}

func noopOngoingTranscriptRuntimeAdmission(clientui.TranscriptMessage) (runtimeTupleMergeResult, error) {
	return runtimeTupleMergeResult{}, nil
}

func (c *testOngoingTranscriptController) Accept(message clientui.TranscriptMessage) (ongoing.Result, error) {
	result, command, err := c.ongoingTranscriptController.Accept(message)
	if command != nil {
		panic("test ongoing transcript controller received an unexpected state command")
	}
	return result, err
}

type ongoingSurfaceCall struct {
	name    string
	message clientui.TranscriptMessage
	frame   ongoing.FrameInput
}

func (s *ongoingSurfaceSpy) ApplyTerminalMessage(message clientui.TranscriptMessage, frame ongoing.FrameInput) (ongoing.Result, error) {
	s.calls = append(s.calls, ongoingSurfaceCall{name: "apply", message: message, frame: frame})
	return ongoing.Result{}, s.err
}

func (s *ongoingSurfaceSpy) Render(frame ongoing.FrameInput) (ongoing.Result, error) {
	s.calls = append(s.calls, ongoingSurfaceCall{name: "render", frame: frame})
	return ongoing.Result{}, s.err
}

func (s *ongoingSurfaceSpy) Resize(_ ongoing.Size, frame ongoing.FrameInput) (ongoing.Result, error) {
	s.calls = append(s.calls, ongoingSurfaceCall{name: "resize", frame: frame})
	return ongoing.Result{}, s.err
}

func (s *ongoingSurfaceSpy) appliedKinds() []clientui.TranscriptMessageKind {
	kinds := make([]clientui.TranscriptMessageKind, 0, len(s.calls))
	for _, call := range s.calls {
		if call.name == "apply" {
			kinds = append(kinds, call.message.Kind())
		}
	}
	return kinds
}

func (s *ongoingSurfaceSpy) callKinds() []string {
	kinds := make([]string, 0, len(s.calls))
	for _, call := range s.calls {
		kinds = append(kinds, call.name)
	}
	return kinds
}

func (s *ongoingSurfaceSpy) lastFrameSectionKinds() []ongoing.FrameSectionKind {
	if len(s.calls) == 0 {
		return nil
	}
	sections := s.calls[len(s.calls)-1].frame.Sections
	kinds := make([]ongoing.FrameSectionKind, 0, len(sections))
	for _, section := range sections {
		kinds = append(kinds, section.Kind)
	}
	return kinds
}

func (s *ongoingSurfaceSpy) lastFrameSectionLines(kind ongoing.FrameSectionKind) []string {
	if len(s.calls) == 0 {
		return nil
	}
	for _, section := range s.calls[len(s.calls)-1].frame.Sections {
		if section.Kind == kind {
			lines := make([]string, 0, len(section.StyledLines)+len(section.Lines))
			for _, line := range section.StyledLines {
				lines = append(lines, line.Plain())
			}
			lines = append(lines, section.Lines...)
			return lines
		}
	}
	return nil
}

func (s *ongoingSurfaceSpy) lastFrameStyledSection(kind ongoing.FrameSectionKind) []transcriptrender.Line {
	if len(s.calls) == 0 {
		return nil
	}
	for _, section := range s.calls[len(s.calls)-1].frame.Sections {
		if section.Kind == kind {
			return append([]transcriptrender.Line(nil), section.StyledLines...)
		}
	}
	return nil
}

func assertTerminalSafeFrameLines(t *testing.T, lines []string) {
	t.Helper()
	for _, line := range lines {
		for _, r := range line {
			if unicode.IsControl(r) {
				t.Fatalf("frame line %q contains control rune %U", line, r)
			}
		}
	}
}

func ongoingHydrationMessage(sequence uint64) clientui.TranscriptMessage {
	return clientui.NewTranscriptMessage(sequence, clientui.NewTranscriptEvent(clientui.TranscriptHydration{
		SessionIdentity: clientui.TranscriptSessionIdentity{
			SessionID:             ongoingTestSessionID(),
			ConversationFreshness: clientui.ConversationFreshnessFresh,
		},
		SessionStatus: clientui.TranscriptSessionStatus{
			ReviewerFrequency: "off",
			ThinkingLevel:     "medium",
			CompactionMode:    "auto",
		},
		RuntimeReadModelUpdate: clientui.RuntimeReadModelUpdate{
			Version: clientui.ReadModelVersion{Epoch: "ongoing-test", Generation: 1, Sequence: 1},
			Activity: clientui.RuntimeActivity{
				State:          clientui.RuntimeActivityRegisteredIdle,
				Reviewer:       clientui.ReviewerActivityInactive,
				QueueAccepting: true,
			},
		},
		CommittedRows: []clientui.TranscriptCommittedRow{},
	}))

}

func ongoingTranscriptMessage(sequence uint64, kind clientui.TranscriptMessageKind) clientui.TranscriptMessage {
	var event clientui.TranscriptEvent
	switch kind {
	case clientui.TranscriptMessageCommittedRow:
		event = clientui.NewTranscriptEvent(clientui.TranscriptCommittedRow{
			Visibility: transcript.EntryVisibilityOngoing,
			Integrity:  transcript.RowIntegrityValid,
			Kind:       clientui.TranscriptRowUser,
			Locator:    transcript.CommittedRowLocator{EventSequence: int64(sequence), RowOrdinal: 1},
			User:       &clientui.TranscriptUserRow{StepID: ongoingTestStepIDPointer(), Text: "hello"},
		})
	case clientui.TranscriptMessageRuntimeReadModelUpdate:
		event = clientui.NewTranscriptEvent(clientui.RuntimeReadModelUpdate{
			Version: clientui.ReadModelVersion{Epoch: "ongoing-test", Generation: 1, Sequence: sequence},
			Activity: clientui.RuntimeActivity{
				State:          clientui.RuntimeActivityRegisteredIdle,
				Reviewer:       clientui.ReviewerActivityInactive,
				QueueAccepting: true,
			},
		})
	case clientui.TranscriptMessageQueuedMessageState:
		text := "queued prompt"
		event = clientui.NewTranscriptEvent(clientui.TranscriptQueuedMessageState{
			QueueItemID: ongoingTestQueueItemID(),
			Status:      clientui.QueuedUserMessageAccepted,
			Text:        &text,
		})
	case clientui.TranscriptMessagePendingWorkReplaced:
		event = clientui.NewTranscriptEvent(clientui.TranscriptPendingWorkReplaced{
			PendingWork: runtimeinput.PendingWork{Items: []runtimeinput.PendingWorkItem{
				pendingWorkMessageForTest(runtimeinput.PendingWorkLaneQueue, "queued prompt"),
			}},
		})
	case clientui.TranscriptMessageUserMessageFlushed:
		event = clientui.NewTranscriptEvent(clientui.TranscriptUserMessageFlushed{
			StepID: ongoingTestStepIDPointer(),
		})
	case clientui.TranscriptMessageSessionStatus:
		event = clientui.NewTranscriptEvent(clientui.TranscriptSessionStatus{
			ReviewerFrequency: "off",
			ThinkingLevel:     "high",
			CompactionMode:    "auto",
		})
	case clientui.TranscriptMessageSessionIdentity:
		sessionName := "KENT-196"
		event = clientui.NewTranscriptEvent(clientui.TranscriptSessionIdentity{
			SessionID:             ongoingTestSessionID(),
			SessionName:           &sessionName,
			ConversationFreshness: clientui.ConversationFreshnessEstablished,
		})
	case clientui.TranscriptMessageCompactionStatus:
		event = clientui.NewTranscriptEvent(clientui.TranscriptCompactionStatus{
			StepID: ongoingTestStepID(),
			Mode:   "auto",
			Count:  2,
			State:  clientui.CompactionCompleted,
		})
	case clientui.TranscriptMessagePrompt:
		event = clientui.NewTranscriptEvent(clientui.TranscriptPrompt{
			Kind:      clientui.TranscriptPromptKindQuestion,
			Status:    clientui.TranscriptPromptStatusPending,
			PromptID:  "ask-1",
			SessionID: ongoingTestSessionID(),
			StepID:    ongoingTestStepID(),
			Question:  "Approve command?",
			CreatedAt: time.Unix(1, 0).UTC(),
		})
	case clientui.TranscriptMessageContextUsage:
		event = clientui.NewTranscriptEvent(clientui.TranscriptContextUsage{UsedTokens: 1200, WindowTokens: 2000})
	case clientui.TranscriptMessageGoalStatus:
		event = clientui.NewTranscriptEvent(clientui.TranscriptGoalStatus{Goal: &clientui.TranscriptGoal{
			Goal: &clientui.Goal{
				ID:        "goal-1",
				Objective: "finish review fixes",
				Status:    clientui.RuntimeGoalStatusActive,
				CreatedAt: time.Unix(1, 0).UTC(),
				UpdatedAt: time.Unix(1, 0).UTC(),
			},
		}})
	case clientui.TranscriptMessageBackgroundActivity:
		preview := "running tests"
		event = clientui.NewTranscriptEvent(clientui.TranscriptBackgroundActivity{
			ActivityID:  ongoingTestBackgroundActivityID(),
			ProcessID:   "process-1",
			OwnerRunID:  ongoingTestRunID(),
			OwnerStepID: ongoingTestStepID(),
			Lifecycle:   clientui.BackgroundLifecycleBackgrounded,
			Command:     "go test ./...",
			Workdir:     "/tmp",
			Preview:     &preview,
		})
	case clientui.TranscriptMessageToolStart:
		event = clientui.NewTranscriptEvent(clientui.TranscriptToolStart{
			StepID:     ongoingTestStepID(),
			ToolCallID: "tool-1",
			ToolName:   "shell",
		})
	case clientui.TranscriptMessageToolAbort:
		event = clientui.NewTranscriptEvent(clientui.TranscriptToolAbort{
			StepID:     ongoingTestStepID(),
			ToolCallID: "tool-1",
			Reason:     clientui.ToolAbortCanceled,
		})
	default:
		panic("unsupported test message kind " + string(kind))
	}
	return clientui.NewTranscriptMessage(sequence, event)
}

func ongoingTestSessionID() runtimeids.SessionID {
	id, err := runtimeids.ParseSessionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if err != nil {
		panic(err)
	}
	return id
}

func ongoingTestRunID() runtimeids.RunID {
	id, err := runtimeids.ParseRunID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	if err != nil {
		panic(err)
	}
	return id
}

func ongoingTestStepID() runtimeids.StepID {
	id, err := runtimeids.ParseStepID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	if err != nil {
		panic(err)
	}
	return id
}

func ongoingTestStepIDPointer() *runtimeids.StepID {
	stepID := ongoingTestStepID()
	return &stepID
}

func ongoingTestQueueItemID() runtimeids.QueueItemID {
	id, err := runtimeids.ParseQueueItemID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
	if err != nil {
		panic(err)
	}
	return id
}

func ongoingTestBackgroundActivityID() runtimeids.BackgroundActivityID {
	id, err := runtimeids.ParseBackgroundActivityID("ffffffff-ffff-4fff-8fff-ffffffffffff")
	if err != nil {
		panic(err)
	}
	return id
}
