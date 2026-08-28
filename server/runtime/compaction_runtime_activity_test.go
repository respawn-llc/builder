package runtime

import (
	"context"
	"testing"
)

func TestActiveStepSnapshotProjectsAuthoritativeCompactionActivity(t *testing.T) {
	events := make([]Event, 0, 4)
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(t), Config{
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})

	err := withActiveTestRun(t, engine, ActiveKindUserTurn, func(_ context.Context, stepID string) error {
		baseline := engine.ActiveStepSnapshot()
		if baseline == nil || baseline.ActiveKind != ActiveKindUserTurn {
			t.Fatalf("baseline activity = %+v, want active user turn", baseline)
		}
		persistence := newCompactionPersistence(engine)
		for _, activeKind := range []ActiveKind{ActiveKindCompaction, ActiveKindPreSubmitCompaction} {
			if err := persistence.setActivity(stepID, nil, compactionModeAuto, 1, activeKind, true); err != nil {
				t.Fatalf("set %s activity: %v", activeKind, err)
			}
			active := engine.ActiveStepSnapshot()
			if active == nil ||
				active.RunID != baseline.RunID ||
				active.StepID != stepID ||
				active.ActiveKind != activeKind {
				t.Fatalf("projected %s activity = %+v, baseline %+v", activeKind, active, baseline)
			}
			if err := persistence.setActivity(stepID, nil, compactionModeAuto, 0, activeKind, false); err != nil {
				t.Fatalf("clear %s activity: %v", activeKind, err)
			}
			restored := engine.ActiveStepSnapshot()
			if restored == nil || restored.ActiveKind != ActiveKindUserTurn {
				t.Fatalf("activity after clearing %s = %+v, want restored user turn", activeKind, restored)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("run active user turn: %v", err)
	}

	activityEvents := 0
	for _, event := range events {
		if event.Kind == EventRuntimeActivityChanged {
			activityEvents++
		}
	}
	if activityEvents != 4 {
		t.Fatalf("runtime activity events = %d, want start and clear for both compaction kinds", activityEvents)
	}
}
