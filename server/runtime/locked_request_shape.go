package runtime

import (
	"strings"

	"core/server/session"
	"core/shared/toolspec"
)

type lockedRequestShape struct {
	EnabledTools  []toolspec.ID
	WebSearchMode string
}

func (e *Engine) lockedRequestShape() (lockedRequestShape, error) {
	locked, ok := e.lockedContractState().Snapshot()
	if !ok {
		return lockedRequestShape{
			EnabledTools:  append([]toolspec.ID(nil), e.cfg.EnabledTools...),
			WebSearchMode: strings.TrimSpace(e.cfg.WebSearchMode),
		}, nil
	}
	ids := toolIDsFromNames(locked.EnabledTools)
	hasEnabled := locked.HasEnabledTools || len(ids) > 0
	webSearchMode := strings.TrimSpace(locked.WebSearchMode)
	if hasEnabled && webSearchMode != "" && locked.HasEnabledTools {
		return lockedRequestShape{EnabledTools: ids, WebSearchMode: webSearchMode}, nil
	}
	if hasEnabled && webSearchMode != "" && !locked.HasEnabledTools {
		result, err := e.store.BackfillLockedRequestShape(session.LockedRequestShapeBackfill{
			EnabledTools:    toolspec.IDStrings(ids),
			HasEnabledTools: true,
			WebSearchMode:   webSearchMode,
		})
		if commitErr := e.applyLockedContractMutationResult(result, err); commitErr != nil {
			return lockedRequestShape{}, commitErr
		}
		if result.Committed && result.Locked != nil {
			ids = toolIDsFromNames(result.Locked.EnabledTools)
			webSearchMode = strings.TrimSpace(result.Locked.WebSearchMode)
		}
		return lockedRequestShape{EnabledTools: ids, WebSearchMode: webSearchMode}, nil
	}
	fallbackIDs := append([]toolspec.ID(nil), e.cfg.EnabledTools...)
	if hasEnabled {
		fallbackIDs = ids
	}
	if webSearchMode == "" {
		webSearchMode = strings.TrimSpace(e.cfg.WebSearchMode)
	}
	result, err := e.store.BackfillLockedRequestShape(session.LockedRequestShapeBackfill{
		EnabledTools:    toolspec.IDStrings(fallbackIDs),
		HasEnabledTools: true,
		WebSearchMode:   webSearchMode,
	})
	if commitErr := e.applyLockedContractMutationResult(result, err); commitErr != nil {
		return lockedRequestShape{}, commitErr
	}
	if result.Committed && result.Locked != nil {
		fallbackIDs = toolIDsFromNames(result.Locked.EnabledTools)
		webSearchMode = strings.TrimSpace(result.Locked.WebSearchMode)
	}
	return lockedRequestShape{EnabledTools: fallbackIDs, WebSearchMode: webSearchMode}, nil
}

func toolIDsFromNames(names []string) []toolspec.ID {
	out := make([]toolspec.ID, 0, len(names))
	for _, raw := range names {
		if id, ok := toolspec.ParseID(raw); ok {
			out = append(out, id)
		}
	}
	return out
}
