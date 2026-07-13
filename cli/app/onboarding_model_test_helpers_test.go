package app

import (
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
)

func newOnboardingModelForWorkspace(_ string, _ string, state onboardingFlowState) *onboardingModel {
	return newOnboardingModel(nil, state)
}

func testOnboardingFlowState(t *testing.T, mutate func(*config.App), suppliedFacts ...serverapi.CapabilityFactsResponse) onboardingFlowState {
	t.Helper()
	cfg := onboardingSeedConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	if cfg.Source.Sources["reviewer.model"] == "default" {
		cfg.Settings.Reviewer.Model = cfg.Settings.Model
	}
	if cfg.Source.Sources["reviewer.thinking_level"] == "default" {
		cfg.Settings.Reviewer.ThinkingLevel = cfg.Settings.ThinkingLevel
	}
	facts := testOnboardingCapabilityFacts()
	if len(suppliedFacts) > 0 {
		facts = suppliedFacts[0]
	}
	state, err := newOnboardingFlowState(cfg, facts)
	if err != nil {
		t.Fatalf("construct test onboarding state: %v", err)
	}
	return state
}

func testOnboardingFlowStatePtr(t *testing.T, mutate func(*config.App), suppliedFacts ...serverapi.CapabilityFactsResponse) *onboardingFlowState {
	t.Helper()
	state := testOnboardingFlowState(t, mutate, suppliedFacts...)
	return &state
}
