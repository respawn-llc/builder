package sessionlaunch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/auth"
	"core/server/launch"
	"core/server/metadata"
	"core/server/requestmemo"
	"core/server/session"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type WorkspaceChatDraft = metadata.WorkspaceChatDraftDocument
type WorkspaceChatDraftResolverInput struct {
	Settings                        config.Settings
	Source                          config.SourceReport
	AuthState                       auth.State
	SkipProviderReadinessValidation bool
}
type WorkspaceChatDraftTransform func(WorkspaceChatDraftResolution) (WorkspaceChatDraft, error)
type workspaceChatDraftLimits struct {
	draft           WorkspaceChatDraft
	settings        config.Settings
	fast, questions bool
	thinking        map[string]struct{}
}
type WorkspaceChatDraftResolution struct {
	Draft                    WorkspaceChatDraft
	Baselines                map[string]WorkspaceChatDraft
	GoalAvailability         session.GoalAvailability
	PersistedQuestionsPolicy bool
	PersistedThinking        string
	CompactionMode           config.CompactionMode
	Catalog                  launch.PreparedChatAgentCatalog
	limits                   map[string]workspaceChatDraftLimits
}
type workspaceChatDraftPersistence interface {
	ReadWorkspaceChatDraft(context.Context, string) (*WorkspaceChatDraft, error)
	ReplaceWorkspaceChatDraft(context.Context, string, *WorkspaceChatDraft) error
}
type WorkspaceChatDraftInputResolver func(context.Context) (WorkspaceChatDraftResolverInput, error)
type WorkspaceChatMaterializer func(context.Context, WorkspaceChatDraftResolution) (runtimeids.SessionID, error)
type WorkspaceChatDraftOwner struct {
	persistence workspaceChatDraftPersistence
	lanes       *requestmemo.MutationLaneRegistry[string]
}

func NewWorkspaceChatDraftOwner(p workspaceChatDraftPersistence) *WorkspaceChatDraftOwner {
	if p == nil {
		return nil
	}
	return &WorkspaceChatDraftOwner{persistence: p, lanes: requestmemo.NewMutationLaneRegistry[string]()}
}

func (o *WorkspaceChatDraftOwner) MaterializeWorkspaceChat(
	ctx context.Context,
	id string,
	resolve WorkspaceChatDraftInputResolver,
	materialize WorkspaceChatMaterializer,
) (runtimeids.SessionID, error) {
	var err error
	if id, err = o.workspaceID(id); err != nil {
		return runtimeids.SessionID{}, err
	}
	if resolve == nil {
		return runtimeids.SessionID{}, errors.New("workspace Chat draft resolver is required")
	}
	if materialize == nil {
		return runtimeids.SessionID{}, errors.New("workspace Chat materializer is required")
	}
	lane, err := o.lanes.Acquire(ctx, id)
	if err != nil {
		return runtimeids.SessionID{}, err
	}
	defer lane.Release()
	input, err := resolve(ctx)
	if err != nil {
		return runtimeids.SessionID{}, err
	}
	stored, err := o.persistence.ReadWorkspaceChatDraft(ctx, id)
	if err != nil {
		return runtimeids.SessionID{}, err
	}
	resolution, err := ResolveWorkspaceChatDraft(input, stored)
	if err != nil {
		return runtimeids.SessionID{}, err
	}
	resolution.Draft.Questions = resolution.PersistedQuestionsPolicy
	sessionID, err := materialize(ctx, resolution)
	if err != nil {
		return runtimeids.SessionID{}, err
	}
	if sessionID.IsZero() || !sessionID.IsCanonicalUUIDv4() {
		return runtimeids.SessionID{}, errors.New("materialized Session id must be a canonical UUIDv4")
	}
	return sessionID, nil
}

func (o *WorkspaceChatDraftOwner) workspaceID(id string) (string, error) {
	if o == nil || o.persistence == nil || o.lanes == nil {
		return "", errors.New("workspace Chat draft owner is required")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("workspace id is required")
	}
	return id, nil
}
func (o *WorkspaceChatDraftOwner) ResolveWorkspaceChatDraft(ctx context.Context, id string, resolve WorkspaceChatDraftInputResolver) (WorkspaceChatDraftResolution, error) {
	var err error
	if id, err = o.workspaceID(id); err != nil {
		return WorkspaceChatDraftResolution{}, err
	}
	if resolve == nil {
		return WorkspaceChatDraftResolution{}, errors.New("workspace Chat draft resolver is required")
	}
	input, err := resolve(ctx)
	if err != nil {
		return WorkspaceChatDraftResolution{}, err
	}
	stored, err := o.persistence.ReadWorkspaceChatDraft(ctx, id)
	if err != nil {
		return WorkspaceChatDraftResolution{}, err
	}
	return ResolveWorkspaceChatDraft(input, stored)
}
func (o *WorkspaceChatDraftOwner) TransformWorkspaceChatDraft(ctx context.Context, id string, resolve WorkspaceChatDraftInputResolver, transform WorkspaceChatDraftTransform) (WorkspaceChatDraft, error) {
	var err error
	if id, err = o.workspaceID(id); err != nil {
		return WorkspaceChatDraft{}, err
	}
	if resolve == nil {
		return WorkspaceChatDraft{}, errors.New("workspace Chat draft resolver is required")
	}
	if transform == nil {
		return WorkspaceChatDraft{}, errors.New("workspace Chat draft transform is required")
	}
	lane, err := o.lanes.Acquire(ctx, id)
	if err != nil {
		return WorkspaceChatDraft{}, err
	}
	defer lane.Release()
	input, err := resolve(ctx)
	if err != nil {
		return WorkspaceChatDraft{}, err
	}
	stored, err := o.persistence.ReadWorkspaceChatDraft(ctx, id)
	if err != nil {
		return WorkspaceChatDraft{}, err
	}
	current, err := ResolveWorkspaceChatDraft(input, stored)
	if err != nil {
		return WorkspaceChatDraft{}, err
	}
	current.Draft.Questions = current.PersistedQuestionsPolicy
	next, err := transform(current)
	if err != nil {
		return WorkspaceChatDraft{}, err
	}
	next.Agent = normalizeWorkspaceChatDraftAgent(next.Agent)
	if err := validateWorkspaceChatDraftTransform(next, current); err != nil {
		return WorkspaceChatDraft{}, err
	}
	replacement := canonicalWorkspaceChatDraft(next, current.Baselines[config.DefaultSubagentRole])
	if err := o.persistence.ReplaceWorkspaceChatDraft(ctx, id, replacement); err != nil {
		return WorkspaceChatDraft{}, err
	}
	return next, nil
}

func (o *WorkspaceChatDraftOwner) MutateWorkspaceChatSettings(
	ctx context.Context,
	id string,
	resolve WorkspaceChatDraftInputResolver,
	operation serverapi.ChatSettingsMutationOperation,
) (PreparedChatSettingsOperationResult, bool, error) {
	var err error
	if id, err = o.workspaceID(id); err != nil {
		return PreparedChatSettingsOperationResult{}, false, err
	}
	lane, err := o.lanes.Acquire(ctx, id)
	if err != nil {
		return PreparedChatSettingsOperationResult{}, false, err
	}
	defer lane.Release()
	input, err := resolve(ctx)
	if err != nil {
		return PreparedChatSettingsOperationResult{}, false, err
	}
	stored, err := o.persistence.ReadWorkspaceChatDraft(ctx, id)
	if err != nil {
		return PreparedChatSettingsOperationResult{}, false, err
	}
	resolved, err := ResolveWorkspaceChatDraft(input, stored)
	if err != nil {
		return PreparedChatSettingsOperationResult{}, false, err
	}
	raw := resolved.Baselines[config.DefaultSubagentRole]
	if stored != nil {
		raw = *stored
		raw.Agent = normalizeWorkspaceChatDraftAgent(raw.Agent)
	}
	rawState, err := session.ChatSettingsStateFromCompleteSettings(raw.Agent, session.ChatSettings{Supervisor: raw.Supervisor, Thinking: raw.Thinking, Fast: raw.Fast, Questions: raw.Questions, AutoCompaction: raw.AutoCompaction})
	if err != nil {
		return PreparedChatSettingsOperationResult{}, false, err
	}
	projected, err := ProjectPreparedChatSettingsOperation(PreparedChatSettingsOperationInput{
		Raw: rawState, Effective: session.ChatSettings{
			Supervisor: resolved.Draft.Supervisor, Thinking: resolved.Draft.Thinking, Fast: resolved.Draft.Fast,
			Questions: resolved.Draft.Questions, AutoCompaction: resolved.Draft.AutoCompaction,
		}, PersistedQuestions: resolved.PersistedQuestionsPolicy, PersistedThinking: resolved.PersistedThinking,
		Catalog: resolved.Catalog, CompactionMode: resolved.CompactionMode,
	}, operation)
	if err != nil {
		return PreparedChatSettingsOperationResult{}, false, err
	}
	if projected.Rejection != nil {
		return projected, false, nil
	}
	next, err := workspaceChatDraftFromSettingsState(raw.Message, projected.State)
	if err != nil {
		return PreparedChatSettingsOperationResult{}, false, err
	}
	replacement := canonicalWorkspaceChatDraft(next, resolved.Baselines[config.DefaultSubagentRole])
	if stored == nil && replacement == nil ||
		stored != nil && replacement != nil && *stored == *replacement {
		return projected, false, nil
	}
	if err := o.persistence.ReplaceWorkspaceChatDraft(ctx, id, replacement); err != nil {
		return PreparedChatSettingsOperationResult{}, false, err
	}
	return projected, true, nil
}

func (o *WorkspaceChatDraftOwner) ClearWorkspaceChatDraft(ctx context.Context, id string) error {
	var err error
	if id, err = o.workspaceID(id); err != nil {
		return err
	}
	lane, err := o.lanes.Acquire(ctx, id)
	if err != nil {
		return err
	}
	defer lane.Release()
	stored, err := o.persistence.ReadWorkspaceChatDraft(ctx, id)
	if err != nil || stored == nil {
		return err
	}
	return o.persistence.ReplaceWorkspaceChatDraft(ctx, id, nil)
}
func normalizeWorkspaceChatDraftAgent(agent string) string {
	agent = strings.TrimSpace(agent)
	if strings.EqualFold(agent, config.DefaultSubagentRole) {
		return config.DefaultSubagentRole
	}
	return config.NormalizeSubagentRole(agent)
}
func ResolveWorkspaceChatDraft(input WorkspaceChatDraftResolverInput, stored *WorkspaceChatDraft) (WorkspaceChatDraftResolution, error) {
	catalog, limits, err := resolveWorkspaceChatDraftBaselines(input)
	if err != nil {
		return WorkspaceChatDraftResolution{}, err
	}
	defaults, ok := limits[config.DefaultSubagentRole]
	if !ok {
		return WorkspaceChatDraftResolution{}, errors.New("default workspace Chat draft baseline is missing")
	}
	baselines := make(map[string]WorkspaceChatDraft, len(limits))
	for agent, limit := range limits {
		baselines[agent] = limit.draft
	}
	finish := func(draft WorkspaceChatDraft, questions bool, thinking string) (WorkspaceChatDraftResolution, error) {
		return workspaceChatDraftResolution(
			draft, questions, thinking, input.Settings.CompactionMode, baselines, catalog, limits,
		)
	}
	if stored == nil {
		return finish(defaults.draft, defaults.draft.Questions, defaults.draft.Thinking)
	}
	draft := *stored
	if err := draft.Validate(); err != nil {
		return WorkspaceChatDraftResolution{}, err
	}
	draft.Agent = normalizeWorkspaceChatDraftAgent(draft.Agent)
	persistedQuestions := draft.Questions
	persistedThinking := strings.TrimSpace(draft.Thinking)
	limit, ok := limits[draft.Agent]
	if !ok {
		message := draft.Message
		draft = defaults.draft
		draft.Message = message
		persistedQuestions = draft.Questions
		persistedThinking = draft.Thinking
	} else {
		if _, ok := limit.thinking[persistedThinking]; !ok {
			draft.Thinking = limit.draft.Thinking
		} else {
			draft.Thinking = persistedThinking
		}
		if draft.Fast && !limit.fast {
			draft.Fast = false
		}
		if draft.Questions && !limit.questions {
			draft.Questions = false
		}
	}
	return finish(draft, persistedQuestions, persistedThinking)
}

func workspaceChatDraftResolution(
	draft WorkspaceChatDraft,
	persistedQuestions bool,
	persistedThinking string,
	compactionMode config.CompactionMode,
	baselines map[string]WorkspaceChatDraft,
	catalog launch.PreparedChatAgentCatalog,
	limits map[string]workspaceChatDraftLimits,
) (WorkspaceChatDraftResolution, error) {
	limit, ok := limits[normalizeWorkspaceChatDraftAgent(draft.Agent)]
	if !ok {
		return WorkspaceChatDraftResolution{}, fmt.Errorf("workspace Chat draft Agent %q has no resolved capability", draft.Agent)
	}
	availability := session.GoalAgentCapabilityMissing
	if limit.questions {
		availability = session.GoalAvailable
	}
	return WorkspaceChatDraftResolution{
		Draft:                    draft,
		Baselines:                baselines,
		GoalAvailability:         availability,
		PersistedQuestionsPolicy: persistedQuestions,
		PersistedThinking:        persistedThinking,
		CompactionMode:           compactionMode,
		Catalog:                  catalog,
		limits:                   limits,
	}, nil
}
func resolveWorkspaceChatDraftBaselines(
	input WorkspaceChatDraftResolverInput,
) (
	launch.PreparedChatAgentCatalog,
	map[string]workspaceChatDraftLimits,
	error,
) {
	catalog, err := launch.PrepareChatAgentCatalog(
		config.App{Settings: input.Settings, Source: input.Source},
		input.AuthState,
		input.SkipProviderReadinessValidation,
	)
	if err != nil {
		return launch.PreparedChatAgentCatalog{}, nil, err
	}
	entries := catalog.Entries()
	result := make(map[string]workspaceChatDraftLimits, len(entries))
	for _, entry := range entries {
		selector := entry.Choice.Role
		preparedSettings := entry.Settings
		thinkingLevels := make(map[string]struct{}, len(preparedSettings.SupportedThinkingValues))
		for _, level := range preparedSettings.SupportedThinkingValues {
			thinkingLevels[level] = struct{}{}
		}
		baseline := preparedSettings.Baseline
		result[selector] = workspaceChatDraftLimits{
			draft: WorkspaceChatDraft{
				Agent:          selector,
				Supervisor:     baseline.Supervisor,
				Thinking:       baseline.Thinking,
				Fast:           baseline.Fast,
				Questions:      baseline.Questions,
				AutoCompaction: baseline.AutoCompaction,
			},
			settings:  entry.ResolvedSettings,
			fast:      preparedSettings.FastAvailable,
			questions: preparedSettings.QuestionsAvailable,
			thinking:  thinkingLevels,
		}
	}
	return catalog, result, nil
}
func workspaceChatDraftSettingsEqual(a, b WorkspaceChatDraft) bool {
	return a.Agent == b.Agent && a.Supervisor == b.Supervisor && a.Thinking == b.Thinking && a.Fast == b.Fast && a.Questions == b.Questions && a.AutoCompaction == b.AutoCompaction
}

func canonicalWorkspaceChatDraft(draft, defaults WorkspaceChatDraft) *WorkspaceChatDraft {
	if strings.TrimSpace(draft.Message) == "" && workspaceChatDraftSettingsEqual(draft, defaults) {
		return nil
	}
	return &draft
}

func workspaceChatDraftFromSettingsState(message string, state session.ChatSettingsState) (WorkspaceChatDraft, error) {
	settings, err := session.NormalizeChatSettingsOverrides(state.Settings)
	if err != nil {
		return WorkspaceChatDraft{}, err
	}
	if settings == nil ||
		settings.Supervisor == nil ||
		settings.Thinking == nil ||
		settings.Fast == nil ||
		settings.Questions == nil ||
		settings.AutoCompaction == nil {
		return WorkspaceChatDraft{}, errors.New("workspace Chat settings state is incomplete")
	}
	return WorkspaceChatDraft{
		Message:        message,
		Agent:          state.Agent,
		Supervisor:     *settings.Supervisor,
		Thinking:       *settings.Thinking,
		Fast:           *settings.Fast,
		Questions:      *settings.Questions,
		AutoCompaction: *settings.AutoCompaction,
	}, nil
}

func validateWorkspaceChatDraftTransform(draft WorkspaceChatDraft, resolved WorkspaceChatDraftResolution) error {
	if err := draft.Validate(); err != nil {
		return err
	}
	agent := normalizeWorkspaceChatDraftAgent(draft.Agent)
	limit, ok := resolved.limits[agent]
	if !ok {
		return fmt.Errorf("workspace Chat draft Agent %q is unavailable", draft.Agent)
	}
	if _, ok := limit.thinking[draft.Thinking]; !ok {
		return fmt.Errorf("workspace Chat draft thinking %q is unavailable for Agent %q", draft.Thinking, agent)
	}
	if draft.Fast && !limit.fast {
		return fmt.Errorf("workspace Chat draft fast mode is unavailable for Agent %q", agent)
	}
	if draft.Questions &&
		!limit.questions &&
		!(agent == normalizeWorkspaceChatDraftAgent(resolved.Draft.Agent) && resolved.PersistedQuestionsPolicy) {
		return fmt.Errorf("workspace Chat draft questions are unavailable for Agent %q", agent)
	}
	return nil
}
