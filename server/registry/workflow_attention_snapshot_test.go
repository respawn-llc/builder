package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	testharness "core/internal/testharness/testsetup"
	"core/server/attentionnotify"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestRuntimeRegistryRootAttentionDeliversEveryDurableSnapshotPage(t *testing.T) {
	notifications := make([]clientui.AttentionNotification, workflowAttentionNotificationSnapshotPageSize*2+1)
	for index := range notifications {
		notifications[index] = registryWorkflowInterruptionNotification(
			fmt.Sprintf("snapshot-%d", index), fmt.Sprintf("task-%d", index), fmt.Sprintf("node-%d", index),
		)
	}
	source := workflowAttentionNotificationSnapshotSourceFunc(func(pageSize int) (WorkflowAttentionNotificationSnapshot, error) {
		if pageSize != workflowAttentionNotificationSnapshotPageSize {
			t.Fatalf("snapshot page size = %d, want %d", pageSize, workflowAttentionNotificationSnapshotPageSize)
		}
		return &workflowAttentionNotificationSnapshotSlice{notifications: notifications}, nil
	})
	registry := NewRuntimeRegistry().
		WithAttentionNotifications(attentionnotify.NewBroker()).
		WithWorkflowAttentionNotificationSnapshot(source)
	sub, err := registry.SubscribeAttentionNotifications(context.Background(), serverapi.AttentionNotificationSubscribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	for index := range notifications {
		event := nextRegistryAttentionEvent(t, sub)
		if event.Source != clientui.AttentionNotificationSourceSnapshot || event.Pending == nil ||
			event.Pending.ID != notifications[index].ID {
			t.Fatalf("durable event %d = %+v", index, event)
		}
	}
}

func TestRuntimeRegistryRootAttentionAllowsSnapshotLiveOverlapWhilePageIsBlocked(t *testing.T) {
	snapshotStarted := make(chan struct{})
	releaseSnapshot := make(chan struct{})
	overlap := registryWorkflowQuestionNotification("overlap")
	source := workflowAttentionNotificationSnapshotSourceFunc(func(_ int) (WorkflowAttentionNotificationSnapshot, error) {
		return &blockingWorkflowAttentionNotificationSnapshot{
			started:      snapshotStarted,
			release:      releaseSnapshot,
			notification: overlap,
		}, nil
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

	results := make(chan clientui.AttentionNotificationEvent, 1)
	go func() {
		event, _ := sub.Next(context.Background())
		results <- event
	}()
	<-snapshotStarted
	if err := broker.PublishPending(attentionnotify.RoutingScope{Kind: attentionnotify.RoutingWorkflowTask, TaskID: overlap.Target.TaskID}, overlap); err != nil {
		t.Fatal(err)
	}
	close(releaseSnapshot)
	seen := map[clientui.AttentionNotificationSource]bool{}
	for _, event := range []clientui.AttentionNotificationEvent{<-results, nextRegistryAttentionEvent(t, sub)} {
		if event.Pending == nil || event.Pending.ID != overlap.ID {
			t.Fatalf("overlapping event = %+v", event)
		}
		seen[event.Source] = true
	}
	if !seen[clientui.AttentionNotificationSourceSnapshot] || !seen[clientui.AttentionNotificationSourceLive] {
		t.Fatalf("overlapping sources = %+v", seen)
	}
}

func TestRuntimeRegistryRootAttentionSurfacesLiveBrokerOverflowAfterHydration(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	source := workflowAttentionNotificationSnapshotSourceFunc(func(_ int) (WorkflowAttentionNotificationSnapshot, error) {
		return &blockingWorkflowAttentionNotificationSnapshot{
			started: started, release: release,
			notification: registryWorkflowInterruptionNotification("snapshot", "task", "node"),
		}, nil
	})
	broker := attentionnotify.NewBroker(attentionnotify.WithBufferSize(1))
	registry := NewRuntimeRegistry().WithAttentionNotifications(broker).WithWorkflowAttentionNotificationSnapshot(source)
	sub, err := registry.SubscribeAttentionNotifications(context.Background(), serverapi.AttentionNotificationSubscribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	result := make(chan error, 1)
	go func() {
		for {
			if _, err := sub.Next(context.Background()); err != nil {
				result <- err
				return
			}
		}
	}()
	<-started
	for index := range 128 {
		notification := registryWorkflowQuestionNotification(fmt.Sprintf("overflow-%d", index))
		if err := broker.PublishPending(attentionnotify.RoutingScope{Kind: attentionnotify.RoutingWorkflowTask, TaskID: notification.Target.TaskID}, notification); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	if err := <-result; !errors.Is(err, serverapi.ErrStreamGap) {
		t.Fatalf("overflow error = %v, want ErrStreamGap", err)
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

type blockingWorkflowAttentionNotificationSnapshot struct {
	started      chan<- struct{}
	release      <-chan struct{}
	notification clientui.AttentionNotification
	emitted      bool
}

func (s *blockingWorkflowAttentionNotificationSnapshot) Next(ctx context.Context, enqueue func(clientui.AttentionNotification) error) error {
	if s.emitted {
		return io.EOF
	}
	close(s.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
	}
	s.emitted = true
	return enqueue(s.notification)
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

func waitForRegistryAttentionCondition(t *testing.T, condition func() bool, failure string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal(failure)
		}
		time.Sleep(time.Millisecond)
	}
}
