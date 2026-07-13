package app

import (
	"errors"
	"reflect"
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
)

func TestOnboardingWorkflowUsesTypedStepIdentityAndOrder(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	steps := newOnboardingWorkflow(state).visibleSteps(state)
	got := make([]onboardingStepID, 0, len(steps))
	for _, step := range steps {
		got = append(got, step.id)
	}
	want := []onboardingStepID{
		onboardingStepTheme,
		onboardingStepEntry,
		onboardingStepModel,
		onboardingStepContextWindow,
		onboardingStepThinking,
		onboardingStepVerbosity,
		onboardingStepAskQuestion,
		onboardingStepReviewer,
		onboardingStepReviewerModel,
		onboardingStepReviewerThinking,
		onboardingStepCompaction,
		onboardingStepReview,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visible step identity/order = %v, want %v", got, want)
	}
}

func TestOnboardingPrimaryThinkingPresetCustomCommitIsAtomic(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	thinking := findWorkflowStep(t, state, onboardingStepThinking)
	if err := thinking.apply(state, "high"); err != nil {
		t.Fatalf("select preset: %v", err)
	}
	if err := thinking.apply(state, "custom"); err != nil {
		t.Fatalf("open custom edit: %v", err)
	}
	if state.selections.thinking.kind != onboardingThinkingLevel || state.selections.thinking.value != "high" {
		t.Fatalf("opening custom edit mutated committed selection: %+v", state.selections.thinking)
	}
	if state.selections.pendingPrimaryThinking.kind != onboardingThinkingEditPending {
		t.Fatalf("pending edit = %+v, want pending", state.selections.pendingPrimaryThinking)
	}
	custom := findWorkflowStep(t, state, onboardingStepThinkingCustom)
	if err := custom.apply(state, "ultra"); err != nil {
		t.Fatalf("commit custom input: %v", err)
	}
	if state.selections.thinking.kind != onboardingThinkingCustom || state.selections.thinking.value != "ultra" {
		t.Fatalf("committed custom selection = %+v", state.selections.thinking)
	}
	if state.selections.pendingPrimaryThinking.kind != onboardingThinkingEditNone {
		t.Fatalf("pending edit survived commit: %+v", state.selections.pendingPrimaryThinking)
	}
}

func TestOnboardingNewPrimaryCustomEditStartsBlankAndCommittedCustomRevisitsValue(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	thinking := findWorkflowStep(t, state, onboardingStepThinking)
	if err := thinking.apply(state, "high"); err != nil {
		t.Fatalf("select preset: %v", err)
	}
	if err := thinking.apply(state, "custom"); err != nil {
		t.Fatalf("open new custom edit: %v", err)
	}
	custom := findWorkflowStep(t, state, onboardingStepThinkingCustom)
	if screen := custom.build(state); screen.InputValue != "" {
		t.Fatalf("new custom edit input = %q, want blank", screen.InputValue)
	}
	if err := custom.apply(state, ""); err == nil {
		t.Fatal("blank custom input unexpectedly committed")
	}
	if state.selections.thinking.kind != onboardingThinkingLevel || state.selections.thinking.value != "high" {
		t.Fatalf("blank custom input changed committed preset: %+v", state.selections.thinking)
	}
	if err := custom.apply(state, "ultra"); err != nil {
		t.Fatalf("commit custom input: %v", err)
	}
	if err := thinking.apply(state, "custom"); err != nil {
		t.Fatalf("revisit custom edit: %v", err)
	}
	if screen := custom.build(state); screen.InputValue != "ultra" {
		t.Fatalf("revisited custom edit input = %q, want committed value", screen.InputValue)
	}
}

func TestOnboardingNewReviewerCustomEditStartsBlankAndCommittedCustomRevisitsValue(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	thinking := findWorkflowStep(t, state, onboardingStepReviewerThinking)
	if err := thinking.apply(state, "high"); err != nil {
		t.Fatalf("select reviewer preset: %v", err)
	}
	if err := thinking.apply(state, "custom"); err != nil {
		t.Fatalf("open new reviewer custom edit: %v", err)
	}
	custom := findWorkflowStep(t, state, onboardingStepReviewerThinkingCustom)
	if screen := custom.build(state); screen.InputValue != "" {
		t.Fatalf("new reviewer custom edit input = %q, want blank", screen.InputValue)
	}
	if err := custom.apply(state, ""); err == nil {
		t.Fatal("blank reviewer custom input unexpectedly committed")
	}
	if state.selections.supervisor.thinking.kind != onboardingReviewerThinkingOverridden ||
		state.selections.supervisor.thinking.override.kind != onboardingThinkingLevel ||
		state.selections.supervisor.thinking.override.value != "high" {
		t.Fatalf("blank reviewer custom input changed committed preset: %+v", state.selections.supervisor.thinking)
	}
	if err := custom.apply(state, "ultra"); err != nil {
		t.Fatalf("commit reviewer custom input: %v", err)
	}
	if err := thinking.apply(state, "custom"); err != nil {
		t.Fatalf("revisit reviewer custom edit: %v", err)
	}
	if screen := custom.build(state); screen.InputValue != "ultra" {
		t.Fatalf("revisited reviewer custom edit input = %q, want committed value", screen.InputValue)
	}
}

func TestOnboardingSupervisorOffClearsHiddenReviewerCustomEdit(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	if err := state.chooseReviewerThinking("custom"); err != nil {
		t.Fatalf("open reviewer custom edit: %v", err)
	}
	if state.selections.pendingReviewerThinking.kind != onboardingThinkingEditPending {
		t.Fatalf("pending reviewer edit = %+v, want pending", state.selections.pendingReviewerThinking)
	}
	if err := state.chooseSupervisorFrequency(string(onboardingSupervisorOff)); err != nil {
		t.Fatalf("disable supervisor: %v", err)
	}
	if state.selections.pendingReviewerThinking.kind != onboardingThinkingEditNone {
		t.Fatalf("hidden reviewer edit survived supervisor disable: %+v", state.selections.pendingReviewerThinking)
	}
	if _, err := onboardingFinalizeRequest(*state, false); err != nil {
		t.Fatalf("finalize with disabled supervisor: %v", err)
	}
}

func TestOnboardingTraversalPreservesSeededSameValuedReviewerOverrides(t *testing.T) {
	state := testOnboardingFlowState(t, func(cfg *config.App) {
		cfg.Settings.Reviewer.Model = cfg.Settings.Model
		cfg.Settings.Reviewer.ThinkingLevel = cfg.Settings.ThinkingLevel
		cfg.Source.Sources["reviewer.model"] = "file"
		cfg.Source.Sources["reviewer.thinking_level"] = "file"
	})
	model := newOnboardingModel(nil, state)
	model.stepIndex = visibleOnboardingStepIndex(t, model, onboardingStepReviewerModel)
	model.syncScreen(true)

	next, _ := model.submitCurrentScreen()
	model = next.(*onboardingModel)
	if model.currentScreen.ID != onboardingStepReviewerThinking {
		t.Fatalf("step after reviewer model = %q, want reviewer thinking", model.currentScreen.ID)
	}
	next, _ = model.submitCurrentScreen()
	model = next.(*onboardingModel)
	if model.terminalErr != nil || model.errorText != "" {
		t.Fatalf("ordinary reviewer traversal failed: terminal=%v validation=%q", model.terminalErr, model.errorText)
	}

	request, err := onboardingFinalizeRequest(model.state, false)
	if err != nil {
		t.Fatalf("project traversed selections: %v", err)
	}
	if request.Supervisor == nil || request.Supervisor.Model == nil || request.Supervisor.Thinking == nil {
		t.Fatalf("ordinary traversal erased explicit same-valued reviewer overrides: %+v", request.Supervisor)
	}
}

func TestOnboardingPrimaryThinkingBackRetainsPendingAndPresetCancelsIt(t *testing.T) {
	state := testOnboardingFlowState(t, nil)
	model := newOnboardingModel(nil, state)
	model.stepIndex = visibleOnboardingStepIndex(t, model, onboardingStepThinking)
	model.syncScreen(true)
	step := model.currentStep()
	if step == nil {
		t.Fatal("thinking step is missing")
	}
	if err := step.apply(&model.state, "custom"); err != nil {
		t.Fatalf("open custom edit: %v", err)
	}
	model.stepIndex++
	model.syncScreen(true)
	if model.currentScreen.ID != onboardingStepThinkingCustom {
		t.Fatalf("current step = %q, want custom thinking", model.currentScreen.ID)
	}
	next, _ := model.goBack()
	model = next.(*onboardingModel)
	if model.currentScreen.ID != onboardingStepThinking || model.currentScreen.DefaultOptionID != "custom" {
		t.Fatalf("back did not retain pending custom selection: %+v", model.currentScreen)
	}
	step = model.currentStep()
	if err := step.apply(&model.state, "low"); err != nil {
		t.Fatalf("select preset after back: %v", err)
	}
	if model.state.selections.pendingPrimaryThinking.kind != onboardingThinkingEditNone {
		t.Fatalf("preset did not cancel pending edit: %+v", model.state.selections.pendingPrimaryThinking)
	}
	if workflowIncludesStep(model.workflow.visibleSteps(&model.state), onboardingStepThinkingCustom) {
		t.Fatal("custom input step remained visible after preset cancellation")
	}
}

func TestOnboardingPendingThinkingBlocksCustomFinalizeButNotDefaults(t *testing.T) {
	state := testOnboardingFlowState(t, nil)
	state.selections.pendingPrimaryThinking = onboardingThinkingEdit{kind: onboardingThinkingEditPending}

	if _, err := onboardingFinalizeRequest(state, false); err == nil {
		t.Fatal("custom finalization accepted an uncommitted thinking edit")
	}
	if _, err := onboardingFinalizeRequest(state, true); err != nil {
		t.Fatalf("theme-only defaults finalization rejected stale custom state: %v", err)
	}
}

func TestOnboardingTypedSelectionTransitions(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	if err := findWorkflowStep(t, state, onboardingStepContextWindow).apply(state, "large"); err != nil {
		t.Fatalf("select large context: %v", err)
	}
	if state.selections.contextWindow.kind != onboardingContextLarge {
		t.Fatalf("context selection = %+v", state.selections.contextWindow)
	}
	if err := findWorkflowStep(t, state, onboardingStepVerbosity).apply(state, "high"); err != nil {
		t.Fatalf("select verbosity: %v", err)
	}
	if state.selections.verbosity.kind != onboardingVerbosityLevel || state.selections.verbosity.value != "high" {
		t.Fatalf("verbosity selection = %+v", state.selections.verbosity)
	}
	if err := findWorkflowStep(t, state, onboardingStepAskQuestion).apply(state, "yes"); err != nil {
		t.Fatalf("enable questions: %v", err)
	}
	if !state.selections.askQuestion {
		t.Fatal("ask-question selection was not enabled")
	}
	if err := findWorkflowStep(t, state, onboardingStepCompaction).apply(state, string(config.CompactionModeNone)); err != nil {
		t.Fatalf("select manual compaction: %v", err)
	}
	if state.selections.compaction != onboardingCompactionManualOnly {
		t.Fatalf("compaction selection = %q", state.selections.compaction)
	}
}

func TestOnboardingImportAndSkillTransitionsRemainComplete(t *testing.T) {
	root := t.TempDir()
	choice := skillSymlinkChoiceFact("codex", root, 1)
	facts := serverapi.ImportCapabilityFacts{
		Skills: serverapi.ImportItemGroupFact{Choices: []serverapi.ImportChoiceFact{choice}},
		SkillEnablement: []serverapi.SkillEnablementProjectionFact{{
			ChoiceRef: choice.Ref,
			Candidates: []serverapi.ImportItemFact{
				skillItemFact("codex", root, root+"/one", "one", "one", nil, true),
			},
		}},
	}
	state := testOnboardingFlowStatePtr(t, nil)
	state.imports = onboardingImportDiscoveryFromFacts(facts)
	importStep := findWorkflowStep(t, state, onboardingStepSkillsImport)
	importOptionID := ""
	for _, candidate := range state.imports.skillChoices {
		if candidate.Mode == onboardingImportModeSymlinkSource {
			importOptionID = candidate.OptionID
			break
		}
	}
	if importOptionID == "" {
		t.Fatal("server-provided symlink import choice is missing")
	}
	if err := importStep.apply(state, importOptionID); err != nil {
		t.Fatalf("select import: %v", err)
	}
	if state.selections.skillImport.Mode != onboardingImportModeSymlinkSource {
		t.Fatalf("import selection = %+v", state.selections.skillImport)
	}
	candidates := skillSelectionCandidates(state)
	if len(candidates) != 1 {
		t.Fatalf("skill candidates = %d, want 1", len(candidates))
	}
	if _, ok := state.selections.skillEnablement[candidates[0].ID]; !ok {
		t.Fatalf("skill enablement omitted candidate %q: %+v", candidates[0].ID, state.selections.skillEnablement)
	}
	if err := findWorkflowStep(t, state, onboardingStepSkillsEnabled).applyMultiSelect(state, map[string]bool{candidates[0].ID: false}); err != nil {
		t.Fatalf("disable imported skill: %v", err)
	}
	if state.selections.skillEnablement[candidates[0].ID] {
		t.Fatal("skill disable selection was not retained")
	}
	if err := importStep.apply(state, importOptionID); err != nil {
		t.Fatalf("reconfirm import: %v", err)
	}
	if state.selections.skillEnablement[candidates[0].ID] {
		t.Fatal("reconfirming the selected import reset the skill disable selection")
	}
}

func TestOnboardingThinkingCapabilityLossDoesNotResurrectLatentValues(t *testing.T) {
	thinkingModel := "thinking-model"
	limitedModel := "limited-model"
	noThinkingModel := "no-thinking-model"
	contextWindow := 100_000
	facts := serverapi.CapabilityFactsResponse{Models: serverapi.ModelCapabilityFacts{
		KnownModels: []serverapi.ModelCapabilityFact{
			{ModelID: &thinkingModel, Known: true, ContextWindowTokens: &contextWindow, SupportsThinking: true, SupportedThinkingLevels: []string{"low", "high"}},
			{ModelID: &limitedModel, Known: true, ContextWindowTokens: &contextWindow, SupportsThinking: true, SupportedThinkingLevels: []string{"low"}},
			{ModelID: &noThinkingModel, Known: true, ContextWindowTokens: &contextWindow},
		},
	}}
	state := testOnboardingFlowStatePtr(t, func(cfg *config.App) {
		cfg.Settings.Model = thinkingModel
		cfg.Settings.ModelContextWindow = contextWindow
		cfg.Settings.ThinkingLevel = "high"
		cfg.Settings.Reviewer.Model = thinkingModel
		cfg.Settings.Reviewer.ThinkingLevel = "high"
		cfg.Source.Sources["thinking_level"] = "file"
	}, facts)

	if err := state.submitPrimaryModel(limitedModel); err != nil {
		t.Fatalf("select limited thinking model: %v", err)
	}
	if state.selections.thinking.kind != onboardingThinkingCustom || state.selections.thinking.value != "high" {
		t.Fatalf("unsupported preset was not retained as custom: %+v", state.selections.thinking)
	}
	if err := state.submitPrimaryModel(noThinkingModel); err != nil {
		t.Fatalf("select no-thinking model: %v", err)
	}
	if state.selections.thinking.kind != onboardingThinkingDisabled {
		t.Fatalf("thinking capability loss did not disable selection: %+v", state.selections.thinking)
	}
	if err := state.submitPrimaryModel(thinkingModel); err != nil {
		t.Fatalf("restore thinking-capable model: %v", err)
	}
	if state.selections.thinking.kind != onboardingThinkingDisabled {
		t.Fatalf("thinking selection resurrected after capability regain: %+v", state.selections.thinking)
	}
}

func TestOnboardingSupervisorThinkingCapabilityLossDoesNotResurrectLatentValues(t *testing.T) {
	mainModel := "main-model"
	reviewerModel := "reviewer-model"
	noThinkingReviewer := "no-thinking-reviewer"
	contextWindow := 100_000
	facts := serverapi.CapabilityFactsResponse{Models: serverapi.ModelCapabilityFacts{
		KnownModels: []serverapi.ModelCapabilityFact{
			{ModelID: &mainModel, Known: true, ContextWindowTokens: &contextWindow, SupportsThinking: true, SupportedThinkingLevels: []string{"low", "high"}},
			{ModelID: &reviewerModel, Known: true, ContextWindowTokens: &contextWindow, SupportsThinking: true, SupportedThinkingLevels: []string{"low"}},
			{ModelID: &noThinkingReviewer, Known: true, ContextWindowTokens: &contextWindow},
		},
	}}
	state := testOnboardingFlowStatePtr(t, func(cfg *config.App) {
		cfg.Settings.Model = mainModel
		cfg.Settings.ModelContextWindow = contextWindow
		cfg.Settings.ThinkingLevel = "high"
		cfg.Settings.Reviewer.Model = reviewerModel
		cfg.Settings.Reviewer.ThinkingLevel = "low"
		cfg.Source.Sources["thinking_level"] = "file"
		cfg.Source.Sources["reviewer.model"] = "file"
		cfg.Source.Sources["reviewer.thinking_level"] = "file"
	}, facts)

	reviewerStep := findWorkflowStep(t, state, onboardingStepReviewerModel)
	if err := reviewerStep.apply(state, noThinkingReviewer); err != nil {
		t.Fatalf("select no-thinking reviewer: %v", err)
	}
	if state.selections.supervisor.thinking.kind != onboardingReviewerThinkingCapabilityDisabled {
		t.Fatalf("reviewer thinking capability loss did not disable selection: %+v", state.selections.supervisor.thinking)
	}
	request, err := onboardingFinalizeRequest(*state, false)
	if err != nil {
		t.Fatalf("project capability-disabled reviewer: %v", err)
	}
	if request.Supervisor == nil || request.Supervisor.Thinking == nil ||
		request.Supervisor.Thinking.Kind != serverapi.OnboardingThinkingDisabled {
		t.Fatalf("capability-disabled reviewer projection = %+v", request.Supervisor)
	}
	if err := reviewerStep.apply(state, reviewerModel); err != nil {
		t.Fatalf("restore thinking-capable reviewer: %v", err)
	}
	if state.selections.supervisor.thinking.kind != onboardingReviewerThinkingInherited ||
		state.selections.reviewerThinkingValue() != "high" {
		t.Fatalf("reviewer thinking did not restore inheritance after capability regain: %+v", state.selections.supervisor.thinking)
	}
	request, err = onboardingFinalizeRequest(*state, false)
	if err != nil {
		t.Fatalf("project restored reviewer inheritance: %v", err)
	}
	if request.Supervisor == nil || request.Supervisor.Thinking != nil {
		t.Fatalf("restored reviewer inheritance projected an override: %+v", request.Supervisor)
	}

	explicitDisabled := testOnboardingFlowStatePtr(t, func(cfg *config.App) {
		cfg.Settings.Model = mainModel
		cfg.Settings.ModelContextWindow = contextWindow
		cfg.Settings.Reviewer.Model = reviewerModel
		cfg.Source.Sources["reviewer.model"] = "file"
	}, facts)
	if err := explicitDisabled.chooseReviewerThinking("disable"); err != nil {
		t.Fatalf("explicitly disable reviewer thinking: %v", err)
	}
	if err := explicitDisabled.submitReviewerModel(noThinkingReviewer); err != nil {
		t.Fatalf("select no-thinking reviewer after explicit disable: %v", err)
	}
	if err := explicitDisabled.submitReviewerModel(reviewerModel); err != nil {
		t.Fatalf("restore thinking-capable reviewer after explicit disable: %v", err)
	}
	if explicitDisabled.selections.supervisor.thinking.kind != onboardingReviewerThinkingOverridden ||
		explicitDisabled.selections.supervisor.thinking.override.kind != onboardingThinkingDisabled {
		t.Fatalf("explicit reviewer disable lost provenance across capability change: %+v", explicitDisabled.selections.supervisor.thinking)
	}
}

func TestOnboardingMalformedUserInputsReturnTypedErrorsInBothModes(t *testing.T) {
	for _, debugMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "debug"}[debugMode], func(t *testing.T) {
			tests := []struct {
				name  string
				apply func(*onboardingFlowState) error
			}{
				{name: "model", apply: func(state *onboardingFlowState) error {
					return state.submitPrimaryModel(" ")
				}},
				{name: "primary custom thinking", apply: func(state *onboardingFlowState) error {
					if err := findWorkflowStep(t, state, onboardingStepThinking).apply(state, "custom"); err != nil {
						return err
					}
					return findWorkflowStep(t, state, onboardingStepThinkingCustom).apply(state, " ")
				}},
				{name: "reviewer model", apply: func(state *onboardingFlowState) error {
					return findWorkflowStep(t, state, onboardingStepReviewerModel).apply(state, " ")
				}},
				{name: "reviewer custom thinking", apply: func(state *onboardingFlowState) error {
					if err := findWorkflowStep(t, state, onboardingStepReviewerThinking).apply(state, "custom"); err != nil {
						return err
					}
					return findWorkflowStep(t, state, onboardingStepReviewerThinkingCustom).apply(state, " ")
				}},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					state := testOnboardingFlowStatePtr(t, nil)
					state.debug = debugMode
					err := tt.apply(state)
					var conversionErr *onboardingSelectionConversionError
					if !errors.As(err, &conversionErr) {
						t.Fatalf("error = %T %v, want typed conversion error", err, err)
					}
				})
			}
		})
	}
}

func TestOnboardingSelectionOperationsAreAtomicOnFailure(t *testing.T) {
	state := testOnboardingFlowStatePtr(t, nil)
	before := state.selections.clone()
	if err := state.submitPrimaryModel(" "); err == nil {
		t.Fatal("blank model submission unexpectedly succeeded")
	}
	if !reflect.DeepEqual(state.selections, before) {
		t.Fatalf("failed selection operation mutated state:\n got: %+v\nwant: %+v", state.selections, before)
	}
}

func visibleOnboardingStepIndex(t *testing.T, model *onboardingModel, target onboardingStepID) int {
	t.Helper()
	for index, step := range model.workflow.visibleSteps(&model.state) {
		if step.id == target {
			return index
		}
	}
	t.Fatalf("visible onboarding step %q is missing", target)
	return -1
}
