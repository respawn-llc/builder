package sessionruntime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"core/server/runtime"
	"core/server/runtimecommand"
	"core/server/session"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestRuntimeEventLifecycle(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "idle resource owns one generation-bound queue", run: testIdleRuntimeEventOwnership},
		{name: "accepted event outlives submitting context", run: testAcceptedRuntimeEventLifetime},
		{name: "resource close settles running and waiting events", run: testRuntimeEventCloseSettlement},
		{name: "resource close joins queue-owned work", run: testRuntimeEventWorkJoin},
		{name: "replacement receives an empty independent queue", run: testRuntimeEventReplacement},
		{name: "worktree boundary ownership is exact to one generation", run: testWorktreeBoundaryOwnership},
		{name: "runtime close settles Worktree boundary ownership", run: testWorktreeBoundaryCloseSettlement},
		{name: "independent Sessions own independent Worktree boundaries", run: testIndependentWorktreeBoundaries},
		{name: "runtime close joins outside Session admission", run: testRuntimeCloseOutsideSessionAdmission},
		{name: "runtime replacement joins outside Session admission", run: testRuntimeReplacementOutsideSessionAdmission},
		{name: "runtime close preserves accepted terminal outcomes before canonical settlement", run: testRuntimeClosePreservesAcceptedOutcomes},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func testIndependentWorktreeBoundaries(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	firstSessionID := lifecycleSessionID(t, fixture)
	secondStore, err := session.Create(
		filepath.Dir(fixture.store.Dir()),
		filepath.Base(filepath.Dir(fixture.store.Dir())),
		fixture.config.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("create second Session: %v", err)
	}
	secondSessionID, err := runtimeids.ParseSessionID(secondStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse second Session ID: %v", err)
	}
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	first := openLifecycleRuntime(t, fixture.authority, firstSessionID, "worktree-boundary-a", &plan)
	second := openLifecycleRuntime(t, fixture.authority, secondSessionID, "worktree-boundary-b", &plan)
	t.Cleanup(func() {
		if _, err := first.Release(context.Background(), RuntimeReleaseClose); err != nil {
			t.Errorf("close first runtime: %v", err)
		}
		if _, err := second.Release(context.Background(), RuntimeReleaseClose); err != nil {
			t.Errorf("close second runtime: %v", err)
		}
	})

	firstClaim, err := fixture.authority.ClaimWorktreeBoundary(first.Resource(), serverapi.NewWorktreeOperationID())
	if err != nil {
		t.Fatalf("claim first Worktree boundary: %v", err)
	}
	secondClaim, err := fixture.authority.ClaimWorktreeBoundary(second.Resource(), serverapi.NewWorktreeOperationID())
	if err != nil {
		t.Fatalf("claim second Worktree boundary: %v", err)
	}
	if _, err := fixture.authority.GrantWorktreeBoundary(first.Resource()); err != nil {
		t.Fatalf("grant first Worktree boundary: %v", err)
	}
	if err := firstClaim.AwaitGrant(context.Background()); err != nil {
		t.Fatalf("await first Worktree boundary: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := secondClaim.AwaitGrant(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Worktree boundary before its grant = %v, want deadline", err)
	}
	if err := firstClaim.Release(); err != nil {
		t.Fatalf("release first Worktree boundary: %v", err)
	}
	if _, err := fixture.authority.GrantWorktreeBoundary(second.Resource()); err != nil {
		t.Fatalf("grant second Worktree boundary: %v", err)
	}
	if err := secondClaim.AwaitGrant(context.Background()); err != nil {
		t.Fatalf("await second Worktree boundary: %v", err)
	}
	if err := secondClaim.Release(); err != nil {
		t.Fatalf("release second Worktree boundary: %v", err)
	}
}

var errRuntimeCloseHeldSessionAdmission = errors.New("runtime close held Session admission")

type runtimeCloseReentrantLifecycle struct {
	authority *Authority
	sessionID runtimeids.SessionID
	plan      *AgentRuntimePlan
}

func (l *runtimeCloseReentrantLifecycle) ResourceReady(
	context.Context,
	AgentResourceDescriptor,
	*runtime.Engine,
	AgentResourceRetainer,
) error {
	return nil
}

func (l *runtimeCloseReentrantLifecycle) ResourceDraining(
	_ context.Context,
	_ AgentResourceDescriptor,
) error {
	result := make(chan error, 1)
	go func() {
		_, err := l.authority.OpenRuntime(context.Background(), RuntimeOpenRequest{
			SessionID: l.sessionID,
			OwnerID:   "reentrant-close-observer",
			Runtime:   l.plan,
		})
		result <- err
	}()
	select {
	case <-result:
		return nil
	case <-time.After(100 * time.Millisecond):
		return errRuntimeCloseHeldSessionAdmission
	}
}

func testRuntimeCloseOutsideSessionAdmission(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	lifecycle := &runtimeCloseReentrantLifecycle{sessionID: sessionID, plan: &plan}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot:   fixture.config.PersistenceRoot,
		StoreOptions:      fixture.metadata.AuthoritativeSessionStoreOptions(),
		ResourceLifecycle: lifecycle,
	})
	lifecycle.authority = authority
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	attachment := openLifecycleRuntime(t, authority, sessionID, "close-lock", &plan)

	if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
		if errors.Is(err, errRuntimeCloseHeldSessionAdmission) {
			t.Fatal("runtime closure joined while holding the Session admission gate")
		}
		t.Fatalf("close runtime: %v", err)
	}
}

func testRuntimeReplacementOutsideSessionAdmission(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	lifecycle := &runtimeCloseReentrantLifecycle{sessionID: sessionID, plan: &plan}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot:   fixture.config.PersistenceRoot,
		StoreOptions:      fixture.metadata.AuthoritativeSessionStoreOptions(),
		ResourceLifecycle: lifecycle,
	})
	lifecycle.authority = authority
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	openLifecycleRuntime(t, authority, sessionID, "replace-lock", &plan)

	replacement, err := authority.StartAgentExecution(context.Background(), AgentExecutionRequest{
		Descriptor: mustOpenSessionDescriptor(t, sessionID),
		Runtime:    &plan,
		Resource:   ReplaceAgentResource{},
		Runner:     func(context.Context, ExecutionScope, AgentRuntimeBridge) error { return nil },
	})
	if errors.Is(err, errRuntimeCloseHeldSessionAdmission) {
		t.Fatal("runtime replacement joined while holding the Session admission gate")
	}
	if err != nil {
		t.Fatalf("replace runtime: %v", err)
	}
	if _, err := replacement.Wait(context.Background()); err != nil {
		t.Fatalf("wait replacement execution: %v", err)
	}
}

func testRuntimeClosePreservesAcceptedOutcomes(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	lifecycle := &authorityLifecycleProbe{draining: make(chan struct{})}
	authority := NewAuthority(AuthorityOptions{
		PersistenceRoot:   fixture.config.PersistenceRoot,
		StoreOptions:      fixture.metadata.AuthoritativeSessionStoreOptions(),
		ResourceLifecycle: lifecycle,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	attachment := openLifecycleRuntime(t, authority, sessionID, "terminal-outcomes", &plan)
	var target RuntimeEventTarget
	if err := authority.WithRuntimeEvents(context.Background(), attachment.Resource(), func(_ context.Context, current RuntimeEventTarget) error {
		target = current
		return nil
	}); err != nil {
		t.Fatalf("read runtime target: %v", err)
	}

	blockerStarted := make(chan struct{})
	releaseBlocker := make(chan struct{})
	if _, err := runtimecommand.Submit(context.Background(), target.Events, "blocker", func(
		_ runtimecommand.Admission,
		value string,
		complete func(string, error),
	) error {
		close(blockerStarted)
		<-releaseBlocker
		complete(value, nil)
		return nil
	}); err != nil {
		t.Fatalf("submit blocker: %v", err)
	}
	awaitRuntimeEventSignal(t, blockerStarted)

	committedCompletion, err := runtimecommand.Submit(
		context.Background(),
		target.Events,
		"committed-completion",
		runtimeEventStringEcho,
	)
	if err != nil {
		t.Fatalf("submit committed completion outcome: %v", err)
	}
	acceptedStopDisposition, err := runtimecommand.Submit(
		context.Background(),
		target.Events,
		"accepted-stop-disposition",
		runtimeEventStringEcho,
	)
	if err != nil {
		t.Fatalf("submit accepted Stop disposition outcome: %v", err)
	}
	claim, err := authority.ClaimWorktreeBoundary(
		attachment.Resource(),
		serverapi.NewWorktreeOperationID(),
	)
	if err != nil {
		t.Fatalf("claim Worktree boundary: %v", err)
	}

	closed := make(chan error, 1)
	go func() {
		_, err := attachment.Release(context.Background(), RuntimeReleaseClose)
		closed <- err
	}()
	awaitRuntimeEventSignal(t, lifecycle.draining)

	claimSettled := make(chan error, 1)
	go func() {
		claimSettled <- claim.AwaitGrant(context.Background())
	}()
	select {
	case err := <-claimSettled:
		t.Fatalf("canonical runtime state settled before accepted outcomes: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	close(releaseBlocker)
	if got := awaitRuntimeEventResult(t, committedCompletion); got != "committed-completion" {
		t.Fatalf("committed completion outcome = %q", got)
	}
	if got := awaitRuntimeEventResult(t, acceptedStopDisposition); got != "accepted-stop-disposition" {
		t.Fatalf("accepted Stop disposition outcome = %q", got)
	}
	if err := awaitRuntimeEventChannel(t, claimSettled); !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("canonical Worktree settlement = %v, want runtime unavailable", err)
	}
	if err := awaitRuntimeEventChannel(t, closed); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
}

func runtimeEventStringEcho(
	_ runtimecommand.Admission,
	value string,
	complete func(string, error),
) error {
	complete(value, nil)
	return nil
}

var _ AgentResourceLifecycle = (*runtimeCloseReentrantLifecycle)(nil)

func testWorktreeBoundaryCloseSettlement(t *testing.T) {
	for _, test := range []struct {
		name  string
		grant bool
	}{
		{name: "pending"},
		{name: "active", grant: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionRuntimeFixture(t)
			sessionID := lifecycleSessionID(t, fixture)
			plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
			first := openLifecycleRuntime(t, fixture.authority, sessionID, "worktree-boundary-a", &plan)
			claim, err := fixture.authority.ClaimWorktreeBoundary(
				first.Resource(),
				serverapi.NewWorktreeOperationID(),
			)
			if err != nil {
				t.Fatalf("claim Worktree boundary: %v", err)
			}
			if test.grant {
				if _, err := fixture.authority.GrantWorktreeBoundary(first.Resource()); err != nil {
					t.Fatalf("grant Worktree boundary: %v", err)
				}
			}
			if _, err := first.Release(context.Background(), RuntimeReleaseClose); err != nil {
				t.Fatalf("close runtime: %v", err)
			}

			if test.grant {
				if err := claim.Release(); !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
					t.Fatalf("release closed active claim = %v, want runtime unavailable", err)
				}
			} else if err := claim.AwaitGrant(context.Background()); !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
				t.Fatalf("closed pending claim = %v, want runtime unavailable", err)
			}

			replacement := openLifecycleRuntime(t, fixture.authority, sessionID, "worktree-boundary-b", &plan)
			t.Cleanup(func() {
				if _, err := replacement.Release(context.Background(), RuntimeReleaseClose); err != nil {
					t.Errorf("close replacement runtime: %v", err)
				}
			})
			replacementClaim, err := fixture.authority.ClaimWorktreeBoundary(
				replacement.Resource(),
				serverapi.NewWorktreeOperationID(),
			)
			if err != nil {
				t.Fatalf("claim replacement Worktree boundary: %v", err)
			}
			if _, err := fixture.authority.GrantWorktreeBoundary(replacement.Resource()); err != nil {
				t.Fatalf("grant replacement Worktree boundary: %v", err)
			}
			if err := replacementClaim.Release(); err != nil {
				t.Fatalf("release replacement Worktree boundary: %v", err)
			}
		})
	}
}

func testWorktreeBoundaryOwnership(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "worktree-boundary", &plan)
	t.Cleanup(func() {
		if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
			t.Errorf("close runtime: %v", err)
		}
	})

	operationID := serverapi.NewWorktreeOperationID()
	claim, err := fixture.authority.ClaimWorktreeBoundary(attachment.Resource(), operationID)
	if err != nil {
		t.Fatalf("claim Worktree boundary: %v", err)
	}
	if duplicate, err := fixture.authority.ClaimWorktreeBoundary(
		attachment.Resource(),
		serverapi.NewWorktreeOperationID(),
	); !errors.Is(err, ErrWorktreeBoundaryClaimed) || duplicate != nil {
		t.Fatalf("second Worktree boundary claim = (%v, %v), want claimed error", duplicate, err)
	}

	grant, err := fixture.authority.GrantWorktreeBoundary(attachment.Resource())
	if err != nil {
		t.Fatalf("grant Worktree boundary: %v", err)
	}
	if grant == nil || grant.OperationID != operationID {
		t.Fatalf("Worktree boundary grant = %+v, want operation %s", grant, operationID)
	}
	if err := claim.AwaitGrant(context.Background()); err != nil {
		t.Fatalf("await Worktree boundary grant: %v", err)
	}
	if err := claim.Release(); err != nil {
		t.Fatalf("release Worktree boundary: %v", err)
	}
	if grant, err := fixture.authority.GrantWorktreeBoundary(attachment.Resource()); err != nil || grant != nil {
		t.Fatalf("grant released Worktree boundary = (%+v, %v), want no claim", grant, err)
	}
}

func testIdleRuntimeEventOwnership(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "runtime-events", &plan)
	t.Cleanup(func() {
		if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
			t.Errorf("close runtime: %v", err)
		}
	})

	var first RuntimeEventTarget
	if err := fixture.authority.WithRuntimeEvents(context.Background(), attachment.Resource(), func(_ context.Context, target RuntimeEventTarget) error {
		first = target
		return nil
	}); err != nil {
		t.Fatalf("read first runtime event target: %v", err)
	}
	if first.Resource != attachment.Resource() || first.Engine == nil || first.Events == nil {
		t.Fatalf("runtime event target = %+v, want exact resource, engine, and queue", first)
	}
	if _, active := fixture.authority.SessionExecution(sessionID); active {
		t.Fatal("idle runtime event target created an Exact Execution Scope")
	}

	if err := fixture.authority.WithRuntimeEvents(context.Background(), attachment.Resource(), func(_ context.Context, target RuntimeEventTarget) error {
		if target.Events != first.Events {
			t.Fatal("same Resource Generation exposed more than one Runtime Event queue")
		}
		return nil
	}); err != nil {
		t.Fatalf("read runtime event target again: %v", err)
	}
}

func testAcceptedRuntimeEventLifetime(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "runtime-events", &plan)
	t.Cleanup(func() {
		if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
			t.Errorf("close runtime: %v", err)
		}
	})

	submissionCtx, cancelSubmission := context.WithCancel(context.Background())
	release := make(chan struct{})
	var accepted *runtimecommand.Deferred[string]
	if err := fixture.authority.WithRuntimeEvents(submissionCtx, attachment.Resource(), func(callbackCtx context.Context, target RuntimeEventTarget) error {
		var err error
		accepted, err = runtimecommand.Submit(callbackCtx, target.Events, "accepted", func(
			admission runtimecommand.Admission,
			value string,
			complete func(string, error),
		) error {
			select {
			case <-release:
				complete(value, nil)
			case <-admission.Context().Done():
			}
			return nil
		})
		return err
	}); err != nil {
		t.Fatalf("submit runtime event: %v", err)
	}
	cancelSubmission()
	close(release)
	if got := awaitRuntimeEventResult(t, accepted); got != "accepted" {
		t.Fatalf("accepted result = %q, want accepted", got)
	}
}

func testRuntimeEventCloseSettlement(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "runtime-events", &plan)

	started := make(chan struct{})
	var running *runtimecommand.Deferred[int]
	var waiting *runtimecommand.Deferred[int]
	if err := fixture.authority.WithRuntimeEvents(context.Background(), attachment.Resource(), func(callbackCtx context.Context, target RuntimeEventTarget) error {
		var err error
		running, err = runtimecommand.Submit(callbackCtx, target.Events, 1, func(
			admission runtimecommand.Admission,
			_ int,
			_ func(int, error),
		) error {
			return admission.StartWork(func(ctx context.Context) {
				close(started)
				<-ctx.Done()
			})
		})
		if err != nil {
			return err
		}
		waiting, err = runtimecommand.Submit(callbackCtx, target.Events, 2, func(
			_ runtimecommand.Admission,
			_ int,
			_ func(int, error),
		) error {
			return nil
		})
		return err
	}); err != nil {
		t.Fatalf("submit close-settlement events: %v", err)
	}
	awaitRuntimeEventSignal(t, started)

	closed := make(chan error, 1)
	go func() {
		_, err := attachment.Release(context.Background(), RuntimeReleaseClose)
		closed <- err
	}()
	assertRuntimeEventUnavailable(t, running)
	assertRuntimeEventUnavailable(t, waiting)
	if err := awaitRuntimeEventChannel(t, closed); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
}

func testRuntimeEventWorkJoin(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(t, fixture.authority, sessionID, "runtime-events", &plan)

	workStarted := make(chan struct{})
	workStopped := make(chan struct{})
	var deferred *runtimecommand.Deferred[string]
	if err := fixture.authority.WithRuntimeEvents(context.Background(), attachment.Resource(), func(callbackCtx context.Context, target RuntimeEventTarget) error {
		var err error
		deferred, err = runtimecommand.Submit(callbackCtx, target.Events, "work", func(
			admission runtimecommand.Admission,
			_ string,
			_ func(string, error),
		) error {
			return admission.StartWork(func(ctx context.Context) {
				close(workStarted)
				<-ctx.Done()
				close(workStopped)
			})
		})
		return err
	}); err != nil {
		t.Fatalf("submit queue-owned work: %v", err)
	}
	awaitRuntimeEventSignal(t, workStarted)

	if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	select {
	case <-workStopped:
	default:
		t.Fatal("runtime close returned before queue-owned work stopped")
	}
	assertRuntimeEventUnavailable(t, deferred)
}

func testRuntimeEventReplacement(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	firstAttachment := openLifecycleRuntime(t, fixture.authority, sessionID, "runtime-events-a", &plan)
	var first RuntimeEventTarget
	if err := fixture.authority.WithRuntimeEvents(context.Background(), firstAttachment.Resource(), func(_ context.Context, target RuntimeEventTarget) error {
		first = target
		return nil
	}); err != nil {
		t.Fatalf("read first runtime target: %v", err)
	}
	if _, err := firstAttachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
		t.Fatalf("close first runtime: %v", err)
	}

	secondAttachment := openLifecycleRuntime(t, fixture.authority, sessionID, "runtime-events-b", &plan)
	t.Cleanup(func() {
		if _, err := secondAttachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
			t.Errorf("close second runtime: %v", err)
		}
	})
	var second RuntimeEventTarget
	if err := fixture.authority.WithRuntimeEvents(context.Background(), secondAttachment.Resource(), func(_ context.Context, target RuntimeEventTarget) error {
		second = target
		return nil
	}); err != nil {
		t.Fatalf("read replacement runtime target: %v", err)
	}
	if first.Resource == second.Resource || first.Events == second.Events {
		t.Fatal("replacement reused the prior Resource Generation or Runtime Event queue")
	}
	if deferred, err := runtimecommand.Submit(context.Background(), first.Events, 1, runtimeEventEcho); !errors.Is(err, runtimecommand.ErrUnavailable) || deferred != nil {
		t.Fatalf("submit through closed generation = (%v, %v), want unavailable", deferred, err)
	}
	deferred, err := runtimecommand.Submit(context.Background(), second.Events, 2, runtimeEventEcho)
	if err != nil {
		t.Fatalf("submit through replacement generation: %v", err)
	}
	if got := awaitRuntimeEventResult(t, deferred); got != 2 {
		t.Fatalf("replacement result = %d, want 2", got)
	}
}

func runtimeEventEcho(
	_ runtimecommand.Admission,
	value int,
	complete func(int, error),
) error {
	complete(value, nil)
	return nil
}

func awaitRuntimeEventResult[Result interface{}](t *testing.T, deferred *runtimecommand.Deferred[Result]) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := deferred.Await(ctx)
	if err != nil {
		t.Fatalf("await runtime event: %v", err)
	}
	return result
}

func assertRuntimeEventUnavailable[Result interface{}](t *testing.T, deferred *runtimecommand.Deferred[Result]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := deferred.Await(ctx); !errors.Is(err, runtimecommand.ErrUnavailable) {
		t.Fatalf("runtime event settlement = %v, want unavailable", err)
	}
}

func awaitRuntimeEventSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for runtime event signal")
	}
}

func awaitRuntimeEventChannel[Value interface{}](t *testing.T, values <-chan Value) Value {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for runtime event value")
		var zero Value
		return zero
	}
}
