package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
)

// Real runtime publication and retention must compose while a Question is
// waiting, including when the operator interrupts and reopens its transcript.
func TestQuestionAnswerAndInterruptAcrossTranscriptReopen(t *testing.T) {
	for _, test := range []struct {
		name              string
		answer            bool
		delayedSupervisor bool
	}{
		{name: "answer", answer: true},
		{name: "interrupt_during_publication"},
		{name: "delayed_supervisor_interrupt_during_reopen", delayedSupervisor: true},
		{name: "delayed_supervisor_silent_completion_clears_review", answer: true, delayedSupervisor: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			exerciseQuestionResolution(t, test.answer, test.delayedSupervisor)
		})
	}
}

func TestTranscriptSubscriptionReleasesRetentionWhenRuntimeDrainsDuringAdmission(t *testing.T) {
	releaseFailure := errors.New("retention release failed")
	for _, test := range []struct {
		name       string
		releaseErr error
	}{
		{name: "released"},
		{name: "release_failure_is_reported", releaseErr: releaseFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRuntimeRegistry()
			engine := newRegistryTestRuntime(t, nil)
			ref := registryTestResourceRef(engine.SessionID())
			retaining, release := make(chan struct{}), make(chan struct{})
			releaseRetainer := sync.OnceFunc(func() { close(release) })
			defer releaseRetainer()
			var releases atomic.Int32
			if err := registry.ResourceReady(t.Context(), registryTestResource(ref), engine, func() (io.Closer, error) {
				close(retaining)
				<-release
				return subscriptionRetentionReleaseFunc(func() error {
					releases.Add(1)
					return test.releaseErr
				}), nil
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			subscribed := make(chan error, 1)
			go func() {
				sub, err := registry.SubscribeSessionTranscript(ctx, serverapi.TranscriptSubscribeRequest{SessionID: engine.SessionID()})
				if sub != nil {
					err = errors.Join(err, sub.Close(), errors.New("subscribed to retired runtime"))
				}
				subscribed <- err
			}()
			<-retaining
			drained := make(chan error, 1)
			go func() { drained <- registry.ResourceDraining(t.Context(), registryTestResource(ref)) }()
			select {
			case err := <-drained:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("runtime drain waited for pending subscription retention")
			}
			cancel()
			releaseRetainer()
			expectedErr := test.releaseErr
			if expectedErr == nil {
				expectedErr = context.Canceled
			}
			if err := <-subscribed; !errors.Is(err, expectedErr) {
				t.Fatalf("subscription error = %v, want %v", err, expectedErr)
			}
			if got := releases.Load(); got != 1 {
				t.Fatalf("retention releases = %d, want 1", got)
			}
		})
	}
}

type subscriptionRetentionReleaseFunc func() error

func (release subscriptionRetentionReleaseFunc) Close() error { return release() }

type questionDeadlockClient struct {
	calls    atomic.Int32
	generate func(context.Context, int32) (llm.Response, error)
}

func (c *questionDeadlockClient) Generate(ctx context.Context, _ llm.Request, _ llm.StreamCallbacks) (llm.Response, error) {
	return c.generate(ctx, c.calls.Add(1))
}

func questionDeadlockResponse(call, questionCall int32) llm.Response {
	if call == questionCall {
		return llm.Response{
			Assistant: llm.Message{
				Role: llm.RoleAssistant, Content: textutil.Value("Choosing a design"),
				Phase: textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: []llm.ToolCall{{
				ID: "question-deadlock-call", Name: string(toolspec.ToolAskQuestion),
				Input: json.RawMessage(`{"question":"Which design?","suggestions":["Native steering","Queued steering"],"recommended_option_index":1}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		}
	}
	return llm.Response{
		Assistant: llm.Message{
			Role: llm.RoleAssistant, Content: textutil.Value("Done"),
			Phase: textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}
}

func (*questionDeadlockClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true}, nil
}

type questionDeadlockStepLifecycle struct {
	registry *RuntimeRegistry
}

type questionSubscriptionLifecycle struct {
	registry  *RuntimeRegistry
	armed     atomic.Bool
	retaining chan struct{}
}

func (s *questionSubscriptionLifecycle) ResourceReady(ctx context.Context, resource sessionruntime.AgentResourceDescriptor, engine *runtime.Engine, retain sessionruntime.AgentResourceRetainer) error {
	return s.registry.ResourceReady(ctx, resource, engine, func() (io.Closer, error) {
		if s.armed.CompareAndSwap(true, false) {
			close(s.retaining)
		}
		return retain()
	})
}

func (s *questionSubscriptionLifecycle) ResourceDraining(ctx context.Context, resource sessionruntime.AgentResourceDescriptor) error {
	return s.registry.ResourceDraining(ctx, resource)
}

func (s questionDeadlockStepLifecycle) StepBegan(ctx context.Context, resource sessionruntime.AgentResourceDescriptor, snapshot runtime.StepLifecycleSnapshot) error {
	return runtimewire.NewStepLifecycleSink(resource.Ref.SessionID().String(), s.registry).StepBegan(ctx, snapshot)
}

func (s questionDeadlockStepLifecycle) StepEnded(ctx context.Context, resource sessionruntime.AgentResourceDescriptor, snapshot runtime.StepLifecycleSnapshot) error {
	return runtimewire.NewStepLifecycleSink(resource.Ref.SessionID().String(), s.registry).StepEnded(ctx, snapshot)
}

func exerciseQuestionResolution(t *testing.T, answer, delayedSupervisor bool) {
	t.Helper()
	root, workspace := t.TempDir(), t.TempDir()
	persistence := testsetup.OpenStore(t, root)
	binding, err := persistence.RegisterWorkspaceBinding(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.Create(
		filepath.Join(root, "projects", binding.ProjectID, "sessions"), "sessions",
		workspace, sessioncontract.SessionCategoryMain, persistence.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := session.NewOpenSessionDescriptor(id)
	if err != nil {
		t.Fatal(err)
	}
	filesystem, err := runtimewire.NewFilesystemContext(workspace, workspace, metadata.ProjectWorkspaceBoundary{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	settings := config.DefaultOnboardingSettings()
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200000
	settings.Reviewer.Frequency = "off"
	settings.ProviderOverride = "openai"
	settings.OpenAIBaseURL = "http://127.0.0.1:1/v1"
	questionCall := int32(1)
	releaseReviewer := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseReviewer) })
	defer release()
	if delayedSupervisor {
		settings.Reviewer.Frequency = "all"
		settings.Reviewer.Model = settings.Model
		questionCall = 2
	}
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: settings, FilesystemContext: filesystem,
		EnabledTools:     []toolspec.ID{toolspec.ToolAskQuestion},
		QuestionsEnabled: textutil.Value(true), AutoCompactionEnabled: textutil.Value(false),
		Client: &questionDeadlockClient{generate: func(_ context.Context, call int32) (llm.Response, error) {
			response := questionDeadlockResponse(call, questionCall)
			if delayedSupervisor && call > questionCall {
				response.Assistant.Content = textutil.Value("")
			}
			return response, nil
		}},
		ReviewerClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return &questionDeadlockClient{generate: func(ctx context.Context, _ int32) (llm.Response, error) {
				select {
				case <-releaseReviewer:
					return llm.Response{
						Assistant: llm.Message{
							Role:    llm.RoleAssistant,
							Content: textutil.Value(`{"suggestions":["Ask the user before continuing."]}`),
						},
						Usage: llm.Usage{WindowTokens: 200000},
					}, nil
				case <-ctx.Done():
					return llm.Response{}, context.Cause(ctx)
				}
			}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRuntimeRegistry().WithExecutionTargetResolver(persistence.ResolveOptionalSessionExecutionTarget)
	lifecycle := &questionSubscriptionLifecycle{registry: registry, retaining: make(chan struct{})}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: root, StoreOptions: persistence.AuthoritativeSessionStoreOptions(),
		ResourceLifecycle: lifecycle, PromptFeed: registry,
		StepLifecycle: questionDeadlockStepLifecycle{registry: registry},
		EventFeed: func(ref runtimeids.SessionResourceRef, event runtime.Event) {
			if event.LocalEntry != nil && event.LocalEntry.ReviewerError != nil {
				t.Logf("Supervisor error: %+v", event.LocalEntry.ReviewerError)
			}
			if err := registry.PublishAuthorityRuntimeEvent(ref, event); err != nil {
				panic(err)
			}
		},
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	attachment, err := authority.OpenRuntime(t.Context(), sessionruntime.RuntimeOpenRequest{
		SessionID: id, OwnerID: "original-client", Runtime: &plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := registry.SubscribeSessionTranscript(t.Context(), serverapi.TranscriptSubscribeRequest{SessionID: id.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = subscription.Close() })
	handle, err := authority.StartAgentExecution(t.Context(), sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor, Resource: sessionruntime.CurrentAgentResource{},
		Runner: func(ctx context.Context, _ sessionruntime.ExecutionScope, bridge sessionruntime.AgentRuntimeBridge) error {
			return bridge.WithEngine(ctx, func(ctx context.Context, engine *runtime.Engine) error {
				_, err := engine.SubmitUserMessage(ctx, "Choose a native steering design")
				return err
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if delayedSupervisor {
		if _, err := handle.Wait(t.Context()); err != nil {
			t.Fatalf("complete original execution before Supervisor returns: %v", err)
		}
		if _, live := authority.SessionExecution(id); live {
			t.Fatal("original execution remained active while Supervisor waited")
		}
		release()
	}
	var pendingToolCallID clientui.ToolCallID
	promptCtx, cancelPrompt := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelPrompt()
	for {
		message, err := subscription.Next(promptCtx)
		if err != nil {
			t.Fatal(err)
		}
		if message.Kind() != clientui.TranscriptMessagePrompt {
			continue
		}
		prompt := message.Payload().(clientui.TranscriptPrompt)
		if prompt.Status != clientui.TranscriptPromptStatusPending {
			continue
		}
		pendingToolCallID = prompt.ToolCallID
		if delayedSupervisor {
			followUp, live := authority.SessionExecution(id)
			if !live || followUp.Scope().ID() == handle.Scope().ID() {
				t.Fatal("delayed Supervisor question has no new exact execution")
			}
			handle = followUp
		}
		if answer {
			answers, err := authority.ResolvePromptBatch(t.Context(), id, prompt.StepID, []sessionruntime.PromptAnswerCommand{{
				ToolCallID: prompt.ToolCallID,
				Payload: sessionruntime.PromptQuestionAnswerCommand{
					Answer: tools.AskQuestionAnswer{Freeform: textutil.Value("Native steering")},
				},
			}})
			if err != nil || len(answers) != 1 || answers[0].Outcome != sessionruntime.PromptAnswerOutcomeResolved {
				t.Fatalf("resolve question: answers=%+v err=%v", answers, err)
			}
		}
		break
	}
	if !answer {
		if err := subscription.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := attachment.Release(t.Context(), sessionruntime.RuntimeReleaseDetach); err != nil {
			t.Fatal(err)
		}
		if _, err := authority.OpenRuntime(t.Context(), sessionruntime.RuntimeOpenRequest{
			SessionID: id, OwnerID: "reopened-client", Runtime: &plan,
		}); err != nil {
			t.Fatal(err)
		}
		reopenedSubscription, err := registry.SubscribeSessionTranscript(t.Context(), serverapi.TranscriptSubscribeRequest{SessionID: id.String()})
		if err != nil {
			t.Fatal(err)
		}
		hydration := transcriptPayload[clientui.TranscriptHydration](t, nextTranscriptMessage(t, reopenedSubscription))
		if len(hydration.PendingPrompts) != 1 || hydration.PendingPrompts[0].ToolCallID != pendingToolCallID {
			t.Fatalf("reopened transcript did not restore the pending question: %+v", hydration.PendingPrompts)
		}
		if err := reopenedSubscription.Close(); err != nil {
			t.Fatal(err)
		}

		interruptEntered, releaseInterrupt := make(chan struct{}), make(chan struct{})
		interrupted := make(chan error, 1)
		go func() {
			interrupted <- authority.WithInterruptibleAgentTurn(t.Context(), id, nil, func(_ context.Context, engine *runtime.Engine) error {
				close(interruptEntered)
				<-releaseInterrupt
				return engine.Interrupt()
			})
		}()
		<-interruptEntered

		publicationEntered, releasePublication := make(chan struct{}), make(chan struct{})
		var publicationGate sync.Once
		registry.WithExecutionTargetResolver(func(ctx context.Context, sessionID string) (*clientui.SessionExecutionTarget, error) {
			publicationGate.Do(func() {
				close(publicationEntered)
				<-releasePublication
			})
			return persistence.ResolveOptionalSessionExecutionTarget(ctx, sessionID)
		})
		published := make(chan error, 1)
		go func() { published <- registry.PublishSessionIdentity(id.String()) }()
		<-publicationEntered

		lifecycle.armed.Store(true)
		reopened := make(chan error, 1)
		go func() {
			sub, err := registry.SubscribeSessionTranscript(t.Context(), serverapi.TranscriptSubscribeRequest{SessionID: id.String()})
			if err == nil {
				_, err = sub.Next(t.Context())
				err = errors.Join(err, sub.Close())
			}
			reopened <- err
		}()
		<-lifecycle.retaining
		close(releaseInterrupt)
		close(releasePublication)
		if err := <-interrupted; err != nil {
			t.Fatalf("interrupt: %v", err)
		}
		if err := <-reopened; err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if err := <-published; err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	if _, err := handle.Wait(t.Context()); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("execution: %v", err)
	}
	if delayedSupervisor {
		view, available := registry.RuntimeMainViewSnapshot(id.String())
		if !available || view.Activity.Reviewer != clientui.ReviewerActivityInactive ||
			view.Activity.State != clientui.RuntimeActivityRegisteredIdle {
			t.Fatalf("completed Supervisor turn left the client activity stuck: available=%t activity=%+v", available, view.Activity)
		}
	}
}
