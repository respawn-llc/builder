package session

import "testing"

func TestCompleteGoalIfActiveRespectsGuard(t *testing.T) {
	store := newSessionTestStore(t)
	active, err := store.SetGoal("ship the rework", GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	if _, err := store.SetGoalStatus(GoalStatusPaused, GoalActorUser); err != nil {
		t.Fatalf("pause: %v", err)
	}
	goal, transitioned, err := store.CompleteGoalIfActive(active.ID, GoalActorSystem)
	if err != nil || transitioned {
		t.Fatalf("paused guard: goal=%+v transitioned=%v err=%v, want no transition", goal, transitioned, err)
	}
	if store.Meta().Goal.Status != GoalStatusPaused {
		t.Fatalf("paused goal mutated to %q", store.Meta().Goal.Status)
	}

	if _, err := store.SetGoalStatus(GoalStatusActive, GoalActorUser); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, transitioned, err := store.CompleteGoalIfActive("some-other-id", GoalActorSystem); err != nil || transitioned {
		t.Fatalf("id guard: transitioned=%v err=%v, want no transition for mismatched id", transitioned, err)
	}
	if store.Meta().Goal.Status != GoalStatusActive {
		t.Fatalf("active goal mutated to %q after id-mismatch", store.Meta().Goal.Status)
	}

	completed, transitioned, err := store.CompleteGoalIfActive(active.ID, GoalActorSystem)
	if err != nil || !transitioned {
		t.Fatalf("active match: transitioned=%v err=%v, want transition", transitioned, err)
	}
	if completed.Status != GoalStatusComplete || store.Meta().Goal.Status != GoalStatusComplete {
		t.Fatalf("goal not completed: %+v", store.Meta().Goal)
	}

	if _, transitioned, err := store.CompleteGoalIfActive(active.ID, GoalActorSystem); err != nil || transitioned {
		t.Fatalf("already complete: transitioned=%v err=%v, want no second transition", transitioned, err)
	}
}
