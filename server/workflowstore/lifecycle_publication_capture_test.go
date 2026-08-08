package workflowstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/internal/testharness/databaseseed"
	"core/server/workflow"
	"core/shared/config"
	"core/shared/runtimeids"
	sqlitedriver "modernc.org/sqlite"
)

type lifecycleCaptureBarrierPhase uint8

const (
	lifecycleCaptureBeforeSQLiteAnchor lifecycleCaptureBarrierPhase = iota + 1
	lifecycleCaptureAfterSQLiteAnchor
	lifecycleCaptureAfterRootPin
)

func (p lifecycleCaptureBarrierPhase) String() string {
	switch p {
	case lifecycleCaptureBeforeSQLiteAnchor:
		return "before_sqlite_anchor"
	case lifecycleCaptureAfterSQLiteAnchor:
		return "after_sqlite_anchor"
	case lifecycleCaptureAfterRootPin:
		return "after_root_pin"
	default:
		return fmt.Sprintf("unknown_%d", p)
	}
}

type lifecycleSQLiteQueryBarrier struct {
	queryOrdinal int64
	phase        lifecycleCaptureBarrierPhase
	queries      atomic.Int64
	entered      chan struct{}
	release      chan struct{}
	once         sync.Once
}

func (b *lifecycleSQLiteQueryBarrier) beforeQuery(ctx context.Context) error {
	if b.queries.Add(1) != b.queryOrdinal || b.phase != lifecycleCaptureBeforeSQLiteAnchor {
		return nil
	}
	return b.pause(ctx)
}

func (b *lifecycleSQLiteQueryBarrier) afterRow(ctx context.Context) error {
	if b.queries.Load() != b.queryOrdinal || b.phase != lifecycleCaptureAfterSQLiteAnchor {
		return nil
	}
	return b.pause(ctx)
}

func (b *lifecycleSQLiteQueryBarrier) pause(ctx context.Context) error {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

type lifecycleBarrierSQLiteDriver struct {
	delegate driver.Driver
	barrier  *lifecycleSQLiteQueryBarrier
}

func (d *lifecycleBarrierSQLiteDriver) Open(name string) (driver.Conn, error) {
	connection, err := d.delegate.Open(name)
	if err != nil {
		return nil, err
	}
	return &lifecycleBarrierSQLiteConnection{
		Conn:    connection,
		barrier: d.barrier,
	}, nil
}

type lifecycleBarrierSQLiteConnection struct {
	driver.Conn
	barrier *lifecycleSQLiteQueryBarrier
}

func (c *lifecycleBarrierSQLiteConnection) BeginTx(
	ctx context.Context,
	options driver.TxOptions,
) (driver.Tx, error) {
	beginner, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, driver.ErrSkip
	}
	return beginner.BeginTx(ctx, options)
}

func (c *lifecycleBarrierSQLiteConnection) PrepareContext(
	ctx context.Context,
	query string,
) (driver.Stmt, error) {
	preparer, ok := c.Conn.(driver.ConnPrepareContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return preparer.PrepareContext(ctx, query)
}

func (c *lifecycleBarrierSQLiteConnection) ExecContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Result, error) {
	executor, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return executor.ExecContext(ctx, query, args)
}

func (c *lifecycleBarrierSQLiteConnection) QueryContext(
	ctx context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	if err := c.barrier.beforeQuery(ctx); err != nil {
		return nil, err
	}
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := queryer.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return &lifecycleBarrierSQLiteRows{
		Rows:    rows,
		ctx:     ctx,
		barrier: c.barrier,
	}, nil
}

type lifecycleBarrierSQLiteRows struct {
	driver.Rows
	ctx     context.Context
	barrier *lifecycleSQLiteQueryBarrier
	once    sync.Once
	err     error
}

func (r *lifecycleBarrierSQLiteRows) Next(values []driver.Value) error {
	err := r.Rows.Next(values)
	if err != nil {
		return err
	}
	r.once.Do(func() {
		r.err = r.barrier.afterRow(r.ctx)
	})
	return r.err
}

var lifecycleBarrierSQLiteDriverSequence atomic.Uint64

func installLifecycleSQLiteQueryBarrier(
	t *testing.T,
	store *Store,
	persistenceRoot string,
	queryOrdinal int64,
	phase lifecycleCaptureBarrierPhase,
) *lifecycleSQLiteQueryBarrier {
	t.Helper()
	barrier := &lifecycleSQLiteQueryBarrier{
		queryOrdinal: queryOrdinal,
		phase:        phase,
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	driverName := fmt.Sprintf(
		"lifecycle-publication-barrier-%d",
		lifecycleBarrierSQLiteDriverSequence.Add(1),
	)
	sql.Register(driverName, &lifecycleBarrierSQLiteDriver{
		delegate: &sqlitedriver.Driver{},
		barrier:  barrier,
	})
	databasePath := filepath.Join(
		persistenceRoot,
		databaseseed.CurrentMetadataDatabaseRelativePath,
	)
	databaseURL, ok := config.LocalFileURL(databasePath)
	if !ok {
		t.Fatalf("metadata database path %q is not absolute", databasePath)
	}
	pragmas := url.Values{}
	pragmas.Add("_pragma", "foreign_keys(1)")
	pragmas.Add("_pragma", "journal_mode(WAL)")
	pragmas.Add("_pragma", "synchronous(NORMAL)")
	pragmas.Add("_pragma", "busy_timeout(5000)")
	databaseURL.RawQuery = pragmas.Encode()
	db, err := sql.Open(driverName, databaseURL.String())
	if err != nil {
		t.Fatalf("open barrier SQLite database: %v", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	store.db = db
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close barrier SQLite database: %v", err)
		}
	})
	return barrier
}

func TestLifecycleCapturePinsCompatiblePairAcrossControlledPublicationBarriers(t *testing.T) {
	for _, transition := range []struct {
		name       string
		initialRun bool
		publish    func(context.Context, *LifecyclePublication, workflow.CurrentNodeReference) error
	}{
		{
			name:       "stopped_to_queued_creates_entry",
			initialRun: false,
			publish: func(ctx context.Context, publication *LifecyclePublication, reference workflow.CurrentNodeReference) error {
				delta, err := NewQueuedTaskLifecycleDelta(reference.TaskID, []workflow.CurrentNodeReference{reference})
				if err != nil {
					return err
				}
				_, err = publication.PublishResume(ctx, delta)
				return err
			},
		},
		{
			name:       "queued_to_stopped_removes_entry",
			initialRun: true,
			publish: func(ctx context.Context, publication *LifecyclePublication, reference workflow.CurrentNodeReference) error {
				_, err := publication.PublishCurrentNodeInterruption(
					ctx,
					[]workflow.CurrentNodeReference{reference},
					CurrentNodeInterruptionFromReadyOrAdmitted,
					LifecycleFieldPresent,
					workflow.CurrentNodeInterruptionReasonUserInterrupt,
					workflow.NewCurrentNodeInterruptionDetail("capture barrier", nil),
					nil,
				)
				return err
			},
		},
	} {
		for _, phase := range []lifecycleCaptureBarrierPhase{
			lifecycleCaptureBeforeSQLiteAnchor,
			lifecycleCaptureAfterSQLiteAnchor,
			lifecycleCaptureAfterRootPin,
		} {
			t.Run(transition.name+"/"+phase.String(), func(t *testing.T) {
				ctx, publication, reference, persistenceRoot := lifecyclePublicationCaptureFixture(
					t,
					transition.initialRun,
				)
				if phase == lifecycleCaptureAfterRootPin {
					testLifecycleCaptureAfterRootPin(
						t,
						ctx,
						publication,
						reference,
						transition.initialRun,
						transition.publish,
					)
					return
				}
				barrier := installLifecycleSQLiteQueryBarrier(
					t,
					publication.store,
					persistenceRoot,
					1,
					phase,
				)
				captured := make(chan LifecycleCapture, 1)
				captureErr := make(chan error, 1)
				go func() {
					capture, err := publication.Capture(ctx)
					if err != nil {
						captureErr <- err
						return
					}
					captured <- capture
				}()
				awaitSignal(t, barrier.entered, "capture did not reach "+phase.String())

				published := make(chan error, 1)
				go func() {
					published <- transition.publish(ctx, publication, reference)
				}()
				awaitQueuedLifecycleWriter(t, publication)
				select {
				case err := <-published:
					t.Fatalf("publication crossed paused capture boundary: %v", err)
				default:
				}
				close(barrier.release)
				oldCapture := awaitLifecycleCapture(t, captured, captureErr)
				defer func() {
					if err := oldCapture.Close(); err != nil {
						t.Errorf("close old capture: %v", err)
					}
				}()
				if err := <-published; err != nil {
					t.Fatalf("publish transition: %v", err)
				}
				requireLifecycleCapturePair(t, oldCapture, reference, transition.initialRun)
				requireLatestLifecycleCapturePair(
					t,
					ctx,
					publication,
					reference,
					!transition.initialRun,
				)
			})
		}
	}
}

func testLifecycleCaptureAfterRootPin(
	t *testing.T,
	ctx context.Context,
	publication *LifecyclePublication,
	reference workflow.CurrentNodeReference,
	initialRun bool,
	publish func(context.Context, *LifecyclePublication, workflow.CurrentNodeReference) error,
) {
	t.Helper()
	pinned := make(chan LifecycleCapture, 1)
	captureErr := make(chan error, 1)
	releaseReader := make(chan struct{})
	readerReleased := make(chan struct{})
	go func() {
		capture, err := publication.Capture(ctx)
		if err != nil {
			captureErr <- err
			close(readerReleased)
			return
		}
		pinned <- capture
		<-releaseReader
		close(readerReleased)
	}()
	oldCapture := awaitLifecycleCapture(t, pinned, captureErr)
	defer func() {
		close(releaseReader)
		<-readerReleased
		if err := oldCapture.Close(); err != nil {
			t.Errorf("close old capture: %v", err)
		}
	}()

	published := make(chan error, 1)
	go func() {
		published <- publish(ctx, publication, reference)
	}()
	select {
	case err := <-published:
		if err != nil {
			t.Fatalf("publish transition after root pin: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("post-pin reader barrier blocked lifecycle publication")
	}
	select {
	case <-readerReleased:
		t.Fatal("post-pin reader barrier released before the test allowed it")
	default:
	}
	requireLifecycleCapturePair(t, oldCapture, reference, initialRun)
	requireLatestLifecycleCapturePair(t, ctx, publication, reference, !initialRun)
}

func lifecyclePublicationCaptureFixture(
	t *testing.T,
	initialRun bool,
) (context.Context, *LifecyclePublication, workflow.CurrentNodeReference, string) {
	t.Helper()
	ctx, store, binding, cfg := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	started, err := publication.PublishTaskStart(
		ctx,
		task.ID,
		func(result StartTaskResult) (TaskLifecycleDelta, func(error), error) {
			delta, err := NewTaskStartLifecycleDelta(result)
			return delta, nil, err
		},
	)
	if err != nil {
		t.Fatalf("PublishTaskStart: %v", err)
	}
	reference := started.Mutation.Created[0].Reference
	if !initialRun {
		if _, err := publication.PublishCurrentNodeInterruption(
			ctx,
			[]workflow.CurrentNodeReference{reference},
			CurrentNodeInterruptionFromReadyOrAdmitted,
			LifecycleFieldPresent,
			workflow.CurrentNodeInterruptionReasonUserInterrupt,
			workflow.NewCurrentNodeInterruptionDetail("capture fixture", nil),
			nil,
		); err != nil {
			t.Fatalf("publish initial interruption: %v", err)
		}
	}
	return ctx, publication, reference, cfg.PersistenceRoot
}

func requireLatestLifecycleCapturePair(
	t *testing.T,
	ctx context.Context,
	publication *LifecyclePublication,
	reference workflow.CurrentNodeReference,
	runPresent bool,
) {
	t.Helper()
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("capture latest pair: %v", err)
	}
	defer func() {
		if err := capture.Close(); err != nil {
			t.Errorf("close latest capture: %v", err)
		}
	}()
	requireLifecycleCapturePair(t, capture, reference, runPresent)
}

func requireLifecycleCapturePair(
	t *testing.T,
	capture LifecycleCapture,
	reference workflow.CurrentNodeReference,
	runPresent bool,
) {
	t.Helper()
	currentNodes, err := capture.CurrentNodes(context.Background(), reference.TaskID)
	if err != nil {
		t.Fatalf("captured Current Nodes: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(reference) || currentNodes[0].Scheduling == nil {
		t.Fatalf("captured Current Nodes = %+v, want exactly %v", currentNodes, reference)
	}
	wantScheduling := workflow.CurrentNodeSchedulingInterrupted
	if runPresent {
		wantScheduling = workflow.CurrentNodeSchedulingReady
	}
	if currentNodes[0].Scheduling.State != wantScheduling {
		t.Fatalf("captured scheduling = %q, want %q", currentNodes[0].Scheduling.State, wantScheduling)
	}
	queued := capture.QueuedCurrentNodes(reference.TaskID)
	if runPresent {
		if len(queued) != 1 || !queued[0].Equal(reference) {
			t.Fatalf("captured queued Current Nodes = %+v, want %v", queued, reference)
		}
		return
	}
	if len(queued) != 0 {
		t.Fatalf("captured queued Current Nodes = %+v, want none", queued)
	}
}

func awaitLifecycleCapture(
	t *testing.T,
	captured <-chan LifecycleCapture,
	captureErr <-chan error,
) LifecycleCapture {
	t.Helper()
	select {
	case capture := <-captured:
		return capture
	case err := <-captureErr:
		t.Fatalf("capture lifecycle: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("lifecycle capture did not complete")
	}
	panic("unreachable")
}

func awaitQueuedLifecycleWriter(t *testing.T, publication *LifecyclePublication) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if publication.mu.TryRLock() {
			publication.mu.RUnlock()
			time.Sleep(time.Millisecond)
			continue
		}
		return
	}
	t.Fatal("lifecycle writer did not queue behind capture")
}

func awaitSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal(failure)
	}
}

func TestLifecycleCaptureReleasesSQLiteSnapshotOnEveryClosePath(t *testing.T) {
	ctx, publication, _, _ := lifecyclePublicationCaptureFixture(t, true)
	for index := range 32 {
		capture, err := publication.Capture(ctx)
		if err != nil {
			t.Fatalf("Capture %d: %v", index, err)
		}
		if err := capture.Close(); err != nil {
			t.Fatalf("Close %d: %v", index, err)
		}
		if err := capture.Close(); err != nil {
			t.Fatalf("second Close %d: %v", index, err)
		}
	}
	stats := publication.store.db.Stats()
	if stats.InUse != 0 {
		t.Fatalf("lifecycle captures retained %d SQLite connections after Close", stats.InUse)
	}
	if stats.OpenConnections > stats.Idle {
		t.Fatalf("SQLite connection stats after capture reclamation = %+v", stats)
	}
}

func TestLifecycleCaptureAnchorFailureReleasesPublicationBoundary(t *testing.T) {
	ctx, publication, _, persistenceRoot := lifecyclePublicationCaptureFixture(t, true)
	barrier := installLifecycleSQLiteQueryBarrier(
		t,
		publication.store,
		persistenceRoot,
		1,
		lifecycleCaptureBeforeSQLiteAnchor,
	)
	captureCtx, cancel := context.WithCancel(ctx)
	captured := make(chan error, 1)
	go func() {
		_, err := publication.Capture(captureCtx)
		captured <- err
	}()
	awaitSignal(t, barrier.entered, "capture did not pause before SQLite anchoring")
	cancel()
	if err := <-captured; err == nil {
		t.Fatal("canceled lifecycle capture succeeded")
	}
	if !publication.mu.TryLock() {
		t.Fatal("failed capture retained lifecycle publication read boundary")
	}
	publication.mu.Unlock()
}

func TestLifecyclePublicationDoesNotWaitForPinnedCaptureDownstreamQueryWork(t *testing.T) {
	ctx, publication, reference, persistenceRoot := lifecyclePublicationCaptureFixture(t, true)
	barrier := installLifecycleSQLiteQueryBarrier(
		t,
		publication.store,
		persistenceRoot,
		2,
		lifecycleCaptureBeforeSQLiteAnchor,
	)
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer func() {
		if err := capture.Close(); err != nil {
			t.Errorf("close capture: %v", err)
		}
	}()
	queryDone := make(chan error, 1)
	go func() {
		_, err := capture.CurrentNodes(ctx, reference.TaskID)
		queryDone <- err
	}()
	awaitSignal(t, barrier.entered, "downstream lifecycle query did not reach database")

	published := make(chan error, 1)
	go func() {
		_, err := publication.PublishCurrentNodeInterruption(
			ctx,
			[]workflow.CurrentNodeReference{reference},
			CurrentNodeInterruptionFromReadyOrAdmitted,
			LifecycleFieldPresent,
			workflow.CurrentNodeInterruptionReasonUserInterrupt,
			workflow.NewCurrentNodeInterruptionDetail("downstream query barrier", nil),
			nil,
		)
		published <- err
	}()
	select {
	case err := <-published:
		if err != nil {
			close(barrier.release)
			<-queryDone
			t.Fatalf("publish while downstream query paused: %v", err)
		}
	case <-time.After(3 * time.Second):
		close(barrier.release)
		<-queryDone
		t.Fatal("lifecycle publication waited for downstream query work")
	}
	close(barrier.release)
	if err := <-queryDone; err != nil {
		t.Fatalf("downstream lifecycle query: %v", err)
	}
	requireLifecycleCapturePair(t, capture, reference, true)
}

func TestLifecycleCaptureKeepsCompletePendingPromptAfterSuccessorPublication(t *testing.T) {
	ctx, publication, reference, _ := lifecyclePublicationCaptureFixture(t, true)
	scopeID := runtimeids.NewExecutionScopeID()
	sessionID := runtimeids.NewSessionID()
	exact := LifecycleExactExecution{
		ProjectID:   "project-test",
		WorkflowID:  runtimeids.NewWorkflowID(),
		CurrentNode: reference,
		ScopeID:     scopeID,
		Agent:       &LifecycleAgentExecutionTarget{SessionID: sessionID},
		Phase:       LifecycleExactExecutionRunning,
	}
	if err := publication.PublishExactRegistration(ctx, exact, &lifecycleExactActivation{}); err != nil {
		t.Fatalf("PublishExactRegistration: %v", err)
	}
	recommended := 1
	prompt := LifecyclePendingPrompt{
		ID:                     "approval-old-capture",
		Kind:                   LifecyclePendingPromptSessionApproval,
		CreatedAt:              time.UnixMilli(4_000).UTC(),
		Question:               "Approve this action?",
		RecommendedOptionIndex: &recommended,
		ApprovalDecisions: []LifecycleApprovalDecision{
			LifecycleApprovalAllowOnce,
			LifecycleApprovalDeny,
		},
	}
	if err := publication.PublishExactPromptPending(ctx, scopeID, prompt); err != nil {
		t.Fatalf("PublishExactPromptPending: %v", err)
	}
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer func() { _ = capture.Close() }()

	if err := publication.PublishExactPromptResolved(ctx, scopeID, prompt.ID); err != nil {
		t.Fatalf("PublishExactPromptResolved: %v", err)
	}
	if err := publication.PublishExactFinalizing(ctx, scopeID); err != nil {
		t.Fatalf("PublishExactFinalizing: %v", err)
	}

	captured := capture.ExactExecutions(reference.TaskID)
	if len(captured) != 1 ||
		captured[0].Phase != LifecycleExactExecutionRunning ||
		captured[0].Agent == nil ||
		captured[0].Agent.SessionID != sessionID ||
		len(captured[0].PendingPrompts) != 1 ||
		captured[0].PendingPrompts[0].ID != prompt.ID ||
		captured[0].PendingPrompts[0].Question != prompt.Question ||
		len(captured[0].PendingPrompts[0].ApprovalDecisions) != 2 {
		t.Fatalf("old capture Exact execution = %+v, want complete prior prompt", captured)
	}
	next, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("next Capture: %v", err)
	}
	defer func() { _ = next.Close() }()
	successor := next.ExactExecutions(reference.TaskID)
	if len(successor) != 1 ||
		successor[0].Phase != LifecycleExactExecutionFinalizing ||
		len(successor[0].PendingPrompts) != 0 {
		t.Fatalf("successor Exact execution = %+v, want finalizing without prompt", successor)
	}
}

var _ driver.Rows = (*lifecycleBarrierSQLiteRows)(nil)
var _ driver.ConnBeginTx = (*lifecycleBarrierSQLiteConnection)(nil)
var _ driver.ConnPrepareContext = (*lifecycleBarrierSQLiteConnection)(nil)
var _ driver.ExecerContext = (*lifecycleBarrierSQLiteConnection)(nil)
var _ driver.QueryerContext = (*lifecycleBarrierSQLiteConnection)(nil)
var _ driver.Driver = (*lifecycleBarrierSQLiteDriver)(nil)
