package runtime

import (
	"context"
	"errors"
	"strings"

	"core/prompts"
	"core/server/session"
	"core/shared/toolspec"
)

func (e *Engine) ensureMainPromptFacingContractFresh(ctx context.Context, locked session.LockedContract) (session.LockedContract, error) {
	if locked.HasSystemPrompt || strings.TrimSpace(locked.SystemPrompt) != "" {
		return locked, nil
	}
	next := locked
	next.ToolPreambles = e.promptRefreshToolPreambles(e.cfg.ToolPreambles)
	if e.cfg.ContextWindowTokens > 0 {
		next.ContextWindow = e.cfg.ContextWindowTokens
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
		SystemPromptFiles: e.cfg.SystemPromptFiles,
	}, e.lockedToolIDsFromConfigFallback())
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
	if commitErr := e.applyLockedContractMutationResult(result, err, e.lockedContractState().ApplyMainPromptSnapshot); commitErr != nil {
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
	path := strings.TrimSpace(e.cfg.Reviewer.SystemPromptFile)
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
	if commitErr := e.applyLockedContractMutationResult(result, err, e.lockedContractState().ApplyReviewerPromptSnapshot); commitErr != nil {
		return "", false, commitErr
	}
	if err != nil {
		return "", false, err
	}
	return prompt, true, nil
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

func (e *Engine) applyLockedContractMutationResult(result session.LockedContractMutationResult, err error, apply func(session.LockedContract)) error {
	if result.Committed && result.Locked != nil {
		apply(*result.Locked)
		return nil
	}
	return err
}
