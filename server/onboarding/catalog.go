package onboarding

import (
	"fmt"

	"core/server/onboardingimports"

	"github.com/google/uuid"
)

var (
	providerClaudeCodeUUID = uuid.MustParse("3a8f7b06-2e28-4e5a-91dc-4a57e54c6b87")
	providerCodexUUID      = uuid.MustParse("a48bd9cb-8a99-470c-b88d-3f087369a8dc")
	providerAgentsUUID     = uuid.MustParse("76ff2a0e-bf7c-4d62-83fb-82d22bd63661")
)

type Provider struct {
	UUID             uuid.UUID `json:"uuid"`
	ImportProviderID onboardingimports.ProviderID
	HomeEntry        string
}

func ProductionProviderCatalog() []Provider {
	providerUUIDs := map[onboardingimports.ProviderID]uuid.UUID{
		onboardingimports.ProviderClaudeCode: providerClaudeCodeUUID,
		onboardingimports.ProviderCodex:      providerCodexUUID,
		onboardingimports.ProviderAgents:     providerAgentsUUID,
	}
	providers := onboardingimports.Providers()
	out := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		providerUUID, ok := providerUUIDs[provider.ID]
		if !ok || providerUUID == uuid.Nil {
			panic(fmt.Sprintf("onboarding provider %q has no stable uuid mapping", provider.ID))
		}
		out = append(out, Provider{UUID: providerUUID, ImportProviderID: provider.ID, HomeEntry: provider.HomeEntry})
	}
	return out
}
