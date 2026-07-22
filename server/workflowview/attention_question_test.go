package workflowview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"core/server/runtime"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionview"
	askquestion "core/server/tools"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/toolspec"
)

func TestAttentionQuestionRecoveryUsesLivePrompt(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	sessionID := "session-live-prompt"
	askID := "ask-live-prompt"
	task, started := createWorkflowViewWaitingAskTask(t, ctx, metadataStore, workflowStore, binding, sessionID, askID)
	attention, err := NewAttention(metadataStore.Queries(), NewTaskProjector(), nil, staticPendingPromptSource{sessionID: {{
		Request: askquestion.AskQuestionRequest{
			ID:                     askID,
			Question:               "Choose a release channel",
			Suggestions:            []string{"stable", "preview"},
			RecommendedOptionIndex: 1,
		},
	}}})
	if err != nil {
		t.Fatalf("NewAttention: %v", err)
	}

	response, err := attention.ListTask(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(task.ID)})
	if err != nil {
		t.Fatalf("ListTask: %v", err)
	}
	if len(response.Items) != 1 {
		t.Fatalf("attention items = %+v", response.Items)
	}
	item := response.Items[0]
	if !attentionStringEquals(item.RunID, string(started.RunID)) ||
		!attentionStringEquals(item.AskID, askID) ||
		item.Question == nil ||
		item.Question.Kind != serverapi.WorkflowAttentionQuestionKindOrdinary ||
		len(item.Suggestions) != 2 ||
		!attentionIntEquals(item.RecommendedOptionIndex, 1) {
		t.Fatalf("live prompt attention = %+v", item)
	}
}

func TestAttentionQuestionRecoveryUsesDormantNewestActiveSegment(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	persistence := sessiontest.NewPersistence()
	sessionStore, err := session.Create(t.TempDir(), "attention", t.TempDir(), sessioncontract.SessionCategoryMain, persistence.Options()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	eventLog, err := sessionStore.MaterializeEventLog()
	if err != nil {
		t.Fatalf("MaterializeEventLog: %v", err)
	}
	askID := "ask-dormant"
	oldStepID := "step-old"
	if _, _, err := eventLog.AppendRecord(&oldStepID, session.MessageRecord{
		Role: session.MessageRoleAssistant,
		ToolCalls: []session.MessageToolCallRecord{{
			CallID: askID,
			Name:   string(toolspec.ToolAskQuestion),
			Kind:   session.ToolCallKindFunction,
			Input:  json.RawMessage(`{"question":"stale question"}`),
		}},
	}); err != nil {
		t.Fatalf("AppendRecord stale question: %v", err)
	}
	compactionStepID := "step-compaction"
	if _, _, err := eventLog.AppendCompactionHistoryReplacement(&compactionStepID, session.HistoryReplacementRecord{
		Engine: "local",
		Mode:   session.CompactionModeAuto,
	}); err != nil {
		t.Fatalf("AppendCompactionHistoryReplacement: %v", err)
	}
	input, err := json.Marshal(askquestion.AskQuestionToolRequest{
		Question:               "Resume dormant work?",
		Suggestions:            []string{"yes", "no"},
		RecommendedOptionIndex: 2,
	})
	if err != nil {
		t.Fatalf("marshal ask request: %v", err)
	}
	dormantStepID := "step-dormant"
	if _, _, err := eventLog.AppendRecord(&dormantStepID, session.MessageRecord{
		Role: session.MessageRoleAssistant,
		ToolCalls: []session.MessageToolCallRecord{{
			CallID: askID,
			Name:   string(toolspec.ToolAskQuestion),
			Kind:   session.ToolCallKindFunction,
			Input:  input,
		}},
	}); err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}
	task, _ := createWorkflowViewWaitingAskTask(t, ctx, metadataStore, workflowStore, binding, sessionStore.Meta().SessionID, askID)
	sessionViews := sessionview.NewService(singleSessionStoreResolver{store: sessionStore}, nil, nil, nil)
	attention, err := NewAttention(metadataStore.Queries(), NewTaskProjector(), sessionViewActiveTranscriptProvider{views: sessionViews}, nil)
	if err != nil {
		t.Fatalf("NewAttention: %v", err)
	}

	response, err := attention.ListTask(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(task.ID)})
	if err != nil {
		t.Fatalf("ListTask: %v", err)
	}
	if len(response.Items) != 1 ||
		!attentionStringEquals(response.Items[0].AskID, askID) ||
		response.Items[0].Question == nil ||
		len(response.Items[0].Suggestions) != 2 ||
		!attentionIntEquals(response.Items[0].RecommendedOptionIndex, 2) {
		t.Fatalf("dormant transcript attention = %+v", response.Items)
	}
}

func TestAttentionQuestionRecoveryFallsBackLocally(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	task, _ := createWorkflowViewWaitingAskTask(t, ctx, metadataStore, workflowStore, binding, "session-fallback", "ask-fallback")
	transcripts := &failingActiveTranscriptProvider{}
	attention, err := NewAttention(metadataStore.Queries(), NewTaskProjector(), transcripts, nil)
	if err != nil {
		t.Fatalf("NewAttention: %v", err)
	}

	response, err := attention.ListTask(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(task.ID)})
	if err != nil {
		t.Fatalf("ListTask: %v", err)
	}
	if transcripts.calls != 1 ||
		len(response.Items) != 1 ||
		!attentionStringEquals(response.Items[0].AskID, "ask-fallback") ||
		response.Items[0].Message != pendingQuestionFallbackMessage {
		t.Fatalf("fallback attention = %+v, transcript calls=%d", response.Items, transcripts.calls)
	}
}

type singleSessionStoreResolver struct {
	store *session.Store
}

func (r singleSessionStoreResolver) ResolveSessionStore(_ context.Context, sessionID string) (*session.Store, error) {
	if r.store == nil || strings.TrimSpace(sessionID) != r.store.Meta().SessionID {
		return nil, errors.New("session is unavailable")
	}
	return r.store, nil
}

type sessionViewActiveTranscriptProvider struct {
	views *sessionview.Service
}

func (p sessionViewActiveTranscriptProvider) SessionNewestActiveSegmentEntries(ctx context.Context, sessionID string) ([]runtime.ChatEntry, error) {
	return p.views.SessionTranscriptTailEntries(ctx, sessionID)
}

type failingActiveTranscriptProvider struct {
	calls int
}

func (p *failingActiveTranscriptProvider) SessionNewestActiveSegmentEntries(context.Context, string) ([]runtime.ChatEntry, error) {
	p.calls++
	return nil, errors.New("transcript unavailable")
}
