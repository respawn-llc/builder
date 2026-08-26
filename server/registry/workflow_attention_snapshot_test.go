package registry

import (
	"context"
	"io"
	"testing"
	"time"

	testharness "core/internal/testharness/testsetup"
	"core/server/attentionnotify"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestRuntimeRegistryRootAttentionLazilyMergesDurableSnapshotAndBufferedLiveEvents(t *testing.T) {
	opened := false
	source := workflowAttentionNotificationSnapshotSourceFunc(func(pageSize int) (WorkflowAttentionNotificationSnapshot, error) {
		opened = true
		if pageSize != workflowAttentionNotificationSnapshotPageSize {
			t.Fatalf("snapshot page size = %d, want %d", pageSize, workflowAttentionNotificationSnapshotPageSize)
		}
		return &workflowAttentionNotificationSnapshotSlice{notifications: []clientui.AttentionNotification{
			registryWorkflowInterruptionNotification("snapshot-1", "task-1", "node-1"),
			registryWorkflowInterruptionNotification("snapshot-2", "task-2", "node-2"),
		}}, nil
	})
	broker := attentionnotify.NewBroker()
	registry := NewRuntimeRegistry().
		WithAttentionNotifications(broker).
		WithWorkflowAttentionNotificationSnapshot(source)

	sub, err := registry.SubscribeAttentionNotifications(context.Background(), serverapi.AttentionNotificationSubscribeRequest{})
	if err != nil {
		t.Fatalf("SubscribeAttentionNotifications: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	if opened {
		t.Fatal("root attention snapshot opened before the subscriber requested an event")
	}

	live := registryWorkflowInterruptionNotification("live-1", "task-3", "node-3")
	if err := broker.PublishPending(attentionnotify.RoutingScope{Kind: attentionnotify.RoutingWorkflowTask, TaskID: "task-3"}, live); err != nil {
		t.Fatalf("PublishPending live: %v", err)
	}

	seen := map[string]clientui.AttentionNotificationSource{}
	for index := range 3 {
		event := nextRegistryAttentionEvent(t, sub)
		if event.Sequence != uint64(index+1) || event.Pending == nil {
			t.Fatalf("root attention event %d = %+v", index, event)
		}
		seen[event.Pending.ID.UUID] = event.Source
	}
	if !opened {
		t.Fatal("root attention snapshot did not open on first Next")
	}
	if seen["snapshot-1"] != clientui.AttentionNotificationSourceSnapshot ||
		seen["snapshot-2"] != clientui.AttentionNotificationSourceSnapshot ||
		seen["live-1"] != clientui.AttentionNotificationSourceLive {
		t.Fatalf("merged root attention events = %+v", seen)
	}
}

type workflowAttentionNotificationSnapshotSourceFunc func(int) (WorkflowAttentionNotificationSnapshot, error)

func (f workflowAttentionNotificationSnapshotSourceFunc) OpenSnapshot(pageSize int) (WorkflowAttentionNotificationSnapshot, error) {
	return f(pageSize)
}

type workflowAttentionNotificationSnapshotSlice struct {
	notifications []clientui.AttentionNotification
	index         int
}

func (s *workflowAttentionNotificationSnapshotSlice) Next(_ context.Context, enqueue func(clientui.AttentionNotification) error) error {
	if s.index >= len(s.notifications) {
		return io.EOF
	}
	notification := s.notifications[s.index]
	s.index++
	return enqueue(notification)
}

func registryWorkflowInterruptionNotification(id string, taskID string, nodeID string) clientui.AttentionNotification {
	return clientui.AttentionNotification{
		ID:         clientui.AttentionNotificationID{Kind: clientui.AttentionNotificationKindInterruptedCurrentNode, UUID: id},
		Kind:       clientui.AttentionNotificationKindInterruptedCurrentNode,
		OccurredAt: time.Unix(1, 0).UTC(),
		Revision:   1,
		InterruptedCurrentNode: &clientui.AttentionNotificationInterruptedCurrentNodeState{
			Message: "Current Node interrupted",
		},
		Target: clientui.AttentionNotificationTarget{
			Kind:          clientui.AttentionNotificationTargetWorkflowTask,
			WorkflowID:    registryWorkflowID(),
			TaskID:        taskID,
			CurrentNodeID: &nodeID,
			Focus: &clientui.AttentionNotificationTaskDetailFocus{
				Kind: clientui.AttentionNotificationFocusInterruptedCurrentNode,
			},
		},
	}
}

func registryWorkflowQuestionNotification(id string) clientui.AttentionNotification {
	nodeID := "node-" + id
	return clientui.AttentionNotification{
		ID:         clientui.AttentionNotificationID{Kind: clientui.AttentionNotificationKindQuestion, UUID: id},
		Kind:       clientui.AttentionNotificationKindQuestion,
		OccurredAt: time.Unix(1, 0).UTC(),
		Revision:   1,
		Question: &clientui.AttentionNotificationQuestionState{
			PreparedAskIDs:          []string{id},
			MaterializedAskIDs:      []string{id},
			CurrentUnresolvedAskIDs: []string{id},
			DisplayCount:            1,
			MaterializedCount:       1,
		},
		Target: clientui.AttentionNotificationTarget{
			Kind:          clientui.AttentionNotificationTargetWorkflowTask,
			WorkflowID:    registryWorkflowID(),
			TaskID:        "task-" + id,
			CurrentNodeID: &nodeID,
			Focus: &clientui.AttentionNotificationTaskDetailFocus{
				Kind:   clientui.AttentionNotificationFocusQuestion,
				AskIDs: []string{id},
			},
		},
	}
}

func registryWorkflowID() *runtimeids.WorkflowID {
	workflowID := testharness.WorkflowIDValue("registry-workflow-attention")
	return &workflowID
}
