package metadata

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"core/server/session"

	"github.com/google/uuid"
)

func TestResolveOptionalSessionExecutionTargetAllowsUnlinkedSession(t *testing.T) {
	store, cfg, binding := newMetadataTestStore(t)
	session := createMetadataTestSession(t, store, cfg, binding)
	if _, err := store.db.ExecContext(context.Background(),
		"UPDATE sessions SET workspace_id = NULL, worktree_id = NULL, cwd_relpath = '.' WHERE id = ?",
		session.Meta().SessionID,
	); err != nil {
		t.Fatalf("clear session workspace binding: %v", err)
	}
	target, err := store.ResolveOptionalSessionExecutionTarget(context.Background(), session.Meta().SessionID)
	if err != nil {
		t.Fatalf("resolve optional target: %v", err)
	}
	if target != nil {
		t.Fatalf("unlinked session target = %+v, want nil", target)
	}
}

func TestResolveSessionExecutionTargetMapsMissingSession(t *testing.T) {
	store, _, _ := newMetadataTestStore(t)
	_, err := store.ResolveSessionExecutionTarget(context.Background(), uuid.NewString())
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("resolve missing Session error = %v, want ErrSessionNotFound", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("resolve missing Session leaked sql.ErrNoRows: %v", err)
	}
}
