package workflowview

import (
	"errors"

	"core/shared/serverapi"
)

type attentionPageFetch func(attentionPageCursor, int) ([]attentionCandidateRow, error)

type attentionCandidateProject func(attentionCandidateRow) (serverapi.WorkflowAttentionItem, bool, error)

type filledAttentionPage struct {
	items        []serverapi.WorkflowAttentionItem
	continuation *attentionPageCursor
}

func fillAttentionPage(
	pageSize int,
	cursor attentionPageCursor,
	fetch attentionPageFetch,
	project attentionCandidateProject,
) (filledAttentionPage, error) {
	if pageSize <= 0 {
		return filledAttentionPage{}, errors.New("attention page size must be positive")
	}
	if fetch == nil {
		return filledAttentionPage{}, errors.New("attention candidate fetch is required")
	}
	if project == nil {
		return filledAttentionPage{}, errors.New("attention candidate projection is required")
	}

	items := make([]serverapi.WorkflowAttentionItem, 0, pageSize)
	current := cursor
	for len(items) < pageSize {
		candidates, err := fetch(current, pageSize+1)
		if err != nil {
			return filledAttentionPage{}, err
		}
		if len(candidates) == 0 {
			return filledAttentionPage{items: items}, nil
		}

		batch := candidates
		moreCandidates := len(batch) > pageSize
		if moreCandidates {
			batch = batch[:pageSize]
		}
		for _, candidate := range batch {
			item, include, err := project(candidate)
			if err != nil {
				return filledAttentionPage{}, err
			}
			if !include {
				continue
			}
			items = append(items, item)
			if len(items) == pageSize {
				continuation := attentionCandidateCursor(candidate)
				return filledAttentionPage{items: items, continuation: &continuation}, nil
			}
		}
		if !moreCandidates {
			return filledAttentionPage{items: items}, nil
		}
		current = attentionCandidateCursor(batch[len(batch)-1])
	}
	return filledAttentionPage{items: items}, nil
}
