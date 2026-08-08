package registry

import (
	"context"
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

func TestRuntimeRegistryRootAttentionDrainsLiveQuestionsWhileDurableSnapshotLoads(t *testing.T) {
	snapshotStarted := make(chan struct{})
	releaseSnapshot := make(chan struct{})
	source := workflowAttentionNotificationSnapshotSourceFunc(func(_ int) (WorkflowAttentionNotificationSnapshot, error) {
		return &blockingWorkflowAttentionNotificationSnapshot{
			started:      snapshotStarted,
			release:      releaseSnapshot,
			notification: registryWorkflowInterruptionNotification("snapshot-1", "task-snapshot", "node-snapshot"),
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

	type nextResult struct {
		event clientui.AttentionNotificationEvent
		err   error
	}
	results := make(chan nextResult, 66)
	go func() {
		for range 66 {
			event, err := sub.Next(context.Background())
			results <- nextResult{event: event, err: err}
			if err != nil {
				return
			}
		}
	}()
	<-snapshotStarted
	for index := range 65 {
		notification := registryWorkflowQuestionNotification(fmt.Sprintf("question-%d", index))
		if err := broker.PublishPending(attentionnotify.RoutingScope{Kind: attentionnotify.RoutingWorkflowTask, TaskID: notification.Target.TaskID}, notification); err != nil {
			t.Fatalf("PublishPending question %d: %v", index, err)
		}
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatalf("Next question %d while snapshot loads: %v", index, result.err)
			}
			if result.event.Source != clientui.AttentionNotificationSourceLive ||
				result.event.Pending == nil ||
				result.event.Pending.ID != notification.ID {
				t.Fatalf("live question %d = %+v", index, result.event)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("live question %d was starved by the durable snapshot", index)
		}
	}
	close(releaseSnapshot)
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatalf("Next snapshot after live questions: %v", result.err)
		}
		if result.event.Source != clientui.AttentionNotificationSourceSnapshot ||
			result.event.Pending == nil ||
			result.event.Pending.ID.UUID != "snapshot-1" {
			t.Fatalf("snapshot after live questions = %+v", result.event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("durable snapshot did not resume after live questions drained")
	}
}

func TestRuntimeRegistryRootAttentionBoundsLiveEventsAheadOfReadySnapshot(t *testing.T) {
	broker := attentionnotify.NewBroker()
	live, err := broker.SubscribeDesktop()
	if err != nil {
		t.Fatalf("SubscribeDesktop: %v", err)
	}
	source := workflowAttentionNotificationSnapshotSourceFunc(func(_ int) (WorkflowAttentionNotificationSnapshot, error) {
		return &workflowAttentionNotificationSnapshotSlice{notifications: []clientui.AttentionNotification{
			registryWorkflowInterruptionNotification("snapshot-1", "task-snapshot", "node-snapshot"),
		}}, nil
	})
	sub := newWorkflowAttentionNotificationSubscription(live, source).(*workflowAttentionNotificationSubscription)
	t.Cleanup(func() { _ = sub.Close() })
	sub.start.Do(sub.startWorkers)
	waitForRegistryAttentionCondition(t, func() bool { return len(sub.snapshotOut) == 1 }, "snapshot item was not ready")

	firstLive := registryWorkflowQuestionNotification("question-1")
	if err := broker.PublishPending(attentionnotify.RoutingScope{Kind: attentionnotify.RoutingWorkflowTask, TaskID: firstLive.Target.TaskID}, firstLive); err != nil {
		t.Fatalf("PublishPending first live: %v", err)
	}
	waitForRegistryAttentionCondition(t, func() bool { return len(sub.liveOut) == 1 }, "first live event was not ready")
	first := nextRegistryAttentionEvent(t, sub)
	if first.Source != clientui.AttentionNotificationSourceLive || first.Pending == nil || first.Pending.ID != firstLive.ID {
		t.Fatalf("first merged event = %+v, want one live event ahead of snapshot", first)
	}

	secondLive := registryWorkflowQuestionNotification("question-2")
	if err := broker.PublishPending(attentionnotify.RoutingScope{Kind: attentionnotify.RoutingWorkflowTask, TaskID: secondLive.Target.TaskID}, secondLive); err != nil {
		t.Fatalf("PublishPending second live: %v", err)
	}
	waitForRegistryAttentionCondition(t, func() bool { return len(sub.liveOut) == 1 }, "second live event was not ready")
	second := nextRegistryAttentionEvent(t, sub)
	if second.Source != clientui.AttentionNotificationSourceSnapshot || second.Pending == nil || second.Pending.ID.UUID != "snapshot-1" {
		t.Fatalf("second merged event = %+v, want ready snapshot after one unrelated live event", second)
	}
	third := nextRegistryAttentionEvent(t, sub)
	if third.Source != clientui.AttentionNotificationSourceLive || third.Pending == nil || third.Pending.ID != secondLive.ID {
		t.Fatalf("third merged event = %+v, want retained second live event", third)
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
