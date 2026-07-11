package runtimeactivity

import (
	"testing"

	"core/shared/clientui"
)

func TestResolveRuntimeActivityUsesOnlyLiveResolverInputs(t *testing.T) {
	tests := []struct {
		name     string
		snapshot ResolverSnapshot
		want     clientui.RuntimeActivityState
		wantKind clientui.RuntimeActivityActiveKind
		active   bool
	}{
		{
			name:     "no runtime entry unavailable",
			snapshot: ResolverSnapshot{},
			want:     clientui.RuntimeActivityUnavailable,
		},
		{
			name:     "registered idle",
			snapshot: ResolverSnapshot{Registry: RegistrySnapshot{Registered: true, QueueAccepting: true}},
			want:     clientui.RuntimeActivityRegisteredIdle,
		},
		{
			name: "running copies runtime-owned active kind",
			snapshot: ResolverSnapshot{
				Registry: RegistrySnapshot{Registered: true, QueueAccepting: true},
				Active:   &ActiveStepSnapshot{RunID: "run-1", StepID: "step-1", ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop},
			},
			want:     clientui.RuntimeActivityRunning,
			wantKind: clientui.RuntimeActivityActiveKindGoalLoop,
			active:   true,
		},
		{
			name:     "draining masks active details",
			snapshot: ResolverSnapshot{Registry: RegistrySnapshot{Registered: true, Draining: true}},
			want:     clientui.RuntimeActivityDraining,
			active:   true,
		},
		{
			name:     "closing masks active details",
			snapshot: ResolverSnapshot{Registry: RegistrySnapshot{Registered: true, Closing: true}},
			want:     clientui.RuntimeActivityClosing,
			active:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activity, err := ResolveRuntimeActivity(tt.snapshot)
			if err != nil {
				t.Fatalf("ResolveRuntimeActivity: %v", err)
			}
			if activity.State != tt.want {
				t.Fatalf("state = %q, want %q", activity.State, tt.want)
			}
			if activity.ActiveKind != tt.wantKind {
				t.Fatalf("active kind = %q, want %q", activity.ActiveKind, tt.wantKind)
			}
			if activity.ActiveForControl() != tt.active {
				t.Fatalf("active = %t, want %t", activity.ActiveForControl(), tt.active)
			}
		})
	}
}

func TestResolveRuntimeActivityCopiesEveryRuntimeOwnedActiveKind(t *testing.T) {
	for _, kind := range []clientui.RuntimeActivityActiveKind{
		clientui.RuntimeActivityActiveKindUserTurn,
		clientui.RuntimeActivityActiveKindWorkflowTurn,
		clientui.RuntimeActivityActiveKindGoalLoop,
		clientui.RuntimeActivityActiveKindCompaction,
		clientui.RuntimeActivityActiveKindPreSubmitCompaction,
		clientui.RuntimeActivityActiveKindUserShell,
		clientui.RuntimeActivityActiveKindBackground,
		clientui.RuntimeActivityActiveKindRuntimeMaintenance,
	} {
		t.Run(string(kind), func(t *testing.T) {
			activity, err := ResolveRuntimeActivity(ResolverSnapshot{
				Registry: RegistrySnapshot{Registered: true, QueueAccepting: true},
				Active:   &ActiveStepSnapshot{RunID: "run-1", StepID: "step-1", ActiveKind: kind},
			})
			if err != nil {
				t.Fatalf("ResolveRuntimeActivity: %v", err)
			}
			if activity.State != clientui.RuntimeActivityRunning || activity.ActiveKind != kind {
				t.Fatalf("activity = %+v, want running %q", activity, kind)
			}
			if !activity.ActiveForControl() {
				t.Fatalf("activity must block runtime control while active: %+v", activity)
			}
		})
	}
}

func TestResolveRuntimeActivityProjectsPromptWaitFromExplicitFact(t *testing.T) {
	activity, err := ResolveRuntimeActivity(ResolverSnapshot{
		Registry:   RegistrySnapshot{Registered: true},
		Active:     &ActiveStepSnapshot{RunID: "run-1", StepID: "step-1", ActiveKind: clientui.RuntimeActivityActiveKindUserTurn},
		PromptWait: true,
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeActivity: %v", err)
	}
	if activity.State != clientui.RuntimeActivityAwaitingPrompt || activity.ActiveKind != clientui.RuntimeActivityActiveKindUserTurn {
		t.Fatalf("activity = %+v, want awaiting prompt on active runtime step", activity)
	}
}

func TestResolveRuntimeActivityKeepsPassiveQueuedFactsIdle(t *testing.T) {
	activity, err := ResolveRuntimeActivity(ResolverSnapshot{
		Registry: RegistrySnapshot{Registered: true, QueueAccepting: true},
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeActivity: %v", err)
	}
	if activity.State != clientui.RuntimeActivityRegisteredIdle {
		t.Fatalf("passive queued facts must not become active activity, got %+v", activity)
	}

	activity, err = ResolveRuntimeActivity(ResolverSnapshot{
		Registry:            RegistrySnapshot{Registered: true},
		PendingContinuation: PendingContinuationSnapshot{Promoted: true},
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeActivity promoted continuation: %v", err)
	}
	if activity.State != clientui.RuntimeActivityStarting {
		t.Fatalf("promoted continuation activity = %+v, want starting", activity)
	}
}

func TestResolveRuntimeActivityTreatsOpenLiveRunGroupAsBlocking(t *testing.T) {
	activity, err := ResolveRuntimeActivity(ResolverSnapshot{
		Registry:      RegistrySnapshot{Registered: true, QueueAccepting: true},
		LiveRunActive: true,
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeActivity: %v", err)
	}
	if activity.State != clientui.RuntimeActivityDraining || !activity.ActiveForControl() {
		t.Fatalf("activity = %+v, want blocking live-run group projection", activity)
	}
}

func TestCoordinatorResponseSnapshotsUseOneVersionAndPermitResponseOnlyHoles(t *testing.T) {
	cache := NewCoordinatorCache(4)
	first, err := cache.Snapshot("session-1", ResolverSnapshot{Registry: RegistrySnapshot{Registered: true}})
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	hole := cache.Next("session-1")
	second, err := cache.Snapshot("session-1", ResolverSnapshot{Registry: RegistrySnapshot{Registered: true}})
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if first.Version != first.InputReconciliation.Version {
		t.Fatalf("main submodels must share one version, got activity=%+v reconciliation=%+v", first.Version, first.InputReconciliation.Version)
	}
	if !hole.NewerThan(first.Version) || !second.Version.NewerThan(hole) {
		t.Fatalf("versions did not preserve response-only hole ordering: first=%+v hole=%+v second=%+v", first.Version, hole, second.Version)
	}
}

func TestCoordinatorCacheEvictsDormantSessionsWithGenerationRollover(t *testing.T) {
	cache := NewCoordinatorCache(1)
	first := cache.Next("session-1")
	other := cache.Next("session-2")
	second := cache.Next("session-1")
	if first.Epoch != second.Epoch {
		t.Fatalf("same cache epoch changed: first=%+v second=%+v", first, second)
	}
	if second.Generation == first.Generation {
		t.Fatalf("recreated coordinator must receive a new generation, first=%+v second=%+v other=%+v", first, second, other)
	}
	if cache.IsCurrent("session-1", first) {
		t.Fatalf("old generation event must be rejected after eviction/recreate")
	}
	if !cache.IsCurrent("session-1", second) {
		t.Fatalf("current generation event was rejected: %+v", second)
	}
}
