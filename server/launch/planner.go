package launch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"core/server/auth"
	"core/server/llm"
	"core/server/metadata"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type Mode string

const (
	ModeInteractive Mode = "interactive"
	ModeHeadless    Mode = "headless"

	SubagentSessionSuffix = "subagent"
)

// MetadataExecutionTargetStore is the metadata subset needed to copy a parent
// session execution target into a newly created child session.
type MetadataExecutionTargetStore interface {
	ResolveSessionExecutionTarget(ctx context.Context, sessionID string) (clientui.SessionExecutionTarget, error)
	UpdateSessionExecutionTarget(ctx context.Context, update metadata.SessionExecutionTargetUpdate) error
	DeleteSessionRecordByID(ctx context.Context, sessionID string) error
	Close() error
}

// MetadataExecutionTargetStoreOpener opens metadata storage for launch planning.
type MetadataExecutionTargetStoreOpener func(persistenceRoot string) (MetadataExecutionTargetStore, error)

type Planner struct {
	Config              config.App
	ContainerDir        string
	StoreOptions        []session.StoreOption
	ReloadConfig        func() (config.App, error)
	MetadataStoreOpener MetadataExecutionTargetStoreOpener
}

type SessionRequest struct {
	Mode                                Mode
	SelectedSessionID                   string
	ForceNewSession                     bool
	ParentSessionID                     string
	SkipContinuationAgentRoleValidation bool
}

type SessionPlan struct {
	Store                               *session.Store
	ActiveSettings                      config.Settings
	BaseSettings                        config.Settings
	EnabledTools                        []toolspec.ID
	ConfiguredModelName                 string
	SessionName                         string
	PromptHistory                       []string
	ModelContractLocked                 bool
	SkipContinuationAgentRoleValidation bool
	WorkspaceRoot                       string
	Source                              config.SourceReport
	BaseSource                          config.SourceReport
}

type RunPromptOverrideOptions struct {
	AllowLockedAgentRoleChange bool
}

type PromptFacingSnapshotResolution struct {
	Settings      config.Settings
	Source        config.SourceReport
	ActiveToolIDs []toolspec.ID
	WebSearchMode string
}

func ResolvePromptFacingSnapshotConfig(app config.App, store *session.Store, skipContinuationAgentRoleValidation bool) (PromptFacingSnapshotResolution, error) {
	plan, err := ResolvePromptFacingSnapshotPlan(app, store, skipContinuationAgentRoleValidation)
	if err != nil {
		return PromptFacingSnapshotResolution{}, err
	}
	return PromptFacingSnapshotResolution{
		Settings:      plan.ActiveSettings,
		Source:        plan.Source,
		ActiveToolIDs: plan.EnabledTools,
		WebSearchMode: strings.TrimSpace(plan.ActiveSettings.WebSearch),
	}, nil
}

// ResolvePromptFacingSnapshotPlan reconstructs the request-facing session plan
// from a persisted store without creating or selecting a session. It is shared
// by diagnostic paths that need the same settings, source, tools, and
// locked-contract semantics as launch planning.
func ResolvePromptFacingSnapshotPlan(app config.App, store *session.Store, skipContinuationAgentRoleValidation bool) (SessionPlan, error) {
	if store == nil {
		return SessionPlan{}, errors.New("session store is required")
	}
	meta := store.Meta()
	baseActive := EffectiveSettings(app.Settings, meta.Locked)
	baseSource := app.Source
	active, source := baseActive, baseSource
	if meta.Continuation != nil {
		role := strings.TrimSpace(meta.Continuation.AgentRole)
		var err error
		active, source, err = applyPersistedSubagentRoleSettings(baseActive, baseSource, role, meta.Locked == nil, !skipContinuationAgentRoleValidation)
		if err != nil {
			return SessionPlan{}, err
		}
		if shouldApplyPersistedContinuationBaseURL(baseActive, role) {
			if baseURL := strings.TrimSpace(meta.Continuation.OpenAIBaseURL); baseURL != "" {
				active.OpenAIBaseURL = baseURL
			}
		}
	}
	enabledTools, err := ActiveToolIDsForPlan(active, source, meta.Locked)
	if err != nil {
		return SessionPlan{}, err
	}
	if meta.Locked != nil && (!meta.Locked.HasEnabledTools || strings.TrimSpace(meta.Locked.WebSearchMode) == "") {
		backfill, backfillErr := store.BackfillLockedRequestShape(session.LockedRequestShapeBackfill{
			EnabledTools:    toolspec.IDStrings(enabledTools),
			HasEnabledTools: true,
			WebSearchMode:   strings.TrimSpace(active.WebSearch),
		})
		if backfillErr != nil && !backfill.Committed {
			return SessionPlan{}, backfillErr
		}
		if backfill.Committed && backfill.Locked != nil {
			meta.Locked = backfill.Locked
		}
	}
	configuredModelName := app.Settings.Model
	if meta.Locked == nil {
		configuredModelName = active.Model
	}
	return SessionPlan{
		Store:                               store,
		ActiveSettings:                      active,
		BaseSettings:                        baseActive,
		EnabledTools:                        enabledTools,
		ConfiguredModelName:                 configuredModelName,
		SessionName:                         meta.Name,
		ModelContractLocked:                 meta.Locked != nil,
		SkipContinuationAgentRoleValidation: skipContinuationAgentRoleValidation,
		WorkspaceRoot:                       app.WorkspaceRoot,
		Source:                              source,
		BaseSource:                          baseSource,
	}, nil
}

func (p Planner) PlanSession(ctx context.Context, req SessionRequest) (SessionPlan, error) {
	if p.ReloadConfig != nil {
		cfg, err := p.ReloadConfig()
		if err != nil {
			return SessionPlan{}, err
		}
		p.Config = cfg
	}
	store, err := p.openStore(ctx, req)
	if err != nil {
		return SessionPlan{}, err
	}
	if req.Mode == ModeHeadless {
		if err := EnsureSubagentSessionName(store); err != nil {
			return SessionPlan{}, err
		}
	}
	meta := store.Meta()
	baseActive := EffectiveSettings(p.Config.Settings, meta.Locked)
	baseSource := p.Config.Source
	continuationAgentRole := ""
	continuationBaseURL := ""
	if meta.Continuation != nil {
		continuationAgentRole = strings.TrimSpace(meta.Continuation.AgentRole)
		continuationBaseURL = strings.TrimSpace(meta.Continuation.OpenAIBaseURL)
	}
	active, source := baseActive, baseSource
	if meta.Continuation != nil {
		active, source, err = applyPersistedSubagentRoleSettings(baseActive, baseSource, continuationAgentRole, meta.Locked == nil, !req.SkipContinuationAgentRoleValidation)
		if err != nil {
			return SessionPlan{}, err
		}
		if shouldApplyPersistedContinuationBaseURL(baseActive, continuationAgentRole) && continuationBaseURL != "" {
			active.OpenAIBaseURL = continuationBaseURL
		}
	}
	continuation := session.ContinuationContext{OpenAIBaseURL: active.OpenAIBaseURL}
	if meta.Continuation != nil {
		continuation.AgentRole = continuationAgentRole
	}
	if err := store.SetContinuationContext(continuation); err != nil {
		return SessionPlan{}, err
	}
	enabledTools, err := ActiveToolIDsForPlan(active, source, meta.Locked)
	if err != nil {
		return SessionPlan{}, err
	}
	if meta.Locked != nil && (!meta.Locked.HasEnabledTools || strings.TrimSpace(meta.Locked.WebSearchMode) == "") {
		backfill, backfillErr := store.BackfillLockedRequestShape(session.LockedRequestShapeBackfill{
			EnabledTools:    toolspec.IDStrings(enabledTools),
			HasEnabledTools: true,
			WebSearchMode:   strings.TrimSpace(active.WebSearch),
		})
		if backfillErr != nil && !backfill.Committed {
			return SessionPlan{}, backfillErr
		}
		if backfill.Committed && backfill.Locked != nil {
			meta.Locked = backfill.Locked
		}
	}
	configuredModelName := p.Config.Settings.Model
	if meta.Locked == nil {
		configuredModelName = active.Model
	}
	return SessionPlan{
		Store:                               store,
		ActiveSettings:                      active,
		BaseSettings:                        baseActive,
		EnabledTools:                        enabledTools,
		ConfiguredModelName:                 configuredModelName,
		SessionName:                         meta.Name,
		ModelContractLocked:                 meta.Locked != nil,
		SkipContinuationAgentRoleValidation: req.SkipContinuationAgentRoleValidation,
		WorkspaceRoot:                       p.Config.WorkspaceRoot,
		Source:                              source,
		BaseSource:                          baseSource,
	}, nil
}

func applyPersistedSubagentRoleSettings(base config.Settings, source config.SourceReport, roleName string, allowModelOverride bool, validate bool) (config.Settings, config.SourceReport, error) {
	lookup := config.LookupSubagentRole(base, roleName)
	if lookup.Status == config.SubagentRoleLookupInvalid {
		return base, source, nil
	}
	if lookup.Status == config.SubagentRoleLookupMissing {
		return base, source, nil
	}
	providerSettings := cloneSettings(base)
	applySubagentProviderOverrides(&providerSettings, lookup.Role)
	resolved, effectiveSource, _, err := resolveSubagentSettingsWithProviderID(base, source, *lookup.NormalizedSelector, persistedRoleProviderID(providerSettings), allowModelOverride, validate)
	if err != nil {
		return config.Settings{}, config.SourceReport{}, err
	}
	return resolved, effectiveSource, nil
}

func shouldApplyPersistedContinuationBaseURL(base config.Settings, roleName string) bool {
	lookup := config.LookupSubagentRole(base, roleName)
	if lookup.Status == config.SubagentRoleLookupInvalid {
		return true
	}
	if lookup.Status == config.SubagentRoleLookupMissing {
		return false
	}
	_, hasRoleBaseURL := lookup.Role.Sources["openai_base_url"]
	return !hasRoleBaseURL
}

func persistedRoleProviderID(settings config.Settings) string {
	if providerID := strings.TrimSpace(settings.ProviderCapabilities.ProviderID); providerID != "" {
		return providerID
	}
	if providerOverride := strings.TrimSpace(settings.ProviderOverride); providerOverride != "" {
		return providerOverride
	}
	if baseURL := strings.TrimSpace(settings.OpenAIBaseURL); baseURL != "" {
		if llm.IsOpenAIFirstPartyBaseURL(baseURL) {
			return "openai"
		}
		return "openai-compatible"
	}
	provider, err := llm.InferProviderFromModel(settings.Model)
	if err != nil {
		return "openai"
	}
	return string(provider)
}

func ApplyRunPromptOverrides(plan SessionPlan, overrides serverapi.RunPromptOverrides, authState auth.State) (SessionPlan, []string, error) {
	return ApplyRunPromptOverridesWithOptions(plan, overrides, authState, RunPromptOverrideOptions{})
}

func ApplyRunPromptOverridesWithOptions(plan SessionPlan, overrides serverapi.RunPromptOverrides, authState auth.State, options RunPromptOverrideOptions) (SessionPlan, []string, error) {
	return applyRunPromptOverridesWithBudgetApplier(plan, overrides, authState, options, applyDerivedModelContextBudgetOverrides)
}

type modelContextBudgetApplier func(settings *config.Settings, explicitSources map[string]string, originalModel string, allowModelOverride bool)

func applyRunPromptOverridesWithBudgetApplier(plan SessionPlan, overrides serverapi.RunPromptOverrides, authState auth.State, options RunPromptOverrideOptions, applyBudget modelContextBudgetApplier) (SessionPlan, []string, error) {
	if !overrides.HasAny() {
		return plan, nil, nil
	}
	var warnings []string
	next := plan
	baseSettings := plan.BaseSettings
	if strings.TrimSpace(baseSettings.Model) == "" {
		baseSettings = plan.ActiveSettings
	}
	baseSource := plan.BaseSource
	if baseSource.Sources == nil {
		baseSource = plan.Source
	}
	shouldPersistContinuation := false
	continuationAgentRole := ""
	if plan.Store.Meta().Continuation != nil {
		continuationAgentRole = strings.TrimSpace(plan.Store.Meta().Continuation.AgentRole)
	}
	activeToolLock := plan.Store.Meta().Locked
	if options.AllowLockedAgentRoleChange {
		activeToolLock = nil
	}
	staleLockedPromptFacingContract := false
	persistContinuation := func() error {
		ctx := session.ContinuationContext{
			OpenAIBaseURL: next.ActiveSettings.OpenAIBaseURL,
			AgentRole:     continuationAgentRole,
		}
		if staleLockedPromptFacingContract {
			_, err := next.Store.SetContinuationContextAndMarkLockedPromptFacingContractStale(ctx)
			return err
		}
		return next.Store.SetContinuationContext(ctx)
	}
	roleOverride, err := overrides.AgentRoleOverride()
	if err != nil {
		return SessionPlan{}, nil, fmt.Errorf("%w: %v", errInvalidAgentRole, err)
	}
	if roleOverride.Present && plan.ModelContractLocked && continuationAgentRole != roleOverride.Role && !options.AllowLockedAgentRoleChange && !plan.SkipContinuationAgentRoleValidation {
		return SessionPlan{}, nil, fmt.Errorf("%w: current=%q requested=%q", ErrLockedAgentRoleChange, continuationAgentRole, roleOverride.Role)
	}
	if roleOverride.Present && plan.ModelContractLocked && continuationAgentRole != roleOverride.Role && options.AllowLockedAgentRoleChange {
		staleLockedPromptFacingContract = true
	}
	if roleOverride.Present {
		shouldPersistContinuation = true
		continuationAgentRole = roleOverride.Role
		next.ActiveSettings = cloneSettings(baseSettings)
		next.Source = baseSource
		if !plan.ModelContractLocked {
			next.ConfiguredModelName = next.ActiveSettings.Model
		}
		if roleOverride.Default {
			enabledTools, err := ActiveToolIDsForPlan(next.ActiveSettings, next.Source, activeToolLock)
			if err != nil {
				return SessionPlan{}, nil, err
			}
			next.EnabledTools = enabledTools
		}
	}
	if roleOverride.Role != "" {
		providerBase := cloneSettings(baseSettings)
		if value := strings.TrimSpace(overrides.ProviderOverride); value != "" {
			providerBase.ProviderOverride = value
		}
		if value := strings.TrimSpace(overrides.OpenAIBaseURL); value != "" {
			providerBase.OpenAIBaseURL = value
		}
		resolved, warning, err := resolveSubagentSettingsWithValidation(baseSettings, providerBase, baseSource.Sources, roleOverride.Role, authState, !plan.ModelContractLocked, !overrides.HasConfigOverrides())
		if err != nil {
			return SessionPlan{}, nil, err
		}
		next.ActiveSettings = resolved
		if !plan.ModelContractLocked {
			next.ConfiguredModelName = resolved.Model
		}
		roleSource := sourceReportWithSubagentRoleSources(baseSource, baseSettings, roleOverride.Role, !plan.ModelContractLocked)
		enabledTools, err := ActiveToolIDsForPlan(next.ActiveSettings, roleSource, activeToolLock)
		if err != nil {
			return SessionPlan{}, nil, err
		}
		next.EnabledTools = enabledTools
		next.Source = roleSource
		if strings.TrimSpace(warning) != "" {
			warnings = append(warnings, warning)
		}
	}
	if !overrides.HasConfigOverrides() {
		if shouldPersistContinuation {
			if err := persistContinuation(); err != nil {
				return SessionPlan{}, nil, err
			}
		}
		return next, warnings, nil
	}
	loaded, err := config.Load(plan.WorkspaceRoot, config.LoadOptions{
		Model:               strings.TrimSpace(overrides.Model),
		ProviderOverride:    strings.TrimSpace(overrides.ProviderOverride),
		ThinkingLevel:       strings.TrimSpace(overrides.ThinkingLevel),
		Theme:               strings.TrimSpace(overrides.Theme),
		ModelTimeoutSeconds: overrides.ModelTimeoutSeconds,
		Tools:               strings.TrimSpace(overrides.Tools),
		OpenAIBaseURL:       strings.TrimSpace(overrides.OpenAIBaseURL),
	})
	if err != nil {
		return SessionPlan{}, nil, err
	}
	locked := plan.Store.Meta().Locked
	mergedSource := mergeOverrideSources(next.Source, loaded.Source)
	if strings.TrimSpace(overrides.Model) != "" && !next.ModelContractLocked {
		originalModel := strings.TrimSpace(next.ActiveSettings.Model)
		explicitSources := map[string]string{}
		for key, source := range mergedSource.Sources {
			if strings.TrimSpace(source) == "" || strings.TrimSpace(source) == "default" {
				continue
			}
			explicitSources[key] = source
		}
		next.ActiveSettings.Model = loaded.Settings.Model
		applyBudget(&next.ActiveSettings, explicitSources, originalModel, true)
		next.ConfiguredModelName = loaded.Settings.Model
	}
	if strings.TrimSpace(overrides.ProviderOverride) != "" {
		next.ActiveSettings.ProviderOverride = loaded.Settings.ProviderOverride
	}
	if strings.TrimSpace(overrides.ThinkingLevel) != "" {
		next.ActiveSettings.ThinkingLevel = loaded.Settings.ThinkingLevel
	}
	if strings.TrimSpace(overrides.Theme) != "" {
		next.ActiveSettings.Theme = loaded.Settings.Theme
	}
	if overrides.ModelTimeoutSeconds > 0 {
		next.ActiveSettings.Timeouts.ModelRequestSeconds = loaded.Settings.Timeouts.ModelRequestSeconds
	}
	if strings.TrimSpace(overrides.OpenAIBaseURL) != "" {
		shouldPersistContinuation = true
		next.ActiveSettings.OpenAIBaseURL = loaded.Settings.OpenAIBaseURL
	}
	next.Source = mergedSource
	validated, err := validateRunPromptOverrideSettings(next.ActiveSettings, next.Source)
	if err != nil {
		return SessionPlan{}, nil, err
	}
	next.ActiveSettings = validated
	if locked == nil {
		if strings.TrimSpace(overrides.Tools) != "" {
			next.ActiveSettings.EnabledTools = cloneEnabledToolSet(loaded.Settings.EnabledTools)
		}
		if strings.TrimSpace(overrides.Tools) != "" || strings.TrimSpace(overrides.Model) != "" {
			enabledTools, err := ActiveToolIDsForPlan(next.ActiveSettings, mergedSource, locked)
			if err != nil {
				return SessionPlan{}, nil, err
			}
			next.EnabledTools = enabledTools
		}
	}
	if shouldPersistContinuation {
		if err := persistContinuation(); err != nil {
			return SessionPlan{}, nil, err
		}
	}
	return next, warnings, nil
}

func validateRunPromptOverrideSettings(settings config.Settings, source config.SourceReport) (config.Settings, error) {
	validated := cloneSettings(settings)
	sources := cloneStringMap(source.Sources)
	applyReviewerInheritance(&validated, sources)
	if err := config.ValidateSettingsWithSources(validated, sources); err != nil {
		return config.Settings{}, err
	}
	return validated, nil
}

func mergeOverrideSources(base config.SourceReport, override config.SourceReport) config.SourceReport {
	merged := base
	merged.SettingsPath = override.SettingsPath
	merged.SettingsFileExists = override.SettingsFileExists
	merged.CreatedDefaultConfig = override.CreatedDefaultConfig
	merged.Sources = make(map[string]string, len(base.Sources)+len(override.Sources))
	for key, value := range base.Sources {
		merged.Sources[key] = value
	}
	for key, value := range override.Sources {
		if strings.TrimSpace(value) == "cli" {
			merged.Sources[key] = value
		}
	}
	return merged
}

func sourceReportWithSubagentRoleSources(base config.SourceReport, settings config.Settings, roleName string, allowModelOverride bool) config.SourceReport {
	lookup := config.LookupSubagentRole(settings, roleName)
	if lookup.Status != config.SubagentRoleLookupPresent || len(lookup.Role.Sources) == 0 {
		return base
	}
	next := base
	next.Sources = cloneStringMap(base.Sources)
	if !allowModelOverride && strings.TrimSpace(next.Sources["model"]) == "default" {
		next.Sources["model"] = "session"
	}
	for key := range lookup.Role.Sources {
		if key == "model" && !allowModelOverride {
			continue
		}
		next.Sources[key] = "subagent"
	}
	return next
}

func (p Planner) openStore(ctx context.Context, req SessionRequest) (*session.Store, error) {
	if strings.TrimSpace(p.Config.PersistenceRoot) == "" {
		return nil, errors.New("launch planner persistence root is required")
	}
	if strings.TrimSpace(p.ContainerDir) == "" {
		return nil, errors.New("launch planner container dir is required")
	}
	if strings.TrimSpace(req.SelectedSessionID) != "" {
		return p.openScopedSession(req.SelectedSessionID)
	}
	if req.ForceNewSession || req.Mode == ModeHeadless {
		return p.createSession(ctx, req.ParentSessionID, req.Mode)
	}
	return nil, errSessionSelectionRequired
}

func (p Planner) openScopedSession(sessionID string) (*session.Store, error) {
	realSessionDir, err := session.ResolveScopedSessionDir(p.ContainerDir, sessionID)
	if err != nil {
		return nil, err
	}
	return session.Open(realSessionDir, p.StoreOptions...)
}

func (p Planner) createSession(ctx context.Context, parentSessionID string, mode Mode) (*session.Store, error) {
	containerName := filepath.Base(p.ContainerDir)
	created, err := session.NewLazy(p.ContainerDir, containerName, p.Config.WorkspaceRoot, p.StoreOptions...)
	if err != nil {
		return nil, err
	}
	parentID := strings.TrimSpace(parentSessionID)
	if parentID != "" {
		if err := p.initializeChildSessionContext(ctx, created, parentID, mode); err != nil {
			return nil, err
		}
	} else {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := created.EnsureDurable(); err != nil {
			return nil, err
		}
	}
	return created, nil
}

func (p Planner) initializeChildSessionContext(ctx context.Context, child *session.Store, parentSessionID string, mode Mode) error {
	if child == nil {
		return errors.New("child session store is required")
	}
	parentID := strings.TrimSpace(parentSessionID)
	if parentID == "" {
		return child.EnsureDurable()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, err := p.openParentSession(parentID)
	if err != nil {
		return err
	}
	if parent != nil {
		childContextOptions := session.ChildContextOptions{
			InheritLockedContract: true,
			InheritContinuation:   true,
		}
		if mode == ModeHeadless {
			// Headless children are subagent launches: they keep parent workspace
			// and worktree targeting, but their model/tools/prompts/base URL come
			// from the selected role and current config rather than the parent
			// session.
			childContextOptions = session.ChildContextOptions{}
		}
		if err := session.InitializeChildFromParentWithOptions(child, parent, childContextOptions); err != nil {
			return err
		}
	}
	if parent == nil {
		return child.SetParentSessionID(parentID)
	}
	target, hasTarget, err := p.resolveParentExecutionTarget(ctx, parentID)
	if err != nil {
		return err
	}
	if err := child.EnsureDurable(); err != nil {
		return err
	}
	if !hasTarget {
		return nil
	}
	if err := p.updateChildExecutionTarget(ctx, child.Meta().SessionID, target); err != nil {
		return errors.Join(err, p.rollbackChildSession(child))
	}
	return nil
}

func (p Planner) openParentSession(parentSessionID string) (*session.Store, error) {
	parent, err := p.openScopedSession(parentSessionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, session.ErrSessionNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return parent, nil
}

func (p Planner) openMetadataStore() (MetadataExecutionTargetStore, error) {
	if p.MetadataStoreOpener != nil {
		return p.MetadataStoreOpener(p.Config.PersistenceRoot)
	}
	return metadata.Open(p.Config.PersistenceRoot)
}

func (p Planner) resolveParentExecutionTarget(ctx context.Context, parentSessionID string) (clientui.SessionExecutionTarget, bool, error) {
	if err := ctx.Err(); err != nil {
		return clientui.SessionExecutionTarget{}, false, err
	}
	store, err := p.openMetadataStore()
	if err != nil {
		return clientui.SessionExecutionTarget{}, false, err
	}
	defer func() { _ = store.Close() }()
	target, err := store.ResolveSessionExecutionTarget(ctx, parentSessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, session.ErrSessionNotFound) {
			return clientui.SessionExecutionTarget{}, false, nil
		}
		return clientui.SessionExecutionTarget{}, false, err
	}
	return target, true, nil
}

func (p Planner) updateChildExecutionTarget(ctx context.Context, childSessionID string, target clientui.SessionExecutionTarget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store, err := p.openMetadataStore()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	return store.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdateFromReadModel(childSessionID, target))
}

func (p Planner) rollbackChildSession(child *session.Store) error {
	if child == nil {
		return nil
	}
	childMeta := child.Meta()
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var rollbackErrs []error
	if store, err := p.openMetadataStore(); err == nil {
		if err := store.DeleteSessionRecordByID(rollbackCtx, childMeta.SessionID); err != nil {
			rollbackErrs = append(rollbackErrs, err)
		}
		if err := store.Close(); err != nil {
			rollbackErrs = append(rollbackErrs, err)
		}
	} else {
		rollbackErrs = append(rollbackErrs, err)
	}
	if err := child.RemoveDurable(); err != nil {
		rollbackErrs = append(rollbackErrs, err)
	}
	return errors.Join(rollbackErrs...)
}

func EnsureSubagentSessionName(store *session.Store) error {
	if store == nil {
		return errors.New("session store is required")
	}
	meta := store.Meta()
	if strings.TrimSpace(meta.Name) != "" {
		return nil
	}
	name := strings.TrimSpace(meta.SessionID + " " + SubagentSessionSuffix)
	if name == "" {
		return nil
	}
	return store.SetName(name)
}

func EffectiveSettings(base config.Settings, locked *session.LockedContract) config.Settings {
	out := base
	if locked == nil {
		return out
	}
	if strings.TrimSpace(locked.Model) != "" {
		out.Model = locked.Model
	}
	return out
}

func ActiveToolIDsForPlan(settings config.Settings, source config.SourceReport, locked *session.LockedContract) ([]toolspec.ID, error) {
	if locked != nil && (locked.HasEnabledTools || len(locked.EnabledTools) > 0) {
		ids := make([]toolspec.ID, 0, len(locked.EnabledTools))
		for _, raw := range locked.EnabledTools {
			if id, ok := toolspec.ParseID(raw); ok {
				ids = append(ids, id)
			}
		}
		return DedupeSortToolIDs(ids), nil
	}
	enabled := cloneEnabledToolSet(settings.EnabledTools)
	if bothEditToolSourcesDefault(source) {
		if settings.ProviderCapabilities.IsOpenAIFirstParty || strings.HasPrefix(strings.ToLower(strings.TrimSpace(settings.Model)), "gpt-") {
			enabled[toolspec.ToolPatch] = true
			enabled[toolspec.ToolEdit] = false
		} else {
			enabled[toolspec.ToolPatch] = false
			enabled[toolspec.ToolEdit] = true
		}
	}
	if enabled[toolspec.ToolPatch] && enabled[toolspec.ToolEdit] {
		return nil, ErrPatchEditToolsConflict
	}
	return DedupeSortToolIDs(enabledToolIDs(enabled)), nil
}

func bothEditToolSourcesDefault(source config.SourceReport) bool {
	return strings.TrimSpace(source.Sources["tools.patch"]) == "default" && strings.TrimSpace(source.Sources["tools.edit"]) == "default"
}

func enabledToolIDs(enabled map[toolspec.ID]bool) []toolspec.ID {
	ids := make([]toolspec.ID, 0, len(enabled))
	for _, id := range toolspec.CatalogIDs() {
		if enabled[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

func cloneEnabledToolSet(in map[toolspec.ID]bool) map[toolspec.ID]bool {
	if len(in) == 0 {
		return map[toolspec.ID]bool{}
	}
	out := make(map[toolspec.ID]bool, len(in))
	for id, enabled := range in {
		out[id] = enabled
	}
	return out
}

func DedupeSortToolIDs(ids []toolspec.ID) []toolspec.ID {
	seen := map[toolspec.ID]bool{}
	out := make([]toolspec.ID, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
