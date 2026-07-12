package app

func newOnboardingModelForWorkspace(_ string, _ string, state onboardingFlowState) *onboardingModel {
	return newOnboardingModel(nil, state)
}
