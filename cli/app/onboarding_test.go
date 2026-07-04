package app

import (
	"context"
	"errors"

	"core/cli/app/internal/onboarding"
	"core/prompts"
	"core/server/runtime"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/theme"
	"core/shared/toolspec"
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
		Mode:       onboardingImportModeSymlinkSource,
		Provider:   &provider,
		SourceRoot: &sourceRoot,
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
	state := &onboardingFlowState{imports: discovery, skillImport: selection}
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
	state := &onboardingFlowState{imports: onboardingImportDiscoveryFromFacts(facts)}

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

func TestOnboardingImportTargetSkipFactsHideImportSteps(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	facts := serverapi.ImportCapabilityFacts{
		Skills: serverapi.ImportItemGroupFact{
			Choices: []serverapi.ImportChoiceFact{skillSymlinkChoiceFact("codex", root, 1)},
			Target:  serverapi.ImportTargetFact{Skip: true, Conflicts: []serverapi.ImportConflictFact{{SourceKind: "global"}}},
		},
	}
	state := &onboardingFlowState{imports: onboardingImportDiscoveryFromFacts(facts)}

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
	state := &onboardingFlowState{imports: onboardingImportDiscoveryFromFacts(facts)}
	screen := buildSkillImportScreen(state)
	if len(screen.Options) != 1 {
		t.Fatalf("expected only none option for skipped import, got %+v", screen.Options)
	}
	if err := applyImportChoice(&state.skillImport, screen.Options[0].ID, state.imports.skillChoices); err != nil {
		t.Fatalf("expected skipped none choice to be accepted: %v", err)
	}
	if state.skillImport.Mode != onboardingImportModeNone {
		t.Fatalf("expected none selection, got %+v", state.skillImport)
	}
}

func TestExecuteSkillImportSymlinksRootDirectory(t *testing.T) {
	home := newAppTestHome(t)
	globalRoot := t.TempDir()
	sourceDir := filepath.Join(home, ".codex", "skills", "local")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if _, err := executeSkillImport(globalRoot, onboardingImportDiscovery{}, testImportSelection(onboardingImportProviderCodex, sourceDir)); err != nil {
		t.Fatalf("execute skill import: %v", err)
	}
	targetPath := filepath.Join(globalRoot, "skills")
	info, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("lstat target: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %s to be a symlink", targetPath)
	}
	resolved, err := os.Readlink(targetPath)
	if err != nil {
		t.Fatalf("readlink target: %v", err)
	}
	if resolved != sourceDir {
		t.Fatalf("expected skills root symlink to point to %q, got %q", sourceDir, resolved)
	}
}

func TestExecuteSkillImportSymlinksAgentsRootDirectory(t *testing.T) {
	home := newAppTestHome(t)
	globalRoot := t.TempDir()
	sourceDir := filepath.Join(home, ".agents", "skills")
	writeOnboardingTestSkill(t, filepath.Join(sourceDir, "demo-skill"), "demo", "from agents")
	if _, err := executeSkillImport(globalRoot, onboardingImportDiscovery{}, testImportSelection(onboardingImportProviderAgents, sourceDir)); err != nil {
		t.Fatalf("execute skill import: %v", err)
	}
	targetPath := filepath.Join(globalRoot, "skills")
	resolved, err := os.Readlink(targetPath)
	if err != nil {
		t.Fatalf("readlink target: %v", err)
	}
	if resolved != sourceDir {
		t.Fatalf("expected agents skills root symlink to point to %q, got %q", sourceDir, resolved)
	}
}

func TestExecuteSkillImportReplacesEmptyTargetDirectory(t *testing.T) {
	home := newAppTestHome(t)
	globalRoot := t.TempDir()
	sourceDir := filepath.Join(home, ".codex", "skills", "local")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	targetPath := filepath.Join(globalRoot, "skills")
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		t.Fatalf("mkdir empty target: %v", err)
	}

	if _, err := executeSkillImport(globalRoot, onboardingImportDiscovery{}, testImportSelection(onboardingImportProviderCodex, sourceDir)); err != nil {
		t.Fatalf("execute skill import with empty target: %v", err)
	}
	info, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("lstat target: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %s to be replaced with a symlink", targetPath)
	}
}

func TestExecuteSkillImportDoesNotDeleteEmptyTargetWhenSourceValidationFails(t *testing.T) {
	globalRoot := t.TempDir()
	targetPath := filepath.Join(globalRoot, "skills")
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		t.Fatalf("mkdir empty target: %v", err)
	}

	_, err := executeSkillImport(globalRoot, onboardingImportDiscovery{}, testImportSelection(onboardingImportProviderCodex, filepath.Join(t.TempDir(), "missing-skills")))
	if err == nil {
		t.Fatal("expected missing skill source to fail")
	}
	info, statErr := os.Lstat(targetPath)
	if statErr != nil {
		t.Fatalf("expected empty target directory to remain after source validation failure: %v", statErr)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected %s to remain a plain directory, got mode %v", targetPath, info.Mode())
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

func TestExecuteCommandImportSymlinksRootDirectory(t *testing.T) {
	home := newAppTestHome(t)
	globalRoot := t.TempDir()
	sourceDir := filepath.Join(home, ".claude", "commands")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "review.md"), []byte("review"), 0o644); err != nil {
		t.Fatalf("write source command: %v", err)
	}
	if _, err := executeCommandImport(globalRoot, onboardingImportDiscovery{}, testImportSelection(onboardingImportProviderClaudeCode, sourceDir)); err != nil {
		t.Fatalf("execute command import: %v", err)
	}
	targetPath := filepath.Join(globalRoot, "prompts")
	info, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("lstat target: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %s to be a symlink", targetPath)
	}
	resolved, err := os.Readlink(targetPath)
	if err != nil {
		t.Fatalf("readlink target: %v", err)
	}
	if resolved != sourceDir {
		t.Fatalf("expected prompts root symlink to point to %q, got %q", sourceDir, resolved)
	}
}

func TestExecuteCommandImportSymlinksAgentsRootDirectory(t *testing.T) {
	home := newAppTestHome(t)
	globalRoot := t.TempDir()
	sourceDir := filepath.Join(home, ".agents", "commands")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "review.md"), []byte("review"), 0o644); err != nil {
		t.Fatalf("write source command: %v", err)
	}
	if _, err := executeCommandImport(globalRoot, onboardingImportDiscovery{}, testImportSelection(onboardingImportProviderAgents, sourceDir)); err != nil {
		t.Fatalf("execute command import: %v", err)
	}
	targetPath := filepath.Join(globalRoot, "prompts")
	resolved, err := os.Readlink(targetPath)
	if err != nil {
		t.Fatalf("readlink target: %v", err)
	}
	if resolved != sourceDir {
		t.Fatalf("expected agents prompts root symlink to point to %q, got %q", sourceDir, resolved)
	}
}

func TestExecuteCommandImportValidatesSourceDirectory(t *testing.T) {
	globalRoot := t.TempDir()
	missingSource := filepath.Join(t.TempDir(), "missing-prompts")
	_, err := executeCommandImport(globalRoot, onboardingImportDiscovery{}, testImportSelection(onboardingImportProviderClaudeCode, missingSource))
	if err == nil {
		t.Fatal("expected missing command source to fail")
	}
	if !errors.Is(err, onboarding.ErrSourceDirectoryInvalid) {
		t.Fatalf("expected source validation error, got %v", err)
	}
}

func TestExecuteCommandImportDoesNotDeleteEmptyTargetWhenSourceValidationFails(t *testing.T) {
	globalRoot := t.TempDir()
	targetPath := filepath.Join(globalRoot, "prompts")
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		t.Fatalf("mkdir empty target: %v", err)
	}

	_, err := executeCommandImport(globalRoot, onboardingImportDiscovery{}, testImportSelection(onboardingImportProviderClaudeCode, filepath.Join(t.TempDir(), "missing-prompts")))
	if err == nil {
		t.Fatal("expected missing command source to fail")
	}
	info, statErr := os.Lstat(targetPath)
	if statErr != nil {
		t.Fatalf("expected empty target directory to remain after source validation failure: %v", statErr)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected %s to remain a plain directory, got mode %v", targetPath, info.Mode())
	}
}

func TestExecuteCommandImportFallsBackToPromptsWhenCommandsHasNoDirectMarkdown(t *testing.T) {
	home := newAppTestHome(t)
	globalRoot := t.TempDir()
	commandsDir := filepath.Join(home, ".claude", "commands")
	promptsDir := filepath.Join(home, ".claude", "prompts")
	if err := os.MkdirAll(filepath.Join(commandsDir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested commands: %v", err)
	}
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "nested", "ignored.md"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write nested command: %v", err)
	}
	if err := os.WriteFile(filepath.Join(promptsDir, "review.md"), []byte("prompts"), 0o644); err != nil {
		t.Fatalf("write prompt command: %v", err)
	}

	if _, err := executeCommandImport(globalRoot, onboardingImportDiscovery{}, testImportSelection(onboardingImportProviderClaudeCode, promptsDir)); err != nil {
		t.Fatalf("execute command import: %v", err)
	}
	targetPath := filepath.Join(globalRoot, "prompts")
	resolved, err := os.Readlink(targetPath)
	if err != nil {
		t.Fatalf("readlink target: %v", err)
	}
	if resolved != promptsDir {
		t.Fatalf("expected prompts root symlink to point to %q, got %q", promptsDir, resolved)
	}
}

func TestExecuteCommandImportFallsBackToAgentsPromptsWhenCommandsMissing(t *testing.T) {
	home := newAppTestHome(t)
	globalRoot := t.TempDir()
	promptsDir := filepath.Join(home, ".agents", "prompts")
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(promptsDir, "review.md"), []byte("prompts"), 0o644); err != nil {
		t.Fatalf("write prompt command: %v", err)
	}

	if _, err := executeCommandImport(globalRoot, onboardingImportDiscovery{}, testImportSelection(onboardingImportProviderAgents, promptsDir)); err != nil {
		t.Fatalf("execute command import: %v", err)
	}
	targetPath := filepath.Join(globalRoot, "prompts")
	resolved, err := os.Readlink(targetPath)
	if err != nil {
		t.Fatalf("readlink target: %v", err)
	}
	if resolved != promptsDir {
		t.Fatalf("expected prompts root symlink to point to %q, got %q", promptsDir, resolved)
	}
}

func TestExecuteOnboardingImportsTreatsZeroValueModesAsNone(t *testing.T) {
	rollback, err := executeOnboardingImports(t.TempDir(), onboardingFlowState{})
	if err != nil {
		t.Fatalf("execute onboarding imports: %v", err)
	}
	if rollback == nil {
		t.Fatal("expected rollback func")
	}
}

func TestOnboardingModelBackspaceTogglesMultiSelect(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", onboardingFlowState{theme: "dark"})
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
	model := newOnboardingModelForWorkspace(t.TempDir(), "", onboardingFlowState{theme: "dark"})
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
	state := &onboardingFlowState{
		skillImport: testImportSelection(onboardingImportProviderCodex, sourceRoot),
		imports: onboardingImportDiscovery{skillEnablementByChoice: map[string][]onboardingSkillImportItem{
			choiceID: {
				{ID: "codex:one", Provider: testImportProviderPtr(onboardingImportProviderCodex), ProviderLabel: "Codex", TargetDirName: "one", SkillName: "one", DefaultEnabled: true},
				{ID: "codex:two", Provider: testImportProviderPtr(onboardingImportProviderCodex), ProviderLabel: "Codex", TargetDirName: "two", SkillName: "two", DefaultEnabled: true},
				{ID: "codex:three", Provider: testImportProviderPtr(onboardingImportProviderCodex), ProviderLabel: "Codex", TargetDirName: "three", SkillName: "three", DefaultEnabled: true},
			},
		}, skillChoices: []onboardingImportChoice{
			{OptionID: choiceID, Mode: onboardingImportModeSymlinkSource, Provider: testImportProviderPtr(onboardingImportProviderCodex), SourceRoot: &sourceRoot, Ref: testImportSelection(onboardingImportProviderCodex, sourceRoot).ChoiceRef},
		}},
	}
	screen := buildSkillSelectionScreen(state)
	if len(screen.Options) == 0 || screen.Options[0].ID != onboardingToggleAllOptionID {
		t.Fatalf("expected first option to be toggle-all action, got %+v", screen.Options)
	}
	if screen.Options[0].Title != "Disable all" {
		t.Fatalf("expected initial toggle-all label to disable all, got %q", screen.Options[0].Title)
	}
}

func TestBuildSkillSelectionScreenShowsGeneratedSkillsWithoutImport(t *testing.T) {
	state := &onboardingFlowState{
		imports: onboardingImportDiscovery{generatedSkillItems: []onboardingSkillImportItem{
			{ID: "generated:kent-dogfooding", ProviderLabel: "Preinstalled", TargetDirName: "kent-dogfooding", SkillName: "kent-dogfooding", DefaultEnabled: true},
			{ID: "generated:creating-skills", ProviderLabel: "Preinstalled", TargetDirName: "creating-skills", SkillName: "creating-skills", DefaultEnabled: true},
		}},
		skillImport: onboardingImportSelection{Mode: onboardingImportModeNone},
	}
	screen := buildSkillSelectionScreen(state)
	if len(screen.Options) != 2 {
		t.Fatalf("expected generated skills as selectable options, got %+v", screen.Options)
	}
	for _, want := range []string{"Preinstalled / kent-dogfooding", "Preinstalled / creating-skills"} {
		found := false
		for _, option := range screen.Options {
			if option.Title == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected option %q, got %+v", want, screen.Options)
		}
	}
}

func TestBuildSkillTogglesCanDisableGeneratedSkillWithoutImport(t *testing.T) {
	state := &onboardingFlowState{
		imports: onboardingImportDiscovery{generatedSkillItems: []onboardingSkillImportItem{
			{ID: "generated:kent-dogfooding", ProviderLabel: "Preinstalled", TargetDirName: "kent-dogfooding", SkillName: "kent-dogfooding", DefaultEnabled: true},
			{ID: "generated:creating-skills", ProviderLabel: "Preinstalled", TargetDirName: "creating-skills", SkillName: "creating-skills", DefaultEnabled: true},
		}},
		skillImport: onboardingImportSelection{Mode: onboardingImportModeNone},
	}
	toggles := buildSkillToggles(state, map[string]bool{
		"generated:kent-dogfooding": true,
		"generated:creating-skills": false,
	})
	disabled, ok := toggles["creating-skills"]
	if len(toggles) != 1 || !ok || disabled {
		t.Fatalf("expected disabled generated skill toggle, got %+v", toggles)
	}
}

func TestReviewSummaryIncludesGeneratedSkillSelectionWithoutImport(t *testing.T) {
	state := &onboardingFlowState{
		settings: config.Settings{
			Theme:        theme.Auto,
			Model:        "gpt-5.5",
			EnabledTools: map[toolspec.ID]bool{},
		},
		imports: onboardingImportDiscovery{generatedSkillItems: []onboardingSkillImportItem{
			{ID: "generated:kent-dogfooding", ProviderLabel: "Preinstalled", TargetDirName: "kent-dogfooding", SkillName: "kent-dogfooding", DefaultEnabled: true},
			{ID: "generated:creating-skills", ProviderLabel: "Preinstalled", TargetDirName: "creating-skills", SkillName: "creating-skills", DefaultEnabled: true},
		}},
		skillSelection: map[string]bool{
			"generated:kent-dogfooding": true,
			"generated:creating-skills": false,
		},
	}
	lines := reviewSummaryLines(state)
	hasEnabledLine := false
	for _, line := range lines {
		switch line {
		case "- Enabled skills: `1 enabled, 1 disabled`":
			hasEnabledLine = true
		case "- Skills import:":
			t.Fatalf("did not expect import summary when only generated skills were configured, got %q", lines)
		}
	}
	if !hasEnabledLine {
		t.Fatalf("expected generated skill counts in review summary, got %q", lines)
	}
}

func TestOnboardingFinalWritePersistsDisabledGeneratedSkillAndRuntimeHonorsIt(t *testing.T) {
	home := newAppTestHome(t)
	if _, err := prompts.GeneratedSync(context.Background(), prompts.GeneratedSyncOptions{HomeDir: home, FS: prompts.GeneratedSkillsFS}); err != nil {
		t.Fatalf("sync generated skills: %v", err)
	}
	defaultCfg, err := config.LoadGlobal(config.LoadOptions{})
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	state := onboardingFlowState{
		settings: defaultCfg.Settings,
		imports: onboardingImportDiscovery{generatedSkillItems: []onboardingSkillImportItem{
			{ID: "generated:kent-dogfooding", ProviderLabel: "Preinstalled", TargetDirName: "kent-dogfooding", SkillName: "kent-dogfooding", DefaultEnabled: true},
			{ID: "generated:creating-skills", ProviderLabel: "Preinstalled", TargetDirName: "creating-skills", SkillName: "creating-skills", DefaultEnabled: true},
		}},
		skillSelection: map[string]bool{
			"generated:kent-dogfooding": false,
			"generated:creating-skills": true,
		},
	}
	state.settings.SkillToggles = buildSkillToggles(&state, state.skillSelection)
	model := newOnboardingModelForWorkspace(filepath.Join(home, config.ConfigDirName), "", state)
	msg := model.finalizeCmd(false)()
	done, ok := msg.(onboardingFinalizeDoneMsg)
	if !ok {
		t.Fatalf("expected onboarding finalize message, got %T", msg)
	}
	if done.err != nil {
		t.Fatalf("finalize onboarding: %v", done.err)
	}
	cfg, err := config.LoadGlobal(config.LoadOptions{})
	if err != nil {
		t.Fatalf("load written config: %v", err)
	}
	disabled := config.DisabledSkillToggles(cfg.Settings)
	if !disabled["kent-dogfooding"] {
		t.Fatalf("expected disabled generated skill in loaded config, got toggles=%+v disabled=%+v", cfg.Settings.SkillToggles, disabled)
	}
	inspections, err := runtime.InspectSkills("", "", disabled)
	if err != nil {
		t.Fatalf("inspect skills: %v", err)
	}
	foundDisabled := false
	for _, inspection := range inspections {
		if inspection.SourceKind == "generated" && inspection.Name == "kent-dogfooding" {
			foundDisabled = inspection.Disabled
		}
	}
	if !foundDisabled {
		t.Fatalf("expected runtime inspection to mark generated kent-dogfooding disabled, got %+v", inspections)
	}
}

func TestOnboardingModelToggleAllHotkeyTogglesMultiSelection(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", onboardingFlowState{theme: "dark"})
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
	if updated.currentScreen.Options[0].Title != "Enable all" {
		t.Fatalf("expected toggle-all label to update after hotkey, got %q", updated.currentScreen.Options[0].Title)
	}
}

func TestOnboardingModelToggleAllMenuItemTogglesMultiSelection(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", onboardingFlowState{theme: "dark"})
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
	model := newOnboardingModelForWorkspace(t.TempDir(), "", onboardingFlowState{theme: "dark"})
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
	if got := model.currentScreen.Options[0].Title; got != "Disable all" {
		t.Fatalf("expected toggle-all title to stay on Disable all, got %q", got)
	}

	model.selection["two"] = false
	model.refreshToggleAllOption()
	if model.selection[onboardingToggleAllOptionID] {
		t.Fatal("expected toggle-all action to render unchecked when not all options are enabled")
	}
	if got := model.currentScreen.Options[0].Title; got != "Enable all" {
		t.Fatalf("expected toggle-all title to switch to Enable all, got %q", got)
	}
}

func TestOnboardingSubmitCurrentScreenShowsValidationError(t *testing.T) {
	model := newOnboardingModelForWorkspace(t.TempDir(), "", onboardingFlowState{})
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
	workflow := newOnboardingWorkflow(&onboardingFlowState{})
	steps := workflow.visibleSteps(&onboardingFlowState{})
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
	lightState := &onboardingFlowState{}
	lightScreen := newOnboardingWorkflow(lightState).visibleSteps(lightState)[0].build(lightState)
	if lightScreen.DefaultOptionID != "light" {
		t.Fatalf("expected light background detection to preselect light theme, got %q", lightScreen.DefaultOptionID)
	}

	lipgloss.SetHasDarkBackground(true)
	darkState := &onboardingFlowState{}
	darkScreen := newOnboardingWorkflow(darkState).visibleSteps(darkState)[0].build(darkState)
	if darkScreen.DefaultOptionID != "dark" {
		t.Fatalf("expected dark background detection to preselect dark theme, got %q", darkScreen.DefaultOptionID)
	}
}

func TestThemeStepChoicePreservesAutoWhenKeepingDetectedDefault(t *testing.T) {
	original := lipgloss.HasDarkBackground()
	defer lipgloss.SetHasDarkBackground(original)

	lipgloss.SetHasDarkBackground(true)
	state := &onboardingFlowState{settings: config.Settings{Theme: theme.Auto}}
	themeStep := newOnboardingWorkflow(state).visibleSteps(state)[0]
	if err := themeStep.apply(state, "dark"); err != nil {
		t.Fatalf("apply detected theme choice: %v", err)
	}
	if state.settings.Theme != theme.Auto {
		t.Fatalf("expected detected default to preserve auto, got %q", state.settings.Theme)
	}

	lipgloss.SetHasDarkBackground(false)
	state = &onboardingFlowState{settings: config.Settings{Theme: theme.Auto}}
	themeStep = newOnboardingWorkflow(state).visibleSteps(state)[0]
	if err := themeStep.apply(state, "dark"); err != nil {
		t.Fatalf("apply explicit override: %v", err)
	}
	if state.settings.Theme != theme.Dark {
		t.Fatalf("expected overriding detected default to persist explicit dark, got %q", state.settings.Theme)
	}
}

func TestOnboardingDefaultsPathPersistsChosenTheme(t *testing.T) {
	newAppTestHome(t)
	model := newOnboardingModelForWorkspace(t.TempDir(), "", onboardingFlowState{settings: config.Settings{Theme: "light"}, theme: "light"})
	msg := model.finalizeCmd(true)()
	done, ok := msg.(onboardingFinalizeDoneMsg)
	if !ok {
		t.Fatalf("expected onboarding finalize message, got %T", msg)
	}
	if done.err != nil {
		t.Fatalf("finalize defaults path: %v", done.err)
	}
	if !done.result.Completed || !done.result.CreatedDefaultConfig {
		t.Fatalf("expected defaults path to create config, got %+v", done.result)
	}
	contents, err := os.ReadFile(done.result.SettingsPath)
	if err != nil {
		t.Fatalf("read written settings: %v", err)
	}
	if !strings.Contains(string(contents), "theme = \"light\"") {
		t.Fatalf("expected defaults path to persist chosen theme, got %q", string(contents))
	}
}

func TestOnboardingDefaultsPathWritesIntoThreadedSettingsRoot(t *testing.T) {
	newAppTestHome(t)
	root := t.TempDir()
	wantPath := filepath.Join(root, "config.toml")
	model := newOnboardingModelForWorkspace(root, "", onboardingFlowState{settings: config.Settings{Theme: "light"}, theme: "light"})
	model.settingsPath = wantPath

	msg := model.finalizeCmd(true)()
	done, ok := msg.(onboardingFinalizeDoneMsg)
	if !ok {
		t.Fatalf("expected onboarding finalize message, got %T", msg)
	}
	if done.err != nil {
		t.Fatalf("finalize defaults path: %v", done.err)
	}
	if done.result.SettingsPath != wantPath {
		t.Fatalf("expected settings written to threaded root %q, got %q", wantPath, done.result.SettingsPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected config.toml in threaded root: %v", err)
	}
}

func TestOnboardingCustomPathWritesIntoThreadedSettingsRoot(t *testing.T) {
	newAppTestHome(t)
	root := t.TempDir()
	wantPath := filepath.Join(root, "config.toml")
	baseline, err := config.LoadGlobal(config.LoadOptions{})
	if err != nil {
		t.Fatalf("load baseline settings: %v", err)
	}
	model := newOnboardingModelForWorkspace(root, "", onboardingFlowState{settings: baseline.Settings, theme: "light"})
	model.settingsPath = wantPath

	msg := model.finalizeCmd(false)()
	done, ok := msg.(onboardingFinalizeDoneMsg)
	if !ok {
		t.Fatalf("expected onboarding finalize message, got %T", msg)
	}
	if done.err != nil {
		t.Fatalf("finalize custom path: %v", done.err)
	}
	if done.result.SettingsPath != wantPath {
		t.Fatalf("expected settings written to threaded root %q, got %q", wantPath, done.result.SettingsPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected config.toml in threaded root: %v", err)
	}
}
