package metadata

import (
	"context"
	"errors"
	"testing"
)

func TestProjectLifecycleTransitionRequiresCurrentActiveGeneration(t *testing.T) {
	ctx := context.Background()
	store, _, binding := newMetadataTestStore(t)

	initial, err := store.GetProjectLifecycle(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("GetProjectLifecycle initial: %v", err)
	}
	if initial.State != ProjectLifecycleStateActive || initial.Generation <= 0 {
		t.Fatalf("initial lifecycle = %+v, want active positive generation", initial)
	}
	if err := store.RecheckProjectActiveLifecycle(ctx, binding.ProjectID, initial.Generation); err != nil {
		t.Fatalf("RecheckProjectActiveLifecycle: %v", err)
	}

	deleting, err := store.TransitionProjectLifecycleToDeleting(ctx, binding.ProjectID, initial.Generation)
	if err != nil {
		t.Fatalf("TransitionProjectLifecycleToDeleting: %v", err)
	}
	if deleting.State != ProjectLifecycleStateDeleting || deleting.Generation != initial.Generation+1 {
		t.Fatalf("deleting lifecycle = %+v, want deleting generation %d", deleting, initial.Generation+1)
	}
	if err := store.RecheckProjectActiveLifecycle(ctx, binding.ProjectID, initial.Generation); !errors.Is(err, ErrProjectLifecycleConflict) {
		t.Fatalf("RecheckProjectActiveLifecycle after transition error = %v, want %v", err, ErrProjectLifecycleConflict)
	}
	if _, err := store.TransitionProjectLifecycleToDeleting(ctx, binding.ProjectID, initial.Generation); !errors.Is(err, ErrProjectLifecycleConflict) {
		t.Fatalf("stale transition error = %v, want %v", err, ErrProjectLifecycleConflict)
	}

	current, err := store.GetProjectLifecycle(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("GetProjectLifecycle current: %v", err)
	}
	if current != deleting {
		t.Fatalf("current lifecycle = %+v, want %+v", current, deleting)
	}
}
