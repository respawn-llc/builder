package onboarding

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"core/server/auth"
	"core/server/llm"
	"core/server/onboardingimports"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/theme"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

type Finalizer struct {
	persistenceRoot string
	workspaceRoot   string
	settingsPath    string
	homeDir         string
}

type Options struct {
	PersistenceRoot string
	WorkspaceRoot   string
	SettingsPath    string
	HomeDir         string
}

func NewFinalizer(options Options) (*Finalizer, error) {
	root := strings.TrimSpace(options.PersistenceRoot)
	if root == "" {
		return nil, errors.New("persistence root is required")
	}
	settingsPath := strings.TrimSpace(options.SettingsPath)
	if settingsPath == "" {
		var err error
		settingsPath, err = config.ResolveSettingsFilePathInRoot(root)
		if err != nil {
			return nil, err
		}
	}
	homeDir := strings.TrimSpace(options.HomeDir)
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir: %w", err)
		}
	}
	return &Finalizer{
		persistenceRoot: root,
		workspaceRoot:   strings.TrimSpace(options.WorkspaceRoot),
		settingsPath:    settingsPath,
		homeDir:         homeDir,
	}, nil
}

func (f *Finalizer) FinalizeOnboarding(ctx context.Context, req serverapi.OnboardingFinalizeRequest) (serverapi.OnboardingFinalizeResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	settingsPath := strings.TrimSpace(f.settingsPath)
	if exists, err := pathExists(settingsPath); err != nil {
		return serverapi.OnboardingFinalizeResponse{}, configWriteFailed(settingsPath, "validate", err)
	} else if exists {
		return serverapi.OnboardingFinalizeResponse{}, configAlreadyExists(settingsPath)
	}
	release, err := globalRootLocks.acquire(ctx, f.persistenceRoot)
	if err != nil {
		return serverapi.OnboardingFinalizeResponse{}, serverapi.NewOnboardingCanceledError(serverapi.OnboardingCancelWaitingForLock)
	}
	defer release()
	if exists, err := pathExists(settingsPath); err != nil {
		return serverapi.OnboardingFinalizeResponse{}, configWriteFailed(settingsPath, "validate", err)
	} else if exists {
		return serverapi.OnboardingFinalizeResponse{}, configAlreadyExists(settingsPath)
	}
	if err := ctx.Err(); err != nil {
		return serverapi.OnboardingFinalizeResponse{}, serverapi.NewOnboardingCanceledError(serverapi.OnboardingCancelValidating)
	}
	if err := serverapi.ValidateOnboardingFinalizeRequest(req); err != nil {
		return serverapi.OnboardingFinalizeResponse{}, err
	}
	settings, preserved, err := projectSettings(req)
	if err != nil {
		return serverapi.OnboardingFinalizeResponse{}, err
	}
	if _, err := config.RenderSettingsTOMLForOnboarding(settings, config.OnboardingWriteOptions{PreservedDefaults: preserved}); err != nil {
		return serverapi.OnboardingFinalizeResponse{}, invalidRequest("settings", "invalid")
	}
	ledger := &mutationLedger{}
	if err := f.executeImports(ctx, req, ledger); err != nil {
		return serverapi.OnboardingFinalizeResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		if rollbackErr := ledger.rollback(); rollbackErr != nil {
			return serverapi.OnboardingFinalizeResponse{}, rollbackFailed(serverapi.NewOnboardingCanceledError(serverapi.OnboardingCancelImporting), rollbackErr)
		}
		return serverapi.OnboardingFinalizeResponse{}, serverapi.NewOnboardingCanceledError(serverapi.OnboardingCancelImporting)
	}
	path, err := config.WriteSettingsFileForOnboardingWithOptionsAt(settingsPath, settings, config.OnboardingWriteOptions{PreservedDefaults: preserved})
	if err != nil {
		if rollbackErr := ledger.rollback(); rollbackErr != nil {
			return serverapi.OnboardingFinalizeResponse{}, rollbackFailed(configWriteFailed(settingsPath, "write", err), rollbackErr)
		}
		if config.IsSettingsFileAlreadyExists(err) {
			return serverapi.OnboardingFinalizeResponse{}, configAlreadyExists(settingsPath)
		}
		return serverapi.OnboardingFinalizeResponse{}, configWriteFailed(settingsPath, "write", err)
	}
	return serverapi.OnboardingFinalizeResponse{Completed: true, SettingsPath: path}, nil
}

func projectSettings(req serverapi.OnboardingFinalizeRequest) (config.Settings, map[string]bool, error) {
	settings := config.DefaultOnboardingSettings()
	preserved := map[string]bool{}
	effectiveModel := settings.Model
	if req.Model != nil {
		model, err := modelChoiceValue(*req.Model)
		if err != nil {
			return config.Settings{}, nil, err
		}
		if model != settings.Model {
			settings.Model = model
			effectiveModel = model
			llm.ApplyDerivedModelContextBudget(&settings, model, settings.ModelContextWindow, settings.ContextCompactionThresholdTokens)
		}
	}
	if req.Theme != nil {
		settings.Theme = theme.Normalize(string(*req.Theme))
	}
	if req.ContextWindow != nil {
		if err := applyContextWindow(&settings, effectiveModel, *req.ContextWindow); err != nil {
			return config.Settings{}, nil, err
		}
	}
	if req.Thinking != nil {
		value, err := thinkingChoiceValue(*req.Thinking, effectiveModel, "thinking")
		if err != nil {
			return config.Settings{}, nil, err
		}
		settings.ThinkingLevel = value
		if req.Thinking.Kind == serverapi.OnboardingThinkingDisabled {
			preserved["thinking_level"] = true
		}
	}
	if req.Verbosity != nil {
		if !supportsVerbosity(settings, effectiveModel) {
			return config.Settings{}, nil, invalidRequest("verbosity", "unsupported_for_model")
		}
		settings.ModelVerbosity = config.ModelVerbosity(*req.Verbosity)
	}
	if req.AskQuestion != nil {
		if settings.EnabledTools == nil {
			settings.EnabledTools = map[toolspec.ID]bool{}
		}
		settings.EnabledTools[toolspec.ToolAskQuestion] = *req.AskQuestion
	}
	if req.Supervisor != nil {
		if err := applySupervisor(&settings, preserved, *req.Supervisor); err != nil {
			return config.Settings{}, nil, err
		}
	}
	if req.Compaction != nil {
		if *req.Compaction == serverapi.OnboardingCompactionNative && !supportsNativeCompaction(settings, effectiveModel) {
			return config.Settings{}, nil, invalidRequest("compaction", "unsupported_for_provider")
		}
		settings.CompactionMode = config.CompactionMode(*req.Compaction)
	}
	if len(req.DisabledSkillNames) > 0 {
		settings.SkillToggles = map[string]bool{}
		for index, name := range req.DisabledSkillNames {
			normalized := config.NormalizeSkillName(name)
			if normalized == "" {
				return config.Settings{}, nil, invalidRequest(fmt.Sprintf("disabled_skill_names.%d", index), "required")
			}
			if _, exists := settings.SkillToggles[normalized]; exists {
				return config.Settings{}, nil, invalidRequest(fmt.Sprintf("disabled_skill_names.%d", index), "duplicate")
			}
			settings.SkillToggles[normalized] = false
		}
	}
	if len(preserved) == 0 {
		preserved = nil
	}
	return settings, preserved, nil
}

func modelChoiceValue(choice serverapi.OnboardingModelChoice) (string, error) {
	switch choice.Kind {
	case serverapi.OnboardingModelKnown:
		model := strings.TrimSpace(choice.ModelID)
		if _, ok := llm.LookupModelCapabilityContract(model); !ok {
			return "", invalidRequest("model.model_id", "unknown_model")
		}
		return model, nil
	case serverapi.OnboardingModelCustom:
		return strings.TrimSpace(choice.Alias), nil
	default:
		return "", invalidRequest("model.kind", "unsupported_value")
	}
}

func applyContextWindow(settings *config.Settings, model string, choice serverapi.OnboardingContextWindowChoice) error {
	switch choice.Kind {
	case serverapi.OnboardingContextWindowDefault:
		return nil
	case serverapi.OnboardingContextWindowLarge:
		meta, ok := llm.LookupModelMetadata(model)
		if !ok || meta.LargeContextWindowTokens <= 0 {
			return invalidRequest("context_window.kind", "unsupported_for_model")
		}
		settings.ModelContextWindow = meta.LargeContextWindowTokens
	case serverapi.OnboardingContextWindowCustom:
		settings.ModelContextWindow = choice.Tokens
	default:
		return invalidRequest("context_window.kind", "unsupported_value")
	}
	settings.ContextCompactionThresholdTokens = settings.ModelContextWindow * 95 / 100
	settings.PreSubmitCompactionLeadTokens = clampedPreSubmitRunway(settings.ContextCompactionThresholdTokens, settings.ModelContextWindow, settings.PreSubmitCompactionLeadTokens)
	return nil
}

func clampedPreSubmitRunway(thresholdTokens int, windowTokens int, configuredRunway int) int {
	maxRunway := thresholdTokens - config.MinimumThresholdTokens(windowTokens)
	if maxRunway < 1 {
		return 1
	}
	if configuredRunway > maxRunway {
		return maxRunway
	}
	return configuredRunway
}

func thinkingChoiceValue(choice serverapi.OnboardingThinkingChoice, model, field string) (string, error) {
	switch choice.Kind {
	case serverapi.OnboardingThinkingDefault:
		return config.DefaultOnboardingSettings().ThinkingLevel, nil
	case serverapi.OnboardingThinkingDisabled:
		return "", nil
	case serverapi.OnboardingThinkingLevel:
		level := strings.TrimSpace(choice.Level)
		if !contains(llm.SupportedThinkingLevelsModel(model), level) {
			return "", invalidRequest(field+".level", "unsupported_for_model")
		}
		return level, nil
	case serverapi.OnboardingThinkingCustom:
		return strings.TrimSpace(choice.Value), nil
	default:
		return "", invalidRequest(field+".kind", "unsupported_value")
	}
}

func applySupervisor(settings *config.Settings, preserved map[string]bool, choice serverapi.OnboardingSupervisorChoice) error {
	settings.Reviewer.Frequency = string(choice.Frequency)
	if choice.Model != nil {
		model, err := modelChoiceValue(*choice.Model)
		if err != nil {
			return err
		}
		if model != settings.Model {
			settings.Reviewer.Model = model
			preserved["reviewer.model"] = true
		}
	}
	reviewerModel := settings.Reviewer.Model
	if strings.TrimSpace(reviewerModel) == "" {
		reviewerModel = settings.Model
	}
	if choice.Thinking != nil {
		thinking, err := thinkingChoiceValue(*choice.Thinking, reviewerModel, "supervisor.thinking")
		if err != nil {
			return err
		}
		if choice.Thinking.Kind == serverapi.OnboardingThinkingDisabled || thinking != settings.ThinkingLevel {
			settings.Reviewer.ThinkingLevel = thinking
			preserved["reviewer.thinking_level"] = true
		}
	}
	return nil
}

func supportsNativeCompaction(settings config.Settings, model string) bool {
	if caps, ok := llm.ProviderCapabilitiesFromOverride(settings.ProviderCapabilities); ok {
		return caps.SupportsResponsesCompact
	}
	providerID := strings.TrimSpace(settings.ProviderOverride)
	if providerID == "" {
		if strings.TrimSpace(settings.OpenAIBaseURL) != "" {
			if llm.IsOpenAIFirstPartyBaseURL(settings.OpenAIBaseURL) {
				providerID = "openai"
			} else {
				providerID = "openai-compatible"
			}
		} else if provider, err := llm.InferProviderFromModel(model); err == nil {
			providerID = string(provider)
		} else {
			return false
		}
	}
	caps, err := llm.InferProviderCapabilities(providerID)
	return err == nil && caps.SupportsResponsesCompact
}

func supportsVerbosity(settings config.Settings, model string) bool {
	caps, err := llm.ResolveRuntimeProviderCapabilities(auth.EmptyState(), settings)
	if err != nil {
		return llm.SupportsVerbosityModel(model)
	}
	return llm.VerbositySupportForModelAndProvider(model, caps).Supported
}

func (f *Finalizer) executeImports(ctx context.Context, req serverapi.OnboardingFinalizeRequest, ledger *mutationLedger) error {
	skillsRequested := importRequested(req.SkillsImport)
	commandsRequested := importRequested(req.CommandsImport)
	if !skillsRequested && !commandsRequested {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return serverapi.NewOnboardingCanceledError(serverapi.OnboardingCancelDiscoveringImports)
	}
	if skillsRequested {
		target := filepath.Join(f.persistenceRoot, "skills")
		if blocked, err := targetUnavailableForImport(target); err != nil {
			return serverapi.NewOnboardingFinalizeError(serverapi.OnboardingFinalizeImportFailed, serverapi.OnboardingImportFailedDetails{ImportKind: serverapi.OnboardingImportKindSkills, Operation: serverapi.OnboardingImportOperationPrepareTarget, Cause: err.Error()}, err)
		} else if blocked {
			return importUnavailable(req.SkillsImport, serverapi.OnboardingImportKindSkills, serverapi.OnboardingImportReasonTargetExists)
		}
	}
	if commandsRequested {
		for _, target := range []string{filepath.Join(f.persistenceRoot, "prompts"), filepath.Join(f.persistenceRoot, "commands")} {
			if blocked, err := targetUnavailableForImport(target); err != nil {
				return serverapi.NewOnboardingFinalizeError(serverapi.OnboardingFinalizeImportFailed, serverapi.OnboardingImportFailedDetails{ImportKind: serverapi.OnboardingImportKindCommands, Operation: serverapi.OnboardingImportOperationPrepareTarget, Cause: err.Error()}, err)
			} else if blocked {
				return importUnavailable(req.CommandsImport, serverapi.OnboardingImportKindCommands, serverapi.OnboardingImportReasonTargetExists)
			}
		}
	}
	workspaceRoot := strings.TrimSpace(f.workspaceRoot)
	var workspaceRootPtr *string
	if workspaceRoot != "" {
		workspaceRootPtr = &workspaceRoot
	}
	discovery, err := onboardingimports.Discover(onboardingimports.Options{ConfigRoot: f.persistenceRoot, WorkspaceRoot: workspaceRootPtr, HomeDir: f.homeDir})
	if err != nil {
		return serverapi.NewOnboardingFinalizeError(serverapi.OnboardingFinalizeImportFailed, serverapi.OnboardingImportFailedDetails{Operation: serverapi.OnboardingImportOperationDiscover, Cause: err.Error()}, err)
	}
	if err := executeSelection(ctx, ledger, discovery.Skills.Choices, discovery.Errors, req.SkillsImport, filepath.Join(f.persistenceRoot, "skills"), serverapi.OnboardingImportKindSkills); err != nil {
		return rollbackAfterImportError(err, ledger)
	}
	if err := executeSelection(ctx, ledger, discovery.Commands.Choices, discovery.Errors, req.CommandsImport, filepath.Join(f.persistenceRoot, "prompts"), serverapi.OnboardingImportKindCommands); err != nil {
		return rollbackAfterImportError(err, ledger)
	}
	return nil
}

func rollbackAfterImportError(primary error, ledger *mutationLedger) error {
	if ledger == nil || len(ledger.ops) == 0 {
		return primary
	}
	if rollbackErr := ledger.rollback(); rollbackErr != nil {
		return rollbackFailed(primary, rollbackErr)
	}
	return primary
}

func importRequested(selection *serverapi.OnboardingImportSelection) bool {
	return selection != nil && selection.Mode == serverapi.OnboardingImportModeSymlinkSource
}

func executeSelection(ctx context.Context, ledger *mutationLedger, choices []onboardingimports.Choice, discoveryErrors []onboardingimports.Error, selection *serverapi.OnboardingImportSelection, target string, kind serverapi.OnboardingImportKind) error {
	if selection == nil || selection.Mode == "" || selection.Mode == serverapi.OnboardingImportModeNone {
		return nil
	}
	if selection.Mode != serverapi.OnboardingImportModeSymlinkSource {
		return invalidRequest(string(kind)+"_import.mode", "unsupported_value")
	}
	choice, err := selectedImportChoice(choices, discoveryErrors, selection, kind)
	if err != nil {
		return err
	}
	sourceRoot := strings.TrimSpace(derefString(choice.Ref.SourceRoot))
	if sourceRoot == "" {
		return importUnavailable(selection, kind, serverapi.OnboardingImportReasonNotDiscovered)
	}
	if err := ctx.Err(); err != nil {
		return serverapi.NewOnboardingCanceledError(serverapi.OnboardingCancelImporting)
	}
	if err := executeSymlink(ledger, target, sourceRoot); err != nil {
		return serverapi.NewOnboardingFinalizeError(serverapi.OnboardingFinalizeImportFailed, serverapi.OnboardingImportFailedDetails{
			ImportKind: kind, ProviderUUID: selection.ProviderUUID, ImportProviderID: selection.ImportProviderID, SourceRootPath: selection.SourceRootPath, Operation: serverapi.OnboardingImportOperationCreateSymlink, Cause: err.Error(),
		}, err)
	}
	return nil
}

func selectedImportChoice(choices []onboardingimports.Choice, discoveryErrors []onboardingimports.Error, selection *serverapi.OnboardingImportSelection, kind serverapi.OnboardingImportKind) (onboardingimports.Choice, error) {
	if selection.ProviderUUID != nil {
		if selection.ProviderUUID.Version() != 4 {
			return onboardingimports.Choice{}, invalidRequest(string(kind)+"_import.provider_uuid", "uuid_v4_required")
		}
		providerID, ok := importProviderIDForUUID(*selection.ProviderUUID)
		if !ok {
			return onboardingimports.Choice{}, importUnavailable(selection, kind, serverapi.OnboardingImportReasonNotDiscovered)
		}
		for _, choice := range choices {
			if symlinkChoiceForProvider(choice, providerID) {
				return choice, nil
			}
		}
		if err := providerDiscoveryError(discoveryErrors, providerID, kind); err != nil {
			return onboardingimports.Choice{}, importDiscoveryFailed(selection, kind, err)
		}
		return onboardingimports.Choice{}, importUnavailable(selection, kind, serverapi.OnboardingImportReasonNotDiscovered)
	}
	providerID := onboardingimports.ProviderID(strings.TrimSpace(derefString(selection.ImportProviderID)))
	root := filepath.Clean(strings.TrimSpace(derefString(selection.SourceRootPath)))
	for _, choice := range choices {
		if choiceMatchesRef(choice, providerID, root) {
			return choice, nil
		}
	}
	if err := rootDiscoveryError(discoveryErrors, providerID, root, kind); err != nil {
		return onboardingimports.Choice{}, importDiscoveryFailed(selection, kind, err)
	}
	return onboardingimports.Choice{}, importUnavailable(selection, kind, serverapi.OnboardingImportReasonNotDiscovered)
}

func importProviderIDForUUID(providerUUID uuid.UUID) (onboardingimports.ProviderID, bool) {
	for _, provider := range ProductionProviderCatalog() {
		if provider.UUID == providerUUID {
			return provider.ImportProviderID, true
		}
	}
	return "", false
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func symlinkChoiceForProvider(choice onboardingimports.Choice, providerID onboardingimports.ProviderID) bool {
	return choice.Ref.Mode == onboardingimports.ChoiceModeSymlinkSource &&
		choice.Ref.ProviderID != nil &&
		*choice.Ref.ProviderID == providerID &&
		choice.Ref.SourceRoot != nil
}

func choiceMatchesRef(choice onboardingimports.Choice, providerID onboardingimports.ProviderID, root string) bool {
	return symlinkChoiceForProvider(choice, providerID) && filepath.Clean(derefString(choice.Ref.SourceRoot)) == root
}

func rootDiscoveryError(discoveryErrors []onboardingimports.Error, providerID onboardingimports.ProviderID, root string, kind serverapi.OnboardingImportKind) error {
	for _, discoveryErr := range sortedDiscoveryErrors(discoveryErrors) {
		if discoveryErr.ProviderID == nil || *discoveryErr.ProviderID != providerID || discoveryErr.Path == nil || filepath.Clean(*discoveryErr.Path) != root || !discoveryErrorMatchesKind(discoveryErr, kind) {
			continue
		}
		return discoveryError(discoveryErr)
	}
	return nil
}

func providerDiscoveryError(discoveryErrors []onboardingimports.Error, providerID onboardingimports.ProviderID, kind serverapi.OnboardingImportKind) error {
	for _, discoveryErr := range sortedDiscoveryErrors(discoveryErrors) {
		if discoveryErr.ProviderID == nil || *discoveryErr.ProviderID != providerID || !discoveryErrorMatchesKind(discoveryErr, kind) {
			continue
		}
		return discoveryError(discoveryErr)
	}
	return nil
}

func sortedDiscoveryErrors(discoveryErrors []onboardingimports.Error) []onboardingimports.Error {
	out := append([]onboardingimports.Error(nil), discoveryErrors...)
	sort.Slice(out, func(i, j int) bool {
		leftPath, rightPath := derefString(out[i].Path), derefString(out[j].Path)
		if leftPath != rightPath {
			return leftPath < rightPath
		}
		return out[i].Operation < out[j].Operation
	})
	return out
}

func discoveryErrorMatchesKind(discoveryErr onboardingimports.Error, kind serverapi.OnboardingImportKind) bool {
	switch kind {
	case serverapi.OnboardingImportKindSkills:
		return discoveryErr.Operation == "inspect_skill_root" || discoveryErr.Operation == "discover_skills"
	case serverapi.OnboardingImportKindCommands:
		return discoveryErr.Operation == "inspect_command_root" || discoveryErr.Operation == "discover_commands"
	default:
		return false
	}
}

func discoveryError(discoveryErr onboardingimports.Error) error {
	if strings.TrimSpace(discoveryErr.Operation) == "" {
		return errors.New(discoveryErr.Message)
	}
	return fmt.Errorf("%s: %s", discoveryErr.Operation, discoveryErr.Message)
}

func importDiscoveryFailed(selection *serverapi.OnboardingImportSelection, kind serverapi.OnboardingImportKind, err error) error {
	return serverapi.NewOnboardingFinalizeError(serverapi.OnboardingFinalizeImportFailed, serverapi.OnboardingImportFailedDetails{
		ImportKind: kind, ProviderUUID: selection.ProviderUUID, ImportProviderID: selection.ImportProviderID, SourceRootPath: selection.SourceRootPath, Operation: serverapi.OnboardingImportOperationDiscover, Cause: err.Error(),
	}, err)
}

func importUnavailable(selection *serverapi.OnboardingImportSelection, kind serverapi.OnboardingImportKind, reason serverapi.OnboardingImportUnavailableReason) error {
	details := serverapi.OnboardingImportUnavailableDetails{ImportKind: kind, Mode: serverapi.OnboardingImportModeSymlinkSource, ReasonCode: reason}
	if selection != nil {
		details.Mode = selection.Mode
		details.ProviderUUID = selection.ProviderUUID
		details.ImportProviderID = selection.ImportProviderID
		details.SourceRootPath = selection.SourceRootPath
	}
	return serverapi.NewOnboardingFinalizeError(serverapi.OnboardingFinalizeImportUnavailable, details, nil)
}

func invalidRequest(field, code string) error {
	return serverapi.NewOnboardingFinalizeError(serverapi.OnboardingFinalizeInvalidRequest, serverapi.OnboardingInvalidRequestDetails{FieldErrors: []serverapi.OnboardingFinalizeFieldError{{Field: field, Code: code}}}, nil)
}

func configAlreadyExists(path string) error {
	return serverapi.NewOnboardingFinalizeError(serverapi.OnboardingFinalizeConfigAlreadyExists, serverapi.OnboardingConfigAlreadyExistsDetails{SettingsPath: path}, nil)
}

func configWriteFailed(path, op string, cause error) error {
	return serverapi.NewOnboardingFinalizeError(serverapi.OnboardingFinalizeConfigWriteFailed, serverapi.OnboardingConfigWriteFailedDetails{SettingsPath: path, Operation: op, Cause: cause.Error()}, cause)
}

func rollbackFailed(primary error, rollback error) error {
	primaryDetail := any(map[string]string{"cause": primary.Error()})
	var finalizeErr *serverapi.OnboardingFinalizeError
	if errors.As(primary, &finalizeErr) {
		primaryDetail = serverapi.OnboardingFinalizeErrorEnvelope{Type: "onboarding_finalize_error", Code: finalizeErr.Code, Details: finalizeErr.Details}
	}
	rollbackDetail := map[string]string{"operation": "rollback", "cause": rollback.Error()}
	var rollbackErr rollbackFailure
	if errors.As(rollback, &rollbackErr) {
		rollbackDetail["operation"] = rollbackErr.operation
		rollbackDetail["cause"] = rollbackErr.cause.Error()
	}
	return serverapi.NewOnboardingFinalizeError(serverapi.OnboardingFinalizeRollbackFailed, serverapi.OnboardingRollbackFailedDetails{Primary: primaryDetail, Rollback: rollbackDetail}, errors.Join(primary, rollback))
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func pathExists(path string) (bool, error) {
	if _, err := os.Lstat(path); err == nil {
		return true, nil
	} else if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else {
		return false, err
	}
}

type rootLocks struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

var globalRootLocks = &rootLocks{locks: map[string]chan struct{}{}}

func (l *rootLocks) acquire(ctx context.Context, root string) (func(), error) {
	l.mu.Lock()
	ch := l.locks[root]
	if ch == nil {
		ch = make(chan struct{}, 1)
		ch <- struct{}{}
		l.locks[root] = ch
	}
	l.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ch:
		return func() { ch <- struct{}{} }, nil
	}
}

type mutationLedger struct {
	ops []rollbackOp
}

type rollbackOp struct {
	operation string
	op        func() error
}

type rollbackFailure struct {
	operation string
	cause     error
}

func (e rollbackFailure) Error() string {
	return e.operation + ": " + e.cause.Error()
}

func (e rollbackFailure) Unwrap() error {
	return e.cause
}

func (l *mutationLedger) add(operation string, op func() error) {
	l.ops = append(l.ops, rollbackOp{operation: operation, op: op})
}

func (l *mutationLedger) rollback() error {
	var out error
	for i := len(l.ops) - 1; i >= 0; i-- {
		if err := l.ops[i].op(); err != nil {
			out = errors.Join(out, rollbackFailure{operation: l.ops[i].operation, cause: err})
		}
	}
	return out
}

func executeSymlink(ledger *mutationLedger, target, source string) error {
	if info, err := os.Stat(source); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("source is not directory")
	}
	targetState, err := inspectImportTarget(target)
	if err != nil {
		return err
	}
	if targetState.unavailable {
		return fmt.Errorf("target exists")
	}
	if targetState.emptyDirectory {
		if err := os.Remove(target); err != nil {
			return err
		}
		ledger.add("restore_empty_dir", func() error { return os.Mkdir(target, 0o755) })
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.Symlink(source, target); err != nil {
		return err
	}
	ledger.add("remove_created_path", func() error {
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
	return nil
}

func targetUnavailableForImport(path string) (bool, error) {
	targetState, err := inspectImportTarget(path)
	if err != nil {
		return false, err
	}
	return targetState.unavailable, nil
}

type importTargetState struct {
	unavailable    bool
	emptyDirectory bool
}

func inspectImportTarget(path string) (importTargetState, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return importTargetState{}, nil
	}
	if err != nil {
		return importTargetState{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return importTargetState{unavailable: true}, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return importTargetState{}, err
	}
	if len(entries) > 0 {
		return importTargetState{unavailable: true}, nil
	}
	return importTargetState{emptyDirectory: true}, nil
}
