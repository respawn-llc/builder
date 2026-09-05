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
		model, _ = updateQuestionProjection(model, askEventMsg{event: askEvent{resolvedToolCallID: "ask-1"}})
		defer func() {
			if recovered := recover(); recovered != "stale renderer panic" {
				t.Fatalf("recovered panic = %#v, want stale renderer panic", recovered)
			}
			if model.forcedLocalExit || ringer.total() != 0 {
				t.Fatal("late projector panic mutated the resolved prompt")
			}
		}()
		_ = command()
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
	model, command := updateQuestionProjection(model, askEventMsg{event: askEvent{resolvedToolCallID: "ask-a"}})
	if command != nil || model.ask.current.prompt.ToolCallID != "ask-b" {
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
	model, command = updateQuestionProjection(model, askEventMsg{event: askEvent{resolvedToolCallID: "ask-b"}})
	if command == nil || model.ask.current.prompt.ToolCallID != "ask-c" {
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
	hydration := questionHydrationMessage(promptB)
	model, hydrationCommand := updateQuestionProjection(model, ongoingTranscriptEvent{
		Kind: ongoingTranscriptEventMessage, Message: hydration,
	})
	if model.ask.current.prompt.ToolCallID != "ask-b" {
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
	hydration = questionHydrationMessage(refreshed)
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

func TestAskProjectionUpdateSameIDActiveReplacementPreservesPromptLocalState(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	initial := testQuestionAskEvent("ask-1", "Same question", "one", "two")
	testSetActiveAsk(model, &initial)
	model.ask.cursor = 1
	model.ask.freeform = true
	testSetAskInputAtRuneCursor(model, "draft text", 5)
	currentToken := model.ask.currentToken
	activeProjection := model.ask.activeProjection

	replacement := testQuestionAskEvent("ask-1", "Same question", "new one", "new two", "new three")
	recommended := 3
	replacement.prompt.RecommendedOptionIndex = &recommended
	model, command := updateQuestionProjection(model, askEventMsg{event: replacement})

	if command != nil {
		t.Fatal("same-identity active replacement scheduled redundant projection")
	}
	if model.ask.currentToken != currentToken || model.ask.activeProjection != activeProjection {
		t.Fatal("same-ID replacement changed current or projection ownership")
	}
	if !slices.Equal(model.ask.current.prompt.Suggestions, replacement.prompt.Suggestions) ||
		model.ask.current.prompt.RecommendedOptionIndex == nil ||
		*model.ask.current.prompt.RecommendedOptionIndex != recommended {
		t.Fatalf("same-ID replacement did not install the latest payload immediately: %+v", model.ask.current.prompt)
	}
	if model.ask.cursor != 1 || !model.ask.freeform ||
		model.ask.editor.Text() != "draft text" || model.ask.editor.Cursor() != 5 {
		t.Fatalf(
			"same-ID replacement changed prompt-local state: cursor=%d freeform=%t editor=%q/%d",
			model.ask.cursor,
			model.ask.freeform,
			model.ask.editor.Text(),
			model.ask.editor.Cursor(),
		)
	}
}

func TestAskProjectionUpdateUnrelatedMessagesDoNotScheduleRendering(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	renderCount := 0
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		renderCount++
		return questionRenderResultMsg{request: request, rows: []string{"rendered"}}
	}
	model, command := updateQuestionProjection(
		model,
		askEventMsg{event: testQuestionAskEvent("ask-1", "Question?", "yes", "no")},
	)
	model, _ = updateQuestionProjection(model, requireQuestionRenderResult(t, command))

	messages := []tea.Msg{
		tea.WindowSizeMsg{Width: 64, Height: 24},
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyTab},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("draft")},
		tea.KeyMsg{Type: tea.KeyLeft},
		spinnerTickMsg{},
		clearTransientStatusMsg{},
	}
	for _, message := range messages {
		model, command = updateQuestionProjection(model, message)
		if command != nil || model.ask.inFlightProjection != nil {
			t.Fatalf("message %T scheduled question rendering", message)
		}
	}
	if renderCount != 1 {
		t.Fatalf("render count = %d, want only the initial projection", renderCount)
	}
}

func TestAskProjectionUpdateLiveTranscriptAdmissionReturnsProjectionCommand(t *testing.T) {
	model := sizedTestUIModel(newProjectedStaticUIModel(), 64, 20)
	renderCount := 0
	model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
		renderCount++
		return questionRenderResultMsg{request: request, rows: []string{"rendered"}}
	}
	prompt := testQuestionPrompt("ask-1", "Live question", "yes")
	message := clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(prompt))

	command := model.applyAdmittedTranscriptMessageState(message, runtimeTupleMergeResult{})
	if command == nil {
		t.Fatal("live prompt admission dropped the projection command")
	}
	if renderCount != 0 {
		t.Fatal("live prompt admission rendered Markdown on the UI thread")
	}
	model, _ = updateQuestionProjection(model, command())
	if renderCount != 1 || model.ask.activeProjection == nil {
		t.Fatal("live prompt projection did not install through its command result")
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

func TestAskProjectionFailureDiagnosticDistinguishesInactiveAndInvalidDeliveryGeneration(t *testing.T) {
	for _, test := range []struct {
		name            string
		invalidDelivery bool
		wantPresent     bool
	}{
		{name: "inactive"},
		{name: "invalid generation", invalidDelivery: true, wantPresent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			logger := &projectionDiagnosticLogger{}
			model := sizedTestUIModel(newProjectedStaticUIModel(WithUILogger(logger)), 64, 20)
			model.questionProjector = func(request questionRenderRequest) questionRenderResultMsg {
				return questionRenderResultMsg{request: request, err: errors.New("render failed")}
			}
			model, command := updateQuestionProjection(model, askEventMsg{event: testQuestionAskEvent("ask-1", "Question?")})
			if test.invalidDelivery {
				key, err := newTranscriptPromptKey(model.ask.current.prompt)
				if err != nil {
					t.Fatalf("create valid delivery key: %v", err)
				}
				model.ask.activeDelivery = &activePromptAnswerDelivery{
					key: key, generation: 0, cancel: func() {},
				}
			}
			_, _ = updateQuestionProjection(model, requireQuestionRenderResult(t, command))

			if len(logger.arguments) != 1 || len(logger.arguments[0]) < 6 {
				t.Fatalf("projection diagnostics = %#v, want one complete diagnostic", logger.arguments)
			}
			generation, ok := logger.arguments[0][5].(*promptDeliveryGenerationDiagnostic)
			if !ok || (generation != nil) != test.wantPresent {
				t.Fatalf("delivery generation diagnostic = %#v, want present %t", logger.arguments[0][5], test.wantPresent)
			}
			if test.wantPresent && *generation != 0 {
				t.Fatalf("invalid delivery generation diagnostic = %d, want 0", *generation)
			}
		})
	}
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

type projectionDiagnosticLogger struct {
	arguments [][]any
}

func (l *projectionDiagnosticLogger) Logf(_ string, arguments ...any) {
	l.arguments = append(l.arguments, append([]any(nil), arguments...))
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

func questionHydrationMessage(prompt clientui.TranscriptPrompt) clientui.TranscriptMessage {
	message := ongoingHydrationMessage(1)
	hydration := message.Payload().(clientui.TranscriptHydration)
	hydration.RuntimeReadModelUpdate.Activity = runtimeTupleTestRunningActivity()
	hydration.PendingPrompts = []clientui.TranscriptPrompt{prompt}
	return clientui.NewTranscriptMessage(message.Sequence, clientui.NewTranscriptEvent(hydration))
}
