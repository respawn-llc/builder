package runtime

import (
	"context"
	"errors"
	"strings"

	"core/prompts"
	"core/server/session"
	"core/shared/config"
	"core/shared/toolspec"
)

type PromptFacingSnapshotReloader interface {
	ReloadPromptFacingSnapshotConfig(ctx context.Context, sessionID string) (PromptFacingSnapshotConfig, error)
}

type PromptFacingSnapshotConfig struct {
	Settings      config.Settings
	Source        config.SourceReport
	ActiveToolIDs []toolspec.ID
	WebSearchMode string
}

func (e *Engine) ensureMainPromptFacingContractFresh(ctx context.Context, locked session.LockedContract) (session.LockedContract, error) {
	if locked.HasSystemPrompt || strings.TrimSpace(locked.SystemPrompt) != "" {
		return locked, nil
	}
	reloaded, err := e.reloadPromptFacingSnapshotConfig(ctx)
	if err != nil {
		return session.LockedContract{}, err
	}
	next := locked
	next.ToolPreambles = e.promptRefreshToolPreambles(reloaded.Settings.ToolPreambles)
	if reloaded.Settings.ModelContextWindow > 0 {
		next.ContextWindow = reloaded.Settings.ModelContextWindow
	}
	if next.ContextPercent <= 0 {
		next.ContextPercent = e.cfg.EffectiveContextWindowPercent
	}
	if next.ContextWindow <= 0 {
		next.ContextWindow = e.cfg.ContextWindowTokens
	}
	prompt, err := e.buildSystemPromptSnapshotFromConfig(next, e.systemPromptWorkspaceRoot(), systemPromptSnapshotOptions{
		WorkspaceRoot:     e.systemPromptWorkspaceRoot(),
		GlobalConfigDir:   e.cfg.GlobalConfigDir,
		SystemPromptFiles: reloaded.Settings.SystemPromptFiles,
	}, reloaded.ActiveToolIDs)
	if err != nil {
		return session.LockedContract{}, err
	}
	result, err := e.store.RefreshLockedMainPromptSnapshot(session.LockedMainPromptSnapshot{
		SystemPrompt:    prompt,
		HasSystemPrompt: true,
		ToolPreambles:   next.ToolPreambles,
		ContextWindow:   next.ContextWindow,
		ContextPercent:  next.ContextPercent,
	})
	if commitErr := e.applyLockedContractMutationResult(result, err); commitErr != nil {
		return session.LockedContract{}, commitErr
	}
	if result.Committed && result.Locked != nil {
		return *result.Locked, nil
	}
	if err != nil {
		return session.LockedContract{}, err
	}
	return session.LockedContract{}, errors.New("locked main prompt snapshot refresh did not commit")
}

func (e *Engine) ensureReviewerPromptFresh(ctx context.Context) (string, bool, error) {
	if prompt, ok := e.lockedReviewerPromptSnapshot(); ok || strings.TrimSpace(prompt) != "" {
		return "", false, nil
	}
	reloaded, err := e.reloadPromptFacingSnapshotConfig(ctx)
	if err != nil {
		return "", false, err
	}
	path := strings.TrimSpace(reloaded.Settings.Reviewer.SystemPromptFile)
	if path == "" {
		return prompts.ReviewerSystemPrompt, true, nil
	}
	prompt, err := buildReviewerPromptSnapshotFromFile(path)
	if err != nil {
		return "", false, err
	}
	result, err := e.store.RefreshLockedReviewerPromptSnapshot(session.LockedReviewerPromptSnapshot{
		ReviewerPrompt:    prompt,
		HasReviewerPrompt: true,
	})
	if commitErr := e.applyLockedContractMutationResult(result, err); commitErr != nil {
		return "", false, commitErr
	}
	if err != nil {
		return "", false, err
	}
	return prompt, true, nil
}

func (e *Engine) reloadPromptFacingSnapshotConfig(ctx context.Context) (PromptFacingSnapshotConfig, error) {
	if e.cfg.PromptFacingSnapshotReloader != nil {
		return e.cfg.PromptFacingSnapshotReloader.ReloadPromptFacingSnapshotConfig(ctx, e.SessionID())
	}
	return PromptFacingSnapshotConfig{
		Settings: config.Settings{
			SystemPromptFiles:  e.cfg.SystemPromptFiles,
			ToolPreambles:      e.cfg.ToolPreambles,
			ModelContextWindow: e.cfg.ContextWindowTokens,
			Reviewer: config.ReviewerSettings{
				SystemPromptFile: e.cfg.Reviewer.SystemPromptFile,
			},
		},
		ActiveToolIDs: e.lockedToolIDsFromConfigFallback(),
		WebSearchMode: e.cfg.WebSearchMode,
	}, nil
}

func (e *Engine) reconstructionSkillPolicy(ctx context.Context) (config.SkillPolicy, error) {
	if e.cfg.PromptFacingSnapshotReloader == nil {
		return e.cfg.SkillPolicy, nil
	}
	reloaded, err := e.reloadPromptFacingSnapshotConfig(ctx)
	if err != nil {
		return config.SkillPolicy{}, err
	}
	return config.ResolveSkillPolicy(reloaded.Settings), nil
}

func (e *Engine) promptRefreshToolPreambles(enabled bool) *bool {
	value := !e.cfg.HeadlessMode && enabled
	return &value
}

func (e *Engine) lockedToolIDsFromConfigFallback() []toolspec.ID {
	if locked, ok := e.lockedContractState().Snapshot(); ok {
		ids := toolIDsFromNames(locked.EnabledTools)
		if locked.HasEnabledTools || len(ids) > 0 {
			return ids
		}
	}
	return append([]toolspec.ID(nil), e.cfg.EnabledTools...)
}

func (e *Engine) applyLockedContractMutationResult(result session.LockedContractMutationResult, err error) error {
	if result.Committed && result.Locked != nil {
		e.lockedContractState().Set(*result.Locked)
		return nil
	}
	return err
}
