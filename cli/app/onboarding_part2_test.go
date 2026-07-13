package app

import (
	tuiinput "core/cli/tui/input"
	"core/shared/config"
	"core/shared/serverapi"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOnboardingImportDiscoveryKeepsTypedInput(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, nil))
	steps := model.workflow.visibleSteps(&model.state)
	modelStepIndex := -1
	for index, step := range steps {
		if step.id == "model" {
			modelStepIndex = index
			break
		}
	}
	if modelStepIndex < 0 {
		t.Fatal("expected model input step to be visible")
	}
	model.stepIndex = modelStepIndex
	model.syncScreen(true)
	model.input.Replace(strings.NewReplacer("\r", "", "\n", "").Replace("draft-model-alias"))
	next, _ := model.Update(onboardingImportDiscoveryDoneMsg{discovery: onboardingImportDiscovery{skillSymlinkItems: map[onboardingImportProviderID][]onboardingSkillImportItem{}}})
	updated := next.(*onboardingModel)
	if updated.currentScreen.ID != "model" {
		t.Fatalf("expected to stay on model input screen, got %q", updated.currentScreen.ID)
	}
	if got := updated.input.Text(); got != "draft-model-alias" {
		t.Fatalf("expected import discovery refresh to preserve typed input, got %q", got)
	}
}

func TestOnboardingInputRendersReusableEditorFieldCursor(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, func(cfg *config.App) { cfg.Settings.Theme = "dark" }))
	model.currentScreen = onboardingScreen{Kind: onboardingScreenInput, Title: "Enter value"}
	model.input = newSingleLineEditor("abc")
	model.input.SetCursor(byteOffsetForRuneCursor(model.input.Text(), 1))

	content := model.buildContent(24)
	expected := tuiinput.RenderSoftCursorLines(24, renderSingleLineEditor(24, 0, model.input, "> ", true, 0, ""), model.styles.inputText)
	if content.cursorRow < 0 || content.cursorRow+len(expected) > len(content.lines) {
		t.Fatalf("input cursor row %d outside content lines %#v", content.cursorRow, content.lines)
	}
	got := content.lines[content.cursorRow : content.cursorRow+len(expected)]
	if strings.Join(got, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("onboarding input did not render through reusable text input, got %#v want %#v", got, expected)
	}
}

func TestOnboardingInputCursorRowTracksWrappedReusableEditorField(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, func(cfg *config.App) { cfg.Settings.Theme = "dark" }))
	model.currentScreen = onboardingScreen{Kind: onboardingScreenInput, Title: "Enter value"}
	model.input = newSingleLineEditor("alpha beta gamma")

	content := model.buildContent(8)
	rendered := renderSingleLineEditor(8, 0, model.input, "> ", true, 0, "")
	if !rendered.Cursor.Visible || rendered.Cursor.Row < 1 {
		t.Fatalf("expected wrapped input cursor below first row, cursor=%+v lines=%#v", rendered.Cursor, rendered.Lines)
	}
	if got, want := content.cursorRow, rendered.Cursor.Row; got != want {
		t.Fatalf("content cursor row = %d, want %d", got, want)
	}
}

func TestOnboardingInputUsesRealAltScreenCursorWhenAvailable(t *testing.T) {
	state := newUITerminalCursorState()
	model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, func(cfg *config.App) { cfg.Settings.Theme = "dark" }))
	model.terminalCursor = state
	model.width = 24
	model.height = 12
	model.currentScreen = onboardingScreen{Kind: onboardingScreenInput, Title: "Enter value"}
	model.input = newSingleLineEditor("alpha beta gamma")

	view := model.View()
	placement, ok := state.Snapshot()
	if !ok {
		t.Fatalf("expected real cursor placement for onboarding input, view=%q", view)
	}
	if !placement.AltScreen {
		t.Fatalf("expected alt-screen cursor placement, got %+v", placement)
	}
	if placement.CursorCol >= model.width {
		t.Fatalf("cursor col %d outside width %d", placement.CursorCol, model.width)
	}
}

func TestOnboardingEditorFieldDeleteCurrentLineUsesAppKeyAdapter(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyMsg
	}{
		{name: "ctrl-backspace-csi", key: tea.KeyMsg{Type: keyTypeCtrlBackspaceCSI}},
		{name: "super-backspace-csi", key: tea.KeyMsg{Type: keyTypeSuperBackspaceCSI}},
	}
	if runtime.GOOS == "darwin" {
		cases = append(cases, struct {
			name string
			key  tea.KeyMsg
		}{name: "darwin-ctrl-u", key: tea.KeyMsg{Type: tea.KeyCtrlU}})
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, func(cfg *config.App) { cfg.Settings.Theme = "dark" }))
			model.currentScreen = onboardingScreen{Kind: onboardingScreenInput, Title: "Enter value"}
			model.input = newSingleLineEditor("project name")
			model.input.SetCursor(byteOffsetForRuneCursor(model.input.Text(), len([]rune("project"))))

			next, _ := model.Update(tt.key)
			updated := next.(*onboardingModel)
			if got := updated.input.Text(); got != "" {
				t.Fatalf("value after delete-current-line key = %q, want empty", got)
			}
			if got := runeOffsetForByteCursor(updated.input.Text(), updated.input.Cursor()); got != 0 {
				t.Fatalf("cursor after delete-current-line key = %d, want 0", got)
			}
		})
	}
}

func newOnboardingModelAtModelInput(t *testing.T) *onboardingModel {
	t.Helper()
	model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, func(cfg *config.App) { cfg.Settings.Theme = "dark" }))
	steps := model.workflow.visibleSteps(&model.state)
	modelStepIndex := -1
	for index, step := range steps {
		if step.id == "model" {
			modelStepIndex = index
			break
		}
	}
	if modelStepIndex < 1 {
		t.Fatalf("model input step index = %d, want non-initial input step", modelStepIndex)
	}
	model.stepIndex = modelStepIndex
	model.syncScreen(true)
	if model.currentScreen.Kind != onboardingScreenInput {
		t.Fatalf("screen kind = %q, want input", model.currentScreen.Kind)
	}
	return model
}

func TestOnboardingInputWordNavigationStaysInField(t *testing.T) {
	cases := []struct {
		name          string
		key           tea.KeyMsg
		initialCursor func(string) int
		wantCursor    func(string, int) int
	}{
		{
			name:          "alt-left",
			key:           tea.KeyMsg{Type: tea.KeyLeft, Alt: true},
			initialCursor: func(text string) int { return len([]rune(text)) },
			wantCursor:    moveBufferCursorWordLeft,
		},
		{
			name:          "alt-right",
			key:           tea.KeyMsg{Type: tea.KeyRight, Alt: true},
			initialCursor: func(string) int { return 0 },
			wantCursor:    moveBufferCursorWordRight,
		},
		{
			name:          "alt-b",
			key:           tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'b'}},
			initialCursor: func(text string) int { return len([]rune(text)) },
			wantCursor:    moveBufferCursorWordLeft,
		},
		{
			name:          "alt-f",
			key:           tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'f'}},
			initialCursor: func(string) int { return 0 },
			wantCursor:    moveBufferCursorWordRight,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			model := newOnboardingModelAtModelInput(t)
			const input = "alpha beta gamma"
			model.input = newSingleLineEditor(input)
			initialCursor := tt.initialCursor(input)
			model.input.SetCursor(byteOffsetForRuneCursor(input, initialCursor))
			screenID := model.currentScreen.ID

			next, _ := model.Update(tt.key)
			updated := next.(*onboardingModel)
			if got := updated.currentScreen.ID; got != screenID {
				t.Fatalf("screen after %s = %q, want %q", tt.name, got, screenID)
			}
			if got := updated.input.Text(); got != input {
				t.Fatalf("input after %s = %q, want %q", tt.name, got, input)
			}
			if got, want := runeOffsetForByteCursor(updated.input.Text(), updated.input.Cursor()), tt.wantCursor(input, initialCursor); got != want {
				t.Fatalf("cursor after %s = %d, want %d", tt.name, got, want)
			}
		})
	}
}

func TestOnboardingInputPlainArrowsKeepStepNavigation(t *testing.T) {
	cases := []struct {
		name      string
		key       tea.KeyMsg
		stepDelta int
	}{
		{name: "left", key: tea.KeyMsg{Type: tea.KeyLeft}, stepDelta: -1},
		{name: "right", key: tea.KeyMsg{Type: tea.KeyRight}, stepDelta: 1},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			model := newOnboardingModelAtModelInput(t)
			initialStepIndex := model.stepIndex

			next, _ := model.Update(tt.key)
			updated := next.(*onboardingModel)
			if got, want := updated.stepIndex, initialStepIndex+tt.stepDelta; got != want {
				t.Fatalf("step index after plain %s = %d, want %d", tt.name, got, want)
			}
		})
	}
}

func TestOnboardingSpinnerTickDoesNotRescheduleOutsideLoadingOrFinalize(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, func(cfg *config.App) { cfg.Settings.Theme = "dark" }))
	model.state.imports.pending = false
	model.syncScreen(true)
	if model.currentScreen.Kind == onboardingScreenLoading {
		t.Fatalf("expected non-loading onboarding screen, got %q", model.currentScreen.Kind)
	}
	tickAt := model.spinnerClock.anchor.Add(spinnerTickInterval)
	next, cmd := model.Update(onboardingSpinnerTickMsg{at: tickAt})
	updated := next.(*onboardingModel)
	if updated.spinnerFrame == 0 {
		t.Fatal("expected spinner tick to advance frame even when stopping animation")
	}
	if cmd != nil {
		t.Fatalf("did not expect spinner tick to reschedule on %q screen", updated.currentScreen.Kind)
	}
}

func TestOnboardingSpinnerTickReschedulesWhileLoading(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, func(cfg *config.App) { cfg.Settings.Theme = "dark" }))
	model.currentScreen = onboardingScreen{Kind: onboardingScreenLoading}
	tickAt := model.spinnerClock.anchor.Add(spinnerTickInterval)
	next, cmd := model.Update(onboardingSpinnerTickMsg{at: tickAt})
	updated := next.(*onboardingModel)
	if updated.spinnerFrame == 0 {
		t.Fatal("expected loading spinner tick to advance frame")
	}
	if cmd == nil {
		t.Fatal("expected loading spinner tick to reschedule")
	}
}

func TestOnboardingSpinnerTickReschedulesWhileFinalizing(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, func(cfg *config.App) { cfg.Settings.Theme = "dark" }))
	model.state.imports.pending = false
	model.syncScreen(true)
	model.finalizing = true
	tickAt := model.spinnerClock.anchor.Add(spinnerTickInterval)
	next, cmd := model.Update(onboardingSpinnerTickMsg{at: tickAt})
	updated := next.(*onboardingModel)
	if updated.spinnerFrame == 0 {
		t.Fatal("expected finalizing spinner tick to advance frame")
	}
	if cmd == nil {
		t.Fatal("expected finalizing spinner tick to reschedule")
	}
}

func TestApplyOnboardingModelUpdatesKnownContextWindow(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, func(cfg *config.App) {
		cfg.Settings.Model = "gpt-5.3-codex"
	})
	if err := state.submitPrimaryModel("gpt-5.6-sol"); err != nil {
		t.Fatalf("apply onboarding model: %v", err)
	}
	if state.selections.contextWindow.kind != onboardingContextDefault {
		t.Fatalf("expected gpt-5.6-sol default context window, got %+v", state.selections.contextWindow)
	}
	if state.selections.reviewerModelValue() != "gpt-5.6-sol" {
		t.Fatalf("expected reviewer model to follow main model, got %q", state.selections.reviewerModelValue())
	}
	if state.selections.reviewerThinkingValue() != "medium" {
		t.Fatalf("expected reviewer thinking to follow main thinking, got %q", state.selections.reviewerThinkingValue())
	}
}

func TestApplyOnboardingModelResetsUnknownModelContextWindowToBaseline(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	if err := state.submitPrimaryModel("my-team-alias"); err != nil {
		t.Fatalf("apply onboarding model: %v", err)
	}
	if state.selections.contextWindow.kind != onboardingContextCustom || state.selections.contextWindow.tokens != 272_000 {
		t.Fatalf("expected unknown model context window to reset to onboarding baseline, got %+v", state.selections.contextWindow)
	}
}

func TestReviewerModelStepDefaultsToMainModel(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	screen := findWorkflowStep(t, state, "reviewer_model").build(state)
	if screen.InputValue != "gpt-5.6-sol" {
		t.Fatalf("expected reviewer model default to follow main model, got %q", screen.InputValue)
	}
}

func TestReviewerThinkingStepDefaultsToMainThinking(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, func(cfg *config.App) {
		cfg.Settings.ThinkingLevel = "high"
		cfg.Settings.Reviewer.ThinkingLevel = "high"
		cfg.Source.Sources["thinking_level"] = "file"
	})
	screen := findWorkflowStep(t, state, "reviewer_thinking").build(state)
	if screen.DefaultOptionID != "high" {
		t.Fatalf("expected reviewer thinking default to follow main thinking, got %q", screen.DefaultOptionID)
	}
}

func TestMainThinkingChoiceSynchronizesReviewerThinking(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	if err := findWorkflowStep(t, state, "thinking").apply(state, "high"); err != nil {
		t.Fatalf("apply thinking choice: %v", err)
	}
	if state.selections.reviewerThinkingValue() != "high" {
		t.Fatalf("expected reviewer thinking to track updated main thinking, got %q", state.selections.reviewerThinkingValue())
	}
}

func TestMainThinkingChoicePreservesCustomReviewerThinking(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, func(cfg *config.App) {
		cfg.Settings.Reviewer.ThinkingLevel = "low"
		cfg.Source.Sources["reviewer.thinking_level"] = "file"
	})
	if err := findWorkflowStep(t, state, "thinking").apply(state, "high"); err != nil {
		t.Fatalf("apply thinking choice: %v", err)
	}
	if state.selections.reviewerThinkingValue() != "low" {
		t.Fatalf("expected custom reviewer thinking to be preserved, got %q", state.selections.reviewerThinkingValue())
	}
}

func TestReviewerThinkingDisableDoesNotForceCustomInput(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	if err := findWorkflowStep(t, state, "reviewer_thinking").apply(state, "disable"); err != nil {
		t.Fatalf("apply reviewer disable choice: %v", err)
	}
	if state.selections.supervisor.thinking.override.kind != onboardingThinkingDisabled {
		t.Fatalf("expected reviewer thinking to be disabled, got %+v", state.selections.supervisor.thinking)
	}
	if state.selections.pendingReviewerThinking.kind != onboardingThinkingEditNone {
		t.Fatal("expected disable choice not to open custom reviewer thinking input")
	}
	if workflowIncludesStep(newOnboardingWorkflow(state).visibleSteps(state), "reviewer_thinking_custom") {
		t.Fatal("expected custom reviewer thinking step to stay hidden after disable choice")
	}
}

func TestReviewerThinkingPresetChoiceDoesNotForceCustomInput(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	if err := findWorkflowStep(t, state, "reviewer_thinking").apply(state, "low"); err != nil {
		t.Fatalf("apply reviewer preset choice: %v", err)
	}
	if state.selections.reviewerThinkingValue() != "low" {
		t.Fatalf("expected reviewer thinking preset to be preserved, got %q", state.selections.reviewerThinkingValue())
	}
	if state.selections.supervisor.thinking.kind != onboardingReviewerThinkingOverridden {
		t.Fatal("expected non-primary reviewer preset to remain an override")
	}
	if state.selections.pendingReviewerThinking.kind != onboardingThinkingEditNone {
		t.Fatal("expected preset reviewer thinking choice not to open custom input")
	}
	if workflowIncludesStep(newOnboardingWorkflow(state).visibleSteps(state), "reviewer_thinking_custom") {
		t.Fatal("expected custom reviewer thinking step to stay hidden after preset choice")
	}
}

func TestMainThinkingChoicePreservesDisabledReviewerThinking(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	if err := findWorkflowStep(t, state, "reviewer_thinking").apply(state, "disable"); err != nil {
		t.Fatalf("apply reviewer disable choice: %v", err)
	}
	if err := findWorkflowStep(t, state, "thinking").apply(state, "high"); err != nil {
		t.Fatalf("apply main thinking choice: %v", err)
	}
	if state.selections.reviewerThinkingValue() != "" {
		t.Fatalf("expected reviewer thinking to remain disabled after main thinking change, got %q", state.selections.reviewerThinkingValue())
	}
	if state.selections.supervisor.thinking.kind != onboardingReviewerThinkingOverridden ||
		state.selections.supervisor.thinking.override.kind != onboardingThinkingDisabled {
		t.Fatal("expected explicit reviewer disable choice to remain sticky")
	}
}

func TestApplyOnboardingModelPreservesCustomReviewerOverrides(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, func(cfg *config.App) {
		cfg.Settings.Reviewer.Model = "gpt-4.1"
		cfg.Settings.Reviewer.ThinkingLevel = "low"
		cfg.Source.Sources["reviewer.model"] = "file"
		cfg.Source.Sources["reviewer.thinking_level"] = "file"
	})
	if err := state.submitPrimaryModel("gpt-5.3-codex"); err != nil {
		t.Fatalf("apply onboarding model: %v", err)
	}
	if state.selections.reviewerModelValue() != "gpt-4.1" {
		t.Fatalf("expected custom reviewer model to be preserved, got %q", state.selections.reviewerModelValue())
	}
	if state.selections.reviewerThinkingValue() != "low" {
		t.Fatalf("expected custom reviewer thinking to be preserved, got %q", state.selections.reviewerThinkingValue())
	}
}

func findWorkflowStep(t *testing.T, state *onboardingFlowState, id onboardingStepID) onboardingStepDefinition {
	t.Helper()
	if len(state.facts.Models.KnownModels) == 0 && !state.facts.Models.UnknownFallback.SupportsThinking {
		state.facts = testOnboardingCapabilityFacts()
	}
	for _, step := range newOnboardingWorkflow(state).visibleSteps(state) {
		if step.id == id {
			return step
		}
	}
	t.Fatalf("expected workflow step %q", id)
	return onboardingStepDefinition{}
}

func testOnboardingCapabilityFacts() serverapi.CapabilityFactsResponse {
	contextWindow := 272_000
	largeWindow := 400_000
	models := []serverapi.ModelCapabilityFact{
		{ModelID: ptrString("gpt-5.6-sol"), Known: true, ContextWindowTokens: &contextWindow, LargeWindow: &serverapi.ModelLargeWindowFact{Tokens: largeWindow}, SupportsThinking: true, SupportedThinkingLevels: []string{"low", "medium", "high"}, Verbosity: serverapi.ModelVerbosityFact{Supported: true, Levels: []string{"low", "medium", "high"}}},
		{ModelID: ptrString("gpt-5.3-codex"), Known: true, ContextWindowTokens: &contextWindow, SupportsThinking: true, SupportedThinkingLevels: []string{"low", "medium", "high"}, Verbosity: serverapi.ModelVerbosityFact{Supported: true, Levels: []string{"low", "medium", "high"}}},
		{ModelID: ptrString("gpt-4.1"), Known: true, ContextWindowTokens: &contextWindow, SupportsThinking: true, SupportedThinkingLevels: []string{"low", "medium", "high"}, Verbosity: serverapi.ModelVerbosityFact{Supported: true, Levels: []string{"low", "medium", "high"}}},
	}
	return serverapi.CapabilityFactsResponse{
		Models: serverapi.ModelCapabilityFacts{
			KnownModels: models,
			UnknownFallback: serverapi.ModelCapabilityFact{
				Known:                   false,
				SupportsThinking:        true,
				SupportedThinkingLevels: []string{"low", "medium", "high"},
				Verbosity:               serverapi.ModelVerbosityFact{Supported: true, Levels: []string{"low", "medium", "high"}},
			},
		},
		Providers: serverapi.ProviderCapabilityFacts{CurrentEffective: &serverapi.LLMProviderCapabilityFact{SupportsNativeCompaction: true}},
	}
}

func ptrString(value string) *string {
	return &value
}

func workflowIncludesStep(steps []onboardingStepDefinition, id onboardingStepID) bool {
	for _, step := range steps {
		if step.id == id {
			return true
		}
	}
	return false
}
