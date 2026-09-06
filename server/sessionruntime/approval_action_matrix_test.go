package sessionruntime

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func requireApproval(t *testing.T, condition bool, format string, args ...any) {
	t.Helper()
	if !condition {
		t.Fatalf(format, args...)
	}
}

type approvalActionClient struct{ responses []llm.Response }

func (*approvalActionClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return sessionRuntimeTestProviderCapabilities(), nil
}
func (c *approvalActionClient) Generate(context.Context, llm.Request, llm.StreamCallbacks) (llm.Response, error) {
	if len(c.responses) == 0 {
		return llm.Response{}, errors.New("unexpected provider request")
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

type approvalActionFeed struct {
	pending                                    chan authorityPromptEvent
	pendingGate, resolvedGate, resolvedStarted chan struct{}
	pendingOnce, resolvedOnce                  sync.Once
	resolvedErr                                error
	resolved                                   atomic.Int32
}

func newApprovalActionFeed(options approvalActionHarnessOptions) *approvalActionFeed {
	f := &approvalActionFeed{
		pending:         make(chan authorityPromptEvent, 4),
		resolvedStarted: make(chan struct{}, 4),
		resolvedErr:     options.resolvedErr,
	}
	if options.blockPending {
		f.pendingGate = make(chan struct{})
	}
	if options.blockResolved {
		f.resolvedGate = make(chan struct{})
	}
	return f
}
func (f *approvalActionFeed) PromptPendingScope(scope ExecutionScope, request tools.AskQuestionRequest, _ time.Time) error {
	stepID, err := runtimeids.ParseStepID(request.StepID)
	if err != nil {
		return err
	}
	resource, _ := scope.Resource()
	f.pending <- authorityPromptEvent{resource: resource, scopeID: scope.ID(), stepID: stepID, requestID: request.ToolCallID}
	if f.pendingGate != nil {
		<-f.pendingGate
	}
	return nil
}
func (f *approvalActionFeed) PromptResolvedScope(ExecutionScope, string) error {
	f.resolvedStarted <- struct{}{}
	if f.resolvedGate != nil {
		<-f.resolvedGate
	}
	f.resolved.Add(1)
	return f.resolvedErr
}
func releaseApprovalActionGate(gate chan struct{}, once *sync.Once) {
	if gate != nil {
		once.Do(func() { close(gate) })
	}
}

type approvalActionHarnessOptions struct {
	blockPending, blockConsumer, blockResolved, secondPatch, multiTarget bool
	consumerErr, resolvedErr                                             error
	storeOptions                                                         []session.StoreOption
	onEvent                                                              func(runtime.Event)
}
type approvalActionHarness struct {
	authority                        *Authority
	handle                           ExecutionHandle
	engine                           *runtime.Engine
	feed                             *approvalActionFeed
	sessionID                        runtimeids.SessionID
	pending                          authorityPromptEvent
	paths, callIDs                   []string
	consumerStarted, releaseConsumer chan struct{}
	releaseOnce                      sync.Once
	consumerAccepted                 atomic.Int32
}

func newApprovalActionHarness(t *testing.T, options approvalActionHarnessOptions) *approvalActionHarness {
	t.Helper()
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	outside := testsetup.NonTemporaryDirectory(t, "kent-approval-action-", tools.IsPathInTemporaryDir)
	paths := []string{outside + "/first.txt"}
	callIDs := []string{"approval-action"}
	responses := []llm.Response{}
	if options.multiTarget {
		paths = append(paths, outside+"/second.txt")
		responses = append(responses, approvalPatchResponse(callIDs[0], paths))
	} else {
		responses = append(responses, approvalPatchResponse(callIDs[0], paths[:1]))
		if options.secondPatch {
			paths = append(paths, outside+"/second.txt")
			callIDs = append(callIDs, "approval-action-second")
			responses = append(responses, approvalPatchResponse(callIDs[1], paths[1:]))
		}
	}
	responses = append(responses, llm.Response{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)}, Usage: llm.Usage{WindowTokens: 200000}})
	client := &approvalActionClient{responses: responses}
	feed := newApprovalActionFeed(options)
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    append(fixture.metadata.AuthoritativeSessionStoreOptions(), options.storeOptions...),
		PromptFeed:      feed,
	})
	h := &approvalActionHarness{
		authority: authority, feed: feed, sessionID: sessionID,
		paths: paths, callIDs: callIDs, consumerStarted: make(chan struct{}), releaseConsumer: make(chan struct{}),
	}
	plan := authorityTestRuntimePlan(t, fixture, client)
	plan.options.EnabledTools = []toolspec.ID{toolspec.ToolPatch}
	plan.options.OnEvent = options.onEvent
	engineReady := make(chan *runtime.Engine, 1)
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID), Runtime: &plan, Resource: OpenAgentResource{},
		Ask: func(ctx context.Context, scope ExecutionScope, request tools.AskQuestionRequest) (tools.AskQuestionResolution, error) {
			consumer := request.ApprovalConsumer
			request.ApprovalConsumer = func(answer tools.AskQuestionApproval) error {
				close(h.consumerStarted)
				if options.blockConsumer {
					<-h.releaseConsumer
				}
				if options.consumerErr != nil {
					return options.consumerErr
				}
				err := consumer(answer)
				if err == nil {
					h.consumerAccepted.Add(1)
				}
				return err
			}
			return authority.AwaitPromptResolution(ctx, scope.ID(), request)
		},
		Runner: func(ctx context.Context, _ ExecutionScope, bridge AgentRuntimeBridge) error {
			return bridge.WithEngine(ctx, func(ctx context.Context, engine *runtime.Engine) error {
				engineReady <- engine
				_, err := engine.SubmitUserMessage(ctx, "apply patch")
				return err
			})
		},
	})
	requireApproval(t, err == nil, "start Approval action: %v", err)
	h.handle, h.engine = handle, <-engineReady
	h.pending = receiveApprovalAction(t, feed.pending, "Approval action")
	t.Cleanup(func() {
		h.release()
		handle.RequestStop()
		_, _ = handle.Wait(context.Background())
		requireApproval(t, authority.Close(context.Background()) == nil, "close Approval action authority")
	})
	return h
}
func approvalPatchResponse(callID string, paths []string) llm.Response {
	patch := "*** Begin Patch\n"
	for _, path := range paths {
		patch += "*** Add File: " + path + "\n+approved\n"
	}
	patch += "*** End Patch\n"
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
		ToolCalls: []llm.ToolCall{{ID: callID, Name: string(toolspec.ToolPatch), Custom: true, CustomInput: &patch}},
		Usage:     llm.Usage{WindowTokens: 200000},
	}
}
func (h *approvalActionHarness) release() {
	releaseApprovalActionGate(h.feed.pendingGate, &h.feed.pendingOnce)
	releaseApprovalActionGate(h.feed.resolvedGate, &h.feed.resolvedOnce)
	h.releaseOnce.Do(func() { close(h.releaseConsumer) })
}
func (h *approvalActionHarness) resolve(ctx context.Context, payload PromptAnswerPayload) promptBatchCallResult {
	results, err := h.authority.ResolvePromptBatch(ctx, h.sessionID, h.pending.stepID, []PromptAnswerCommand{{
		ToolCallID: clientui.ToolCallID(h.pending.requestID), Payload: payload,
	}})
	return promptBatchCallResult{results: results, err: err}
}
func approvalActionAnswer(decision tools.AskQuestionApprovalDecision, commentary *string) PromptAnswerPayload {
	return PromptApprovalAnswerCommand{Answer: tools.AskQuestionApproval{Decision: decision, Commentary: commentary}}
}
func beginApprovalAction(h *approvalActionHarness, ctx context.Context, payload PromptAnswerPayload) <-chan promptBatchCallResult {
	done := make(chan promptBatchCallResult, 1)
	go func() { done <- h.resolve(ctx, payload) }()
	return done
}

type approvalActionInterruptResult struct {
	interrupted bool
	err         error
}

func beginApprovalActionInterrupt(
	h *approvalActionHarness,
	interrupt func(*approvalActionHarness) (bool, error),
) <-chan approvalActionInterruptResult {
	done := make(chan approvalActionInterruptResult, 1)
	go func() {
		interrupted, err := interrupt(h)
		done <- approvalActionInterruptResult{interrupted: interrupted, err: err}
	}()
	return done
}
func receiveApprovalAction[T any](t *testing.T, done <-chan T, operation string) T {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
		var zero T
		return zero
	}
}
func requireApprovalActionBlocked[T any](t *testing.T, done <-chan T, operation string) {
	t.Helper()
	select {
	case result := <-done:
		t.Fatalf("%s completed early: %+v", operation, result)
	case <-time.After(100 * time.Millisecond):
	}
}
func requireApprovalActionOutcome(t *testing.T, result promptBatchCallResult, want PromptAnswerOutcome) {
	t.Helper()
	requireApproval(t, result.err == nil && len(result.results) == 1 && result.results[0].Outcome == want,
		"Approval result = (%+v, %v), want %s", result.results, result.err, want)
}
func waitApprovalHandle(t *testing.T, h *approvalActionHarness) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := h.handle.Wait(context.Background()); done <- err }()
	select {
	case err := <-done:
		return err
	case next := <-h.feed.pending:
		t.Fatalf("unexpected second Approval: %+v", next)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Approval action")
	}
	return nil
}
func approvalActionRows(t *testing.T, h *approvalActionHarness, commentary string) (int, []runtime.TranscriptToolRowFact) {
	t.Helper()
	var users int
	var toolRows []runtime.TranscriptToolRowFact
	err := h.engine.WithTranscriptHydrationSnapshot(func(snapshot runtime.TranscriptHydrationSnapshot) error {
		for _, row := range snapshot.CommittedRows {
			if row.User != nil && row.User.Text == commentary {
				users++
			}
			if row.Tool != nil && strings.HasPrefix(row.Tool.ToolCallID, "approval-action") {
				toolRows = append(toolRows, *row.Tool)
			}
		}
		return nil
	})
	requireApproval(t, err == nil, "read Approval transcript: %v", err)
	return users, toolRows
}
func requireApprovalActionTerminal(t *testing.T, h *approvalActionHarness, payload PromptAnswerPayload) {
	t.Helper()
	result := h.resolve(context.Background(), payload)
	requireApproval(t, result.err == nil && len(result.results) == 1 && result.results[0].Outcome == PromptAnswerOutcomeSkipped,
		"stale Approval retry = (%+v, %v), want Skipped", result.results, result.err)
}
func requireApprovalEffects(t *testing.T, h *approvalActionHarness, commentary string, allowed bool) {
	t.Helper()
	users, rows := approvalActionRows(t, h, commentary)
	wantUsers := 0
	if allowed && commentary != "" {
		wantUsers = 1
	}
	requireApproval(t, users == wantUsers, "commentary rows = %d, want %d", users, wantUsers)
	for _, path := range h.paths {
		content, err := os.ReadFile(path)
		requireApproval(t, !allowed || err == nil && string(content) == "approved\n", "allowed path %q = %q/%v", path, content, err)
		requireApproval(t, allowed || errors.Is(err, os.ErrNotExist), "denied path %q exists", path)
	}
	requireApproval(t, len(rows) == len(h.callIDs), "tool outcomes = %+v", rows)
	requireApproval(t, allowed || rows[0].IsError, "terminal tool outcome = %+v, want error", rows[0])
}
func TestApprovalActionCallerCancellationAfterCommit(t *testing.T) {
	commentary := "caller cancellation commentary"
	tests := []struct {
		name          string
		payload       PromptAnswerPayload
		commentary    string
		allow, second bool
	}{
		{"Allow commentary", approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &commentary), commentary, true, false},
		{"Allow no commentary", approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, nil), "", true, false},
		{"Allow session", approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowSession, nil), "", true, true},
		{"Deny commentary", approvalActionAnswer(tools.AskQuestionApprovalDecisionDeny, &commentary), commentary, false, false},
		{"Decline", PromptDeclinedCommand{}, "", false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newApprovalActionHarness(t, approvalActionHarnessOptions{blockResolved: true, secondPatch: test.second})
			ctx, cancel := context.WithCancel(context.Background())
			done := beginApprovalAction(h, ctx, test.payload)
			receiveApprovalAction(t, h.feed.resolvedStarted, "Approval finalization")
			cancel()
			h.release()
			requireApprovalActionOutcome(t, receiveApprovalAction(t, done, "caller-owned response"), PromptAnswerOutcomeResolved)
			err := waitApprovalHandle(t, h)
			requireApproval(t, err == nil, "execution-owned Approval: %v", err)
			requireApprovalEffects(t, h, test.commentary, test.allow)
			requireApprovalActionTerminal(t, h, test.payload)
		})
	}
}
func TestApprovalActionOperationalCancellationAfterDelivery(t *testing.T) {
	commentary := "accepted before parent cancellation"
	h := newApprovalActionHarness(t, approvalActionHarnessOptions{blockConsumer: true})
	done := beginApprovalAction(h, context.Background(), approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &commentary))
	receiveApprovalAction(t, h.consumerStarted, "ApprovalConsumer delivery")
	requireApprovalActionBlocked(t, done, "answer before consumer acceptance")
	requireApproval(t, h.handle.RequestStop(), "real parent execution cancellation was not accepted")
	h.release()
	result := receiveApprovalAction(t, done, "canceled Approval answer")
	if result.err == nil {
		requireApprovalActionOutcome(t, result, PromptAnswerOutcomeResolved)
	}
	requireApproval(t, waitApprovalHandle(t, h) != nil, "parent-canceled tool execution succeeded")
	requireApprovalEffects(t, h, "", false)
	users, _ := approvalActionRows(t, h, commentary)
	requireApproval(t, users == 1, "accepted commentary rows = %d, want 1", users)
	requireApproval(t, h.feed.resolved.Load() == 1, "terminal publications = %d, want 1", h.feed.resolved.Load())
	requireApprovalActionTerminal(t, h, approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &commentary))
}
func TestApprovalActionOperationalCancellationAfterClaimBeforeDelivery(t *testing.T) {
	commentary := "claimed before parent cancellation"
	h := newApprovalActionHarness(t, approvalActionHarnessOptions{blockPending: true})
	done := beginApprovalAction(h, context.Background(), approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &commentary))
	requireApprovalActionBlocked(t, done, "answer before pending publication")
	requireApproval(t, h.handle.RequestStop(), "real parent execution cancellation was not accepted")
	h.release()
	result := receiveApprovalAction(t, done, "canceled claimed Approval")
	requireApproval(t, result.err != nil, "canceled claimed Approval = %+v, want failure", result.results)
	requireApproval(t, waitApprovalHandle(t, h) != nil, "parent-canceled tool execution succeeded")
	requireApprovalEffects(t, h, "", false)
	users, _ := approvalActionRows(t, h, commentary)
	requireApproval(t, users <= 1, "canceled claimed commentary rows = %d, want at most one", users)
	requireApproval(t, h.feed.resolved.Load() == 1, "terminal publications = %d, want 1", h.feed.resolved.Load())
	requireApprovalActionTerminal(t, h, approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &commentary))
}
func TestApprovalActionEditedRetryAfterTwoPreclaimFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		decision tools.AskQuestionApprovalDecision
		allow    bool
	}{
		{"Allow", tools.AskQuestionApprovalDecisionAllowOnce, true},
		{"Deny", tools.AskQuestionApprovalDecisionDeny, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newApprovalActionHarness(t, approvalActionHarnessOptions{})
			drafts := []string{"first draft", "second draft"}
			for _, draft := range drafts {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				result := h.resolve(ctx, approvalActionAnswer(test.decision, &draft))
				requireApproval(t, errors.Is(result.err, context.Canceled), "preclaim failure = %v, want cancellation", result.err)
			}
			edited := "edited same-ID retry"
			requireApprovalActionOutcome(t, h.resolve(context.Background(), approvalActionAnswer(test.decision, &edited)), PromptAnswerOutcomeResolved)
			err := waitApprovalHandle(t, h)
			requireApproval(t, err == nil, "edited retry: %v", err)
			requireApprovalEffects(t, h, edited, test.allow)
			for _, draft := range drafts {
				users, _ := approvalActionRows(t, h, draft)
				requireApproval(t, users == 0, "preclaim-canceled commentary rows = %d, want 0", users)
			}
		})
	}
}
func TestApprovalActionConcurrentWinnerOrders(t *testing.T) {
	allowCommentary, denyCommentary := "allow winner", "deny winner"
	for _, test := range []struct {
		name          string
		first, second PromptAnswerPayload
		allow         bool
	}{
		{"Allow before Deny", approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &allowCommentary), approvalActionAnswer(tools.AskQuestionApprovalDecisionDeny, &denyCommentary), true},
		{"Deny before Allow", approvalActionAnswer(tools.AskQuestionApprovalDecisionDeny, &denyCommentary), approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &allowCommentary), false},
		{"Allow before decline", approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &allowCommentary), PromptDeclinedCommand{}, true},
		{"Decline before Allow", PromptDeclinedCommand{}, approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &allowCommentary), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newApprovalActionHarness(t, approvalActionHarnessOptions{blockResolved: true})
			winner := beginApprovalAction(h, context.Background(), test.first)
			receiveApprovalAction(t, h.feed.resolvedStarted, "winning Approval finalization")
			loser := beginApprovalAction(h, context.Background(), test.second)
			requireApprovalActionBlocked(t, loser, "loser before exact winner finalization")
			h.release()
			requireApprovalActionOutcome(t, receiveApprovalAction(t, winner, "winner"), PromptAnswerOutcomeResolved)
			requireApprovalActionOutcome(t, receiveApprovalAction(t, loser, "loser"), PromptAnswerOutcomeSkipped)
			err := waitApprovalHandle(t, h)
			requireApproval(t, err == nil, "winning action: %v", err)
			requireApprovalEffects(t, h, allowCommentary, test.allow)
		})
	}
}
func TestApprovalActionCommentaryFIFOAtStepBoundary(t *testing.T) {
	commentary := "FIFO commentary"
	h := newApprovalActionHarness(t, approvalActionHarnessOptions{blockConsumer: true})
	done := beginApprovalAction(h, context.Background(), approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &commentary))
	receiveApprovalAction(t, h.consumerStarted, "ApprovalConsumer")
	transitionStarted, releaseTransition := make(chan struct{}), make(chan struct{})
	transitionDone := make(chan error, 1)
	go func() {
		transitionDone <- h.engine.RunExecutionTargetTransition(context.Background(), nil, func() error {
			close(transitionStarted)
			<-releaseTransition
			return nil
		})
	}()
	h.release()
	requireApprovalActionOutcome(t, receiveApprovalAction(t, done, "FIFO answer"), PromptAnswerOutcomeResolved)
	receiveApprovalAction(t, transitionStarted, "Step Boundary drain")
	requireApprovalEffects(t, h, commentary, true)
	close(releaseTransition)
	err := receiveApprovalAction(t, transitionDone, "Runtime transition")
	requireApproval(t, err == nil, "Runtime transition: %v", err)
	err = waitApprovalHandle(t, h)
	requireApproval(t, err == nil, "Step completion: %v", err)
}

func TestApprovalActionAcceptedFailureBoundaries(t *testing.T) {
	t.Run("ApprovalConsumer", func(t *testing.T) {
		cause := errors.New("ApprovalConsumer failed")
		commentary := "accepted before ApprovalConsumer"
		h := newApprovalActionHarness(t, approvalActionHarnessOptions{consumerErr: cause})
		result := h.resolve(context.Background(), approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &commentary))
		requireApproval(t, errors.Is(result.err, cause) && len(result.results) == 0, "ApprovalConsumer = %+v/%v", result.results, result.err)
		requireApproval(t, waitApprovalHandle(t, h) == nil, "execution after ApprovalConsumer failure")
		users, rows := approvalActionRows(t, h, commentary)
		requireApproval(t, users == 1 && len(rows) == 1 && rows[0].IsError, "ApprovalConsumer effects = commentary:%d tools:%+v", users, rows)
		_, statErr := os.Stat(h.paths[0])
		requireApproval(t, errors.Is(statErr, os.ErrNotExist), "ApprovalConsumer applied tool effect: %v", statErr)
		requireApprovalActionTerminal(t, h, approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &commentary))
	})
	t.Run("commentary application", func(t *testing.T) {
		cause := errors.New("commentary application failed")
		gate := sessiontest.NewPersistenceGate(sessiontest.NewPersistence())
		var once sync.Once
		h := newApprovalActionHarness(t, approvalActionHarnessOptions{
			storeOptions: []session.StoreOption{session.WithPersistenceObserver(gate)},
			onEvent: func(event runtime.Event) {
				if event.Kind == runtime.EventToolCallCompleted && event.ToolResult != nil && event.ToolResult.CallID == "approval-action" {
					once.Do(func() { gate.FailNext(cause) })
				}
			},
		})
		commentary := "accepted commentary"
		requireApprovalActionOutcome(t, h.resolve(context.Background(), approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &commentary)), PromptAnswerOutcomeResolved)
		runErr := waitApprovalHandle(t, h)
		requireApproval(t, errors.Is(runErr, cause), "application error = %v, want %v", runErr, cause)
		users, _ := approvalActionRows(t, h, commentary)
		requireApproval(t, users == 1, "commentary rows = %d, want 1", users)
	})
	t.Run("PromptResolvedScope", func(t *testing.T) {
		cause := errors.New("PromptResolvedScope failed")
		commentary := "accepted before publication failure"
		h := newApprovalActionHarness(t, approvalActionHarnessOptions{resolvedErr: cause, secondPatch: true})
		result := h.resolve(context.Background(), approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowSession, &commentary))
		requireApproval(t, errors.Is(result.err, cause) && len(result.results) == 0, "publication failure = %+v/%v", result.results, result.err)
		requireApproval(t, h.consumerAccepted.Load() == 1, "accepted consumer effects = %d, want 1", h.consumerAccepted.Load())
		requireApproval(t, waitApprovalHandle(t, h) == nil, "execution after publication failure")
		users, rows := approvalActionRows(t, h, commentary)
		requireApproval(t, users == 1 && len(rows) == 2 && rows[0].IsError && !rows[1].IsError,
			"publication failure effects = commentary:%d tools:%+v", users, rows)
		first, firstErr := os.ReadFile(h.paths[0])
		second, secondErr := os.ReadFile(h.paths[1])
		requireApproval(t, errors.Is(firstErr, os.ErrNotExist) && secondErr == nil && string(second) == "approved\n",
			"committed session grant effects = first:%q/%v second:%q/%v", first, firstErr, second, secondErr)
		requireApproval(t, h.feed.resolved.Load() == 1, "terminal publications = %d, want 1", h.feed.resolved.Load())
		requireApprovalActionTerminal(t, h, approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowSession, &commentary))
	})
}
func TestApprovalActionClosedEngineAdmission(t *testing.T) {
	commentary := "must not survive Engine close"
	h := newApprovalActionHarness(t, approvalActionHarnessOptions{blockPending: true})
	done := beginApprovalAction(h, context.Background(), approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &commentary))
	requireApprovalActionBlocked(t, done, "answer before pending publication")
	closeDone := make(chan error, 1)
	go func() { closeDone <- h.engine.Close() }()
	closeDeadline := time.Now().Add(3 * time.Second)
	for !h.engine.BeginRetirement() {
		requireApproval(t, time.Now().Before(closeDeadline), "Engine close did not reject admission")
		time.Sleep(time.Millisecond)
	}
	h.release()
	closeErr := receiveApprovalAction(t, closeDone, "Engine close")
	requireApproval(t, closeErr == nil || errors.Is(closeErr, context.Canceled), "close Engine: %v", closeErr)
	result := receiveApprovalAction(t, done, "closed-Engine answer")
	requireApproval(t, result.err != nil, "closed-Engine Approval = %+v, want failure", result.results)
	requireApproval(t, waitApprovalHandle(t, h) != nil, "closed-Engine tool execution succeeded")
	users, _ := approvalActionRows(t, h, commentary)
	requireApproval(t, users == 0, "closed-Engine commentary rows = %d, want 0", users)
	_, statErr := os.Stat(h.paths[0])
	requireApproval(t, errors.Is(statErr, os.ErrNotExist), "closed-Engine tool effect: %v", statErr)
	requireApprovalActionTerminal(t, h, approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &commentary))
}
func TestApprovalActionResponseLossAndSessionGrant(t *testing.T) {
	commentary := "multi-target response loss"
	for _, test := range []struct {
		name       string
		options    approvalActionHarnessOptions
		decision   tools.AskQuestionApprovalDecision
		commentary *string
		allowed    bool
	}{
		{"multi-target Allow response loss", approvalActionHarnessOptions{multiTarget: true}, tools.AskQuestionApprovalDecisionAllowOnce, &commentary, true},
		{"later call observes session grant", approvalActionHarnessOptions{secondPatch: true}, tools.AskQuestionApprovalDecisionAllowSession, nil, true},
		{"Deny response loss", approvalActionHarnessOptions{}, tools.AskQuestionApprovalDecisionDeny, &commentary, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newApprovalActionHarness(t, test.options)
			payload := approvalActionAnswer(test.decision, test.commentary)
			requireApprovalActionOutcome(t, h.resolve(context.Background(), payload), PromptAnswerOutcomeResolved)
			err := waitApprovalHandle(t, h)
			requireApproval(t, err == nil, "accepted Approval: %v", err)
			requireApprovalActionEffects := ""
			if test.commentary != nil {
				requireApprovalActionEffects = *test.commentary
			}
			requireApprovalEffects(t, h, requireApprovalActionEffects, test.allowed)
			requireApprovalActionTerminal(t, h, payload)
			requireApproval(t, h.feed.resolved.Load() == 1, "Approval publications = %d, want 1", h.feed.resolved.Load())
		})
	}
}
func TestApprovalActionRuntimeInterruptWinnerOrders(t *testing.T) {
	commentary := "Runtime interrupt race"
	for _, interrupt := range []struct {
		name string
		run  func(*approvalActionHarness) (bool, error)
	}{
		{"InterruptCurrentAgentTurn", func(h *approvalActionHarness) (bool, error) {
			return h.authority.InterruptCurrentAgentTurn(context.Background(), h.sessionID, func() error { return ErrExecutionNoLongerLive })
		}},
		{"InterruptCurrentLiveRun", func(h *approvalActionHarness) (bool, error) {
			return h.authority.InterruptCurrentLiveRun(context.Background(), h.sessionID)
		}},
	} {
		for _, approvalFirst := range []bool{true, false} {
			name := "Interrupt first"
			if approvalFirst {
				name = "Approval first"
			}
			t.Run(interrupt.name+"/"+name, func(t *testing.T) {
				h := newApprovalActionHarness(t, approvalActionHarnessOptions{blockResolved: true})
				var answerDone <-chan promptBatchCallResult
				var interruptDone <-chan approvalActionInterruptResult
				if approvalFirst {
					answerDone = beginApprovalAction(h, context.Background(), approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &commentary))
					receiveApprovalAction(t, h.feed.resolvedStarted, "Approval finalization")
					interruptDone = beginApprovalActionInterrupt(h, interrupt.run)
					requireApprovalActionBlocked(t, interruptDone, "Interrupt before Approval finalization")
				} else {
					interruptDone = beginApprovalActionInterrupt(h, interrupt.run)
					receiveApprovalAction(t, h.feed.resolvedStarted, "Interrupt prompt closure")
					answerDone = beginApprovalAction(h, context.Background(), approvalActionAnswer(tools.AskQuestionApprovalDecisionAllowOnce, &commentary))
					requireApprovalActionBlocked(t, answerDone, "stale answer before prompt closure")
					requireApprovalActionBlocked(t, interruptDone, "Interrupt before prompt closure")
				}
				releaseApprovalActionGate(h.feed.resolvedGate, &h.feed.resolvedOnce)
				stopped := receiveApprovalAction(t, interruptDone, "Runtime interrupt")
				requireApproval(t, stopped.err == nil && stopped.interrupted, "Runtime interrupt = %+v", stopped)
				answer := receiveApprovalAction(t, answerDone, "Approval answer")
				want := PromptAnswerOutcomeSkipped
				if approvalFirst {
					want = PromptAnswerOutcomeResolved
				}
				requireApprovalActionOutcome(t, answer, want)
				waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_, err := h.handle.Wait(waitCtx)
				requireApproval(t, !errors.Is(err, context.DeadlineExceeded), "Runtime interrupt did not stop Agent Step")
				requireApprovalEffects(t, h, commentary, approvalFirst)
			})
		}
	}
}
