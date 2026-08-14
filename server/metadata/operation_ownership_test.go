package metadata

import (
	"context"
	"testing"

	"core/server/metadata/sqlitegen"
)

func TestGeneratedQueryOwnershipModesDoNotNest(t *testing.T) {
	t.Parallel()

	store := openInMemoryMetadataTestStore(t, t.TempDir())
	store.queries = sqlitegen.New(monitoredDBTX{DBTX: store.db, monitor: store})
	if !store.Queries().IsMonitored() {
		t.Fatal("nontransaction generated adapter is not monitored")
	}
	transaction, err := store.BeginTransaction(context.Background(), "ownership guard", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !transaction.Queries().IsRaw() || transaction.Queries().IsMonitored() {
		t.Fatal("transaction generated adapter must be raw and unmonitored")
	}
	var settleErr error
	transaction.Settle(context.Background(), &settleErr)
	if settleErr != nil {
		t.Fatal(settleErr)
	}
}
