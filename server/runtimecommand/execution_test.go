package runtimecommand

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtime"
	"core/server/runtimeops"
	"core/server/session"
	"core/server/sessionruntime"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type operationDriverStepBarrier struct {
	blockAt int
	entered chan runtime.StepLifecycleSnapshot
	release chan struct{}

	mu    sync.Mutex
	count int
}

type manualCompactionEligibilityClient struct {
	mu    sync.Mutex
	calls int
}

func (c *manualCompactionEligibilityClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 {
		return llm.Response{
			Assistant: llm.Message{
				Role:  llm.RoleAssistant,
				Phase: textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: []llm.ToolCall{{
				ID:    "prime-compaction-eligibility",
				Name:  string(toolspec.ToolExecCommand),
				Input: []byte(`{"cmd":"pwd"}`),
			}},
		}, nil
	}
	return llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("done"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
	}, nil
}

func (c *manualCompactionEligibilityClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

func newOperationDriverStepBarrier(blockAt int) *operationDriverStepBarrier {
	return &operationDriverStepBarrier{
		blockAt: blockAt,
		entered: make(chan runtime.StepLifecycleSnapshot, 1),
		release: make(chan struct{}),
	}
}

func (b *operationDriverStepBarrier) StepBegan(
	_ context.Context,
	_ sessionruntime.AgentResourceDescriptor,
	snapshot runtime.StepLifecycleSnapshot,
) error {
	b.mu.Lock()
	b.count++
	block := b.count == b.blockAt
	b.mu.Unlock()
	if !block {
		return nil
	}
	b.entered <- snapshot
	<-b.release
	return nil
}

func (*operationDriverStepBarrier) StepEnded(
	context.Context,
	sessionruntime.AgentResourceDescriptor,
	runtime.StepLifecycleSnapshot,
) error {
	return nil
}

func TestSessionAgentOperationOwnerOrderingCompletesExactlyOnce(t *testing.T) {
	notifier, notification := NewSessionAgentOperationOwnerOrdering()

	select {
	case <-notification.Done():
		t.Fatal("owner ordering completed during construction")
	default:
	}

	if !notifier.Complete() {
		t.Fatal("first owner-ordering completion was not accepted")
	}
	if notifier.Complete() {
		t.Fatal("second owner-ordering completion was accepted")
	}

	select {
	case <-notification.Done():
	default:
		t.Fatal("owner ordering did not complete at its explicit boundary")
	}
}

func TestGoalDriverOrdersAtAcceptedWithErrorBoundary(t *testing.T) {
	store, authority, _, observer := newGoalAuthorityFixture(t, nil)
	sessionID := mustGoalAuthoritySessionID(t, store)
	plan := ordinaryGoalAuthorityPlan(t, store.Meta().WorkspaceRoot)
	attachment, err := authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "goal-driver-ordering-test",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	t.Cleanup(func() {
		if _, releaseErr := attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose); releaseErr != nil {
			t.Errorf("release runtime: %v", releaseErr)
		}
	})

	committedErr := errors.New("committed goal observer failure")
	observer.setError(committedErr)
	driver, err := NewGoalMutationDriver(GoalSetCommand{
		SessionID: sessionID,
		Objective: "order accepted goal",
		Actor:     session.GoalActorUser,
	})
	if err != nil {
		t.Fatalf("NewGoalMutationDriver: %v", err)
	}
	notifier, notification := NewSessionAgentOperationOwnerOrdering()

	var outcome SessionAgentOperationOutcome
	err = authority.WithCurrentRuntime(context.Background(), sessionID, func(ctx context.Context, engine *runtime.Engine) error {
		var runErr error
		outcome, runErr = driver.StartOwner(ctx, engine, notifier)
		return runErr
	})
	if !errors.Is(err, committedErr) {
		t.Fatalf("goal driver error = %v, want committed observer error", err)
	}
	goalOutcome, ok := outcome.(GoalMutationOperationOutcome)
	if !ok || !goalOutcome.Result.Accepted() {
		t.Fatalf("goal outcome = %#v, want accepted-with-error", outcome)
	}
	select {
	case <-notification.Done():
	default:
		t.Fatal("accepted-with-error Goal did not complete owner ordering")
	}
	if notifier.Complete() {
		t.Fatal("accepted-with-error Goal completed owner ordering more than once")
	}
}

func TestDriverReturnBeforeBoundaryDoesNotCompleteOwnerOrdering(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("018fdd67-89ab-4cde-8123-456789abcdef")
	if err != nil {
		t.Fatalf("parse session ID: %v", err)
	}
	driver, err := NewUserShellDriver(UserShellDriverOptions{
		SessionID: sessionID,
		Command:   "pwd",
		OperationRef: clientui.RuntimeOperationRef{
			Kind:            clientui.RuntimeOperationKindUserShell,
			ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
		},
		Operations: runtimeops.NewCoordinator(),
	})
	if err != nil {
		t.Fatalf("NewUserShellDriver: %v", err)
	}
	notifier, notification := NewSessionAgentOperationOwnerOrdering()

	if _, err := driver.StartOwner(context.Background(), nil, notifier); err == nil {
		t.Fatal("driver accepted a missing Engine")
	}
	select {
	case <-notification.Done():
		t.Fatal("driver return before its active hook completed owner ordering")
	default:
	}
}

func TestUserTurnDriverOrdersOnlyAfterParentMessageAcceptance(t *testing.T) {
	barrier := newOperationDriverStepBarrier(1)
	store, authority, _, _ := newGoalAuthorityFixture(t, nil, barrier)
	sessionID, engine := openOperationDriverRuntime(t, store, authority)
	operations := runtimeops.NewCoordinator()
	parentRef := operationDriverRef(clientui.RuntimeOperationKindSubmit)
	driver, err := NewUserTurnDriver(UserTurnDriverOptions{
		SessionID:                       sessionID,
		ExecutionText:                   "parent message",
		HistoryText:                     "parent message",
		ClientRequestID:                 parentRef.ClientRequestID,
		OperationRef:                    parentRef,
		PreSubmitCompactionOperationRef: operationDriverRef(clientui.RuntimeOperationKindPreSubmitCompact),
		Operations:                      operations,
	})
	if err != nil {
		t.Fatalf("NewUserTurnDriver: %v", err)
	}
	notifier, notification := NewSessionAgentOperationOwnerOrdering()
	done := make(chan error, 1)
	go func() {
		_, runErr := driver.StartOwner(context.Background(), engine, notifier)
		done <- runErr
	}()

	waitForOperationDriverBoundary(t, barrier, runtime.ActiveKindUserTurn, notification)
	close(barrier.release)
	<-done
	assertOperationDriverOrderedOnce(t, notifier, notification)
}

func TestUserTurnNestedPreSubmitDoesNotCompleteParentOrdering(t *testing.T) {
	barrier := newOperationDriverStepBarrier(1)
	store, authority, _, _ := newGoalAuthorityFixture(t, nil, barrier)
	sessionID, engine := openOperationDriverRuntime(t, store, authority)
	parentRef := operationDriverRef(clientui.RuntimeOperationKindSubmit)
	driver, err := NewUserTurnDriver(UserTurnDriverOptions{
		SessionID:                       sessionID,
		ExecutionText:                   strings.Repeat("compact me ", 200_000),
		HistoryText:                     "compact me",
		ClientRequestID:                 parentRef.ClientRequestID,
		OperationRef:                    parentRef,
		PreSubmitCompactionOperationRef: operationDriverRef(clientui.RuntimeOperationKindPreSubmitCompact),
		Operations:                      runtimeops.NewCoordinator(),
	})
	if err != nil {
		t.Fatalf("NewUserTurnDriver: %v", err)
	}
	notifier, notification := NewSessionAgentOperationOwnerOrdering()
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, runErr := driver.StartOwner(runCtx, engine, notifier)
		done <- runErr
	}()

	waitForOperationDriverBoundary(t, barrier, runtime.ActiveKindPreSubmitCompaction, notification)
	cancel()
	close(barrier.release)
	<-done
	select {
	case <-notification.Done():
		t.Fatal("nested pre-submit child completed parent owner ordering")
	default:
	}
}

func TestUserShellDriverOrdersOnlyAtShellActiveHook(t *testing.T) {
	barrier := newOperationDriverStepBarrier(1)
	store, authority, _, _ := newGoalAuthorityFixture(t, nil, barrier)
	sessionID, engine := openOperationDriverRuntime(t, store, authority)
	operations := runtimeops.NewCoordinator()
	driver, err := NewUserShellDriver(UserShellDriverOptions{
		SessionID:    sessionID,
		Command:      "pwd",
		OperationRef: operationDriverRef(clientui.RuntimeOperationKindUserShell),
		Operations:   operations,
	})
	if err != nil {
		t.Fatalf("NewUserShellDriver: %v", err)
	}
	notifier, notification := NewSessionAgentOperationOwnerOrdering()
	done := make(chan error, 1)
	go func() {
		_, runErr := driver.StartOwner(context.Background(), engine, notifier)
		done <- runErr
	}()

	waitForOperationDriverBoundary(t, barrier, runtime.ActiveKindUserShell, notification)
	close(barrier.release)
	<-done
	assertOperationDriverOrderedOnce(t, notifier, notification)
}

func TestManualCompactionDriverOrdersOnlyAtCompactActiveHook(t *testing.T) {
	barrier := newOperationDriverStepBarrier(2)
	store, authority, _, _ := newGoalAuthorityFixture(t, nil, barrier)
	sessionID, engine := openOperationDriverRuntime(t, store, authority, &manualCompactionEligibilityClient{})
	if _, err := engine.SubmitUserMessage(context.Background(), "prime compactable context"); err != nil {
		t.Fatalf("prime manual compaction eligibility: %v", err)
	}
	operations := runtimeops.NewCoordinator()
	driver, err := NewManualCompactionDriver(ManualCompactionDriverOptions{
		SessionID:    sessionID,
		Arguments:    "summarize",
		OperationRef: operationDriverRef(clientui.RuntimeOperationKindCompact),
		Operations:   operations,
	})
	if err != nil {
		t.Fatalf("NewManualCompactionDriver: %v", err)
	}
	notifier, notification := NewSessionAgentOperationOwnerOrdering()
	done := make(chan error, 1)
	go func() {
		_, runErr := driver.StartOwner(context.Background(), engine, notifier)
		done <- runErr
	}()

	waitForOperationDriverBoundary(t, barrier, runtime.ActiveKindCompaction, notification)
	close(barrier.release)
	<-done
	assertOperationDriverOrderedOnce(t, notifier, notification)
}

func openOperationDriverRuntime(
	t *testing.T,
	store *session.Store,
	authority *sessionruntime.Authority,
	clients ...llm.Client,
) (runtimeids.SessionID, *runtime.Engine) {
	t.Helper()
	sessionID := mustGoalAuthoritySessionID(t, store)
	plan := ordinaryGoalAuthorityPlan(t, store.Meta().WorkspaceRoot, clients...)
	attachment, err := authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "operation-driver-ordering-test",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	t.Cleanup(func() {
		if _, releaseErr := attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose); releaseErr != nil {
			t.Errorf("release runtime: %v", releaseErr)
		}
	})
	var engine *runtime.Engine
	if err := authority.WithCurrentRuntime(context.Background(), sessionID, func(_ context.Context, current *runtime.Engine) error {
		engine = current
		return nil
	}); err != nil {
		t.Fatalf("resolve runtime Engine: %v", err)
	}
	return sessionID, engine
}

func operationDriverRef(kind clientui.RuntimeOperationKind) clientui.RuntimeOperationRef {
	return clientui.RuntimeOperationRef{
		Kind:            kind,
		ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
	}
}

func waitForOperationDriverBoundary(
	t *testing.T,
	barrier *operationDriverStepBarrier,
	want runtime.ActiveKind,
	notification SessionAgentOperationOwnerOrderingNotification,
) {
	t.Helper()
	select {
	case snapshot := <-barrier.entered:
		if snapshot.ActiveKind != want {
			t.Fatalf("active kind = %q, want %q", snapshot.ActiveKind, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s boundary", want)
	}
	select {
	case <-notification.Done():
		t.Fatalf("%s ordering completed before its active hook", want)
	default:
	}
}

func assertOperationDriverOrderedOnce(
	t *testing.T,
	notifier SessionAgentOperationOwnerOrderingNotifier,
	notification SessionAgentOperationOwnerOrderingNotification,
) {
	t.Helper()
	select {
	case <-notification.Done():
	default:
		t.Fatal("operation owner ordering did not complete at its boundary")
	}
	if notifier.Complete() {
		t.Fatal("operation owner ordering completed more than once")
	}
}
