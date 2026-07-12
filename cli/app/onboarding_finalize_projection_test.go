package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/client"
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

var _ client.OnboardingFinalizeClient = (*recordingOnboardingFinalizer)(nil)

type onboardingCapabilityFactsClientFunc func(context.Context, serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error)

func (fn onboardingCapabilityFactsClientFunc) GetCapabilityFacts(ctx context.Context, req serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error) {
	return fn(ctx, req)
}

var _ client.CapabilityFactsClient = onboardingCapabilityFactsClientFunc(nil)

func TestOnboardingDefaultsFinalizeThroughServerAPI(t *testing.T) {
	finalizer := &recordingOnboardingFinalizer{response: serverapi.OnboardingFinalizeResponse{
		Completed:    true,
		SettingsPath: "/server/.kent/config.toml",
	}}
	model := newOnboardingModel(newOnboardingFinalization(finalizer, context.Background()), onboardingFlowState{
		settings: config.Settings{Theme: theme.Dark},
	})

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
	model := newOnboardingModel(newOnboardingFinalization(&recordingOnboardingFinalizer{err: expected}, context.Background()), onboardingFlowState{
		settings: config.Settings{Theme: theme.Light},
	})

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
	model := newOnboardingModel(newOnboardingFinalization(finalizer, ctx), onboardingFlowState{settings: config.Settings{Theme: theme.Light}})

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
	model := newOnboardingModel(nil, onboardingFlowState{})
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
	model := newOnboardingModel(finalization, onboardingFlowState{settings: config.Settings{Theme: theme.Light}})

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
	model := newOnboardingModel(finalization, onboardingFlowState{settings: config.Settings{Theme: theme.Light}})

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
	model := newOnboardingModel(finalization, onboardingFlowState{settings: config.Settings{Theme: theme.Light}})

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
	model := newOnboardingModel(finalization, onboardingFlowState{settings: config.Settings{Theme: theme.Light}})

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
	state := onboardingFlowState{
		settings: config.Settings{
			Theme:              theme.Light,
			Model:              modelID,
			ModelContextWindow: 1_000_000,
			ThinkingLevel:      "high",
			ModelVerbosity:     config.ModelVerbosityHigh,
			CompactionMode:     config.CompactionModeNative,
			EnabledTools:       map[toolspec.ID]bool{toolspec.ToolAskQuestion: true},
			Reviewer: config.ReviewerSettings{
				Frequency:     "all",
				Model:         "custom-reviewer",
				ThinkingLevel: "custom-think",
			},
		},
		facts: serverapi.CapabilityFactsResponse{Models: serverapi.ModelCapabilityFacts{
			KnownModels: []serverapi.ModelCapabilityFact{{
				ModelID:             &modelID,
				Known:               true,
				ContextWindowTokens: ptr(272_000),
				LargeWindow:         &serverapi.ModelLargeWindowFact{Tokens: 1_000_000},
			}},
		}},
		customThinking:              false,
		reviewerCustomModel:         true,
		reviewerCustomThinking:      true,
		reviewerCustomThinkingInput: true,
		skillImport:                 onboardingImportSelection{Mode: onboardingImportModeNone},
	}

	request, err := onboardingFinalizeRequest(state, false)
	if err != nil {
		t.Fatalf("project request: %v", err)
	}
	if request.Model == nil || request.Model.Kind != serverapi.OnboardingModelKnown {
		t.Fatalf("model = %+v", request.Model)
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

func ptr(value int) *int { return &value }

type onboardingFinalizeClientFunc func(context.Context, serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error)

func (fn onboardingFinalizeClientFunc) FinalizeOnboarding(ctx context.Context, req serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
	return fn(ctx, req)
}
