package metadata

import (
	"testing"

	"core/internal/testharness/databaseseed"
)

func installMetadataTestDatabase(t *testing.T, persistenceRoot string) {
	t.Helper()
	if err := databaseseed.MaterializeCurrentMetadataDatabase(persistenceRoot, Open); err != nil {
		t.Fatalf("materialize metadata test database fixture: %v", err)
	}
}
