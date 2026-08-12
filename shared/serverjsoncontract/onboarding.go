package serverjsoncontract

import (
	"encoding/json"

	"core/shared/jsoncontract"
	"core/shared/serverapi"
)

type onboardingFinalizeRequestSource struct {
	Theme               *serverapi.OnboardingTheme               `json:"theme,omitempty" jsonschema:"nullable"`
	MainProvider        *serverapi.OnboardingProviderChoice      `json:"main_provider,omitempty" jsonschema:"nullable"`
	Model               *serverapi.OnboardingModelChoice         `json:"model,omitempty" jsonschema:"nullable"`
	ContextWindow       *serverapi.OnboardingContextWindowChoice `json:"context_window,omitempty" jsonschema:"nullable"`
	Thinking            *serverapi.OnboardingThinkingChoice      `json:"thinking,omitempty" jsonschema:"nullable"`
	Verbosity           *serverapi.OnboardingVerbosity           `json:"verbosity,omitempty" jsonschema:"nullable"`
	ModelTimeoutSeconds *int                                     `json:"model_timeout_seconds,omitempty" jsonschema:"nullable"`
	AskQuestion         *bool                                    `json:"ask_question,omitempty" jsonschema:"nullable"`
	ToolOverrides       []serverapi.OnboardingToolOverride       `json:"tool_overrides,omitempty" jsonschema:"nullable"`
	Supervisor          *serverapi.OnboardingSupervisorChoice    `json:"supervisor,omitempty" jsonschema:"nullable"`
	Compaction          *serverapi.OnboardingCompactionMode      `json:"compaction,omitempty" jsonschema:"nullable"`
	SkillsImport        *onboardingImportSelectionSource         `json:"skills_import,omitempty" jsonschema:"nullable"`
	CommandsImport      *onboardingImportSelectionSource         `json:"commands_import,omitempty" jsonschema:"nullable"`
	DisabledSkillNames  []string                                 `json:"disabled_skill_names,omitempty" jsonschema:"nullable"`
}

type onboardingImportSelectionSource struct {
	Mode             serverapi.OnboardingImportMode `json:"mode"`
	ProviderUUID     *string                        `json:"provider_uuid,omitempty" jsonschema:"nullable"`
	ImportProviderID *string                        `json:"import_provider_id,omitempty" jsonschema:"nullable"`
	SourceRootPath   *string                        `json:"source_root_path,omitempty" jsonschema:"nullable"`
}

type OnboardingFinalizeRequest struct {
	schema jsoncontract.Internal
}

func PrepareOnboardingFinalizeRequest(preparer jsoncontract.Preparer) (OnboardingFinalizeRequest, error) {
	schema, err := preparer.Internal("Onboarding Finalize request", onboardingFinalizeRequestSource{})
	if err != nil {
		return OnboardingFinalizeRequest{}, err
	}
	return OnboardingFinalizeRequest{schema: schema}, nil
}

func (c OnboardingFinalizeRequest) Decode(raw []byte) (serverapi.OnboardingFinalizeRequest, error) {
	if err := c.schema.Validate(raw); err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	var source onboardingFinalizeRequestSource
	if err := json.Unmarshal(raw, &source); err != nil {
		return serverapi.OnboardingFinalizeRequest{}, err
	}
	return serverapi.OnboardingFinalizeRequest{
		Theme:               source.Theme,
		MainProvider:        source.MainProvider,
		Model:               source.Model,
		ContextWindow:       source.ContextWindow,
		Thinking:            source.Thinking,
		Verbosity:           source.Verbosity,
		ModelTimeoutSeconds: source.ModelTimeoutSeconds,
		AskQuestion:         source.AskQuestion,
		ToolOverrides:       source.ToolOverrides,
		Supervisor:          source.Supervisor,
		Compaction:          source.Compaction,
		SkillsImport:        onboardingImportSelectionFromSource(source.SkillsImport),
		CommandsImport:      onboardingImportSelectionFromSource(source.CommandsImport),
		DisabledSkillNames:  source.DisabledSkillNames,
	}, nil
}

func onboardingImportSelectionFromSource(source *onboardingImportSelectionSource) *serverapi.OnboardingImportSelection {
	if source == nil {
		return nil
	}
	selection := serverapi.OnboardingImportSelectionFromWire(
		source.Mode,
		source.ProviderUUID,
		source.ImportProviderID,
		source.SourceRootPath,
	)
	return &selection
}
