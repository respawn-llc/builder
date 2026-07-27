package workflowview

import (
	"context"
	"errors"
	"sync"
	"testing"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/shared/runtimeids"
)

func TestTaskStatusSnapshotCaptureRetriesOnlyWhenLiveObservationsAdvance(t *testing.T) {
	ctx, metadataStore, _, _, _ := newWorkflowViewTestContextFixture(t)
	authority := &scriptedWorkflowExecutionObservations{
		snapshots: []sessionruntime.AllWorkflowExecutionSnapshot{
			workflowExecutionSnapshotForTest(1),
			workflowExecutionSnapshotForTest(2),
			workflowExecutionSnapshotForTest(2),
			workflowExecutionSnapshotForTest(2),
		},
	}
	scheduler := staticSchedulerObservations{snapshot: schedulerSnapshotForTest(1)}
	coordinator, err := newTaskStatusSnapshotCoordinator(
		metadataStore.DB(),
		metadataStore.Queries(),
		workflowexecution.NewMutationPermit(),
		authority,
		scheduler,
	)
	if err != nil {
		t.Fatalf("newTaskStatusSnapshotCoordinator: %v", err)
	}

	snapshot, err := coordinator.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	t.Cleanup(func() {
		if err := snapshot.Close(); err != nil {
			t.Errorf("close status snapshot: %v", err)
		}
	})
	if authority.callCount() != 4 {
		t.Fatalf("authority observation calls = %d, want two stable-capture attempts", authority.callCount())
	}
	if snapshot.authority.ExecutionMapRevision != 2 || snapshot.scheduler.Revision != 1 {
		t.Fatalf("captured revisions = authority:%d scheduler:%d, want authority:2 scheduler:1", snapshot.authority.ExecutionMapRevision, snapshot.scheduler.Revision)
	}
	if len(snapshot.currentRunFacts) != 1 ||
		!snapshot.currentRunFacts[0].ObservedRunID.Valid ||
		snapshot.currentRunFacts[0].ObservedRunID.String != "run-observation" {
		t.Fatalf("anchored current-run facts = %+v, want the one deduplicated observed run", snapshot.currentRunFacts)
	}
}

func TestTaskStatusSnapshotCaptureReturnsStructuralErrorWithoutRetry(t *testing.T) {
	ctx, metadataStore, _, _, _ := newWorkflowViewTestContextFixture(t)
	authority := &scriptedWorkflowExecutionObservations{
		snapshots: []sessionruntime.AllWorkflowExecutionSnapshot{
			workflowExecutionSnapshotForTest(1),
			workflowExecutionSnapshotForTest(1),
		},
	}
	coordinator, err := newTaskStatusSnapshotCoordinator(
		metadataStore.DB(),
		metadataStore.Queries(),
		workflowexecution.NewMutationPermit(),
		authority,
		staticSchedulerObservations{snapshot: workflowexecution.SchedulerActiveRunSnapshot{Revision: 1, ActiveRuns: []workflowexecution.SchedulerActiveRunObservation{}}},
	)
	if err != nil {
		t.Fatalf("newTaskStatusSnapshotCoordinator: %v", err)
	}

	_, err = coordinator.Capture(ctx)
	var consistencyErr *TaskStatusSnapshotConsistencyError
	if !errors.As(err, &consistencyErr) {
		t.Fatalf("Capture error = %T %v, want TaskStatusSnapshotConsistencyError", err, err)
	}
	if consistencyErr.Reason != TaskStatusSnapshotConsistencyReasonAuthorityMissingScheduler {
		t.Fatalf("consistency reason = %q, want %q", consistencyErr.Reason, TaskStatusSnapshotConsistencyReasonAuthorityMissingScheduler)
	}
	if consistencyErr.Attempts != 1 || authority.callCount() != 2 {
		t.Fatalf("structural failure attempts=%d authority calls=%d, want one attempt/two observations", consistencyErr.Attempts, authority.callCount())
	}
}

func TestTaskStatusSnapshotCaptureExhaustsAfterThreeChangingAttempts(t *testing.T) {
	_, metadataStore, _, _, _ := newWorkflowViewTestContextFixture(t)
	authority := &scriptedWorkflowExecutionObservations{
		snapshots: []sessionruntime.AllWorkflowExecutionSnapshot{
			workflowExecutionSnapshotForTest(1),
			workflowExecutionSnapshotForTest(2),
			workflowExecutionSnapshotForTest(3),
			workflowExecutionSnapshotForTest(4),
			workflowExecutionSnapshotForTest(5),
			workflowExecutionSnapshotForTest(6),
		},
	}
	coordinator, err := newTaskStatusSnapshotCoordinator(
		metadataStore.DB(),
		metadataStore.Queries(),
		workflowexecution.NewMutationPermit(),
		authority,
		staticSchedulerObservations{snapshot: schedulerSnapshotForTest(1)},
	)
	if err != nil {
		t.Fatalf("newTaskStatusSnapshotCoordinator: %v", err)
	}

	_, err = coordinator.Capture(context.Background())
	var consistencyErr *TaskStatusSnapshotConsistencyError
	if !errors.As(err, &consistencyErr) {
		t.Fatalf("Capture error = %T %v, want TaskStatusSnapshotConsistencyError", err, err)
	}
	if consistencyErr.Reason != TaskStatusSnapshotConsistencyReasonLiveObservationChanged {
		t.Fatalf("consistency reason = %q, want %q", consistencyErr.Reason, TaskStatusSnapshotConsistencyReasonLiveObservationChanged)
	}
	if consistencyErr.Attempts != 3 || authority.callCount() != 6 {
		t.Fatalf("changing attempts=%d authority calls=%d, want three attempts/six observations", consistencyErr.Attempts, authority.callCount())
	}
}

func TestTaskStatusSnapshotCaptureCancellationWinsAtObservationBoundaries(t *testing.T) {
	tests := []struct {
		name         string
		cancelOnCall int
		wantCalls    int
	}{
		{name: "before_anchor", cancelOnCall: 1, wantCalls: 1},
		{name: "after_anchor", cancelOnCall: 2, wantCalls: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			_, metadataStore, _, _, _ := newWorkflowViewTestContextFixture(t)
			authority := &scriptedWorkflowExecutionObservations{
				snapshots: []sessionruntime.AllWorkflowExecutionSnapshot{
					workflowExecutionSnapshotForTest(1),
					workflowExecutionSnapshotForTest(1),
				},
				cancel:       cancel,
				cancelOnCall: test.cancelOnCall,
			}
			coordinator, err := newTaskStatusSnapshotCoordinator(
				metadataStore.DB(),
				metadataStore.Queries(),
				workflowexecution.NewMutationPermit(),
				authority,
				staticSchedulerObservations{snapshot: schedulerSnapshotForTest(1)},
			)
			if err != nil {
				t.Fatalf("newTaskStatusSnapshotCoordinator: %v", err)
			}

			_, err = coordinator.Capture(ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Capture error = %v, want context.Canceled", err)
			}
			if authority.callCount() != test.wantCalls {
				t.Fatalf("authority calls = %d, want %d", authority.callCount(), test.wantCalls)
			}
		})
	}
}

func TestTaskStatusSnapshotCaptureRejectsInvalidSchedulerPhase(t *testing.T) {
	ctx, metadataStore, _, _, _ := newWorkflowViewTestContextFixture(t)
	coordinator, err := newTaskStatusSnapshotCoordinator(
		metadataStore.DB(),
		metadataStore.Queries(),
		workflowexecution.NewMutationPermit(),
		&scriptedWorkflowExecutionObservations{
			snapshots: []sessionruntime.AllWorkflowExecutionSnapshot{
				{ExecutionMapRevision: 1, Executions: []sessionruntime.WorkflowExecutionObservation{}},
				{ExecutionMapRevision: 1, Executions: []sessionruntime.WorkflowExecutionObservation{}},
			},
		},
		staticSchedulerObservations{snapshot: workflowexecution.SchedulerActiveRunSnapshot{
			Revision: 1,
			ActiveRuns: []workflowexecution.SchedulerActiveRunObservation{{
				RunID:       "run-observation",
				TaskID:      "task-observation",
				PlacementID: "placement-observation",
				NodeID:      "node-observation",
				Generation:  1,
				Phase:       workflowexecution.SchedulerActiveRunPhase(99),
			}},
		}},
	)
	if err != nil {
		t.Fatalf("newTaskStatusSnapshotCoordinator: %v", err)
	}

	_, err = coordinator.Capture(ctx)
	var consistencyErr *TaskStatusSnapshotConsistencyError
	if !errors.As(err, &consistencyErr) {
		t.Fatalf("Capture error = %T %v, want TaskStatusSnapshotConsistencyError", err, err)
	}
	if consistencyErr.Reason != TaskStatusSnapshotConsistencyReasonInvalidSchedulerObservation || consistencyErr.Attempts != 1 {
		t.Fatalf("consistency error = %+v, want invalid scheduler observation at first attempt", consistencyErr)
	}
}

func TestTaskStatusSnapshotCaptureRejectsEachStableStructuralViolationWithoutRetry(t *testing.T) {
	ctx, metadataStore, _, _, _ := newWorkflowViewTestContextFixture(t)
	validAuthority := workflowExecutionSnapshotForTest(1)
	validScheduler := schedulerSnapshotForTest(1)
	invalidAuthorityTarget := validAuthority
	invalidAuthorityTarget.Executions = append([]sessionruntime.WorkflowExecutionObservation(nil), validAuthority.Executions...)
	invalidAuthorityTarget.Executions[0].Execution.Agent = nil
	invalidAuthorityIdentity := validAuthority
	invalidAuthorityIdentity.Executions = append([]sessionruntime.WorkflowExecutionObservation(nil), validAuthority.Executions...)
	invalidAuthorityIdentity.Executions[0].Execution.Ref.TaskID = ""
	duplicateAuthority := validAuthority
	duplicateAuthority.Executions = append(append([]sessionruntime.WorkflowExecutionObservation(nil), validAuthority.Executions...), validAuthority.Executions[0])
	duplicateScheduler := validScheduler
	duplicateScheduler.ActiveRuns = append(append([]workflowexecution.SchedulerActiveRunObservation(nil), validScheduler.ActiveRuns...), validScheduler.ActiveRuns[0])

	tests := []struct {
		name      string
		authority sessionruntime.AllWorkflowExecutionSnapshot
		scheduler workflowexecution.SchedulerActiveRunSnapshot
		reason    TaskStatusSnapshotConsistencyReason
	}{
		{
			name:      "invalid_authority_target",
			authority: invalidAuthorityTarget,
			scheduler: validScheduler,
			reason:    TaskStatusSnapshotConsistencyReasonInvalidAuthorityObservation,
		},
		{
			name:      "invalid_authority_identity",
			authority: invalidAuthorityIdentity,
			scheduler: validScheduler,
			reason:    TaskStatusSnapshotConsistencyReasonInvalidAuthorityObservation,
		},
		{
			name:      "duplicate_authority_identity",
			authority: duplicateAuthority,
			scheduler: validScheduler,
			reason:    TaskStatusSnapshotConsistencyReasonDuplicateAuthorityIdentity,
		},
		{
			name:      "duplicate_scheduler_identity",
			authority: validAuthority,
			scheduler: duplicateScheduler,
			reason:    TaskStatusSnapshotConsistencyReasonDuplicateSchedulerIdentity,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authority := &scriptedWorkflowExecutionObservations{snapshots: []sessionruntime.AllWorkflowExecutionSnapshot{test.authority, test.authority}}
			coordinator, err := newTaskStatusSnapshotCoordinator(
				metadataStore.DB(),
				metadataStore.Queries(),
				workflowexecution.NewMutationPermit(),
				authority,
				staticSchedulerObservations{snapshot: test.scheduler},
			)
			if err != nil {
				t.Fatalf("newTaskStatusSnapshotCoordinator: %v", err)
			}

			_, err = coordinator.Capture(ctx)
			var consistencyErr *TaskStatusSnapshotConsistencyError
			if !errors.As(err, &consistencyErr) {
				t.Fatalf("Capture error = %T %v, want TaskStatusSnapshotConsistencyError", err, err)
			}
			if consistencyErr.Reason != test.reason || consistencyErr.Attempts != 1 || authority.callCount() != 2 {
				t.Fatalf("consistency error = %+v calls=%d, want reason=%q attempt=1 calls=2", consistencyErr, authority.callCount(), test.reason)
			}
		})
	}
}

type scriptedWorkflowExecutionObservations struct {
	mu           sync.Mutex
	snapshots    []sessionruntime.AllWorkflowExecutionSnapshot
	calls        int
	cancel       context.CancelFunc
	cancelOnCall int
}

func (s *scriptedWorkflowExecutionObservations) AllWorkflowExecutionSnapshot() (sessionruntime.AllWorkflowExecutionSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.snapshots) == 0 {
		return sessionruntime.AllWorkflowExecutionSnapshot{}, errors.New("workflow execution observation script is empty")
	}
	index := s.calls
	if index >= len(s.snapshots) {
		index = len(s.snapshots) - 1
	}
	s.calls++
	if s.cancel != nil && s.calls == s.cancelOnCall {
		s.cancel()
	}
	return s.snapshots[index], nil
}

func (s *scriptedWorkflowExecutionObservations) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type staticSchedulerObservations struct {
	snapshot workflowexecution.SchedulerActiveRunSnapshot
}

func (s staticSchedulerObservations) ActiveRunSnapshot() workflowexecution.SchedulerActiveRunSnapshot {
	return s.snapshot
}

func workflowExecutionSnapshotForTest(revision sessionruntime.WorkflowExecutionMapRevision) sessionruntime.AllWorkflowExecutionSnapshot {
	sessionID, err := runtimeids.ParseSessionID("session-observation")
	if err != nil {
		panic(err)
	}
	return sessionruntime.AllWorkflowExecutionSnapshot{
		ExecutionMapRevision: revision,
		Executions: []sessionruntime.WorkflowExecutionObservation{{
			Execution: sessionruntime.TaskExecution{
				Ref: sessionruntime.WorkflowExecutionRef{
					TaskID:     workflow.TaskID("task-observation"),
					RunID:      workflow.RunID("run-observation"),
					Generation: 1,
				},
				Agent: &sessionruntime.TaskAgentExecutionTarget{SessionID: sessionID},
			},
			PromptRevision: 0,
		}},
	}
}

func schedulerSnapshotForTest(revision workflowexecution.SchedulerActiveRunRevision) workflowexecution.SchedulerActiveRunSnapshot {
	return workflowexecution.SchedulerActiveRunSnapshot{
		Revision: revision,
		ActiveRuns: []workflowexecution.SchedulerActiveRunObservation{{
			RunID:       "run-observation",
			TaskID:      "task-observation",
			PlacementID: "placement-observation",
			NodeID:      "node-observation",
			Generation:  1,
			Phase:       workflowexecution.SchedulerActiveRunPhaseRunning,
		}},
	}
}

var _ workflowExecutionObservationSource = (*scriptedWorkflowExecutionObservations)(nil)
var _ schedulerActiveRunObservationSource = staticSchedulerObservations{}
