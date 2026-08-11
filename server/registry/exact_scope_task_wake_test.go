package registry

import (
	"context"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/metadata"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

type exactScopeTaskWakeClient struct{}

func (exactScopeTaskWakeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("done"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
	}, nil
}

func (exactScopeTaskWakeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{ProviderID: "test", SupportsResponsesAPI: true}, nil
}

type exactScopeTaskWakeEvents struct {
	mu     sync.Mutex
	events []serverapi.WorkflowProjectEvent
}

func (p *exactScopeTaskWakeEvents) publish(_ context.Context, event serverapi.WorkflowProjectEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return nil
}

func (p *exactScopeTaskWakeEvents) snapshot() []serverapi.WorkflowProjectEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]serverapi.WorkflowProjectEvent(nil), p.events...)
}

func TestPromptPendingScopePublishesTaskWakeOnlyFromWorkflowScope(t *testing.T) {
	persistenceRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(
		persistenceRoot,
		"exact-scope-test",
		workspaceRoot,
		sessioncontract.SessionCategoryMain,
		persistence.Options()...,
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}

	settings := config.DefaultOnboardingSettings()
	settings.ProviderOverride = "openai"
	settings.Model = "gpt-5"
	settings.OpenAIBaseURL = "http://127.0.0.1:1/v1"
	filesystemContext, err := runtimewire.NewFilesystemContext(
		workspaceRoot,
		workspaceRoot,
		metadata.ProjectWorkspaceBoundary{ProjectID: "exact-scope-test"},
	)
	if err != nil {
		t.Fatalf("new filesystem context: %v", err)
	}
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings:          settings,
		FilesystemContext: filesystemContext,
		Client:            exactScopeTaskWakeClient{},
	})
	if err != nil {
		t.Fatalf("new runtime plan: %v", err)
	}
	events := &exactScopeTaskWakeEvents{}
	registry := NewRuntimeRegistry().WithWorkflowEventPublisher(events.publish)
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot:   persistenceRoot,
		ResourceLifecycle: registry,
		StoreOptions:      persistence.Options(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	if _, err := authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "exact-scope-test",
		Runtime:   &plan,
	}); err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new session descriptor: %v", err)
	}
	node, err := workflow.NewCurrentNodeReference("task-exact-scope", "node-exact-scope", nil)
	if err != nil {
		t.Fatalf("new current node: %v", err)
	}
	workflowRef := sessionruntime.WorkflowExecutionRef{
		ProjectID:   "project-exact-scope",
		WorkflowID:  runtimeids.NewWorkflowID(),
		CurrentNode: node,
	}
	lease, err := authority.NewWorkflowExecutionLease(workflowRef)
	if err != nil {
		t.Fatalf("new workflow lease: %v", err)
	}
	lease.Release()
	request := askquestion.AskQuestionRequest{
		ID:       "ask-exact-scope",
		StepID:   registryTestStepID,
		Question: "Continue?",
	}
	workflowID := workflowRef.WorkflowID
	request.AttentionTarget = &clientui.AttentionNotificationTarget{
		Kind:       clientui.AttentionNotificationTargetWorkflowTask,
		ProjectID:  workflowRef.ProjectID,
		WorkflowID: &workflowID,
		TaskID:     string(workflowRef.CurrentNode.TaskID),
		SessionID:  sessionID.String(),
	}
	workflowPromptDone := make(chan error, 1)
	workflowHandle, err := authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor,
		Runtime:    &plan,
		Workflow:   &lease,
		Resource:   sessionruntime.OpenAgentResource{},
		Runner: func(ctx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			err := registry.PromptPendingScope(scope, request, time.Now().UTC())
			workflowPromptDone <- err
			<-ctx.Done()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start workflow execution: %v", err)
	}
	if err := <-workflowPromptDone; err != nil {
		t.Fatalf("workflow prompt projection: %v", err)
	}
	projected := events.snapshot()
	if len(projected) != 1 {
		t.Fatalf("workflow wake events = %+v, want one", projected)
	}
	event := projected[0]
	if event.ProjectID == nil || *event.ProjectID != workflowRef.ProjectID ||
		event.WorkflowID == nil || *event.WorkflowID != workflowRef.WorkflowID ||
		event.Resource != serverapi.WorkflowProjectEventResourceTask ||
		event.Action != serverapi.WorkflowProjectEventActionQuestionWaiting ||
		event.PrimaryEntityID != string(workflowRef.CurrentNode.TaskID) ||
		len(event.RelatedIDs) != 2 ||
		event.RelatedIDs[0] != sessionID.String() ||
		event.RelatedIDs[1] != request.ID {
		t.Fatalf("workflow wake event = %+v", event)
	}
	if err := registry.PromptResolvedScope(workflowHandle.Scope(), request.ID); err != nil {
		t.Fatalf("resolve workflow prompt projection: %v", err)
	}
	projected = events.snapshot()
	if len(projected) != 2 {
		t.Fatalf("workflow prompt events = %+v, want waiting and cleared", projected)
	}
	cleared := projected[1]
	if cleared.ProjectID == nil || *cleared.ProjectID != workflowRef.ProjectID ||
		cleared.WorkflowID == nil || *cleared.WorkflowID != workflowRef.WorkflowID ||
		cleared.Resource != serverapi.WorkflowProjectEventResourceTask ||
		cleared.Action != serverapi.WorkflowProjectEventActionQuestionCleared ||
		cleared.PrimaryEntityID != string(workflowRef.CurrentNode.TaskID) ||
		len(cleared.RelatedIDs) != 2 ||
		cleared.RelatedIDs[0] != sessionID.String() ||
		cleared.RelatedIDs[1] != request.ID {
		t.Fatalf("workflow cleared event = %+v", cleared)
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelStop()
	if err := workflowHandle.Stop(stopCtx); err != nil {
		t.Fatalf("stop workflow execution: %v", err)
	}

	nonWorkflowRequest := request
	nonWorkflowRequest.AttentionTarget = nil
	nonWorkflowPromptDone := make(chan error, 1)
	nonWorkflowHandle, err := authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor,
		Runtime:    &plan,
		Resource:   sessionruntime.OpenAgentResource{},
		Runner: func(ctx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			err := registry.PromptPendingScope(scope, nonWorkflowRequest, time.Now().UTC())
			nonWorkflowPromptDone <- err
			<-ctx.Done()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("start non-workflow execution: %v", err)
	}
	if err := <-nonWorkflowPromptDone; err != nil {
		t.Fatalf("non-workflow prompt projection: %v", err)
	}
	if projected := events.snapshot(); len(projected) != 2 {
		t.Fatalf("non-workflow wake events = %+v, want workflow event only", projected)
	}
	nonWorkflowStopCtx, cancelNonWorkflowStop := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelNonWorkflowStop()
	if err := nonWorkflowHandle.Stop(nonWorkflowStopCtx); err != nil {
		t.Fatalf("stop non-workflow execution: %v", err)
	}
}
