package app

import (
	"errors"
	"slices"
	"testing"

	tuitest "core/internal/testharness/pty"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
)

func TestAskProjectionUpdateIgnoresLateOrMismatchedCompletion(t *testing.T) {
	t.Run("mismatched completion", func(t *testing.T) {
		model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
		model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
			return questionRenderResultMsg{request: request, rows: []string{"ready"}}
		}
		model, command := updateQuestionProjection(model, askEventMsg{event: testQuestionAskEvent("ask-1", "Question?")})
		result := requireQuestionRenderResult(t, command)

		wrongCurrent := result
		wrongCurrent.request.currentToken = nextNonZeroToken(result.request.currentToken)
		wrongOperation := result
		wrongOperation.request.operationToken = uuid.New()
		for _, mismatch := range []questionRenderResultMsg{wrongCurrent, wrongOperation} {
			model, command = updateQuestionProjection(model, mismatch)
			if command != nil || model.ask.activeProjection != nil {
				t.Fatal("mismatched completion changed visible projection state")
			}
		}
		model, _ = updateQuestionProjection(model, result)
		if model.ask.activeProjection == nil ||
			!slices.Equal(model.ask.activeProjection.rows, []string{"ready"}) {
			t.Fatal("mismatched completion prevented the legitimate result from installing")
		}
	})

	t.Run("late projector panic", func(t *testing.T) {
		ringer := &countRinger{}
		model := sizedTestUIModel(newProjectedStaticUIModel(WithUIDebug(true)), 64, 20)
		model.promptAttention = newUnfocusedBellHooks(ringer)
		model.questionProjector = func(questionRenderRequest) questionRenderResultMsg {
			panic("stale renderer panic")
		}
		model, command := updateQuestionProjection(model, askEventMsg{event: testQuestionAskEvent("ask-1", "Question?")})
		model, _ = updateQuestionProjection(model, askEventMsg{event: askEvent{resolvedPromptID: "ask-1"}})
		result := requireQuestionRenderResult(t, command)
		model, quit := updateQuestionProjection(model, result)
		if result.err == nil || len(result.stack) == 0 ||
			quit != nil || model.forcedLocalExit || ringer.total() != 0 {
			t.Fatal("late projector panic affected the resolved prompt")
		}
	})
}

func TestAskProjectionUpdateLatestQuestionWinsAfterLateCompletion(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	gate := &questionProjectionGate{started: make(chan questionRenderRequest, 4), release: make(chan struct{})}
	model.questionProjector = gate.project
	model, firstCommand := updateQuestionProjection(model, askEventMsg{event: testQuestionAskEvent("ask-1", "Question A")})
	firstResult := make(chan tea.Msg, 1)
	go func() { firstResult <- firstCommand() }()
	<-gate.started

	model, command := updateQuestionProjection(model, askEventMsg{event: testQuestionAskEvent("ask-1", "Question B")})
	if command != nil {
		t.Fatal("newer question started while the prior projection was running")
	}
	close(gate.release)

	model, command = updateQuestionProjection(model, <-firstResult)
	if command == nil || model.ask.activeProjection != nil {
		t.Fatal("late completion installed obsolete rows or failed to schedule the latest question")
	}
	result := requireQuestionRenderResult(t, command)
	<-gate.started
	model, _ = updateQuestionProjection(model, result)
	if !slices.Equal(model.ask.activeProjection.rows, []string{"Question B"}) {
		t.Fatal("latest question did not become the visible projection")
	}
}

func TestAskProjectionUpdateQueuedPromotionRejectsOldCompletion(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	gate := &questionProjectionGate{started: make(chan questionRenderRequest, 4), release: make(chan struct{})}
	model.questionProjector = gate.project
	model, firstCommand := updateQuestionProjection(model, askEventMsg{event: testQuestionAskEvent("ask-a", "Question A")})
	firstResult := make(chan tea.Msg, 1)
	go func() { firstResult <- firstCommand() }()
	<-gate.started

	model, queuedBCommand := updateQuestionProjection(model, askEventMsg{event: testQuestionAskEvent("ask-b", "Question B")})
	model, queuedCCommand := updateQuestionProjection(model, askEventMsg{event: testQuestionAskEvent("ask-c", "Question C")})
	if queuedBCommand != nil || queuedCCommand != nil {
		t.Fatal("queued prompt projected before promotion")
	}
	model, command := updateQuestionProjection(model, askEventMsg{event: askEvent{resolvedPromptID: "ask-a"}})
	if command != nil || model.ask.current.prompt.PromptID != "ask-b" {
		t.Fatal("resolving A did not promote B without overlapping projection")
	}
	close(gate.release)

	model, command = updateQuestionProjection(model, <-firstResult)
	if command == nil || model.ask.activeProjection != nil {
		t.Fatal("A completion installed after B promotion")
	}
	result := requireQuestionRenderResult(t, command)
	<-gate.started
	model, _ = updateQuestionProjection(model, result)
	if !slices.Equal(model.ask.activeProjection.rows, []string{"Question B"}) {
		t.Fatal("B did not install after A's stale completion")
	}
	model, command = updateQuestionProjection(model, askEventMsg{event: askEvent{resolvedPromptID: "ask-b"}})
	if command == nil || model.ask.current.prompt.PromptID != "ask-c" {
		t.Fatal("resolving B did not preserve C as the next FIFO prompt")
	}
}

func TestAskProjectionUpdateHydrationReplacementRejectsOldCompletion(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	model.ongoingTranscript = newOngoingTranscriptController(
		&ongoingSurfaceSpy{},
		model.ongoingFrameInput,
		noopOngoingTranscriptRuntimeAdmission,
		model.applyAdmittedTranscriptMessageState,
	)
	gate := &questionProjectionGate{started: make(chan questionRenderRequest, 4), release: make(chan struct{})}
	model.questionProjector = gate.project
	model, firstCommand := updateQuestionProjection(model, askEventMsg{event: testQuestionAskEvent("ask-a", "Question A")})
	firstResult := make(chan tea.Msg, 1)
	go func() { firstResult <- firstCommand() }()
	<-gate.started

	promptB := testQuestionPrompt("ask-b", "Question B", "b")
	hydration := ongoingHydrationMessage(1)
	hydration.Payload.Hydration.RuntimeReadModelUpdate.Activity = runtimeTupleTestRunningActivity()
	hydration.Payload.Hydration.PendingPrompts = []clientui.TranscriptPrompt{promptB}
	model, hydrationCommand := updateQuestionProjection(model, ongoingTranscriptEvent{
		Kind: ongoingTranscriptEventMessage, Message: hydration,
	})
	if model.ask.current.prompt.PromptID != "ask-b" {
		t.Fatal("hydration did not replace A with B")
	}
	close(gate.release)
	_ = collectCmdMessages(t, hydrationCommand)
	select {
	case request := <-gate.started:
		t.Fatalf("hydrated prompt projected early: %+v", request)
	default:
	}

	model, command := updateQuestionProjection(model, <-firstResult)
	if command == nil || model.ask.activeProjection != nil {
		t.Fatal("A completion installed after hydration replacement")
	}
	result := requireQuestionRenderResult(t, command)
	<-gate.started
	model, _ = updateQuestionProjection(model, result)

	model.ongoingTranscript.ResetForScratchHydration()
	refreshed := promptB
	refreshed.Suggestions = []string{"new b", "other"}
	recommended := 1
	refreshed.RecommendedOptionIndex = &recommended
	hydration = ongoingHydrationMessage(1)
	hydration.Payload.Hydration.RuntimeReadModelUpdate.Activity = runtimeTupleTestRunningActivity()
	hydration.Payload.Hydration.PendingPrompts = []clientui.TranscriptPrompt{refreshed}
	model, command = updateQuestionProjection(model, ongoingTranscriptEvent{
		Kind: ongoingTranscriptEventMessage, Message: hydration,
	})
	_ = collectCmdMessages(t, command)
	select {
	case request := <-gate.started:
		t.Fatalf("same-question hydration refresh reprojected: %+v", request)
	default:
	}
	if !slices.Equal(model.ask.current.prompt.Suggestions, refreshed.Suggestions) ||
		model.ask.current.prompt.RecommendedOptionIndex == nil ||
		*model.ask.current.prompt.RecommendedOptionIndex != recommended {
		t.Fatal("hydration refresh did not retain the latest controls")
	}
}

func TestAskProjectionUpdateSameIdentityRefreshUsesLatestControls(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	gate := &questionProjectionGate{started: make(chan questionRenderRequest, 4), release: make(chan struct{})}
	model.questionProjector = gate.project
	model, firstCommand := updateQuestionProjection(model, askEventMsg{event: testQuestionAskEvent("ask-1", "Same question", "one")})
	firstResult := make(chan tea.Msg, 1)
	go func() { firstResult <- firstCommand() }()
	<-gate.started

	replacement := testQuestionAskEvent("ask-1", "Same question", "new one", "new two")
	recommended := 1
	replacement.prompt.RecommendedOptionIndex = &recommended
	model, command := updateQuestionProjection(model, askEventMsg{event: replacement})
	if command != nil {
		t.Fatal("same-identity refresh started a second projection")
	}
	close(gate.release)

	model, command = updateQuestionProjection(model, <-firstResult)
	if command != nil ||
		!slices.Equal(model.ask.current.prompt.Suggestions, replacement.prompt.Suggestions) ||
		model.ask.current.prompt.RecommendedOptionIndex == nil ||
		*model.ask.current.prompt.RecommendedOptionIndex != recommended {
		t.Fatal("same-identity completion did not use the latest controls")
	}
}

func TestAskProjectionUpdateFailureExitsInReleaseAndPanicsInDebug(t *testing.T) {
	fail := func(request questionRenderRequest) questionRenderResultMsg {
		return questionRenderResultMsg{request: request, err: errors.New("render failed")}
	}
	t.Run("release", func(t *testing.T) {
		logger := &testUILogger{}
		ringer := &countRinger{}
		model := sizedTestUIModel(newProjectedStaticUIModel(WithUILogger(logger)), 64, 20)
		model.promptAttention = newUnfocusedBellHooks(ringer)
		model.questionProjector = fail
		model, command := updateQuestionProjection(model, askEventMsg{event: testQuestionAskEvent("ask-1", "Question?")})
		result := requireQuestionRenderResult(t, command)
		model, quit := updateQuestionProjection(model, result)
		if len(result.stack) == 0 || quit == nil || !model.forcedLocalExit ||
			model.ask.current == nil || model.ask.activeProjection != nil ||
			model.transientStatus == "" || model.transientStatusKind != uiStatusNoticeError ||
			len(logger.lines) != 1 || ringer.total() != 0 {
			t.Fatal("release projection failure lost prompt, exit, or diagnostic state")
		}
		if _, ok := quit().(tea.QuitMsg); !ok {
			t.Fatal("release projection failure did not request Bubble Tea quit")
		}
	})

	t.Run("debug", func(t *testing.T) {
		logger := &testUILogger{}
		model := sizedTestUIModel(newProjectedStaticUIModel(WithUILogger(logger), WithUIDebug(true)), 64, 20)
		model.questionProjector = fail
		model, command := updateQuestionProjection(model, askEventMsg{event: testQuestionAskEvent("ask-1", "Question?")})
		result := requireQuestionRenderResult(t, command)
		defer func() {
			if recover() == nil || len(logger.lines) != 1 {
				t.Fatal("debug projection failure did not log before panicking")
			}
		}()
		_, _ = model.Update(result)
	})
}

func TestAskProjectionUpdateResizeKeepsVisibleQuestionWidthSafe(t *testing.T) {
	const initialWidth, targetWidth, height = 64, 18, 12
	model := sizedTestUIModel(newProjectedStaticUIModel(), initialWidth, height)
	renderCount := 0
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		renderCount++
		return projectAskQuestionMarkdown(request)
	}
	model, command := updateQuestionProjection(model, askEventMsg{event: testQuestionAskEvent(
		"ask-1", "[a long linked label](https://example.com/destination)", "yes",
	)})
	model, _ = updateQuestionProjection(model, requireQuestionRenderResult(t, command))
	model, command = updateQuestionProjection(model, tea.WindowSizeMsg{Width: targetWidth, Height: height})
	if renderCount != 1 || command == nil {
		t.Fatal("resize did not retain the visible prompt while scheduling projection")
	}
	visible, _ := testVisibleAskPaneContent(model, targetWidth)
	questionRows := 0
	for _, line := range visible {
		if line.prompt.Kind != askPromptLineKindQuestion {
			continue
		}
		questionRows++
		if lipgloss.Width(line.text) > targetWidth {
			t.Fatal("stale visible question exceeded the resized width")
		}
		trace := tuitest.TraceTerminalHyperlinks(t, line.text+" plain")
		if trace.Fragments[len(trace.Fragments)-1].Link != nil {
			t.Fatal("stale clamped question leaked hyperlink styling")
		}
	}
	if questionRows == 0 {
		t.Fatal("resize hid every visible question row")
	}
	model, _ = updateQuestionProjection(model, requireQuestionRenderResult(t, command))
	if renderCount != 2 || model.ask.activeProjection.renderedAt.terminalWidth != targetWidth {
		t.Fatal("resize command did not install the target-width projection")
	}

	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		return questionRenderResultMsg{request: request, err: errors.New("width render failed")}
	}
	model, command = updateQuestionProjection(model, tea.WindowSizeMsg{Width: targetWidth - 2, Height: height})
	model, quit := updateQuestionProjection(model, requireQuestionRenderResult(t, command))
	if quit == nil || !model.forcedLocalExit || model.ask.current == nil ||
		model.ask.activeProjection == nil {
		t.Fatal("resize failure did not retain the unresolved visible prompt and exit")
	}
}

type questionProjectionGate struct {
	started chan questionRenderRequest
	release chan struct{}
}

func (g *questionProjectionGate) project(request questionRenderRequest) questionRenderResultMsg {
	g.started <- request
	<-g.release
	return questionRenderResultMsg{request: request, rows: []string{request.questionSource}}
}

func updateQuestionProjection(model *uiModel, message tea.Msg) (*uiModel, tea.Cmd) {
	next, command := model.Update(message)
	return next.(*uiModel), command
}

func requireQuestionRenderResult(t *testing.T, command tea.Cmd) questionRenderResultMsg {
	if command == nil {
		t.Fatal("expected question projection command")
	}
	return command().(questionRenderResultMsg)
}
