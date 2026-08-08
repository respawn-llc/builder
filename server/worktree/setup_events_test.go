package worktree

import (
	"context"
	"errors"
	"io"
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
		SetupOperationID: setupID,
		Phase:            serverapi.WorktreeSetupPhaseStarted,
		Started: &serverapi.WorktreeSetupStarted{
			SourceWorkspaceRoot: "/source",
			WorktreeRoot:        "/worktree",
			ScriptPath:          "/source/setup.sh",
		},
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
		SetupOperationID: setupID,
		Phase:            serverapi.WorktreeSetupPhaseStarted,
		Started: &serverapi.WorktreeSetupStarted{
			SourceWorkspaceRoot: "/source",
			WorktreeRoot:        "/worktree",
			ScriptPath:          "/source/setup.sh",
		},
	})
	evt, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if evt.SetupOperationID != setupID {
		t.Fatalf("setup operation id = %s, want %s", evt.SetupOperationID, setupID)
	}
}

func TestSetupEventBrokerAllowsRepeatedAttemptsAndExactlyOneTerminalPayload(t *testing.T) {
	broker := newSetupEventBroker()
	setupID := serverapi.NewWorktreeSetupOperationID()
	sub, err := broker.Subscribe(serverapi.WorktreeSetupSubscribeRequest{SetupOperationID: setupID})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	started := serverapi.WorktreeSetupEvent{
		SetupOperationID: setupID,
		Phase:            serverapi.WorktreeSetupPhaseStarted,
		Started: &serverapi.WorktreeSetupStarted{
			SourceWorkspaceRoot: "/source",
			WorktreeRoot:        "/worktree",
			ScriptPath:          "/source/setup.sh",
		},
	}
	broker.Publish(started)
	broker.Publish(started)
	broker.Publish(serverapi.WorktreeSetupEvent{
		SetupOperationID: setupID,
		Phase:            serverapi.WorktreeSetupPhaseNotRequired,
		NotRequired: &serverapi.WorktreeSetupNotRequired{
			Reason: serverapi.WorktreeSetupNotRequiredNoConfiguredScript,
		},
	})
	broker.Publish(serverapi.WorktreeSetupEvent{
		SetupOperationID: setupID,
		Phase:            serverapi.WorktreeSetupPhaseCompleted,
		Completed:        &serverapi.WorktreeSetupCompleted{},
	})

	for index, phase := range []serverapi.WorktreeSetupPhase{
		serverapi.WorktreeSetupPhaseStarted,
		serverapi.WorktreeSetupPhaseStarted,
		serverapi.WorktreeSetupPhaseNotRequired,
	} {
		event, err := sub.Next(context.Background())
		if err != nil {
			t.Fatalf("Next event %d: %v", index, err)
		}
		if event.Phase != phase {
			t.Fatalf("event %d phase = %q, want %q", index, event.Phase, phase)
		}
	}
	if _, err := sub.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Next after terminal error = %v, want EOF", err)
	}
}
