package registry

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"core/server/attentionnotify"
	"core/server/runtime"
	askquestion "core/server/tools"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestRuntimeRegistryKeepsGenericPromptAttentionOffDesktopRootStream(t *testing.T) {
	broker := attentionnotify.NewBroker()
	registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	sessionSub, err := registry.SubscribeSessionAttentionNotifications(context.Background(), serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications: %v", err)
	}
	desktopSub, err := registry.SubscribeAttentionNotifications(context.Background(), serverapi.AttentionNotificationSubscribeRequest{})
	if err != nil {
		t.Fatalf("SubscribeAttentionNotifications: %v", err)
	}

	projectPendingPromptForTest(registry, "session-1", askquestion.AskQuestionRequest{ID: "ask-1", StepID: registryTestStepID, Question: "Proceed?"})
	pending := nextRegistryAttentionEvent(t, sessionSub)
	promptID := attentionNotificationID(clientui.AttentionNotificationKindQuestion, "ask-1")
	if pending.Source != clientui.AttentionNotificationSourceLive || pending.Type != clientui.AttentionNotificationEventPending || pending.Pending.ID != promptID || pending.Pending.Target.Kind != clientui.AttentionNotificationTargetSessionPrompt {
		t.Fatalf("pending event = %+v", pending)
	}
	if event, err := desktopSub.Next(shortRegistryContext(t)); err == nil {
		t.Fatalf("desktop received generic pending event: %+v", event)
	}
	resolvePendingPromptForTest(registry, "session-1", "ask-1")
	resolved := nextRegistryAttentionEvent(t, sessionSub)
	if resolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(resolved, promptID) {
		t.Fatalf("resolved event = %+v", resolved)
	}
	if event, err := desktopSub.Next(shortRegistryContext(t)); err == nil {
		t.Fatalf("desktop received generic resolved event: %+v", event)
	}
}

func TestRuntimeRegistryPublishesTaskQuestionBatchWithoutGenericResolve(t *testing.T) {
	broker := attentionnotify.NewBroker()
	registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	desktopSub, err := registry.SubscribeAttentionNotifications(context.Background(), serverapi.AttentionNotificationSubscribeRequest{})
	if err != nil {
		t.Fatalf("SubscribeAttentionNotifications: %v", err)
	}
	req := taskBatchAskRequest("ask-1")
	projectPendingPromptForTest(registry, "session-1", req)

	pending := nextRegistryAttentionEvent(t, desktopSub)
	batchID := attentionNotificationID(clientui.AttentionNotificationKindQuestion, "batch-1")
	if pending.Pending.ID != batchID {
		t.Fatalf("pending id = %q", pending.Pending.ID)
	}
	if pending.Pending.Question.DisplayCount != 2 || len(pending.Pending.Question.CurrentUnresolvedAskIDs) != 1 {
		t.Fatalf("question state = %+v", pending.Pending.Question)
	}
	resolvePendingPromptForTest(registry, "session-1", "ask-1")
	if event, err := desktopSub.Next(shortRegistryContext(t)); err == nil {
		t.Fatalf("task question resolved from prompt answer before durable clear: %+v", event)
	}
	skipped := *req.QuestionBatch
	skipped.PromptID = "ask-2"
	registry.MarkTaskQuestionSkipped(skipped)
	if event, err := desktopSub.Next(shortRegistryContext(t)); err == nil {
		t.Fatalf("skip published duplicate pending attention: %+v", event)
	}
	registry.MarkTaskQuestionCleared(*req.QuestionBatch, "ask-1")
	resolved := nextRegistryAttentionEvent(t, desktopSub)
	if resolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(resolved, batchID) {
		t.Fatalf("durable clear resolved event = %+v", resolved)
	}
}

func TestRuntimeRegistryPublishesTaskApprovalPromptAsDurablyClearedQuestionAttention(t *testing.T) {
	broker := attentionnotify.NewBroker()
	registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	desktopSub, err := registry.SubscribeAttentionNotifications(context.Background(), serverapi.AttentionNotificationSubscribeRequest{})
	if err != nil {
		t.Fatalf("SubscribeAttentionNotifications: %v", err)
	}
	target := clientui.AttentionNotificationTarget{
		Kind:      clientui.AttentionNotificationTargetWorkflowTask,
		ProjectID: "project-1",
		TaskID:    "task-1",
		SessionID: "session-1",
		Focus: &clientui.AttentionNotificationTaskDetailFocus{
			Kind:   clientui.AttentionNotificationFocusQuestion,
			AskIDs: []string{"approval-1"},
		},
	}
	req := askquestion.AskQuestionRequest{
		ID:              "approval-1",
		StepID:          registryTestStepID,
		Question:        "Approve protected path?",
		Approval:        true,
		AttentionTarget: &target,
		ApprovalOptions: []askquestion.AskQuestionApprovalOption{
			{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
			{Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"},
		},
	}
	projectPendingPromptForTest(registry, "session-1", req)

	pending := nextRegistryAttentionEvent(t, desktopSub)
	if pending.Type != clientui.AttentionNotificationEventPending || pending.Pending.Kind != clientui.AttentionNotificationKindQuestion || pending.Pending.Target.Kind != clientui.AttentionNotificationTargetWorkflowTask {
		t.Fatalf("pending approval question event = %+v", pending)
	}
	if pending.Pending.Question == nil || pending.Pending.Question.Preview != "Approve protected path?" || len(pending.Pending.Question.CurrentUnresolvedAskIDs) != 1 || pending.Pending.Question.CurrentUnresolvedAskIDs[0] != "approval-1" {
		t.Fatalf("pending approval question state = %+v", pending.Pending.Question)
	}
	if pending.Pending.Approval != nil {
		t.Fatalf("task-scoped approval prompt must not publish approval attention: %+v", pending.Pending.Approval)
	}
	resolvePendingPromptForTest(registry, "session-1", "approval-1")
	if event, err := desktopSub.Next(shortRegistryContext(t)); err == nil {
		t.Fatalf("prompt response resolved task approval before durable clear: %+v", event)
	}
	approvalOccurrenceKey := taskApprovalOccurrenceKey{sessionID: "session-1", askID: "approval-1"}
	registry.taskApprovalOccurrenceMu.Lock()
	occurrence, retained := registry.taskApprovalOccurrences[approvalOccurrenceKey]
	registry.taskApprovalOccurrenceMu.Unlock()
	if ordinal, ok := occurrence.OrdinaryOrdinal(); !retained || !ok || ordinal != 1 {
		t.Fatalf("retained task approval occurrence = %d / %t / %t, want 1 / true / true", ordinal, ok, retained)
	}
	registry.MarkTaskApprovalQuestionCleared(target, "approval-1")
	resolved := nextRegistryAttentionEvent(t, desktopSub)
	if resolved.Type != clientui.AttentionNotificationEventResolved || resolved.Kind != clientui.AttentionNotificationKindQuestion || !attentionNotificationEventIDMatches(resolved, attentionNotificationID(clientui.AttentionNotificationKindQuestion, "approval-1")) {
		t.Fatalf("durable clear resolved event = %+v", resolved)
	}
	registry.taskApprovalOccurrenceMu.Lock()
	_, retained = registry.taskApprovalOccurrences[approvalOccurrenceKey]
	registry.taskApprovalOccurrenceMu.Unlock()
	if retained {
		t.Fatal("task approval occurrence remained after durable clear")
	}
}

func TestRuntimeRegistrySkippedFirstTaskQuestionPreparesBatchBeforeMaterialization(t *testing.T) {
	broker := attentionnotify.NewBroker()
	registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	desktopSub, err := registry.SubscribeAttentionNotifications(context.Background(), serverapi.AttentionNotificationSubscribeRequest{})
	if err != nil {
		t.Fatalf("SubscribeAttentionNotifications: %v", err)
	}
	first := taskBatchAskRequest("ask-1")
	if err := registry.PrepareTaskQuestionBatch(*first.QuestionBatch, "session-1", first.AttentionTarget, first.Question, time.Now().UTC()); err != nil {
		t.Fatalf("PrepareTaskQuestionBatch: %v", err)
	}
	registry.MarkTaskQuestionSkipped(*first.QuestionBatch)

	second := taskBatchAskRequest("ask-2")
	projectPendingPromptForTest(registry, "session-1", second)

	pending := nextRegistryAttentionEvent(t, desktopSub)
	if pending.Type != clientui.AttentionNotificationEventPending || pending.Pending.Question.DisplayCount != 1 {
		t.Fatalf("pending after skipped first ask = %+v", pending)
	}
	if len(pending.Pending.Question.SkippedAskIDs) != 1 || pending.Pending.Question.SkippedAskIDs[0] != "ask-1" {
		t.Fatalf("skipped ask ids = %+v", pending.Pending.Question)
	}
	resolvePendingPromptForTest(registry, "session-1", "ask-2")
	registry.MarkTaskQuestionCleared(*second.QuestionBatch, "ask-2")
	resolved := nextRegistryAttentionEvent(t, desktopSub)
	if resolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(resolved, attentionNotificationID(clientui.AttentionNotificationKindQuestion, "batch-1")) {
		t.Fatalf("resolved event = %+v", resolved)
	}
}

func TestRuntimeRegistrySessionAttentionSnapshotUsesPendingPromptStore(t *testing.T) {
	broker := attentionnotify.NewBroker()
	registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	projectPendingPromptForTest(registry, "session-1", askquestion.AskQuestionRequest{ID: "ask-1", StepID: registryTestStepID, Question: "Proceed?"})

	sub, err := registry.SubscribeSessionAttentionNotifications(context.Background(), serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: "session-1", IncludePendingPromptSnapshot: true})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications: %v", err)
	}
	pending := nextRegistryAttentionEvent(t, sub)
	if pending.Source != clientui.AttentionNotificationSourceSnapshot || pending.Pending.ID != attentionNotificationID(clientui.AttentionNotificationKindQuestion, "ask-1") {
		t.Fatalf("snapshot pending = %+v", pending)
	}
	complete := nextRegistryAttentionEvent(t, sub)
	if complete.Type != clientui.AttentionNotificationEventSnapshotComplete || complete.SessionID != "session-1" {
		t.Fatalf("snapshot complete = %+v", complete)
	}
}

func TestRuntimeRegistrySessionAttentionSnapshotIsIndependentFromLiveBuffer(t *testing.T) {
	tests := []struct {
		name          string
		liveBuffer    int
		snapshotCount int
	}{
		{name: "empty snapshot", liveBuffer: 1},
		{name: "buffer below snapshot cardinality", liveBuffer: 2, snapshotCount: 3},
		{name: "buffer equals snapshot cardinality", liveBuffer: 3, snapshotCount: 3},
		{name: "buffer above snapshot cardinality", liveBuffer: 4, snapshotCount: 3},
		{name: "large snapshot with minimal live buffer", liveBuffer: 1, snapshotCount: 65},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broker := attentionnotify.NewBroker(attentionnotify.WithBufferSize(test.liveBuffer))
			registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
			engine := &runtime.Engine{}
			registerReady(t, registry, "session-1", engine)
			t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

			for index := 0; index < test.snapshotCount; index++ {
				projectPendingPromptForTest(registry, "session-1", askquestion.AskQuestionRequest{
					ID:       fmt.Sprintf("snapshot-%03d", index),
					StepID:   registryTestStepID,
					Question: "Proceed?",
				})
			}

			sub, err := registry.SubscribeSessionAttentionNotifications(context.Background(), serverapi.AttentionSessionNotificationSubscribeRequest{
				SessionID:                    "session-1",
				IncludePendingPromptSnapshot: true,
			})
			if err != nil {
				t.Fatalf("SubscribeSessionAttentionNotifications: %v", err)
			}
			t.Cleanup(func() { _ = sub.Close() })

			projectPendingPromptForTest(registry, "session-1", askquestion.AskQuestionRequest{
				ID:       "live-after-snapshot",
				StepID:   registryTestStepID,
				Question: "Proceed?",
			})

			for index := 0; index < test.snapshotCount; index++ {
				event := nextRegistryAttentionEvent(t, sub)
				wantID := attentionNotificationID(clientui.AttentionNotificationKindQuestion, fmt.Sprintf("snapshot-%03d", index))
				if event.Source != clientui.AttentionNotificationSourceSnapshot ||
					event.Type != clientui.AttentionNotificationEventPending ||
					event.Pending == nil ||
					event.Pending.ID != wantID {
					t.Fatalf("snapshot event %d = %+v, want %s", index, event, wantID.UUID)
				}
			}
			complete := nextRegistryAttentionEvent(t, sub)
			if complete.Source != clientui.AttentionNotificationSourceSnapshot ||
				complete.Type != clientui.AttentionNotificationEventSnapshotComplete {
				t.Fatalf("snapshot complete = %+v", complete)
			}
			live := nextRegistryAttentionEvent(t, sub)
			if live.Source != clientui.AttentionNotificationSourceLive ||
				live.Type != clientui.AttentionNotificationEventPending ||
				live.Pending == nil ||
				live.Pending.ID != attentionNotificationID(clientui.AttentionNotificationKindQuestion, "live-after-snapshot") {
				t.Fatalf("live event after snapshot = %+v", live)
			}
		})
	}
}

func TestRuntimeRegistrySessionAttentionSnapshotCapturesOpeningOrdinaryWatermark(t *testing.T) {
	broker := attentionnotify.NewBroker(attentionnotify.WithBufferSize(1))
	registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	projectPendingPromptForTest(registry, "session-1", askquestion.AskQuestionRequest{ID: "snapshot-1", StepID: registryTestStepID, Question: "Proceed?"})

	sub, err := registry.SubscribeSessionAttentionNotifications(context.Background(), serverapi.AttentionSessionNotificationSubscribeRequest{
		SessionID:                    "session-1",
		IncludePendingPromptSnapshot: true,
	})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications: %v", err)
	}
	snapshot, ok := sub.(*attentionnotify.SnapshotSubscription)
	if !ok {
		t.Fatalf("snapshot subscription = %T, want *attentionnotify.SnapshotSubscription", sub)
	}
	if snapshot.OpeningOrdinaryWatermark() != 1 {
		t.Fatalf("opening ordinary watermark = %d, want 1", snapshot.OpeningOrdinaryWatermark())
	}

	projectPendingPromptForTest(registry, "session-1", askquestion.AskQuestionRequest{ID: "live-2", StepID: registryTestStepID, Question: "Proceed?"})
	pending := registry.ListPendingPrompts("session-1")
	foundLivePending := false
	for _, item := range pending {
		if item.Request.ID != "live-2" {
			continue
		}
		foundLivePending = true
		ordinal, present := item.occurrence.OrdinaryOrdinal()
		if !present || ordinal != 2 {
			t.Fatalf("live ordinary occurrence = %d / %t, want 2 / true", ordinal, present)
		}
		break
	}
	if !foundLivePending {
		t.Fatal("live pending prompt was not found")
	}

	_ = nextRegistryAttentionEvent(t, sub)
	_ = nextRegistryAttentionEvent(t, sub)
	live := nextRegistryAttentionEvent(t, sub)
	if live.Source != clientui.AttentionNotificationSourceLive || live.Pending == nil || live.Pending.ID.UUID != "live-2" {
		t.Fatalf("live event = %+v", live)
	}
}

func TestRuntimeRegistrySessionAttentionSnapshotSuppressesDelayedOrdinaryPublication(t *testing.T) {
	tests := []struct {
		name string
		req  askquestion.AskQuestionRequest
	}{
		{name: "question", req: ordinaryAttentionRequest("question-1", false)},
		{name: "approval", req: ordinaryAttentionRequest("approval-1", true)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broker := attentionnotify.NewBroker(attentionnotify.WithBufferSize(4))
			registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
			engine := &runtime.Engine{}
			registerReady(t, registry, "session-1", engine)
			t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
			entry := registry.directory.Entry("session-1")
			if entry == nil {
				t.Fatal("runtime entry is unavailable")
			}

			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			publishStarted := make(chan struct{})
			releasePublish := make(chan struct{})
			published := make(chan struct{})
			awaitDone := make(chan error, 1)
			go func() {
				_, err := entry.pendingPrompts.Await(ctx, test.req, func(snapshot PendingPromptSnapshot, eventType pendingPromptEventType) {
					switch eventType {
					case pendingPromptEventPending:
						close(publishStarted)
						<-releasePublish
						registry.publishAttentionPending("session-1", snapshot)
						close(published)
					case pendingPromptEventResolved:
						registry.publishAttentionResolved("session-1", snapshot)
					}
				})
				awaitDone <- err
			}()
			waitForPendingPromptSignal(t, publishStarted, "timed out waiting for delayed ordinary publication")

			sub, err := registry.SubscribeSessionAttentionNotifications(context.Background(), serverapi.AttentionSessionNotificationSubscribeRequest{
				SessionID:                    "session-1",
				IncludePendingPromptSnapshot: true,
			})
			if err != nil {
				t.Fatalf("SubscribeSessionAttentionNotifications: %v", err)
			}
			t.Cleanup(func() { _ = sub.Close() })
			close(releasePublish)
			waitForPendingPromptSignal(t, published, "timed out waiting for delayed ordinary publication")

			snapshot := nextRegistryAttentionEvent(t, sub)
			if snapshot.Source != clientui.AttentionNotificationSourceSnapshot ||
				snapshot.Type != clientui.AttentionNotificationEventPending ||
				snapshot.Pending == nil ||
				snapshot.Pending.ID.UUID != test.req.ID {
				t.Fatalf("snapshot pending = %+v", snapshot)
			}
			if complete := nextRegistryAttentionEvent(t, sub); complete.Type != clientui.AttentionNotificationEventSnapshotComplete {
				t.Fatalf("snapshot complete = %+v", complete)
			}
			requireNoRegistryAttentionEvent(t, sub, "delayed ordinary publication duplicated the snapshot")

			_ = sub.Close()
			cancel()
			if err := <-awaitDone; !errors.Is(err, context.Canceled) {
				t.Fatalf("Await error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestRuntimeRegistrySessionAttentionSnapshotDeliversOrdinaryPublicationAfterCapture(t *testing.T) {
	tests := []struct {
		name string
		req  askquestion.AskQuestionRequest
	}{
		{name: "question", req: ordinaryAttentionRequest("question-1", false)},
		{name: "approval", req: ordinaryAttentionRequest("approval-1", true)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broker := attentionnotify.NewBroker(attentionnotify.WithBufferSize(4))
			registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
			engine := &runtime.Engine{}
			registerReady(t, registry, "session-1", engine)
			t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
			entry := registry.directory.Entry("session-1")
			if entry == nil {
				t.Fatal("runtime entry is unavailable")
			}

			insertStarted := make(chan struct{})
			inserted := make(chan struct{})
			var sub serverapi.AttentionNotificationSubscription
			_, err := entry.pendingPrompts.WithLockedAttentionSnapshotResult(func(snapshot pendingAttentionSnapshot) (serverapi.AttentionNotificationSubscription, error) {
				descriptors, err := registry.attentionSnapshotDescriptors("session-1", snapshot.items)
				if err != nil {
					return nil, err
				}
				sub, err = broker.SubscribeSessionSnapshot("session-1", descriptors, snapshot.ordinaryOccurrenceWatermark)
				if err != nil {
					return nil, err
				}
				go func() {
					close(insertStarted)
					entry.pendingPrompts.Begin(test.req, func(pending PendingPromptSnapshot, eventType pendingPromptEventType) {
						if eventType == pendingPromptEventPending {
							registry.publishAttentionPending("session-1", pending)
						}
					})
					close(inserted)
				}()
				waitForPendingPromptSignal(t, insertStarted, "timed out starting ordinary insertion")
				select {
				case <-inserted:
					t.Fatal("ordinary insertion completed while snapshot capture lock was held")
				case <-time.After(10 * time.Millisecond):
				}
				return sub, nil
			})
			if err != nil {
				t.Fatalf("WithLockedAttentionSnapshotResult: %v", err)
			}
			t.Cleanup(func() { _ = sub.Close() })
			waitForPendingPromptSignal(t, inserted, "timed out waiting for ordinary insertion")

			if complete := nextRegistryAttentionEvent(t, sub); complete.Type != clientui.AttentionNotificationEventSnapshotComplete {
				t.Fatalf("snapshot complete = %+v", complete)
			}
			live := nextRegistryAttentionEvent(t, sub)
			if live.Source != clientui.AttentionNotificationSourceLive ||
				live.Type != clientui.AttentionNotificationEventPending ||
				live.Pending == nil ||
				live.Pending.ID.UUID != test.req.ID {
				t.Fatalf("live ordinary publication = %+v", live)
			}
			requireNoRegistryAttentionEvent(t, sub, "ordinary publication was delivered more than once")
		})
	}
}

func TestRuntimeRegistrySessionAttentionSnapshotProjectsSerializedTaskBatchPerAttachment(t *testing.T) {
	broker := attentionnotify.NewBroker(attentionnotify.WithBufferSize(8))
	registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })

	existing, err := registry.SubscribeSessionAttentionNotifications(context.Background(), serverapi.AttentionSessionNotificationSubscribeRequest{
		SessionID:                    "session-1",
		IncludePendingPromptSnapshot: true,
	})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications existing: %v", err)
	}
	t.Cleanup(func() { _ = existing.Close() })
	if complete := nextRegistryAttentionEvent(t, existing); complete.Type != clientui.AttentionNotificationEventSnapshotComplete {
		t.Fatalf("existing snapshot complete = %+v", complete)
	}

	first := taskBatchAskRequest("ask-1")
	firstDone := awaitRegistryPrompt(t, registry, first)
	firstPending := nextRegistryAttentionEvent(t, existing)
	if firstPending.Source != clientui.AttentionNotificationSourceLive ||
		firstPending.Pending == nil ||
		firstPending.Pending.Revision != 1 {
		t.Fatalf("first task batch pending = %+v", firstPending)
	}
	if err := registry.SubmitPromptResponse("session-1", askquestion.AskQuestionResponse{RequestID: "ask-1", Answer: "yes"}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse ask-1: %v", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("AwaitPromptResponse ask-1: %v", err)
	}
	registry.MarkTaskQuestionCleared(*first.QuestionBatch, "ask-1")

	duringGap, err := registry.SubscribeSessionAttentionNotifications(context.Background(), serverapi.AttentionSessionNotificationSubscribeRequest{
		SessionID:                    "session-1",
		IncludePendingPromptSnapshot: true,
	})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications during gap: %v", err)
	}
	t.Cleanup(func() { _ = duringGap.Close() })
	if complete := nextRegistryAttentionEvent(t, duringGap); complete.Type != clientui.AttentionNotificationEventSnapshotComplete {
		t.Fatalf("gap snapshot complete = %+v", complete)
	}

	second := taskBatchAskRequest("ask-2")
	secondDone := awaitRegistryPrompt(t, registry, second)
	secondPending := nextRegistryAttentionEvent(t, duringGap)
	if secondPending.Source != clientui.AttentionNotificationSourceLive ||
		secondPending.Pending == nil ||
		secondPending.Pending.Revision <= firstPending.Pending.Revision {
		t.Fatalf("gap attachment task batch pending = %+v", secondPending)
	}
	requireNoRegistryAttentionEvent(t, existing, "existing attachment repeated the serialized task batch")

	if err := registry.SubmitPromptResponse("session-1", askquestion.AskQuestionResponse{RequestID: "ask-2", Answer: "yes"}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse ask-2: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("AwaitPromptResponse ask-2: %v", err)
	}
	registry.MarkTaskQuestionCleared(*second.QuestionBatch, "ask-2")

	for name, sub := range map[string]serverapi.AttentionNotificationSubscription{
		"existing":   existing,
		"during_gap": duringGap,
	} {
		resolved := nextRegistryAttentionEvent(t, sub)
		if resolved.Type != clientui.AttentionNotificationEventResolved ||
			!attentionNotificationEventIDMatches(resolved, attentionNotificationID(clientui.AttentionNotificationKindQuestion, "batch-1")) {
			t.Fatalf("%s task batch resolved = %+v", name, resolved)
		}
	}
}

func TestRuntimeRegistrySessionAttentionSnapshotPreservesTaskQuestionBatch(t *testing.T) {
	broker := attentionnotify.NewBroker()
	registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	req := taskBatchAskRequest("ask-1")
	projectPendingPromptForTest(registry, "session-1", req)

	sub, err := registry.SubscribeSessionAttentionNotifications(context.Background(), serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: "session-1", IncludePendingPromptSnapshot: true})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications: %v", err)
	}
	pending := nextRegistryAttentionEvent(t, sub)
	if pending.Source != clientui.AttentionNotificationSourceSnapshot || pending.Type != clientui.AttentionNotificationEventPending {
		t.Fatalf("snapshot event = %+v", pending)
	}
	if pending.Pending.ID != attentionNotificationID(clientui.AttentionNotificationKindQuestion, "batch-1") || pending.Pending.Target.Kind != clientui.AttentionNotificationTargetWorkflowTask {
		t.Fatalf("snapshot pending = %+v", pending.Pending)
	}
	if pending.Pending.Target.Focus == nil || len(pending.Pending.Target.Focus.AskIDs) != 2 || pending.Pending.Target.Focus.AskIDs[0] != "ask-1" {
		t.Fatalf("snapshot focus = %+v", pending.Pending.Target.Focus)
	}
	if pending.Pending.Question == nil || pending.Pending.Question.DisplayCount != 2 || len(pending.Pending.Question.CurrentUnresolvedAskIDs) != 1 || pending.Pending.Question.CurrentUnresolvedAskIDs[0] != "ask-1" {
		t.Fatalf("snapshot question state = %+v", pending.Pending.Question)
	}
	complete := nextRegistryAttentionEvent(t, sub)
	if complete.Type != clientui.AttentionNotificationEventSnapshotComplete || complete.SessionID != "session-1" {
		t.Fatalf("snapshot complete = %+v", complete)
	}
	skipped := *req.QuestionBatch
	skipped.PromptID = "ask-2"
	registry.MarkTaskQuestionSkipped(skipped)
	if event, err := sub.Next(shortRegistryContext(t)); err == nil {
		t.Fatalf("skip published duplicate pending snapshot attention: %+v", event)
	}
	registry.MarkTaskQuestionCleared(*req.QuestionBatch, "ask-1")
	resolved := nextRegistryAttentionEvent(t, sub)
	if resolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(resolved, attentionNotificationID(clientui.AttentionNotificationKindQuestion, "batch-1")) {
		t.Fatalf("resolved event = %+v", resolved)
	}
}

func nextRegistryAttentionEvent(t *testing.T, sub serverapi.AttentionNotificationSubscription) clientui.AttentionNotificationEvent {
	t.Helper()
	event, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return event
}

func requireNoRegistryAttentionEvent(t *testing.T, sub serverapi.AttentionNotificationSubscription, failure string) {
	t.Helper()
	event, err := sub.Next(shortRegistryContext(t))
	if err == nil {
		t.Fatalf("%s: %+v", failure, event)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%s: Next error = %v, want context deadline", failure, err)
	}
}

func awaitRegistryPrompt(t *testing.T, registry *RuntimeRegistry, req askquestion.AskQuestionRequest) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := registry.AwaitPromptResponse(context.Background(), "session-1", req)
		done <- err
	}()
	return done
}

func shortRegistryContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func ordinaryAttentionRequest(id string, approval bool) askquestion.AskQuestionRequest {
	req := askquestion.AskQuestionRequest{
		ID:       id,
		Question: "Proceed?",
	}
	if approval {
		req.Approval = true
		req.ApprovalOptions = []askquestion.AskQuestionApprovalOption{
			{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
			{Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"},
		}
	}
	return req
}

func attentionNotificationID(kind clientui.AttentionNotificationKind, uuid string) clientui.AttentionNotificationID {
	return clientui.AttentionNotificationID{Kind: kind, UUID: uuid}
}

func attentionNotificationEventIDMatches(event clientui.AttentionNotificationEvent, id clientui.AttentionNotificationID) bool {
	return event.ID != nil && *event.ID == id
}

func taskBatchAskRequest(id string) askquestion.AskQuestionRequest {
	return askquestion.AskQuestionRequest{
		ID:       id,
		StepID:   registryTestStepID,
		Question: "Proceed?",
		QuestionBatch: &askquestion.AskQuestionBatchMetadata{
			Origin:              askquestion.AskQuestionOriginModelTool,
			RunID:               "run-1",
			StepID:              "step-1",
			BatchID:             "batch-1",
			PromptID:            id,
			BatchPromptIDs:      []string{"ask-1", "ask-2"},
			CandidateOrdinal:    0,
			PreparedPromptCount: 2,
		},
		AttentionTarget: &clientui.AttentionNotificationTarget{
			Kind:      clientui.AttentionNotificationTargetWorkflowTask,
			TaskID:    "task-1",
			SessionID: "session-1",
			RunID:     "run-1",
			Focus: &clientui.AttentionNotificationTaskDetailFocus{
				Kind:   clientui.AttentionNotificationFocusQuestion,
				AskIDs: []string{"ask-1", "ask-2"},
			},
		},
	}
}
