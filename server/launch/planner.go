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
	"core/shared/sessioncontract"
	"core/shared/toolspec"

	"github.com/google/uuid"
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

type missingWorktreeRecoveryStore interface {
	MetadataExecutionTargetStore
	GetWorktreeRecordByID(context.Context, string) (metadata.WorktreeRecord, error)
	LookupWorkspaceBindingByID(context.Context, string) (metadata.Binding, error)
	ListProjectWorkspaces(context.Context, string) ([]clientui.ProjectWorkspaceSummary, error)
}

type sessionRecoveryStoreRegistry interface {
	ResolveStore(context.Context, string) (*session.Store, error)
	RegisterStore(*session.Store)
	UnregisterStore(string)
}

// MetadataExecutionTargetStoreOpener opens metadata storage for launch planning.
type MetadataExecutionTargetStoreOpener func(persistenceRoot string) (MetadataExecutionTargetStore, error)

type Planner struct {
	Config              config.App
	ContainerDir        string
	StoreOptions        []session.StoreOption
	ReloadConfig        func() (config.App, error)
	MetadataStoreOpener MetadataExecutionTargetStoreOpener
	RuntimeActive       func(string) bool
	BlockSessionRuns    func([]string) func()
	SessionStores       sessionRecoveryStoreRegistry
	RetargetWorkspace   func(context.Context, serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error)
}

type SessionRequest struct {
	Mode                                Mode
	Intent                              serverapi.SessionLaunchIntent
	SelectedSessionID                   string
	ForceNewSession                     bool
	ParentSessionID                     *string
	SkipContinuationAgentRoleValidation bool
	PreparedPromptFacingTarget          *PreparedBaseTarget
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
	Recovery                            *serverapi.SessionPlanRecovery
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
	Warning      string
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
	return plan, nil
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
	var recovery *serverapi.SessionPlanRecovery
	if req.Mode == ModeInteractive && req.Intent.Kind() == serverapi.SessionLaunchIntentOpenExisting {
		recovery, store, err = p.recoverMissingManagedWorktreeTarget(ctx, store)
		if err != nil {
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
	workspaceRoot := p.Config.WorkspaceRoot
	if recovery != nil {
		workspaceRoot = recovery.WorkspaceRoot
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
		WorkspaceRoot:                       workspaceRoot,
		Source:                              source,
		BaseSource:                          baseSource,
		Recovery:                            recovery,
	}, nil
}

type SessionTargetRecoveryErrorReason string

const (
	SessionTargetRecoveryActive           SessionTargetRecoveryErrorReason = "active"
	SessionTargetRecoveryUnmanaged        SessionTargetRecoveryErrorReason = "unmanaged"
	SessionTargetRecoveryNoCandidate      SessionTargetRecoveryErrorReason = "no_candidate"
	SessionTargetRecoveryStateUnavailable SessionTargetRecoveryErrorReason = "runtime_state_unavailable"
)

type SessionTargetRecoveryError struct {
	Reason    SessionTargetRecoveryErrorReason
	Candidate *clientui.ProjectWorkspaceSummary
	Cause     error
}

func (e *SessionTargetRecoveryError) Error() string {
	return fmt.Sprintf("session execution target recovery failed: %s", e.Reason)
}

func (e *SessionTargetRecoveryError) Unwrap() error { return e.Cause }

func (p Planner) recoverMissingManagedWorktreeTarget(ctx context.Context, store *session.Store) (*serverapi.SessionPlanRecovery, *session.Store, error) {
	if store == nil {
		return nil, nil, errors.New("session store is required")
	}
	metadataStore, err := p.openMetadataStore()
	if err != nil {
		return nil, store, err
	}
	defer func() { _ = metadataStore.Close() }()
	recoveryStore, ok := metadataStore.(missingWorktreeRecoveryStore)
	if !ok {
		return nil, store, &SessionTargetRecoveryError{Reason: SessionTargetRecoveryStateUnavailable}
	}
	target, err := recoveryStore.ResolveSessionExecutionTarget(ctx, store.Meta().SessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, session.ErrSessionNotFound) {
			return nil, store, nil
		}
		return nil, store, err
	}
	missing := target.Worktree != nil && target.Worktree.Availability == string(clientui.ProjectAvailabilityMissing)
	if !missing {
		return nil, store, nil
	}
	candidate, err := recoveryWorkspaceCandidate(ctx, recoveryStore, target.WorkspaceID)
	if err != nil {
		return nil, store, &SessionTargetRecoveryError{Reason: SessionTargetRecoveryNoCandidate, Cause: err}
	}
	record, err := recoveryStore.GetWorktreeRecordByID(ctx, target.Worktree.ID)
	if err != nil {
		return nil, store, err
	}
	if !record.Managed {
		return nil, store, &SessionTargetRecoveryError{Reason: SessionTargetRecoveryUnmanaged, Candidate: &candidate}
	}
	if p.RuntimeActive == nil || p.BlockSessionRuns == nil || p.SessionStores == nil || p.RetargetWorkspace == nil {
		return nil, store, &SessionTargetRecoveryError{Reason: SessionTargetRecoveryStateUnavailable, Candidate: &candidate}
	}
	release := p.BlockSessionRuns([]string{store.Meta().SessionID})
	defer release()
	if p.RuntimeActive(store.Meta().SessionID) {
		return nil, store, &SessionTargetRecoveryError{Reason: SessionTargetRecoveryActive, Candidate: &candidate}
	}
	registered, err := p.SessionStores.ResolveStore(ctx, store.Meta().SessionID)
	if err != nil {
		return nil, store, err
	}
	if registered == nil {
		p.SessionStores.RegisterStore(store)
	} else {
		store = registered
	}
	result, err := p.RetargetWorkspace(ctx, serverapi.SessionRetargetWorkspaceRequest{
		ClientRequestID: uuid.NewString(),
		SessionID:       store.Meta().SessionID,
		WorkspaceRoot:   candidate.RootPath,
	})
	if err != nil {
		if registered == nil {
			p.SessionStores.UnregisterStore(store.Meta().SessionID)
		}
		return nil, store, err
	}
	if err := store.SetWorktreeReminderState(nil); err != nil {
		return nil, store, err
	}
	return &serverapi.SessionPlanRecovery{
		Kind:          serverapi.SessionPlanRecoveryKindDeletedManagedWorktree,
		WorkspaceRoot: result.Binding.CanonicalRoot,
	}, store, nil
}

func recoveryWorkspaceCandidate(ctx context.Context, store missingWorktreeRecoveryStore, workspaceID string) (clientui.ProjectWorkspaceSummary, error) {
	binding, err := store.LookupWorkspaceBindingByID(ctx, workspaceID)
	if err != nil {
		return clientui.ProjectWorkspaceSummary{}, err
	}
	workspaces, err := store.ListProjectWorkspaces(ctx, binding.ProjectID)
	if err != nil {
		return clientui.ProjectWorkspaceSummary{}, err
	}
	for _, workspace := range workspaces {
		root := filepath.Clean(strings.TrimSpace(workspace.RootPath))
		if workspace.WorkspaceID == workspaceID && workspace.Availability == clientui.ProjectAvailabilityAvailable && filepath.IsAbs(root) {
			workspace.RootPath = root
			return workspace, nil
		}
	}
	return clientui.ProjectWorkspaceSummary{}, errors.New("owning project has no available registered workspace")
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
	applySubagentProviderOverrides(&providerSettings, lookup.Role)
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

func ApplyRunPromptOverrides(plan SessionPlan, overrides serverapi.RunPromptOverrides, authState auth.State) (SessionPlan, []string, error) {
	return ApplyRunPromptOverridesWithOptions(plan, overrides, authState, RunPromptOverrideOptions{})
}

func ApplyRunPromptOverridesWithOptions(plan SessionPlan, overrides serverapi.RunPromptOverrides, authState auth.State, options RunPromptOverrideOptions) (SessionPlan, []string, error) {
	return applyRunPromptOverridesWithBudgetApplier(plan, overrides, authState, options, applyDerivedModelContextBudgetOverrides)
}

func applyRunPromptOverridesWithBudgetApplier(plan SessionPlan, overrides serverapi.RunPromptOverrides, authState auth.State, options RunPromptOverrideOptions, applyBudget modelContextBudgetApplier) (SessionPlan, []string, error) {
	var locked *session.LockedContract
	if plan.Store != nil {
		locked = plan.Store.Meta().Locked
	}
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
	return applyPreparedRunPromptOverridesWithBudgetApplier(plan, overrides, prepared, options, applyBudget)
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

func prepareRunPromptOverrides(app config.App, overrides serverapi.RunPromptOverrides, authState auth.State, preparation RunPromptPreparationContext) (PreparedRunPromptOverrides, error) {
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
	providerSettings := cloneSettings(app.Settings)
	providerSettings.ProviderOverride = overrideConfig.Settings.ProviderOverride
	providerSettings.OpenAIBaseURL = overrideConfig.Settings.OpenAIBaseURL
	providerSettings.Subagents = nil
	applySubagentProviderOverrides(&providerSettings, lookup.Role)
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
		settings.EnabledTools = cloneEnabledToolSet(overrideConfig.Settings.EnabledTools)
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

func ApplyPreparedRunPromptOverrides(plan SessionPlan, overrides serverapi.RunPromptOverrides, prepared PreparedRunPromptOverrides) (SessionPlan, []string, error) {
	return ApplyPreparedRunPromptOverridesWithOptions(plan, overrides, prepared, RunPromptOverrideOptions{})
}

func ApplyPreparedRunPromptOverridesWithOptions(plan SessionPlan, overrides serverapi.RunPromptOverrides, prepared PreparedRunPromptOverrides, options RunPromptOverrideOptions) (SessionPlan, []string, error) {
	return applyPreparedRunPromptOverridesWithBudgetApplier(plan, overrides, prepared, options, applyDerivedModelContextBudgetOverrides)
}

func applyPreparedRunPromptOverridesWithBudgetApplier(plan SessionPlan, overrides serverapi.RunPromptOverrides, prepared PreparedRunPromptOverrides, options RunPromptOverrideOptions, applyBudget modelContextBudgetApplier) (SessionPlan, []string, error) {
	if !overrides.HasAny() && prepared.BaseTarget == nil {
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
	var continuationAgentRole *string
	if plan.Store.Meta().Continuation != nil {
		continuationAgentRole = cloneContinuationRole(plan.Store.Meta().Continuation.AgentRole)
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
		return next, warnings, nil
	}
	var requestedContinuationRole *string
	if roleOverride.Present && !roleOverride.Default {
		requestedContinuationRole = cloneContinuationRole(&roleOverride.Role)
	}
	if roleOverride.Present && plan.ModelContractLocked && !sameContinuationRole(continuationAgentRole, requestedContinuationRole) && !options.AllowLockedAgentRoleChange && !plan.SkipContinuationAgentRoleValidation {
		return SessionPlan{}, nil, fmt.Errorf("%w: current=%q requested=%q", ErrLockedAgentRoleChange, continuationRoleDisplay(continuationAgentRole), roleOverride.Role)
	}
	if roleOverride.Present && plan.ModelContractLocked && !sameContinuationRole(continuationAgentRole, requestedContinuationRole) && options.AllowLockedAgentRoleChange {
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
				panic("prepared base target is required for explicit default selector")
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
			return next, warnings, nil
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
		if strings.TrimSpace(prepared.NamedTarget.Warning) != "" {
			warnings = append(warnings, prepared.NamedTarget.Warning)
		}
		if shouldPersistContinuation {
			if err := persistContinuation(); err != nil {
				return SessionPlan{}, nil, err
			}
		}
		return next, warnings, nil
	}
	if !overrides.HasConfigOverrides() {
		if shouldPersistContinuation {
			if err := persistContinuation(); err != nil {
				return SessionPlan{}, nil, err
			}
		}
		return next, warnings, nil
	}
	loaded := prepared.OverrideConfig
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

func resolvePreparedSubagentSettings(base config.Settings, baseSource config.SourceReport, target preparedSubagentIdentity, allowModelOverride bool, validate bool) (config.Settings, config.SourceReport, string, error) {
	resolved := cloneSettings(base)
	_ = applyBuiltInRoleHeuristics(&resolved, target.Selector, target.ProviderID, allowModelOverride)
	applySubagentRoleOverrides(&resolved, target.Role, allowModelOverride)
	effectiveSource := sourceReportWithPreparedSubagentRoleSources(baseSource, target.Role, allowModelOverride)
	effectiveSources := cloneStringMap(effectiveSource.Sources)
	applyReviewerInheritance(&resolved, effectiveSources)
	effectiveSource.Sources = effectiveSources
	if validate {
		if err := config.ValidateSettingsWithSources(resolved, effectiveSources); err != nil {
			return config.Settings{}, config.SourceReport{}, "", fmt.Errorf("invalid subagent role %q: %w", target.Selector, err)
		}
	}
	warning := ""
	if target.Selector == config.BuiltInSubagentRoleFast && sameResolvedSubagentSettings(base, resolved) {
		warning = fastRoleSameAsMainWarning
	}
	return resolved, effectiveSource, warning, nil
}

func sourceReportWithPreparedSubagentRoleSources(base config.SourceReport, role config.SubagentRole, allowModelOverride bool) config.SourceReport {
	if len(role.Sources) == 0 {
		return base
	}
	next := base
	next.Sources = cloneStringMap(base.Sources)
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

func sameContinuationRole(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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

func cloneSourceReport(source config.SourceReport) config.SourceReport {
	next := source
	next.Sources = cloneStringMap(source.Sources)
	return next
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
	switch req.Intent.Kind() {
	case serverapi.SessionLaunchIntentOpenExisting:
		sessionID, _ := req.Intent.SessionID()
		req.SelectedSessionID = sessionID.String()
	case serverapi.SessionLaunchIntentCreateNew:
		req.ForceNewSession = true
		if parentID, ok := req.Intent.ParentID(); ok {
			parent := parentID.String()
			req.ParentSessionID = &parent
		}
	}
	if strings.TrimSpace(req.SelectedSessionID) != "" {
		opened, err := p.openScopedSession(req.SelectedSessionID)
		if err != nil {
			return nil, err
		}
		if req.Mode == ModeInteractive {
			if _, err := opened.PromoteSubagentToMain(); err != nil {
				return nil, err
			}
		}
		return opened, nil
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

// SelectedSessionLockedContract reads a selected session's persisted lock
// without materializing a new child or mutating the selected session.
func (p Planner) SelectedSessionLockedContract(sessionID string) (*session.LockedContract, error) {
	store, err := p.openScopedSession(sessionID)
	if err != nil {
		return nil, err
	}
	return store.Meta().Locked, nil
}

// SelectedSessionPromptFacingTarget resolves a selected session's persisted
// continuation and lock without materializing or mutating the session.
func (p Planner) SelectedSessionPromptFacingTarget(sessionID string) (PreparedBaseTarget, error) {
	store, err := p.openScopedSession(sessionID)
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

func (p Planner) createSession(ctx context.Context, parentSessionID *string, mode Mode) (*session.Store, error) {
	containerName := filepath.Base(p.ContainerDir)
	category := sessioncontract.SessionCategoryMain
	if mode == ModeHeadless {
		category = sessioncontract.SessionCategorySubagent
	}
	created, err := session.NewLazy(p.ContainerDir, containerName, p.Config.WorkspaceRoot, category, p.StoreOptions...)
	if err != nil {
		return nil, err
	}
	if parentSessionID != nil {
		parentID := strings.TrimSpace(*parentSessionID)
		if parentID == "" {
			return nil, errors.New("parent session id must not be empty")
		}
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
		return child.SetParentSessionID(&parentID)
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
