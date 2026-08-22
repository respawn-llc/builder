package app

import (
	"core/shared/config"
	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
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
		wantCursor    int
	}{
		{
			name:          "alt-left",
			key:           tea.KeyMsg{Type: tea.KeyLeft, Alt: true},
			initialCursor: func(text string) int { return len([]rune(text)) },
			wantCursor:    len([]rune("alpha beta ")),
		},
		{
			name:          "alt-right",
			key:           tea.KeyMsg{Type: tea.KeyRight, Alt: true},
			initialCursor: func(string) int { return 0 },
			wantCursor:    len([]rune("alpha")),
		},
		{
			name:          "alt-b",
			key:           tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'b'}},
			initialCursor: func(text string) int { return len([]rune(text)) },
			wantCursor:    len([]rune("alpha beta ")),
		},
		{
			name:          "alt-f",
			key:           tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'f'}},
			initialCursor: func(string) int { return 0 },
			wantCursor:    len([]rune("alpha")),
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
			if got, want := runeOffsetForByteCursor(updated.input.Text(), updated.input.Cursor()), tt.wantCursor; got != want {
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

func TestOnboardingSpinnerSchedulingTracksScreenState(t *testing.T) {
	tests := []struct {
		name        string
		configure   func(*onboardingModel)
		reschedules bool
	}{
		{name: "idle", configure: func(model *onboardingModel) {
			model.state.imports.pending = false
			model.syncScreen(true)
		}},
		{name: "loading", configure: func(model *onboardingModel) {
			model.currentScreen = onboardingScreen{Kind: onboardingScreenLoading}
		}, reschedules: true},
		{name: "finalizing", configure: func(model *onboardingModel) {
			model.state.imports.pending = false
			model.syncScreen(true)
			model.finalizing = true
		}, reschedules: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, func(cfg *config.App) { cfg.Settings.Theme = "dark" }))
			tt.configure(model)
			next, cmd := model.Update(onboardingSpinnerTickMsg{at: model.spinnerClock.anchor.Add(spinnerTickInterval)})
			if next.(*onboardingModel).spinnerFrame == 0 {
				t.Fatal("spinner tick did not advance")
			}
			if got := cmd != nil; got != tt.reschedules {
				t.Fatalf("spinner rescheduled = %t, want %t", got, tt.reschedules)
			}
		})
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

func testOnboardingCapabilityFacts() *capabilitypb.Facts {
	contextWindow := uint32(272_000)
	models := []*capabilitypb.ModelFact{
		{ModelId: ptrString("gpt-5.6-sol"), Known: true, ContextWindowTokens: &contextWindow, LargeWindow: &capabilitypb.ModelLargeWindowFact{Tokens: 400_000}, SupportsThinking: true, SupportedThinkingLevels: []string{"low", "medium", "high"}, Verbosity: &capabilitypb.ModelVerbosityFact{Supported: true, Source: "catalog", Levels: []string{"low", "medium", "high"}}},
		{ModelId: ptrString("gpt-5.3-codex"), Known: true, ContextWindowTokens: &contextWindow, SupportsThinking: true, SupportedThinkingLevels: []string{"low", "medium", "high"}, Verbosity: &capabilitypb.ModelVerbosityFact{Supported: true, Source: "catalog", Levels: []string{"low", "medium", "high"}}},
		{ModelId: ptrString("gpt-4.1"), Known: true, ContextWindowTokens: &contextWindow, SupportsThinking: true, SupportedThinkingLevels: []string{"low", "medium", "high"}, Verbosity: &capabilitypb.ModelVerbosityFact{Supported: true, Source: "catalog", Levels: []string{"low", "medium", "high"}}},
	}
	facts := emptyOnboardingCapabilityFacts()
	facts.Models = &capabilitypb.ModelFacts{
		KnownModels: models,
		UnknownFallback: &capabilitypb.ModelFact{
			Known:                   false,
			SupportsThinking:        true,
			SupportedThinkingLevels: []string{"low", "medium", "high"},
			Verbosity:               &capabilitypb.ModelVerbosityFact{Supported: true, Source: "catalog", Levels: []string{"low", "medium", "high"}},
		},
	}
	facts.Providers.CurrentEffective = &capabilitypb.ProviderFact{
		LlmProviderId:            "openai",
		Role:                     "main",
		SupportsNativeCompaction: true,
	}
	return facts
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
