package worktree

import (
	"context"
	"sync"
	"testing"

	"core/shared/serverapi"
)

func TestSetupEventBrokerPublishCloseConcurrent(t *testing.T) {
	broker := newSetupEventBroker()
	setupID := serverapi.NewWorktreeSetupOperationID()
	const subscribers = 64
	subs := make([]serverapi.WorktreeSetupSubscription, 0, subscribers)
	for i := 0; i < subscribers; i++ {
		sub, err := broker.Subscribe(serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		subs = append(subs, sub)
	}
	evt := serverapi.WorktreeSetupEvent{
		SetupOperationID:    setupID,
		SourceWorkspaceRoot: "/source",
		WorktreeRoot:        "/worktree",
		ScriptPath:          "/source/setup.sh",
		Phase:               serverapi.WorktreeSetupPhaseStarted,
	}
	var wg sync.WaitGroup
	for _, sub := range subs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sub.Close()
		}()
	}
	for i := 0; i < subscribers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			broker.Publish(evt)
		}()
	}
	wg.Wait()
}

func TestSetupEventBrokerUsesTypedSetupOperationIDKey(t *testing.T) {
	broker := newSetupEventBroker()
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := broker.Subscribe(serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()
	broker.Publish(serverapi.WorktreeSetupEvent{
		SetupOperationID:    setupID,
		SourceWorkspaceRoot: "/source",
		WorktreeRoot:        "/worktree",
		ScriptPath:          "/source/setup.sh",
		Phase:               serverapi.WorktreeSetupPhaseStarted,
	})
	evt, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if evt.SetupOperationID != setupID {
		t.Fatalf("setup operation id = %s, want %s", evt.SetupOperationID, setupID)
	}
}
