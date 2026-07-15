package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/theme"
	"core/shared/toolspec"

	tea "github.com/charmbracelet/bubbletea"
)

type recordingOnboardingFinalizer struct {
	requests []serverapi.OnboardingFinalizeRequest
	response serverapi.OnboardingFinalizeResponse
	err      error
}

func (f *recordingOnboardingFinalizer) FinalizeOnboarding(_ context.Context, req serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
	f.requests = append(f.requests, req)
	return f.response, f.err
}

var _ apicontract.OnboardingFinalizeService = (*recordingOnboardingFinalizer)(nil)

type onboardingCapabilityFactsClientFunc func(context.Context, serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error)

func (fn onboardingCapabilityFactsClientFunc) GetCapabilityFacts(ctx context.Context, req serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error) {
	return fn(ctx, req)
}

var _ apicontract.CapabilityFactsService = onboardingCapabilityFactsClientFunc(nil)

func TestOnboardingDefaultsFinalizeThroughServerAPI(t *testing.T) {
	finalizer := &recordingOnboardingFinalizer{response: serverapi.OnboardingFinalizeResponse{
		Completed:    true,
		SettingsPath: "/server/.kent/config.toml",
	}}
	state := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
		cfg.Settings.Theme = theme.Dark
	}, serverapi.CapabilityFactsResponse{})
	model := newOnboardingModel(newOnboardingFinalization(finalizer, context.Background()), state)

	msg := model.finalizeCmd(true)()
	done, ok := msg.(onboardingFinalizeDoneMsg)
	if !ok {
		t.Fatalf("message = %T, want onboardingFinalizeDoneMsg", msg)
	}
	if done.err != nil {
		t.Fatalf("finalize defaults: %v", done.err)
	}
	if len(finalizer.requests) != 1 {
		t.Fatalf("finalize requests = %d, want 1", len(finalizer.requests))
	}
	request := finalizer.requests[0]
	if request.Theme == nil || *request.Theme != serverapi.OnboardingThemeDark {
		t.Fatalf("theme = %+v, want dark", request.Theme)
	}
	if request.Model != nil || request.CommandsImport == nil || request.CommandsImport.Mode != serverapi.OnboardingImportModeNone {
		t.Fatalf("unexpected defaults request: %+v", request)
	}
	if !done.result.Completed || !done.result.CreatedDefaultConfig || done.result.SettingsPath != "/server/.kent/config.toml" {
		t.Fatalf("unexpected result: %+v", done.result)
	}
}

func TestOnboardingFinalizeClientErrorReachesDoneMessage(t *testing.T) {
	expected := errors.New("server unavailable")
	state := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
		cfg.Settings.Theme = theme.Light
	}, serverapi.CapabilityFactsResponse{})
	model := newOnboardingModel(newOnboardingFinalization(&recordingOnboardingFinalizer{err: expected}, context.Background()), state)

	msg := model.finalizeCmd(true)()
	done, ok := msg.(onboardingFinalizeDoneMsg)
	if !ok {
		t.Fatalf("message = %T, want onboardingFinalizeDoneMsg", msg)
	}
	if !errors.Is(done.err, expected) {
		t.Fatalf("error = %v, want %v", done.err, expected)
	}
}

func TestOnboardingFinalizationRejectsPreSubmissionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	finalizer := &recordingOnboardingFinalizer{}
	state := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
		cfg.Settings.Theme = theme.Light
	}, serverapi.CapabilityFactsResponse{})
	model := newOnboardingModel(newOnboardingFinalization(finalizer, ctx), state)

	msg := model.finalizeCmd(true)()
	done := msg.(onboardingFinalizeDoneMsg)
	if !errors.Is(done.err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", done.err)
	}
	if len(finalizer.requests) != 0 {
		t.Fatalf("finalize requests = %d, want 0", len(finalizer.requests))
	}
}

func TestRunOnboardingFlowHonorsPreSubmissionParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	finalizer := &recordingOnboardingFinalizer{}
	_, err := runOnboardingFlow(
		ctx,
		config.App{},
		onboardingCapabilityFactsClientFunc(func(context.Context, serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error) {
			return serverapi.CapabilityFactsResponse{}, nil
		}),
		finalizer,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run onboarding error = %v, want context canceled", err)
	}
	if len(finalizer.requests) != 0 {
		t.Fatalf("finalize requests = %d, want no pre-submission write", len(finalizer.requests))
	}
}

func TestOnboardingFinalizationIgnoresEscapeAfterSubmission(t *testing.T) {
	state := newOnboardingFinalizeProjectionState(t, nil, serverapi.CapabilityFactsResponse{})
	model := newOnboardingModel(nil, state)
	model.finalizing = true

	next, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := next.(*onboardingModel)
	if updated.canceled {
		t.Fatal("escape must not cancel a submitted finalization")
	}
	if cmd != nil {
		t.Fatal("submitted finalization must not emit a quit command")
	}
}

type blockingOnboardingFinalizer struct{}

func (blockingOnboardingFinalizer) FinalizeOnboarding(ctx context.Context, _ serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
	<-ctx.Done()
	return serverapi.OnboardingFinalizeResponse{}, ctx.Err()
}

func TestOnboardingFinalizationTimeoutIsTerminalAndIndeterminate(t *testing.T) {
	finalization := newOnboardingFinalization(blockingOnboardingFinalizer{}, context.Background())
	finalization.timeout = time.Millisecond
	state := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
		cfg.Settings.Theme = theme.Light
	}, serverapi.CapabilityFactsResponse{})
	model := newOnboardingModel(finalization, state)

	msg := model.finalizeCmd(true)()
	done := msg.(onboardingFinalizeDoneMsg)
	if done.err == nil {
		t.Fatal("expected indeterminate timeout error")
	}
	next, cmd := model.Update(done)
	updated := next.(*onboardingModel)
	if updated.terminalErr == nil {
		t.Fatal("expected terminal indeterminate error")
	}
	if cmd == nil {
		t.Fatal("expected terminal quit command")
	}
}

func TestOnboardingFinalizationTypedFailureCanBeRetried(t *testing.T) {
	typedFailure := serverapi.NewOnboardingFinalizeError(
		serverapi.OnboardingFinalizeConfigWriteFailed,
		serverapi.OnboardingConfigWriteFailedDetails{SettingsPath: "/server/config.toml", Operation: "write"},
		errors.New("write failed"),
	)
	finalizer := &recordingOnboardingFinalizer{err: typedFailure}
	finalization := newOnboardingFinalization(finalizer, context.Background())
	state := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
		cfg.Settings.Theme = theme.Light
	}, serverapi.CapabilityFactsResponse{})
	model := newOnboardingModel(finalization, state)

	first := model.finalizeCmd(true)().(onboardingFinalizeDoneMsg)
	next, _ := model.Update(first)
	model = next.(*onboardingModel)
	if model.errorText == "" {
		t.Fatal("typed finalization failure must be rendered in the wizard")
	}
	if _, submitted := finalization.waitIfSubmitted(); submitted {
		t.Fatal("typed finalization failure must not retain a submitted lifecycle")
	}

	finalizer.err = nil
	finalizer.response = serverapi.OnboardingFinalizeResponse{Completed: true, SettingsPath: "/server/config.toml"}
	second := model.finalizeCmd(true)().(onboardingFinalizeDoneMsg)
	if second.err != nil {
		t.Fatalf("retry finalization: %v", second.err)
	}
	if len(finalizer.requests) != 2 {
		t.Fatalf("finalize requests = %d, want retry to invoke server twice", len(finalizer.requests))
	}
}

func TestOnboardingTypedFailureReturnsToCancelableWizard(t *testing.T) {
	typedFailure := serverapi.NewOnboardingFinalizeError(
		serverapi.OnboardingFinalizeConfigWriteFailed,
		serverapi.OnboardingConfigWriteFailedDetails{SettingsPath: "/server/config.toml", Operation: "write"},
		errors.New("write failed"),
	)
	finalization := newOnboardingFinalization(
		&recordingOnboardingFinalizer{err: typedFailure},
		context.Background(),
	)
	state := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
		cfg.Settings.Theme = theme.Light
	}, serverapi.CapabilityFactsResponse{})
	model := newOnboardingModel(finalization, state)

	done := model.finalizeCmd(true)().(onboardingFinalizeDoneMsg)
	next, _ := model.Update(done)
	model = next.(*onboardingModel)
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !next.(*onboardingModel).canceled {
		t.Fatal("escape after a recoverable finalization failure must cancel setup")
	}
	if _, submitted := finalization.waitIfSubmitted(); submitted {
		t.Fatal("canceled retry-ready wizard must not retain a submitted finalization")
	}
}

func TestOnboardingFinalizationJoinsSubmittedResultAfterParentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	finalizer := onboardingFinalizeClientFunc(func(context.Context, serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
		close(started)
		<-release
		return serverapi.OnboardingFinalizeResponse{Completed: true, SettingsPath: "/server/config.toml"}, nil
	})
	finalization := newOnboardingFinalization(finalizer, parent)
	request := serverapi.OnboardingFinalizeRequest{}
	if err := finalization.start(request, true, theme.Dark); err != nil {
		t.Fatalf("start finalization: %v", err)
	}
	<-started
	cancelParent()

	type joinedFinalization struct {
		outcome   onboardingFinalizeDoneMsg
		submitted bool
	}
	joined := make(chan joinedFinalization, 1)
	go func() {
		outcome, submitted := finalization.waitIfSubmitted()
		joined <- joinedFinalization{outcome: outcome, submitted: submitted}
	}()
	select {
	case <-joined:
		t.Fatal("submitted finalization returned before the server result")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	completed := <-joined
	if !completed.submitted {
		t.Fatal("expected submitted finalization")
	}
	outcome := completed.outcome
	if outcome.err != nil || !outcome.result.Completed {
		t.Fatalf("joined outcome = %+v", outcome)
	}
}

func TestOnboardingTerminalActivationFailureKeepsCommittedOutcomeContext(t *testing.T) {
	activationFailure := serverapi.NewServerNotReadyError(
		serverapi.ServerNotReadyActivationFailed,
		serverapi.ServerNotReadyDetails{OnboardingCompleted: true},
		errors.New("activation failed"),
	)
	finalization := newOnboardingFinalization(
		&recordingOnboardingFinalizer{err: activationFailure},
		context.Background(),
	)
	state := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
		cfg.Settings.Theme = theme.Light
	}, serverapi.CapabilityFactsResponse{})
	model := newOnboardingModel(finalization, state)

	done := model.finalizeCmd(true)().(onboardingFinalizeDoneMsg)
	next, _ := model.Update(done)
	finalized := next.(*onboardingModel)
	if finalized.terminalErr == nil {
		t.Fatal("expected a terminal activation failure")
	}
	outcome, submitted := finalization.waitIfSubmitted()
	if !submitted {
		t.Fatal("expected submitted finalization")
	}
	if !errors.Is(outcome.err, activationFailure) {
		t.Fatalf("finalization outcome = %v, want activation failure", outcome.err)
	}
	if !errors.Is(finalized.terminalErr, activationFailure) {
		t.Fatalf("terminal error = %v, want activation failure", finalized.terminalErr)
	}
	if got := finalized.terminalErr.Error(); got == activationFailure.Error() {
		t.Fatalf("terminal error = %q, want committed-onboarding context", got)
	}
}

func TestOnboardingCustomProjectionPreservesTypedChoices(t *testing.T) {
	modelID := "gpt-5"
	state := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
		cfg.Settings.Theme = theme.Light
		cfg.Settings.Model = modelID
		cfg.Settings.ProviderOverride = "openai"
		cfg.Settings.OpenAIBaseURL = "http://127.0.0.1:8080/v1"
		cfg.Settings.ModelContextWindow = 1_000_000
		cfg.Settings.ThinkingLevel = "high"
		cfg.Settings.ModelVerbosity = config.ModelVerbosityHigh
		cfg.Settings.Timeouts = config.Timeouts{ModelRequestSeconds: 123}
		cfg.Settings.CompactionMode = config.CompactionModeNative
		cfg.Settings.EnabledTools = map[toolspec.ID]bool{
			toolspec.ToolAskQuestion: true,
			toolspec.ToolEdit:        true,
			toolspec.ToolPatch:       false,
		}
		cfg.Settings.Reviewer = config.ReviewerSettings{
			Frequency:     "all",
			Model:         "custom-reviewer",
			ThinkingLevel: "custom-think",
		}
		cfg.Source.Sources["thinking_level"] = "file"
		cfg.Source.Sources["reviewer.model"] = "file"
		cfg.Source.Sources["reviewer.thinking_level"] = "file"
	}, serverapi.CapabilityFactsResponse{Models: serverapi.ModelCapabilityFacts{
		KnownModels: []serverapi.ModelCapabilityFact{{
			ModelID:             &modelID,
			Known:               true,
			ContextWindowTokens: ptr(272_000),
			LargeWindow:         &serverapi.ModelLargeWindowFact{Tokens: 1_000_000},
		}},
	}})

	request, err := onboardingFinalizeRequest(state, false)
	if err != nil {
		t.Fatalf("project request: %v", err)
	}
	if request.Model == nil || request.Model.Kind != serverapi.OnboardingModelKnown {
		t.Fatalf("model = %+v", request.Model)
	}
	if request.MainProvider == nil || request.MainProvider.ProviderOverride == nil || *request.MainProvider.ProviderOverride != "openai" || request.MainProvider.OpenAIBaseURL == nil || *request.MainProvider.OpenAIBaseURL != "http://127.0.0.1:8080/v1" {
		t.Fatalf("main provider = %+v", request.MainProvider)
	}
	toolOverrides := map[toolspec.ID]bool{}
	for _, override := range request.ToolOverrides {
		toolOverrides[override.ID] = override.Enabled
	}
	if len(toolOverrides) != 2 || !toolOverrides[toolspec.ToolEdit] || toolOverrides[toolspec.ToolPatch] {
		t.Fatalf("tool overrides = %+v", request.ToolOverrides)
	}
	if request.ModelTimeoutSeconds == nil || *request.ModelTimeoutSeconds != 123 {
		t.Fatalf("model timeout = %+v", request.ModelTimeoutSeconds)
	}
	if request.ContextWindow == nil || request.ContextWindow.Kind != serverapi.OnboardingContextWindowLarge {
		t.Fatalf("context window = %+v", request.ContextWindow)
	}
	if request.Supervisor == nil || request.Supervisor.Model == nil || request.Supervisor.Model.Kind != serverapi.OnboardingModelCustom {
		t.Fatalf("supervisor = %+v", request.Supervisor)
	}
	if request.CommandsImport == nil || request.CommandsImport.Mode != serverapi.OnboardingImportModeNone {
		t.Fatalf("commands import = %+v", request.CommandsImport)
	}
}

func TestOnboardingPrimaryThinkingChoicePreservesSeededOverride(t *testing.T) {
	state := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
		cfg.Settings.ThinkingLevel = "high"
		cfg.Source.Sources["thinking_level"] = "file"
	}, testOnboardingCapabilityFacts())

	request, err := onboardingFinalizeRequest(state, false)
	if err != nil {
		t.Fatalf("project thinking choice: %v", err)
	}
	if request.Thinking == nil || request.Thinking.Kind != serverapi.OnboardingThinkingLevel || request.Thinking.Level != "high" {
		t.Fatalf("thinking choice = %+v, want explicit high level", request.Thinking)
	}
}

func TestOnboardingFinalizeProjectionPreservesPrimaryThinkingVariants(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*config.App)
		want      serverapi.OnboardingThinkingChoice
	}{
		{
			name: "default",
			want: serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingDefault},
		},
		{
			name: "explicit-supported-level",
			configure: func(cfg *config.App) {
				cfg.Settings.ThinkingLevel = "high"
				cfg.Source.Sources["thinking_level"] = "file"
			},
			want: serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingLevel, Level: "high"},
		},
		{
			name: "custom",
			configure: func(cfg *config.App) {
				cfg.Settings.ThinkingLevel = "ultra"
				cfg.Source.Sources["thinking_level"] = "file"
			},
			want: serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingCustom, Value: "ultra"},
		},
		{
			name: "disabled",
			configure: func(cfg *config.App) {
				cfg.Settings.ThinkingLevel = ""
				cfg.Source.Sources["thinking_level"] = "file"
			},
			want: serverapi.OnboardingThinkingChoice{Kind: serverapi.OnboardingThinkingDisabled},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := newOnboardingFinalizeProjectionState(t, tt.configure, testOnboardingCapabilityFacts())
			request, err := onboardingFinalizeRequest(state, false)
			if err != nil {
				t.Fatalf("project request: %v", err)
			}
			if request.Thinking == nil || !reflect.DeepEqual(*request.Thinking, tt.want) {
				t.Fatalf("thinking = %+v, want %+v", request.Thinking, tt.want)
			}
		})
	}
}

func TestOnboardingFinalizeProjectionPreservesReviewerInheritanceOverridesAndOff(t *testing.T) {
	t.Run("inherited", func(t *testing.T) {
		state := newOnboardingFinalizeProjectionState(t, nil, testOnboardingCapabilityFacts())
		request, err := onboardingFinalizeRequest(state, false)
		if err != nil {
			t.Fatalf("project request: %v", err)
		}
		if request.Supervisor == nil || request.Supervisor.Model != nil || request.Supervisor.Thinking != nil {
			t.Fatalf("inherited supervisor projection = %+v", request.Supervisor)
		}
	})

	t.Run("explicit-same-values", func(t *testing.T) {
		state := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
			cfg.Settings.Reviewer.Model = cfg.Settings.Model
			cfg.Settings.Reviewer.ThinkingLevel = cfg.Settings.ThinkingLevel
			cfg.Source.Sources["reviewer.model"] = "file"
			cfg.Source.Sources["reviewer.thinking_level"] = "file"
		}, testOnboardingCapabilityFacts())
		request, err := onboardingFinalizeRequest(state, false)
		if err != nil {
			t.Fatalf("project request: %v", err)
		}
		if request.Supervisor == nil || request.Supervisor.Model == nil || request.Supervisor.Thinking == nil {
			t.Fatalf("explicit same-valued supervisor projection = %+v", request.Supervisor)
		}
		if request.Supervisor.Thinking.Kind != serverapi.OnboardingThinkingLevel {
			t.Fatalf("reviewer thinking = %+v, want explicit level", request.Supervisor.Thinking)
		}
	})

	t.Run("explicit-disabled", func(t *testing.T) {
		state := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
			cfg.Settings.Reviewer.ThinkingLevel = ""
			cfg.Source.Sources["reviewer.thinking_level"] = "file"
		}, testOnboardingCapabilityFacts())
		request, err := onboardingFinalizeRequest(state, false)
		if err != nil {
			t.Fatalf("project request: %v", err)
		}
		if request.Supervisor == nil || request.Supervisor.Thinking == nil ||
			request.Supervisor.Thinking.Kind != serverapi.OnboardingThinkingDisabled {
			t.Fatalf("disabled reviewer projection = %+v", request.Supervisor)
		}
	})

	t.Run("off-omits-latent-overrides", func(t *testing.T) {
		state := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
			cfg.Settings.Reviewer.Frequency = "off"
			cfg.Settings.Reviewer.Model = cfg.Settings.Model
			cfg.Settings.Reviewer.ThinkingLevel = cfg.Settings.ThinkingLevel
			cfg.Source.Sources["reviewer.model"] = "file"
			cfg.Source.Sources["reviewer.thinking_level"] = "file"
		}, testOnboardingCapabilityFacts())
		request, err := onboardingFinalizeRequest(state, false)
		if err != nil {
			t.Fatalf("project request: %v", err)
		}
		if request.Supervisor == nil || request.Supervisor.Frequency != serverapi.OnboardingSupervisorOff ||
			request.Supervisor.Model != nil || request.Supervisor.Thinking != nil {
			t.Fatalf("off supervisor projection = %+v", request.Supervisor)
		}
	})
}

func TestOnboardingFinalizeProjectionPreservesModelContextAndVerbosityVariants(t *testing.T) {
	customState := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
		cfg.Settings.Model = "team-alias"
		cfg.Settings.ModelContextWindow = 123_000
		cfg.Settings.ModelVerbosity = ""
	}, testOnboardingCapabilityFacts())
	customRequest, err := onboardingFinalizeRequest(customState, false)
	if err != nil {
		t.Fatalf("project custom request: %v", err)
	}
	if customRequest.Model == nil || customRequest.Model.Kind != serverapi.OnboardingModelCustom ||
		customRequest.ContextWindow == nil || customRequest.ContextWindow.Kind != serverapi.OnboardingContextWindowCustom ||
		customRequest.ContextWindow.Tokens != 123_000 || customRequest.Verbosity != nil {
		t.Fatalf("custom model/context/verbosity projection = %+v", customRequest)
	}

	knownState := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
		cfg.Settings.ModelContextWindow = 272_000
	}, testOnboardingCapabilityFacts())
	knownRequest, err := onboardingFinalizeRequest(knownState, false)
	if err != nil {
		t.Fatalf("project known request: %v", err)
	}
	if knownRequest.Model == nil || knownRequest.Model.Kind != serverapi.OnboardingModelKnown ||
		knownRequest.ContextWindow == nil || knownRequest.ContextWindow.Kind != serverapi.OnboardingContextWindowDefault ||
		knownRequest.Verbosity == nil {
		t.Fatalf("known model/default-context/present-verbosity projection = %+v", knownRequest)
	}
}

func TestOnboardingFinalizeProjectionPreservesImportAndDisabledSkills(t *testing.T) {
	root := t.TempDir()
	choice := skillSymlinkChoiceFact("codex", root, 2)
	facts := testOnboardingCapabilityFacts()
	facts.Imports = serverapi.ImportCapabilityFacts{
		Skills: serverapi.ImportItemGroupFact{Choices: []serverapi.ImportChoiceFact{choice}},
		SkillEnablement: []serverapi.SkillEnablementProjectionFact{{
			ChoiceRef: choice.Ref,
			Candidates: []serverapi.ImportItemFact{
				skillItemFact("codex", root, root+"/one", "one", "one", nil, true),
				skillItemFact("codex", root, root+"/two", "two", "two", nil, true),
			},
		}},
	}
	state := newOnboardingFinalizeProjectionState(t, nil, facts)
	importStep := findWorkflowStep(t, &state, onboardingStepSkillsImport)
	importOptionID := ""
	for _, candidate := range state.imports.skillChoices {
		if candidate.Mode == onboardingImportModeSymlinkSource {
			importOptionID = candidate.OptionID
		}
	}
	if err := importStep.apply(&state, importOptionID); err != nil {
		t.Fatalf("select import: %v", err)
	}
	candidates := skillSelectionCandidates(&state)
	if err := findWorkflowStep(t, &state, onboardingStepSkillsEnabled).applyMultiSelect(&state, map[string]bool{
		candidates[0].ID: true,
		candidates[1].ID: false,
	}); err != nil {
		t.Fatalf("select skills: %v", err)
	}
	request, err := onboardingFinalizeRequest(state, false)
	if err != nil {
		t.Fatalf("project request: %v", err)
	}
	if request.SkillsImport == nil || request.SkillsImport.Mode != serverapi.OnboardingImportModeSymlinkSource {
		t.Fatalf("skills import = %+v", request.SkillsImport)
	}
	if !reflect.DeepEqual(request.DisabledSkillNames, []string{"two"}) {
		t.Fatalf("disabled skills = %+v, want [two]", request.DisabledSkillNames)
	}
}

func TestOnboardingRecoverableRetrySubmitsUnchangedRequest(t *testing.T) {
	typedFailure := serverapi.NewOnboardingFinalizeError(
		serverapi.OnboardingFinalizeConfigWriteFailed,
		serverapi.OnboardingConfigWriteFailedDetails{SettingsPath: "/server/config.toml", Operation: "write"},
		errors.New("write failed"),
	)
	finalizer := &recordingOnboardingFinalizer{err: typedFailure}
	state := newOnboardingFinalizeProjectionState(t, func(cfg *config.App) {
		cfg.Settings.ProviderOverride = "openai"
		cfg.Settings.ThinkingLevel = "high"
		cfg.Source.Sources["thinking_level"] = "file"
	}, testOnboardingCapabilityFacts())
	model := newOnboardingModel(newOnboardingFinalization(finalizer, context.Background()), state)

	first := model.finalizeCmd(false)().(onboardingFinalizeDoneMsg)
	next, _ := model.Update(first)
	model = next.(*onboardingModel)
	finalizer.err = nil
	finalizer.response = serverapi.OnboardingFinalizeResponse{Completed: true}
	second := model.finalizeCmd(false)().(onboardingFinalizeDoneMsg)
	if second.err != nil {
		t.Fatalf("retry finalization: %v", second.err)
	}
	if len(finalizer.requests) != 2 || !reflect.DeepEqual(finalizer.requests[0], finalizer.requests[1]) {
		t.Fatalf("retry requests changed without a user edit: %+v", finalizer.requests)
	}
}

func ptr(value int) *int { return &value }

func newOnboardingFinalizeProjectionState(t *testing.T, configure func(*config.App), facts serverapi.CapabilityFactsResponse) onboardingFlowState {
	t.Helper()
	settings := config.DefaultOnboardingSettings()
	cfg := config.App{
		Settings: settings,
		Source: config.SourceReport{Sources: map[string]string{
			"thinking_level":          "default",
			"reviewer.model":          "default",
			"reviewer.thinking_level": "default",
		}},
	}
	if configure != nil {
		configure(&cfg)
	}
	normalizeOnboardingReviewerSeedInheritance(&cfg)
	state, err := newOnboardingFlowState(cfg, facts)
	if err != nil {
		t.Fatalf("construct onboarding state: %v", err)
	}
	return state
}

type onboardingFinalizeClientFunc func(context.Context, serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error)

func (fn onboardingFinalizeClientFunc) FinalizeOnboarding(ctx context.Context, req serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
	return fn(ctx, req)
}
