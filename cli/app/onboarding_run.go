package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func runOnboardingFlow(ctx context.Context, cfg config.App, factsClient client.CapabilityFactsClient, finalizer client.OnboardingFinalizeClient) (onboardingResult, error) {
	if factsClient == nil {
		return onboardingResult{}, errors.New("capability facts client is required")
	}
	if finalizer == nil {
		return onboardingResult{}, errors.New("onboarding finalization client is required")
	}
	var workspaceRoot *string
	if strings.TrimSpace(cfg.WorkspaceRoot) != "" {
		workspaceRoot = &cfg.WorkspaceRoot
	}
	facts, err := factsClient.GetCapabilityFacts(ctx, serverapi.CapabilityFactsRequest{WorkspaceRoot: workspaceRoot})
	if err != nil {
		return onboardingResult{}, err
	}
	state := onboardingFlowState{
		settings:         cfg.Settings,
		baselineSettings: cfg.Settings,
		theme:            cfg.Settings.Theme,
		facts:            facts,
		imports:          onboardingImportDiscoveryFromFacts(facts.Imports),
		skillImport:      onboardingImportSelection{Mode: onboardingImportModeNone},
	}
	finalization := newOnboardingFinalization(finalizer, ctx)
	model := newOnboardingModel(finalization, state)
	terminalCursor := newUITerminalCursorState()
	model.terminalCursor = terminalCursor
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithOutput(newUITerminalCursorWriter(os.Stdout, terminalCursor)))
	finalModel, err := program.Run()
	if err != nil {
		if outcome, submitted := finalization.waitIfSubmitted(); submitted {
			return onboardingFlowOutcome(outcome)
		}
		return onboardingResult{}, err
	}
	finalized, ok := finalModel.(*onboardingModel)
	if !ok {
		return onboardingResult{}, fmt.Errorf("unexpected onboarding model type %T", finalModel)
	}
	if finalized.canceled {
		return onboardingResult{}, errors.New("first-time setup canceled")
	}
	if outcome, submitted := finalization.waitIfSubmitted(); submitted {
		return onboardingFlowOutcome(outcome)
	}
	if finalized.terminalErr != nil {
		return onboardingResult{}, finalized.terminalErr
	}
	return finalized.result, nil
}

func onboardingFlowOutcome(outcome onboardingFinalizeDoneMsg) (onboardingResult, error) {
	if outcome.err != nil {
		return onboardingResult{}, outcome.err
	}
	return outcome.result, nil
}
