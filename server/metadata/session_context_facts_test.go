package metadata

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"core/server/session"
	"core/shared/sessioncontract"
)

func TestSessionContextFactsPersistIndependentlyFromFullSnapshots(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := openInMemoryMetadataTestStore(t, root)
	workspaceRoot := root + "/workspace"
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll workspace: %v", err)
	}
	workspace, err := store.RegisterWorkspaceBinding(ctx, workspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	options := store.AuthoritativeSessionStoreOptions()
	sessionStore, err := session.Create(
		workspace.CanonicalRoot,
		"workspace",
		workspace.CanonicalRoot,
		sessioncontract.SessionCategoryMain,
		options...,
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := sessionStore.SetSessionContextFacts(4, true); err != nil {
		t.Fatalf("SetSessionContextFacts: %v", err)
	}
	if err := sessionStore.SetName("ordinary snapshot"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	reopened, err := session.OpenByID(root, sessionStore.Meta().SessionID, options...)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}
	facts := reopened.ContextFacts()
	if facts.CompletedCompactionCount == nil || *facts.CompletedCompactionCount != 4 ||
		facts.ManualCompactEligible == nil || !*facts.ManualCompactEligible {
		t.Fatalf("reopened Context facts = %+v", facts)
	}

	var count sql.NullInt64
	var eligible sql.NullInt64
	if err := store.DB().QueryRowContext(ctx, `
SELECT completed_compaction_count, manual_compact_eligible
FROM sessions
WHERE id = ?`, sessionStore.Meta().SessionID).Scan(&count, &eligible); err != nil {
		t.Fatalf("query Context facts: %v", err)
	}
	if !count.Valid || count.Int64 != 4 || !eligible.Valid || eligible.Int64 != 1 {
		t.Fatalf("stored Context facts = count:%+v eligible:%+v", count, eligible)
	}
}

func TestOlderSessionContextFactsRemainAbsent(t *testing.T) {
	root := t.TempDir()
	store := openInMemoryMetadataTestStore(t, root)
	workspaceRoot := root + "/workspace"
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll workspace: %v", err)
	}
	workspace, err := store.RegisterWorkspaceBinding(context.Background(), workspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	options := store.AuthoritativeSessionStoreOptions()
	sessionStore, err := session.Create(
		workspace.CanonicalRoot,
		"workspace",
		workspace.CanonicalRoot,
		sessioncontract.SessionCategoryMain,
		options...,
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.DB().ExecContext(
		context.Background(),
		`UPDATE sessions SET completed_compaction_count = NULL, manual_compact_eligible = NULL WHERE id = ?`,
		sessionStore.Meta().SessionID,
	); err != nil {
		t.Fatalf("clear Context facts: %v", err)
	}

	reopened, err := session.OpenByID(root, sessionStore.Meta().SessionID, options...)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}
	facts := reopened.ContextFacts()
	if facts.CompletedCompactionCount != nil || facts.ManualCompactEligible != nil {
		t.Fatalf("older Session Context facts = %+v, want absent", facts)
	}
}

func TestSessionContextFactWriterRejectsMissingSession(t *testing.T) {
	store := openInMemoryMetadataTestStore(t, t.TempDir())
	count := 1
	eligible := true
	err := store.WriteSessionContextFacts(context.Background(), "missing", session.SessionContextFacts{
		CompletedCompactionCount: &count,
		ManualCompactEligible:    &eligible,
	})
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("WriteSessionContextFacts error = %v, want Session not found", err)
	}
}

func TestFailedContextWriteIsNotPublishedByLaterFullSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := openInMemoryMetadataTestStore(t, root)
	workspaceRoot := root + "/workspace"
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll workspace: %v", err)
	}
	workspace, err := store.RegisterWorkspaceBinding(ctx, workspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	options := store.AuthoritativeSessionStoreOptions()
	sessionStore, err := session.Create(
		workspace.CanonicalRoot,
		"workspace",
		workspace.CanonicalRoot,
		sessioncontract.SessionCategoryMain,
		options...,
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.DB().ExecContext(
		ctx,
		`DROP TRIGGER IF EXISTS fail_context_write;
CREATE TRIGGER fail_context_write
BEFORE UPDATE OF manual_compact_eligible ON sessions
BEGIN
    SELECT RAISE(FAIL, 'forced Context write failure');
END;`,
	); err != nil {
		t.Fatalf("install failing trigger: %v", err)
	}
	if err := sessionStore.SetManualCompactEligibility(true); err == nil {
		t.Fatal("SetManualCompactEligibility succeeded despite trigger")
	}
	if _, err := store.DB().ExecContext(ctx, `DROP TRIGGER fail_context_write`); err != nil {
		t.Fatalf("drop failing trigger: %v", err)
	}
	if err := sessionStore.SetName("later full snapshot"); err != nil {
		t.Fatalf("SetName: %v", err)
	}

	var eligible sql.NullInt64
	if err := store.DB().QueryRowContext(
		ctx,
		`SELECT manual_compact_eligible FROM sessions WHERE id = ?`,
		sessionStore.Meta().SessionID,
	).Scan(&eligible); err != nil {
		t.Fatalf("query eligibility: %v", err)
	}
	if !eligible.Valid || eligible.Int64 != 0 {
		t.Fatalf("later full snapshot published failed eligibility: %+v", eligible)
	}
}
