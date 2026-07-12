package app

import (
	"fmt"
	"strings"

	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/theme"
	"core/shared/toolspec"
)

type onboardingScreenKind string

const (
	onboardingScreenChoice  onboardingScreenKind = "choice"
	onboardingScreenInput   onboardingScreenKind = "input"
	onboardingScreenMulti   onboardingScreenKind = "multi"
	onboardingScreenLoading onboardingScreenKind = "loading"
)

type onboardingOption struct {
	ID          string
	Title       string
	Description string
	Warning     string
	Group       string
}

type onboardingScreen struct {
	ID              string
	Kind            onboardingScreenKind
	Title           string
	Body            string
	Helper          string
	ThemePreview    bool
	Options         []onboardingOption
	DefaultOptionID string
	InputValue      string
	Placeholder     string
	SensitiveInput  bool
	LoadingText     string
	LoadingDoneText string
	ErrorText       string
	ContinueLabel   string
	Selection       map[string]bool
}

type onboardingPendingAction string

const (
	onboardingPendingActionNone          onboardingPendingAction = ""
	onboardingPendingActionWriteDefaults onboardingPendingAction = "write_defaults"
	onboardingPendingActionWriteCustom   onboardingPendingAction = "write_custom"
	onboardingPendingActionRestart       onboardingPendingAction = "restart"
)

type onboardingImportProviderID string

const (
	onboardingImportProviderClaudeCode onboardingImportProviderID = "claude_code"
	onboardingImportProviderCodex      onboardingImportProviderID = "codex"
	onboardingImportProviderAgents     onboardingImportProviderID = "agents"
)

type onboardingImportMode string

const (
	onboardingImportModeNone          onboardingImportMode = "none"
	onboardingImportModeSymlinkSource onboardingImportMode = "symlink_source"
)

type onboardingImportSelection struct {
	Mode       onboardingImportMode
	Provider   *onboardingImportProviderID
	SourceRoot *string
	ChoiceRef  serverapi.ImportChoiceRef
}

type onboardingFlowState struct {
	settings                    config.Settings
	baselineSettings            config.Settings
	theme                       string
	facts                       serverapi.CapabilityFactsResponse
	pendingAction               onboardingPendingAction
	customThinking              bool
	reviewerCustomModel         bool
	reviewerCustomThinking      bool
	reviewerCustomThinkingInput bool
	reviewerThinkingDisabled    bool
	skillImport                 onboardingImportSelection
	skillSelection              map[string]bool
	imports                     onboardingImportDiscovery
}

type onboardingResult struct {
	Completed            bool
	CreatedDefaultConfig bool
	SettingsPath         string
	EffectiveTheme       string
}

func applyOnboardingModel(state *onboardingFlowState, value string) error {
	model := strings.TrimSpace(value)
	if model == "" {
		return fmt.Errorf("model must not be empty")
	}
	state.settings.Model = model
	applyDerivedModelContextBudgetFromFacts(state, model)
	if !modelSupportsVerbosity(state, model) {
		state.settings.ModelVerbosity = config.ModelVerbosity("")
	} else if strings.TrimSpace(string(state.settings.ModelVerbosity)) == "" {
		state.settings.ModelVerbosity = config.ModelVerbosityLow
	}
	if !modelSupportsThinking(state, model) {
		state.customThinking = false
		state.settings.ThinkingLevel = ""
	}
	applyContextWindowChoice(state, "default")
	syncReviewerDefaultsFromPrimary(state)
	return nil
}

func reviewerEnabled(state *onboardingFlowState) bool {
	mode := strings.TrimSpace(state.settings.Reviewer.Frequency)
	return mode != "" && mode != "off"
}

func syncReviewerDefaultsFromPrimary(state *onboardingFlowState) {
	if !state.reviewerCustomModel {
		state.settings.Reviewer.Model = state.settings.Model
	}
	syncReviewerThinkingToPrimary(state)
}

func syncReviewerThinkingToPrimary(state *onboardingFlowState) {
	if !modelSupportsThinking(state, state.settings.Reviewer.Model) {
		state.reviewerCustomThinking = false
		state.reviewerCustomThinkingInput = false
		state.settings.Reviewer.ThinkingLevel = ""
		return
	}
	if state.reviewerThinkingDisabled {
		state.reviewerCustomThinking = false
		state.reviewerCustomThinkingInput = false
		state.settings.Reviewer.ThinkingLevel = ""
		return
	}
	if !state.reviewerCustomThinking {
		state.settings.Reviewer.ThinkingLevel = state.settings.ThinkingLevel
	}
}

func applyOnboardingThemeChoice(state *onboardingFlowState, choiceID string) {
	normalizedChoice := theme.Normalize(choiceID)
	if !theme.IsExplicit(state.settings.Theme) && normalizedChoice == theme.Resolve(theme.Auto) {
		state.settings.Theme = theme.Auto
		state.theme = theme.Auto
		return
	}
	state.settings.Theme = normalizedChoice
	state.theme = normalizedChoice
}

func applyContextWindowChoice(state *onboardingFlowState, choiceID string) {
	modelFact := modelFactFor(state, state.settings.Model)
	if modelFact.ContextWindowTokens == nil || *modelFact.ContextWindowTokens <= 0 {
		return
	}
	window := *modelFact.ContextWindowTokens
	if choiceID == "large" && modelFact.LargeWindow != nil && modelFact.LargeWindow.Tokens > 0 {
		window = modelFact.LargeWindow.Tokens
	}
	state.settings.ModelContextWindow = window
	state.settings.ContextCompactionThresholdTokens = window * 95 / 100
}

func reviewSummaryLines(state *onboardingFlowState) []string {
	themeSummary := theme.Auto + " (" + theme.Resolve(state.settings.Theme) + ")"
	if theme.IsExplicit(state.settings.Theme) {
		themeSummary = theme.Resolve(state.settings.Theme)
	}
	lines := []string{
		"Review your first-time setup choices.",
		"",
		"- Theme: `" + themeSummary + "`",
		"- Model: `" + state.settings.Model + "`",
	}
	modelFact := modelFactFor(state, state.settings.Model)
	if modelFact.ContextWindowTokens != nil && *modelFact.ContextWindowTokens > 0 {
		if state.settings.ModelContextWindow == *modelFact.ContextWindowTokens {
			lines = append(lines, "- Context window: `default ("+formatTokenWindow(*modelFact.ContextWindowTokens)+")`")
		} else {
			lines = append(lines, "- Context window: `"+formatTokenWindow(state.settings.ModelContextWindow)+"`")
		}
	}
	thinking := strings.TrimSpace(state.settings.ThinkingLevel)
	if thinking == "" {
		thinking = "off"
	}
	verbosity := string(state.settings.ModelVerbosity)
	if strings.TrimSpace(verbosity) == "" {
		verbosity = "off"
	}
	questions := "off"
	if state.settings.EnabledTools[toolspec.ToolAskQuestion] {
		questions = "on"
	}
	supervisor := state.settings.Reviewer.Frequency
	if strings.TrimSpace(supervisor) == "" {
		supervisor = "off"
	}
	lines = append(lines,
		"- Thinking: `"+thinking+"`",
		"- Verbosity: `"+verbosity+"`",
		"- Questions: `"+questions+"`",
		"- Supervisor: `"+supervisor+"`",
		"- Compaction: `"+string(state.settings.CompactionMode)+"`",
	)
	if reviewerEnabled(state) {
		reviewerThinking := strings.TrimSpace(state.settings.Reviewer.ThinkingLevel)
		if reviewerThinking == "" {
			reviewerThinking = "off"
		}
		lines = append(lines,
			"- Supervisor model: `"+state.settings.Reviewer.Model+"`",
			"- Supervisor thinking: `"+reviewerThinking+"`",
		)
	}
	if summary := skillImportSummary(state); summary != "" {
		lines = append(lines, "- Skills import: `"+summary+"`")
	}
	if enabled, disabled := selectedSkillCounts(state); enabled > 0 || disabled > 0 {
		lines = append(lines, fmt.Sprintf("- Enabled skills: `%d enabled, %d disabled`", enabled, disabled))
	}
	return lines
}

func applyDerivedModelContextBudgetFromFacts(state *onboardingFlowState, model string) {
	modelFact := modelFactFor(state, model)
	if modelFact.ContextWindowTokens != nil && *modelFact.ContextWindowTokens > 0 {
		state.settings.ModelContextWindow = *modelFact.ContextWindowTokens
		state.settings.ContextCompactionThresholdTokens = *modelFact.ContextWindowTokens * 95 / 100
		return
	}
	state.settings.ModelContextWindow = state.baselineSettings.ModelContextWindow
	state.settings.ContextCompactionThresholdTokens = state.baselineSettings.ContextCompactionThresholdTokens
}

func modelFactFor(state *onboardingFlowState, model string) serverapi.ModelCapabilityFact {
	trimmed := strings.TrimSpace(model)
	for _, fact := range state.facts.Models.KnownModels {
		if fact.ModelID != nil && strings.EqualFold(strings.TrimSpace(*fact.ModelID), trimmed) {
			return fact
		}
	}
	return state.facts.Models.UnknownFallback
}

func modelSupportsLargeContextWindow(state *onboardingFlowState, model string) bool {
	fact := modelFactFor(state, model)
	return fact.ContextWindowTokens != nil && *fact.ContextWindowTokens > 0 && fact.LargeWindow != nil && fact.LargeWindow.Tokens > 0
}

func modelSupportsThinking(state *onboardingFlowState, model string) bool {
	return modelFactFor(state, model).SupportsThinking
}

func modelThinkingLevels(state *onboardingFlowState, model string) []string {
	return append([]string(nil), modelFactFor(state, model).SupportedThinkingLevels...)
}

func modelSupportsVerbosity(state *onboardingFlowState, model string) bool {
	return modelFactFor(state, model).Verbosity.Supported
}

func modelVerbosityLevels(state *onboardingFlowState, model string) []string {
	return append([]string(nil), modelFactFor(state, model).Verbosity.Levels...)
}

func titleCaseASCII(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
}

func containsOnboardingOption(options []onboardingOption, target string) bool {
	for _, option := range options {
		if option.ID == target {
			return true
		}
	}
	return false
}

func isKnownThinkingLevel(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func titleCaseThinking(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "xhigh":
		return "Extra high"
	case "max":
		return "Max"
	case "ultra":
		return "Ultra"
	case "":
		return ""
	default:
		return strings.Title(strings.ToLower(strings.TrimSpace(level)))
	}
}

func formatTokenWindow(tokens int) string {
	if tokens >= 1_000_000 {
		return fmt.Sprintf("%dm", tokens/1_000_000)
	}
	if tokens >= 1_000 {
		return fmt.Sprintf("%dk", tokens/1_000)
	}
	return fmt.Sprintf("%d", tokens)
}

func cloneSelection(selection map[string]bool) map[string]bool {
	if len(selection) == 0 {
		return nil
	}
	cloned := make(map[string]bool, len(selection))
	for key, value := range selection {
		cloned[key] = value
	}
	return cloned
}

func buildSkillToggles(state *onboardingFlowState, selection map[string]bool) map[string]bool {
	if len(selection) == 0 {
		return nil
	}
	toggles := map[string]bool{}
	for _, item := range skillSelectionCandidates(state) {
		if selection[item.ID] {
			continue
		}
		toggles[item.SkillName] = false
	}
	if len(toggles) == 0 {
		return nil
	}
	return toggles
}

func selectedSkillCounts(state *onboardingFlowState) (int, int) {
	selected := effectiveSkillSelection(state)
	enabled := 0
	disabled := 0
	for _, item := range skillSelectionCandidates(state) {
		if selected[item.ID] {
			enabled++
		} else {
			disabled++
		}
	}
	return enabled, disabled
}

func effectiveSkillSelection(state *onboardingFlowState) map[string]bool {
	items := skillSelectionCandidates(state)
	selection := cloneSelection(state.skillSelection)
	if selection == nil {
		selection = make(map[string]bool, len(items))
	}
	for _, item := range items {
		if _, ok := selection[item.ID]; !ok {
			selection[item.ID] = item.DefaultEnabled
		}
	}
	return selection
}

func thinkingLevelEstimate(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "disable":
		return ""
	case "low":
		return "Lowest reasoning budget. Best for quick edits and straightforward tasks."
	case "medium":
		return "Balanced reasoning budget. Good default for most work."
	case "high":
		return "Heavier reasoning budget. Better for deeper planning and harder bugs."
	case "xhigh":
		return "Maximum reasoning budget. Slowest and costliest, for the hardest tasks."
	case "max":
		return "Maximum reasoning depth for the hardest problems."
	case "ultra":
		return "Maximum reasoning with automatic task delegation."
	default:
		return ""
	}
}
