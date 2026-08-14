package serverjsoncontract

import (
	"encoding/json"

	"core/shared/jsoncontract"
	"core/shared/serverapi"
)

type OnboardingFinalizeRequest struct {
	schema jsoncontract.Internal
}

func PrepareOnboardingFinalizeRequest(preparer jsoncontract.Preparer) (OnboardingFinalizeRequest, error) {
	schema, err := preparer.Internal("Onboarding Finalize request", serverapi.OnboardingFinalizeRequestWire{})
	if err != nil {
		return OnboardingFinalizeRequest{}, err
	}
	return OnboardingFinalizeRequest{schema: schema}, nil
}

func (c OnboardingFinalizeRequest) Decode(raw []byte) (serverapi.OnboardingFinalizeRequest, error) {
	if err := c.schema.Validate(raw); err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	var wire serverapi.OnboardingFinalizeRequestWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	return wire.Request(), nil
}
