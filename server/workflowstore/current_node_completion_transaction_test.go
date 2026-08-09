package workflowstore

import (
	"database/sql"
	"errors"
	"testing"
)

func TestCurrentNodeCompletionTransactionScopesBusyTimeoutToItsConnection(t *testing.T) {
	for _, test := range []struct {
		name   string
		finish func(*currentNodeCompletionTransaction) error
	}{
		{name: "commit", finish: func(tx *currentNodeCompletionTransaction) error { return tx.Commit(t.Context()) }},
		{name: "rollback", finish: func(tx *currentNodeCompletionTransaction) error { return tx.Rollback() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, store, _ := newTestStoreContext(t)
			store.db.SetMaxOpenConns(1)
			store.db.SetMaxIdleConns(1)

			tx, err := beginCurrentNodeCompletionTransaction(t.Context(), store.db)
			if err != nil {
				t.Fatalf("begin current node completion transaction: %v", err)
			}
			var transactionBusyTimeout int64
			if err := tx.connection.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&transactionBusyTimeout); err != nil {
				t.Fatalf("read transaction busy timeout: %v", err)
			}
			if transactionBusyTimeout != 15000 {
				t.Fatalf("transaction busy timeout = %d, want 15000", transactionBusyTimeout)
			}
			if err := test.finish(tx); err != nil {
				t.Fatalf("%s current node completion transaction: %v", test.name, err)
			}

			connection, err := store.db.Conn(t.Context())
			if err != nil {
				t.Fatalf("acquire metadata connection after %s: %v", test.name, err)
			}
			defer func() { _ = connection.Close() }()
			var pooledBusyTimeout int64
			if err := connection.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&pooledBusyTimeout); err != nil {
				t.Fatalf("read pooled busy timeout: %v", err)
			}
			if pooledBusyTimeout != 5000 {
				t.Fatalf("pooled busy timeout = %d, want 5000", pooledBusyTimeout)
			}
		})
	}
}

func TestDiscardCurrentNodeCompletionConnectionPreventsPoolReuse(t *testing.T) {
	_, store, _ := newTestStoreContext(t)
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)

	connection, err := store.db.Conn(t.Context())
	if err != nil {
		t.Fatalf("acquire metadata connection: %v", err)
	}
	if err := discardCurrentNodeCompletionConnection(connection); err != nil {
		t.Fatalf("discard metadata connection: %v", err)
	}
	if err := connection.PingContext(t.Context()); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("discarded connection ping error = %v, want sql.ErrConnDone", err)
	}

	replacement, err := store.db.Conn(t.Context())
	if err != nil {
		t.Fatalf("acquire replacement metadata connection: %v", err)
	}
	defer func() { _ = replacement.Close() }()
	var busyTimeout int64
	if err := replacement.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("read replacement busy timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("replacement busy timeout = %d, want 5000", busyTimeout)
	}
}
