package metadata

import (
	"context"
	"testing"
)

func TestListMetadataSchemaDefinitionsUsesDependencyOrder(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open metadata store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rows, err := store.Queries().ListMetadataSchemaDefinitions(context.Background())
	if err != nil {
		t.Fatalf("list metadata schema definitions: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("metadata schema definitions are empty")
	}

	kindRank := map[string]int{
		"table":   0,
		"view":    1,
		"index":   2,
		"trigger": 3,
	}
	previousRank := -1
	previousName := ""
	for _, row := range rows {
		if row.ObjectName == "sqlite_sequence" {
			t.Fatal("sqlite_sequence was included in metadata schema definitions")
		}
		rank, ok := kindRank[row.ObjectKind]
		if !ok {
			t.Fatalf("unexpected schema object kind %q", row.ObjectKind)
		}
		if rank < previousRank || (rank == previousRank && row.ObjectName < previousName) {
			t.Fatalf(
				"schema definitions are not ordered by kind and name: %s/%s follows rank=%d name=%q",
				row.ObjectKind,
				row.ObjectName,
				previousRank,
				previousName,
			)
		}
		previousRank = rank
		previousName = row.ObjectName
	}
}
