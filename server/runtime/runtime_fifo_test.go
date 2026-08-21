package runtime

import (
	"context"
	"testing"
	"time"

	"core/server/tools"
	"core/shared/toolspec"
)

func TestRuntimeOperationFIFOCompletesTypedOperationsInAcceptanceOrder(t *testing.T) {
	fifo := newRuntimeOperationFIFO()
	t.Cleanup(fifo.Close)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	first := submitRuntimeOperation(fifo, func(context.Context) (int, error) {
		close(firstStarted)
		<-releaseFirst
		return 41, nil
	})
	select {
	case <-firstStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for first Runtime operation")
	}

	secondStarted := make(chan struct{})
	second := submitRuntimeOperation(fifo, func(context.Context) (string, error) {
		close(secondStarted)
		return "second", nil
	})
	select {
	case <-secondStarted:
		t.Fatal("second Runtime operation started before the first completed")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseFirst)
	firstResult, err := first.Await(t.Context())
	if err != nil || firstResult != 41 {
		t.Fatalf("first Runtime operation = %d, %v; want 41, nil", firstResult, err)
	}
	secondResult, err := second.Await(t.Context())
	if err != nil || secondResult != "second" {
		t.Fatalf("second Runtime operation = %q, %v; want second, nil", secondResult, err)
	}
}

func TestRuntimeOperationFIFODefersAcceptedOperationsUntilTheProtectedStepBoundary(t *testing.T) {
	fifo := newRuntimeOperationFIFO()
	t.Cleanup(fifo.Close)
	if err := fifo.Pause(t.Context()); err != nil {
		t.Fatalf("pause Runtime operations: %v", err)
	}

	started := make(chan struct{})
	deferred := submitRuntimeOperation(fifo, func(context.Context) (string, error) {
		close(started)
		return "applied", nil
	})
	select {
	case <-started:
		t.Fatal("Runtime operation applied during a protected Agent Step")
	case <-time.After(25 * time.Millisecond):
	}

	if err := fifo.Drain(t.Context()); err != nil {
		t.Fatalf("drain Runtime operations at Step Boundary: %v", err)
	}
	result, err := deferred.Await(t.Context())
	if err != nil || result != "applied" {
		t.Fatalf("deferred Runtime operation = %q, %v; want applied, nil", result, err)
	}
}

func TestEngineDefersOrdinaryRuntimeMutationUntilProtectedStepFinishes(t *testing.T) {
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	stepStarted := make(chan struct{})
	releaseStep := make(chan struct{})
	stepDone := make(chan error, 1)
	go func() {
		stepDone <- engine.RunWhenIdle(t.Context(), ActiveKindUserTurn, func() error {
			close(stepStarted)
			<-releaseStep
			return nil
		})
	}()
	select {
	case <-stepStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for protected Agent Step")
	}

	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- engine.AppendCommittedEntry("system", "after boundary")
	}()
	select {
	case err := <-mutationDone:
		t.Fatalf("ordinary Runtime mutation completed during protected Step: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseStep)
	if err := <-stepDone; err != nil {
		t.Fatalf("finish protected Agent Step: %v", err)
	}
	if err := <-mutationDone; err != nil {
		t.Fatalf("apply Runtime mutation at boundary: %v", err)
	}
	entries := engine.ChatSnapshot().Entries
	if len(entries) != 1 || entries[0].Text != "after boundary" {
		t.Fatalf("post-boundary transcript = %+v", entries)
	}
}

func TestForegroundShellReleasesRuntimeFIFOAfterScheduling(t *testing.T) {
	handler := &heldRuntimeShell{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: handler,
		}),
		Config{Model: "gpt-5"},
	)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	first := submitEngineRuntimeOperation(engine, func(context.Context) (struct{}, error) {
		close(firstStarted)
		<-releaseFirst
		return struct{}{}, nil
	})
	select {
	case <-firstStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for preceding Runtime mutation")
	}

	shellDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserShellCommand(t.Context(), "pwd")
		shellDone <- err
	}()
	select {
	case <-handler.started:
		t.Fatal("foreground shell started before the preceding Runtime mutation")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	if _, err := first.Await(t.Context()); err != nil {
		t.Fatalf("complete preceding Runtime mutation: %v", err)
	}
	select {
	case <-handler.started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for foreground shell")
	}

	settingDone := make(chan error, 1)
	go func() {
		settingDone <- engine.SetThinkingLevel("low")
	}()
	select {
	case err := <-settingDone:
		if err != nil {
			t.Fatalf("apply setting while shell is held: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("foreground shell retained the Runtime FIFO head")
	}

	close(handler.release)
	if err := <-shellDone; err != nil {
		t.Fatalf("foreground shell: %v", err)
	}
}

func TestWorktreeTransitionReleasesRuntimeFIFOAfterScheduling(t *testing.T) {
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	first := submitEngineRuntimeOperation(engine, func(context.Context) (struct{}, error) {
		close(firstStarted)
		<-releaseFirst
		return struct{}{}, nil
	})
	select {
	case <-firstStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for preceding Runtime mutation")
	}

	transitionStarted := make(chan struct{})
	releaseTransition := make(chan struct{})
	transitionDone := make(chan error, 1)
	go func() {
		transitionDone <- engine.RunWorktreeTransition(t.Context(), func() error {
			close(transitionStarted)
			<-releaseTransition
			return nil
		})
	}()
	select {
	case <-transitionStarted:
		t.Fatal("Worktree transition started before the preceding Runtime mutation")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	if _, err := first.Await(t.Context()); err != nil {
		t.Fatalf("complete preceding Runtime mutation: %v", err)
	}
	select {
	case <-transitionStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for Worktree transition")
	}

	if err := engine.SetThinkingLevel("low"); err != nil {
		t.Fatalf("apply setting while Worktree transition is held: %v", err)
	}
	close(releaseTransition)
	if err := <-transitionDone; err != nil {
		t.Fatalf("Worktree transition: %v", err)
	}
}

type heldRuntimeShell struct {
	started chan struct{}
	release chan struct{}
}

func (h *heldRuntimeShell) Call(ctx context.Context, call tools.Call) (tools.Result, error) {
	close(h.started)
	select {
	case <-h.release:
		return tools.Result{
			CallID: call.ID,
			Name:   toolspec.ToolExecCommand,
			Output: []byte(`{"output":"done"}`),
		}, nil
	case <-ctx.Done():
		return tools.Result{}, context.Cause(ctx)
	}
}
