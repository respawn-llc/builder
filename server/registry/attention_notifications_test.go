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

	done := make(chan error, 1)
	go func() {
		_, err := registry.AwaitPromptResponse(context.Background(), "session-1", askquestion.AskQuestionRequest{ID: "ask-1", Question: "Proceed?"})
		done <- err
	}()
	pending := nextRegistryAttentionEvent(t, sessionSub)
	promptID := attentionNotificationID(clientui.AttentionNotificationKindQuestion, "ask-1")
	if pending.Source != clientui.AttentionNotificationSourceLive || pending.Type != clientui.AttentionNotificationEventPending || pending.Pending.ID != promptID || pending.Pending.Target.Kind != clientui.AttentionNotificationTargetSessionPrompt {
		t.Fatalf("pending event = %+v", pending)
	}
	if event, err := desktopSub.Next(shortRegistryContext(t)); err == nil {
		t.Fatalf("desktop received generic pending event: %+v", event)
	}
	if err := registry.SubmitPromptResponse("session-1", askquestion.AskQuestionResponse{RequestID: "ask-1", Answer: "yes"}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse: %v", err)
	}
	resolved := nextRegistryAttentionEvent(t, sessionSub)
	if resolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(resolved, promptID) {
		t.Fatalf("resolved event = %+v", resolved)
	}
	if event, err := desktopSub.Next(shortRegistryContext(t)); err == nil {
		t.Fatalf("desktop received generic resolved event: %+v", event)
	}
	if err := <-done; err != nil {
		t.Fatalf("AwaitPromptResponse: %v", err)
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
	done := make(chan error, 1)
	go func() {
		_, err := registry.AwaitPromptResponse(context.Background(), "session-1", req)
		done <- err
	}()

	pending := nextRegistryAttentionEvent(t, desktopSub)
	batchID := attentionNotificationID(clientui.AttentionNotificationKindQuestion, "batch-1")
	if pending.Pending.ID != batchID {
		t.Fatalf("pending id = %q", pending.Pending.ID)
	}
	if pending.Pending.Question.DisplayCount != 2 || len(pending.Pending.Question.CurrentUnresolvedAskIDs) != 1 {
		t.Fatalf("question state = %+v", pending.Pending.Question)
	}
	if err := registry.SubmitPromptResponse("session-1", askquestion.AskQuestionResponse{RequestID: "ask-1", Answer: "yes"}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("AwaitPromptResponse: %v", err)
	}
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
		Question:        "Approve protected path?",
		Approval:        true,
		AttentionTarget: &target,
		ApprovalOptions: []askquestion.AskQuestionApprovalOption{
			{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: "Allow once"},
			{Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: "Deny"},
		},
	}
	done := make(chan error, 1)
	go func() {
		_, err := registry.AwaitPromptResponse(context.Background(), "session-1", req)
		done <- err
	}()

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
	if err := registry.SubmitPromptResponse("session-1", askquestion.AskQuestionResponse{
		RequestID: "approval-1",
		Approval:  &askquestion.AskQuestionApprovalPayload{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce},
	}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("AwaitPromptResponse: %v", err)
	}
	if event, err := desktopSub.Next(shortRegistryContext(t)); err == nil {
		t.Fatalf("prompt response resolved task approval before durable clear: %+v", event)
	}
	registry.MarkTaskApprovalQuestionCleared(target, "approval-1")
	resolved := nextRegistryAttentionEvent(t, desktopSub)
	if resolved.Type != clientui.AttentionNotificationEventResolved || resolved.Kind != clientui.AttentionNotificationKindQuestion || !attentionNotificationEventIDMatches(resolved, attentionNotificationID(clientui.AttentionNotificationKindQuestion, "approval-1")) {
		t.Fatalf("durable clear resolved event = %+v", resolved)
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
	done := make(chan error, 1)
	go func() {
		_, err := registry.AwaitPromptResponse(context.Background(), "session-1", second)
		done <- err
	}()

	pending := nextRegistryAttentionEvent(t, desktopSub)
	if pending.Type != clientui.AttentionNotificationEventPending || pending.Pending.Question.DisplayCount != 1 {
		t.Fatalf("pending after skipped first ask = %+v", pending)
	}
	if len(pending.Pending.Question.SkippedAskIDs) != 1 || pending.Pending.Question.SkippedAskIDs[0] != "ask-1" {
		t.Fatalf("skipped ask ids = %+v", pending.Pending.Question)
	}
	if err := registry.SubmitPromptResponse("session-1", askquestion.AskQuestionResponse{RequestID: "ask-2", Answer: "yes"}, nil); err != nil {
		t.Fatalf("SubmitPromptResponse: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("AwaitPromptResponse: %v", err)
	}
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
	registry.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: "ask-1", Question: "Proceed?"})

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

func TestRuntimeRegistrySessionAttentionSnapshotOverflowReturnsStreamGap(t *testing.T) {
	broker := attentionnotify.NewBroker()
	registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	for i := 0; i < 65; i++ {
		registry.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: fmt.Sprintf("ask-%d", i), Question: "Proceed?"})
	}

	sub, err := registry.SubscribeSessionAttentionNotifications(context.Background(), serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: "session-1", IncludePendingPromptSnapshot: true})
	if !errors.Is(err, serverapi.ErrStreamGap) {
		t.Fatalf("SubscribeSessionAttentionNotifications error = %v, want ErrStreamGap", err)
	}
	if sub != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications returned subscription after snapshot overflow: %+v", sub)
	}
}

func TestRuntimeRegistrySessionAttentionSnapshotPreservesTaskQuestionBatch(t *testing.T) {
	broker := attentionnotify.NewBroker()
	registry := NewRuntimeRegistry().WithAttentionNotifications(broker)
	engine := &runtime.Engine{}
	registerReady(t, registry, "session-1", engine)
	t.Cleanup(func() { closeRuntime(registry, "session-1", engine) })
	req := taskBatchAskRequest("ask-1")
	registry.BeginPendingPrompt("session-1", req)

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

func shortRegistryContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
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
