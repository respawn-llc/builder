package sessionruntime

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type supervisorQuestionModel func(context.Context, llm.Request) (llm.Response, error)

func (supervisorQuestionModel) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true}, nil
}

func (f supervisorQuestionModel) Generate(ctx context.Context, request llm.Request, _ llm.StreamCallbacks) (llm.Response, error) {
	return f(ctx, request)
}

func TestSupervisorQuestionAfterOriginalExecutionRetires(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	reviewerStarted := make(chan struct{})
	releaseReviewer := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseReviewer) })
	defer release()
	questionCalled := make(chan struct{})
	answerConsumed := make(chan struct{})
	var calls atomic.Int32
	final := llm.Response{
		Assistant: llm.Message{
			Role: llm.RoleAssistant, Content: textutil.Value("Done"),
			Phase: textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}
	mainModel := supervisorQuestionModel(func(context.Context, llm.Request) (llm.Response, error) {
		switch calls.Add(1) {
		case 2:
			close(questionCalled)
			return llm.Response{
				Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseCommentary)},
				ToolCalls: []llm.ToolCall{{
					ID: "supervisor-question", Name: string(toolspec.ToolAskQuestion),
					Input: json.RawMessage(`{"question":"Apply the suggested change?"}`),
				}},
				Usage: llm.Usage{WindowTokens: 200000},
			}, nil
		case 3:
			close(answerConsumed)
		}
		return final, nil
	})
	reviewer := supervisorQuestionModel(func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		close(reviewerStarted)
		select {
		case <-releaseReviewer:
			return llm.Response{
				Assistant: llm.Message{
					Role:    llm.RoleAssistant,
					Content: textutil.Value(`{"suggestions":["Ask the user before applying the change."]}`),
				},
				Usage: llm.Usage{WindowTokens: 200000},
			}, nil
		case <-ctx.Done():
			return llm.Response{}, context.Cause(ctx)
		}
	})
	settings := fixture.config.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200000
	settings.Reviewer.Frequency = "all"
	plan, err := NewAgentRuntimePlan(AgentRuntimePlanOptions{
		Settings: settings, Client: mainModel,
		EnabledTools:          []toolspec.ID{toolspec.ToolAskQuestion},
		FilesystemContext:     runtimeTestFilesystemContext(t, fixture.config.WorkspaceRoot),
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		ReviewerClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return reviewer, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	feed := make(authorityPromptFeed, 4)
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		PromptFeed:      feed,
	})
	var engine *runtime.Engine
	t.Cleanup(func() {
		// An orphan question has no Authority execution to interrupt. Closing the
		// Engine cancels its lifecycle-owned broker wait before Authority drains.
		done := make(chan error, 1)
		go func() {
			if engine != nil {
				if err := engine.Close(); err != nil {
					done <- err
					return
				}
			}
			done <- authority.Close(context.Background())
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("close Supervisor runtime: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Supervisor runtime cleanup did not finish")
		}
	})
	openLifecycleRuntime(t, authority, sessionID, "supervisor-test", &plan)
	if err := authority.WithCurrentRuntime(ctx, sessionID, func(_ context.Context, current *runtime.Engine) error {
		engine = current
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	handle, err := authority.StartAgentExecution(ctx, AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Resource:   CurrentAgentResource{},
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
	if _, err := handle.Wait(ctx); err != nil {
		t.Fatalf("original answer did not complete: %v", err)
	}
	if _, live := authority.SessionExecution(sessionID); live {
		t.Fatal("original exact execution did not retire")
	}
	select {
	case <-reviewerStarted:
	case <-ctx.Done():
		t.Fatal("Supervisor model did not start")
	}
	release()
	select {
	case <-questionCalled:
	case <-ctx.Done():
		t.Fatal("Supervisor follow-up did not call ask_question")
	}
	select {
	case pending := <-feed:
		current, live := authority.SessionExecution(sessionID)
		if !live || current.Scope().ID() != pending.scopeID || pending.scopeID == handle.Scope().ID() {
			t.Fatal("Supervisor question was not owned by a new current exact execution")
		}
		if err := resolveAuthorityQuestionForTest(authority, sessionID, pending.stepID, pending.requestID, testQuestionResolution("yes")); err != nil {
			t.Fatalf("answer Supervisor question through Authority: %v", err)
		}
		select {
		case <-answerConsumed:
		case <-ctx.Done():
			t.Fatal("Supervisor follow-up did not resume after the answer")
		}
		if _, err := current.Wait(ctx); err != nil {
			t.Fatalf("Supervisor follow-up did not complete: %v", err)
		}
	case <-ctx.Done():
		_, live := authority.SessionExecution(sessionID)
		t.Fatalf("Supervisor follow-up called ask_question after the original exact execution retired, but Authority served no pending question (current execution: %t)", live)
	}
}
