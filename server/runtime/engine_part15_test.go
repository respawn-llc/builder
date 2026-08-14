package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestAutoCompactionRemoteReplacesHistoryAndCarriesCompactionItem(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working")},
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
				},
				Usage: llm.Usage{InputTokens: 190000, OutputTokens: 2000, WindowTokens: 200000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
				Usage:     llm.Usage{InputTokens: 2000, OutputTokens: 1000, WindowTokens: 200000},
			},
		},
		compactionResponses: []llm.CompactionResponse{
			{
				OutputItems: []llm.ResponseItem{
					{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), Content: textutil.Value("run tools")},
					{Type: llm.ResponseItemTypeCompaction, ID: textutil.Value("cmp_1"), EncryptedContent: textutil.Value("enc_1")},
				},
				Usage: llm.Usage{InputTokens: 12000, OutputTokens: 1000, WindowTokens: 200000},
			},
		},
	}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	msg, err := eng.SubmitUserMessage(context.Background(), "run tools")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if len(client.compactionCalls) != 1 {
		t.Fatalf("expected one remote compaction call, got %d", len(client.compactionCalls))
	}
	if len(client.calls) < 2 {
		t.Fatalf("expected second model call after compaction, got %d calls", len(client.calls))
	}

	foundCompactionItem := false
	for _, item := range client.calls[1].Items {
		if item.Type == llm.ResponseItemTypeCompaction && item.EncryptedContent != nil && *item.EncryptedContent == "enc_1" {
			foundCompactionItem = true
			break
		}
	}
	if !foundCompactionItem {
		t.Fatalf("expected compaction item in post-compaction request, got %+v", client.calls[1].Items)
	}

	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	sawHistoryReplace := false
	for _, evt := range events {
		if evt.Kind == "history_replaced" {
			sawHistoryReplace = true
			break
		}
	}
	if !sawHistoryReplace {
		t.Fatalf("expected history_replaced event, got %+v", events)
	}
}

func TestCompactionReplacementPayloadEmbedsReinjectedBaseMetaAndPreservedUserMessageAtomically(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{{
		OutputItems: []llm.ResponseItem{
			{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("remote summary")},
			{Type: llm.ResponseItemTypeCompaction, ID: textutil.Value("cmp_1"), EncryptedContent: textutil.Value("enc_1")},
		},
		Usage: llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	if _, err := eng.SetGoal("preserve atomic goal context", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	mustSetWorktreeReminderState(t, store, testWorktreeReminderState(
		session.WorktreeReminderModeEnter,
		"feature/goal",
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
	))
	restoreStep := setTestActiveStep(eng, "step-1")
	defer restoreStep()
	if err := eng.steer("step-1", steerMessagesWithPersistenceIntent(steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}

	if _, _, err := eng.compactNow(context.Background(), "step-1", compactionModeManual, compactionInstructionsInput{}, true); err != nil {
		t.Fatalf("compactNow: %v", err)
	}

	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	historyIndex := -1
	var replacement historyReplacementPayload
	for idx, evt := range events {
		if evt.Kind != "history_replaced" {
			continue
		}
		historyIndex = idx
		replacement = persistedHistoryReplacementForTest(t, evt)
		break
	}
	if historyIndex < 0 {
		t.Fatalf("expected history_replaced event, got %+v", events)
	}
	summaryIndex, environmentIndex, goalIndex, worktreeIndex, carryoverIndex := -1, -1, -1, -1, -1
	goalCount := 0
	for idx, item := range replacement.Items {
		if item.MessageType == nil {
			continue
		}
		switch *item.MessageType {
		case llm.MessageTypeCompactionSummary:
			summaryIndex = idx
		case llm.MessageTypeEnvironment:
			environmentIndex = idx
		case llm.MessageTypeActiveGoalContinuation:
			goalIndex = idx
			goalCount++
			if item.Content == nil || *item.Content != prompts.RenderActiveGoalContinuationPrompt("preserve atomic goal context") {
				t.Fatalf("active-goal continuation content = %v", item.Content)
			}
		case llm.MessageTypeWorktreeMode:
			worktreeIndex = idx
		case llm.MessageTypeCompactionPreservedUserMessage:
			carryoverIndex = idx
			if item.Content == nil || !strings.Contains(*item.Content, "seed") {
				t.Fatalf("compaction-preserved user message lost the last visible user message: %+v", item)
			}
		}
	}
	if summaryIndex < 0 || environmentIndex < 0 || goalIndex < 0 || worktreeIndex < 0 || carryoverIndex < 0 || goalCount != 1 {
		t.Fatalf("replacement payload must embed summary, base meta, one active-goal continuation, worktree context, and compaction-preserved user message: %+v", replacement.Items)
	}
	if !(summaryIndex < environmentIndex && environmentIndex < goalIndex && goalIndex < worktreeIndex && worktreeIndex < carryoverIndex) || carryoverIndex != len(replacement.Items)-1 {
		t.Fatalf("replacement payload order must be summary, base, active goal, worktree, then compaction-preserved user message: %+v", replacement.Items)
	}
	for _, evt := range events[historyIndex+1:] {
		if evt.Kind != "message" {
			continue
		}
		msg := persistedMessageForTest(t, evt)
		if msg.Role == llm.RoleDeveloper && msg.MessageType != nil &&
			(*msg.MessageType == llm.MessageTypeEnvironment ||
				*msg.MessageType == llm.MessageTypeActiveGoalContinuation ||
				*msg.MessageType == llm.MessageTypeCompactionPreservedUserMessage) {
			t.Fatalf("base meta, active-goal continuation, and compaction-preserved user message must be embedded in the replacement payload, not steered separately afterward: events=%+v", events)
		}
	}
}

type failOnHistoryReplacementAgentResetObservation struct {
	failed bool
}

func (o *failOnHistoryReplacementAgentResetObservation) ObservePersistedStore(_ context.Context, snapshot session.PersistedStoreSnapshot) error {
	if !o.failed && snapshot.Meta.LastSequence >= 2 {
		o.failed = true
		return errors.New("persist observer failed after history replacement append")
	}
	return nil
}

type committedCompactionFixture struct {
	store  *session.Store
	engine *Engine
	client *fakeCompactionClient
	events []Event
}

func newCommittedCompactionFixture(t *testing.T, observer session.PersistenceObserver) *committedCompactionFixture {
	t.Helper()
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(observer))
	if err := store.MarkModelDispatchLocked(session.LockedContract{
		Model:             "gpt-5",
		SystemPrompt:      "stale system prompt",
		HasSystemPrompt:   true,
		ReviewerPrompt:    "stale reviewer prompt",
		HasReviewerPrompt: true,
	}); err != nil {
		t.Fatalf("lock prompt snapshots: %v", err)
	}
	client := &fakeCompactionClient{
		inputTokenCount: 2_000,
		caps: llm.ProviderCapabilities{
			ProviderID:                     "openai",
			SupportsResponsesAPI:           true,
			SupportsResponsesCompact:       true,
			SupportsRequestInputTokenCount: true,
			IsOpenAIFirstParty:             true,
		},
		compactionResponses: []llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")},
				{Type: llm.ResponseItemTypeCompaction, ID: textutil.Value("cmp_1"), EncryptedContent: textutil.Value("enc_1")},
			},
			Usage: llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
		}},
	}
	fixture := &committedCompactionFixture{store: store, client: client}
	fixture.engine = mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:   "gpt-5",
		OnEvent: func(event Event) { fixture.events = append(fixture.events, event) },
	})
	if err := fixture.engine.steer("step-1", steerMessagesWithPersistenceIntent(steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	fixture.engine.compactionRuntimeState().SetSoonReminderIssued(true)
	if err := store.SetCompactionSoonReminderIssued(true); err != nil {
		t.Fatalf("persist compaction reminder: %v", err)
	}
	return fixture
}

func activeGoalCompactionTestClient() *fakeCompactionClient {
	return &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{{
		OutputItems: []llm.ResponseItem{
			{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")},
			{Type: llm.ResponseItemTypeCompaction, ID: textutil.Value("cmp_goal"), EncryptedContent: textutil.Value("enc_goal")},
		},
		Usage: llm.Usage{InputTokens: 1000, OutputTokens: 100, WindowTokens: 200000},
	}}}
}

func activeGoalContinuationMessages(items []llm.ResponseItem) []llm.Message {
	messages := llm.MessagesFromItems(items)
	out := make([]llm.Message, 0, 1)
	for _, message := range messages {
		if message.Role == llm.RoleDeveloper && message.MessageType != nil && *message.MessageType == llm.MessageTypeActiveGoalContinuation {
			out = append(out, message)
		}
	}
	return out
}

func assertSingleActiveGoalContinuation(t *testing.T, items []llm.ResponseItem, objective string) {
	messages := activeGoalContinuationMessages(items)
	if len(messages) != 1 {
		t.Fatalf("active-goal continuation count = %d, want 1; messages=%+v", len(messages), messages)
	}
	if messageContent(messages[0]) != prompts.RenderActiveGoalContinuationPrompt(objective) {
		t.Fatalf("active-goal continuation content = %q", messageContent(messages[0]))
	}
}

func workflowModeMessagesFromItems(items []llm.ResponseItem) []llm.Message {
	messages := llm.MessagesFromItems(items)
	out := make([]llm.Message, 0, 1)
	for _, message := range messages {
		if message.Role == llm.RoleDeveloper && message.MessageType != nil && *message.MessageType == llm.MessageTypeWorkflowMode {
			out = append(out, message)
		}
	}
	return out
}

func TestAutoCompactionRetries400ByCollapsingShellOutput(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		responses: []llm.Response{
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working")},
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
				},
				Usage: llm.Usage{InputTokens: 390000, OutputTokens: 1000, WindowTokens: 400000},
			},
			{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
				Usage:     llm.Usage{InputTokens: 2000, OutputTokens: 500, WindowTokens: 400000},
			},
		},
		compactionErrors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 400, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			nil,
		},
		compactionResponses: []llm.CompactionResponse{
			{
				OutputItems: []llm.ResponseItem{
					{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), Content: textutil.Value("run tools")},
					{Type: llm.ResponseItemTypeCompaction, ID: textutil.Value("cmp_1"), EncryptedContent: textutil.Value("enc_1")},
				},
				Usage: llm.Usage{InputTokens: 8000, OutputTokens: 500, WindowTokens: 400000},
			},
		},
	}

	largeOutput := json.RawMessage(`{"output":"` + strings.Repeat("x", 120_000) + `"}`)
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand, out: largeOutput}}), Config{Model: "gpt-5.3-codex"})

	msg, err := eng.SubmitUserMessage(context.Background(), "run tools")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if len(client.compactionCalls) != 2 {
		t.Fatalf("expected two compact calls (retry after 400), got %d", len(client.compactionCalls))
	}
	if len(client.compactionCalls[1].InputItems) != len(client.compactionCalls[0].InputItems) {
		t.Fatalf("expected repair to preserve item count, first=%d second=%d", len(client.compactionCalls[0].InputItems), len(client.compactionCalls[1].InputItems))
	}
	foundCollapsed := false
	for _, item := range client.compactionCalls[1].InputItems {
		if item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID != nil && *item.CallID == "call_1" {
			foundCollapsed = isCollapsedCompactionOverflowShellOutput(item.Output)
		}
	}
	if !foundCollapsed {
		t.Fatalf("expected repaired retry to collapse shell output, got %+v", client.compactionCalls[1].InputItems)
	}
}
