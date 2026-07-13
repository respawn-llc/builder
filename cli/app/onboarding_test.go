package app

import (
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/theme"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func writeOnboardingTestSkill(t *testing.T, dir string, name string, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: "+description+"\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

func skillSymlinkChoiceFact(provider string, root string, count int) serverapi.ImportChoiceFact {
	sourceKind := "external_provider"
	return serverapi.ImportChoiceFact{
		Ref: serverapi.ImportChoiceRef{
			Mode:             string(onboardingImportModeSymlinkSource),
			SourceKind:       &sourceKind,
			ImportProviderID: &provider,
			SourceRootPath:   &root,
		},
		ImportProviderID: &provider,
		SourceRootPath:   &root,
		ItemCount:        count,
	}
}

func skillItemFact(provider string, root string, path string, target string, name string, conflicts []serverapi.ImportConflictFact, enabled bool) serverapi.ImportItemFact {
	return serverapi.ImportItemFact{
		Ref: serverapi.ImportItemRef{
			ItemKind:         "skill",
			SourceKind:       "external_provider",
			ImportProviderID: &provider,
			SourceRootPath:   &root,
			SourcePath:       &path,
			TargetName:       target,
			Name:             &name,
		},
		Conflicts:      conflicts,
		DefaultEnabled: &enabled,
	}
}

func testImportProviderPtr(provider onboardingImportProviderID) *onboardingImportProviderID {
	return &provider
}

func testImportSelection(provider onboardingImportProviderID, sourceRoot string) onboardingImportSelection {
	sourceKind := "external_provider"
	providerID := string(provider)
	return onboardingImportSelection{
		Mode: onboardingImportModeSymlinkSource,
		ChoiceRef: serverapi.ImportChoiceRef{
			Mode:             string(onboardingImportModeSymlinkSource),
			SourceKind:       &sourceKind,
			ImportProviderID: &providerID,
			SourceRootPath:   &sourceRoot,
		},
	}
}

func TestOnboardingImportDiscoveryUsesServerFactsForChoicesAndCandidates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	itemPath := filepath.Join(root, "skill-creator")
	otherPath := filepath.Join(root, "skill-creator-copy")
	facts := serverapi.ImportCapabilityFacts{
		Skills: serverapi.ImportItemGroupFact{Choices: []serverapi.ImportChoiceFact{skillSymlinkChoiceFact("codex", root, 2)}},
		SkillEnablement: []serverapi.SkillEnablementProjectionFact{{
			ChoiceRef: skillSymlinkChoiceFact("codex", root, 2).Ref,
			Candidates: []serverapi.ImportItemFact{
				skillItemFact("codex", root, itemPath, "skill-creator", "skill-creator", []serverapi.ImportConflictFact{{SourceKind: "external_provider", Path: &otherPath}}, true),
				skillItemFact("codex", root, otherPath, "skill-creator", "skill-creator", []serverapi.ImportConflictFact{{SourceKind: "external_provider", Path: &itemPath}}, true),
			},
		}},
		Recommendations: serverapi.ImportRecommendationFacts{Skills: &serverapi.ImportModeRecommendationFact{ChoiceRef: skillSymlinkChoiceFact("codex", root, 2).Ref, ItemCount: 2}},
	}
	discovery := onboardingImportDiscoveryFromFacts(facts)
	choiceID := discovery.skillRecommendationID
	var selection onboardingImportSelection
	if err := applyImportChoice(&selection, choiceID, discovery.skillChoices); err != nil {
		t.Fatalf("apply import choice from facts: %v", err)
	}
	state := testOnboardingFlowStatePtr(t, nil)
	state.imports = discovery
	state.selections.skillImport = selection
	state.selections.skillEnablement = initialSkillEnablement(state)
	items := skillSelectionCandidates(state)
	if len(items) != 2 {
		t.Fatalf("expected both server-projected duplicate candidates to remain visible, got %d", len(items))
	}
	if discovery.skillRecommendationID == "" {
		t.Fatal("expected recommendation from server facts")
	}
	for _, item := range items {
		if !strings.Contains(item.Warning, "skill-creator") {
			t.Fatalf("expected warning derived from server conflict facts, got %q", item.Warning)
		}
	}
}

func TestOnboardingImportErrorsDoNotHideValidServerChoices(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	facts := serverapi.ImportCapabilityFacts{
		Skills: serverapi.ImportItemGroupFact{Choices: []serverapi.ImportChoiceFact{skillSymlinkChoiceFact("codex", root, 1)}},
		Errors: []serverapi.ImportErrorFact{{Code: "provider_discovery_failed", Scope: "provider", Operation: "discover_skills", Message: "unreadable source"}},
		Recommendations: serverapi.ImportRecommendationFacts{
			Skills: &serverapi.ImportModeRecommendationFact{ChoiceRef: skillSymlinkChoiceFact("codex", root, 1).Ref, ItemCount: 1},
		},
	}
	state := testOnboardingFlowStatePtr(t, nil)
	state.imports = onboardingImportDiscoveryFromFacts(facts)

	screen := buildSkillImportScreen(state)
	foundChoice := false
	for _, option := range screen.Options {
		for _, choice := range state.imports.skillChoices {
			if option.ID == choice.OptionID && choice.Mode == onboardingImportModeSymlinkSource && choice.Count == 1 {
				foundChoice = true
			}
		}
	}
	if !foundChoice {
		t.Fatalf("expected valid choices to remain visible despite scoped import error, got %+v", screen.Options)
	}
}

func TestOnboardingCommandImportErrorsDoNotSurfaceInSkillsFlow(t *testing.T) {
	commandKind := serverapi.ImportErrorItemKindCommand
	facts := serverapi.ImportCapabilityFacts{
		Errors: []serverapi.ImportErrorFact{{
			Code:      "provider_discovery_failed",
			Scope:     "provider",
			ItemKind:  &commandKind,
			Operation: "discover_commands",
			Message:   "unreadable commands",
		}},
	}
	discovery := onboardingImportDiscoveryFromFacts(facts)
	if discovery.err != nil {
		t.Fatalf("command-only import error must not become a skill error: %v", discovery.err)
	}
	state := testOnboardingFlowStatePtr(t, nil)
	state.imports = discovery
	for _, step := range newOnboardingWorkflow(state).steps {
		if step.id == "skills_import" && step.visible(state) {
			t.Fatal("command-only import error must not make the skills import step visible")
		}
	}
}

func TestOnboardingImportTargetSkipFactsHideImportSteps(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	facts := serverapi.ImportCapabilityFacts{
		Skills: serverapi.ImportItemGroupFact{
			Choices: []serverapi.ImportChoiceFact{skillSymlinkChoiceFact("codex", root, 1)},
			Target:  serverapi.ImportTargetFact{Skip: true, Conflicts: []serverapi.ImportConflictFact{{SourceKind: "global"}}},
		},
	}
	state := testOnboardingFlowStatePtr(t, nil)
	state.imports = onboardingImportDiscoveryFromFacts(facts)

	for _, step := range newOnboardingWorkflow(state).steps {
		if step.id == "skills_import" && step.visible(state) {
			t.Fatalf("expected server target skip facts to hide skill import step")
		}
	}
}

func TestOnboardingSkippedImportErrorScreenCanContinueWithNoneChoice(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	facts := serverapi.ImportCapabilityFacts{
		Skills: serverapi.ImportItemGroupFact{
			Choices: []serverapi.ImportChoiceFact{skillSymlinkChoiceFact("codex", root, 1)},
			Target:  serverapi.ImportTargetFact{Skip: true, Conflicts: []serverapi.ImportConflictFact{{SourceKind: "global"}}},
		},
		Errors: []serverapi.ImportErrorFact{{Code: "target_read_failed", Scope: "target", Operation: "read_import_target", Message: "permission denied"}},
	}
	state := testOnboardingFlowStatePtr(t, nil)
	state.imports = onboardingImportDiscoveryFromFacts(facts)
	screen := buildSkillImportScreen(state)
	if len(screen.Options) != 1 {
		t.Fatalf("expected only none option for skipped import, got %+v", screen.Options)
	}
	if err := applyImportChoice(&state.selections.skillImport, screen.Options[0].ID, state.imports.skillChoices); err != nil {
		t.Fatalf("expected skipped none choice to be accepted: %v", err)
	}
	if state.selections.skillImport.Mode != onboardingImportModeNone {
		t.Fatalf("expected none selection, got %+v", state.selections.skillImport)
	}
}

func TestApplyImportChoiceRejectsRemovedCopyModes(t *testing.T) {
	selection := onboardingImportSelection{}
	if err := applyImportChoice(&selection, "copy:claude_code", nil); err == nil {
		t.Fatal("expected removed copy mode to be rejected")
	}
	if err := applyImportChoice(&selection, "merge", nil); err == nil {
		t.Fatal("expected removed merge mode to be rejected")
	}
}

func TestOnboardingModelBackspaceTogglesMultiSelect(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, func(cfg *config.App) { cfg.Settings.Theme = theme.Dark }))
	model.currentScreen = onboardingScreen{
		ID:        "skills_enabled",
		Kind:      onboardingScreenMulti,
		Title:     "Choose enabled skills",
		Options:   []onboardingOption{{ID: "one", Title: "One"}},
		Selection: map[string]bool{"one": true},
	}
	model.selection = map[string]bool{"one": true}
	model.cursor = 0
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	updated := next.(*onboardingModel)
	if updated.selection["one"] {
		t.Fatal("expected backspace to toggle the current multi-select option off")
	}
}

func TestOnboardingModelCtrlHTogglesMultiSelect(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, func(cfg *config.App) { cfg.Settings.Theme = theme.Dark }))
	model.currentScreen = onboardingScreen{
		ID:        "skills_enabled",
		Kind:      onboardingScreenMulti,
		Title:     "Choose enabled skills",
		Options:   []onboardingOption{{ID: "one", Title: "One"}},
		Selection: map[string]bool{"one": true},
	}
	model.selection = map[string]bool{"one": true}
	model.cursor = 0
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlH})
	updated := next.(*onboardingModel)
	if updated.selection["one"] {
		t.Fatal("expected ctrl+h to toggle the current multi-select option off")
	}
}

func TestBuildSkillSelectionScreenAddsToggleAllOptionWhenThereAreMoreThanTwoItems(t *testing.T) {
	choiceID := "test-choice"
	sourceRoot := t.TempDir()
	state := testOnboardingFlowStatePtr(t, nil)
	state.selections.skillImport = testImportSelection(onboardingImportProviderCodex, sourceRoot)
	state.imports = onboardingImportDiscovery{skillEnablementByChoice: map[string][]onboardingSkillImportItem{
		choiceID: {
			{ID: "codex:one", Provider: testImportProviderPtr(onboardingImportProviderCodex), ProviderLabel: "Codex", TargetDirName: "one", SkillName: "one", DefaultEnabled: true},
			{ID: "codex:two", Provider: testImportProviderPtr(onboardingImportProviderCodex), ProviderLabel: "Codex", TargetDirName: "two", SkillName: "two", DefaultEnabled: true},
			{ID: "codex:three", Provider: testImportProviderPtr(onboardingImportProviderCodex), ProviderLabel: "Codex", TargetDirName: "three", SkillName: "three", DefaultEnabled: true},
		},
	}, skillChoices: []onboardingImportChoice{
		{OptionID: choiceID, Mode: onboardingImportModeSymlinkSource, Ref: testImportSelection(onboardingImportProviderCodex, sourceRoot).ChoiceRef},
	}}
	state.selections.skillEnablement = initialSkillEnablement(state)
	screen := buildSkillSelectionScreen(state)
	if len(screen.Options) == 0 || screen.Options[0].ID != onboardingToggleAllOptionID {
		t.Fatalf("expected first option to be toggle-all action, got %+v", screen.Options)
	}
}

func TestBuildSkillSelectionScreenShowsGeneratedSkillsWithoutImport(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	state.imports = onboardingImportDiscovery{generatedSkillItems: []onboardingSkillImportItem{
		{ID: "generated:kent-dogfooding", ProviderLabel: "Preinstalled", TargetDirName: "kent-dogfooding", SkillName: "kent-dogfooding", DefaultEnabled: true},
		{ID: "generated:creating-skills", ProviderLabel: "Preinstalled", TargetDirName: "creating-skills", SkillName: "creating-skills", DefaultEnabled: true},
	}}
	state.selections.skillEnablement = initialSkillEnablement(state)
	screen := buildSkillSelectionScreen(state)
	if len(screen.Options) != 2 {
		t.Fatalf("expected generated skills as selectable options, got %+v", screen.Options)
	}
	for _, want := range []string{"generated:kent-dogfooding", "generated:creating-skills"} {
		found := false
		for _, option := range screen.Options {
			if option.ID == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected option ID %q, got %+v", want, screen.Options)
		}
	}
}

func TestDisabledOnboardingSkillNamesCanDisableGeneratedSkillWithoutImport(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	state.imports = onboardingImportDiscovery{generatedSkillItems: []onboardingSkillImportItem{
		{ID: "generated:kent-dogfooding", ProviderLabel: "Preinstalled", TargetDirName: "kent-dogfooding", SkillName: "kent-dogfooding", DefaultEnabled: true},
		{ID: "generated:creating-skills", ProviderLabel: "Preinstalled", TargetDirName: "creating-skills", SkillName: "creating-skills", DefaultEnabled: true},
	}}
	state.selections.skillEnablement = initialSkillEnablement(state)
	state.selections.skillEnablement = map[string]bool{
		"generated:kent-dogfooding": true,
		"generated:creating-skills": false,
	}
	disabled := disabledOnboardingSkillNames(*state)
	if len(disabled) != 1 || disabled[0] != "creating-skills" {
		t.Fatalf("disabled generated skills = %+v, want creating-skills", disabled)
	}
}

func TestGeneratedSkillSelectionCountsWithoutImport(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	state.imports = onboardingImportDiscovery{generatedSkillItems: []onboardingSkillImportItem{
		{ID: "generated:kent-dogfooding", ProviderLabel: "Preinstalled", TargetDirName: "kent-dogfooding", SkillName: "kent-dogfooding", DefaultEnabled: true},
		{ID: "generated:creating-skills", ProviderLabel: "Preinstalled", TargetDirName: "creating-skills", SkillName: "creating-skills", DefaultEnabled: true},
	}}
	state.selections.skillEnablement = map[string]bool{
		"generated:kent-dogfooding": true,
		"generated:creating-skills": false,
	}
	enabled, disabled := selectedSkillCounts(state)
	if enabled != 1 || disabled != 1 {
		t.Fatalf("generated skill counts = (%d, %d), want (1, 1)", enabled, disabled)
	}
	if state.selections.skillImport.Mode != onboardingImportModeNone {
		t.Fatalf("generated-only selection unexpectedly changed import mode: %+v", state.selections.skillImport)
	}
}

func TestOnboardingModelToggleAllHotkeyTogglesMultiSelection(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, func(cfg *config.App) { cfg.Settings.Theme = theme.Dark }))
	model.currentScreen = onboardingScreen{
		ID:      "skills_enabled",
		Kind:    onboardingScreenMulti,
		Title:   "Choose enabled skills",
		Options: []onboardingOption{{ID: onboardingToggleAllOptionID, Title: "Disable all"}, {ID: "one", Title: "One"}, {ID: "two", Title: "Two"}, {ID: "three", Title: "Three"}},
	}
	model.selection = map[string]bool{"one": true, "two": true, "three": true}
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	updated := next.(*onboardingModel)
	for _, id := range []string{"one", "two", "three"} {
		if updated.selection[id] {
			t.Fatalf("expected %q to be toggled off", id)
		}
	}
	if updated.selection[onboardingToggleAllOptionID] {
		t.Fatal("toggle-all selection must be unchecked when selectable options are disabled")
	}
}

func TestOnboardingModelToggleAllMenuItemTogglesMultiSelection(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, func(cfg *config.App) { cfg.Settings.Theme = theme.Dark }))
	model.currentScreen = onboardingScreen{
		ID:      "skills_enabled",
		Kind:    onboardingScreenMulti,
		Title:   "Choose enabled skills",
		Options: []onboardingOption{{ID: onboardingToggleAllOptionID, Title: "Disable all"}, {ID: "one", Title: "One"}, {ID: "two", Title: "Two"}, {ID: "three", Title: "Three"}},
	}
	model.selection = map[string]bool{"one": true, "two": true, "three": true}
	model.cursor = 0
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
	updated := next.(*onboardingModel)
	for _, id := range []string{"one", "two", "three"} {
		if updated.selection[id] {
			t.Fatalf("expected %q to be toggled off", id)
		}
	}
}

func TestOnboardingModelRefreshToggleAllTracksCheckedState(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, func(cfg *config.App) { cfg.Settings.Theme = theme.Dark }))
	model.currentScreen = onboardingScreen{
		ID:      "skills_enabled",
		Kind:    onboardingScreenMulti,
		Title:   "Choose enabled skills",
		Options: []onboardingOption{{ID: onboardingToggleAllOptionID, Title: "Disable all"}, {ID: "one", Title: "One"}, {ID: "two", Title: "Two"}},
	}
	model.selection = map[string]bool{"one": true, "two": true}
	model.refreshToggleAllOption()
	if !model.selection[onboardingToggleAllOptionID] {
		t.Fatal("expected toggle-all action to render checked when all options are enabled")
	}

	model.selection["two"] = false
	model.refreshToggleAllOption()
	if model.selection[onboardingToggleAllOptionID] {
		t.Fatal("expected toggle-all action to render unchecked when not all options are enabled")
	}
}

func TestOnboardingSubmitCurrentScreenShowsValidationError(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", testOnboardingFlowState(t, nil))
	model.stepIndex = 2
	model.syncScreen(true)
	model.input.Replace(strings.NewReplacer("\r", "", "\n", "").Replace(""))
	next, _ := model.submitCurrentScreen()
	updated := next.(*onboardingModel)
	if updated.errorText == "" {
		t.Fatal("expected submit validation error to be captured")
	}
	if updated.currentScreen.ErrorText == "" {
		t.Fatal("expected submit validation error to be shown on the current screen")
	}
}

func TestOnboardingWorkflowStartsWithThemeStep(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	workflow := newOnboardingWorkflow(state)
	steps := workflow.visibleSteps(state)
	if len(steps) == 0 {
		t.Fatal("expected onboarding workflow to include steps")
	}
	if steps[0].id != "theme" {
		t.Fatalf("expected first onboarding step to be theme, got %q", steps[0].id)
	}
}

func TestThemeStepDefaultsToDetectedTheme(t *testing.T) {
	original := lipgloss.HasDarkBackground()
	defer lipgloss.SetHasDarkBackground(original)

	lipgloss.SetHasDarkBackground(false)
	lightState := testOnboardingFlowStatePtr(t, nil)
	lightScreen := newOnboardingWorkflow(lightState).visibleSteps(lightState)[0].build(lightState)
	if lightScreen.DefaultOptionID != "light" {
		t.Fatalf("expected light background detection to preselect light theme, got %q", lightScreen.DefaultOptionID)
	}

	lipgloss.SetHasDarkBackground(true)
	darkState := testOnboardingFlowStatePtr(t, nil)
	darkScreen := newOnboardingWorkflow(darkState).visibleSteps(darkState)[0].build(darkState)
	if darkScreen.DefaultOptionID != "dark" {
		t.Fatalf("expected dark background detection to preselect dark theme, got %q", darkScreen.DefaultOptionID)
	}
}

func TestThemeStepChoicePreservesAutoWhenKeepingDetectedDefault(t *testing.T) {
	original := lipgloss.HasDarkBackground()
	defer lipgloss.SetHasDarkBackground(original)

	lipgloss.SetHasDarkBackground(true)
	state := testOnboardingFlowStatePtr(t, nil)
	themeStep := newOnboardingWorkflow(state).visibleSteps(state)[0]
	if err := themeStep.apply(state, "dark"); err != nil {
		t.Fatalf("apply detected theme choice: %v", err)
	}
	if state.selections.theme.kind != onboardingThemeAuto {
		t.Fatalf("expected detected default to preserve auto, got %q", state.selections.theme.kind)
	}

	lipgloss.SetHasDarkBackground(false)
	state = testOnboardingFlowStatePtr(t, nil)
	themeStep = newOnboardingWorkflow(state).visibleSteps(state)[0]
	if err := themeStep.apply(state, "dark"); err != nil {
		t.Fatalf("apply explicit override: %v", err)
	}
	if state.selections.theme.kind != onboardingThemeDark {
		t.Fatalf("expected overriding detected default to persist explicit dark, got %q", state.selections.theme.kind)
	}
}
