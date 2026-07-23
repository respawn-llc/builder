package app

import (
	"context"
	"errors"
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/theme"
	"core/shared/toolspec"
)

func TestNewOnboardingFlowStatePreservesTypedSeedIntent(t *testing.T) {
	cfg := onboardingSeedConfig()
	cfg.Settings.Theme = theme.Auto
	cfg.Settings.Model = "gpt-5.6-sol"
	cfg.Settings.ThinkingLevel = config.DefaultOnboardingSettings().ThinkingLevel
	cfg.Settings.ModelVerbosity = config.ModelVerbosityHigh
	cfg.Settings.ProviderOverride = "openai"
	cfg.Settings.OpenAIBaseURL = "http://127.0.0.1:8080/v1"
	cfg.Settings.Timeouts.ModelRequestSeconds = 123
	cfg.Settings.EnabledTools[toolspec.ToolAskQuestion] = true
	cfg.Settings.EnabledTools[toolspec.ToolEdit] = true
	cfg.Settings.EnabledTools[toolspec.ToolPatch] = false
	cfg.Settings.Reviewer = config.ReviewerSettings{
		Frequency:     "all",
		Model:         cfg.Settings.Model,
		ThinkingLevel: cfg.Settings.ThinkingLevel,
	}
	cfg.Source.Sources["thinking_level"] = "file"
	cfg.Source.Sources["reviewer.model"] = "file"
	cfg.Source.Sources["reviewer.thinking_level"] = "file"

	state, err := newOnboardingFlowState(cfg, testOnboardingCapabilityFacts())
	if err != nil {
		t.Fatalf("construct onboarding state: %v", err)
	}

	if state.selections.theme.kind != onboardingThemeAuto {
		t.Fatalf("theme kind = %q, want auto", state.selections.theme.kind)
	}
	if state.selections.model.kind != onboardingModelKnown || state.selections.model.value != cfg.Settings.Model {
		t.Fatalf("model selection = %+v, want known seed", state.selections.model)
	}
	if state.selections.contextWindow.kind != onboardingContextDefault {
		t.Fatalf("context selection = %+v, want default", state.selections.contextWindow)
	}
	if state.selections.thinking.kind != onboardingThinkingLevel || state.selections.thinking.value != cfg.Settings.ThinkingLevel {
		t.Fatalf("thinking selection = %+v, want explicit same-valued level", state.selections.thinking)
	}
	if state.selections.verbosity.kind != onboardingVerbosityLevel || state.selections.verbosity.value != string(config.ModelVerbosityHigh) {
		t.Fatalf("verbosity selection = %+v, want explicit high", state.selections.verbosity)
	}
	if state.selections.supervisor.frequency != onboardingSupervisorAll {
		t.Fatalf("supervisor frequency = %q, want all", state.selections.supervisor.frequency)
	}
	if state.selections.supervisor.model.kind != onboardingReviewerModelOverridden {
		t.Fatalf("reviewer model = %+v, want explicit same-valued override", state.selections.supervisor.model)
	}
	if state.selections.supervisor.thinking.kind != onboardingReviewerThinkingOverridden {
		t.Fatalf("reviewer thinking = %+v, want explicit same-valued override", state.selections.supervisor.thinking)
	}
	if state.selections.skillImport.Mode != onboardingImportModeNone {
		t.Fatalf("skill import = %+v, want explicit none", state.selections.skillImport)
	}
	if state.selections.pendingPrimaryThinking.kind != onboardingThinkingEditNone ||
		state.selections.pendingReviewerThinking.kind != onboardingThinkingEditNone {
		t.Fatalf("pending thinking edits must start explicit none: %+v", state.selections)
	}
	if state.selections.preserved.providerOverride == nil || *state.selections.preserved.providerOverride != "openai" ||
		state.selections.preserved.openAIBaseURL == nil || *state.selections.preserved.openAIBaseURL != "http://127.0.0.1:8080/v1" ||
		state.selections.preserved.modelTimeoutSeconds == nil || *state.selections.preserved.modelTimeoutSeconds != 123 {
		t.Fatalf("preserved inputs = %+v", state.selections.preserved)
	}
	overrides := onboardingToolOverrides(state.selections.preserved.enabledTools)
	if len(overrides) != 2 {
		t.Fatalf("tool overrides = %+v, want edit/patch deviations", overrides)
	}
}

func TestNewOnboardingFlowStateDistinguishesDefaultAndInheritedSeedIntent(t *testing.T) {
	cfg := onboardingSeedConfig()
	defaultThinking := config.DefaultOnboardingSettings().ThinkingLevel
	cfg.Settings.ThinkingLevel = defaultThinking
	cfg.Settings.Reviewer.Model = cfg.Settings.Model
	cfg.Settings.Reviewer.ThinkingLevel = defaultThinking

	state, err := newOnboardingFlowState(cfg, testOnboardingCapabilityFacts())
	if err != nil {
		t.Fatalf("construct onboarding state: %v", err)
	}
	if state.selections.thinking.kind != onboardingThinkingDefault {
		t.Fatalf("thinking = %+v, want onboarding default", state.selections.thinking)
	}
	if state.selections.supervisor.model.kind != onboardingReviewerModelInherited {
		t.Fatalf("reviewer model = %+v, want inherited", state.selections.supervisor.model)
	}
	if state.selections.supervisor.thinking.kind != onboardingReviewerThinkingInherited {
		t.Fatalf("reviewer thinking = %+v, want inherited", state.selections.supervisor.thinking)
	}
}

func TestNewOnboardingFlowStatePreservesExplicitReviewerThinkingDisable(t *testing.T) {
	cfg := onboardingSeedConfig()
	cfg.Settings.Reviewer.ThinkingLevel = ""
	cfg.Source.Sources["reviewer.thinking_level"] = "env"

	state, err := newOnboardingFlowState(cfg, testOnboardingCapabilityFacts())
	if err != nil {
		t.Fatalf("construct onboarding state: %v", err)
	}
	if state.selections.supervisor.thinking.kind != onboardingReviewerThinkingOverridden ||
		state.selections.supervisor.thinking.override.kind != onboardingThinkingDisabled {
		t.Fatalf("reviewer thinking = %+v, want explicit disabled override", state.selections.supervisor.thinking)
	}
}

func TestNewOnboardingFlowStateRejectsMalformedStructuralInputsInBothModes(t *testing.T) {
	for _, debug := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "debug"}[debug], func(t *testing.T) {
			cfg := onboardingSeedConfig()
			cfg.Settings.Debug = debug
			cfg.Settings.Model = " "
			_, err := newOnboardingFlowState(cfg, testOnboardingCapabilityFacts())
			var conversionErr *onboardingSelectionConversionError
			if !errors.As(err, &conversionErr) {
				t.Fatalf("error = %T %v, want typed conversion error", err, err)
			}
		})
	}
}

func TestNewOnboardingFlowStateRejectsMalformedProvenanceAndCapabilityFacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.App, *serverapi.CapabilityFactsResponse)
	}{
		{
			name: "unknown provenance",
			mutate: func(cfg *config.App, _ *serverapi.CapabilityFactsResponse) {
				cfg.Source.Sources["thinking_level"] = "mystery"
			},
		},
		{
			name: "non-positive model fact",
			mutate: func(_ *config.App, facts *serverapi.CapabilityFactsResponse) {
				facts.Models.KnownModels[0].ContextWindowTokens = ptr(-1)
			},
		},
		{
			name: "malformed import choice",
			mutate: func(_ *config.App, facts *serverapi.CapabilityFactsResponse) {
				facts.Imports.Skills.Choices = []serverapi.ImportChoiceFact{{
					Ref: serverapi.ImportChoiceRef{Mode: string(onboardingImportModeSymlinkSource)},
				}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := onboardingSeedConfig()
			facts := testOnboardingCapabilityFacts()
			tt.mutate(&cfg, &facts)
			_, err := newOnboardingFlowState(cfg, facts)
			var conversionErr *onboardingSelectionConversionError
			if !errors.As(err, &conversionErr) {
				t.Fatalf("error = %T %v, want typed conversion error", err, err)
			}
		})
	}
}

func TestOnboardingSelectionInvariantFailurePanicsWithTypedDiagnosticsInDebug(t *testing.T) {
	state, err := newOnboardingFlowState(onboardingSeedConfig(), testOnboardingCapabilityFacts())
	if err != nil {
		t.Fatalf("construct onboarding state: %v", err)
	}
	state.debug = true
	state.selections.theme.kind = onboardingThemeKind("synthetic-uninitialized")

	defer func() {
		recovered := recover()
		diagnostic, ok := recovered.(onboardingInvariantDiagnostic)
		if !ok {
			t.Fatalf("panic = %T %+v, want onboardingInvariantDiagnostic", recovered, recovered)
		}
		if diagnostic.Operation == "" || diagnostic.StepID == "" ||
			diagnostic.ModelIdentity == "" || diagnostic.VariantType == "" ||
			diagnostic.VariantTag == "" {
			t.Fatalf("diagnostic fields must be inspectable: %+v", diagnostic)
		}
	}()
	_ = state.validateInvariant("screen_projection", "theme")
}

func TestOnboardingSelectionInvariantFailureReturnsTypedErrorInRelease(t *testing.T) {
	state, err := newOnboardingFlowState(onboardingSeedConfig(), testOnboardingCapabilityFacts())
	if err != nil {
		t.Fatalf("construct onboarding state: %v", err)
	}
	state.selections.theme.kind = onboardingThemeKind("synthetic-uninitialized")

	err = state.validateInvariant("finalize_projection", "review")
	var internalErr *onboardingInternalStateError
	if !errors.As(err, &internalErr) {
		t.Fatalf("error = %T %v, want typed internal-state error", err, err)
	}
}

func TestOnboardingSelectionInvariantDiagnosticReportsInvalidImportReferenceValue(t *testing.T) {
	state, err := newOnboardingFlowState(onboardingSeedConfig(), testOnboardingCapabilityFacts())
	if err != nil {
		t.Fatalf("construct onboarding state: %v", err)
	}
	invalidProviderID := " \t"
	state.selections.skillImport = testImportSelection(onboardingImportProviderCodex, "/tmp/skills")
	state.selections.skillImport.ChoiceRef.ImportProviderID = &invalidProviderID

	violation, ok := state.selections.invariantViolation()
	if !ok {
		t.Fatal("invalid import provider reference unexpectedly passed invariants")
	}
	if violation.VariantType != "skill_import.choice_ref.import_provider_id" ||
		violation.VariantTag != invalidProviderID {
		t.Fatalf("import provider diagnostic = %+v, want offending value", violation)
	}
}

func TestOnboardingSelectionInvariantFailureCannotSubmitFinalizationInRelease(t *testing.T) {
	state, err := newOnboardingFlowState(onboardingSeedConfig(), testOnboardingCapabilityFacts())
	if err != nil {
		t.Fatalf("construct onboarding state: %v", err)
	}
	state.selections.theme.kind = onboardingThemeKind("synthetic-uninitialized")
	finalizer := &recordingOnboardingFinalizer{}
	model := newOnboardingModel(newOnboardingFinalization(finalizer, context.Background()), state)

	done := model.finalizeCmd(false)().(onboardingFinalizeDoneMsg)
	var internalErr *onboardingInternalStateError
	if !errors.As(done.err, &internalErr) {
		t.Fatalf("error = %T %v, want typed internal-state error", done.err, done.err)
	}
	next, cmd := model.Update(done)
	if next.(*onboardingModel).terminalErr == nil || cmd == nil {
		t.Fatal("release invariant failure must exit onboarding with a terminal error")
	}
	if len(finalizer.requests) != 0 {
		t.Fatalf("finalizer requests = %d, want zero", len(finalizer.requests))
	}
}

func onboardingSeedConfig() config.App {
	settings := config.DefaultOnboardingSettings()
	settings.Model = "gpt-5.6-sol"
	settings.ModelContextWindow = 272_000
	settings.ContextCompactionThresholdTokens = 272_000 * 95 / 100
	settings.Reviewer.Model = settings.Model
	settings.Reviewer.ThinkingLevel = settings.ThinkingLevel
	return config.App{
		Settings: settings,
		Source: config.SourceReport{Sources: map[string]string{
			"theme":                   "default",
			"model":                   "default",
			"thinking_level":          "default",
			"model_verbosity":         "default",
			"reviewer.frequency":      "default",
			"reviewer.model":          "default",
			"reviewer.thinking_level": "default",
			"compaction_mode":         "default",
		}},
	}
}

func TestNewOnboardingFlowStateDoesNotApplyProviderCompatibilityPolicy(t *testing.T) {
	cfg := onboardingSeedConfig()
	cfg.Settings.ProviderOverride = "anthropic"
	facts := serverapi.CapabilityFactsResponse{
		Models: testOnboardingCapabilityFacts().Models,
		Providers: serverapi.ProviderCapabilityFacts{CurrentEffective: &serverapi.LLMProviderCapabilityFact{
			LLMProviderID: "openai",
		}},
	}
	if _, err := newOnboardingFlowState(cfg, facts); err != nil {
		t.Fatalf("constructor must not reject pre-existing provider/facts drift: %v", err)
	}
}
