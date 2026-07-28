package core

import (
	"context"
	"errors"
	"fmt"
	"io"

	"core/server/registry"
	"core/server/workflowattention"
	"core/server/workflowview"
	"core/shared/clientui"
)

type workflowAttentionNotificationSnapshotSource struct {
	attention *workflowview.Attention
	finalizer *workflowattention.Finalizer
}

func (s workflowAttentionNotificationSnapshotSource) OpenSnapshot(
	pageSize int,
) (registry.WorkflowAttentionNotificationSnapshot, error) {
	if s.attention == nil {
		return nil, errors.New("workflow attention read model is required")
	}
	if s.finalizer == nil {
		return nil, errors.New("workflow attention finalizer is required")
	}
	if pageSize <= 0 {
		return nil, errors.New("workflow attention notification snapshot page size must be positive")
	}
	return &workflowAttentionNotificationSnapshot{
		attention: s.attention,
		finalizer: s.finalizer,
		pageSize:  pageSize,
	}, nil
}

type workflowAttentionNotificationSnapshot struct {
	attention       *workflowview.Attention
	finalizer       *workflowattention.Finalizer
	pageSize        int
	cursor          *workflowview.DurableAttentionNotificationCursor
	references      []workflowview.DurableAttentionNotificationReference
	referenceIndex  int
	finalPageLoaded bool
}

func (s *workflowAttentionNotificationSnapshot) Next(
	ctx context.Context,
	enqueue func(clientui.AttentionNotification) error,
) error {
	if s == nil {
		return io.EOF
	}
	if enqueue == nil {
		return errors.New("workflow attention notification snapshot enqueue is required")
	}
	for {
		if s.referenceIndex < len(s.references) {
			reference := s.references[s.referenceIndex]
			s.referenceIndex++
			enqueued, err := s.enqueueReference(ctx, reference, enqueue)
			if err != nil {
				return err
			}
			if enqueued {
				return nil
			}
			continue
		}
		if s.finalPageLoaded {
			return io.EOF
		}
		page, err := s.attention.ListDurableNotificationReferences(ctx, s.cursor, s.pageSize)
		if err != nil {
			return err
		}
		s.references = page.References
		s.referenceIndex = 0
		s.cursor = page.Next
		s.finalPageLoaded = page.Next == nil
	}
}

func (s *workflowAttentionNotificationSnapshot) enqueueReference(
	ctx context.Context,
	reference workflowview.DurableAttentionNotificationReference,
	enqueue func(clientui.AttentionNotification) error,
) (bool, error) {
	switch typed := reference.(type) {
	case workflowview.DurableApprovalAttentionReference:
		return s.finalizer.EnqueuePendingApprovalSnapshot(ctx, typed.ApprovalID, enqueue)
	case workflowview.DurableInterruptedCurrentNodeAttentionReference:
		return s.finalizer.EnqueuePendingInterruptedCurrentNodeSnapshot(ctx, typed.CurrentNode, enqueue)
	default:
		return false, fmt.Errorf(
			"unsupported durable workflow attention notification reference %T",
			reference,
		)
	}
}

var _ registry.WorkflowAttentionNotificationSnapshotSource = workflowAttentionNotificationSnapshotSource{}
var _ registry.WorkflowAttentionNotificationSnapshot = (*workflowAttentionNotificationSnapshot)(nil)
