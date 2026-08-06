package workflowexecution

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
)

func TestResumeCaptureWaitsForStoppedToQueuedPublication(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-resume-publication", "node-agent")
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{{
			Reference: reference,
			Scheduling: &workflow.CurrentNodeScheduling{
				State: workflow.CurrentNodeSchedulingInterrupted,
			},
		}},
		resumeCommitStarted: make(chan struct{}),
		resumeCommitRelease: make(chan struct{}),
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &blockingCurrentNodeRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		close(runner.release)
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	resumed := make(chan error, 1)
	go func() {
		_, err := controller.ResumeTaskWithPreparation(context.Background(), reference.TaskID, func(context.Context) error {
			return nil
		})
		resumed <- err
	}()
	select {
	case <-store.resumeCommitStarted:
	case <-time.After(time.Second):
		t.Fatal("Resume did not enter lifecycle publication")
	}

	captured := make(chan workflowstore.LifecycleCapture, 1)
	captureErr := make(chan error, 1)
	go func() {
		capture, err := controller.CaptureLifecycle(context.Background())
		if err != nil {
			captureErr <- err
			return
		}
		captured <- capture
	}()
	select {
	case capture := <-captured:
		_ = capture.Close()
		t.Fatal("capture returned while Resume publication was committing")
	case err := <-captureErr:
		t.Fatalf("capture failed while Resume publication was committing: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(store.resumeCommitRelease)
	if err := <-resumed; err != nil {
		t.Fatalf("ResumeTaskWithPreparation: %v", err)
	}
	var capture workflowstore.LifecycleCapture
	select {
	case capture = <-captured:
	case err := <-captureErr:
		t.Fatalf("capture after Resume publication: %v", err)
	case <-time.After(time.Second):
		t.Fatal("capture did not resume after Resume publication")
	}
	defer func() {
		if err := capture.Close(); err != nil {
			t.Errorf("close capture: %v", err)
		}
	}()

	currentNodes, err := capture.CurrentNodes(context.Background(), reference.TaskID)
	if err != nil {
		t.Fatalf("capture CurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		!currentNodes[0].Reference.Equal(reference) ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("captured durable Current Nodes = %+v, want resumed ready Current Node", currentNodes)
	}
	queued := capture.QueuedCurrentNodes(reference.TaskID)
	if len(queued) != 1 || !queued[0].Equal(reference) {
		t.Fatalf("captured queued Current Nodes = %+v, want %v", queued, reference)
	}
}

func TestResumePublicationRollbackLeavesStoppedDurableAndRuntimeState(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-resume-rollback", "node-agent")
	commitErr := errors.New("commit Resume")
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{{
			Reference: reference,
			Scheduling: &workflow.CurrentNodeScheduling{
				State: workflow.CurrentNodeSchedulingInterrupted,
			},
		}},
		resumeCommitErr: commitErr,
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(
		t,
		store,
		&blockingCurrentNodeRunner{entered: make(chan struct{}), release: make(chan struct{})},
		authority,
		1,
	)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if _, err := controller.ResumeTaskWithPreparation(
		context.Background(),
		reference.TaskID,
		func(context.Context) error { return nil },
	); !errors.Is(err, commitErr) {
		t.Fatalf("Resume error = %v, want %v", err, commitErr)
	}

	capture, err := controller.CaptureLifecycle(context.Background())
	if err != nil {
		t.Fatalf("capture after rollback: %v", err)
	}
	defer func() {
		if err := capture.Close(); err != nil {
			t.Errorf("close capture: %v", err)
		}
	}()
	currentNodes, err := capture.CurrentNodes(context.Background(), reference.TaskID)
	if err != nil {
		t.Fatalf("capture CurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
		t.Fatalf("captured durable Current Nodes = %+v, want interrupted Current Node", currentNodes)
	}
	if queued := capture.QueuedCurrentNodes(reference.TaskID); len(queued) != 0 {
		t.Fatalf("captured queued Current Nodes = %+v, want none", queued)
	}
	if snapshot := controller.Snapshot(); len(snapshot.ExplicitStarts) != 0 {
		t.Fatalf("controller explicit starts after rollback = %+v, want none", snapshot.ExplicitStarts)
	}
	if err := controller.EnsureTaskQuiescent(reference.TaskID); err != nil {
		t.Fatalf("Task remained runtime-owned after rollback: %v", err)
	}
}

func TestTaskDeletionGatePreventsResumeAfterQuiescenceRevalidation(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-delete-resume-race", "node-agent")
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{{
			Reference: reference,
			Scheduling: &workflow.CurrentNodeScheduling{
				State: workflow.CurrentNodeSchedulingInterrupted,
			},
		}},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &countingCurrentNodeRunner{}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	deleteEntered := make(chan struct{})
	deleteRelease := make(chan struct{})
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- controller.RunTaskDeletion(
			context.Background(),
			[]workflow.TaskID{reference.TaskID},
			func(context.Context) error {
				close(deleteEntered)
				<-deleteRelease
				store.mu.Lock()
				store.interrupted = nil
				store.mu.Unlock()
				return nil
			},
		)
	}()
	select {
	case <-deleteEntered:
	case <-time.After(time.Second):
		t.Fatal("Task deletion did not enter after quiescence revalidation")
	}

	resumeDone := make(chan error, 1)
	go func() {
		_, err := controller.ResumeTask(context.Background(), reference.TaskID)
		resumeDone <- err
	}()
	select {
	case err := <-resumeDone:
		t.Fatalf("Resume escaped the Task deletion gate: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(deleteRelease)
	if err := <-deleteDone; err != nil {
		t.Fatalf("RunTaskDeletion: %v", err)
	}
	select {
	case err := <-resumeDone:
		var conflict *TaskResumeConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("Resume after Task deletion = %v, want no resumable Current Node", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Resume did not revalidate after Task deletion released its gate")
	}
	if runner.starts() != 0 {
		t.Fatalf("Current Node starts after Task deletion = %d, want 0", runner.starts())
	}
}

func TestWorkflowDeletionGatesEveryAffectedTaskAgainstResume(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-workflow-delete-resume-race", "node-agent")
	siblingTaskID := workflow.TaskID("task-workflow-delete-sibling")
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{{
			Reference: reference,
			Scheduling: &workflow.CurrentNodeScheduling{
				State: workflow.CurrentNodeSchedulingInterrupted,
			},
		}},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &countingCurrentNodeRunner{}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	deleteEntered := make(chan struct{})
	deleteRelease := make(chan struct{})
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- controller.RunTaskDeletion(
			context.Background(),
			[]workflow.TaskID{siblingTaskID, reference.TaskID},
			func(context.Context) error {
				close(deleteEntered)
				<-deleteRelease
				store.mu.Lock()
				store.interrupted = nil
				store.mu.Unlock()
				return nil
			},
		)
	}()
	select {
	case <-deleteEntered:
	case <-time.After(time.Second):
		t.Fatal("Workflow deletion did not enter after all-Task quiescence revalidation")
	}

	resumeDone := make(chan error, 1)
	go func() {
		_, err := controller.ResumeTask(context.Background(), reference.TaskID)
		resumeDone <- err
	}()
	select {
	case err := <-resumeDone:
		t.Fatalf("Resume escaped the Workflow deletion Task gates: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(deleteRelease)
	if err := <-deleteDone; err != nil {
		t.Fatalf("Workflow RunTaskDeletion: %v", err)
	}
	select {
	case err := <-resumeDone:
		var conflict *TaskResumeConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("Resume after Workflow deletion = %v, want no resumable Current Node", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Resume did not revalidate after Workflow deletion released its Task gates")
	}
	if runner.starts() != 0 {
		t.Fatalf("Current Node starts after Workflow deletion = %d, want 0", runner.starts())
	}
}

func TestResumeCloseBeforePublicationCommitRollsBackWithoutStranding(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-resume-close-race", "node-agent")
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{{
			Reference: reference,
			Scheduling: &workflow.CurrentNodeScheduling{
				State: workflow.CurrentNodeSchedulingInterrupted,
			},
		}},
		resumePrepareStarted:  make(chan struct{}),
		resumePrepareRelease:  make(chan struct{}),
		resumePrepareCanceled: make(chan struct{}),
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(
		t,
		store,
		&blockingCurrentNodeRunner{entered: make(chan struct{}), release: make(chan struct{})},
		authority,
		1,
	)
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	type resumeOutcome struct {
		err   error
		panic any
	}
	resumed := make(chan resumeOutcome, 1)
	go func() {
		outcome := resumeOutcome{}
		defer func() {
			outcome.panic = recover()
			resumed <- outcome
		}()
		_, outcome.err = controller.ResumeTaskWithPreparation(
			context.Background(),
			reference.TaskID,
			func(context.Context) error { return nil },
		)
	}()
	select {
	case <-store.resumePrepareStarted:
	case <-time.After(time.Second):
		t.Fatal("Resume did not prepare its SQLite mutation")
	}

	closed := make(chan error, 1)
	go func() {
		closed <- controller.Close()
	}()
	select {
	case <-store.resumePrepareCanceled:
	case <-time.After(time.Second):
		t.Fatal("controller Close did not cancel the prepared Resume before publication commit")
	}

	outcome := <-resumed
	if outcome.panic != nil {
		t.Fatalf("Resume panicked while racing controller Close: %v", outcome.panic)
	}
	if !errors.Is(outcome.err, errCurrentNodeControllerClosed) {
		t.Fatalf("Resume error = %v, want controller-closed cancellation", outcome.err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("controller Close: %v", err)
	}

	if _, err := controller.CaptureLifecycle(context.Background()); !errors.Is(err, workflowstore.ErrLifecyclePublicationClosed) {
		t.Fatalf("capture after controller Close = %v, want publication closed", err)
	}
	store.mu.Lock()
	currentNodes := append([]workflow.CurrentNode(nil), store.interrupted...)
	store.mu.Unlock()
	if len(currentNodes) != 1 ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
		t.Fatalf("durable Current Nodes = %+v, want unchanged interruption", currentNodes)
	}
	store.publication.mu.RLock()
	rootCount := len(store.publication.root)
	store.publication.mu.RUnlock()
	if rootCount != 0 {
		t.Fatalf("closed publication retained %d lifecycle root entries", rootCount)
	}
	controller.mu.Lock()
	runCount := len(controller.runs.byCurrentNode)
	controller.mu.Unlock()
	if runCount != 0 {
		t.Fatalf("Close race left %d staged Runs", runCount)
	}
}

func TestResumePublicationCommitWinningCloseRaceLeavesNoRootWithoutRun(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-resume-close-commit-wins", "node-agent")
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{{
			Reference: reference,
			Scheduling: &workflow.CurrentNodeScheduling{
				State: workflow.CurrentNodeSchedulingInterrupted,
			},
		}},
		resumeCommitWon:        make(chan struct{}),
		resumeCommitWinRelease: make(chan struct{}),
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(
		t,
		store,
		&blockingCurrentNodeRunner{entered: make(chan struct{}), release: make(chan struct{})},
		authority,
		1,
	)
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	type resumeOutcome struct {
		err   error
		panic any
	}
	resumed := make(chan resumeOutcome, 1)
	go func() {
		outcome := resumeOutcome{}
		defer func() {
			outcome.panic = recover()
			resumed <- outcome
		}()
		_, outcome.err = controller.ResumeTaskWithPreparation(
			context.Background(),
			reference.TaskID,
			func(context.Context) error { return nil },
		)
	}()
	select {
	case <-store.resumeCommitWon:
	case <-time.After(time.Second):
		t.Fatal("Resume did not enter its commit-wins boundary")
	}

	closed := make(chan error, 1)
	go func() {
		closed <- controller.Close()
	}()
	select {
	case <-controller.workerContext.Done():
	case <-time.After(time.Second):
		t.Fatal("controller Close did not begin while Resume commit was in progress")
	}
	select {
	case err := <-closed:
		t.Fatalf("controller Close returned before Resume publication resolved: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(store.resumeCommitWinRelease)
	outcome := <-resumed
	if outcome.panic != nil {
		t.Fatalf("commit-winning Resume panicked while racing controller Close: %v", outcome.panic)
	}
	if outcome.err != nil {
		t.Fatalf("commit-winning Resume: %v", outcome.err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("controller Close after commit-winning Resume: %v", err)
	}

	store.mu.Lock()
	currentNodes := append([]workflow.CurrentNode(nil), store.interrupted...)
	store.mu.Unlock()
	if len(currentNodes) != 1 ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("durable Current Nodes = %+v, want committed ready Current Node", currentNodes)
	}
	controller.mu.Lock()
	runCount := len(controller.runs.byCurrentNode)
	controller.mu.Unlock()
	if runCount != 0 {
		t.Fatalf("closed controller retained %d Runs", runCount)
	}
	store.publication.mu.RLock()
	rootCount := len(store.publication.root)
	publicationClosed := store.publication.closed
	store.publication.mu.RUnlock()
	if !publicationClosed || rootCount != 0 {
		t.Fatalf("closed publication state = closed:%t roots:%d, want closed with no retained root", publicationClosed, rootCount)
	}
	if _, err := controller.CaptureLifecycle(context.Background()); !errors.Is(err, workflowstore.ErrLifecyclePublicationClosed) {
		t.Fatalf("capture after commit-winning Close = %v, want publication closed", err)
	}
}

func TestResumeCallerCancellationBeforePublicationCommitRollsBackWithoutLeaks(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-resume-cancel", "node-agent")
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{{
			Reference: reference,
			Scheduling: &workflow.CurrentNodeScheduling{
				State: workflow.CurrentNodeSchedulingInterrupted,
			},
		}},
		resumePrepareStarted:  make(chan struct{}),
		resumePrepareRelease:  make(chan struct{}),
		resumePrepareCanceled: make(chan struct{}),
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(
		t,
		store,
		&blockingCurrentNodeRunner{entered: make(chan struct{}), release: make(chan struct{})},
		authority,
		1,
	)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	resumed := make(chan error, 1)
	go func() {
		_, err := controller.ResumeTaskWithPreparation(
			ctx,
			reference.TaskID,
			func(context.Context) error { return nil },
		)
		resumed <- err
	}()
	select {
	case <-store.resumePrepareStarted:
	case <-time.After(time.Second):
		t.Fatal("Resume did not prepare its SQLite mutation")
	}
	cancel()
	select {
	case <-store.resumePrepareCanceled:
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not abort prepared Resume")
	}
	if err := <-resumed; !errors.Is(err, context.Canceled) {
		t.Fatalf("Resume error = %v, want caller cancellation", err)
	}

	capture, err := controller.CaptureLifecycle(context.Background())
	if err != nil {
		t.Fatalf("capture after caller cancellation: %v", err)
	}
	defer func() {
		if err := capture.Close(); err != nil {
			t.Errorf("close capture: %v", err)
		}
	}()
	currentNodes, err := capture.CurrentNodes(context.Background(), reference.TaskID)
	if err != nil {
		t.Fatalf("capture CurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
		t.Fatalf("captured durable Current Nodes = %+v, want unchanged interruption", currentNodes)
	}
	if queued := capture.QueuedCurrentNodes(reference.TaskID); len(queued) != 0 {
		t.Fatalf("captured queued Current Nodes = %+v, want none", queued)
	}
	controller.mu.Lock()
	runCount := len(controller.runs.byCurrentNode)
	controller.mu.Unlock()
	if runCount != 0 {
		t.Fatalf("caller cancellation left %d staged Runs", runCount)
	}
}

func TestLifecycleCaptureRetainsPinnedRootAfterLaterResume(t *testing.T) {
	first := currentNodeReferenceForControllerTest(t, "task-resume-first", "node-agent")
	second := currentNodeReferenceForControllerTest(t, "task-resume-second", "node-agent")
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{
			{
				Reference: first,
				Scheduling: &workflow.CurrentNodeScheduling{
					State: workflow.CurrentNodeSchedulingInterrupted,
				},
			},
			{
				Reference: second,
				Scheduling: &workflow.CurrentNodeScheduling{
					State: workflow.CurrentNodeSchedulingInterrupted,
				},
			},
		},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &blockingCurrentNodeRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		close(runner.release)
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	store.resumeClassifications = []workflowstore.CurrentNodeResumeClassification{{
		CurrentNode: store.interrupted[0],
	}}
	if _, err := controller.ResumeTask(context.Background(), first.TaskID); err != nil {
		t.Fatalf("Resume first Task: %v", err)
	}
	oldCapture, err := controller.CaptureLifecycle(context.Background())
	if err != nil {
		t.Fatalf("capture first root: %v", err)
	}
	defer func() {
		if err := oldCapture.Close(); err != nil {
			t.Errorf("close old capture: %v", err)
		}
	}()

	store.resumeClassifications = []workflowstore.CurrentNodeResumeClassification{{
		CurrentNode: store.interrupted[1],
	}}
	if _, err := controller.ResumeTask(context.Background(), second.TaskID); err != nil {
		t.Fatalf("Resume second Task: %v", err)
	}
	if queued := oldCapture.QueuedCurrentNodes(first.TaskID); len(queued) != 1 || !queued[0].Equal(first) {
		t.Fatalf("old capture first queued Current Nodes = %+v, want %v", queued, first)
	}
	if queued := oldCapture.QueuedCurrentNodes(second.TaskID); len(queued) != 0 {
		t.Fatalf("old capture observed later Resume: %+v", queued)
	}

	newCapture, err := controller.CaptureLifecycle(context.Background())
	if err != nil {
		t.Fatalf("capture latest root: %v", err)
	}
	defer func() {
		if err := newCapture.Close(); err != nil {
			t.Errorf("close new capture: %v", err)
		}
	}()
	if queued := newCapture.QueuedCurrentNodes(first.TaskID); len(queued) != 1 || !queued[0].Equal(first) {
		t.Fatalf("new capture first queued Current Nodes = %+v, want %v", queued, first)
	}
	if queued := newCapture.QueuedCurrentNodes(second.TaskID); len(queued) != 1 || !queued[0].Equal(second) {
		t.Fatalf("new capture second queued Current Nodes = %+v, want %v", queued, second)
	}
}
