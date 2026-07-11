package runtimewirefixture

import (
	"context"
	"fmt"
	"time"

	shelltool "core/server/tools/shell"
)

func BackgroundCompletionEvent(id string, ownerSessionID string, root string) shelltool.Event {
	return BackgroundCompletionEventWithExit(id, ownerSessionID, root, 0)
}

func BackgroundCompletionEventWithExit(id string, ownerSessionID string, root string, exitCode int) shelltool.Event {
	manager, err := shelltool.NewManager(shelltool.WithMinimumExecToBgTime(time.Millisecond))
	if err != nil {
		panic(fmt.Sprintf("create background shell manager fixture: %v", err))
	}
	defer func() { _ = manager.Close() }()

	events := make(chan shelltool.Event, 1)
	manager.SetEventHandler(func(event shelltool.Event) {
		if event.Type == shelltool.EventCompleted || event.Type == shelltool.EventKilled {
			events <- event
		}
	})
	result, err := manager.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"/bin/sh", "-c", fmt.Sprintf("sleep 0.02; printf done; exit %d", exitCode)},
		DisplayCommand: "fixture background completion",
		OwnerSessionID: ownerSessionID,
		Workdir:        root,
		YieldTime:      time.Millisecond,
	})
	if err != nil {
		panic(fmt.Sprintf("start background shell fixture: %v", err))
	}
	if !result.MovedToBackground {
		panic("background shell fixture completed before background transition")
	}
	select {
	case event := <-events:
		event.Snapshot.ID = id
		return event
	case <-time.After(time.Second):
		panic("timed out waiting for background shell fixture completion")
	}
}
