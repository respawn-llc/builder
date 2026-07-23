package clientui

import "testing"

func TestRunLifecycleTransitionTable(t *testing.T) {
	tests := []struct {
		name        string
		lifecycle   RunLifecycle
		wantRunning bool
		wantGoal    bool
	}{
		{name: "run start", lifecycle: MustRunLifecycle(RunLifecycleRunning, RunModeTurn), wantRunning: true},
		{name: "goal start", lifecycle: MustRunLifecycle(RunLifecycleRunning, RunModeGoalLoop), wantRunning: true, wantGoal: true},
		{name: "run finish", lifecycle: MustRunLifecycle(RunLifecycleFinished, RunModeTurn)},
		{name: "interrupt", lifecycle: MustRunLifecycle(RunLifecycleFinished, RunModeTurn)},
		{name: "panic failure", lifecycle: MustRunLifecycle(RunLifecycleFinished, RunModeTurn)},
		{name: "idle hydration", lifecycle: IdleRunLifecycle()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.lifecycle.Validate(); err != nil {
				t.Fatalf("valid lifecycle rejected: %v", err)
			}
			if tt.lifecycle.IsRunning() != tt.wantRunning {
				t.Fatalf("running = %t, want %t", tt.lifecycle.IsRunning(), tt.wantRunning)
			}
			if tt.lifecycle.IsGoalLoopRunning() != tt.wantGoal {
				t.Fatalf("goal running = %t, want %t", tt.lifecycle.IsGoalLoopRunning(), tt.wantGoal)
			}
		})
	}
}

func TestLifecycleConstructorsRejectImpossibleCombinations(t *testing.T) {
	if _, err := NewRunLifecycle(RunLifecycleIdle, RunModeGoalLoop); err == nil {
		t.Fatal("expected idle goal-loop run lifecycle to be rejected")
	}
	if _, err := NewReviewerLifecycle(false, true); err == nil {
		t.Fatal("expected blocking idle reviewer lifecycle to be rejected")
	}
}

func TestRuntimeSubLifecycleTransitionTables(t *testing.T) {
	compaction := NewCompactionLifecycle(false)
	if compaction.IsRunning() {
		t.Fatal("expected compaction idle")
	}
	compaction = NewCompactionLifecycle(true)
	if !compaction.IsRunning() {
		t.Fatal("expected compaction running after start")
	}
	compaction = NewCompactionLifecycle(false)
	if compaction.IsRunning() {
		t.Fatal("expected compaction idle after complete or fail")
	}

	reviewer, err := NewReviewerLifecycle(true, true)
	if err != nil {
		t.Fatalf("reviewer start rejected: %v", err)
	}
	if !reviewer.IsRunning() || !reviewer.IsBlocking() {
		t.Fatalf("reviewer start = %q, want running blocking", reviewer)
	}
	reviewer, err = NewReviewerLifecycle(false, false)
	if err != nil {
		t.Fatalf("reviewer completion rejected: %v", err)
	}
	if reviewer.IsRunning() || reviewer.IsBlocking() {
		t.Fatalf("reviewer complete = %q, want idle", reviewer)
	}

	connection := NewRuntimeConnectionLifecycle(true)
	if !connection.IsDisconnected() {
		t.Fatal("expected controller reconnect loss to mark disconnected")
	}
	connection = NewRuntimeConnectionLifecycle(false)
	if connection.IsDisconnected() {
		t.Fatal("expected hydration/reconnect recovery to mark connected")
	}
}
