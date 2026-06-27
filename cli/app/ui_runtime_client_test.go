package app

import (
	"context"
	"core/server/llm"
	"core/server/registry"
	"core/server/runtime"
	"core/server/runtimecontrol"
	"core/server/sessionview"
	"core/server/tools"
	sharedclient "core/shared/client"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/toolspec"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countingSessionViewClient struct {
	view              clientui.RuntimeMainView
	page              clientui.TranscriptPage
	suffix            clientui.CommittedTranscriptSuffix
	suffixErr         error
	pageForRequest    func(serverapi.SessionTranscriptPageRequest) clientui.TranscriptPage
	count             atomic.Int32
	mainViewCount     atomic.Int32
	pageCount         atomic.Int32
	suffixCount       atomic.Int32
	lastTranscriptReq serverapi.SessionTranscriptPageRequest
	lastSuffixReq     serverapi.SessionCommittedTranscriptSuffixRequest
}

type runtimeClientWithoutCachedMainView struct {
	clientui.RuntimeClient
	mainView      clientui.RuntimeMainView
	mainViewCalls int
	suffixReq     clientui.CommittedTranscriptSuffixRequest
}

func (c *runtimeClientWithoutCachedMainView) MainView() clientui.RuntimeMainView {
	c.mainViewCalls++
	return c.mainView
}

func (c *runtimeClientWithoutCachedMainView) RefreshCommittedTranscriptSuffix(req clientui.CommittedTranscriptSuffixRequest) (clientui.CommittedTranscriptSuffix, error) {
	c.suffixReq = req
	return clientui.CommittedTranscriptSuffix{
		SessionID:             c.mainView.Session.SessionID,
		Revision:              c.mainView.Session.Transcript.Revision,
		CommittedEntryCount:   c.mainView.Session.Transcript.CommittedEntryCount,
		NextEntryCount:        c.mainView.Session.Transcript.CommittedEntryCount,
		ConversationFreshness: c.mainView.Session.ConversationFreshness,
	}, nil
}

func (c *countingSessionViewClient) GetSessionMainView(context.Context, serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	c.count.Add(1)
	c.mainViewCount.Add(1)
	return serverapi.SessionMainViewResponse{MainView: c.view}, nil
}

func (c *countingSessionViewClient) GetSessionTranscriptPage(ctx context.Context, req serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	_ = ctx
	c.lastTranscriptReq = req
	c.count.Add(1)
	c.pageCount.Add(1)
	if c.pageForRequest != nil {
		return serverapi.SessionTranscriptPageResponse{Transcript: c.pageForRequest(req)}, nil
	}
	return serverapi.SessionTranscriptPageResponse{Transcript: c.page}, nil
}

func (c *countingSessionViewClient) GetSessionCommittedTranscriptSuffix(ctx context.Context, req serverapi.SessionCommittedTranscriptSuffixRequest) (serverapi.SessionCommittedTranscriptSuffixResponse, error) {
	_ = ctx
	c.lastSuffixReq = req
	c.count.Add(1)
	c.suffixCount.Add(1)
	if c.suffixErr != nil {
		return serverapi.SessionCommittedTranscriptSuffixResponse{}, c.suffixErr
	}
	return serverapi.SessionCommittedTranscriptSuffixResponse{Suffix: c.suffix}, nil
}

type blockingSessionViewClient struct{}

func (blockingSessionViewClient) GetSessionMainView(ctx context.Context, _ serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	<-ctx.Done()
	return serverapi.SessionMainViewResponse{}, ctx.Err()
}

func (blockingSessionViewClient) GetSessionTranscriptPage(ctx context.Context, _ serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	<-ctx.Done()
	return serverapi.SessionTranscriptPageResponse{}, ctx.Err()
}

type blockingCountingSessionViewClient struct {
	count atomic.Int32
}

func (c *blockingCountingSessionViewClient) GetSessionMainView(ctx context.Context, _ serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	c.count.Add(1)
	<-ctx.Done()
	return serverapi.SessionMainViewResponse{}, ctx.Err()
}

func (c *blockingCountingSessionViewClient) GetSessionTranscriptPage(ctx context.Context, _ serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	<-ctx.Done()
	return serverapi.SessionTranscriptPageResponse{}, ctx.Err()
}

type mutableRuntimeResolver struct {
	mu     sync.Mutex
	engine *runtime.Engine
}

func (r *mutableRuntimeResolver) Set(engine *runtime.Engine) {
	r.mu.Lock()
	r.engine = engine
	r.mu.Unlock()
}

func (r *mutableRuntimeResolver) ResolveRuntime(context.Context, string) (*runtime.Engine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.engine, nil
}

type flakySessionViewClient struct {
	mu        sync.Mutex
	responses []serverapi.SessionMainViewResponse
	pages     []serverapi.SessionTranscriptPageResponse
	errs      []error
	count     int
}

func (c *flakySessionViewClient) GetSessionMainView(context.Context, serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.count
	c.count++
	if idx < len(c.errs) && c.errs[idx] != nil {
		return serverapi.SessionMainViewResponse{}, c.errs[idx]
	}
	if idx < len(c.responses) {
		return c.responses[idx], nil
	}
	if len(c.responses) > 0 {
		return c.responses[len(c.responses)-1], nil
	}
	return serverapi.SessionMainViewResponse{}, nil
}

func (c *flakySessionViewClient) GetSessionTranscriptPage(context.Context, serverapi.SessionTranscriptPageRequest) (serverapi.SessionTranscriptPageResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.count
	c.count++
	if idx < len(c.errs) && c.errs[idx] != nil {
		return serverapi.SessionTranscriptPageResponse{}, c.errs[idx]
	}
	if idx < len(c.pages) {
		return c.pages[idx], nil
	}
	if len(c.pages) > 0 {
		return c.pages[len(c.pages)-1], nil
	}
	return serverapi.SessionTranscriptPageResponse{}, nil
}

type runtimeClientFakeLLM struct {
	mu        sync.Mutex
	responses []llm.Response
}

func (f *runtimeClientFakeLLM) Generate(_ context.Context, req llm.Request) (llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.responses) == 0 {
		return llm.Response{}, nil
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func (f *runtimeClientFakeLLM) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:                    "openai",
		SupportsResponsesAPI:          true,
		SupportsResponsesCompact:      true,
		SupportsReasoningEncrypted:    true,
		SupportsServerSideContextEdit: true,
		IsOpenAIFirstParty:            true,
	}, nil
}

type runtimeClientBlockingTool struct {
	started chan struct{}
	release chan struct{}
}

func (t runtimeClientBlockingTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	select {
	case <-t.started:
	default:
		close(t.started)
	}
	<-t.release
	out, _ := json.Marshal(map[string]any{"ok": true})
	return tools.Result{CallID: c.ID, Name: c.Name, Output: out}, nil
}

func newRuntimeClientReadTest(reads sharedclient.SessionViewClient) clientui.RuntimeClient {
	return newUIRuntimeClientWithReads(
		"session-1",
		reads,
		sharedclient.NewLoopbackRuntimeControlClient(runtimecontrol.NewService(registry.NewRuntimeRegistry())),
	)
}

func newRuntimeClientReadOnlyTest(reads sharedclient.SessionViewClient) clientui.RuntimeClient {
	return newUIRuntimeClientWithReads(
		"session-1",
		reads,
		sharedclient.NewLoopbackRuntimeControlClient(runtimecontrol.NewService(nil)),
	)
}

func TestRuntimeClientRefreshTranscriptRequestsRecentTail(t *testing.T) {
	reads := &countingSessionViewClient{page: clientui.TranscriptPage{SessionID: "session-1"}}
	runtimeClient := newRuntimeClientReadOnlyTest(reads)

	if _, err := runtimeClient.RefreshTranscript(); err != nil {
		t.Fatalf("refresh transcript: %v", err)
	}
	if reads.lastTranscriptReq.Cursor != 0 {
		t.Fatalf("request cursor = %d, want recent-tail (zero cursor)", reads.lastTranscriptReq.Cursor)
	}
}

func TestRuntimeClientLoadTranscriptPageLetsServerApplyDefaultWindow(t *testing.T) {
	reads := &countingSessionViewClient{page: clientui.TranscriptPage{SessionID: "session-1"}}
	runtimeClient := newRuntimeClientReadOnlyTest(reads)

	if _, err := runtimeClient.LoadTranscriptPage(clientui.TranscriptPageRequest{}); err != nil {
		t.Fatalf("load transcript page: %v", err)
	}
	if reads.lastTranscriptReq.Cursor != 0 {
		t.Fatalf("request cursor = %d, want recent-tail (zero cursor)", reads.lastTranscriptReq.Cursor)
	}
}

func TestRuntimeClientRefreshCommittedTranscriptSuffixUsesSessionViewSuffixAPI(t *testing.T) {
	reads := &countingSessionViewClient{
		suffix: clientui.CommittedTranscriptSuffix{
			SessionID:             "session-1",
			Revision:              12,
			CommittedEntryCount:   5,
			StartEntryCount:       2,
			NextEntryCount:        4,
			HasMore:               true,
			Entries:               []clientui.ChatEntry{{Role: "assistant", Text: "reply-002"}, {Role: "assistant", Text: "reply-003"}},
			ConversationFreshness: clientui.ConversationFreshnessEstablished,
		},
	}
	runtimeClient := newRuntimeClientReadOnlyTest(reads).(*sessionRuntimeClient)

	suffix, err := runtimeClient.RefreshCommittedTranscriptSuffix(clientui.CommittedTranscriptSuffixRequest{})
	if err != nil {
		t.Fatalf("refresh committed transcript suffix: %v", err)
	}
	if reads.lastSuffixReq.SessionID != "session-1" {
		t.Fatalf("unexpected suffix request: %+v", reads.lastSuffixReq)
	}
	if reads.lastTranscriptReq != (serverapi.SessionTranscriptPageRequest{}) {
		t.Fatalf("did not expect transcript page request, got %+v", reads.lastTranscriptReq)
	}
	if suffix.StartEntryCount != 2 || suffix.NextEntryCount != 4 || len(suffix.Entries) != 2 {
		t.Fatalf("unexpected suffix response: %+v", suffix)
	}
	cached := runtimeClient.SessionView()
	if cached.Transcript.Revision != 12 || cached.Transcript.CommittedEntryCount != 5 {
		t.Fatalf("cached transcript metadata = %+v, want revision 12 count 5", cached.Transcript)
	}
}

func TestRuntimeClientCommittedSuffixDisablesUnsupportedRPC(t *testing.T) {
	reads := &countingSessionViewClient{
		page: clientui.TranscriptPage{
			SessionID:    "session-1",
			Revision:     7,
			TotalEntries: 4,
			Offset:       2,
			Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "page fallback"}},
		},
		suffixErr: serverapi.ErrMethodNotFound,
	}
	runtimeClient := newRuntimeClientReadOnlyTest(reads).(*sessionRuntimeClient)
	req := clientui.CommittedTranscriptSuffixRequest{}

	suffix, err := runtimeClient.RefreshCommittedTranscriptSuffix(req)
	if err != nil {
		t.Fatalf("refresh committed transcript suffix fallback: %v", err)
	}
	if suffix.StartEntryCount != 2 || suffix.NextEntryCount != 3 || suffix.Entries[0].Text != "page fallback" {
		t.Fatalf("unexpected fallback suffix: %+v", suffix)
	}
	if reads.suffixCount.Load() != 1 || reads.pageCount.Load() != 1 {
		t.Fatalf("first refresh counts suffix=%d page=%d, want 1/1", reads.suffixCount.Load(), reads.pageCount.Load())
	}

	reads.suffixErr = nil
	reads.suffix = clientui.CommittedTranscriptSuffix{
		SessionID:           "session-1",
		CommittedEntryCount: 4,
		StartEntryCount:     2,
		NextEntryCount:      3,
		Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "rpc should stay disabled"}},
	}
	suffix, err = runtimeClient.RefreshCommittedTranscriptSuffix(req)
	if err != nil {
		t.Fatalf("second refresh committed transcript suffix fallback: %v", err)
	}
	if suffix.Entries[0].Text != "page fallback" {
		t.Fatalf("expected cached unsupported capability to keep page fallback, got %+v", suffix)
	}
	if reads.suffixCount.Load() != 1 || reads.pageCount.Load() != 2 {
		t.Fatalf("second refresh counts suffix=%d page=%d, want 1/2", reads.suffixCount.Load(), reads.pageCount.Load())
	}
}

func TestStartupRuntimeTranscriptSeedsFromCommittedSuffix(t *testing.T) {
	reads := &countingSessionViewClient{
		view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{
			SessionID: "session-1",
			Transcript: clientui.TranscriptMetadata{
				Revision:            10,
				CommittedEntryCount: 600,
			},
		}},
		suffix: clientui.CommittedTranscriptSuffix{
			SessionID:           "session-1",
			Revision:            10,
			CommittedEntryCount: 600,
			StartEntryCount:     100,
			NextEntryCount:      101,
			HasMore:             true,
			Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "reply-100"}},
		},
	}
	runtimeClient := newRuntimeClientReadOnlyTest(reads)

	model := NewProjectedUIModel(runtimeClient, closedProjectedRuntimeEvents(), closedAskEvents()).(*uiModel)

	if reads.lastSuffixReq.SessionID != "session-1" {
		t.Fatalf("startup suffix request = %+v, want session-1", reads.lastSuffixReq)
	}
	if reads.lastTranscriptReq != (serverapi.SessionTranscriptPageRequest{}) {
		t.Fatalf("did not expect startup seed to use transcript page request, got %+v", reads.lastTranscriptReq)
	}
	if model.transcriptBaseOffset != 100 || model.transcriptTotalEntries != 600 {
		t.Fatalf("startup transcript metadata base=%d total=%d, want base 100 total 600", model.transcriptBaseOffset, model.transcriptTotalEntries)
	}
	if got := len(model.transcriptEntries); got != 1 {
		t.Fatalf("startup transcript entries = %d, want 1", got)
	}
	if model.transcriptEntries[0].Text != "reply-100" {
		t.Fatalf("startup transcript entry = %+v, want reply-100", model.transcriptEntries[0])
	}
}

func newSeededCommittedSuffixTestModel(t *testing.T) (*uiModel, *countingSessionViewClient) {
	t.Helper()
	reads := &countingSessionViewClient{
		view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{
			SessionID:  "session-1",
			Transcript: clientui.TranscriptMetadata{Revision: 1, CommittedEntryCount: 1},
		}},
		suffix: clientui.CommittedTranscriptSuffix{
			SessionID:           "session-1",
			Revision:            1,
			CommittedEntryCount: 1,
			StartEntryCount:     0,
			NextEntryCount:      1,
			Entries:             []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
		},
	}
	runtimeClient := newRuntimeClientReadOnlyTest(reads)
	model := NewProjectedUIModel(runtimeClient, closedProjectedRuntimeEvents(), closedAskEvents()).(*uiModel)
	model.termWidth = 100
	model.termHeight = 20
	model.windowSizeKnown = true
	return model, reads
}

func TestContiguousCommittedRuntimeEventAppliesFromEventWithoutServerRead(t *testing.T) {
	model, reads := newSeededCommittedSuffixTestModel(t)
	baselineReads := reads.suffixCount.Load()

	result := model.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         2,
		CommittedEntryCount:        2,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "answer", Phase: string(llm.MessagePhaseFinal)}},
	}).
		cmd
	for _, msg := range collectCmdMessages(t, result) {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("contiguous committed event requested transcript hydration: %+v", msg)
		}
	}
	if got := reads.suffixCount.Load(); got != baselineReads {
		t.Fatalf("server suffix reads = %d, want unchanged baseline %d (no disk re-read on contiguous event)", got, baselineReads)
	}
	if model.transcriptTotalEntries != 2 {
		t.Fatalf("committed total entries = %d, want 2 after local event apply", model.transcriptTotalEntries)
	}
}

func TestCommittedRuntimeEventWithForwardGapFallsBackToServerRead(t *testing.T) {
	model, reads := newSeededCommittedSuffixTestModel(t)
	baselineSuffixReads := reads.suffixCount.Load()
	baselinePageReads := reads.pageCount.Load()
	reads.page = clientui.TranscriptPage{
		SessionID:    "session-1",
		Revision:     3,
		Offset:       0,
		TotalEntries: 6,
		Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "authoritative tail"}},
	}

	result := model.runtimeAdapter().applyProjectedRuntimeEvent(clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         3,
		CommittedEntryCount:        6,
		CommittedEntryStart:        5,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "gapped", Phase: string(llm.MessagePhaseFinal)}},
	}).
		cmd
	foundRefresh := false
	for _, msg := range collectCmdMessages(t, result) {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			foundRefresh = true
		}
	}
	if !foundRefresh {
		t.Fatal("a committed event with a forward gap must trigger authoritative transcript hydration")
	}
	if got := reads.suffixCount.Load(); got != baselineSuffixReads {
		t.Fatalf("server suffix reads = %d, want unchanged baseline %d", got, baselineSuffixReads)
	}
	if got := reads.pageCount.Load(); got != baselinePageReads+1 {
		t.Fatalf("server transcript page reads delta = %d, want 1 for the gap fallback", got-baselinePageReads)
	}
}

func TestRuntimeClientLoadTranscriptPageAlwaysReadsFromServerAuthority(t *testing.T) {
	reads := &countingSessionViewClient{page: clientui.TranscriptPage{SessionID: "session-1", Offset: 300, TotalEntries: 500}}
	runtimeClient := newRuntimeClientReadOnlyTest(reads)
	req := clientui.TranscriptPageRequest{Cursor: 4096}

	if _, err := runtimeClient.LoadTranscriptPage(req); err != nil {
		t.Fatalf("first load transcript page: %v", err)
	}
	if _, err := runtimeClient.LoadTranscriptPage(req); err != nil {
		t.Fatalf("second load transcript page: %v", err)
	}
	if got := reads.count.Load(); got != 2 {
		t.Fatalf("session view call count = %d, want 2", got)
	}
}

func TestRuntimeClientLoadTranscriptPageCachesByRequestKey(t *testing.T) {
	reads := &countingSessionViewClient{page: clientui.TranscriptPage{SessionID: "session-1", TotalEntries: 500}}
	runtimeClient := newRuntimeClientReadOnlyTest(reads)

	if _, err := runtimeClient.LoadTranscriptPage(clientui.TranscriptPageRequest{Cursor: 4096}); err != nil {
		t.Fatalf("first load transcript page: %v", err)
	}
	if _, err := runtimeClient.LoadTranscriptPage(clientui.TranscriptPageRequest{}); err != nil {
		t.Fatalf("second load transcript page: %v", err)
	}
	if got := reads.count.Load(); got != 2 {
		t.Fatalf("session view call count = %d, want 2", got)
	}
}

func TestRuntimeClientRefreshTranscriptBypassesFreshCachedPage(t *testing.T) {
	reads := &countingSessionViewClient{page: clientui.TranscriptPage{SessionID: "session-1"}}
	runtimeClient := newRuntimeClientReadOnlyTest(reads)

	if _, err := runtimeClient.LoadTranscriptPage(clientui.TranscriptPageRequest{}); err != nil {
		t.Fatalf("load transcript page: %v", err)
	}
	if _, err := runtimeClient.RefreshTranscript(); err != nil {
		t.Fatalf("refresh transcript: %v", err)
	}
	if got := reads.count.Load(); got != 2 {
		t.Fatalf("session view call count = %d, want 2", got)
	}
}

func TestRuntimeClientLoadTranscriptPageDoesNotPopulateTranscriptAccessor(t *testing.T) {
	reads := &countingSessionViewClient{
		pageForRequest: func(req serverapi.SessionTranscriptPageRequest) clientui.TranscriptPage {
			if req.Cursor <= 0 {
				return clientui.TranscriptPage{
					SessionID:    "session-1",
					Offset:       0,
					TotalEntries: 500,
					Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "tail"}},
				}
			}
			return clientui.TranscriptPage{
				SessionID:    "session-1",
				Offset:       100,
				TotalEntries: 500,
				Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "paged"}},
			}
		},
	}
	runtimeClient := newRuntimeClientReadOnlyTest(reads)

	if _, err := runtimeClient.RefreshTranscript(); err != nil {
		t.Fatalf("refresh transcript: %v", err)
	}
	if _, err := runtimeClient.LoadTranscriptPage(clientui.TranscriptPageRequest{Cursor: 4096}); err != nil {
		t.Fatalf("load transcript page: %v", err)
	}
	page := runtimeClient.Transcript()
	if page.SessionID != "session-1" || len(page.Entries) != 0 {
		t.Fatalf("transcript accessor page = %+v, want empty session page", page)
	}
	if got := reads.count.Load(); got != 2 {
		t.Fatalf("session view call count = %d, want 2", got)
	}
}

func TestRuntimeClientTranscriptDoesNotReadFromServer(t *testing.T) {
	reads := &countingSessionViewClient{
		pageForRequest: func(req serverapi.SessionTranscriptPageRequest) clientui.TranscriptPage {
			if req.Cursor <= 0 {
				return clientui.TranscriptPage{
					SessionID:    "session-1",
					Offset:       490,
					TotalEntries: 500,
					Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "tail"}},
				}
			}
			return clientui.TranscriptPage{
				SessionID:    "session-1",
				Offset:       100,
				TotalEntries: 500,
				Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "paged"}},
			}
		},
	}
	runtimeClient := newRuntimeClientReadOnlyTest(reads)

	if _, err := runtimeClient.RefreshTranscript(); err != nil {
		t.Fatalf("refresh transcript: %v", err)
	}
	if _, err := runtimeClient.LoadTranscriptPage(clientui.TranscriptPageRequest{}); err != nil {
		t.Fatalf("load transcript page: %v", err)
	}

	page := runtimeClient.Transcript()
	if page.SessionID != "session-1" || len(page.Entries) != 0 {
		t.Fatalf("transcript accessor page = %+v, want empty session page", page)
	}
	if got, want := reads.count.Load(), int32(2); got != want {
		t.Fatalf("session view call count = %d, want %d", got, want)
	}
}

func TestRuntimeClientFromEngineDoesNotSeedTranscriptAccessor(t *testing.T) {
	store := createAppRuntimeSession(t)
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "u1"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "a1", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	eng := newAppRuntimeEngineWithStore(t, store, &runtimeClientFakeLLM{}, runtime.Config{})

	runtimeClient := newUIRuntimeClientFromEngine(eng)
	page := runtimeClient.Transcript()

	if page.SessionID != store.Meta().SessionID || len(page.Entries) != 0 {
		t.Fatalf("transcript accessor page = %+v, want empty session page", page)
	}
	authoritative, err := runtimeClient.RefreshTranscript()
	if err != nil {
		t.Fatalf("refresh transcript: %v", err)
	}
	if got, want := authoritative.TotalEntries, 2; got != want {
		t.Fatalf("total entries = %d, want %d", got, want)
	}
	if got, want := len(authoritative.Entries), 2; got != want {
		t.Fatalf("entry count = %d, want %d", got, want)
	}
	if authoritative.Entries[1].Text != "a1" {
		t.Fatalf("expected authoritative transcript tail entry, got %+v", authoritative.Entries)
	}
}

func TestRuntimeClientMainViewIncludesActiveRunFromRealEngine(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	fakeLLM := &runtimeClientFakeLLM{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	store, eng := newAppRuntimeEngine(t, fakeLLM, runtime.Config{}, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: runtimeClientBlockingTool{started: started, release: release}})
	runtimeRegistry := registry.NewRuntimeRegistry()
	runtimeRegistry.Register(store.Meta().SessionID, eng)

	runtimeClient := newUIRuntimeClientWithReads(
		store.Meta().SessionID,
		sharedclient.NewLoopbackSessionViewClient(sessionview.NewService(nil, runtimeRegistry, nil)),
		sharedclient.NewLoopbackRuntimeControlClient(runtimecontrol.NewService(runtimeRegistry)),
	)
	result := make(chan error, 1)
	go func() {
		_, submitErr := eng.SubmitUserMessage(context.Background(), "run tools")
		result <- submitErr
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active run")
	}

	view := runtimeClient.MainView()
	if view.Session.SessionID != store.Meta().SessionID {
		t.Fatalf("session id = %q, want %q", view.Session.SessionID, store.Meta().SessionID)
	}
	if view.ActiveRun == nil {
		t.Fatal("expected active run in main view")
	}
	if view.ActiveRun.RunID == "" || view.ActiveRun.StepID == "" {
		t.Fatalf("expected run identifiers, got %+v", view.ActiveRun)
	}
	if view.ActiveRun.SessionID != store.Meta().SessionID {
		t.Fatalf("run session id = %q, want %q", view.ActiveRun.SessionID, store.Meta().SessionID)
	}
	if view.ActiveRun.Status != "running" || view.ActiveRun.StartedAt.IsZero() || !view.ActiveRun.FinishedAt.IsZero() {
		t.Fatalf("unexpected active run payload: %+v", view.ActiveRun)
	}

	close(release)
	if err := <-result; err != nil {
		t.Fatalf("submit user message: %v", err)
	}
}

func TestRuntimeClientMainViewFallsBackToLocalRuntimeProjectionOnReadError(t *testing.T) {
	store := createAppRuntimeSession(t)
	if err := store.SetParentSessionID("parent-123"); err != nil {
		t.Fatalf("set parent session id: %v", err)
	}
	eng := newAppRuntimeEngineWithStore(t, store, &runtimeClientFakeLLM{}, runtime.Config{})
	if err := eng.SetThinkingLevel("high"); err != nil {
		t.Fatalf("set thinking level: %v", err)
	}
	runtimeRegistry := registry.NewRuntimeRegistry()
	runtimeRegistry.Register(store.Meta().SessionID, eng)

	runtimeClient := newUIRuntimeClientWithReads(
		store.Meta().SessionID,
		sharedclient.NewLoopbackSessionViewClient(sessionview.NewService(nil, runtimeRegistry, nil)),
		sharedclient.NewLoopbackRuntimeControlClient(runtimecontrol.NewService(runtimeRegistry)),
	)
	view := runtimeClient.MainView()
	if view.Session.SessionID != store.Meta().SessionID {
		t.Fatalf("session id = %q, want %q", view.Session.SessionID, store.Meta().SessionID)
	}
	if view.Status.ParentSessionID != "parent-123" {
		t.Fatalf("parent session id = %q, want parent-123", view.Status.ParentSessionID)
	}
	if view.Status.ThinkingLevel != "high" {
		t.Fatalf("thinking level = %q, want high", view.Status.ThinkingLevel)
	}
}

func TestRuntimeClientMainViewSnapshotDoesNotPopulateTranscriptEndpoint(t *testing.T) {
	store := createAppRuntimeSession(t)
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "seeded from main view", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	eng := newAppRuntimeEngineWithStore(t, store, &runtimeClientFakeLLM{}, runtime.Config{})
	runtimeRegistry := registry.NewRuntimeRegistry()
	runtimeRegistry.Register(store.Meta().SessionID, eng)

	runtimeClient := newUIRuntimeClientWithReads(
		store.Meta().SessionID,
		sharedclient.NewLoopbackSessionViewClient(sessionview.NewService(nil, runtimeRegistry, nil)),
		sharedclient.NewLoopbackRuntimeControlClient(runtimecontrol.NewService(runtimeRegistry)),
	)
	view := runtimeClient.MainView()
	if got := len(view.Session.Chat.Entries); got != 0 {
		t.Fatalf("main view chat entry count = %d, want 0", got)
	}
	if page := runtimeClient.Transcript(); len(page.Entries) != 0 {
		t.Fatalf("expected transcript accessor to stay empty before explicit hydration, got %+v", page)
	}

	page, err := runtimeClient.RefreshTranscript()
	if err != nil {
		t.Fatalf("refresh transcript: %v", err)
	}
	if got := len(page.Entries); got != 1 {
		t.Fatalf("transcript entry count = %d, want 1", got)
	}
	if got := page.Entries[0].Text; got != "seeded from main view" {
		t.Fatalf("transcript entry text = %q, want seeded from main view", got)
	}
}

func TestRuntimeClientWithoutClientsIsNil(t *testing.T) {
	if client := newUIRuntimeClientWithReads("session-1", nil, nil); client != nil {
		t.Fatalf("expected nil runtime client, got %#v", client)
	}
	_ = clientui.RuntimeMainView{}
}

func TestRuntimeClientMainViewCachesSuccessfulRead(t *testing.T) {
	reads := &countingSessionViewClient{view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}, Status: clientui.RuntimeStatus{ThinkingLevel: "high"}}}
	runtimeClient := newRuntimeClientReadTest(reads)

	first := runtimeClient.MainView()
	second := runtimeClient.MainView()
	third := runtimeClient.MainView()
	if first.Status.ThinkingLevel != "high" || second.Status.ThinkingLevel != "high" || third.Status.ThinkingLevel != "high" {
		t.Fatalf("expected cached main view to preserve projected status, got %+v / %+v / %+v", first, second, third)
	}
	if got := reads.count.Load(); got != 1 {
		t.Fatalf("main view read count = %d, want 1", got)
	}
}

func TestRuntimeClientRefreshMainViewBypassesCache(t *testing.T) {
	reads := &countingSessionViewClient{view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{SessionID: "session-1"}, Status: clientui.RuntimeStatus{ThinkingLevel: "high"}}}
	runtimeClient := newRuntimeClientReadTest(reads)
	if _, err := runtimeClient.RefreshMainView(); err != nil {
		t.Fatalf("RefreshMainView: %v", err)
	}
	reads.view.Status.ThinkingLevel = "low"
	refreshed, err := runtimeClient.RefreshMainView()
	if err != nil {
		t.Fatalf("RefreshMainView second call: %v", err)
	}
	if refreshed.Status.ThinkingLevel != "low" {
		t.Fatalf("expected refreshed main view to bypass cache, got %+v", refreshed)
	}
	if got := reads.count.Load(); got != 2 {
		t.Fatalf("refresh main view read count = %d, want 2", got)
	}
}

func TestRuntimeClientMainViewLeavesTranscriptHydrationToTranscriptEndpoint(t *testing.T) {
	reads := &countingSessionViewClient{view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{
		SessionID: "session-1",
		Transcript: clientui.TranscriptMetadata{
			Revision:            3,
			CommittedEntryCount: 1,
		},
		Chat: clientui.ChatSnapshot{
			Entries: []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
		},
	}}}
	runtimeClient := newRuntimeClientReadTest(reads)

	view := runtimeClient.MainView()
	if view.Session.SessionID != "session-1" {
		t.Fatalf("session id = %q, want session-1", view.Session.SessionID)
	}
	page := runtimeClient.Transcript()
	if got := len(page.Entries); got != 0 {
		t.Fatalf("transcript entry count = %d, want 0", got)
	}
	if got := reads.count.Load(); got != 1 {
		t.Fatalf("session view call count = %d, want 1", got)
	}
}

func TestRuntimeClientMainViewBootstrapDoesNotSeedStreamingOngoingState(t *testing.T) {
	reads := &countingSessionViewClient{view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{
		SessionID: "session-1",
		Transcript: clientui.TranscriptMetadata{
			Revision:            3,
			CommittedEntryCount: 1,
		},
		Chat: clientui.ChatSnapshot{
			Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
			Streaming: "NO_OP",
		},
	}}}
	runtimeClient := newRuntimeClientReadTest(reads)

	_ = runtimeClient.MainView()
	page := runtimeClient.Transcript()
	if got := page.Streaming; got != "" {
		t.Fatalf("bootstrap ongoing text = %q, want empty", got)
	}
}

func TestRuntimeClientRefreshMainViewDoesNotDowngradeCachedTranscriptTail(t *testing.T) {
	reads := &countingSessionViewClient{
		view: clientui.RuntimeMainView{Session: clientui.RuntimeSessionView{
			SessionID: "session-1",
			Transcript: clientui.TranscriptMetadata{
				Revision:            3,
				CommittedEntryCount: 2,
			},
			Chat: clientui.ChatSnapshot{
				Entries: []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
			},
		}},
		page: clientui.TranscriptPage{
			SessionID:    "session-1",
			Revision:     3,
			TotalEntries: 2,
			Entries: []clientui.ChatEntry{
				{Role: "assistant", Text: "seed"},
				{Role: "reviewer_status", Text: "Supervisor ran and applied 2 suggestions."},
			},
		},
	}
	runtimeClient := newRuntimeClientReadTest(reads)
	if _, err := runtimeClient.RefreshTranscript(); err != nil {
		t.Fatalf("RefreshTranscript: %v", err)
	}
	if _, err := runtimeClient.RefreshMainView(); err != nil {
		t.Fatalf("RefreshMainView: %v", err)
	}
	page, err := runtimeClient.RefreshTranscript()
	if err != nil {
		t.Fatalf("refresh transcript after main view refresh: %v", err)
	}
	if got := len(page.Entries); got != 2 {
		t.Fatalf("transcript entry count = %d, want 2", got)
	}
	if got := page.Entries[1].Role; got != "reviewer_status" {
		t.Fatalf("second transcript role = %q, want reviewer_status", got)
	}
	if got := page.Entries[1].Text; got != "Supervisor ran and applied 2 suggestions." {
		t.Fatalf("second transcript text = %q", got)
	}
	if got := reads.count.Load(); got != 3 {
		t.Fatalf("session view call count = %d, want 3", got)
	}
}

func TestRuntimeClientRefreshTranscriptUpdatesMainViewChatForWindowedRecentTail(t *testing.T) {
	reads := &countingSessionViewClient{
		page: clientui.TranscriptPage{
			SessionID:    "session-1",
			Revision:     3,
			Offset:       490,
			TotalEntries: 500,
			HasMore:      true,
			Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "windowed tail"}},
			Streaming:    "streaming",
		},
	}
	runtimeClient := newRuntimeClientReadTest(reads)

	if _, err := runtimeClient.RefreshTranscript(); err != nil {
		t.Fatalf("RefreshTranscript: %v", err)
	}
	view := runtimeClient.MainView()
	if got := len(view.Session.Chat.Entries); got != 1 {
		t.Fatalf("main view chat entry count = %d, want 1", got)
	}
	if got := view.Session.Chat.Entries[0].Text; got != "windowed tail" {
		t.Fatalf("main view chat text = %q, want windowed tail", got)
	}
	if got := view.Session.Chat.Streaming; got != "streaming" {
		t.Fatalf("main view ongoing = %q, want streaming", got)
	}
}

func TestRuntimeClientMainViewFailsFastWhenReadStalls(t *testing.T) {
	withUIRuntimeReadTimeout(t, time.Millisecond)

	runtimeClient := newRuntimeClientReadTest(blockingSessionViewClient{})
	start := time.Now()
	view := runtimeClient.MainView()
	elapsed := time.Since(start)
	if elapsed >= time.Second {
		t.Fatalf("expected stalled main-view read to fail fast, took %v", elapsed)
	}
	if view.Session.SessionID != "session-1" {
		t.Fatalf("expected fallback main view to preserve session id, got %+v", view)
	}
}

func withUIRuntimeReadTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	original := uiRuntimeReadTimeout
	uiRuntimeReadTimeout = timeout
	t.Cleanup(func() { uiRuntimeReadTimeout = original })
}

func TestRuntimeClientMainViewCachesFallbackAfterReadError(t *testing.T) {
	withUIRuntimeReadTimeout(t, time.Millisecond)

	reads := &blockingCountingSessionViewClient{}
	runtimeClient := newRuntimeClientReadTest(reads)

	first := runtimeClient.MainView()
	if first.Session.SessionID != "session-1" {
		t.Fatalf("fallback session id = %q, want session-1", first.Session.SessionID)
	}
	second := runtimeClient.MainView()
	if second.Session.SessionID != "session-1" {
		t.Fatalf("expected cached fallback session id preserved, got %+v", second)
	}
	if got := reads.count.Load(); got != 1 {
		t.Fatalf("main view read count after cached fallback = %d, want 1", got)
	}
}

func TestRuntimeClientRefreshTranscriptPageDoesNotUseHiddenPageCacheOnReadError(t *testing.T) {
	reads := &countingSessionViewClient{}
	runtimeClient := newRuntimeClientReadTest(reads)
	seedReq := clientui.TranscriptPageRequest{Cursor: 4096}
	concrete := runtimeClient.(*sessionRuntimeClient)

	var observedErr error
	concrete.SetConnectionStateObserver(func(err error) { observedErr = err })
	concrete.reads = &flakySessionViewClient{errs: []error{context.DeadlineExceeded}}

	page, err := concrete.refreshTranscriptPageSync(seedReq, time.Millisecond)
	if err != context.DeadlineExceeded {
		t.Fatalf("refresh transcript page error = %v, want %v", err, context.DeadlineExceeded)
	}
	if observedErr != context.DeadlineExceeded {
		t.Fatalf("observed connection state error = %v, want %v", observedErr, context.DeadlineExceeded)
	}
	if page.SessionID != "session-1" || len(page.Entries) != 0 {
		t.Fatalf("refresh transcript page fallback = %+v, want empty session page", page)
	}
}

func TestRuntimeClientQueueUserMessageNotifiesConnectionObserverOnFailure(t *testing.T) {
	runtimeClient := newUIRuntimeClientWithReads(
		"session-1",
		&countingSessionViewClient{},
		sharedclient.NewLoopbackRuntimeControlClient(nil),
	)
	concrete, ok := runtimeClient.(*sessionRuntimeClient)
	if !ok {
		t.Fatalf("runtime client type = %T, want *sessionRuntimeClient", runtimeClient)
	}
	var observedErr error
	concrete.SetConnectionStateObserver(func(err error) { observedErr = err })

	concrete.QueueUserMessage("queued input")

	if observedErr == nil || !errors.Is(observedErr, sharedclient.ErrLoopbackServiceUnavailable) {
		t.Fatalf("observed connection state error = %v, want runtime control service unavailable", observedErr)
	}
}

func TestRuntimeClientRefreshTranscriptPageRecoveryReturnsAuthoritativePage(t *testing.T) {
	reads := &countingSessionViewClient{}
	runtimeClient := newRuntimeClientReadTest(reads)
	concrete, ok := runtimeClient.(*sessionRuntimeClient)
	if !ok {
		t.Fatalf("runtime client type = %T, want *sessionRuntimeClient", runtimeClient)
	}
	seedReq := clientui.TranscriptPageRequest{Cursor: 4096}
	authoritativePage := clientui.TranscriptPage{
		SessionID:    "session-1",
		Revision:     8,
		Offset:       25,
		TotalEntries: 41,
		Entries:      []clientui.ChatEntry{{Role: "assistant", Text: "authoritative page"}},
	}

	var observed []error
	concrete.SetConnectionStateObserver(func(err error) {
		observed = append(observed, err)
	})
	concrete.reads = &flakySessionViewClient{
		errs:  []error{context.DeadlineExceeded, nil},
		pages: []serverapi.SessionTranscriptPageResponse{{}, {Transcript: authoritativePage}},
	}

	page, err := concrete.refreshTranscriptPageSync(seedReq, time.Millisecond)
	if err != context.DeadlineExceeded {
		t.Fatalf("refresh transcript page error = %v, want %v", err, context.DeadlineExceeded)
	}
	if page.SessionID != "session-1" || len(page.Entries) != 0 {
		t.Fatalf("refresh transcript page fallback = %+v, want empty session page", page)
	}

	page, err = concrete.refreshTranscriptPageSync(seedReq, time.Millisecond)
	if err != nil {
		t.Fatalf("refresh transcript page recovery error = %v", err)
	}
	if page.SessionID != authoritativePage.SessionID || page.Revision != authoritativePage.Revision || len(page.Entries) != 1 || page.Entries[0].Text != "authoritative page" {
		t.Fatalf("refresh transcript page recovery = %+v, want %+v", page, authoritativePage)
	}
	if len(observed) != 2 || observed[0] != context.DeadlineExceeded || observed[1] != nil {
		t.Fatalf("connection observer sequence = %+v, want [%v <nil>]", observed, context.DeadlineExceeded)
	}
}
