package capabilityfacts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"core/server/auth"
	"core/server/llm"
	"core/server/onboardingimports"
	"core/shared/clientui"
	"core/shared/config"
	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
	"core/shared/serverapi"
	"core/shared/textutil"
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
	return &Service{cfg: opts.Config, authManager: opts.AuthManager, homeDir: opts.HomeDir}
}

func (s *Service) GetFacts(ctx context.Context, req *capabilitypb.GetFactsRequest) (*capabilitypb.Facts, error) {
	if req == nil {
		return nil, errors.New("capability facts request is required")
	}
	providerIDs := normalizedProviderIDs(req.ExplicitLlmProviderIds)
	currentProvider, err := s.currentProviderFacts(ctx)
	if err != nil {
		return nil, err
	}
	explicitProviders, err := explicitProviderFacts(providerIDs)
	if err != nil {
		return nil, err
	}
	defaults, err := defaultFacts(s.cfg.Settings)
	if err != nil {
		return nil, err
	}
	imports, err := onboardingimports.Discover(onboardingimports.Options{
		ConfigRoot:    s.cfg.PersistenceRoot,
		WorkspaceRoot: req.WorkspaceRoot,
		HomeDir:       s.homeDir,
		SkillPolicy:   config.ResolveSkillPolicy(s.cfg.Settings),
	})
	if err != nil {
		return nil, err
	}
	models, err := modelFacts(currentProvider)
	if err != nil {
		return nil, err
	}
	return &capabilitypb.Facts{
		Models: models,
		Providers: &capabilitypb.ProviderFacts{
			CurrentEffective: providerFact(currentProvider, providerRoleCurrentEffective),
			Explicit:         explicitProviders,
		},
		Imports:         importFacts(imports),
		Defaults:        defaults,
		Recommendations: &capabilitypb.RecommendationFacts{},
	}, nil
}

func normalizedProviderIDs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func (s *Service) currentProviderFacts(ctx context.Context) (llm.ProviderCapabilities, error) {
	authState := auth.EmptyState()
	if s.authManager != nil {
		loaded, err := s.authManager.Load(ctx)
		if err != nil {
			return llm.ProviderCapabilities{}, fmt.Errorf("load stored auth state for capability facts: %w", err)
		}
		authState = loaded
	}
	return llm.ResolveRuntimeProviderCapabilities(authState, s.cfg.Settings)
}

func explicitProviderFacts(providerIDs []string) ([]*capabilitypb.ProviderFact, error) {
	facts := make([]*capabilitypb.ProviderFact, 0, len(providerIDs))
	for _, providerID := range providerIDs {
		caps, err := llm.InferProviderCapabilities(providerID)
		if err != nil {
			if errors.Is(err, llm.ErrUnsupportedProvider) {
				return nil, &serverapi.UnsupportedProviderError{ProviderID: providerID}
			}
			return nil, err
		}
		facts = append(facts, providerFact(caps, providerRoleExplicitCatalog))
	}
	return facts, nil
}

func modelFacts(providerCaps llm.ProviderCapabilities) (*capabilitypb.ModelFacts, error) {
	contracts := llm.KnownModelCapabilityContracts()
	known := make([]*capabilitypb.ModelFact, 0, len(contracts))
	for _, contract := range contracts {
		fact, err := modelFact(contract, providerCaps)
		if err != nil {
			return nil, err
		}
		known = append(known, fact)
	}
	return &capabilitypb.ModelFacts{KnownModels: known, UnknownFallback: unknownModelFallback(providerCaps)}, nil
}

func modelFact(contract llm.ModelCapabilityContract, providerCaps llm.ProviderCapabilities) (*capabilitypb.ModelFact, error) {
	modelID := strings.TrimSpace(contract.Model)
	if modelID == "" {
		return nil, errBlankModelCatalogEntry
	}
	contextWindow, err := positiveUint32Ptr(contract.ContextWindowTokens, "model context window")
	if err != nil {
		return nil, err
	}
	fact := &capabilitypb.ModelFact{
		ModelId:                  &modelID,
		Known:                    true,
		ContextWindowTokens:      contextWindow,
		SupportsThinking:         contract.SupportsReasoningEffort,
		SupportedThinkingLevels:  cloneStrings(llm.SupportedThinkingLevelsModel(modelID)),
		SupportsReasoningSummary: contract.SupportsReasoningSummary,
		SupportsVisionInputs:     contract.SupportsVisionInputs,
		Verbosity:                verbosityFact(llm.VerbositySupportForModelAndProvider(modelID, providerCaps)),
	}
	if contract.LargeContextWindowTokens > contract.ContextWindowTokens {
		tokens, err := uint32Value(contract.LargeContextWindowTokens, "model large context window")
		if err != nil {
			return nil, err
		}
		fact.LargeWindow = &capabilitypb.ModelLargeWindowFact{Tokens: tokens}
		fact.DefaultContextWindowMode = ptr(contextWindowModeStandard)
	}
	return fact, nil
}

func unknownModelFallback(providerCaps llm.ProviderCapabilities) *capabilitypb.ModelFact {
	return &capabilitypb.ModelFact{
		SupportsThinking:        true,
		SupportedThinkingLevels: cloneStrings(llm.SupportedThinkingLevelsModel("unknown-model")),
		Verbosity:               verbosityFact(llm.VerbositySupportForModelAndProvider("unknown-model", providerCaps)),
	}
}

func verbosityFact(verbosity llm.ModelVerbositySupport) *capabilitypb.ModelVerbosityFact {
	return &capabilitypb.ModelVerbosityFact{
		Supported: verbosity.Supported,
		Source:    string(verbosity.Source),
		Levels:    cloneStrings(verbosity.Levels),
	}
}

func providerFact(caps llm.ProviderCapabilities, role string) *capabilitypb.ProviderFact {
	return &capabilitypb.ProviderFact{
		LlmProviderId:                 strings.TrimSpace(caps.ProviderID),
		Role:                          role,
		SupportsResponsesApi:          caps.SupportsResponsesAPI,
		SupportsNativeCompaction:      caps.SupportsResponsesCompact,
		SupportsPromptCacheKey:        caps.SupportsPromptCacheKey,
		SupportsNativeWebSearch:       caps.SupportsNativeWebSearch,
		SupportsReasoningEncryption:   caps.SupportsReasoningEncrypted,
		SupportsServerSideContextEdit: caps.SupportsServerSideContextEdit,
		IsOpenaiFirstParty:            caps.IsOpenAIFirstParty,
		SupportsProviderVerbosity:     caps.SupportsProviderVerbosity,
	}
}

func defaultFacts(settings config.Settings) (*capabilitypb.DefaultFacts, error) {
	modelID := strings.TrimSpace(settings.Model)
	if modelID == "" {
		return nil, errors.New("capability facts require a non-blank primary model")
	}
	return &capabilitypb.DefaultFacts{
		PrimaryModelId: modelID,
		Thinking:       thinkingDefaultFact(settings.ThinkingLevel),
		Verbosity:      verbosityDefaultFact(settings.ModelVerbosity),
		CompactionMode: strings.TrimSpace(string(settings.CompactionMode)),
	}, nil
}

func verbosityDefaultFact(raw config.ModelVerbosity) *capabilitypb.VerbosityDefaultFact {
	level := strings.TrimSpace(string(raw))
	if level == "" {
		return nil
	}
	return &capabilitypb.VerbosityDefaultFact{Level: level}
}

func thinkingDefaultFact(raw string) *capabilitypb.ThinkingDefaultFact {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return &capabilitypb.ThinkingDefaultFact{Mode: thinkingModeDisabled}
	}
	normalized, ok := clientui.NormalizeThinkingLevel(trimmed)
	if ok {
		return &capabilitypb.ThinkingDefaultFact{Mode: thinkingModeLevel, Level: &normalized}
	}
	return &capabilitypb.ThinkingDefaultFact{Mode: thinkingModeCustom, Value: &trimmed}
}

func importFacts(result onboardingimports.Result) *capabilitypb.ImportFacts {
	return &capabilitypb.ImportFacts{
		Workspace:       &capabilitypb.ImportWorkspaceFact{Root: textutil.Pointer(result.Workspace.Root)},
		Skills:          itemGroupFact(result.Skills),
		Commands:        itemGroupFact(result.Commands),
		SkillEnablement: skillEnablementFacts(result.SkillEnablement),
		Errors:          importErrorFacts(result.Errors),
		Recommendations: importRecommendationFacts(result.Recommendations),
	}
}

func itemGroupFact(group onboardingimports.ItemGroup) *capabilitypb.ImportItemGroupFact {
	return &capabilitypb.ImportItemGroupFact{
		Choices: importChoiceFacts(group.Choices),
		Roots:   importRootFacts(group.Roots),
		Items:   importItemFacts(group.Items),
		Target: &capabilitypb.ImportTargetFact{
			Skip:      group.Target.Skip,
			Conflicts: conflictFacts(group.Target.Conflicts),
		},
	}
}

func importChoiceFacts(choices []onboardingimports.Choice) []*capabilitypb.ImportChoiceFact {
	out := make([]*capabilitypb.ImportChoiceFact, 0, len(choices))
	for _, choice := range choices {
		out = append(out, &capabilitypb.ImportChoiceFact{
			Ref:              choiceRefFact(choice.Ref),
			ImportProviderId: providerIDPtr(choice.ProviderID),
			SourceRootPath:   textutil.Pointer(choice.SourceRoot),
			ItemCount:        uint32(choice.ItemCount),
		})
	}
	return out
}

func importRootFacts(roots []onboardingimports.Root) []*capabilitypb.ImportRootFact {
	out := make([]*capabilitypb.ImportRootFact, 0, len(roots))
	for _, root := range roots {
		out = append(out, &capabilitypb.ImportRootFact{
			SourceKind:       sourceKind(root.SourceKind),
			ImportProviderId: providerIDPtr(root.ProviderID),
			Path:             root.Path,
			Exists:           root.Exists,
		})
	}
	return out
}

func importItemFacts(items []onboardingimports.Item) []*capabilitypb.ImportItemFact {
	out := make([]*capabilitypb.ImportItemFact, 0, len(items))
	for _, item := range items {
		out = append(out, &capabilitypb.ImportItemFact{
			Ref:            itemRefFact(item.Ref),
			Conflicts:      conflictFacts(item.Conflicts),
			DefaultEnabled: textutil.Pointer(item.DefaultEnabled),
		})
	}
	return out
}

func itemRefFact(ref onboardingimports.ItemRef) *capabilitypb.ImportItemRef {
	return &capabilitypb.ImportItemRef{
		ItemKind:         itemKind(ref.ItemKind),
		SourceKind:       sourceKind(ref.SourceKind),
		ImportProviderId: providerIDPtr(ref.ProviderID),
		SourceRootPath:   textutil.Pointer(ref.SourceRoot),
		SourcePath:       textutil.Pointer(ref.SourcePath),
		TargetName:       ref.TargetName,
		Name:             textutil.Pointer(ref.Name),
		ModifiedUnixMs:   textutil.Pointer(ref.ModifiedUnixMs),
	}
}

func choiceRefFact(ref onboardingimports.ChoiceRef) *capabilitypb.ImportChoiceRef {
	fact := &capabilitypb.ImportChoiceRef{
		Mode:             choiceMode(ref.Mode),
		ImportProviderId: providerIDPtr(ref.ProviderID),
		SourceRootPath:   textutil.Pointer(ref.SourceRoot),
	}
	if ref.SourceKind != nil {
		value := sourceKind(*ref.SourceKind)
		fact.SourceKind = &value
	}
	return fact
}

func conflictFacts(conflicts []onboardingimports.Conflict) []*capabilitypb.ImportConflictFact {
	out := make([]*capabilitypb.ImportConflictFact, 0, len(conflicts))
	for _, conflict := range conflicts {
		out = append(out, &capabilitypb.ImportConflictFact{
			SourceKind:       sourceKind(conflict.SourceKind),
			ImportProviderId: providerIDPtr(conflict.ProviderID),
			Path:             textutil.Pointer(conflict.Path),
		})
	}
	return out
}

func skillEnablementFacts(projections []onboardingimports.SkillEnablementProjection) []*capabilitypb.SkillEnablementProjectionFact {
	out := make([]*capabilitypb.SkillEnablementProjectionFact, 0, len(projections))
	for _, projection := range projections {
		out = append(out, &capabilitypb.SkillEnablementProjectionFact{
			ChoiceRef:  choiceRefFact(projection.ChoiceRef),
			Candidates: importItemFacts(projection.Candidates),
		})
	}
	return out
}

func importErrorFacts(errs []onboardingimports.Error) []*capabilitypb.ImportErrorFact {
	out := make([]*capabilitypb.ImportErrorFact, 0, len(errs))
	for _, item := range errs {
		fact := &capabilitypb.ImportErrorFact{
			Code:             item.Code,
			Scope:            string(item.Scope),
			ImportProviderId: providerIDPtr(item.ProviderID),
			Path:             textutil.Pointer(item.Path),
			Operation:        item.Operation,
			Message:          item.Message,
		}
		if item.ItemKind != nil {
			value := itemKind(*item.ItemKind)
			fact.ItemKind = &value
		}
		out = append(out, fact)
	}
	return out
}

func importRecommendationFacts(recommendations onboardingimports.Recommendations) *capabilitypb.ImportRecommendationFacts {
	return &capabilitypb.ImportRecommendationFacts{
		Skills:   modeRecommendationFact(recommendations.Skills),
		Commands: modeRecommendationFact(recommendations.Commands),
	}
}

func modeRecommendationFact(recommendation *onboardingimports.ModeRecommendation) *capabilitypb.ImportModeRecommendationFact {
	if recommendation == nil {
		return nil
	}
	return &capabilitypb.ImportModeRecommendationFact{
		ChoiceRef:   choiceRefFact(recommendation.ChoiceRef),
		ItemCount:   uint32(recommendation.ItemCount),
		SourcePaths: cloneStrings(recommendation.SourcePaths),
	}
}

func choiceMode(value onboardingimports.ChoiceMode) capabilitypb.ImportChoiceMode {
	switch value {
	case onboardingimports.ChoiceModeNone:
		return capabilitypb.ImportChoiceMode_IMPORT_CHOICE_MODE_NONE
	case onboardingimports.ChoiceModeSymlinkSource:
		return capabilitypb.ImportChoiceMode_IMPORT_CHOICE_MODE_SYMLINK_SOURCE
	default:
		return capabilitypb.ImportChoiceMode_IMPORT_CHOICE_MODE_UNSPECIFIED
	}
}

func sourceKind(value onboardingimports.SourceKind) capabilitypb.ImportSourceKind {
	switch value {
	case onboardingimports.SourceKindExternalProvider:
		return capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_EXTERNAL_PROVIDER
	case onboardingimports.SourceKindGenerated:
		return capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_GENERATED
	case onboardingimports.SourceKindGlobal:
		return capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_GLOBAL
	case onboardingimports.SourceKindWorkspace:
		return capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_WORKSPACE
	default:
		return capabilitypb.ImportSourceKind_IMPORT_SOURCE_KIND_UNSPECIFIED
	}
}

func itemKind(value onboardingimports.ItemKind) capabilitypb.ImportItemKind {
	switch value {
	case onboardingimports.ItemKindSkill:
		return capabilitypb.ImportItemKind_IMPORT_ITEM_KIND_SKILL
	case onboardingimports.ItemKindCommand:
		return capabilitypb.ImportItemKind_IMPORT_ITEM_KIND_COMMAND
	default:
		return capabilitypb.ImportItemKind_IMPORT_ITEM_KIND_UNSPECIFIED
	}
}

func positiveUint32Ptr(value int, field string) (*uint32, error) {
	if value <= 0 {
		return nil, nil
	}
	converted, err := uint32Value(value, field)
	if err != nil {
		return nil, err
	}
	return &converted, nil
}

func uint32Value(value int, field string) (uint32, error) {
	if value < 0 || uint64(value) > math.MaxUint32 {
		return 0, fmt.Errorf("%s is outside uint32 range: %d", field, value)
	}
	return uint32(value), nil
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

func ptr[T any](value T) *T {
	return &value
}
