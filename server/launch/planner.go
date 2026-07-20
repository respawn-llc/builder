package launch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
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
	PersistedSessions   session.PersistedSessionResolver
	MetadataStoreOpener MetadataExecutionTargetStoreOpener
}

type SessionRequest struct {
	Mode                                Mode
	Intent                              serverapi.SessionLaunchIntent
	SkipContinuationAgentRoleValidation bool
	PreparedPromptFacingTarget          *PreparedBaseTarget
}

type SessionPlan struct {
	Descriptor                          session.SessionDescriptor
	ActiveSettings                      config.Settings
	BaseSettings                        config.Settings
	EnabledTools                        []toolspec.ID
	ConfiguredModelName                 string
	SessionName                         string
	FirstPromptPreview                  string
	Goal                                *session.GoalState
	WorktreeReminder                    *session.WorktreeReminderState
	Continuation                        *session.ContinuationContext
	Locked                              *session.LockedContract
	WorkflowSession                     *session.WorkflowSessionState
	PromptHistory                       []string
	ModelContractLocked                 bool
	SkipContinuationAgentRoleValidation bool
	WorkspaceRoot                       string
	Source                              config.SourceReport
	BaseSource                          config.SourceReport
}

func sessionPlanWithSnapshot(plan SessionPlan, store *session.Store, containerDir string) SessionPlan {
	if store == nil {
		panic("session plan snapshot requires a store")
	}
	meta := store.Meta()
	sessionID, err := runtimeids.ParseSessionID(meta.SessionID)
	if err != nil {
		panic(fmt.Sprintf("session plan snapshot has invalid session id %q: %v", meta.SessionID, err))
	}
	descriptor, err := session.NewScopedOpenSessionDescriptor(sessionID, containerDir)
	if err != nil {
		panic(fmt.Sprintf("session plan snapshot cannot scope session %q to %q: %v", meta.SessionID, containerDir, err))
	}
	plan.Descriptor = descriptor
	plan.SessionName = meta.Name
	plan.FirstPromptPreview = meta.FirstPromptPreview
	plan.Goal = meta.Goal
	plan.WorktreeReminder = meta.WorktreeReminder
	plan.Continuation = meta.Continuation
	plan.Locked = meta.Locked
	plan.WorkflowSession = meta.WorkflowSession
	plan.ModelContractLocked = meta.Locked != nil
	return plan
}

func (p Planner) sessionPlanWithSnapshot(plan SessionPlan, store *session.Store) SessionPlan {
	return sessionPlanWithSnapshot(plan, store, p.ContainerDir)
}

func (p Planner) materializePlanStore(plan SessionPlan) (*session.Store, error) {
	return session.MaterializeSessionDescriptor(p.Config.PersistenceRoot, plan.Descriptor, p.StoreOptions...)
}

type RunPromptOverrideOptions struct {
	AllowLockedAgentRoleChange bool
}

// PreparedRunPromptOverrides is the immutable, snapshot-bound portion of a
// RunPrompt override. Session launch prepares it before any new session is
// materialized; applying it later must not reload config or look up a role.
type PreparedRunPromptOverrides struct {
	OverrideConfig config.App
	AgentRole      serverapi.RunPromptAgentRoleOverride
	BaseTarget     *PreparedBaseTarget
	NamedTarget    *PreparedSubagentTarget
}

type PreparedBaseTarget struct {
	Settings     config.Settings
	Source       config.SourceReport
	EnabledTools []toolspec.ID
}

// RunPromptPreparationContext carries a selected session's immutable,
// prompt-facing contract into pre-materialization target preparation.
type RunPromptPreparationContext struct {
	ModelLock     *session.LockedContract
	ToolLock      *session.LockedContract
	OmittedTarget *PreparedBaseTarget
}

type PreparedSubagentTarget struct {
	Selector     string
	Settings     config.Settings
	Source       config.SourceReport
	EnabledTools []toolspec.ID
	Warning      *string
}

type preparedSubagentIdentity struct {
	Selector   string
	Role       config.SubagentRole
	ProviderID string
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
	plan, err := resolvePromptFacingSnapshotPlan(app, store, skipContinuationAgentRoleValidation)
	if err != nil {
		return SessionPlan{}, err
	}
	meta := store.Meta()
	if meta.Locked != nil && (!meta.Locked.HasEnabledTools || strings.TrimSpace(meta.Locked.WebSearchMode) == "") {
		backfill, backfillErr := store.BackfillLockedRequestShape(session.LockedRequestShapeBackfill{
			EnabledTools:    toolspec.IDStrings(plan.EnabledTools),
			HasEnabledTools: true,
			WebSearchMode:   strings.TrimSpace(plan.ActiveSettings.WebSearch),
		})
		if backfillErr != nil && !backfill.Committed {
			return SessionPlan{}, backfillErr
		}
	}
	return sessionPlanWithSnapshot(plan, store, filepath.Dir(store.Dir())), nil
}

func resolvePromptFacingSnapshotPlan(app config.App, store *session.Store, skipContinuationAgentRoleValidation bool) (SessionPlan, error) {
	if store == nil {
		return SessionPlan{}, errors.New("session store is required")
	}
	meta := store.Meta()
	baseActive := EffectiveSettings(app.Settings, meta.Locked)
	baseSource := app.Source
	active, source := baseActive, baseSource
	if meta.Continuation != nil {
		var err error
		active, source, err = applyPersistedSubagentRoleSettings(baseActive, baseSource, meta.Continuation.AgentRole, meta.Locked == nil, !skipContinuationAgentRoleValidation)
		if err != nil {
			return SessionPlan{}, err
		}
		if shouldApplyPersistedContinuationBaseURL(baseActive, meta.Continuation.AgentRole) {
			if baseURL := strings.TrimSpace(meta.Continuation.OpenAIBaseURL); baseURL != "" {
				active.OpenAIBaseURL = baseURL
			}
		}
	}
	enabledTools, err := ActiveToolIDsForPlan(active, source, meta.Locked)
	if err != nil {
		return SessionPlan{}, err
	}
	configuredModelName := app.Settings.Model
	if meta.Locked == nil {
		configuredModelName = active.Model
	}
	return sessionPlanWithSnapshot(SessionPlan{
		ActiveSettings:                      active,
		BaseSettings:                        baseActive,
		EnabledTools:                        enabledTools,
		ConfiguredModelName:                 configuredModelName,
		ModelContractLocked:                 meta.Locked != nil,
		SkipContinuationAgentRoleValidation: skipContinuationAgentRoleValidation,
		WorkspaceRoot:                       app.WorkspaceRoot,
		Source:                              source,
		BaseSource:                          baseSource,
	}, store, filepath.Dir(store.Dir())), nil
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
	var continuationAgentRole *string
	continuationBaseURL := ""
	if meta.Continuation != nil {
		continuationAgentRole = cloneContinuationRole(meta.Continuation.AgentRole)
		continuationBaseURL = strings.TrimSpace(meta.Continuation.OpenAIBaseURL)
	}
	active, source := baseActive, baseSource
	enabledTools := []toolspec.ID(nil)
	if req.PreparedPromptFacingTarget != nil {
		active = cloneSettings(req.PreparedPromptFacingTarget.Settings)
		source = cloneSourceReport(req.PreparedPromptFacingTarget.Source)
		enabledTools = append([]toolspec.ID(nil), req.PreparedPromptFacingTarget.EnabledTools...)
	} else if meta.Continuation != nil {
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
	if req.PreparedPromptFacingTarget == nil {
		enabledTools, err = ActiveToolIDsForPlan(active, source, meta.Locked)
		if err != nil {
			return SessionPlan{}, err
		}
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
	return p.sessionPlanWithSnapshot(SessionPlan{
		ActiveSettings:                      active,
		BaseSettings:                        baseActive,
		EnabledTools:                        enabledTools,
		ConfiguredModelName:                 configuredModelName,
		ModelContractLocked:                 meta.Locked != nil,
		SkipContinuationAgentRoleValidation: req.SkipContinuationAgentRoleValidation,
		WorkspaceRoot:                       p.Config.WorkspaceRoot,
		Source:                              source,
		BaseSource:                          baseSource,
	}, store), nil
}

func applyPersistedSubagentRoleSettings(base config.Settings, source config.SourceReport, roleName *string, allowModelOverride bool, validate bool) (config.Settings, config.SourceReport, error) {
	if roleName == nil {
		return base, source, nil
	}
	lookup := config.LookupSubagentRole(base, *roleName)
	if lookup.Status == config.SubagentRoleLookupInvalid {
		return base, source, nil
	}
	if lookup.Status == config.SubagentRoleLookupMissing {
		return base, source, nil
	}
	providerSettings := cloneSettings(base)
	providerSettings = config.OverlaySubagentRoleProviderSettings(providerSettings, lookup.Role)
	resolved, effectiveSource, _, err := resolveSubagentSettingsWithProviderID(base, source, *lookup.NormalizedSelector, persistedRoleProviderID(providerSettings), allowModelOverride, validate)
	if err != nil {
		return config.Settings{}, config.SourceReport{}, err
	}
	return resolved, effectiveSource, nil
}

func shouldApplyPersistedContinuationBaseURL(base config.Settings, roleName *string) bool {
	if roleName == nil {
		return true
	}
	lookup := config.LookupSubagentRole(base, *roleName)
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

func (p Planner) ApplyRunPromptOverrides(plan SessionPlan, overrides serverapi.RunPromptOverrides, authState auth.State) (SessionPlan, []string, error) {
	return p.ApplyRunPromptOverridesWithOptions(plan, overrides, authState, RunPromptOverrideOptions{})
}

func (p Planner) ApplyRunPromptOverridesWithOptions(plan SessionPlan, overrides serverapi.RunPromptOverrides, authState auth.State, options RunPromptOverrideOptions) (SessionPlan, []string, error) {
	store, err := p.materializePlanStore(plan)
	if err != nil {
		return SessionPlan{}, nil, err
	}
	return p.applyRunPromptOverridesWithBudgetApplier(plan, store, overrides, authState, options, applyDerivedModelContextBudgetOverrides)
}

func (p Planner) applyRunPromptOverridesWithBudgetApplier(plan SessionPlan, store *session.Store, overrides serverapi.RunPromptOverrides, authState auth.State, options RunPromptOverrideOptions, applyBudget modelContextBudgetApplier) (SessionPlan, []string, error) {
	locked := store.Meta().Locked
	toolLock := locked
	if options.AllowLockedAgentRoleChange {
		toolLock = nil
	}
	prepared, err := prepareRunPromptOverridesWithBudget(baseConfigForPlan(plan), overrides, authState, RunPromptPreparationContext{
		ModelLock: locked,
		ToolLock:  toolLock,
		OmittedTarget: &PreparedBaseTarget{
			Settings:     plan.ActiveSettings,
			Source:       plan.Source,
			EnabledTools: plan.EnabledTools,
		},
	}, applyBudget)
	if err != nil {
		return SessionPlan{}, nil, err
	}
	return p.applyPreparedRunPromptOverridesWithBudgetApplier(plan, store, overrides, prepared, options, applyBudget)
}

func baseConfigForPlan(plan SessionPlan) config.App {
	settings := plan.BaseSettings
	if strings.TrimSpace(settings.Model) == "" {
		settings = plan.ActiveSettings
	}
	source := plan.BaseSource
	if source.Sources == nil {
		source = plan.Source
	}
	return config.App{
		WorkspaceRoot: plan.WorkspaceRoot,
		Settings:      settings,
		Source:        source,
	}
}

type modelContextBudgetApplier func(settings *config.Settings, explicitSources map[string]string, originalModel string, allowModelOverride bool)

// PrepareRunPromptOverrides resolves every config-backed part of a RunPrompt
// target from one loaded application snapshot. It intentionally performs no
// store mutation, config reload, or session materialization.
func PrepareRunPromptOverrides(app config.App, overrides serverapi.RunPromptOverrides, authState auth.State) (PreparedRunPromptOverrides, error) {
	return PrepareRunPromptOverridesWithContext(app, overrides, authState, RunPromptPreparationContext{})
}

func PrepareRunPromptOverridesForLockedSession(app config.App, overrides serverapi.RunPromptOverrides, authState auth.State, locked *session.LockedContract) (PreparedRunPromptOverrides, error) {
	return PrepareRunPromptOverridesWithContext(app, overrides, authState, RunPromptPreparationContext{
		ModelLock: locked,
		ToolLock:  locked,
	})
}

func PrepareRunPromptOverridesWithContext(app config.App, overrides serverapi.RunPromptOverrides, authState auth.State, preparation RunPromptPreparationContext) (PreparedRunPromptOverrides, error) {
	return prepareRunPromptOverridesWithBudget(app, overrides, authState, preparation, applyDerivedModelContextBudgetOverrides)
}

func prepareRunPromptOverridesWithBudget(app config.App, overrides serverapi.RunPromptOverrides, authState auth.State, preparation RunPromptPreparationContext, applyBudget modelContextBudgetApplier) (PreparedRunPromptOverrides, error) {
	roleOverride, err := overrides.AgentRoleOverride()
	if err != nil {
		return PreparedRunPromptOverrides{}, fmt.Errorf("%w: %v", errInvalidAgentRole, err)
	}
	overrideConfig := app
	if overrides.HasConfigOverrides() {
		overrideConfig, err = config.ApplyLoadOptionsToSnapshot(app, runPromptLoadOptions(overrides))
		if err != nil {
			return PreparedRunPromptOverrides{}, err
		}
	}
	prepared := PreparedRunPromptOverrides{
		OverrideConfig: overrideConfig,
		AgentRole:      roleOverride,
	}
	if !roleOverride.Present || roleOverride.Default {
		if !roleOverride.Present && preparation.OmittedTarget != nil {
			target, targetErr := preparePreparedBaseTarget(*preparation.OmittedTarget, overrideConfig, overrides, preparation.ModelLock, preparation.ToolLock, applyBudget)
			if targetErr != nil {
				return PreparedRunPromptOverrides{}, targetErr
			}
			prepared.BaseTarget = &target
		} else {
			target, targetErr := prepareBaseTarget(app, overrideConfig, overrides, preparation.ModelLock, preparation.ToolLock, applyBudget)
			if targetErr != nil {
				return PreparedRunPromptOverrides{}, targetErr
			}
			prepared.BaseTarget = &target
		}
		return prepared, nil
	}
	lookup := config.LookupSubagentRole(app.Settings, roleOverride.Role)
	switch lookup.Status {
	case config.SubagentRoleLookupInvalid:
		return PreparedRunPromptOverrides{}, fmt.Errorf("%w: invalid subagent role %q", errInvalidAgentRole, roleOverride.Role)
	case config.SubagentRoleLookupMissing:
		return PreparedRunPromptOverrides{}, fmt.Errorf("%w: unrecognized role %q", errInvalidAgentRole, roleOverride.Role)
	}
	providerSettings := EffectiveSettings(app.Settings, preparation.ModelLock)
	providerSettings.ProviderOverride = overrideConfig.Settings.ProviderOverride
	providerSettings.OpenAIBaseURL = overrideConfig.Settings.OpenAIBaseURL
	providerSettings.Subagents = nil
	providerSettings = config.OverlaySubagentRoleProviderSettings(providerSettings, lookup.Role)
	providerCaps, err := llm.ProviderCapabilitiesForSettings(authState, providerSettings)
	if err != nil {
		return PreparedRunPromptOverrides{}, err
	}
	target, err := prepareNamedTarget(app, overrideConfig, overrides, *lookup.NormalizedSelector, lookup.Role, strings.TrimSpace(providerCaps.ProviderID), preparation.ModelLock, preparation.ToolLock, applyBudget)
	if err != nil {
		return PreparedRunPromptOverrides{}, err
	}
	prepared.NamedTarget = &target
	return prepared, nil
}

func prepareBaseTarget(app, overrideConfig config.App, overrides serverapi.RunPromptOverrides, modelLock, toolLock *session.LockedContract, applyBudget modelContextBudgetApplier) (PreparedBaseTarget, error) {
	resolved := EffectiveSettings(app.Settings, modelLock)
	source := app.Source
	enabledTools, err := ActiveToolIDsForPlan(resolved, source, toolLock)
	if err != nil {
		return PreparedBaseTarget{}, err
	}
	return preparePreparedBaseTarget(PreparedBaseTarget{Settings: resolved, Source: source, EnabledTools: enabledTools}, overrideConfig, overrides, modelLock, toolLock, applyBudget)
}

func preparePreparedBaseTarget(target PreparedBaseTarget, overrideConfig config.App, overrides serverapi.RunPromptOverrides, modelLock, toolLock *session.LockedContract, applyBudget modelContextBudgetApplier) (PreparedBaseTarget, error) {
	resolved := cloneSettings(target.Settings)
	source := cloneSourceReport(target.Source)
	enabledTools := append([]toolspec.ID(nil), target.EnabledTools...)
	var err error
	resolved, source, enabledTools, err = applyPreparedConfigOverrides(resolved, source, enabledTools, overrideConfig, overrides, modelLock, toolLock, applyBudget)
	if err != nil {
		return PreparedBaseTarget{}, err
	}
	if overrides.HasConfigOverrides() {
		resolved, err = validateRunPromptOverrideSettings(resolved, source)
		if err != nil {
			return PreparedBaseTarget{}, err
		}
	}
	return PreparedBaseTarget{Settings: resolved, Source: source, EnabledTools: append([]toolspec.ID(nil), enabledTools...)}, nil
}

func prepareNamedTarget(app, overrideConfig config.App, overrides serverapi.RunPromptOverrides, selector string, role config.SubagentRole, providerID string, modelLock, toolLock *session.LockedContract, applyBudget modelContextBudgetApplier) (PreparedSubagentTarget, error) {
	input := preparedSubagentIdentity{Selector: selector, Role: role, ProviderID: providerID}
	baseSettings := EffectiveSettings(app.Settings, modelLock)
	resolved, source, warning, err := resolvePreparedSubagentSettings(baseSettings, app.Source, input, modelLock == nil, false)
	if err != nil {
		return PreparedSubagentTarget{}, err
	}
	enabledTools, err := ActiveToolIDsForPlan(resolved, source, toolLock)
	if err != nil {
		return PreparedSubagentTarget{}, err
	}
	resolved, source, enabledTools, err = applyPreparedConfigOverrides(resolved, source, enabledTools, overrideConfig, overrides, modelLock, toolLock, applyBudget)
	if err != nil {
		return PreparedSubagentTarget{}, err
	}
	resolved, err = validateRunPromptOverrideSettings(resolved, source)
	if err != nil {
		return PreparedSubagentTarget{}, err
	}
	return PreparedSubagentTarget{
		Selector:     selector,
		Settings:     resolved,
		Source:       source,
		EnabledTools: append([]toolspec.ID(nil), enabledTools...),
		Warning:      warning,
	}, nil
}

func applyPreparedConfigOverrides(settings config.Settings, source config.SourceReport, enabledTools []toolspec.ID, overrideConfig config.App, overrides serverapi.RunPromptOverrides, modelLock, toolLock *session.LockedContract, applyBudget modelContextBudgetApplier) (config.Settings, config.SourceReport, []toolspec.ID, error) {
	if !overrides.HasConfigOverrides() {
		return settings, source, enabledTools, nil
	}
	source = mergeOverrideSources(source, overrideConfig.Source)
	if strings.TrimSpace(overrides.Model) != "" && modelLock == nil {
		originalModel := settings.Model
		explicitSources := map[string]string{}
		for key, value := range source.Sources {
			if strings.TrimSpace(value) != "" && strings.TrimSpace(value) != "default" {
				explicitSources[key] = value
			}
		}
		settings.Model = overrideConfig.Settings.Model
		applyBudget(&settings, explicitSources, originalModel, true)
	}
	if strings.TrimSpace(overrides.ProviderOverride) != "" {
		settings.ProviderOverride = overrideConfig.Settings.ProviderOverride
	}
	if strings.TrimSpace(overrides.ThinkingLevel) != "" {
		settings.ThinkingLevel = overrideConfig.Settings.ThinkingLevel
	}
	if strings.TrimSpace(overrides.Theme) != "" {
		settings.Theme = overrideConfig.Settings.Theme
	}
	if overrides.ModelTimeoutSeconds > 0 {
		settings.Timeouts.ModelRequestSeconds = overrideConfig.Settings.Timeouts.ModelRequestSeconds
	}
	if strings.TrimSpace(overrides.OpenAIBaseURL) != "" {
		settings.OpenAIBaseURL = overrideConfig.Settings.OpenAIBaseURL
	}
	if strings.TrimSpace(overrides.Tools) != "" && toolLock == nil {
		settings.EnabledTools = cloneMapOrEmpty(overrideConfig.Settings.EnabledTools)
	}
	if toolLock == nil && (strings.TrimSpace(overrides.Tools) != "" || strings.TrimSpace(overrides.Model) != "") {
		var err error
		enabledTools, err = ActiveToolIDsForPlan(settings, source, nil)
		if err != nil {
			return config.Settings{}, config.SourceReport{}, nil, err
		}
	}
	return settings, source, enabledTools, nil
}

func (p Planner) ApplyPreparedRunPromptOverrides(plan SessionPlan, overrides serverapi.RunPromptOverrides, prepared PreparedRunPromptOverrides) (SessionPlan, []string, error) {
	return p.ApplyPreparedRunPromptOverridesWithOptions(plan, overrides, prepared, RunPromptOverrideOptions{})
}

func (p Planner) ApplyPreparedRunPromptOverridesWithOptions(plan SessionPlan, overrides serverapi.RunPromptOverrides, prepared PreparedRunPromptOverrides, options RunPromptOverrideOptions) (SessionPlan, []string, error) {
	store, err := p.materializePlanStore(plan)
	if err != nil {
		return SessionPlan{}, nil, err
	}
	return p.applyPreparedRunPromptOverridesWithBudgetApplier(plan, store, overrides, prepared, options, applyDerivedModelContextBudgetOverrides)
}

func (p Planner) applyPreparedRunPromptOverridesWithBudgetApplier(plan SessionPlan, store *session.Store, overrides serverapi.RunPromptOverrides, prepared PreparedRunPromptOverrides, options RunPromptOverrideOptions, applyBudget modelContextBudgetApplier) (SessionPlan, []string, error) {
	if !overrides.HasAny() && prepared.BaseTarget == nil {
		return p.sessionPlanWithSnapshot(plan, store), nil, nil
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
	var continuationAgentRole *string
	if store.Meta().Continuation != nil {
		continuationAgentRole = cloneContinuationRole(store.Meta().Continuation.AgentRole)
	}
	staleLockedPromptFacingContract := false
	persistContinuation := func() error {
		ctx := session.ContinuationContext{
			OpenAIBaseURL: next.ActiveSettings.OpenAIBaseURL,
			AgentRole:     continuationAgentRole,
		}
		if staleLockedPromptFacingContract {
			_, err := store.SetContinuationContextAndMarkLockedPromptFacingContractStale(ctx)
			return err
		}
		return store.SetContinuationContext(ctx)
	}
	roleOverride := prepared.AgentRole
	if !roleOverride.Present && prepared.BaseTarget != nil {
		next.ActiveSettings = cloneSettings(prepared.BaseTarget.Settings)
		next.Source = cloneSourceReport(prepared.BaseTarget.Source)
		next.EnabledTools = append([]toolspec.ID(nil), prepared.BaseTarget.EnabledTools...)
		if !plan.ModelContractLocked {
			next.ConfiguredModelName = next.ActiveSettings.Model
		}
		if strings.TrimSpace(overrides.OpenAIBaseURL) != "" {
			if err := persistContinuation(); err != nil {
				return SessionPlan{}, nil, err
			}
		}
		return p.sessionPlanWithSnapshot(next, store), warnings, nil
	}
	var requestedContinuationRole *string
	if roleOverride.Present && !roleOverride.Default {
		requestedContinuationRole = cloneContinuationRole(&roleOverride.Role)
	}
	if roleOverride.Present && plan.ModelContractLocked && !textutil.EqualOptional(continuationAgentRole, requestedContinuationRole) && !options.AllowLockedAgentRoleChange {
		return SessionPlan{}, nil, fmt.Errorf("%w: current=%q requested=%q", ErrLockedAgentRoleChange, continuationRoleDisplay(continuationAgentRole), roleOverride.Role)
	}
	if roleOverride.Present && plan.ModelContractLocked && !textutil.EqualOptional(continuationAgentRole, requestedContinuationRole) && options.AllowLockedAgentRoleChange {
		staleLockedPromptFacingContract = true
	}
	if roleOverride.Present {
		shouldPersistContinuation = true
		continuationAgentRole = requestedContinuationRole
		next.ActiveSettings = cloneSettings(baseSettings)
		next.Source = baseSource
		if !plan.ModelContractLocked {
			next.ConfiguredModelName = next.ActiveSettings.Model
		}
		if roleOverride.Default {
			if prepared.BaseTarget == nil {
				return SessionPlan{}, nil, errors.New("prepared base target is required for explicit default selector")
			}
			next.ActiveSettings = cloneSettings(prepared.BaseTarget.Settings)
			next.Source = prepared.BaseTarget.Source
			next.EnabledTools = append([]toolspec.ID(nil), prepared.BaseTarget.EnabledTools...)
			if !plan.ModelContractLocked {
				next.ConfiguredModelName = next.ActiveSettings.Model
			}
			if shouldPersistContinuation {
				if err := persistContinuation(); err != nil {
					return SessionPlan{}, nil, err
				}
			}
			return p.sessionPlanWithSnapshot(next, store), warnings, nil
		}
	}
	if roleOverride.Role != "" {
		if prepared.NamedTarget == nil || prepared.NamedTarget.Selector != roleOverride.Role {
			return SessionPlan{}, nil, errors.New("prepared named subagent target is required")
		}
		next.ActiveSettings = cloneSettings(prepared.NamedTarget.Settings)
		if !plan.ModelContractLocked {
			next.ConfiguredModelName = next.ActiveSettings.Model
		}
		next.EnabledTools = append([]toolspec.ID(nil), prepared.NamedTarget.EnabledTools...)
		next.Source = prepared.NamedTarget.Source
		if prepared.NamedTarget.Warning != nil {
			warnings = append(warnings, *prepared.NamedTarget.Warning)
		}
		if shouldPersistContinuation {
			if err := persistContinuation(); err != nil {
				return SessionPlan{}, nil, err
			}
		}
		return p.sessionPlanWithSnapshot(next, store), warnings, nil
	}
	if !overrides.HasConfigOverrides() {
		if shouldPersistContinuation {
			if err := persistContinuation(); err != nil {
				return SessionPlan{}, nil, err
			}
		}
		return p.sessionPlanWithSnapshot(next, store), warnings, nil
	}
	loaded := prepared.OverrideConfig
	locked := store.Meta().Locked
	var err error
	if strings.TrimSpace(overrides.OpenAIBaseURL) != "" {
		shouldPersistContinuation = true
	}
	next.ActiveSettings, next.Source, next.EnabledTools, err = applyPreparedConfigOverrides(
		cloneSettings(next.ActiveSettings),
		cloneSourceReport(next.Source),
		append([]toolspec.ID(nil), next.EnabledTools...),
		loaded,
		overrides,
		locked,
		locked,
		applyBudget,
	)
	if err != nil {
		return SessionPlan{}, nil, err
	}
	if strings.TrimSpace(overrides.Model) != "" && !next.ModelContractLocked {
		next.ConfiguredModelName = loaded.Settings.Model
	}
	validated, err := validateRunPromptOverrideSettings(next.ActiveSettings, next.Source)
	if err != nil {
		return SessionPlan{}, nil, err
	}
	next.ActiveSettings = validated
	if shouldPersistContinuation {
		if err := persistContinuation(); err != nil {
			return SessionPlan{}, nil, err
		}
	}
	return p.sessionPlanWithSnapshot(next, store), warnings, nil
}

func runPromptLoadOptions(overrides serverapi.RunPromptOverrides) config.LoadOptions {
	return config.LoadOptions{
		Model:               strings.TrimSpace(overrides.Model),
		ProviderOverride:    strings.TrimSpace(overrides.ProviderOverride),
		ThinkingLevel:       strings.TrimSpace(overrides.ThinkingLevel),
		Theme:               strings.TrimSpace(overrides.Theme),
		ModelTimeoutSeconds: overrides.ModelTimeoutSeconds,
		Tools:               strings.TrimSpace(overrides.Tools),
		OpenAIBaseURL:       strings.TrimSpace(overrides.OpenAIBaseURL),
	}
}

func resolvePreparedSubagentSettings(base config.Settings, baseSource config.SourceReport, target preparedSubagentIdentity, allowModelOverride bool, validate bool) (config.Settings, config.SourceReport, *string, error) {
	return resolveSubagentSettingsFromRole(base, baseSource, target.Selector, target.Role, target.ProviderID, allowModelOverride, validate)
}

func continuationRoleDisplay(role *string) string {
	if role == nil {
		return config.DefaultSubagentRole
	}
	return *role
}

func cloneContinuationRole(role *string) *string {
	if role == nil {
		return nil
	}
	copyRole := *role
	return &copyRole
}

func validateRunPromptOverrideSettings(settings config.Settings, source config.SourceReport) (config.Settings, error) {
	validated := cloneSettings(settings)
	sources := cloneMapOrEmpty(source.Sources)
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

func cloneSourceReport(source config.SourceReport) config.SourceReport {
	next := source
	next.Sources = cloneMapOrEmpty(source.Sources)
	return next
}

func sourceReportWithSubagentRoleSources(base config.SourceReport, role config.SubagentRole, allowModelOverride bool) config.SourceReport {
	if len(role.Sources) == 0 {
		return base
	}
	next := base
	next.Sources = cloneMapOrEmpty(base.Sources)
	if !allowModelOverride && strings.TrimSpace(next.Sources["model"]) == "default" {
		next.Sources["model"] = "session"
	}
	for key := range role.Sources {
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
	switch req.Intent.Kind() {
	case serverapi.SessionLaunchIntentOpenExisting:
		sessionID, _ := req.Intent.SessionID()
		opened, err := p.openScopedSession(sessionID.String())
		if err != nil {
			return nil, err
		}
		if req.Mode == ModeInteractive {
			if _, err := opened.PromoteSubagentToMain(); err != nil {
				return nil, err
			}
		}
		return opened, nil
	case serverapi.SessionLaunchIntentCreateNew:
		origin, ok := req.Intent.CreateOrigin()
		if !ok {
			return nil, errors.New("create-new session launch intent requires origin")
		}
		return p.createSession(ctx, origin, req.Mode)
	default:
		return nil, errSessionLaunchIntentRequired
	}
}

func (p Planner) openScopedSession(sessionID string) (*session.Store, error) {
	realSessionDir, err := session.ResolveScopedSessionDir(p.ContainerDir, sessionID)
	if err != nil {
		return nil, err
	}
	return session.Open(realSessionDir, p.StoreOptions...)
}

// SelectedSessionLockedContract reads a selected session's persisted lock
// without materializing a new child or mutating the selected session.
func (p Planner) SelectedSessionLockedContract(sessionID runtimeids.SessionID) (*session.LockedContract, error) {
	store, err := p.openScopedSession(sessionID.String())
	if err != nil {
		return nil, err
	}
	return store.Meta().Locked, nil
}

// SelectedSessionPromptFacingTarget resolves a selected session's persisted
// continuation and lock without materializing or mutating the session.
func (p Planner) SelectedSessionPromptFacingTarget(sessionID runtimeids.SessionID) (PreparedBaseTarget, error) {
	store, err := p.openScopedSession(sessionID.String())
	if err != nil {
		return PreparedBaseTarget{}, err
	}
	plan, err := resolvePromptFacingSnapshotPlan(p.Config, store, false)
	if err != nil {
		return PreparedBaseTarget{}, err
	}
	return PreparedBaseTarget{
		Settings:     cloneSettings(plan.ActiveSettings),
		Source:       cloneSourceReport(plan.Source),
		EnabledTools: append([]toolspec.ID(nil), plan.EnabledTools...),
	}, nil
}

// SelectedSessionContinuationAgentRole reads the persisted continuation role
// without applying it or mutating the selected session.
func (p Planner) SelectedSessionContinuationAgentRole(sessionID runtimeids.SessionID) (*string, error) {
	store, err := p.openScopedSession(sessionID.String())
	if err != nil {
		return nil, err
	}
	continuation := store.Meta().Continuation
	if continuation == nil {
		return nil, nil
	}
	return cloneContinuationRole(continuation.AgentRole), nil
}

func (p Planner) createSession(ctx context.Context, origin serverapi.SessionCreateOrigin, mode Mode) (*session.Store, error) {
	containerName := filepath.Base(p.ContainerDir)
	category := sessioncontract.SessionCategoryMain
	if mode == ModeHeadless {
		category = sessioncontract.SessionCategorySubagent
	}
	if origin.Kind() == serverapi.SessionCreateOriginIndependent {
		created, err := session.NewLazy(p.ContainerDir, containerName, p.Config.WorkspaceRoot, category, p.StoreOptions...)
		if err != nil {
			return nil, err
		}
		if err := session.InitializeCreationContext(created, nil, session.SessionCreationSourceIndependent, session.ChildContextOptions{}); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := created.EnsureDurable(); err != nil {
			return nil, err
		}
		return created, nil
	}
	sourceID, present := origin.SessionID()
	if !present {
		return nil, errors.New("session creation source is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, err := p.openPersistedSession(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if origin.Kind() == serverapi.SessionCreateOriginParentAgent {
		if err := (parentAgentDepthPolicy{sessions: p.PersistedSessions}).enforce(
			ctx,
			source.Meta(),
			p.Config.Settings.MaxSubagentDepth,
			p.Config.Settings.Debug,
		); err != nil {
			return nil, err
		}
	}
	created, err := session.NewLazy(p.ContainerDir, containerName, p.Config.WorkspaceRoot, category, p.StoreOptions...)
	if err != nil {
		return nil, err
	}
	if err := p.initializeChildSessionContext(ctx, created, source, sourceID, origin.Kind(), mode); err != nil {
		return nil, err
	}
	return created, nil
}

func (p Planner) initializeChildSessionContext(ctx context.Context, child *session.Store, source *session.Store, sourceSessionID runtimeids.SessionID, originKind serverapi.SessionCreateOriginKind, mode Mode) error {
	if child == nil {
		return errors.New("child session store is required")
	}
	if source == nil {
		return errors.New("session creation source is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	childContextOptions := session.ChildContextOptions{
		InheritLockedContract: true,
		InheritContinuation:   true,
	}
	creationSourceKind := session.SessionCreationSourcePreviousSession
	if originKind == serverapi.SessionCreateOriginParentAgent {
		creationSourceKind = session.SessionCreationSourceParentAgent
		childContextOptions = session.ChildContextOptions{}
	} else if originKind != serverapi.SessionCreateOriginPreviousSession {
		return errors.New("session creation origin kind is invalid")
	}
	if err := session.InitializeCreationContext(child, source, creationSourceKind, childContextOptions); err != nil {
		return err
	}
	target, hasTarget, err := p.resolveParentExecutionTarget(ctx, sourceSessionID.String())
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

func (p Planner) openPersistedSession(ctx context.Context, sessionID runtimeids.SessionID) (*session.Store, error) {
	if p.PersistedSessions == nil {
		return nil, errors.New("persisted session resolver is required")
	}
	record, err := p.PersistedSessions.ResolvePersistedSession(ctx, sessionID.String())
	if err != nil {
		return nil, err
	}
	return session.OpenResolved(record, p.StoreOptions...)
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
	enabled := cloneMapOrEmpty(settings.EnabledTools)
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
