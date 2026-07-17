package registry

import (
	"context"
	"testing"
	"time"

	askquestion "core/server/tools"
	"core/shared/serverapi"
)

func TestPendingPromptStoreSnapshotsDoNotWaitForLivePendingPublish(t *testing.T) {
	store := newPendingPromptStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	publishStarted := make(chan struct{})
	releasePublish := make(chan struct{})
	publishDone := make(chan struct{})
	awaitDone := make(chan error, 1)

	go func() {
		_, err := store.Await(ctx, askquestion.AskQuestionRequest{ID: "ask-1", Question: "Proceed?"}, func(snapshot PendingPromptSnapshot, eventType pendingPromptEventType) {
			if eventType != pendingPromptEventPending || snapshot.Request.ID != "ask-1" {
				return
			}
			close(publishStarted)
			<-releasePublish
			close(publishDone)
		})
		awaitDone <- err
	}()

	waitForPendingPromptSignal(t, publishStarted, "timed out waiting for pending prompt live publication")

	snapshotEntered := make(chan struct{})
	snapshotDone := make(chan struct{})
	go func() {
		_, _ = store.WithLockedAttentionSnapshotResult(func(items []PendingPromptSnapshot) (serverapi.AttentionNotificationSubscription, error) {
			close(snapshotEntered)
			if len(items) != 1 || items[0].Request.ID != "ask-1" {
				t.Errorf("snapshot items = %+v", items)
			}
			return nil, nil
		})
		close(snapshotDone)
	}()

	waitForPendingPromptSignal(t, snapshotEntered, "timed out waiting for snapshot during live pending publication")

	close(releasePublish)
	waitForPendingPromptSignal(t, publishDone, "timed out waiting for pending prompt live publication to finish")
	waitForPendingPromptSignal(t, snapshotDone, "timed out waiting for snapshot after live pending publication")

	cancel()
	select {
	case <-awaitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pending prompt await to stop")
	}
}

func TestPendingPromptStoreBeginSnapshotsDoNotWaitForLivePendingPublish(t *testing.T) {
	store := newPendingPromptStore()
	publishStarted := make(chan struct{})
	releasePublish := make(chan struct{})
	beginDone := make(chan struct{})

	go func() {
		store.Begin(askquestion.AskQuestionRequest{ID: "ask-1", Question: "Proceed?"}, func(snapshot PendingPromptSnapshot, eventType pendingPromptEventType) {
			if eventType != pendingPromptEventPending || snapshot.Request.ID != "ask-1" {
				return
			}
			close(publishStarted)
			<-releasePublish
		})
		close(beginDone)
	}()

	waitForPendingPromptSignal(t, publishStarted, "timed out waiting for pending prompt live publication")

	snapshotEntered := make(chan struct{})
	snapshotDone := make(chan struct{})
	go func() {
		_, _ = store.WithLockedAttentionSnapshotResult(func(items []PendingPromptSnapshot) (serverapi.AttentionNotificationSubscription, error) {
			close(snapshotEntered)
			if len(items) != 1 || items[0].Request.ID != "ask-1" {
				t.Errorf("snapshot items = %+v", items)
			}
			return nil, nil
		})
		close(snapshotDone)
	}()

	waitForPendingPromptSignal(t, snapshotEntered, "timed out waiting for snapshot during Begin live pending publication")

	close(releasePublish)
	waitForPendingPromptSignal(t, beginDone, "timed out waiting for Begin to finish")
	waitForPendingPromptSignal(t, snapshotDone, "timed out waiting for snapshot after Begin live pending publication")
}

func waitForPendingPromptSignal(t *testing.T, signal <-chan struct{}, timeoutMessage string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal(timeoutMessage)
	}
}
