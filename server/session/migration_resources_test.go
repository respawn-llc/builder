package session

import "testing"

func TestMigrationResourceLedgerEnforcesSpoolFileBudget(t *testing.T) {
	ledger := newMigrationResourceLedger()
	releases := make([]func(), 0, migrationMaxOpenSpoolFiles)
	for index := 0; index < migrationMaxOpenSpoolFiles; index++ {
		release, err := ledger.acquireSpoolFile()
		if err != nil {
			t.Fatalf("acquire spool file %d: %v", index, err)
		}
		releases = append(releases, release)
	}
	if _, err := ledger.acquireSpoolFile(); err == nil {
		t.Fatal("expected spool file budget error")
	}
	for _, release := range releases {
		release()
	}
	stats := ledger.snapshot()
	if stats.OpenSpoolFiles != 0 || stats.MaxOpenSpoolFiles != migrationMaxOpenSpoolFiles {
		t.Fatalf("spool file resource stats = %+v", stats)
	}
}
