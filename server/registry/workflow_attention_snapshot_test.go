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
	notifications := make([]clientui.AttentionNotification, 0, workflowAttentionNotificationSnapshotPageSize*2+1)
	for index := range workflowAttentionNotificationSnapshotPageSize*2 + 1 {
		notifications = append(notifications, registryWorkflowInterruptionNotification(
			fmt.Sprintf("snapshot-%d", index),
			fmt.Sprintf("task-%d", index),
			fmt.Sprintf("node-%d", index),
		))
	}
	source := workflowAttentionNotificationSnapshotSourceFunc(func(pageSize int) (WorkflowAttentionNotificationSnapshot, error) {
		if pageSize != workflowAttentionNotificationSnapshotPageSize {
			t.Fatalf("snapshot page size = %d, want %d", pageSize, workflowAttentionNotificationSnapshotPageSize)
		}
		return &workflowAttentionNotificationSnapshotSlice{notifications: notifications}, nil
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

	seen := make(map[string]struct{}, len(notifications))
	for index := range notifications {
		event := nextRegistryAttentionEvent(t, sub)
		if event.Source != clientui.AttentionNotificationSourceSnapshot || event.Pending == nil {
			t.Fatalf("durable root attention event %d = %+v", index, event)
		}
		seen[event.Pending.ID.UUID] = struct{}{}
	}
	for _, notification := range notifications {
		if _, ok := seen[notification.ID.UUID]; !ok {
			t.Fatalf("durable root attention snapshot omitted %q", notification.ID.UUID)
		}
	}
}

func TestRuntimeRegistryRootAttentionAllowsSnapshotLiveOverlapWhilePageIsBlocked(t *testing.T) {
	pageBlocked := make(chan struct{})
	releasePage := make(chan struct{})
	overlappingID := "overlap-1"
	source := workflowAttentionNotificationSnapshotSourceFunc(func(_ int) (WorkflowAttentionNotificationSnapshot, error) {
		return &workflowAttentionSnapshotBlockedBetweenItems{
			blocked: pageBlocked,
			release: releasePage,
			notifications: []clientui.AttentionNotification{
				registryWorkflowInterruptionNotification("snapshot-1", "task-1", "node-1"),
				registryWorkflowQuestionNotification(overlappingID),
			},
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

	first := nextRegistryAttentionEvent(t, sub)
	if first.Source != clientui.AttentionNotificationSourceSnapshot ||
		first.Pending == nil ||
		first.Pending.ID.UUID != "snapshot-1" {
		t.Fatalf("first durable root attention item = %+v", first)
	}

	type nextResult struct {
		event clientui.AttentionNotificationEvent
		err   error
	}
	results := make(chan nextResult, 1)
	go func() {
		event, err := sub.Next(context.Background())
		results <- nextResult{event: event, err: err}
	}()
	<-pageBlocked
	live := registryWorkflowQuestionNotification(overlappingID)
	if err := broker.PublishPending(attentionnotify.RoutingScope{Kind: attentionnotify.RoutingWorkflowTask, TaskID: live.Target.TaskID}, live); err != nil {
		t.Fatalf("PublishPending overlapping live item: %v", err)
	}
	close(releasePage)

	seenSources := map[clientui.AttentionNotificationSource]int{}
	var firstOverlappingResult nextResult
	select {
	case firstOverlappingResult = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("root attention did not resume after the blocked snapshot page")
	}
	pendingResult := &firstOverlappingResult
	for len(seenSources) < 2 {
		var result nextResult
		if pendingResult != nil {
			result = *pendingResult
			pendingResult = nil
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			event, err := sub.Next(ctx)
			cancel()
			result = nextResult{event: event, err: err}
		}
		if result.err != nil {
			if errors.Is(result.err, serverapi.ErrStreamGap) {
				return
			}
			t.Fatalf("Next overlapping root attention item: %v", result.err)
		}
		if result.event.Source != clientui.AttentionNotificationSourceSnapshot &&
			result.event.Source != clientui.AttentionNotificationSourceLive {
			t.Fatalf("overlapping root attention source = %q", result.event.Source)
		}
		if result.event.Pending == nil ||
			result.event.Pending.ID.UUID != overlappingID {
			t.Fatalf("overlapping root attention item = %+v", result.event)
		}
		seenSources[result.event.Source]++
	}
}

func TestRuntimeRegistryRootAttentionSurfacesLiveBrokerOverflowAfterHydration(t *testing.T) {
	snapshotStarted := make(chan struct{})
	releaseSnapshot := make(chan struct{})
	source := workflowAttentionNotificationSnapshotSourceFunc(func(_ int) (WorkflowAttentionNotificationSnapshot, error) {
		return &blockingWorkflowAttentionNotificationSnapshot{
			started:      snapshotStarted,
			release:      releaseSnapshot,
			notification: registryWorkflowInterruptionNotification("snapshot-1", "task-snapshot", "node-snapshot"),
		}, nil
	})
	broker := attentionnotify.NewBroker(attentionnotify.WithBufferSize(1))
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
	firstResult := make(chan nextResult, 1)
	go func() {
		event, err := sub.Next(context.Background())
		firstResult <- nextResult{event: event, err: err}
	}()
	<-snapshotStarted
	for index := range 128 {
		notification := registryWorkflowQuestionNotification(fmt.Sprintf("overflow-%d", index))
		if err := broker.PublishPending(attentionnotify.RoutingScope{Kind: attentionnotify.RoutingWorkflowTask, TaskID: notification.Target.TaskID}, notification); err != nil {
			t.Fatalf("PublishPending overflow item %d: %v", index, err)
		}
	}
	close(releaseSnapshot)

	var result nextResult
	select {
	case result = <-firstResult:
	case <-time.After(5 * time.Second):
		t.Fatal("root attention did not resume after overflowed hydration")
	}
	for {
		if errors.Is(result.err, serverapi.ErrStreamGap) {
			return
		}
		if result.err != nil {
			t.Fatalf("root attention overflow error = %v, want ErrStreamGap", result.err)
		}
		if result.event.Source == clientui.AttentionNotificationSourceSnapshot {
			if result.event.Pending == nil || result.event.Pending.ID.UUID != "snapshot-1" {
				t.Fatalf("durable item during overflow = %+v", result.event)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		event, err := sub.Next(ctx)
		cancel()
		result = nextResult{event: event, err: err}
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

type workflowAttentionSnapshotBlockedBetweenItems struct {
	blocked       chan<- struct{}
	release       <-chan struct{}
	notifications []clientui.AttentionNotification
	index         int
}

func (s *workflowAttentionSnapshotBlockedBetweenItems) Next(ctx context.Context, enqueue func(clientui.AttentionNotification) error) error {
	if s.index >= len(s.notifications) {
		return io.EOF
	}
	if s.index == 1 {
		close(s.blocked)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.release:
		}
	}
	notification := s.notifications[s.index]
	s.index++
	return enqueue(notification)
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
