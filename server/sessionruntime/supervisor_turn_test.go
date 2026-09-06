package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type supervisorModelExchange struct {
	request llm.Request
	reply   chan llm.Response
}

type supervisorTurnHarness struct {
	fixture   sessionRuntimeFixture
	authority *Authority
	engine    *runtime.Engine
	sessionID runtimeids.SessionID
	ctx       context.Context
	main      chan supervisorModelExchange
	reviewer  chan supervisorModelExchange
	prompts   authorityPromptFeed
}

func newSupervisorTurnHarness(t *testing.T) *supervisorTurnHarness {
	t.Helper()
	fixture := newSessionRuntimeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	h := &supervisorTurnHarness{
		fixture: fixture, sessionID: lifecycleSessionID(t, fixture), ctx: ctx,
		main: make(chan supervisorModelExchange, 8), reviewer: make(chan supervisorModelExchange, 8),
		prompts: make(authorityPromptFeed, 8),
	}
	model := func(requests chan supervisorModelExchange) llm.Client {
		return supervisorQuestionModel(func(ctx context.Context, request llm.Request) (llm.Response, error) {
			exchange := supervisorModelExchange{request: request, reply: make(chan llm.Response, 1)}
			select {
			case requests <- exchange:
			case <-ctx.Done():
				return llm.Response{}, context.Cause(ctx)
			}
			select {
			case response := <-exchange.reply:
				return response, nil
			case <-ctx.Done():
				return llm.Response{}, context.Cause(ctx)
			}
		})
	}
	reviewer := model(h.reviewer)
	settings := fixture.config.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200000
	settings.Reviewer.Frequency = "all"
	plan, err := NewAgentRuntimePlan(AgentRuntimePlanOptions{
		Settings: settings, Client: model(h.main),
		EnabledTools:          []toolspec.ID{toolspec.ToolAskQuestion},
		FilesystemContext:     runtimeTestFilesystemContext(t, fixture.config.WorkspaceRoot),
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		ProviderCapabilitiesOverride: &llm.ProviderCapabilities{
			ProviderID: "openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: true,
		},
		ReviewerClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return reviewer, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.authority = NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		PromptFeed:      h.prompts,
	})
	t.Cleanup(func() {
		cancel()
		done := make(chan error, 1)
		go func() {
			var closeErr error
			if h.engine != nil {
				closeErr = h.engine.Close()
			}
			done <- errors.Join(closeErr, h.authority.Close(context.Background()))
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("close Supervisor harness: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Supervisor harness cleanup timed out")
		}
	})
	openLifecycleRuntime(t, h.authority, h.sessionID, "supervisor-turn-test", &plan)
	if err := h.authority.WithCurrentRuntime(ctx, h.sessionID, func(_ context.Context, engine *runtime.Engine) error {
		h.engine = engine
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *supervisorTurnHarness) startHuman(t *testing.T) ExecutionHandle {
	t.Helper()
	handle, err := h.authority.StartAgentExecution(h.ctx, AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, h.sessionID), Resource: CurrentAgentResource{},
		Runner: func(ctx context.Context, _ ExecutionScope, bridge AgentRuntimeBridge) error {
			return bridge.WithEngine(ctx, func(ctx context.Context, engine *runtime.Engine) error {
				_, err := engine.SubmitUserMessage(ctx, "Complete the task")
				return err
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func (h *supervisorTurnHarness) awaitModel(t *testing.T, requests <-chan supervisorModelExchange) supervisorModelExchange {
	t.Helper()
	select {
	case exchange := <-requests:
		return exchange
	case <-h.ctx.Done():
		t.Fatal("timed out waiting for model dispatch")
		return supervisorModelExchange{}
	}
}

func supervisorFinalResponse(content string) llm.Response {
	return llm.Response{
		Assistant: llm.Message{
			Role: llm.RoleAssistant, Content: textutil.Value(content),
			Phase: textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}
}

func (h *supervisorTurnHarness) finishOriginal(t *testing.T) supervisorModelExchange {
	t.Helper()
	handle := h.startHuman(t)
	h.awaitModel(t, h.main).reply <- supervisorFinalResponse("Done")
	review := h.awaitModel(t, h.reviewer)
	if _, err := handle.Wait(h.ctx); err != nil {
		t.Fatalf("finish original execution: %v", err)
	}
	if _, live := h.authority.SessionExecution(h.sessionID); live {
		t.Fatal("original execution did not retire")
	}
	return review
}

func TestSupervisorStartedQuestionCanBeInterrupted(t *testing.T) {
	h := newSupervisorTurnHarness(t)
	review := h.finishOriginal(t)
	review.reply <- supervisorFinalResponse(`{"suggestions":["Ask before proceeding."]}`)
	h.awaitModel(t, h.main).reply <- llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseCommentary)},
		ToolCalls: []llm.ToolCall{{
			ID: "supervisor-interrupt-question", Name: string(toolspec.ToolAskQuestion),
			Input: json.RawMessage(`{"question":"Proceed?"}`),
		}},
		Usage: llm.Usage{WindowTokens: 200000},
	}
	var pending authorityPromptEvent
	select {
	case pending = <-h.prompts:
	case <-h.ctx.Done():
		t.Fatal("Supervisor question was not published")
	}
	handle, live := h.authority.SessionExecution(h.sessionID)
	if !live || handle.Scope().ID() != pending.scopeID {
		t.Fatal("Supervisor question has no current exact execution")
	}
	if interrupted, err := h.authority.InterruptCurrentAgentTurn(h.ctx, h.sessionID, nil); err != nil || !interrupted {
		t.Fatalf("interrupt Supervisor question = (%t, %v)", interrupted, err)
	}
	if _, err := handle.Wait(h.ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Supervisor execution result = %v, want canceled", err)
	}
	select {
	case resolved := <-h.prompts:
		if !resolved.resolved || resolved.requestID != pending.requestID {
			t.Fatalf("question resolution = %+v", resolved)
		}
	case <-h.ctx.Done():
		t.Fatal("interrupted question remained pending")
	}
	if _, live := h.authority.SessionExecution(h.sessionID); live {
		t.Fatal("interrupted Supervisor execution remained current")
	}
	if activity := h.engine.ReviewerActivity(); activity != clientui.ReviewerActivityInactive {
		t.Fatalf("idle Supervisor activity = %s", activity)
	}
}

func (h *supervisorTurnHarness) awaitActivity(t *testing.T, want clientui.ReviewerActivity) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if h.engine.ReviewerActivity() == want {
			return
		}
		select {
		case <-ticker.C:
		case <-h.ctx.Done():
			t.Fatalf("Supervisor activity = %s, want %s", h.engine.ReviewerActivity(), want)
		}
	}
}

func (h *supervisorTurnHarness) awaitPendingOperation(t *testing.T) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !h.engine.HasPendingRuntimeOperations() {
		select {
		case <-ticker.C:
		case <-h.ctx.Done():
			t.Fatal("steer did not reach the next Agent Step boundary")
		}
	}
}

func requireSupervisorRequestMessage(t *testing.T, request llm.Request, kind llm.MessageType) {
	t.Helper()
	if !supervisorRequestHasMessage(request, kind) {
		t.Fatalf("model request did not contain %s", kind)
	}
}

func supervisorRequestHasMessage(request llm.Request, kind llm.MessageType) bool {
	for _, item := range request.Items {
		if item.MessageType != nil && *item.MessageType == kind {
			return true
		}
	}
	return false
}

func (h *supervisorTurnHarness) queueOrdinarySteer(t *testing.T) <-chan error {
	t.Helper()
	steer, err := runtime.NewAgentSteer(runtimeids.NewSessionID(), "Also verify the result.")
	if err != nil {
		t.Fatal(err)
	}
	if h.engine.HasPendingRuntimeOperations() {
		t.Fatal("unexpected pending operation before ordinary steer")
	}
	descriptor := mustOpenSessionDescriptor(t, h.sessionID)
	steered := make(chan error, 1)
	steered <- h.authority.RunCurrentTurn(h.ctx, descriptor,
		func(commit func() (bool, error)) (bool, error) { return commit() },
		func(ctx context.Context, engine *runtime.Engine, accept runtime.CommandAcceptance) error {
			_, err := engine.QueueAgentSteer(ctx, steer, accept)
			return err
		})
	return steered
}

func (h *supervisorTurnHarness) awaitSteer(t *testing.T, steered <-chan error) {
	t.Helper()
	select {
	case err := <-steered:
		if err != nil {
			t.Fatalf("queue ordinary steer during Supervisor turn: %v", err)
		}
	case <-h.ctx.Done():
		t.Fatal("ordinary steer was not accepted")
	}
}

func TestSupervisorTurnStaysMarkedThroughOrdinarySteerAndResetsAtIdle(t *testing.T) {
	h := newSupervisorTurnHarness(t)
	review := h.finishOriginal(t)
	review.reply <- supervisorFinalResponse(`{"suggestions":["Reconsider the result."]}`)
	followUp := h.awaitModel(t, h.main)
	requireSupervisorRequestMessage(t, followUp.request, llm.MessageTypeReviewerFeedback)
	handle, live := h.authority.SessionExecution(h.sessionID)
	if !live {
		t.Fatal("Supervisor follow-up has no exact execution")
	}
	if activity := h.engine.ReviewerActivity(); activity != clientui.ReviewerActivityAddressingFeedback {
		t.Fatalf("Supervisor follow-up activity = %s", activity)
	}
	steered := h.queueOrdinarySteer(t)
	commentary := supervisorFinalResponse("Still reconsidering")
	commentary.Assistant.Phase = textutil.Value(llm.MessagePhaseCommentary)
	followUp.reply <- commentary
	h.awaitSteer(t, steered)
	afterSteer := h.awaitModel(t, h.main)
	requireSupervisorRequestMessage(t, afterSteer.request, llm.MessageTypeAgentSteer)
	current, live := h.authority.SessionExecution(h.sessionID)
	if !live || current.Scope().ID() != handle.Scope().ID() {
		t.Fatalf("ordinary steer did not remain in Supervisor exact execution (current: %t, activity: %s)", live, h.engine.ReviewerActivity())
	}
	if activity := h.engine.ReviewerActivity(); activity != clientui.ReviewerActivityAddressingFeedback {
		t.Fatalf("ordinary steer cleared Supervisor activity: %s", activity)
	}
	afterSteer.reply <- supervisorFinalResponse("Verified")
	if _, err := handle.Wait(h.ctx); err != nil {
		t.Fatalf("finish Supervisor turn: %v", err)
	}
	h.awaitActivity(t, clientui.ReviewerActivityInactive)
	if _, live := h.authority.SessionExecution(h.sessionID); live {
		t.Fatal("Supervisor turn did not retire")
	}
	select {
	case <-h.reviewer:
		t.Fatal("Supervisor turn recursively invoked review")
	default:
	}

	next := h.startHuman(t)
	h.awaitModel(t, h.main).reply <- supervisorFinalResponse("Next task complete")
	nextReview := h.awaitModel(t, h.reviewer)
	nextReview.reply <- supervisorFinalResponse(`{"suggestions":[]}`)
	if _, err := next.Wait(h.ctx); err != nil {
		t.Fatalf("finish next ordinary turn: %v", err)
	}
	h.awaitActivity(t, clientui.ReviewerActivityInactive)
}

func TestSupervisorFeedbackJoinsBusyHumanExactExecution(t *testing.T) {
	h := newSupervisorTurnHarness(t)
	review := h.finishOriginal(t)
	second := h.startHuman(t)
	working := h.awaitModel(t, h.main)
	if h.engine.HasPendingRuntimeOperations() {
		t.Fatal("unexpected pending operation before Supervisor returns")
	}
	review.reply <- supervisorFinalResponse(`{"suggestions":["Check the result before finishing."]}`)
	h.awaitPendingOperation(t)
	commentary := supervisorFinalResponse("Still working")
	commentary.Assistant.Phase = textutil.Value(llm.MessagePhaseCommentary)
	working.reply <- commentary
	withFeedback := h.awaitModel(t, h.main)
	if !supervisorRequestHasMessage(withFeedback.request, llm.MessageTypeReviewerFeedback) {
		// Applying the Reviewer result and submitting its ordinary steer are
		// separate operations. Keep the human turn active for either ordering.
		current, live := h.authority.SessionExecution(h.sessionID)
		if !live || current.Scope().ID() != second.Scope().ID() {
			t.Fatal("human execution retired before Supervisor feedback admission")
		}
		h.awaitActivity(t, clientui.ReviewerActivityAddressingFeedback)
		withFeedback.reply <- commentary
		withFeedback = h.awaitModel(t, h.main)
	}
	requireSupervisorRequestMessage(t, withFeedback.request, llm.MessageTypeReviewerFeedback)
	current, live := h.authority.SessionExecution(h.sessionID)
	if !live || current.Scope().ID() != second.Scope().ID() {
		t.Fatal("busy Supervisor feedback did not join the existing human exact execution")
	}
	if activity := h.engine.ReviewerActivity(); activity != clientui.ReviewerActivityAddressingFeedback {
		t.Fatalf("busy Supervisor feedback activity = %s", activity)
	}
	withFeedback.reply <- supervisorFinalResponse("Checked")
	if _, err := second.Wait(h.ctx); err != nil {
		t.Fatalf("finish human execution with Supervisor feedback: %v", err)
	}
	h.awaitActivity(t, clientui.ReviewerActivityInactive)
	select {
	case <-h.reviewer:
		t.Fatal("human turn containing Supervisor feedback recursively invoked review")
	default:
	}
}

func TestSupervisorFinalBoundarySteerKeepsQuestionOwned(t *testing.T) {
	h := newSupervisorTurnHarness(t)
	review := h.finishOriginal(t)
	review.reply <- supervisorFinalResponse(`{"suggestions":["Reconsider the result."]}`)
	followUp := h.awaitModel(t, h.main)
	originalRun, err := h.engine.CaptureActiveRunResult(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	steered := h.queueOrdinarySteer(t)
	followUp.reply <- supervisorFinalResponse("Reconsidered")
	h.awaitSteer(t, steered)
	afterSteer := h.awaitModel(t, h.main)
	requireSupervisorRequestMessage(t, afterSteer.request, llm.MessageTypeAgentSteer)
	continuedRun, err := h.engine.CaptureActiveRunResult(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	activity := h.engine.ReviewerActivity()
	_, ownedAtDispatch := h.authority.SessionExecution(h.sessionID)
	t.Logf("after final boundary: exact execution present=%t, Supervisor activity=%s", ownedAtDispatch, activity)
	afterSteer.reply <- llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseCommentary)},
		ToolCalls: []llm.ToolCall{{
			ID: "final-boundary-question", Name: string(toolspec.ToolAskQuestion),
			Input: json.RawMessage(`{"question":"Proceed with the verification?"}`),
		}},
		Usage: llm.Usage{WindowTokens: 200000},
	}
	var pending authorityPromptEvent
	select {
	case pending = <-h.prompts:
	case <-h.ctx.Done():
		_, live := h.authority.SessionExecution(h.sessionID)
		t.Fatalf("ordinary steer after Supervisor final called ask_question, but Authority served no pending question (current execution: %t, active live run: %t, Supervisor activity: %s)",
			live, h.engine.HasActiveLiveRunGroup(), h.engine.ReviewerActivity())
	}
	current, live := h.authority.SessionExecution(h.sessionID)
	if !live || current.Scope().ID() != pending.scopeID {
		t.Fatal("final-boundary question has no current exact execution")
	}
	if err := resolveAuthorityQuestionForTest(h.authority, h.sessionID, pending.stepID, pending.requestID, testQuestionResolution("yes")); err != nil {
		t.Fatalf("answer final-boundary question: %v", err)
	}
	h.awaitModel(t, h.main).reply <- supervisorFinalResponse("Verified")
	if _, err := current.Wait(h.ctx); err != nil {
		t.Fatalf("finish final-boundary continuation: %v", err)
	}
	original, err := originalRun.Wait()
	if err != nil {
		t.Fatal(err)
	}
	continued, err := continuedRun.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if original.GroupID == continued.GroupID && activity != clientui.ReviewerActivityAddressingFeedback {
		t.Fatalf("same live turn lost Supervisor activity across final boundary: %s", activity)
	}
}
