package worktree

import (
	"context"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"
	"sync"
	"testing"
)

func TestSetupEventBrokerPublishCloseConcurrent(t *testing.T) {
	broker := newSetupEventBroker()
	setupID := worktreecontract.NewSetupOperationID()
	const subscribers = 64
	subs := make([]SetupSubscription, 0, subscribers)
	for i := 0; i < subscribers; i++ {
		sub, err := broker.Subscribe(&worktreepb.SetupSubscribeRequest{SetupOperationId: setupID.String()})
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		subs = append(subs, sub)
	}
	evt := &worktreepb.SetupEvent{
		SetupOperationId: setupID.String(),
		Phase: &worktreepb.SetupEvent_Started{Started: &worktreepb.SetupStarted{
			SourceWorkspaceRoot: "/source",
			WorktreeRoot:        "/worktree",
			ScriptPath:          "/source/setup.sh",
		}},
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
	setupID := worktreecontract.NewSetupOperationID()
	sub, err := broker.Subscribe(&worktreepb.SetupSubscribeRequest{SetupOperationId: setupID.String()})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()
	broker.Publish(&worktreepb.SetupEvent{
		SetupOperationId: setupID.String(),
		Phase: &worktreepb.SetupEvent_Started{Started: &worktreepb.SetupStarted{
			SourceWorkspaceRoot: "/source",
			WorktreeRoot:        "/worktree",
			ScriptPath:          "/source/setup.sh",
		}},
	})
	evt, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if evt.SetupOperationId != setupID.String() {
		t.Fatalf("setup operation id = %s, want %s", evt.SetupOperationId, setupID)
	}
}
