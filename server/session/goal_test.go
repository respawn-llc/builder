package session

import (
	"errors"
	"sync"
	"testing"
)

func TestSetGoalPersistsOnlyMetadata(t *testing.T) {
	store := newSessionTestStore(t)

	goal, _, err := store.SetGoal("  ship goal mode\nwith docs  ", GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if goal.ID == "" {
		t.Fatalf("goal id is empty")
	}
	if goal.Objective != "ship goal mode\nwith docs" {
		t.Fatalf("objective = %q", goal.Objective)
	}
	if goal.Status != GoalStatusActive {
		t.Fatalf("status = %q, want active", goal.Status)
	}

	reopened, err := openSessionTestStore(store)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	persisted := reopened.Meta().Goal
	if persisted == nil {
		t.Fatalf("persisted goal is nil")
	}
	if *persisted != goal {
		t.Fatalf("persisted goal = %+v, want %+v", *persisted, goal)
	}

	events, err := collectEvents(reopened)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("goal metadata mutation emitted events: %+v", events)
	}
}

func TestSetGoalReportsCommittedObserverFailure(t *testing.T) {
	observer := &recordingPersistenceObserver{}
	store, err := Create(
		t.TempDir(),
		"workspace",
		t.TempDir(),
		testSessionCategory,
		WithPersistenceObserver(observer),
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	observer.err = errors.New("goal observer failed")

	goal, receipt, err := store.SetGoal("ship committed goal", GoalActorUser)
	if !receipt.Committed {
		t.Fatalf("receipt = %+v, want committed", receipt)
	}
	if !errors.Is(err, observer.err) {
		t.Fatalf("SetGoal error = %v, want observer error", err)
	}
	if goal.Objective != "ship committed goal" || goal.Status != GoalStatusActive {
		t.Fatalf("returned goal = %+v, want committed active goal", goal)
	}
	if persisted := store.Meta().Goal; persisted == nil || *persisted != goal {
		t.Fatalf("persisted goal = %+v, want %+v", persisted, goal)
	}
}

func TestGoalMetadataMutationsReportCommitCertainty(t *testing.T) {
	type committedMutation struct {
		name   string
		mutate func(*Store) (GoalState, CommitReceipt, error)
		assert func(*testing.T, *Store, GoalState)
	}
	cases := []committedMutation{
		{
			name: "status",
			mutate: func(store *Store) (GoalState, CommitReceipt, error) {
				goal, _, receipt, err := store.SetGoalStatus(GoalStatusPaused, GoalActorUser)
				return goal, receipt, err
			},
			assert: func(t *testing.T, store *Store, goal GoalState) {
				t.Helper()
				if goal.Status != GoalStatusPaused {
					t.Fatalf("transitioned goal = %+v, want paused", goal)
				}
				if persisted := store.Meta().Goal; persisted == nil || *persisted != goal {
					t.Fatalf("persisted goal = %+v, want %+v", persisted, goal)
				}
			},
		},
		{
			name: "clear",
			mutate: func(store *Store) (GoalState, CommitReceipt, error) {
				return store.ClearGoal(GoalActorUser)
			},
			assert: func(t *testing.T, store *Store, goal GoalState) {
				t.Helper()
				if goal.Status != GoalStatusActive {
					t.Fatalf("cleared goal = %+v, want active prior state", goal)
				}
				if persisted := store.Meta().Goal; persisted != nil {
					t.Fatalf("persisted goal = %+v, want nil", persisted)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observer := &recordingPersistenceObserver{}
			store, err := Create(
				t.TempDir(),
				"workspace",
				t.TempDir(),
				testSessionCategory,
				WithPersistenceObserver(observer),
			)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if _, _, err := store.SetGoal("seed goal", GoalActorUser); err != nil {
				t.Fatalf("SetGoal: %v", err)
			}
			observer.err = errors.New("goal observer failed")

			goal, receipt, err := tc.mutate(store)
			if !receipt.Committed {
				t.Fatalf("receipt = %+v, want committed", receipt)
			}
			if !errors.Is(err, observer.err) {
				t.Fatalf("mutation error = %v, want observer error", err)
			}
			tc.assert(t, store, goal)
		})
	}
}

func TestCompleteGoalIfActiveReportsCommittedObserverFailure(t *testing.T) {
	observer := &recordingPersistenceObserver{}
	store, err := Create(
		t.TempDir(),
		"workspace",
		t.TempDir(),
		testSessionCategory,
		WithPersistenceObserver(observer),
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	active, _, err := store.SetGoal("seed goal", GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	observer.err = errors.New("goal observer failed")

	completed, transitioned, receipt, err := store.CompleteGoalIfActive(active.ID, GoalActorSystem)
	if !transitioned || !receipt.Committed {
		t.Fatalf("transitioned=%v receipt=%+v, want committed transition", transitioned, receipt)
	}
	if !errors.Is(err, observer.err) {
		t.Fatalf("CompleteGoalIfActive error = %v, want observer error", err)
	}
	if completed.Status != GoalStatusComplete {
		t.Fatalf("completed goal = %+v, want complete", completed)
	}
	if persisted := store.Meta().Goal; persisted == nil || *persisted != completed {
		t.Fatalf("persisted goal = %+v, want %+v", persisted, completed)
	}
}

func TestSetGoalReportsUncommittedPersistenceFailure(t *testing.T) {
	store, err := NewLazy(t.TempDir(), "workspace", t.TempDir(), testSessionCategory)
	if err != nil {
		t.Fatalf("NewLazy: %v", err)
	}

	goal, receipt, err := store.SetGoal("uncommitted goal", GoalActorUser)
	if err == nil || receipt.Committed {
		t.Fatalf("SetGoal goal=%+v receipt=%+v err=%v, want uncommitted error", goal, receipt, err)
	}
	if persisted := store.Meta().Goal; persisted != nil {
		t.Fatalf("persisted goal = %+v, want nil", persisted)
	}
}

func TestGoalStatusAndClearPersistOnlyMetadata(t *testing.T) {
	store := newSessionTestStore(t)
	first, _, err := store.SetGoal("first goal", GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal first: %v", err)
	}
	second, _, err := store.SetGoal("second goal", GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal second: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("replacement reused goal id %q", second.ID)
	}

	paused, _, _, err := store.SetGoalStatus(GoalStatusPaused, GoalActorAgent)
	if err != nil {
		t.Fatalf("SetGoalStatus paused: %v", err)
	}
	if paused.Status != GoalStatusPaused {
		t.Fatalf("paused status = %q", paused.Status)
	}
	cleared, _, err := store.ClearGoal(GoalActorUser)
	if err != nil {
		t.Fatalf("ClearGoal: %v", err)
	}
	if cleared.ID != second.ID || cleared.Status != GoalStatusPaused {
		t.Fatalf("cleared goal = %+v, want second paused goal", cleared)
	}
	if store.Meta().Goal != nil {
		t.Fatalf("meta goal after clear = %+v, want nil", store.Meta().Goal)
	}

	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("goal metadata mutations emitted events: %+v", events)
	}
}

func TestSetGoalRejectsAgentOverwriteOfActiveOrPausedGoal(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status GoalStatus
	}{
		{name: "active", status: GoalStatusActive},
		{name: "paused", status: GoalStatusPaused},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newSessionTestStore(t)
			existing, _, err := store.SetGoal("existing goal", GoalActorUser)
			if err != nil {
				t.Fatalf("SetGoal initial: %v", err)
			}
			if tt.status == GoalStatusPaused {
				existing, _, _, err = store.SetGoalStatus(GoalStatusPaused, GoalActorUser)
				if err != nil {
					t.Fatalf("SetGoalStatus paused: %v", err)
				}
			}

			_, _, err = store.SetGoal("agent replacement", GoalActorAgent)
			var blocked GoalAgentOverwriteBlockedError
			if !errors.As(err, &blocked) {
				t.Fatalf("SetGoal agent overwrite error = %v, want GoalAgentOverwriteBlockedError", err)
			}
			if blocked.Goal.ID != existing.ID || blocked.Goal.Objective != existing.Objective || blocked.Goal.Status != tt.status {
				t.Fatalf("blocked goal = %+v, want existing %+v status %q", blocked.Goal, existing, tt.status)
			}
			if goal := store.Meta().Goal; goal == nil || goal.ID != existing.ID || goal.Objective != existing.Objective || goal.Status != tt.status {
				t.Fatalf("persisted goal after rejected overwrite = %+v", goal)
			}
		})
	}
}

func TestSetGoalAllowsOnlyOneConcurrentAgentGoal(t *testing.T) {
	store := newSessionTestStore(t)
	start := make(chan struct{})
	type result struct {
		goal GoalState
		err  error
	}
	results := make(chan result, 2)

	var ready sync.WaitGroup
	ready.Add(2)
	for _, objective := range []string{"first agent goal", "second agent goal"} {
		objective := objective
		go func() {
			ready.Done()
			<-start
			goal, _, err := store.SetGoal(objective, GoalActorAgent)
			results <- result{goal: goal, err: err}
		}()
	}
	ready.Wait()
	close(start)

	successes := make([]GoalState, 0, 1)
	blocked := 0
	for i := 0; i < 2; i++ {
		got := <-results
		if got.err == nil {
			successes = append(successes, got.goal)
			continue
		}
		var blockedErr GoalAgentOverwriteBlockedError
		if !errors.As(got.err, &blockedErr) {
			t.Fatalf("SetGoal concurrent error = %v, want blocked error", got.err)
		}
		blocked++
	}
	if len(successes) != 1 || blocked != 1 {
		t.Fatalf("concurrent agent goals successes=%d blocked=%d, want 1/1", len(successes), blocked)
	}
	if goal := store.Meta().Goal; goal == nil || goal.ID != successes[0].ID || goal.Status != GoalStatusActive {
		t.Fatalf("persisted concurrent goal = %+v, want success %+v", goal, successes[0])
	}
}

func TestGoalValidationRejectsInvalidValues(t *testing.T) {
	store := newSessionTestStore(t)
	if _, _, err := store.SetGoal(" \n\t ", GoalActorUser); err == nil {
		t.Fatalf("SetGoal empty objective error = nil")
	}
	if _, _, err := store.SetGoal("objective", GoalActor("robot")); err == nil {
		t.Fatalf("SetGoal invalid actor error = nil")
	}
	if _, _, err := store.SetGoal("objective", GoalActorUser); err != nil {
		t.Fatalf("SetGoal valid: %v", err)
	}
	if _, _, _, err := store.SetGoalStatus(GoalStatus("blocked"), GoalActorUser); err == nil {
		t.Fatalf("SetGoalStatus invalid status error = nil")
	}
}
