package metadata

import (
	"context"
	"testing"
)

func TestResolveOptionalSessionExecutionTargetAllowsUnlinkedSession(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	session := createMetadataTestSession(t, store, cfg, binding)
	if _, err := store.db.ExecContext(ctx,
		"UPDATE sessions SET workspace_id = NULL, worktree_id = NULL, cwd_relpath = '.' WHERE id = ?",
		session.Meta().SessionID,
	); err != nil {
		t.Fatalf("clear session workspace binding: %v", err)
	}
	target, err := store.ResolveOptionalSessionExecutionTarget(ctx, session.Meta().SessionID)
	if err != nil {
		t.Fatalf("resolve optional target: %v", err)
	}
	if target != nil {
		t.Fatalf("unlinked session target = %+v, want nil", target)
	}
}
