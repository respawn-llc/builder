package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
	"core/shared/textutil"

	"github.com/google/uuid"
)

type onboardingImportChoice struct {
	OptionID string
	Mode     onboardingImportMode
	Count    int
	Ref      *capabilitypb.ImportChoiceRef
}

type onboardingImportDiscovery struct {
	pending                 bool
	fromFacts               bool
	err                     error
	commandErr              error
	skipSkills              bool
	skipCommands            bool
	skillChoices            []onboardingImportChoice
	commandChoices          []onboardingImportChoice
	skillRecommendationID   string
	commandRecommendationID *string
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

func onboardingImportDiscoveryFromFacts(facts *capabilitypb.ImportFacts) onboardingImportDiscovery {
	discovery := onboardingImportDiscovery{
		fromFacts:               true,
		skillSymlinkItems:       map[onboardingImportProviderID][]onboardingSkillImportItem{},
		skillEnablementByChoice: map[string][]onboardingSkillImportItem{},
		existingSkillNames:      map[string]bool{},
	}
	discovery.skipSkills = facts.GetSkills().GetTarget().GetSkip()
	discovery.skipCommands = facts.GetCommands().GetTarget().GetSkip()
	if importErr, ok := firstImportError(facts.GetErrors(), capabilitypb.ImportItemKind_IMPORT_ITEM_KIND_SKILL, true); ok {
		discovery.err = errors.New(importErr.GetMessage())
	}
	if importErr, ok := firstImportError(facts.GetErrors(), capabilitypb.ImportItemKind_IMPORT_ITEM_KIND_COMMAND, false); ok {
		discovery.commandErr = errors.New(importErr.GetMessage())
	}
	discovery.skillChoices = importChoicesFromFacts(facts.GetSkills().GetChoices())
	discovery.skillChoices = ensureNoneImportChoice(discovery.skillChoices)
	discovery.commandChoices = ensureNoneImportChoice(importChoicesFromFacts(facts.GetCommands().GetChoices()))
	if id, ok := importChoiceIDFromRecommendation(facts.GetRecommendations().GetSkills(), discovery.skillChoices); ok {
		discovery.skillRecommendationID = id
	} else if id, ok := noneChoiceID(discovery.skillChoices); ok {
		discovery.skillRecommendationID = id
	}
	if id, ok := importChoiceIDFromRecommendation(facts.GetRecommendations().GetCommands(), discovery.commandChoices); ok {
		discovery.commandRecommendationID = textutil.Value(id)
	} else if id, ok := noneChoiceID(discovery.commandChoices); ok {
		discovery.commandRecommendationID = textutil.Value(id)
	}
	for _, item := range facts.GetSkills().GetItems() {
		converted := skillImportItemFromFact(item)
		if converted.Provider == nil {
			discovery.generatedSkillItems = append(discovery.generatedSkillItems, converted)
			continue
		}
		discovery.skillSymlinkItems[*converted.Provider] = append(discovery.skillSymlinkItems[*converted.Provider], converted)
	}
	for _, projection := range facts.GetSkillEnablement() {
		id, ok := optionIDForChoiceRef(discovery.skillChoices, projection.GetChoiceRef())
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

func firstImportError(errors []*capabilitypb.ImportErrorFact, wanted capabilitypb.ImportItemKind, includeUnscoped bool) (*capabilitypb.ImportErrorFact, bool) {
	for _, importErr := range errors {
		if importErr.ItemKind == nil {
			if includeUnscoped {
				return importErr, true
			}
			continue
		}
		if *importErr.ItemKind == wanted {
			return importErr, true
		}
	}
	return nil, false
}

func importChoiceIDFromRecommendation(recommendation *capabilitypb.ImportModeRecommendationFact, choices []onboardingImportChoice) (string, bool) {
	if recommendation == nil {
		return "", false
	}
	return optionIDForChoiceRef(choices, recommendation.GetChoiceRef())
}

func importChoicesFromFacts(facts []*capabilitypb.ImportChoiceFact) []onboardingImportChoice {
	choices := make([]onboardingImportChoice, 0, len(facts))
	for _, fact := range facts {
		choices = append(choices, onboardingImportChoice{
			OptionID: uuid.NewString(),
			Mode:     onboardingImportModeFromProto(fact.GetRef().GetMode()),
			Count:    int(fact.GetItemCount()),
			Ref:      fact.GetRef(),
		})
	}
	return choices
}

func ensureNoneImportChoice(choices []onboardingImportChoice) []onboardingImportChoice {
	if _, ok := noneChoiceID(choices); ok {
		return choices
	}
	none := onboardingImportChoice{OptionID: uuid.NewString(), Mode: onboardingImportModeNone, Ref: &capabilitypb.ImportChoiceRef{Mode: capabilitypb.ImportChoiceMode_IMPORT_CHOICE_MODE_NONE}}
	return append([]onboardingImportChoice{none}, choices...)
}

func skillImportItemFromFact(item *capabilitypb.ImportItemFact) onboardingSkillImportItem {
	ref := item.GetRef()
	provider := providerIDFromPtr(ref.ImportProviderId)
	name := ref.TargetName
	if ref.Name != nil && strings.TrimSpace(*ref.Name) != "" {
		name = *ref.Name
	}
	sourceDir := textutil.Pointer(ref.SourcePath)
	defaultEnabled := true
	if item.DefaultEnabled != nil {
		defaultEnabled = *item.DefaultEnabled
	}
	return onboardingSkillImportItem{
		ID:             uuid.NewString(),
		Provider:       provider,
		ProviderLabel:  importProviderDisplayLabel(provider, importSourceKindFromProto(ref.GetSourceKind())),
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

func importConflictWarning(conflicts []*capabilitypb.ImportConflictFact) string {
	if len(conflicts) == 0 {
		return ""
	}
	conflict := conflicts[0]
	if conflict.Path != nil && strings.TrimSpace(*conflict.Path) != "" {
		return "Duplicated in " + filepath.Base(*conflict.Path)
	}
	if conflict.ImportProviderId != nil {
		provider := onboardingImportProviderID(*conflict.ImportProviderId)
		return "Duplicated in " + importProviderDisplayLabel(&provider, importSourceKindFromProto(conflict.GetSourceKind()))
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

func optionIDForChoiceRef(choices []onboardingImportChoice, ref *capabilitypb.ImportChoiceRef) (string, bool) {
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
	for _, choice := range choices {
		candidate := onboardingImportSelection{Mode: choice.Mode, ChoiceRef: choice.Ref}
		if importSelectionsEqual(candidate, selection) {
			return choice.OptionID, true
		}
	}
	return "", false
}

func importSelectionsEqual(left, right onboardingImportSelection) bool {
	if left.Mode != right.Mode {
		return false
	}
	switch left.Mode {
	case onboardingImportModeNone:
		return true
	case onboardingImportModeSymlinkSource:
		return importChoiceRefsEqual(left.ChoiceRef, right.ChoiceRef)
	default:
		return false
	}
}

func importChoiceRefsEqual(left, right *capabilitypb.ImportChoiceRef) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Mode == right.Mode &&
		textutil.EqualOptional(left.SourceKind, right.SourceKind) &&
		textutil.EqualOptional(left.ImportProviderId, right.ImportProviderId) &&
		textutil.EqualOptional(left.SourceRootPath, right.SourceRootPath)
}

func onboardingImportModeFromProto(mode capabilitypb.ImportChoiceMode) onboardingImportMode {
	switch mode {
	case capabilitypb.ImportChoiceMode_IMPORT_CHOICE_MODE_NONE:
		return onboardingImportModeNone
	case capabilitypb.ImportChoiceMode_IMPORT_CHOICE_MODE_SYMLINK_SOURCE:
		return onboardingImportModeSymlinkSource
	default:
		return ""
	}
}

func importSourceKindFromProto(kind capabilitypb.ImportSourceKind) string {
	switch kind {
	case capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_EXTERNAL_PROVIDER:
		return "external_provider"
	case capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_GENERATED:
		return "generated"
	case capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_GLOBAL:
		return "global"
	case capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_WORKSPACE:
		return "workspace"
	default:
		return ""
	}
}

func applyImportChoice(selection *onboardingImportSelection, choiceID string, choices []onboardingImportChoice) error {
	if strings.TrimSpace(choiceID) == "" {
		return errors.New("invalid import choice")
	}
	for _, choice := range choices {
		if choice.OptionID != choiceID {
			continue
		}
		*selection = onboardingImportSelection{Mode: choice.Mode, ChoiceRef: choice.Ref}
		return nil
	}
	return fmt.Errorf("unknown import choice %q", choiceID)
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
	if selectedID, ok := optionIDForSelection(state.imports.skillChoices, state.selections.skillImport); ok {
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
			options = append(options, onboardingOption{ID: choice.OptionID, Title: fmt.Sprintf("Symlink to %s (%d found)", importProviderDisplayLabel(providerIDFromPtr(choice.Ref.ImportProviderId), "external_provider"), choice.Count)})
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

func buildCommandImportScreen(state *onboardingFlowState) onboardingScreen {
	if state.imports.skipCommands {
		return skippedImportScreen("commands_import", "Import slash commands?", "slash-command import", state.imports.commandChoices, state.imports.commandErr)
	}
	if state.imports.commandErr != nil && !hasImportChoices(state.imports.commandChoices) {
		optionID, _ := noneChoiceID(state.imports.commandChoices)
		return onboardingScreen{
			ID:              "commands_import",
			Kind:            onboardingScreenChoice,
			Title:           "Import slash commands?",
			Body:            "Kent could not inspect importable slash commands.",
			ErrorText:       state.imports.commandErr.Error(),
			Options:         []onboardingOption{{ID: optionID, Title: "Do not import"}},
			DefaultOptionID: optionID,
		}
	}
	options := make([]onboardingOption, 0, len(state.imports.commandChoices))
	for _, choice := range state.imports.commandChoices {
		switch choice.Mode {
		case onboardingImportModeNone:
			options = append(options, onboardingOption{ID: choice.OptionID, Title: "Do not import"})
		case onboardingImportModeSymlinkSource:
			if choice.Count > 0 {
				providerLabel := importProviderDisplayLabel(providerIDFromPtr(choice.Ref.ImportProviderId), "external_provider")
				options = append(options, onboardingOption{ID: choice.OptionID, Title: fmt.Sprintf("Import slash commands from %s (%d found)", providerLabel, choice.Count)})
			}
		}
	}
	if len(options) == 0 {
		optionID, _ := noneChoiceID(state.imports.commandChoices)
		screen := skippedImportScreen("commands_import", "Import slash commands?", "slash-command import", state.imports.commandChoices, state.imports.commandErr)
		if state.imports.commandErr != nil {
			screen.Body = "Kent could not inspect importable slash commands."
			screen.Options = []onboardingOption{{ID: optionID, Title: "Do not import"}}
			screen.DefaultOptionID = optionID
		}
		return screen
	}
	defaultID := options[0].ID
	if state.imports.commandRecommendationID != nil {
		defaultID = *state.imports.commandRecommendationID
	}
	if selectedID, ok := optionIDForSelection(state.imports.commandChoices, state.selections.commandImport); ok {
		defaultID = selectedID
	}
	if !containsOnboardingOption(options, defaultID) {
		defaultID = options[0].ID
	}
	screen := onboardingScreen{
		ID:              "commands_import",
		Kind:            onboardingScreenChoice,
		Title:           "Import slash commands?",
		Body:            "Kent found importable slash commands. Would you like to import them?",
		Options:         options,
		DefaultOptionID: defaultID,
	}
	if state.imports.commandErr != nil {
		screen.ErrorText = state.imports.commandErr.Error()
	}
	return screen
}

func importSkillsBody(discovery onboardingImportDiscovery) string {
	providers := make([]string, 0)
	for _, choice := range discovery.skillChoices {
		if choice.Mode == onboardingImportModeSymlinkSource && choice.Count > 0 {
			providers = append(providers, importProviderDisplayLabel(providerIDFromPtr(choice.Ref.ImportProviderId), "external_provider"))
		}
	}
	return "Kent found importable skills from " + strings.Join(providers, ", ") + ". Would you like to symlink to the other provider's directories?"
}

func skippedImportScreen(id onboardingStepID, title string, bodyKind string, choices []onboardingImportChoice, err error) onboardingScreen {
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
	if state.selections.skillImport.Mode == onboardingImportModeSymlinkSource && !state.imports.skipSkills {
		choiceID, hasChoice = optionIDForSelection(state.imports.skillChoices, state.selections.skillImport)
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
	if state.selections.skillImport.Mode != onboardingImportModeSymlinkSource {
		return ""
	}
	return fmt.Sprintf(
		"Symlink %d skills from %s",
		len(skillSelectionCandidates(state)),
		importProviderDisplayLabel(providerIDFromPtr(state.selections.skillImport.ChoiceRef.ImportProviderId), "external_provider"),
	)
}

func commandImportSummary(state *onboardingFlowState) string {
	if state.imports.skipCommands {
		return "skipped - existing found"
	}
	switch state.selections.commandImport.Mode {
	case onboardingImportModeNone:
		return "disabled"
	case onboardingImportModeSymlinkSource:
		return "from " + importProviderDisplayLabel(providerIDFromPtr(state.selections.commandImport.ChoiceRef.ImportProviderId), "external_provider")
	default:
		panic(fmt.Sprintf("invalid command import mode %q", state.selections.commandImport.Mode))
	}
}
