package capabilityfacts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/auth"
	"core/server/llm"
	"core/server/onboardingimports"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/valuecopy"
)

const (
	providerRoleCurrentEffective = "current_effective"
	providerRoleExplicitCatalog  = "explicit_catalog"
	contextWindowModeStandard    = "standard"
	thinkingModeDisabled         = "disabled"
	thinkingModeLevel            = "level"
	thinkingModeCustom           = "custom"
)

type Options struct {
	Config      config.App
	AuthManager *auth.Manager
	HomeDir     string
}

type Service struct {
	cfg         config.App
	authManager *auth.Manager
	homeDir     string
}

func NewService(opts Options) *Service {
	return &Service{
		cfg:         opts.Config,
		authManager: opts.AuthManager,
		homeDir:     opts.HomeDir,
	}
}

func (s *Service) GetCapabilityFacts(ctx context.Context, req serverapi.CapabilityFactsRequest) (serverapi.CapabilityFactsResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.CapabilityFactsResponse{}, err
	}
	currentProvider, err := s.currentProviderFacts(ctx)
	if err != nil {
		return serverapi.CapabilityFactsResponse{}, err
	}
	explicitProviders, err := explicitProviderFacts(req.NormalizedExplicitLLMProviderIDs())
	if err != nil {
		return serverapi.CapabilityFactsResponse{}, err
	}
	defaults, err := defaultFacts(s.cfg.Settings)
	if err != nil {
		return serverapi.CapabilityFactsResponse{}, err
	}
	imports, err := onboardingimports.Discover(onboardingimports.Options{
		ConfigRoot:     s.cfg.PersistenceRoot,
		WorkspaceRoot:  req.WorkspaceRoot,
		HomeDir:        s.homeDir,
		DisabledSkills: config.DisabledSkillToggles(s.cfg.Settings),
	})
	if err != nil {
		return serverapi.CapabilityFactsResponse{}, err
	}
	models, err := modelFacts(currentProvider)
	if err != nil {
		return serverapi.CapabilityFactsResponse{}, err
	}
	return serverapi.CapabilityFactsResponse{
		Models: models,
		Providers: serverapi.ProviderCapabilityFacts{
			CurrentEffective: ptr(providerFact(currentProvider, providerRoleCurrentEffective)),
			Explicit:         explicitProviders,
		},
		Imports:  importFacts(imports),
		Defaults: defaults,
	}, nil
}

func (s *Service) currentProviderFacts(ctx context.Context) (llm.ProviderCapabilities, error) {
	authState := auth.EmptyState()
	if s.authManager != nil {
		loaded, err := s.authManager.StoredState(ctx)
		if err != nil {
			return llm.ProviderCapabilities{}, fmt.Errorf("load stored auth state for capability facts: %w", err)
		}
		authState = loaded
	}
	return llm.ResolveRuntimeProviderCapabilities(authState, s.cfg.Settings)
}

func explicitProviderFacts(providerIDs []string) ([]serverapi.LLMProviderCapabilityFact, error) {
	facts := make([]serverapi.LLMProviderCapabilityFact, 0, len(providerIDs))
	for _, providerID := range providerIDs {
		caps, err := llm.InferProviderCapabilities(providerID)
		if err != nil {
			if errors.Is(err, llm.ErrUnsupportedProvider) {
				return nil, fmt.Errorf("%w: %s", serverapi.ErrUnsupportedProvider, providerID)
			}
			return nil, err
		}
		facts = append(facts, providerFact(caps, providerRoleExplicitCatalog))
	}
	return facts, nil
}

func modelFacts(providerCaps llm.ProviderCapabilities) (serverapi.ModelCapabilityFacts, error) {
	contracts := llm.KnownModelCapabilityContracts()
	known := make([]serverapi.ModelCapabilityFact, 0, len(contracts))
	for _, contract := range contracts {
		fact, err := modelFact(contract, providerCaps)
		if err != nil {
			return serverapi.ModelCapabilityFacts{}, err
		}
		known = append(known, fact)
	}
	return serverapi.ModelCapabilityFacts{
		KnownModels:     known,
		UnknownFallback: unknownModelFallback(providerCaps),
	}, nil
}

func modelFact(contract llm.ModelCapabilityContract, providerCaps llm.ProviderCapabilities) (serverapi.ModelCapabilityFact, error) {
	modelID := strings.TrimSpace(contract.Model)
	if modelID == "" {
		return serverapi.ModelCapabilityFact{}, errBlankModelCatalogEntry
	}
	verbosity := llm.VerbositySupportForModelAndProvider(modelID, providerCaps)
	fact := serverapi.ModelCapabilityFact{
		ModelID:                  &modelID,
		Known:                    true,
		ContextWindowTokens:      positiveIntPtr(contract.ContextWindowTokens),
		SupportsThinking:         contract.SupportsReasoningEffort,
		SupportedThinkingLevels:  cloneStrings(llm.SupportedThinkingLevelsModel(modelID)),
		SupportsReasoningSummary: contract.SupportsReasoningSummary,
		SupportsVisionInputs:     contract.SupportsVisionInputs,
		Verbosity:                verbosityFact(verbosity),
	}
	if contract.LargeContextWindowTokens > contract.ContextWindowTokens {
		fact.LargeWindow = &serverapi.ModelLargeWindowFact{Tokens: contract.LargeContextWindowTokens}
		fact.DefaultContextWindowMode = ptr(contextWindowModeStandard)
	}
	return fact, nil
}

func unknownModelFallback(providerCaps llm.ProviderCapabilities) serverapi.ModelCapabilityFact {
	verbosity := llm.VerbositySupportForModelAndProvider("unknown-model", providerCaps)
	return serverapi.ModelCapabilityFact{
		Known:                   false,
		SupportsThinking:        true,
		SupportedThinkingLevels: cloneStrings(llm.SupportedThinkingLevelsModel("unknown-model")),
		Verbosity:               verbosityFact(verbosity),
	}
}

func verbosityFact(verbosity llm.ModelVerbositySupport) serverapi.ModelVerbosityFact {
	return serverapi.ModelVerbosityFact{
		Supported: verbosity.Supported,
		Source:    string(verbosity.Source),
		Levels:    cloneStrings(verbosity.Levels),
	}
}

func providerFact(caps llm.ProviderCapabilities, role string) serverapi.LLMProviderCapabilityFact {
	return serverapi.LLMProviderCapabilityFact{
		LLMProviderID:                 strings.TrimSpace(caps.ProviderID),
		Role:                          role,
		SupportsResponsesAPI:          caps.SupportsResponsesAPI,
		SupportsNativeCompaction:      caps.SupportsResponsesCompact,
		SupportsInputTokenCount:       caps.SupportsRequestInputTokenCount,
		SupportsPromptCacheKey:        caps.SupportsPromptCacheKey,
		SupportsNativeWebSearch:       caps.SupportsNativeWebSearch,
		SupportsReasoningEncryption:   caps.SupportsReasoningEncrypted,
		SupportsServerSideContextEdit: caps.SupportsServerSideContextEdit,
		IsOpenAIFirstParty:            caps.IsOpenAIFirstParty,
		SupportsProviderVerbosity:     caps.SupportsProviderVerbosity,
	}
}

func defaultFacts(settings config.Settings) (serverapi.CapabilityDefaultFacts, error) {
	modelID := strings.TrimSpace(settings.Model)
	if modelID == "" {
		return serverapi.CapabilityDefaultFacts{}, errors.New("capability facts require a non-blank primary model")
	}
	return serverapi.CapabilityDefaultFacts{
		PrimaryModelID: modelID,
		Thinking:       thinkingDefaultFact(settings.ThinkingLevel),
		Verbosity:      verbosityDefaultFact(settings.ModelVerbosity),
		CompactionMode: strings.TrimSpace(string(settings.CompactionMode)),
	}, nil
}

func verbosityDefaultFact(raw config.ModelVerbosity) *serverapi.VerbosityDefaultFact {
	level := strings.TrimSpace(string(raw))
	if level == "" {
		return nil
	}
	return &serverapi.VerbosityDefaultFact{Level: level}
}

func thinkingDefaultFact(raw string) serverapi.ThinkingDefaultFact {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return serverapi.ThinkingDefaultFact{Mode: thinkingModeDisabled}
	}
	normalized, ok := clientui.NormalizeThinkingLevel(trimmed)
	if ok {
		return serverapi.ThinkingDefaultFact{Mode: thinkingModeLevel, Level: &normalized}
	}
	return serverapi.ThinkingDefaultFact{Mode: thinkingModeCustom, Value: &trimmed}
}

func importFacts(result onboardingimports.Result) serverapi.ImportCapabilityFacts {
	return serverapi.ImportCapabilityFacts{
		Workspace:       serverapi.ImportWorkspaceFact{Root: valuecopy.Pointer(result.Workspace.Root)},
		Skills:          itemGroupFact(result.Skills),
		Commands:        itemGroupFact(result.Commands),
		SkillEnablement: skillEnablementFacts(result.SkillEnablement),
		Errors:          importErrorFacts(result.Errors),
		Recommendations: importRecommendationFacts(result.Recommendations),
	}
}

func itemGroupFact(group onboardingimports.ItemGroup) serverapi.ImportItemGroupFact {
	return serverapi.ImportItemGroupFact{
		Choices: importChoiceFacts(group.Choices),
		Roots:   importRootFacts(group.Roots),
		Items:   importItemFacts(group.Items),
		Target:  importTargetFact(group.Target),
	}
}

func importTargetFact(target onboardingimports.Target) serverapi.ImportTargetFact {
	return serverapi.ImportTargetFact{
		Skip:      target.Skip,
		Conflicts: conflictFacts(target.Conflicts),
	}
}

func importChoiceFacts(choices []onboardingimports.Choice) []serverapi.ImportChoiceFact {
	out := make([]serverapi.ImportChoiceFact, 0, len(choices))
	for _, choice := range choices {
		out = append(out, serverapi.ImportChoiceFact{
			Ref:              choiceRefFact(choice.Ref),
			ImportProviderID: providerIDPtr(choice.ProviderID),
			SourceRootPath:   valuecopy.Pointer(choice.SourceRoot),
			ItemCount:        choice.ItemCount,
		})
	}
	return out
}

func importRootFacts(roots []onboardingimports.Root) []serverapi.ImportRootFact {
	out := make([]serverapi.ImportRootFact, 0, len(roots))
	for _, root := range roots {
		out = append(out, serverapi.ImportRootFact{
			SourceKind:       string(root.SourceKind),
			ImportProviderID: providerIDPtr(root.ProviderID),
			Path:             root.Path,
			Exists:           root.Exists,
		})
	}
	return out
}

func importItemFacts(items []onboardingimports.Item) []serverapi.ImportItemFact {
	out := make([]serverapi.ImportItemFact, 0, len(items))
	for _, item := range items {
		out = append(out, importItemFact(item))
	}
	return out
}

func importItemFact(item onboardingimports.Item) serverapi.ImportItemFact {
	return serverapi.ImportItemFact{
		Ref:            itemRefFact(item.Ref),
		Conflicts:      conflictFacts(item.Conflicts),
		DefaultEnabled: valuecopy.Pointer(item.DefaultEnabled),
	}
}

func itemRefFact(ref onboardingimports.ItemRef) serverapi.ImportItemRef {
	return serverapi.ImportItemRef{
		ItemKind:         string(ref.ItemKind),
		SourceKind:       string(ref.SourceKind),
		ImportProviderID: providerIDPtr(ref.ProviderID),
		SourceRootPath:   valuecopy.Pointer(ref.SourceRoot),
		SourcePath:       valuecopy.Pointer(ref.SourcePath),
		TargetName:       ref.TargetName,
		Name:             valuecopy.Pointer(ref.Name),
		ModifiedUnixMs:   valuecopy.Pointer(ref.ModifiedUnixMs),
	}
}

func choiceRefFact(ref onboardingimports.ChoiceRef) serverapi.ImportChoiceRef {
	return serverapi.ImportChoiceRef{
		Mode:             string(ref.Mode),
		SourceKind:       sourceKindPtr(ref.SourceKind),
		ImportProviderID: providerIDPtr(ref.ProviderID),
		SourceRootPath:   valuecopy.Pointer(ref.SourceRoot),
	}
}

func conflictFacts(conflicts []onboardingimports.Conflict) []serverapi.ImportConflictFact {
	out := make([]serverapi.ImportConflictFact, 0, len(conflicts))
	for _, conflict := range conflicts {
		out = append(out, serverapi.ImportConflictFact{
			SourceKind:       string(conflict.SourceKind),
			ImportProviderID: providerIDPtr(conflict.ProviderID),
			Path:             valuecopy.Pointer(conflict.Path),
		})
	}
	return out
}

func skillEnablementFacts(projections []onboardingimports.SkillEnablementProjection) []serverapi.SkillEnablementProjectionFact {
	out := make([]serverapi.SkillEnablementProjectionFact, 0, len(projections))
	for _, projection := range projections {
		out = append(out, serverapi.SkillEnablementProjectionFact{
			ChoiceRef:  choiceRefFact(projection.ChoiceRef),
			Candidates: importItemFacts(projection.Candidates),
		})
	}
	return out
}

func importErrorFacts(errs []onboardingimports.Error) []serverapi.ImportErrorFact {
	out := make([]serverapi.ImportErrorFact, 0, len(errs))
	for _, err := range errs {
		out = append(out, serverapi.ImportErrorFact{
			Code:             err.Code,
			Scope:            string(err.Scope),
			ItemKind:         importErrorItemKind(err.ItemKind),
			ImportProviderID: providerIDPtr(err.ProviderID),
			Path:             valuecopy.Pointer(err.Path),
			Operation:        err.Operation,
			Message:          err.Message,
		})
	}
	return out
}

func importErrorItemKind(value *onboardingimports.ItemKind) *serverapi.ImportErrorItemKind {
	if value == nil {
		return nil
	}
	raw := serverapi.ImportErrorItemKind(*value)
	return &raw
}

func importRecommendationFacts(recommendations onboardingimports.Recommendations) serverapi.ImportRecommendationFacts {
	return serverapi.ImportRecommendationFacts{
		Skills:   modeRecommendationFact(recommendations.Skills),
		Commands: modeRecommendationFact(recommendations.Commands),
	}
}

func modeRecommendationFact(recommendation *onboardingimports.ModeRecommendation) *serverapi.ImportModeRecommendationFact {
	if recommendation == nil {
		return nil
	}
	return &serverapi.ImportModeRecommendationFact{
		ChoiceRef:   choiceRefFact(recommendation.ChoiceRef),
		ItemCount:   recommendation.ItemCount,
		SourcePaths: cloneStrings(recommendation.SourcePaths),
	}
}

func positiveIntPtr(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}

func providerIDPtr(value *onboardingimports.ProviderID) *string {
	if value == nil {
		return nil
	}
	out := string(*value)
	return &out
}

func sourceKindPtr(value *onboardingimports.SourceKind) *string {
	if value == nil {
		return nil
	}
	out := string(*value)
	return &out
}

func ptr[T any](value T) *T {
	return &value
}
