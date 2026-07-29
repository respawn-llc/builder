package workflowview

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

type DurableAttentionNotificationReference interface {
	durableAttentionNotificationReference()
}

type DurableApprovalAttentionReference struct {
	ApprovalID workflow.ApprovalID
}

func (DurableApprovalAttentionReference) durableAttentionNotificationReference() {}

type DurableInterruptedCurrentNodeAttentionReference struct {
	CurrentNode workflow.CurrentNodeReference
}

func (DurableInterruptedCurrentNodeAttentionReference) durableAttentionNotificationReference() {}

type DurableAttentionNotificationCursor struct {
	cursor attentionPageCursor
}

type DurableAttentionNotificationReferencePage struct {
	References []DurableAttentionNotificationReference
	Next       *DurableAttentionNotificationCursor
}

func (a *Attention) ListDurableNotificationReferences(
	ctx context.Context,
	cursor *DurableAttentionNotificationCursor,
	pageSize int,
) (DurableAttentionNotificationReferencePage, error) {
	if a == nil {
		return DurableAttentionNotificationReferencePage{}, errors.New("attention read model is required")
	}
	if pageSize <= 0 {
		return DurableAttentionNotificationReferencePage{}, errors.New("durable attention notification page size must be positive")
	}
	pageCursor := attentionPageCursor{}
	if cursor != nil {
		pageCursor = cursor.cursor
	}
	rows, err := a.durableCandidateRows(ctx, pageCursor, nil, pageSize+1)
	if err != nil {
		return DurableAttentionNotificationReferencePage{}, err
	}
	hasNext := len(rows) > pageSize
	if hasNext {
		rows = rows[:pageSize]
	}
	references := make([]DurableAttentionNotificationReference, 0, len(rows))
	for _, row := range rows {
		reference, err := durableAttentionNotificationReference(row)
		if err != nil {
			return DurableAttentionNotificationReferencePage{}, err
		}
		references = append(references, reference)
	}
	page := DurableAttentionNotificationReferencePage{References: references}
	if hasNext {
		last := rows[len(rows)-1]
		page.Next = &DurableAttentionNotificationCursor{cursor: attentionPageCursor{
			occurredAtUnixMs: last.OccurredAtUnixMs,
			itemID:           last.ID,
			hasValue:         true,
		}}
	}
	return page, nil
}

func durableAttentionNotificationReference(row sqlitegen.ListWorkflowDurableAttentionCandidatesRow) (DurableAttentionNotificationReference, error) {
	switch row.Kind {
	case "approval":
		if !row.ApprovalID.Valid {
			return nil, fmt.Errorf("approval attention candidate %q has no approval id", row.ID)
		}
		approvalID, err := workflow.ParseApprovalID(strings.TrimSpace(row.ApprovalID.String))
		if err != nil {
			return nil, fmt.Errorf("approval attention candidate %q has invalid approval id: %w", row.ID, err)
		}
		return DurableApprovalAttentionReference{ApprovalID: approvalID}, nil
	case "interrupted":
		currentNode, err := currentNodeReferenceFromAttentionCandidate(row)
		if err != nil {
			return nil, err
		}
		return DurableInterruptedCurrentNodeAttentionReference{CurrentNode: currentNode}, nil
	default:
		return nil, fmt.Errorf("workflow durable attention candidate %q has invalid kind %q", row.ID, row.Kind)
	}
}
