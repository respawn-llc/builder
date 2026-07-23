package workflowview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
		ID:                     askID,
		Question:               "Choose a release channel",
		Suggestions:            []string{"stable", "preview"},
		RecommendedOptionIndex: attentionQuestionInt(1),
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
	if !attentionPointerEquals(item.RunID, string(started.RunID)) ||
		!attentionPointerEquals(item.AskID, askID) ||
		item.Question == nil ||
		item.Question.Kind != serverapi.WorkflowAttentionQuestionKindOrdinary ||
		len(item.Suggestions) != 2 ||
		!attentionPointerEquals(item.RecommendedOptionIndex, 1) {
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
		!attentionPointerEquals(response.Items[0].AskID, askID) ||
		response.Items[0].Question == nil ||
		len(response.Items[0].Suggestions) != 2 ||
		!attentionPointerEquals(response.Items[0].RecommendedOptionIndex, 2) {
		t.Fatalf("dormant transcript attention = %+v", response.Items)
	}
}

func TestAttentionQuestionRecoveryPropagatesTranscriptFailure(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	task, _ := createWorkflowViewWaitingAskTask(t, ctx, metadataStore, workflowStore, binding, "session-fallback", "ask-fallback")
	sourceErr := errors.New("transcript unavailable")
	transcripts := &failingActiveTranscriptProvider{err: sourceErr}
	attention, err := NewAttention(metadataStore.Queries(), NewTaskProjector(), transcripts, nil)
	if err != nil {
		t.Fatalf("NewAttention: %v", err)
	}

	_, err = attention.ListTask(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(task.ID)})
	if !errors.Is(err, sourceErr) {
		t.Fatalf("ListTask error = %v, want transcript error %v", err, sourceErr)
	}
	if transcripts.calls != 1 {
		t.Fatalf("transcript calls = %d, want 1", transcripts.calls)
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

func (p sessionViewActiveTranscriptProvider) SessionNewestActiveSegmentQuestions(ctx context.Context, sessionID string) ([]PendingQuestionTranscriptEntry, error) {
	entries, err := p.views.SessionTranscriptTailEntries(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	questions := make([]PendingQuestionTranscriptEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Role != "tool_call" ||
			entry.ToolCall == nil ||
			entry.ToolCall.ToolName != string(toolspec.ToolAskQuestion) {
			continue
		}
		questions = append(questions, PendingQuestionTranscriptEntry{
			AskID:                  entry.ToolCallID,
			Question:               entry.ToolCall.Question,
			Suggestions:            append([]string(nil), entry.ToolCall.Suggestions...),
			RecommendedOptionIndex: legacyRecommendedOptionIndex(entry.ToolCall.RecommendedOptionIndex),
		})
	}
	return questions, nil
}

type failingActiveTranscriptProvider struct {
	calls int
	err   error
}

func (p *failingActiveTranscriptProvider) SessionNewestActiveSegmentQuestions(context.Context, string) ([]PendingQuestionTranscriptEntry, error) {
	p.calls++
	return nil, p.err
}

func attentionQuestionInt(value int) *int {
	return &value
}

func legacyRecommendedOptionIndex(index int) *int {
	if index < 1 {
		return nil
	}
	return &index
}
