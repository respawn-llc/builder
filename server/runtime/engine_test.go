package runtime

import (
	"context"
	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/config"
	"core/shared/toolspec"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClient struct {
	mu        sync.Mutex
	responses []llm.Response
	errors    []error
	calls     []llm.Request
	caps      llm.ProviderCapabilities
	capsErr   error
}

type hookClient struct {
	mu           sync.Mutex
	response     llm.Response
	calls        []llm.Request
	caps         llm.ProviderCapabilities
	beforeReturn func() error
}

func requestMessages(req llm.Request) []llm.Message {
	return llm.MessagesFromItems(req.Items)
}

func (f *fakeClient) Generate(_ context.Context, req llm.Request) (llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if len(f.errors) > 0 {
		err := f.errors[0]
		f.errors = f.errors[1:]
		if err != nil {
			return llm.Response{}, err
		}
	}
	if len(f.responses) == 0 {
		return llm.Response{}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func (f *fakeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.capsErr != nil {
		return llm.ProviderCapabilities{}, f.capsErr
	}
	if strings.TrimSpace(f.caps.ProviderID) != "" {
		return f.caps, nil
	}
	return llm.ProviderCapabilities{
		ProviderID:                     "openai",
		SupportsResponsesAPI:           true,
		SupportsResponsesCompact:       true,
		SupportsRequestInputTokenCount: true,
		SupportsPromptCacheKey:         true,
		SupportsNativeWebSearch:        true,
		SupportsReasoningEncrypted:     true,
		SupportsServerSideContextEdit:  true,
		IsOpenAIFirstParty:             true,
	}, nil
}

func (c *hookClient) Generate(_ context.Context, req llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.calls = append(c.calls, req)
	beforeReturn := c.beforeReturn
	response := c.response
	c.mu.Unlock()
	if beforeReturn != nil {
		if err := beforeReturn(); err != nil {
			return llm.Response{}, err
		}
	}
	return response, nil
}

func (c *hookClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(c.caps.ProviderID) != "" {
		return c.caps, nil
	}
	return llm.ProviderCapabilities{
		ProviderID:                     "openai",
		SupportsResponsesAPI:           true,
		SupportsResponsesCompact:       true,
		SupportsRequestInputTokenCount: true,
		SupportsPromptCacheKey:         true,
		SupportsNativeWebSearch:        true,
		SupportsReasoningEncrypted:     true,
		SupportsServerSideContextEdit:  true,
		IsOpenAIFirstParty:             true,
	}, nil
}

type fakeCompactionClient struct {
	mu sync.Mutex

	responses []llm.Response
	errors    []error
	calls     []llm.Request

	inputTokenCount      int
	inputTokenCountFn    func(req llm.Request) int
	countInputTokenCalls int

	compactionResponses []llm.CompactionResponse
	compactionErr       error
	compactionErrors    []error
	compactionCalls     []llm.CompactionRequest

	caps llm.ProviderCapabilities
}

type preciseCompactionClient struct {
	inputTokenCount int
	contextWindow   int
	countErr        error
	countSupported  *bool
	supportErr      error

	countCalls   int
	resolveCalls int
}

func (c *preciseCompactionClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (c *preciseCompactionClient) CountRequestInputTokens(_ context.Context, _ llm.Request) (int, error) {
	c.countCalls++
	if c.countErr != nil {
		return 0, c.countErr
	}
	if c.inputTokenCount < 0 {
		return 0, nil
	}
	return c.inputTokenCount, nil
}

func (c *preciseCompactionClient) SupportsRequestInputTokenCount(_ context.Context) (bool, error) {
	if c.supportErr != nil {
		return false, c.supportErr
	}
	if c.countSupported != nil {
		return *c.countSupported, nil
	}
	return true, nil
}

func (c *preciseCompactionClient) ResolveModelContextWindow(_ context.Context, _ string) (int, error) {
	c.resolveCalls++
	if c.contextWindow <= 0 {
		return 0, nil
	}
	return c.contextWindow, nil
}

func (c *preciseCompactionClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	supportsExactCount := true
	if c.countSupported != nil {
		supportsExactCount = *c.countSupported
	}
	return llm.ProviderCapabilities{
		ProviderID:                     "openai",
		SupportsResponsesAPI:           true,
		SupportsResponsesCompact:       true,
		SupportsRequestInputTokenCount: supportsExactCount,
		SupportsPromptCacheKey:         true,
		SupportsNativeWebSearch:        true,
		SupportsReasoningEncrypted:     true,
		SupportsServerSideContextEdit:  true,
		IsOpenAIFirstParty:             true,
	}, nil
}

func (f *fakeCompactionClient) Generate(_ context.Context, req llm.Request) (llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if len(f.errors) > 0 {
		err := f.errors[0]
		f.errors = f.errors[1:]
		if err != nil {
			return llm.Response{}, err
		}
	}
	if len(f.responses) == 0 {
		return llm.Response{}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func (f *fakeCompactionClient) CountRequestInputTokens(_ context.Context, req llm.Request) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.countInputTokenCalls++
	if f.inputTokenCountFn != nil {
		count := f.inputTokenCountFn(req)
		if count < 0 {
			return 0, nil
		}
		return count, nil
	}
	if f.inputTokenCount < 0 {
		return 0, nil
	}
	return f.inputTokenCount, nil
}

func (f *fakeCompactionClient) Compact(_ context.Context, req llm.CompactionRequest) (llm.CompactionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.compactionCalls = append(f.compactionCalls, req)
	if len(f.compactionErrors) > 0 {
		err := f.compactionErrors[0]
		f.compactionErrors = f.compactionErrors[1:]
		if err != nil {
			return llm.CompactionResponse{}, err
		}
	}
	if f.compactionErr != nil {
		return llm.CompactionResponse{}, f.compactionErr
	}
	if len(f.compactionResponses) == 0 {
		return llm.CompactionResponse{}, nil
	}
	resp := f.compactionResponses[0]
	f.compactionResponses = f.compactionResponses[1:]
	return resp, nil
}

func (f *fakeCompactionClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	if strings.TrimSpace(f.caps.ProviderID) == "" {
		return llm.ProviderCapabilities{
			ProviderID:                     "openai",
			SupportsResponsesAPI:           true,
			SupportsResponsesCompact:       true,
			SupportsRequestInputTokenCount: true,
			SupportsPromptCacheKey:         true,
			SupportsNativeWebSearch:        true,
			SupportsReasoningEncrypted:     true,
			SupportsServerSideContextEdit:  true,
			IsOpenAIFirstParty:             true,
		}, nil
	}
	return f.caps, nil
}

type fakeTool struct {
	name  toolspec.ID
	delay time.Duration
	out   json.RawMessage
}

func (t fakeTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	time.Sleep(t.delay)
	if len(t.out) > 0 {
		return tools.Result{CallID: c.ID, Name: c.Name, Output: append(json.RawMessage(nil), t.out...)}, nil
	}
	out, _ := json.Marshal(map[string]any{"tool": string(t.name)})
	return tools.Result{CallID: c.ID, Name: c.Name, Output: out}, nil
}

type failingTool struct {
	name toolspec.ID
}

func (t failingTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	out, _ := json.Marshal(map[string]any{"error": "failed"})
	return tools.Result{CallID: c.ID, Name: c.Name, Output: out, IsError: true}, nil
}

type blockingTool struct {
	name    toolspec.ID
	started chan struct{}
	release chan struct{}
}

func (t blockingTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	select {
	case <-t.started:
	default:
		close(t.started)
	}
	<-t.release
	out, _ := json.Marshal(map[string]any{"tool": string(t.name)})
	return tools.Result{CallID: c.ID, Name: c.Name, Output: out}, nil
}

type serialPairProbeTool struct {
	firstID       string
	secondID      string
	firstStarted  chan struct{}
	secondStarted chan struct{}
	releaseFirst  chan struct{}
}

func (t *serialPairProbeTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	switch c.ID {
	case t.firstID:
		close(t.firstStarted)
		<-t.releaseFirst
	case t.secondID:
		close(t.secondStarted)
	}
	out, _ := json.Marshal(map[string]any{"tool": string(c.Name)})
	return tools.Result{CallID: c.ID, Name: c.Name, Output: out}, nil
}

type fakeStreamClient struct {
	mu       sync.Mutex
	attempts int
	calls    []llm.Request
}

type fakeAsyncLateDeltaClient struct{}

type fakeSimpleStreamClient struct{}

type fakeNoopStreamClient struct{}

type fakeReasoningStreamClient struct{}

func (f *fakeStreamClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func (f *fakeStreamClient) GenerateStream(_ context.Context, req llm.Request, onDelta func(string)) (llm.Response, error) {
	f.mu.Lock()
	attempt := f.attempts
	f.attempts++
	f.calls = append(f.calls, req)
	f.mu.Unlock()

	switch attempt {
	case 0:
		if onDelta != nil {
			onDelta("partial")
		}
		return llm.Response{}, errors.New("transient stream failure")
	default:
		if onDelta != nil {
			onDelta("final")
		}
		return llm.Response{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "final"},
			Usage:     llm.Usage{WindowTokens: 200000},
		}, nil
	}
}

func TestLastCommittedAssistantFinalAnswerSkipsTrailingReminderEntries(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "final handoff"}})); err != nil {
		t.Fatalf("append assistant final: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeCompactionSoonReminder, Content: "heads up"}})); err != nil {
		t.Fatalf("append reminder: %v", err)
	}

	if got := eng.LastCommittedAssistantFinalAnswer(); got != "final handoff" {
		t.Fatalf("LastCommittedAssistantFinalAnswer() = %q, want %q", got, "final handoff")
	}
}

func TestLastCommittedAssistantFinalAnswerSkipsTrailingErrorFeedback(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "final handoff"}})); err != nil {
		t.Fatalf("append assistant final: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeErrorFeedback, Content: "phase mismatch"}})); err != nil {
		t.Fatalf("append warning: %v", err)
	}

	if got := eng.LastCommittedAssistantFinalAnswer(); got != "final handoff" {
		t.Fatalf("LastCommittedAssistantFinalAnswer() = %q, want %q", got, "final handoff")
	}
}

func TestLastCommittedAssistantFinalAnswerSkipsTrailingHandoffFutureMessage(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "final handoff"}})); err != nil {
		t.Fatalf("append assistant final: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeHandoffFutureMessage, Content: "resume with tests"}})); err != nil {
		t.Fatalf("append handoff future message: %v", err)
	}

	if got := eng.LastCommittedAssistantFinalAnswer(); got != "final handoff" {
		t.Fatalf("LastCommittedAssistantFinalAnswer() = %q, want %q", got, "final handoff")
	}
}

func TestLastCommittedAssistantFinalAnswerSkipsTrailingReviewerFeedback(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "final handoff"}})); err != nil {
		t.Fatalf("append assistant final: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeReviewerFeedback, Content: "reviewer suggestions"}})); err != nil {
		t.Fatalf("append reviewer feedback: %v", err)
	}

	if got := eng.LastCommittedAssistantFinalAnswer(); got != "final handoff" {
		t.Fatalf("LastCommittedAssistantFinalAnswer() = %q, want %q", got, "final handoff")
	}
}

func TestLastCommittedAssistantFinalAnswerSkipsTrailingGoalFeedback(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "final handoff"}})); err != nil {
		t.Fatalf("append assistant final: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{normalizeMessageForTranscript(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeGoal, Content: prompts.RenderGoalSetPrompt("ship goal mode"), CompactContent: "Goal set: \"ship goal mode\""}, eng.transcriptWorkingDir())})); err != nil {
		t.Fatalf("append goal feedback: %v", err)
	}

	if got := eng.LastCommittedAssistantFinalAnswer(); got != "final handoff" {
		t.Fatalf("LastCommittedAssistantFinalAnswer() = %q, want %q", got, "final handoff")
	}
}

func TestLastCommittedAssistantFinalAnswerDoesNotSkipTrailingUntypedDeveloperMessage(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "final handoff"}})); err != nil {
		t.Fatalf("append assistant final: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleDeveloper, Content: "User ran shell command directly:\npwd"}})); err != nil {
		t.Fatalf("append developer message: %v", err)
	}

	if got := eng.LastCommittedAssistantFinalAnswer(); got != "" {
		t.Fatalf("LastCommittedAssistantFinalAnswer() = %q, want empty", got)
	}
}

func (fakeAsyncLateDeltaClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func (fakeAsyncLateDeltaClient) GenerateStream(_ context.Context, _ llm.Request, onDelta func(string)) (llm.Response, error) {
	if onDelta != nil {
		onDelta("final")
		go func() {
			time.Sleep(10 * time.Millisecond)
			onDelta("late")
		}()
	}
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "final"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

func (fakeSimpleStreamClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func (fakeSimpleStreamClient) GenerateStream(_ context.Context, _ llm.Request, onDelta func(string)) (llm.Response, error) {
	if onDelta != nil {
		onDelta("a")
		onDelta("b")
	}
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ab"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

func (fakeNoopStreamClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func (fakeNoopStreamClient) GenerateStream(_ context.Context, _ llm.Request, onDelta func(string)) (llm.Response, error) {
	if onDelta != nil {
		onDelta(reviewerNoopToken)
	}
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: reviewerNoopToken, Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

func (fakeReasoningStreamClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func (fakeReasoningStreamClient) GenerateStreamWithEvents(_ context.Context, _ llm.Request, callbacks llm.StreamCallbacks) (llm.Response, error) {
	if callbacks.OnReasoningSummaryDelta != nil {
		callbacks.OnReasoningSummaryDelta(llm.ReasoningSummaryDelta{Key: "rs_1:summary:0", Role: "reasoning", Text: "Plan"})
		callbacks.OnReasoningSummaryDelta(llm.ReasoningSummaryDelta{Key: "rs_1:summary:0", Role: "reasoning", Text: "Plan summary"})
	}
	if callbacks.OnAssistantDelta != nil {
		callbacks.OnAssistantDelta(llm.AssistantDelta{Text: "done", Phase: llm.MessagePhaseFinal})
	}
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
		Reasoning: []llm.ReasoningEntry{{Role: "reasoning", Text: "Plan summary"}},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

type authFailClient struct {
	mu    sync.Mutex
	calls int
}

func (c *authFailClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return llm.Response{}, &llm.APIStatusError{StatusCode: 401, Body: `{"error":"invalid_api_key"}`}
}

func (c *authFailClient) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type statusFailClient struct {
	mu     sync.Mutex
	calls  int
	status int
}

type providerContractFailClient struct {
	mu    sync.Mutex
	calls int
}

type streamRequiredClient struct {
	mu          sync.Mutex
	streamCalls int
	requests    []llm.Request
	response    llm.Response
}

func (c *streamRequiredClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	return llm.Response{}, &llm.APIStatusError{StatusCode: 400, Body: `{"detail":"Stream must be set to true"}`}
}

func (c *streamRequiredClient) GenerateStream(_ context.Context, req llm.Request, _ func(string)) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streamCalls++
	c.requests = append(c.requests, req)
	return c.response, nil
}

func (c *streamRequiredClient) StreamCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streamCalls
}

func (c *statusFailClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.calls++
	status := c.status
	c.mu.Unlock()
	return llm.Response{}, &llm.APIStatusError{StatusCode: status, Body: `{"error":"request_failed"}`}
}

func (c *statusFailClient) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *providerContractFailClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return llm.Response{}, &llm.ProviderAPIError{
		ProviderID: "openai",
		Code:       llm.UnifiedErrorCodeProviderContract,
		Message:    "provider contract is unavailable",
	}
}

func (c *providerContractFailClient) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestLocksAtFirstDispatch(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:         "gpt-5",
		Temperature:   1,
		ThinkingLevel: "xhigh",
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand},
		ToolPreambles: true,
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "hi"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	meta := store.Meta()
	if meta.Locked == nil {
		t.Fatalf("expected locked contract after first dispatch")
	}
	if meta.Locked.Model != "gpt-5" {
		t.Fatalf("locked model = %q", meta.Locked.Model)
	}
	if len(meta.Locked.EnabledTools) != 1 || meta.Locked.EnabledTools[0] != string(toolspec.ToolExecCommand) {
		t.Fatalf("locked enabled tools = %+v", meta.Locked.EnabledTools)
	}
	if meta.Locked.ToolPreambles == nil || !*meta.Locked.ToolPreambles {
		t.Fatalf("expected locked tool_preambles=true for normal session")
	}
	if !meta.Locked.ModelCapabilities.SupportsReasoningEffort {
		t.Fatalf("expected locked reasoning support for %q", meta.Locked.Model)
	}
	if !meta.Locked.ModelCapabilities.SupportsVisionInputs {
		t.Fatalf("expected locked vision support for %q", meta.Locked.Model)
	}
	if meta.Locked.ProviderContract.ProviderID != "openai" {
		t.Fatalf("expected locked openai provider contract, got %+v", meta.Locked.ProviderContract)
	}
	if !meta.Locked.ProviderContract.SupportsResponsesCompact || !meta.Locked.ProviderContract.IsOpenAIFirstParty {
		t.Fatalf("unexpected locked provider capabilities: %+v", meta.Locked.ProviderContract)
	}
}

func TestHeadlessSessionLocksToolPreamblesOff(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:         "gpt-5",
		Temperature:   1,
		ThinkingLevel: "high",
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand},
		HeadlessMode:  true,
		ToolPreambles: true,
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "hi"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	meta := store.Meta()
	if meta.Locked == nil {
		t.Fatalf("expected locked contract after first dispatch")
	}
	if meta.Locked.ToolPreambles == nil || *meta.Locked.ToolPreambles {
		t.Fatalf("expected locked tool_preambles=false for headless session")
	}
}

func TestLockedToolPreamblesPersistAcrossResume(t *testing.T) {
	store := mustCreateTestSession(t)

	firstClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "first"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	firstEngine := mustNewTestEngine(t, store, firstClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:         "gpt-5",
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand},
		ToolPreambles: false,
	})
	if _, err := firstEngine.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("submit first: %v", err)
	}
	if store.Meta().Locked == nil || store.Meta().Locked.ToolPreambles == nil || *store.Meta().Locked.ToolPreambles {
		t.Fatalf("expected first session to lock tool_preambles=false, got %+v", store.Meta().Locked)
	}

	resumedClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "second"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	resumedEngine := mustNewTestEngine(t, store, resumedClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:         "gpt-5",
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand},
		ToolPreambles: true,
	})
	if _, err := resumedEngine.SubmitUserMessage(context.Background(), "second"); err != nil {
		t.Fatalf("submit second: %v", err)
	}
	if store.Meta().Locked == nil || store.Meta().Locked.ToolPreambles == nil || *store.Meta().Locked.ToolPreambles {
		t.Fatalf("expected resumed session to preserve locked tool_preambles=false, got %+v", store.Meta().Locked)
	}
}

func TestLockedContextWindowKeepsSystemPromptToolCallEstimateStableAcrossResume(t *testing.T) {
	store := mustCreateTestSession(t)

	firstClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "first"},
		Usage:     llm.Usage{WindowTokens: 272_000},
	}}}
	firstEngine := mustNewTestEngine(t, store, firstClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:               "gpt-5",
		EnabledTools:        []toolspec.ID{toolspec.ToolExecCommand},
		ContextWindowTokens: 272_000,
	})
	if _, err := firstEngine.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("submit first: %v", err)
	}
	locked := store.Meta().Locked
	if locked == nil || locked.ContextWindow != 272_000 || locked.ContextPercent != 95 {
		t.Fatalf("expected locked context budget, got %+v", locked)
	}
	if got := firstEngine.estimatedToolCallsForLockedContext(*locked); got != 185 {
		t.Fatalf("estimated tool calls = %d, want 185", got)
	}
	firstPrompt := firstClient.calls[0].SystemPrompt
	if strings.TrimSpace(firstPrompt) == "" {
		t.Fatal("expected non-empty rendered system prompt")
	}
	firstPromptCacheKey := firstClient.calls[0].PromptCacheKey
	if firstPromptCacheKey == "" {
		t.Fatal("expected prompt cache key on first request")
	}

	resumedClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "second"},
		Usage:     llm.Usage{WindowTokens: 400_000},
	}}}
	resumedEngine := mustNewTestEngine(t, store, resumedClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:               "gpt-5",
		EnabledTools:        []toolspec.ID{toolspec.ToolExecCommand},
		ContextWindowTokens: 400_000,
	})
	if _, err := resumedEngine.SubmitUserMessage(context.Background(), "second"); err != nil {
		t.Fatalf("submit second: %v", err)
	}
	if strings.TrimSpace(resumedClient.calls[0].SystemPrompt) == "" {
		t.Fatal("expected resumed system prompt to stay non-empty")
	}
	if resumedClient.calls[0].PromptCacheKey != firstPromptCacheKey {
		t.Fatalf("expected resumed prompt cache key = %q, got %q", firstPromptCacheKey, resumedClient.calls[0].PromptCacheKey)
	}
	if got := resumedEngine.estimatedToolCallsForLockedContext(*store.Meta().Locked); got != 185 {
		t.Fatalf("resumed estimated tool calls = %d, want 185", got)
	}

	alteredLocked := *store.Meta().Locked
	alteredLocked.ContextWindow = 400_000
	if got := resumedEngine.estimatedToolCallsForLockedContext(alteredLocked); got != 271 {
		t.Fatalf("altered estimated tool calls = %d, want 271", got)
	}
	alteredPrompt, err := resumedEngine.systemPrompt(alteredLocked)
	if err != nil {
		t.Fatalf("altered system prompt: %v", err)
	}
	if alteredPrompt != firstPrompt {
		t.Fatal("expected locked system prompt snapshot to stay stable when locked context budget changes")
	}
}

func TestSystemPromptSnapshotUsesLocalFileAndSurvivesMidSessionFileChanges(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	for _, dir := range []string{filepath.Join(home, agentsGlobalDirName), filepath.Join(workspace, agentsGlobalDirName)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeTestFile(t, filepath.Join(home, agentsGlobalDirName, systemPromptFileName), "global system")
	localPath := filepath.Join(workspace, agentsGlobalDirName, systemPromptFileName)
	writeTestFile(t, localPath, "local {{.EstimatedToolCallsForContext}} {{.LaunchCommand}} run")

	store := mustCreateNamedTestSession(t, "ws", workspace)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "first"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                "gpt-5",
		EnabledTools:         []toolspec.ID{toolspec.ToolExecCommand},
		ContextWindowTokens:  272_000,
		TranscriptWorkingDir: workspace,
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("submit first: %v", err)
	}
	firstPrompt := client.calls[0].SystemPrompt
	if !strings.Contains(firstPrompt, "local 185 ") || strings.Contains(firstPrompt, "global system") || strings.Contains(firstPrompt, "{{") {
		t.Fatalf("unexpected first system prompt: %q", firstPrompt)
	}
	firstCacheKey := client.calls[0].PromptCacheKey
	if firstCacheKey == "" {
		t.Fatal("expected prompt cache key")
	}
	writeTestFile(t, localPath, "changed local system")
	if err := eng.Close(); err != nil {
		t.Fatalf("close first engine: %v", err)
	}

	reopened, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	reopenedClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "second"},
		Usage:     llm.Usage{WindowTokens: 400000},
	}}}
	reopenedEngine := mustNewTestEngine(t, reopened, reopenedClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                "gpt-5",
		EnabledTools:         []toolspec.ID{toolspec.ToolExecCommand},
		ContextWindowTokens:  400_000,
		TranscriptWorkingDir: workspace,
	})
	if _, err := reopenedEngine.SubmitUserMessage(context.Background(), "second"); err != nil {
		t.Fatalf("submit second: %v", err)
	}
	if got := reopenedClient.calls[0].SystemPrompt; got != firstPrompt {
		t.Fatalf("system prompt changed after SYSTEM.md edit\ngot: %q\nwant: %q", got, firstPrompt)
	}
	if got := reopenedClient.calls[0].PromptCacheKey; got != firstCacheKey {
		t.Fatalf("prompt cache key changed after SYSTEM.md edit: got %q want %q", got, firstCacheKey)
	}
	if got := reopened.Meta().Locked.SystemPrompt; got != firstPrompt {
		t.Fatalf("locked system prompt mismatch\ngot: %q\nwant: %q", got, firstPrompt)
	}
}

func TestSystemPromptSnapshotRefreshesAfterCompaction(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	systemDir := filepath.Join(workspace, agentsGlobalDirName)
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatalf("mkdir system dir: %v", err)
	}
	systemPath := filepath.Join(systemDir, systemPromptFileName)
	writeTestFile(t, systemPath, "prompt A")
	autoCompactionEnabled := false
	store := mustCreateNamedTestSession(t, "ws", workspace)
	client := &fakeClient{responses: []llm.Response{
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "first"}, Usage: llm.Usage{WindowTokens: 200000}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "second"}, Usage: llm.Usage{WindowTokens: 200000}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "summary"}, Usage: llm.Usage{WindowTokens: 200000}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "third"}, Usage: llm.Usage{WindowTokens: 200000}},
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		EnabledTools:          []toolspec.ID{toolspec.ToolExecCommand},
		CompactionMode:        "local",
		AutoCompactionEnabled: &autoCompactionEnabled,
		ToolPreambles:         false,
		TranscriptWorkingDir:  workspace,
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("submit first: %v", err)
	}
	if got := client.calls[0].SystemPrompt; got != "prompt A" {
		t.Fatalf("first system prompt = %q, want prompt A", got)
	}
	firstCacheKey := client.calls[0].PromptCacheKey
	writeTestFile(t, systemPath, "prompt B")
	if _, err := eng.SubmitUserMessage(context.Background(), "second"); err != nil {
		t.Fatalf("submit second: %v", err)
	}
	if got := client.calls[1].SystemPrompt; got != "prompt A" {
		t.Fatalf("pre-compaction system prompt = %q, want prompt A", got)
	}
	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if got := client.calls[2].SystemPrompt; got != "prompt A" {
		t.Fatalf("compaction system prompt = %q, want prompt A", got)
	}
	if _, err := eng.SubmitUserMessage(context.Background(), "third"); err != nil {
		t.Fatalf("submit third: %v", err)
	}
	if got := client.calls[3].SystemPrompt; got != "prompt B" {
		t.Fatalf("post-compaction system prompt = %q, want prompt B", got)
	}
	if got := client.calls[3].PromptCacheKey; got == "" || got == firstCacheKey {
		t.Fatalf("post-compaction cache key = %q, want rotated from %q", got, firstCacheKey)
	}
	if locked := store.Meta().Locked; locked == nil || !locked.HasSystemPrompt || locked.SystemPrompt != "prompt B" {
		t.Fatalf("locked prompt after refresh = %+v, want prompt B", locked)
	}
}

func TestSystemPromptRefreshFailureKeepsStaleLockAndRetries(t *testing.T) {
	workspace := t.TempDir()
	systemPath := filepath.Join(workspace, "system.md")
	writeTestFile(t, systemPath, "prompt A")
	autoCompactionEnabled := false
	store := mustCreateTestSession(t, workspace)
	client := &fakeClient{responses: []llm.Response{
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "first"}, Usage: llm.Usage{WindowTokens: 200000}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "summary"}, Usage: llm.Usage{WindowTokens: 200000}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "second"}, Usage: llm.Usage{WindowTokens: 200000}},
	}}
	eng := mustNewExecTestEngine(t, store, client, Config{
		CompactionMode:        "local",
		AutoCompactionEnabled: &autoCompactionEnabled,
		ToolPreambles:         false,
		SystemPromptFiles: []config.SystemPromptFile{
			{Path: systemPath, Scope: config.SystemPromptFileScopeWorkspaceConfig},
		},
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("submit first: %v", err)
	}
	writeTestFile(t, systemPath, "prompt B {{")
	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if _, err := eng.SubmitUserMessage(context.Background(), "fails"); err == nil {
		t.Fatal("expected invalid prompt refresh to fail")
	}
	if locked := store.Meta().Locked; locked == nil || locked.HasSystemPrompt || strings.TrimSpace(locked.SystemPrompt) != "" {
		t.Fatalf("locked prompt after failed refresh = %+v, want stale cleared lock", locked)
	}
	writeTestFile(t, systemPath, "prompt B")
	if _, err := eng.SubmitUserMessage(context.Background(), "second"); err != nil {
		t.Fatalf("submit after prompt fix: %v", err)
	}
	if got := client.calls[len(client.calls)-1].SystemPrompt; got != "prompt B" {
		t.Fatalf("system prompt after retry = %q, want prompt B", got)
	}
}

func TestPendingSystemPromptRefreshRunsAfterReopen(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	systemDir := filepath.Join(workspace, agentsGlobalDirName)
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatalf("mkdir system dir: %v", err)
	}
	systemPath := filepath.Join(systemDir, systemPromptFileName)
	writeTestFile(t, systemPath, "prompt A")
	autoCompactionEnabled := false
	store := mustCreateTestSession(t, workspace)
	client := &fakeClient{responses: []llm.Response{
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "first"}, Usage: llm.Usage{WindowTokens: 200000}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "summary"}, Usage: llm.Usage{WindowTokens: 200000}},
	}}
	eng := mustNewExecTestEngine(t, store, client, Config{
		CompactionMode:        "local",
		AutoCompactionEnabled: &autoCompactionEnabled,
		ToolPreambles:         false,
		TranscriptWorkingDir:  workspace,
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "first"); err != nil {
		t.Fatalf("submit first: %v", err)
	}
	writeTestFile(t, systemPath, "prompt B")
	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if locked := store.Meta().Locked; locked == nil || locked.HasSystemPrompt || locked.SystemPrompt != "" {
		t.Fatalf("locked prompt after compaction = %+v, want stale", locked)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}
	reopenedStore, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	reopenedClient := &fakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "second"}, Usage: llm.Usage{WindowTokens: 200000}}}}
	reopened := mustNewExecTestEngine(t, reopenedStore, reopenedClient, Config{
		ToolPreambles:        false,
		TranscriptWorkingDir: workspace,
	})
	if _, err := reopened.SubmitUserMessage(context.Background(), "second"); err != nil {
		t.Fatalf("submit after reopen: %v", err)
	}
	if got := reopenedClient.calls[0].SystemPrompt; got != "prompt B" {
		t.Fatalf("reopened system prompt = %q, want prompt B", got)
	}
	if got, want := reopenedClient.calls[0].PromptCacheKey, conversationPromptCacheKey(reopened.SessionID(), 1); got != want {
		t.Fatalf("reopened cache key = %q, want %q", got, want)
	}
}

func TestLegacyNonBooleanSystemPromptSnapshotIsNotRefreshed(t *testing.T) {
	store := mustCreateTestSession(t)
	if err := store.MarkModelDispatchLocked(session.LockedContract{
		Model:           "gpt-5",
		SystemPrompt:    "legacy prompt",
		HasSystemPrompt: false,
	}); err != nil {
		t.Fatalf("mark locked: %v", err)
	}
	client := &fakeClient{responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok"}, Usage: llm.Usage{WindowTokens: 200000}}}}
	eng := mustNewExecTestEngine(t, store, client, Config{SystemPromptFiles: []config.SystemPromptFile{{Path: filepath.Join(t.TempDir(), "new.md"), Scope: config.SystemPromptFileScopeWorkspaceConfig}}})
	if _, err := eng.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := client.calls[0].SystemPrompt; got != "legacy prompt" {
		t.Fatalf("system prompt = %q, want legacy prompt", got)
	}
}

func TestLockedRequestShapeSurvivesRuntimeConfigToolAndWebSearchToggles(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(
		tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}},
		tools.HandlerRegistration{ID: toolspec.ToolAskQuestion, Handler: fakeTool{name: toolspec.ToolAskQuestion}},
		tools.HandlerRegistration{ID: toolspec.ToolWebSearch, Handler: fakeTool{name: toolspec.ToolWebSearch}},
	), Config{
		Model:         "gpt-5",
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolWebSearch},
		WebSearchMode: "native",
		ToolPreambles: false,
	})
	if _, err := eng.ensureLocked(); err != nil {
		t.Fatalf("ensureLocked: %v", err)
	}
	eng.cfg.EnabledTools = []toolspec.ID{toolspec.ToolAskQuestion}
	eng.cfg.WebSearchMode = "off"

	requestTools, err := eng.requestTools(context.Background(), "")
	if err != nil {
		t.Fatalf("requestTools: %v", err)
	}
	names := make([]string, 0, len(requestTools))
	for _, tool := range requestTools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != string(toolspec.ToolExecCommand) {
		t.Fatalf("request tool names = %v, want locked exec_command only", names)
	}
	native, err := eng.enableNativeWebSearch(context.Background())
	if err != nil {
		t.Fatalf("enable native web search: %v", err)
	}
	if !native {
		t.Fatal("expected locked native web search to remain enabled")
	}
}

func TestReadSystemPromptTemplateUsesGlobalFileWhenLocalMissing(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, agentsGlobalDirName)
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir global dir: %v", err)
	}
	writeTestFile(t, filepath.Join(globalDir, systemPromptFileName), "global system")

	template, sourcePath, ok, err := readSystemPromptTemplate(systemPromptSnapshotOptions{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("read system prompt template: %v", err)
	}
	if !ok || template != "global system" {
		t.Fatalf("template = %q ok=%t, want global system true", template, ok)
	}
	if want := filepath.Join(globalDir, systemPromptFileName); sourcePath != want {
		t.Fatalf("source path = %q, want %q", sourcePath, want)
	}
}
