package app

import (
	"errors"
	"reflect"
	"testing"
	"unicode"

	"core/cli/tui/ongoing"
	"core/shared/clientui"

	"github.com/google/uuid"
)

func TestOngoingTranscriptControllerRequiresHydrationFirst(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)

	result, err := controller.Accept(ongoingTranscriptMessage(1, clientui.TranscriptMessageRunState))
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
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)

	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	surface.calls = nil

	result, err := controller.Accept(ongoingTranscriptMessage(3, clientui.TranscriptMessageRunState))
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
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
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

func TestOngoingTranscriptControllerDrainsQueuedAssistantFinalizationAfterDetail(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	streamID := uuid.New()
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	if _, err := controller.Accept(clientui.TranscriptMessage{
		Sequence: 2,
		Kind:     clientui.TranscriptMessageAssistantDelta,
		AssistantDelta: &clientui.TranscriptAssistantDelta{
			StreamID: streamID,
			Delta:    "roundtrip commentary\n\n",
		},
	}); err != nil {
		t.Fatalf("accept assistant delta: %v", err)
	}
	surface.calls = nil
	if _, err := controller.SetNormalBufferOwned(false); err != nil {
		t.Fatalf("mark unowned: %v", err)
	}

	if _, err := controller.Accept(clientui.TranscriptMessage{
		Sequence: 3,
		Kind:     clientui.TranscriptMessageCommittedRow,
		CommittedRow: &clientui.TranscriptCommittedRow{
			Kind: clientui.TranscriptRowAssistant,
			Assistant: &clientui.TranscriptAssistantRow{
				StreamID: &streamID,
				Text:     "roundtrip commentary\n\nroundtrip complete",
			},
		},
	}); err != nil {
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
	if got, want := surface.calls[0].message.CommittedRow.Assistant.Text, "roundtrip commentary\n\nroundtrip complete"; got != want {
		t.Fatalf("drained finalization text = %q, want %q", got, want)
	}
}

func TestOngoingTranscriptControllerQueueOverflowRequestsScratchRehydrationOnRestore(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.SetNormalBufferOwned(false); err != nil {
		t.Fatalf("mark unowned: %v", err)
	}

	for seq := uint64(1); seq <= ongoingTranscriptQueueLimit+1; seq++ {
		message := ongoingTranscriptMessage(seq, clientui.TranscriptMessageRunState)
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

func TestOngoingTranscriptControllerDrainsQueuedNonRowsInArrivalOrderWithOneRender(t *testing.T) {
	surface := &ongoingSurfaceSpy{}
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	surface.calls = nil

	if _, err := controller.SetNormalBufferOwned(false); err != nil {
		t.Fatalf("mark unowned: %v", err)
	}
	queued := []clientui.TranscriptMessage{
		ongoingTranscriptMessage(2, clientui.TranscriptMessageRuntimeActivity),
		ongoingTranscriptMessage(3, clientui.TranscriptMessageQueuedOrSteeredMessageState),
		ongoingTranscriptMessage(4, clientui.TranscriptMessagePendingSessionPrompt),
		ongoingTranscriptMessage(5, clientui.TranscriptMessageContextUsage),
		ongoingTranscriptMessage(6, clientui.TranscriptMessageGoalStatus),
	}
	for _, message := range queued {
		if _, err := controller.Accept(message); err != nil {
			t.Fatalf("accept queued %s: %v", message.Kind, err)
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
	controller := newOngoingTranscriptController(surface, ongoingTestFrameProvider)

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
			kinds = append(kinds, call.message.Kind)
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
	return clientui.TranscriptMessage{
		Sequence:  sequence,
		Kind:      clientui.TranscriptMessageHydration,
		Hydration: &clientui.TranscriptHydration{},
	}
}

func ongoingTranscriptMessage(sequence uint64, kind clientui.TranscriptMessageKind) clientui.TranscriptMessage {
	message := clientui.TranscriptMessage{Sequence: sequence, Kind: kind}
	switch kind {
	case clientui.TranscriptMessageCommittedRow:
		message.CommittedRow = &clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: "hello"}}
	case clientui.TranscriptMessageRunState:
		message.RunState = &clientui.RunState{Status: clientui.RunStatusRunning, ActiveKind: clientui.RuntimeActivityActiveKindUserTurn}
	case clientui.TranscriptMessageRuntimeActivity:
		message.RuntimeActivity = &clientui.RuntimeActivity{State: clientui.RuntimeActivityRunning, ActiveKind: clientui.RuntimeActivityActiveKindUserTurn}
	case clientui.TranscriptMessageInputReconciliation:
		message.InputReconciliation = &clientui.RuntimeInputReconciliationSnapshot{Operations: []clientui.RuntimeInputReconciliation{{
			State: clientui.RuntimeInputReconciliationUnknown,
		}}}
	case clientui.TranscriptMessageQueuedOrSteeredMessageState:
		message.QueuedOrSteeredMessageState = &clientui.TranscriptQueuedOrSteeredMessageState{QueueItemID: "11111111-1111-4111-8111-111111111111", Status: clientui.QueuedUserMessageAccepted, UserText: "queued prompt"}
	case clientui.TranscriptMessageSessionStatus:
		message.SessionStatus = &clientui.TranscriptSessionStatus{ThinkingLevel: "high", CompactionMode: "auto"}
	case clientui.TranscriptMessageSessionIdentity:
		message.SessionIdentity = &clientui.TranscriptSessionIdentity{
			SessionName: "KENT-196",
			ExecutionTarget: clientui.SessionExecutionTarget{
				WorkspaceName: "Kent",
				Worktree:      &clientui.SessionExecutionWorktreeTarget{Name: "KENT-196"},
			},
		}
	case clientui.TranscriptMessageCompactionStatus:
		message.CompactionStatus = &clientui.TranscriptCompactionStatus{Mode: "auto", Count: 2, State: "ready"}
	case clientui.TranscriptMessagePendingSessionPrompt:
		message.PendingSessionPrompt = &clientui.TranscriptPendingSessionPrompt{ID: "ask-1", State: clientui.TranscriptPromptPending, Data: clientui.TranscriptPendingSessionPromptData{Question: "Approve command?"}}
	case clientui.TranscriptMessageContextUsage:
		message.ContextUsage = &clientui.RuntimeContextUsage{UsedTokens: 1200, WindowTokens: 2000}
	case clientui.TranscriptMessageGoalStatus:
		message.GoalStatus = &clientui.TranscriptGoalStatus{Objective: "finish review fixes", Status: clientui.RuntimeGoalStatusActive}
	case clientui.TranscriptMessageBackgroundActivity:
		message.BackgroundActivity = &clientui.TranscriptBackgroundActivity{ID: "22222222-2222-4222-8222-222222222222", State: "running", Preview: "running tests"}
	case clientui.TranscriptMessageToolStart:
		message.ToolStart = &clientui.TranscriptToolStart{ToolCallID: "tool-1", ToolName: "shell"}
	case clientui.TranscriptMessageToolAbort:
		message.ToolAbort = &clientui.TranscriptToolAbort{ToolCallID: "tool-1"}
	default:
		panic("unsupported test message kind " + string(kind))
	}
	return message
}
