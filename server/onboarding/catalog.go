package onboarding

import (
	"path/filepath"

	"github.com/google/uuid"
)

var (
	providerClaudeCodeUUID = uuid.MustParse("3a8f7b06-2e28-4e5a-91dc-4a57e54c6b87")
	providerCodexUUID      = uuid.MustParse("a48bd9cb-8a99-470c-b88d-3f087369a8dc")
	providerAgentsUUID     = uuid.MustParse("76ff2a0e-bf7c-4d62-83fb-82d22bd63661")
)

type Provider struct {
	UUID                  uuid.UUID `json:"uuid"`
	HomeEntry             string
	SkillSourceCandidates []string
	SupportsCommandImport bool
}

func ProductionProviderCatalog() []Provider {
	return []Provider{
		{UUID: providerClaudeCodeUUID, HomeEntry: ".claude", SkillSourceCandidates: []string{"skills"}, SupportsCommandImport: true},
		{UUID: providerCodexUUID, HomeEntry: ".codex", SkillSourceCandidates: []string{filepath.Join("skills", "local"), "skills"}, SupportsCommandImport: true},
		{UUID: providerAgentsUUID, HomeEntry: ".agents", SkillSourceCandidates: []string{"skills"}, SupportsCommandImport: true},
	}
}
