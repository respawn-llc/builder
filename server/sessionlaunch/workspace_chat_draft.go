package sessionlaunch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"core/server/auth"
	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

type WorkspaceChatDraft = metadata.WorkspaceChatDraftDocument
type WorkspaceChatDraftResolverInput struct {
	Settings      config.Settings
	Source        config.SourceReport
	AuthState     auth.State
	FastModeState *runtime.FastModeState
}
type WorkspaceChatDraftTransform func(WorkspaceChatDraftResolution) (WorkspaceChatDraft, error)
type workspaceChatDraftLimits struct {
	draft           WorkspaceChatDraft
	fast, questions bool
	thinking        map[string]struct{}
}
type WorkspaceChatDraftResolution struct {
	Draft     WorkspaceChatDraft
	Baselines map[string]WorkspaceChatDraft
	limits    map[string]workspaceChatDraftLimits
}
type workspaceChatDraftPersistence interface {
	ReadWorkspaceChatDraft(context.Context, string) (*WorkspaceChatDraft, error)
	ReplaceWorkspaceChatDraft(context.Context, string, *WorkspaceChatDraft) error
}
type WorkspaceChatDraftInputResolver func(context.Context) (WorkspaceChatDraftResolverInput, error)
type WorkspaceChatDraftOwner struct {
	persistence workspaceChatDraftPersistence
	lanes       *metadata.MutationLaneRegistry[string]
}

func NewWorkspaceChatDraftOwner(p workspaceChatDraftPersistence) *WorkspaceChatDraftOwner {
	if p == nil {
		return nil
	}
	return &WorkspaceChatDraftOwner{persistence: p, lanes: metadata.NewMutationLaneRegistry[string]()}
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
	next, err := transform(current)
	if err != nil {
		return WorkspaceChatDraft{}, err
	}
	next.Agent = normalizeWorkspaceChatDraftAgent(next.Agent)
	if err := validateWorkspaceChatDraftTransform(next, current); err != nil {
		return WorkspaceChatDraft{}, err
	}
	var replacement *WorkspaceChatDraft
	defaults := current.Baselines[config.DefaultSubagentRole]
	if strings.TrimSpace(next.Message) != "" || !workspaceChatDraftSettingsEqual(next, defaults) {
		replacement = &next
	}
	if err := o.persistence.ReplaceWorkspaceChatDraft(ctx, id, replacement); err != nil {
		return WorkspaceChatDraft{}, err
	}
	return next, nil
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
	limits, err := resolveWorkspaceChatDraftBaselines(input)
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
	if stored == nil {
		return WorkspaceChatDraftResolution{Draft: defaults.draft, Baselines: baselines, limits: limits}, nil
	}
	draft := *stored
	if err := draft.Validate(); err != nil {
		return WorkspaceChatDraftResolution{}, err
	}
	draft.Agent = normalizeWorkspaceChatDraftAgent(draft.Agent)
	limit, ok := limits[draft.Agent]
	if !ok {
		message := draft.Message
		draft = defaults.draft
		draft.Message = message
	} else {
		if _, ok := limit.thinking[draft.Thinking]; !ok {
			draft.Thinking = limit.draft.Thinking
		}
		if draft.Fast && !limit.fast {
			draft.Fast = false
		}
		if draft.Questions && !limit.questions {
			draft.Questions = false
		}
	}
	return WorkspaceChatDraftResolution{Draft: draft, Baselines: baselines, limits: limits}, nil
}
func resolveWorkspaceChatDraftBaselines(input WorkspaceChatDraftResolverInput) (map[string]workspaceChatDraftLimits, error) {
	selectors := append([]string{config.DefaultSubagentRole}, config.AvailableSubagentRoleNames(input.Settings, false)...)
	result := make(map[string]workspaceChatDraftLimits, len(selectors))
	for _, selector := range selectors {
		selector = normalizeWorkspaceChatDraftAgent(selector)
		if selector == "" {
			return nil, errors.New("workspace Chat draft selector is invalid")
		}
		if _, exists := result[selector]; exists {
			continue
		}
		role := selector
		prepared, err := launch.PrepareRunPromptOverridesWithContext(config.App{Settings: input.Settings, Source: input.Source}, serverapi.RunPromptOverrides{AgentRole: &role}, input.AuthState, launch.RunPromptPreparationContext{})
		if err != nil {
			return nil, err
		}
		target := prepared.BaseTarget
		if selector != config.DefaultSubagentRole {
			target = nil
			if prepared.NamedTarget != nil {
				target = &launch.PreparedBaseTarget{Settings: prepared.NamedTarget.Settings, Source: prepared.NamedTarget.Source, EnabledTools: prepared.NamedTarget.EnabledTools}
			}
		}
		if target == nil {
			return nil, fmt.Errorf("prepare workspace Chat draft Agent %q returned no target", selector)
		}
		capabilities, err := llm.ProviderCapabilitiesForSettings(input.AuthState, target.Settings)
		if err != nil {
			return nil, err
		}
		supervisor, valid := runtime.NormalizeReviewerFrequency(target.Settings.Reviewer.Frequency)
		if !valid || strings.TrimSpace(target.Settings.ThinkingLevel) == "" {
			return nil, errors.New("workspace Chat draft settings are invalid")
		}
		thinking := strings.TrimSpace(target.Settings.ThinkingLevel)
		thinkingLevels := make(map[string]struct{})
		for _, level := range append(llm.SupportedThinkingLevelsModel(target.Settings.Model), thinking) {
			thinkingLevels[level] = struct{}{}
		}
		questions, fast := slices.Contains(target.EnabledTools, toolspec.ToolAskQuestion), llm.SupportsFastModeProvider(capabilities)
		result[selector] = workspaceChatDraftLimits{draft: WorkspaceChatDraft{Agent: selector, Supervisor: supervisor, Thinking: thinking, Fast: input.FastModeState != nil && input.FastModeState.Enabled() && fast, Questions: runtime.DefaultQuestionsEnabled && questions, AutoCompaction: runtime.DefaultAutoCompactionEnabled}, fast: fast, questions: questions, thinking: thinkingLevels}
	}
	return result, nil
}
func workspaceChatDraftSettingsEqual(a, b WorkspaceChatDraft) bool {
	return a.Agent == b.Agent && a.Supervisor == b.Supervisor && a.Thinking == b.Thinking && a.Fast == b.Fast && a.Questions == b.Questions && a.AutoCompaction == b.AutoCompaction
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
	if draft.Questions && !limit.questions {
		return fmt.Errorf("workspace Chat draft questions are unavailable for Agent %q", agent)
	}
	return nil
}
