package app

import (
	"fmt"
	"maps"
	"strings"

	"core/shared/serverapi"
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
	ID              onboardingStepID
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
	onboardingPendingActionNone          onboardingPendingAction = "none"
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
	Mode      onboardingImportMode
	ChoiceRef serverapi.ImportChoiceRef
}

type onboardingFlowState struct {
	selections    onboardingSelections
	facts         serverapi.CapabilityFactsResponse
	pendingAction onboardingPendingAction
	imports       onboardingImportDiscovery
	debug         bool
}

type onboardingResult struct {
	Completed            bool
	CreatedDefaultConfig bool
	SettingsPath         string
	EffectiveTheme       string
}

func reviewerEnabled(state *onboardingFlowState) bool {
	return state.selections.supervisor.frequency != onboardingSupervisorOff
}

func modelFactFor(state *onboardingFlowState, model string) serverapi.ModelCapabilityFact {
	return modelFactForFacts(state.facts, model)
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
	return maps.Clone(selection)
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
	return cloneSelection(state.selections.skillEnablement)
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
