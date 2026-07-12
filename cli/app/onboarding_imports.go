package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"core/shared/serverapi"
	"core/shared/valuecopy"

	"github.com/google/uuid"
)

type onboardingImportChoice struct {
	OptionID   string
	Mode       onboardingImportMode
	Provider   *onboardingImportProviderID
	SourceRoot *string
	Count      int
	Ref        serverapi.ImportChoiceRef
}

type onboardingImportDiscovery struct {
	pending                 bool
	fromFacts               bool
	err                     error
	skipSkills              bool
	skillChoices            []onboardingImportChoice
	skillRecommendationID   string
	skillSymlinkItems       map[onboardingImportProviderID][]onboardingSkillImportItem
	skillEnablementByChoice map[string][]onboardingSkillImportItem
	generatedSkillItems     []onboardingSkillImportItem
	existingSkillNames      map[string]bool
}

type onboardingSkillImportItem struct {
	ID             string
	Provider       *onboardingImportProviderID
	ProviderLabel  string
	SourceDir      *string
	TargetDirName  string
	SkillName      string
	DefaultEnabled bool
	Warning        string
}

type onboardingImportDiscoveryDoneMsg struct {
	discovery onboardingImportDiscovery
}

func onboardingImportDiscoveryFromFacts(facts serverapi.ImportCapabilityFacts) onboardingImportDiscovery {
	discovery := onboardingImportDiscovery{
		fromFacts:               true,
		skillSymlinkItems:       map[onboardingImportProviderID][]onboardingSkillImportItem{},
		skillEnablementByChoice: map[string][]onboardingSkillImportItem{},
		existingSkillNames:      map[string]bool{},
	}
	discovery.skipSkills = facts.Skills.Target.Skip
	if importErr, ok := skillImportError(facts.Errors); ok {
		discovery.err = errors.New(importErr.Message)
	}
	discovery.skillChoices = importChoicesFromFacts(facts.Skills.Choices)
	discovery.skillChoices = ensureNoneImportChoice(discovery.skillChoices)
	if id, ok := importChoiceIDFromRecommendation(facts.Recommendations.Skills, discovery.skillChoices); ok {
		discovery.skillRecommendationID = id
	} else if id, ok := noneChoiceID(discovery.skillChoices); ok {
		discovery.skillRecommendationID = id
	}
	for _, item := range facts.Skills.Items {
		converted := skillImportItemFromFact(item)
		if converted.Provider == nil {
			discovery.generatedSkillItems = append(discovery.generatedSkillItems, converted)
			continue
		}
		discovery.skillSymlinkItems[*converted.Provider] = append(discovery.skillSymlinkItems[*converted.Provider], converted)
	}
	for _, projection := range facts.SkillEnablement {
		id, ok := optionIDForChoiceRef(discovery.skillChoices, projection.ChoiceRef)
		if !ok {
			continue
		}
		items := make([]onboardingSkillImportItem, 0, len(projection.Candidates))
		for _, item := range projection.Candidates {
			items = append(items, skillImportItemFromFact(item))
		}
		discovery.skillEnablementByChoice[id] = items
	}
	return discovery
}

func skillImportError(errors []serverapi.ImportErrorFact) (serverapi.ImportErrorFact, bool) {
	for _, importErr := range errors {
		if importErr.ItemKind == nil || *importErr.ItemKind == serverapi.ImportErrorItemKindSkill {
			return importErr, true
		}
	}
	return serverapi.ImportErrorFact{}, false
}

func importChoiceIDFromRecommendation(recommendation *serverapi.ImportModeRecommendationFact, choices []onboardingImportChoice) (string, bool) {
	if recommendation == nil {
		return "", false
	}
	return optionIDForChoiceRef(choices, recommendation.ChoiceRef)
}

func importChoicesFromFacts(facts []serverapi.ImportChoiceFact) []onboardingImportChoice {
	choices := make([]onboardingImportChoice, 0, len(facts))
	for _, fact := range facts {
		provider := providerIDFromPtr(fact.ImportProviderID)
		sourceRoot := valuecopy.Pointer(fact.SourceRootPath)
		choices = append(choices, onboardingImportChoice{
			OptionID:   uuid.NewString(),
			Mode:       onboardingImportMode(fact.Ref.Mode),
			Provider:   provider,
			SourceRoot: sourceRoot,
			Count:      fact.ItemCount,
			Ref:        fact.Ref,
		})
	}
	return choices
}

func ensureNoneImportChoice(choices []onboardingImportChoice) []onboardingImportChoice {
	if _, ok := noneChoiceID(choices); ok {
		return choices
	}
	none := onboardingImportChoice{OptionID: uuid.NewString(), Mode: onboardingImportModeNone, Ref: serverapi.ImportChoiceRef{Mode: string(onboardingImportModeNone)}}
	return append([]onboardingImportChoice{none}, choices...)
}

func skillImportItemFromFact(item serverapi.ImportItemFact) onboardingSkillImportItem {
	ref := item.Ref
	provider := providerIDFromPtr(ref.ImportProviderID)
	name := ref.TargetName
	if ref.Name != nil && strings.TrimSpace(*ref.Name) != "" {
		name = *ref.Name
	}
	sourceDir := valuecopy.Pointer(ref.SourcePath)
	defaultEnabled := true
	if item.DefaultEnabled != nil {
		defaultEnabled = *item.DefaultEnabled
	}
	return onboardingSkillImportItem{
		ID:             uuid.NewString(),
		Provider:       provider,
		ProviderLabel:  importProviderDisplayLabel(provider, ref.SourceKind),
		SourceDir:      sourceDir,
		TargetDirName:  ref.TargetName,
		SkillName:      name,
		DefaultEnabled: defaultEnabled,
		Warning:        importConflictWarning(item.Conflicts),
	}
}

func importProviderDisplayLabel(provider *onboardingImportProviderID, sourceKind string) string {
	if sourceKind == "generated" {
		return "Preinstalled"
	}
	if provider == nil {
		return strings.TrimSpace(sourceKind)
	}
	switch *provider {
	case onboardingImportProviderClaudeCode:
		return "Claude Code"
	case onboardingImportProviderCodex:
		return "Codex"
	case onboardingImportProviderAgents:
		return "Agents"
	default:
		return string(*provider)
	}
}

func importConflictWarning(conflicts []serverapi.ImportConflictFact) string {
	if len(conflicts) == 0 {
		return ""
	}
	conflict := conflicts[0]
	if conflict.Path != nil && strings.TrimSpace(*conflict.Path) != "" {
		return "Duplicated in " + filepath.Base(*conflict.Path)
	}
	if conflict.ImportProviderID != nil {
		provider := onboardingImportProviderID(*conflict.ImportProviderID)
		return "Duplicated in " + importProviderDisplayLabel(&provider, conflict.SourceKind)
	}
	return "Duplicated"
}

func providerIDFromPtr(value *string) *onboardingImportProviderID {
	if value == nil {
		return nil
	}
	provider := onboardingImportProviderID(*value)
	return &provider
}

func hasImportChoices(choices []onboardingImportChoice) bool {
	for _, choice := range choices {
		if choice.Mode == onboardingImportModeSymlinkSource && choice.Count > 0 {
			return true
		}
	}
	return false
}

func optionIDForChoiceRef(choices []onboardingImportChoice, ref serverapi.ImportChoiceRef) (string, bool) {
	for _, choice := range choices {
		if importChoiceRefsEqual(choice.Ref, ref) {
			return choice.OptionID, true
		}
	}
	return "", false
}

func noneChoiceID(choices []onboardingImportChoice) (string, bool) {
	for _, choice := range choices {
		if choice.Mode == onboardingImportModeNone {
			return choice.OptionID, true
		}
	}
	return "", false
}

func optionIDForSelection(choices []onboardingImportChoice, selection onboardingImportSelection) (string, bool) {
	selection = normalizeImportSelection(selection)
	for _, choice := range choices {
		if choice.Mode != selection.Mode {
			continue
		}
		if choice.Mode == onboardingImportModeNone {
			return choice.OptionID, true
		}
		if ptrEqual(choice.Provider, selection.Provider) && ptrStringEqual(choice.SourceRoot, selection.SourceRoot) {
			return choice.OptionID, true
		}
	}
	return "", false
}

func importChoiceRefsEqual(left, right serverapi.ImportChoiceRef) bool {
	return left.Mode == right.Mode &&
		ptrStringEqual(left.SourceKind, right.SourceKind) &&
		ptrStringEqual(left.ImportProviderID, right.ImportProviderID) &&
		ptrStringEqual(left.SourceRootPath, right.SourceRootPath)
}

func ptrEqual[T comparable](left, right *T) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func ptrStringEqual(left, right *string) bool {
	return ptrEqual(left, right)
}

func applyImportChoice(selection *onboardingImportSelection, choiceID string, choices []onboardingImportChoice) error {
	if strings.TrimSpace(choiceID) == "" {
		return errors.New("invalid import choice")
	}
	for _, choice := range choices {
		if choice.OptionID != choiceID {
			continue
		}
		next := onboardingImportSelection{Mode: choice.Mode, ChoiceRef: choice.Ref}
		if choice.Provider != nil {
			provider := *choice.Provider
			next.Provider = &provider
		}
		next.SourceRoot = valuecopy.Pointer(choice.SourceRoot)
		*selection = next
		return nil
	}
	return fmt.Errorf("unknown import choice %q", choiceID)
}

func normalizeImportSelection(selection onboardingImportSelection) onboardingImportSelection {
	if strings.TrimSpace(string(selection.Mode)) == "" {
		selection.Mode = onboardingImportModeNone
	}
	return selection
}

func buildSkillImportScreen(state *onboardingFlowState) onboardingScreen {
	if state.imports.pending {
		return onboardingScreen{ID: "skills_import", Kind: onboardingScreenLoading, Title: "Import skills?", LoadingText: "Scanning skills..."}
	}
	if state.imports.skipSkills {
		return skippedImportScreen("skills_import", "Import skills?", "skills import", state.imports.skillChoices, state.imports.err)
	}
	if state.imports.err != nil && !hasImportChoices(state.imports.skillChoices) {
		optionID, _ := noneChoiceID(state.imports.skillChoices)
		return onboardingScreen{ID: "skills_import", Kind: onboardingScreenChoice, Title: "Import skills?", Body: "Kent could not inspect importable skills on this machine.", ErrorText: state.imports.err.Error(), Options: []onboardingOption{{ID: optionID, Title: "Do not import"}}, DefaultOptionID: optionID}
	}
	defaultID := state.imports.skillRecommendationID
	if selectedID, ok := optionIDForSelection(state.imports.skillChoices, state.skillImport); ok {
		defaultID = selectedID
	}
	options := make([]onboardingOption, 0, len(state.imports.skillChoices))
	for _, choice := range state.imports.skillChoices {
		switch choice.Mode {
		case onboardingImportModeNone:
			options = append(options, onboardingOption{ID: choice.OptionID, Title: "Do not import"})
		case onboardingImportModeSymlinkSource:
			if choice.Count == 0 {
				continue
			}
			options = append(options, onboardingOption{ID: choice.OptionID, Title: fmt.Sprintf("Symlink to %s (%d found)", importProviderDisplayLabel(choice.Provider, "external_provider"), choice.Count)})
		}
	}
	if len(options) == 0 {
		return skippedImportScreen("skills_import", "Import skills?", "skills import", state.imports.skillChoices, state.imports.err)
	}
	if !containsOnboardingOption(options, defaultID) {
		defaultID = options[0].ID
	}
	screen := onboardingScreen{ID: "skills_import", Kind: onboardingScreenChoice, Title: "Import skills?", Body: importSkillsBody(state.imports), Options: options, DefaultOptionID: defaultID}
	if state.imports.err != nil {
		screen.ErrorText = state.imports.err.Error()
	}
	return screen
}

func importSkillsBody(discovery onboardingImportDiscovery) string {
	providers := make([]string, 0)
	for _, choice := range discovery.skillChoices {
		if choice.Mode == onboardingImportModeSymlinkSource && choice.Count > 0 {
			providers = append(providers, importProviderDisplayLabel(choice.Provider, "external_provider"))
		}
	}
	return "Kent found importable skills from " + strings.Join(providers, ", ") + ". Would you like to symlink to the other provider's directories?"
}

func skippedImportScreen(id string, title string, bodyKind string, choices []onboardingImportChoice, err error) onboardingScreen {
	optionID, ok := noneChoiceID(choices)
	if !ok {
		optionID = uuid.NewString()
	}
	screen := onboardingScreen{ID: id, Kind: onboardingScreenChoice, Title: title, Body: "Kent skipped " + bodyKind + " because an existing target was found.", Options: []onboardingOption{{ID: optionID, Title: "Do not import"}}, DefaultOptionID: optionID}
	if err != nil {
		screen.ErrorText = err.Error()
	}
	return screen
}
func buildSkillSelectionScreen(state *onboardingFlowState) onboardingScreen {
	items := skillSelectionCandidates(state)
	selection := effectiveSkillSelection(state)
	body := "Pick skills to keep enabled for now. Kent will write config toggles for the unchecked skills."
	options := make([]onboardingOption, 0, len(items))
	if len(items) > 2 {
		title := "Enable all"
		if allSkillItemsSelected(items, selection) {
			title = "Disable all"
		}
		options = append(options, onboardingOption{ID: onboardingToggleAllOptionID, Title: title})
	}
	for _, item := range items {
		options = append(options, onboardingOption{ID: item.ID, Title: item.ProviderLabel + " / " + item.TargetDirName, Group: item.ProviderLabel, Warning: item.Warning})
	}
	return onboardingScreen{ID: "skills_enabled", Kind: onboardingScreenMulti, Title: "Choose enabled skills", Body: body, Options: options, Selection: selection}
}

func allSkillItemsSelected(items []onboardingSkillImportItem, selection map[string]bool) bool {
	for _, item := range items {
		if !selection[item.ID] {
			return false
		}
	}
	return len(items) > 0
}

func skillSelectionCandidates(state *onboardingFlowState) []onboardingSkillImportItem {
	choiceID, hasChoice := noneChoiceID(state.imports.skillChoices)
	if state.skillImport.Mode == onboardingImportModeSymlinkSource && !state.imports.skipSkills {
		choiceID, hasChoice = optionIDForSelection(state.imports.skillChoices, state.skillImport)
	}
	if hasChoice {
		if items, ok := state.imports.skillEnablementByChoice[choiceID]; ok {
			return items
		}
	}
	return state.imports.generatedSkillItems
}

func skillImportSummary(state *onboardingFlowState) string {
	if state.imports.skipSkills {
		return "skipped - existing found"
	}
	if state.skillImport.Mode != onboardingImportModeSymlinkSource {
		return ""
	}
	return fmt.Sprintf("Symlink %d skills from %s", len(skillSelectionCandidates(state)), importProviderDisplayLabel(state.skillImport.Provider, "external_provider"))
}
