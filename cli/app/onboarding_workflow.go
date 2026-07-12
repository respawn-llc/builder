package app

import (
	"fmt"
	"strings"

	"core/shared/config"
	"core/shared/theme"
	"core/shared/toolspec"
)

type onboardingStepDefinition struct {
	id               string
	visible          func(*onboardingFlowState) bool
	build            func(*onboardingFlowState) onboardingScreen
	apply            func(*onboardingFlowState, string) error
	applyMultiSelect func(*onboardingFlowState, map[string]bool) error
}

type onboardingWorkflow struct {
	steps []onboardingStepDefinition
}

func (w onboardingWorkflow) visibleSteps(state *onboardingFlowState) []onboardingStepDefinition {
	visible := make([]onboardingStepDefinition, 0, len(w.steps))
	for _, step := range w.steps {
		if step.visible == nil || step.visible(state) {
			visible = append(visible, step)
		}
	}
	return visible
}

func newOnboardingWorkflow(state *onboardingFlowState) onboardingWorkflow {
	return onboardingWorkflow{steps: []onboardingStepDefinition{
		onboardingStepDefinition{
			id: "theme",
			build: func(state *onboardingFlowState) onboardingScreen {
				defaultOption := theme.Resolve(state.settings.Theme)
				return onboardingScreen{
					ID:              "theme",
					Kind:            onboardingScreenChoice,
					Title:           "Choose a theme",
					Body:            "Pick the theme Kent should use. The preview updates as you move. If you keep the detected default, Kent stays on auto.",
					ThemePreview:    true,
					DefaultOptionID: defaultOption,
					Options: []onboardingOption{
						{ID: "dark", Title: "Dark"},
						{ID: "light", Title: "Light"},
					},
				}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				applyOnboardingThemeChoice(state, choiceID)
				return nil
			},
		},
		onboardingStepDefinition{
			id: "entry",
			build: func(state *onboardingFlowState) onboardingScreen {
				return onboardingScreen{
					ID:              "entry",
					Kind:            onboardingScreenChoice,
					Title:           "First-time setup",
					Body:            "Do you want to run the first-time setup wizard now or start with defaults?",
					DefaultOptionID: "configure",
					Options:         []onboardingOption{{ID: "configure", Title: "Yes, configure Kent"}, {ID: "defaults", Title: "No, set up defaults for me"}},
				}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				if choiceID == "defaults" {
					state.pendingAction = onboardingPendingActionWriteDefaults
				}
				return nil
			},
		},
		onboardingStepDefinition{
			id: "model",
			build: func(state *onboardingFlowState) onboardingScreen {
				return onboardingScreen{ID: "model", Kind: onboardingScreenInput, Title: "Choose a default model", Helper: "Press Enter to continue.", InputValue: state.settings.Model}
			},
			apply: func(state *onboardingFlowState, value string) error {
				return applyOnboardingModel(state, value)
			},
		},
		onboardingStepDefinition{
			id: "context_window",
			visible: func(state *onboardingFlowState) bool {
				return modelSupportsLargeContextWindow(state, state.settings.Model)
			},
			build: func(state *onboardingFlowState) onboardingScreen {
				modelFact := modelFactFor(state, state.settings.Model)
				body := fmt.Sprintf("%s supports larger context windows. The larger window costs about 50%% more. Quality degrades as the model gets closer to its limit. If automatic compaction is off, Kent can still go above the limit anyway, so the smaller default is recommended.", state.settings.Model)
				return onboardingScreen{ID: "context_window", Kind: onboardingScreenChoice, Title: "Choose a context window", Body: body, DefaultOptionID: "default", Options: []onboardingOption{{ID: "default", Title: fmt.Sprintf("Default window: %s", formatTokenWindow(*modelFact.ContextWindowTokens))}, {ID: "large", Title: fmt.Sprintf("Higher window: %s", formatTokenWindow(modelFact.LargeWindow.Tokens))}}}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				applyContextWindowChoice(state, choiceID)
				return nil
			},
		},
		onboardingStepDefinition{
			id: "thinking",
			visible: func(state *onboardingFlowState) bool {
				return modelSupportsThinking(state, state.settings.Model)
			},
			build: func(state *onboardingFlowState) onboardingScreen {
				levels := modelThinkingLevels(state, state.settings.Model)
				options := []onboardingOption{{ID: "disable", Title: "Disable", Description: thinkingLevelEstimate("disable")}}
				for _, level := range levels {
					options = append(options, onboardingOption{ID: level, Title: titleCaseThinking(level)})
				}
				options = append(options, onboardingOption{ID: "custom", Title: "Enter a custom value"})
				defaultOption := strings.TrimSpace(state.settings.ThinkingLevel)
				if defaultOption == "" {
					defaultOption = "disable"
				}
				if !containsOnboardingOption(options, defaultOption) {
					defaultOption = "custom"
				}
				return onboardingScreen{ID: "thinking", Kind: onboardingScreenChoice, Title: "Choose a thinking level", Body: "Higher thinking levels usually improve results, but they also cost more, use more context, and respond more slowly.", Options: options, DefaultOptionID: defaultOption}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				switch choiceID {
				case "disable":
					state.customThinking = false
					state.settings.ThinkingLevel = ""
				case "custom":
					state.customThinking = true
				default:
					state.customThinking = false
					state.settings.ThinkingLevel = choiceID
				}
				syncReviewerThinkingToPrimary(state)
				return nil
			},
		},
		onboardingStepDefinition{
			id: "thinking_custom",
			visible: func(state *onboardingFlowState) bool {
				return state.customThinking || (strings.TrimSpace(state.settings.ThinkingLevel) != "" && !isKnownThinkingLevel(state.settings.ThinkingLevel))
			},
			build: func(state *onboardingFlowState) onboardingScreen {
				value := state.settings.ThinkingLevel
				if state.customThinking && isKnownThinkingLevel(value) {
					value = ""
				}
				return onboardingScreen{ID: "thinking_custom", Kind: onboardingScreenInput, Title: "Enter a custom thinking level", Helper: "Press Enter to continue.", InputValue: value}
			},
			apply: func(state *onboardingFlowState, value string) error {
				trimmed := strings.TrimSpace(value)
				if trimmed == "" {
					return fmt.Errorf("thinking value must not be empty")
				}
				state.customThinking = true
				state.settings.ThinkingLevel = trimmed
				syncReviewerThinkingToPrimary(state)
				return nil
			},
		},
		onboardingStepDefinition{
			id: "verbosity",
			visible: func(state *onboardingFlowState) bool {
				return modelSupportsVerbosity(state, state.settings.Model)
			},
			build: func(state *onboardingFlowState) onboardingScreen {
				levels := modelVerbosityLevels(state, state.settings.Model)
				options := make([]onboardingOption, 0, len(levels))
				for _, level := range levels {
					options = append(options, onboardingOption{ID: level, Title: titleCaseASCII(level)})
				}
				return onboardingScreen{ID: "verbosity", Kind: onboardingScreenChoice, Title: "Choose a verbosity level", Body: "Choose how verbose the model should be when it responds.", Options: options, DefaultOptionID: string(state.settings.ModelVerbosity)}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				state.settings.ModelVerbosity = config.ModelVerbosity(choiceID)
				return nil
			},
		},
		onboardingStepDefinition{
			id: "ask_question",
			build: func(state *onboardingFlowState) onboardingScreen {
				defaultChoice := "no"
				if state.settings.EnabledTools[toolspec.ToolAskQuestion] {
					defaultChoice = "yes"
				}
				return onboardingScreen{ID: "ask_question", Kind: onboardingScreenChoice, Title: "Allow follow-up questions?", Body: "Allow Kent to ask follow-up questions when it needs clarification.", Options: []onboardingOption{{ID: "yes", Title: "Yes"}, {ID: "no", Title: "No"}}, DefaultOptionID: defaultChoice}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				state.settings.EnabledTools[toolspec.ToolAskQuestion] = choiceID == "yes"
				return nil
			},
		},
		onboardingStepDefinition{
			id: "reviewer",
			build: func(state *onboardingFlowState) onboardingScreen {
				return onboardingScreen{ID: "reviewer", Kind: onboardingScreenChoice, Title: "Enable Supervisor?", Body: "Supervisor reviews the model's output independently, like an always-on code reviewer. It usually improves results, but it costs about 20% more and takes extra time. You can adjust the supervisor model and thinking level later in config.toml.", Options: []onboardingOption{{ID: "all", Title: "Yes, always"}, {ID: "edits", Title: "Yes, after edits"}, {ID: "off", Title: "No"}}, DefaultOptionID: state.settings.Reviewer.Frequency}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				state.settings.Reviewer.Frequency = choiceID
				syncReviewerDefaultsFromPrimary(state)
				return nil
			},
		},
		onboardingStepDefinition{
			id: "reviewer_model",
			visible: func(state *onboardingFlowState) bool {
				return reviewerEnabled(state)
			},
			build: func(state *onboardingFlowState) onboardingScreen {
				reviewerModel := strings.TrimSpace(state.settings.Reviewer.Model)
				if reviewerModel == "" {
					reviewerModel = state.settings.Model
				}
				return onboardingScreen{
					ID:         "reviewer_model",
					Kind:       onboardingScreenInput,
					Title:      "Choose a Supervisor model",
					Body:       "By default, Supervisor uses the same model you chose above. Enter a different model only if you want a separate reviewer pass.",
					Helper:     "Press Enter to continue.",
					InputValue: reviewerModel,
				}
			},
			apply: func(state *onboardingFlowState, value string) error {
				trimmed := strings.TrimSpace(value)
				if trimmed == "" {
					return fmt.Errorf("supervisor model must not be empty")
				}
				state.settings.Reviewer.Model = trimmed
				state.reviewerCustomModel = trimmed != strings.TrimSpace(state.settings.Model)
				if !modelSupportsThinking(state, trimmed) {
					state.reviewerCustomThinking = false
					state.settings.Reviewer.ThinkingLevel = ""
					return nil
				}
				syncReviewerThinkingToPrimary(state)
				return nil
			},
		},
		onboardingStepDefinition{
			id: "reviewer_thinking",
			visible: func(state *onboardingFlowState) bool {
				return reviewerEnabled(state) && modelSupportsThinking(state, state.settings.Reviewer.Model)
			},
			build: func(state *onboardingFlowState) onboardingScreen {
				levels := modelThinkingLevels(state, state.settings.Reviewer.Model)
				options := []onboardingOption{{ID: "disable", Title: "Disable", Description: thinkingLevelEstimate("disable")}}
				for _, level := range levels {
					options = append(options, onboardingOption{ID: level, Title: titleCaseThinking(level)})
				}
				options = append(options, onboardingOption{ID: "custom", Title: "Enter a custom value"})
				defaultOption := strings.TrimSpace(state.settings.Reviewer.ThinkingLevel)
				if defaultOption == "" {
					defaultOption = "disable"
				}
				if !containsOnboardingOption(options, defaultOption) {
					defaultOption = "custom"
				}
				return onboardingScreen{ID: "reviewer_thinking", Kind: onboardingScreenChoice, Title: "Choose a Supervisor thinking level", Body: "By default, Supervisor uses the same thinking level as the main model. Higher thinking levels usually improve results, but they also cost more, use more context, and respond more slowly.", Options: options, DefaultOptionID: defaultOption}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				switch choiceID {
				case "disable":
					state.reviewerThinkingDisabled = true
					state.settings.Reviewer.ThinkingLevel = ""
					state.reviewerCustomThinking = false
					state.reviewerCustomThinkingInput = false
				case "custom":
					state.reviewerThinkingDisabled = false
					state.reviewerCustomThinking = true
					state.reviewerCustomThinkingInput = true
				default:
					state.reviewerThinkingDisabled = false
					state.settings.Reviewer.ThinkingLevel = choiceID
					state.reviewerCustomThinking = choiceID != strings.TrimSpace(state.settings.ThinkingLevel)
					state.reviewerCustomThinkingInput = false
				}
				return nil
			},
		},
		onboardingStepDefinition{
			id: "reviewer_thinking_custom",
			visible: func(state *onboardingFlowState) bool {
				return reviewerEnabled(state) && modelSupportsThinking(state, state.settings.Reviewer.Model) && (state.reviewerCustomThinkingInput || (strings.TrimSpace(state.settings.Reviewer.ThinkingLevel) != "" && !isKnownThinkingLevel(state.settings.Reviewer.ThinkingLevel)))
			},
			build: func(state *onboardingFlowState) onboardingScreen {
				value := state.settings.Reviewer.ThinkingLevel
				if state.reviewerCustomThinkingInput && isKnownThinkingLevel(value) {
					value = ""
				}
				return onboardingScreen{ID: "reviewer_thinking_custom", Kind: onboardingScreenInput, Title: "Enter a custom Supervisor thinking level", Helper: "Press Enter to continue.", InputValue: value}
			},
			apply: func(state *onboardingFlowState, value string) error {
				trimmed := strings.TrimSpace(value)
				if trimmed == "" {
					return fmt.Errorf("supervisor thinking value must not be empty")
				}
				state.reviewerThinkingDisabled = false
				state.settings.Reviewer.ThinkingLevel = trimmed
				state.reviewerCustomThinking = trimmed != strings.TrimSpace(state.settings.ThinkingLevel)
				state.reviewerCustomThinkingInput = !isKnownThinkingLevel(trimmed)
				return nil
			},
		},
		onboardingStepDefinition{
			id: "compaction",
			build: func(state *onboardingFlowState) onboardingScreen {
				options := []onboardingOption{{ID: string(config.CompactionModeLocal), Title: "Local", Description: "Kent's high-quality, slow, costlier, proprietary compaction algorithm."}}
				if state.facts.Providers.CurrentEffective != nil && state.facts.Providers.CurrentEffective.SupportsNativeCompaction {
					options = append(options, onboardingOption{ID: string(config.CompactionModeNative), Title: "Native", Description: "Model provider compacts the context on their own with varying quality."})
				}
				options = append(options, onboardingOption{ID: string(config.CompactionModeNone), Title: "Manual compaction only", Description: "Model requests will fail if threshold is reached."})
				return onboardingScreen{ID: "compaction", Kind: onboardingScreenChoice, Title: "Choose a compaction mode", Body: "Kent can automatically summarize the conversation when the model reaches its context limit. You can always compact manually with /compact.", Options: options, DefaultOptionID: string(state.settings.CompactionMode)}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				state.settings.CompactionMode = config.CompactionMode(choiceID)
				return nil
			},
		},
		onboardingStepDefinition{
			id: "skills_import",
			visible: func(state *onboardingFlowState) bool {
				return state.imports.pending || state.imports.err != nil || (!state.imports.skipSkills && hasImportChoices(state.imports.skillChoices))
			},
			build: func(state *onboardingFlowState) onboardingScreen { return buildSkillImportScreen(state) },
			apply: func(state *onboardingFlowState, choiceID string) error {
				return applyImportChoice(&state.skillImport, choiceID, state.imports.skillChoices)
			},
		},
		onboardingStepDefinition{
			id:      "skills_enabled",
			visible: func(state *onboardingFlowState) bool { return len(skillSelectionCandidates(state)) > 0 },
			build:   func(state *onboardingFlowState) onboardingScreen { return buildSkillSelectionScreen(state) },
			applyMultiSelect: func(state *onboardingFlowState, selection map[string]bool) error {
				state.skillSelection = cloneSelection(selection)
				state.settings.SkillToggles = buildSkillToggles(state, selection)
				return nil
			},
		},
		onboardingStepDefinition{
			id: "review",
			build: func(state *onboardingFlowState) onboardingScreen {
				return onboardingScreen{ID: "review", Kind: onboardingScreenChoice, Title: "Review setup", Body: "Review your first-time setup choices.", Options: []onboardingOption{{ID: "finish", Title: "Finish setup"}, {ID: "restart", Title: "Start over"}}, DefaultOptionID: "finish"}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				switch choiceID {
				case "finish":
					state.pendingAction = onboardingPendingActionWriteCustom
				case "restart":
					state.pendingAction = onboardingPendingActionRestart
				}
				return nil
			},
		},
	}}
}
