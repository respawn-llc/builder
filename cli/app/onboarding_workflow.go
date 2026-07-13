package app

import (
	"fmt"

	"core/shared/config"
	"core/shared/theme"
)

type onboardingStepID string

const (
	onboardingStepTheme                  onboardingStepID = "theme"
	onboardingStepEntry                  onboardingStepID = "entry"
	onboardingStepModel                  onboardingStepID = "model"
	onboardingStepContextWindow          onboardingStepID = "context_window"
	onboardingStepThinking               onboardingStepID = "thinking"
	onboardingStepThinkingCustom         onboardingStepID = "thinking_custom"
	onboardingStepVerbosity              onboardingStepID = "verbosity"
	onboardingStepAskQuestion            onboardingStepID = "ask_question"
	onboardingStepReviewer               onboardingStepID = "reviewer"
	onboardingStepReviewerModel          onboardingStepID = "reviewer_model"
	onboardingStepReviewerThinking       onboardingStepID = "reviewer_thinking"
	onboardingStepReviewerThinkingCustom onboardingStepID = "reviewer_thinking_custom"
	onboardingStepCompaction             onboardingStepID = "compaction"
	onboardingStepSkillsImport           onboardingStepID = "skills_import"
	onboardingStepSkillsEnabled          onboardingStepID = "skills_enabled"
	onboardingStepReview                 onboardingStepID = "review"
)

type onboardingStepDefinition struct {
	id               onboardingStepID
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
			id: onboardingStepTheme,
			build: func(state *onboardingFlowState) onboardingScreen {
				defaultOption := theme.Resolve(state.selections.themeValue())
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
				return state.chooseTheme(choiceID)
			},
		},
		onboardingStepDefinition{
			id: onboardingStepEntry,
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
			id: onboardingStepModel,
			build: func(state *onboardingFlowState) onboardingScreen {
				return onboardingScreen{ID: "model", Kind: onboardingScreenInput, Title: "Choose a default model", Helper: "Press Enter to continue.", InputValue: state.selections.model.value}
			},
			apply: func(state *onboardingFlowState, value string) error {
				return state.submitPrimaryModel(value)
			},
		},
		onboardingStepDefinition{
			id: onboardingStepContextWindow,
			visible: func(state *onboardingFlowState) bool {
				return modelSupportsLargeContextWindow(state, state.selections.model.value)
			},
			build: func(state *onboardingFlowState) onboardingScreen {
				modelFact := modelFactFor(state, state.selections.model.value)
				body := fmt.Sprintf("%s supports larger context windows. The larger window costs about 50%% more. Quality degrades as the model gets closer to its limit. If automatic compaction is off, Kent can still go above the limit anyway, so the smaller default is recommended.", state.selections.model.value)
				return onboardingScreen{ID: "context_window", Kind: onboardingScreenChoice, Title: "Choose a context window", Body: body, DefaultOptionID: string(state.selections.contextWindow.kind), Options: []onboardingOption{{ID: "default", Title: fmt.Sprintf("Default window: %s", formatTokenWindow(*modelFact.ContextWindowTokens))}, {ID: "large", Title: fmt.Sprintf("Higher window: %s", formatTokenWindow(modelFact.LargeWindow.Tokens))}}}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				return state.chooseContextWindow(choiceID)
			},
		},
		onboardingStepDefinition{
			id: onboardingStepThinking,
			visible: func(state *onboardingFlowState) bool {
				return modelSupportsThinking(state, state.selections.model.value)
			},
			build: func(state *onboardingFlowState) onboardingScreen {
				levels := modelThinkingLevels(state, state.selections.model.value)
				options := []onboardingOption{{ID: "disable", Title: "Disable", Description: thinkingLevelEstimate("disable")}}
				for _, level := range levels {
					options = append(options, onboardingOption{ID: level, Title: titleCaseThinking(level)})
				}
				options = append(options, onboardingOption{ID: "custom", Title: "Enter a custom value"})
				defaultOption := state.selections.thinkingValue()
				if state.selections.pendingPrimaryThinking.pending() || state.selections.thinking.kind == onboardingThinkingCustom {
					defaultOption = "custom"
				} else if state.selections.thinking.kind == onboardingThinkingDisabled {
					defaultOption = "disable"
				}
				if !containsOnboardingOption(options, defaultOption) {
					defaultOption = "custom"
				}
				return onboardingScreen{ID: "thinking", Kind: onboardingScreenChoice, Title: "Choose a thinking level", Body: "Higher thinking levels usually improve results, but they also cost more, use more context, and respond more slowly.", Options: options, DefaultOptionID: defaultOption}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				return state.choosePrimaryThinking(choiceID)
			},
		},
		onboardingStepDefinition{
			id: onboardingStepThinkingCustom,
			visible: func(state *onboardingFlowState) bool {
				return state.selections.pendingPrimaryThinking.pending() || state.selections.thinking.kind == onboardingThinkingCustom
			},
			build: func(state *onboardingFlowState) onboardingScreen {
				return onboardingScreen{ID: "thinking_custom", Kind: onboardingScreenInput, Title: "Enter a custom thinking level", Helper: "Press Enter to continue.", InputValue: state.selections.primaryCustomInputValue()}
			},
			apply: func(state *onboardingFlowState, value string) error {
				return state.commitPrimaryCustomThinking(value)
			},
		},
		onboardingStepDefinition{
			id: onboardingStepVerbosity,
			visible: func(state *onboardingFlowState) bool {
				return modelSupportsVerbosity(state, state.selections.model.value)
			},
			build: func(state *onboardingFlowState) onboardingScreen {
				levels := modelVerbosityLevels(state, state.selections.model.value)
				options := make([]onboardingOption, 0, len(levels))
				for _, level := range levels {
					options = append(options, onboardingOption{ID: level, Title: titleCaseASCII(level)})
				}
				return onboardingScreen{ID: "verbosity", Kind: onboardingScreenChoice, Title: "Choose a verbosity level", Body: "Choose how verbose the model should be when it responds.", Options: options, DefaultOptionID: state.selections.verbosity.value}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				return state.chooseVerbosity(choiceID)
			},
		},
		onboardingStepDefinition{
			id: onboardingStepAskQuestion,
			build: func(state *onboardingFlowState) onboardingScreen {
				defaultChoice := "no"
				if state.selections.askQuestion {
					defaultChoice = "yes"
				}
				return onboardingScreen{ID: "ask_question", Kind: onboardingScreenChoice, Title: "Allow follow-up questions?", Body: "Allow Kent to ask follow-up questions when it needs clarification.", Options: []onboardingOption{{ID: "yes", Title: "Yes"}, {ID: "no", Title: "No"}}, DefaultOptionID: defaultChoice}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				return state.chooseAskQuestion(choiceID)
			},
		},
		onboardingStepDefinition{
			id: onboardingStepReviewer,
			build: func(state *onboardingFlowState) onboardingScreen {
				return onboardingScreen{ID: "reviewer", Kind: onboardingScreenChoice, Title: "Enable Supervisor?", Body: "Supervisor reviews the model's output independently, like an always-on code reviewer. It usually improves results, but it costs about 20% more and takes extra time. You can adjust the supervisor model and thinking level later in config.toml.", Options: []onboardingOption{{ID: "all", Title: "Yes, always"}, {ID: "edits", Title: "Yes, after edits"}, {ID: "off", Title: "No"}}, DefaultOptionID: string(state.selections.supervisor.frequency)}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				return state.chooseSupervisorFrequency(choiceID)
			},
		},
		onboardingStepDefinition{
			id: onboardingStepReviewerModel,
			visible: func(state *onboardingFlowState) bool {
				return reviewerEnabled(state)
			},
			build: func(state *onboardingFlowState) onboardingScreen {
				reviewerModel := state.selections.reviewerModelValue()
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
				return state.submitReviewerModel(value)
			},
		},
		onboardingStepDefinition{
			id: onboardingStepReviewerThinking,
			visible: func(state *onboardingFlowState) bool {
				return reviewerEnabled(state) && modelSupportsThinking(state, state.selections.reviewerModelValue())
			},
			build: func(state *onboardingFlowState) onboardingScreen {
				levels := modelThinkingLevels(state, state.selections.reviewerModelValue())
				options := []onboardingOption{{ID: "disable", Title: "Disable", Description: thinkingLevelEstimate("disable")}}
				for _, level := range levels {
					options = append(options, onboardingOption{ID: level, Title: titleCaseThinking(level)})
				}
				options = append(options, onboardingOption{ID: "custom", Title: "Enter a custom value"})
				defaultOption := state.selections.reviewerThinkingValue()
				if state.selections.pendingReviewerThinking.pending() ||
					(state.selections.supervisor.thinking.kind == onboardingReviewerThinkingOverridden &&
						state.selections.supervisor.thinking.override.kind == onboardingThinkingCustom) {
					defaultOption = "custom"
				} else if state.selections.supervisor.thinking.kind == onboardingReviewerThinkingOverridden &&
					state.selections.supervisor.thinking.override.kind == onboardingThinkingDisabled {
					defaultOption = "disable"
				}
				if !containsOnboardingOption(options, defaultOption) {
					defaultOption = "custom"
				}
				return onboardingScreen{ID: "reviewer_thinking", Kind: onboardingScreenChoice, Title: "Choose a Supervisor thinking level", Body: "By default, Supervisor uses the same thinking level as the main model. Higher thinking levels usually improve results, but they also cost more, use more context, and respond more slowly.", Options: options, DefaultOptionID: defaultOption}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				return state.chooseReviewerThinking(choiceID)
			},
		},
		onboardingStepDefinition{
			id: onboardingStepReviewerThinkingCustom,
			visible: func(state *onboardingFlowState) bool {
				return reviewerEnabled(state) &&
					modelSupportsThinking(state, state.selections.reviewerModelValue()) &&
					(state.selections.pendingReviewerThinking.pending() ||
						(state.selections.supervisor.thinking.kind == onboardingReviewerThinkingOverridden &&
							state.selections.supervisor.thinking.override.kind == onboardingThinkingCustom))
			},
			build: func(state *onboardingFlowState) onboardingScreen {
				return onboardingScreen{ID: "reviewer_thinking_custom", Kind: onboardingScreenInput, Title: "Enter a custom Supervisor thinking level", Helper: "Press Enter to continue.", InputValue: state.selections.reviewerCustomInputValue()}
			},
			apply: func(state *onboardingFlowState, value string) error {
				return state.commitReviewerCustomThinking(value)
			},
		},
		onboardingStepDefinition{
			id: onboardingStepCompaction,
			build: func(state *onboardingFlowState) onboardingScreen {
				options := []onboardingOption{{ID: string(config.CompactionModeLocal), Title: "Local", Description: "Kent's high-quality, slow, costlier, proprietary compaction algorithm."}}
				if state.facts.Providers.CurrentEffective != nil && state.facts.Providers.CurrentEffective.SupportsNativeCompaction {
					options = append(options, onboardingOption{ID: string(config.CompactionModeNative), Title: "Native", Description: "Model provider compacts the context on their own with varying quality."})
				}
				options = append(options, onboardingOption{ID: string(config.CompactionModeNone), Title: "Manual compaction only", Description: "Model requests will fail if threshold is reached."})
				return onboardingScreen{ID: "compaction", Kind: onboardingScreenChoice, Title: "Choose a compaction mode", Body: "Kent can automatically summarize the conversation when the model reaches its context limit. You can always compact manually with /compact.", Options: options, DefaultOptionID: string(state.selections.compactionValue())}
			},
			apply: func(state *onboardingFlowState, choiceID string) error {
				return state.chooseCompaction(choiceID)
			},
		},
		onboardingStepDefinition{
			id: onboardingStepSkillsImport,
			visible: func(state *onboardingFlowState) bool {
				return state.imports.pending || state.imports.err != nil || (!state.imports.skipSkills && hasImportChoices(state.imports.skillChoices))
			},
			build: func(state *onboardingFlowState) onboardingScreen { return buildSkillImportScreen(state) },
			apply: func(state *onboardingFlowState, choiceID string) error {
				return state.chooseSkillImport(choiceID)
			},
		},
		onboardingStepDefinition{
			id:      onboardingStepSkillsEnabled,
			visible: func(state *onboardingFlowState) bool { return len(skillSelectionCandidates(state)) > 0 },
			build:   func(state *onboardingFlowState) onboardingScreen { return buildSkillSelectionScreen(state) },
			applyMultiSelect: func(state *onboardingFlowState, selection map[string]bool) error {
				return state.chooseSkillEnablement(selection)
			},
		},
		onboardingStepDefinition{
			id: onboardingStepReview,
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
