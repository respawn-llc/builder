package sessionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

func TestStalePredecessorFinalizationCannotRemoveResumedSuccessor(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true executable unavailable: %v", err)
	}
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep executable unavailable: %v", err)
	}

	predecessor := WorkflowExecutionRef{
		RunID:      workflow.RunID(uuid.NewString()),
		Generation: 1,
	}
	successor := WorkflowExecutionRef{
		RunID:      predecessor.RunID,
		Generation: 2,
	}
	type startResult struct {
		handle ExecutionHandle
		err    error
	}
	successorStarted := make(chan startResult, 1)
	successorCancellationGrace := 50 * time.Millisecond

	var authority *Authority
	authority = NewAuthority(AuthorityOptions{
		ExecutionFinalized: ExecutionFinalizedFunc(func(finalized WorkflowExecutionRef) {
			if finalized != predecessor {
				return
			}
			handle, startErr := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
				Workflow: &successor,
				Command: ScriptCommand{
					Path:              sleepPath,
					Args:              []string{"30"},
					CancellationGrace: &successorCancellationGrace,
				},
			})
			successorStarted <- startResult{handle: handle, err: startErr}
		}),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	predecessorHandle, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: &predecessor,
		Command:  ScriptCommand{Path: truePath},
	})
	if err != nil {
		t.Fatalf("start predecessor: %v", err)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWait()
	if _, err := predecessorHandle.Wait(waitCtx); err != nil {
		t.Fatalf("wait predecessor: %v", err)
	}

	var successorResult startResult
	select {
	case successorResult = <-successorStarted:
	case <-waitCtx.Done():
		t.Fatal("successor was not admitted from predecessor finalization")
	}
	if successorResult.err != nil {
		t.Fatalf("start successor: %v", successorResult.err)
	}
	if successorResult.handle == nil {
		t.Fatal("successor handle is nil")
	}

	assertSuccessorCurrent := func(stage string) {
		t.Helper()
		current, ok := authority.ExecutionByWorkflow(successor)
		if !ok {
			t.Fatalf("%s: successor execution is not indexed", stage)
		}
		if current.Scope().ID() != successorResult.handle.Scope().ID() {
			t.Fatalf(
				"%s: indexed scope = %q, want successor scope %q",
				stage,
				current.Scope().ID(),
				successorResult.handle.Scope().ID(),
			)
		}
	}
	assertSuccessorCurrent("after predecessor wait")

	if err := predecessorHandle.Close(waitCtx); err != nil {
		t.Fatalf("close predecessor: %v", err)
	}
	if err := predecessorHandle.Close(waitCtx); err != nil {
		t.Fatalf("close predecessor again: %v", err)
	}
	assertSuccessorCurrent("after predecessor close")

	if err := successorResult.handle.Stop(waitCtx); err != nil {
		t.Fatalf("stop successor: %v", err)
	}
	if err := successorResult.handle.Close(waitCtx); err != nil {
		t.Fatalf("close successor: %v", err)
	}
}

func TestNewLazyWithIDUsesExactCanonicalSessionIdentity(t *testing.T) {
	containerDir := t.TempDir()
	sessionID := runtimeids.NewSessionID()
	store, err := session.NewLazyWithID(
		sessionID,
		containerDir,
		"sessions",
		t.TempDir(),
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("new lazy with id: %v", err)
	}
	if store.Meta().SessionID != sessionID.String() {
		t.Fatalf("session id = %q, want %q", store.Meta().SessionID, sessionID)
	}
	wantDir := filepath.Join(containerDir, sessionID.String())
	if store.Dir() != wantDir {
		t.Fatalf("session dir = %q, want %q", store.Dir(), wantDir)
	}
}

func TestNewLazyWithIDRejectsNonCanonicalNewSessionIdentity(t *testing.T) {
	legacy, err := runtimeids.ParseSessionID("session-legacy")
	if err != nil {
		t.Fatalf("parse legacy session id: %v", err)
	}
	for name, sessionID := range map[string]runtimeids.SessionID{
		"zero":   {},
		"legacy": legacy,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := session.NewLazyWithID(
				sessionID,
				t.TempDir(),
				"sessions",
				t.TempDir(),
				sessioncontract.SessionCategoryMain,
			)
			if err == nil {
				t.Fatal("new lazy session accepted a non-canonical identity")
			}
		})
	}
}

func TestNewLazyWithIDPreservesCategoryValidation(t *testing.T) {
	_, err := session.NewLazyWithID(
		runtimeids.NewSessionID(),
		t.TempDir(),
		"sessions",
		t.TempDir(),
		sessioncontract.SessionCategory("invalid"),
	)
	if err == nil {
		t.Fatal("new lazy session accepted an invalid category")
	}
}

func TestNewLazyStillAllocatesCanonicalSessionIdentity(t *testing.T) {
	containerDir := t.TempDir()
	store, err := session.NewLazy(
		containerDir,
		"sessions",
		t.TempDir(),
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("new lazy session: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse allocated session id: %v", err)
	}
	if !sessionID.IsCanonicalUUIDv4() {
		t.Fatalf("allocated session id %q is not canonical UUIDv4", sessionID)
	}
	wantDir := filepath.Join(containerDir, sessionID.String())
	if store.Dir() != wantDir {
		t.Fatalf("session dir = %q, want %q", store.Dir(), wantDir)
	}
}

func TestExactWorkflowExecutionCannotBeLiveAsAgentAndScript(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		RuntimeFactory:  authorityTestRuntimeFactory,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	workflowRef := WorkflowExecutionRef{
		RunID:      workflow.RunID(uuid.NewString()),
		Generation: 1,
	}
	agent, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Workflow:   &workflowRef,
		Resource:   OpenAgentResource{},
		Runner: func(ctx context.Context, _ ExecutionScope, _ AgentRuntimeBridge) error {
			<-ctx.Done()
			return context.Cause(ctx)
		},
	})
	if err != nil {
		t.Fatalf("start agent execution: %v", err)
	}

	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true executable unavailable: %v", err)
	}
	script, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: &workflowRef,
		Command:  ScriptCommand{Path: truePath},
	})
	if err == nil {
		if script != nil {
			_ = script.Close(context.Background())
		}
		t.Fatal("same exact workflow execution was admitted as both agent and script")
	}

	if err := agent.Stop(context.Background()); err != nil {
		t.Fatalf("stop agent execution: %v", err)
	}
}

func TestStaleRuntimeAttachmentReleaseCannotAffectReplacement(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		RuntimeFactory:  authorityTestRuntimeFactory,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	first, err := authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "owner-a",
	})
	if err != nil {
		t.Fatalf("open first runtime: %v", err)
	}
	replacement, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Resource:   ReplaceAgentResource{},
		Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("replace runtime: %v", err)
	}
	if _, err := replacement.Wait(context.Background()); err != nil {
		t.Fatalf("wait replacement execution: %v", err)
	}
	second, err := authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "owner-b",
	})
	if err != nil {
		t.Fatalf("open replacement runtime: %v", err)
	}
	if first.Resource() == second.Resource() {
		t.Fatal("replacement reused the retired resource generation")
	}

	if _, err := first.Release(context.Background(), RuntimeReleaseDetach); err != nil {
		t.Fatalf("release stale attachment: %v", err)
	}
	current, ok := second.Snapshot()
	if !ok {
		t.Fatal("replacement attachment became stale")
	}
	if current.OwnerCount != 1 {
		t.Fatalf("replacement owner count = %d, want 1", current.OwnerCount)
	}
}

func TestResourceRetentionBlocksReplacementUntilReleased(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		RuntimeFactory:  authorityTestRuntimeFactory,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	attachment, err := authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "owner-a",
	})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	retention, err := authority.RetainResource(attachment.Resource())
	if err != nil {
		t.Fatalf("retain resource: %v", err)
	}
	if _, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Resource:   ReplaceAgentResource{},
		Runner:     func(context.Context, ExecutionScope, AgentRuntimeBridge) error { return nil },
	}); err == nil {
		t.Fatal("replacement succeeded while an exact resource retention was live")
	}
	if err := retention.Close(); err != nil {
		t.Fatalf("release resource retention: %v", err)
	}
	if err := retention.Close(); err != nil {
		t.Fatalf("release resource retention again: %v", err)
	}
	replacement, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Resource:   ReplaceAgentResource{},
		Runner:     func(context.Context, ExecutionScope, AgentRuntimeBridge) error { return nil },
	})
	if err != nil {
		t.Fatalf("replace after retention release: %v", err)
	}
	if _, err := replacement.Wait(context.Background()); err != nil {
		t.Fatalf("wait replacement: %v", err)
	}
}

func TestAgentExecutionBindsAndClearsShellCorrelation(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	manager, err := shelltool.NewManager(shelltool.WithMinimumExecToBgTime(20 * time.Millisecond))
	if err != nil {
		t.Fatalf("new shell manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	var binding *runtimewire.LocalToolRegistryBinding
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		RuntimeFactory: func(_ context.Context, store *session.Store, _ AgentResourceDescriptor) (*runtimewire.RuntimeWiring, error) {
			var buildErr error
			binding, _, _, buildErr = runtimewire.NewLocalToolRegistryBinding(runtimewire.LocalToolRegistryOptions{
				WorkspaceRoot:       fixture.config.WorkspaceRoot,
				OwnerSessionID:      sessionID.String(),
				Enabled:             []toolspec.ID{toolspec.ToolExecCommand},
				MinimumExecToBgTime: 20 * time.Millisecond,
				ShellOutputMaxChars: 16_000,
				SupportsVision:      true,
				Background:          manager,
			})
			if buildErr != nil {
				return nil, buildErr
			}
			engine, buildErr := runtime.New(store, &sessionRuntimeTestLLMClient{}, binding.Registry(), runtime.Config{Model: "gpt-5"})
			if buildErr != nil {
				return nil, buildErr
			}
			return &runtimewire.RuntimeWiring{Engine: engine, LocalTools: binding, Background: manager}, nil
		},
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	startBackground := func(callID string) (shelltool.Snapshot, error) {
		handler, ok := binding.Registry().Get(toolspec.ToolExecCommand)
		if !ok {
			return shelltool.Snapshot{}, fmt.Errorf("exec_command handler is unavailable")
		}
		before := make(map[string]struct{})
		for _, snapshot := range manager.List() {
			before[snapshot.ID] = struct{}{}
		}
		input, marshalErr := json.Marshal(map[string]any{
			"cmd":           "sleep 5",
			"shell":         "/bin/sh",
			"login":         false,
			"yield_time_ms": 20,
		})
		if marshalErr != nil {
			return shelltool.Snapshot{}, marshalErr
		}
		result, callErr := handler.Call(context.Background(), tools.Call{
			ID:    callID,
			Name:  toolspec.ToolExecCommand,
			Input: input,
		})
		if callErr != nil {
			return shelltool.Snapshot{}, callErr
		}
		if result.IsError {
			return shelltool.Snapshot{}, fmt.Errorf("exec_command failed: %s", string(result.Output))
		}
		for _, snapshot := range manager.List() {
			if _, existed := before[snapshot.ID]; !existed {
				return snapshot, nil
			}
		}
		return shelltool.Snapshot{}, fmt.Errorf("new background process is unavailable")
	}

	type backgroundStartResult struct {
		snapshot shelltool.Snapshot
		err      error
	}
	attachment, err := authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "test-owner",
	})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	started := make(chan backgroundStartResult, 1)
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Resource:   CurrentAgentResource{},
		Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
			snapshot, startErr := startBackground("scoped")
			started <- backgroundStartResult{snapshot: snapshot, err: startErr}
			return startErr
		},
	})
	if err != nil {
		t.Fatalf("start agent execution: %v", err)
	}
	startResult := <-started
	if startResult.err != nil {
		t.Fatalf("start scoped process: %v", startResult.err)
	}
	scoped := startResult.snapshot
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait agent execution: %v", err)
	}
	resource, ok := handle.Scope().Resource()
	if !ok {
		t.Fatal("agent scope has no resource")
	}
	want, err := runtimeids.NewExecutionCorrelation(handle.Scope().ID(), resource.Generation())
	if err != nil {
		t.Fatalf("new expected correlation: %v", err)
	}
	if scoped.ExecutionCorrelation == nil || *scoped.ExecutionCorrelation != want {
		t.Fatalf("scoped process correlation = %#v, want %#v", scoped.ExecutionCorrelation, want)
	}

	unscoped, err := startBackground("idle")
	if err != nil {
		t.Fatalf("start idle process: %v", err)
	}
	if unscoped.ExecutionCorrelation != nil {
		t.Fatalf("idle process correlation = %#v, want nil", *unscoped.ExecutionCorrelation)
	}
	if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
		t.Fatalf("release runtime: %v", err)
	}
}

func TestDormantSessionStoreCallbacksAreSerialized(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
	})
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new open session descriptor: %v", err)
	}

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- authority.WithSessionStore(context.Background(), descriptor, func(context.Context, *session.Store) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- authority.WithSessionStore(context.Background(), descriptor, func(context.Context, *session.Store) error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second dormant Store callback overlapped the first")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Store callback: %v", err)
	}
	select {
	case <-secondEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("second dormant Store callback did not enter after the first completed")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second Store callback: %v", err)
	}
}

func TestAuthorityMaterializesCreateSessionDescriptor(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := runtimeids.NewSessionID()
	containerDir := filepath.Dir(fixture.store.Dir())
	descriptor, err := session.NewCreateSessionDescriptor(
		sessionID,
		containerDir,
		filepath.Base(containerDir),
		fixture.config.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("new create session descriptor: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
	})

	err = authority.WithSessionStore(context.Background(), descriptor, func(_ context.Context, store *session.Store) error {
		if store.Meta().SessionID != sessionID.String() {
			t.Fatalf("materialized session id = %q, want %q", store.Meta().SessionID, sessionID)
		}
		wantDir := filepath.Join(containerDir, sessionID.String())
		if store.Dir() != wantDir {
			t.Fatalf("materialized session dir = %q, want %q", store.Dir(), wantDir)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("with materialized session store: %v", err)
	}
	reopened, err := session.OpenByID(
		fixture.config.PersistenceRoot,
		sessionID.String(),
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("reopen materialized session: %v", err)
	}
	if reopened.Meta().SessionID != sessionID.String() {
		t.Fatalf("reopened session id = %q, want %q", reopened.Meta().SessionID, sessionID)
	}
}

func TestPromptResponseResolvesCurrentExactExecutionScope(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID, err := runtimeids.ParseSessionID(fixture.store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	var broker *tools.AskQuestionBroker
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot: fixture.config.PersistenceRoot,
		StoreOptions:    fixture.metadata.AuthoritativeSessionStoreOptions(),
		RuntimeFactory: func(_ context.Context, store *session.Store, _ AgentResourceDescriptor) (*runtimewire.RuntimeWiring, error) {
			broker = tools.NewAskQuestionBroker()
			engine, buildErr := runtime.New(store, &sessionRuntimeTestLLMClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
			if buildErr != nil {
				return nil, buildErr
			}
			return &runtimewire.RuntimeWiring{Engine: engine, AskBroker: broker}, nil
		},
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	askID := uuid.NewString()
	request := tools.AskQuestionRequest{
		ID:       askID,
		StepID:   uuid.NewString(),
		Question: "Proceed?",
	}
	type promptResult struct {
		response tools.AskQuestionResponse
		err      error
	}
	responseDone := make(chan promptResult, 1)
	handle, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Resource:   OpenAgentResource{},
		Runner: func(ctx context.Context, _ ExecutionScope, _ AgentRuntimeBridge) error {
			response, askErr := broker.Ask(ctx, request)
			responseDone <- promptResult{response: response, err: askErr}
			return askErr
		},
	})
	if err != nil {
		t.Fatalf("start agent execution: %v", err)
	}

	var pending []ExecutionPromptSnapshot
	deadline := time.Now().Add(3 * time.Second)
	for len(pending) == 0 && time.Now().Before(deadline) {
		pending = authority.PendingPrompts(sessionID)
		if len(pending) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if len(pending) != 1 {
		t.Fatalf("pending prompts = %+v, want one", pending)
	}
	if pending[0].Scope.ID() != handle.Scope().ID() || pending[0].Request.ID != askID {
		t.Fatalf("pending prompt = %+v, want exact scope %s ask %s", pending[0], handle.Scope().ID(), askID)
	}

	want := tools.AskQuestionResponse{RequestID: askID, Answer: "yes"}
	if err := authority.SubmitPromptResponse(sessionID, want, nil); err != nil {
		t.Fatalf("submit prompt response: %v", err)
	}
	result := <-responseDone
	if result.err != nil {
		t.Fatalf("await prompt response: %v", result.err)
	}
	if result.response != want {
		t.Fatalf("prompt response = %+v, want %+v", result.response, want)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait agent execution: %v", err)
	}
	if pending := authority.PendingPrompts(sessionID); len(pending) != 0 {
		t.Fatalf("pending prompts after execution = %+v, want none", pending)
	}
}

func authorityTestRuntimeFactory(_ context.Context, store *session.Store, _ AgentResourceDescriptor) (*runtimewire.RuntimeWiring, error) {
	engine, err := runtime.New(store, &sessionRuntimeTestLLMClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		return nil, err
	}
	return &runtimewire.RuntimeWiring{Engine: engine}, nil
}

func mustOpenSessionDescriptor(t *testing.T, sessionID runtimeids.SessionID) session.SessionDescriptor {
	t.Helper()
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new open session descriptor: %v", err)
	}
	return descriptor
}
