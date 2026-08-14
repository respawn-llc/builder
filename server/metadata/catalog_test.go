package metadata

import (
	"context"
	"testing"
)

func TestListMetadataSchemaDefinitionsUsesDependencyOrder(t *testing.T) {
	t.Parallel()
	store := openInMemoryMetadataTestStore(t, t.TempDir())

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
	type catalogPosition struct {
		rank int
		name string
	}
	var previous *catalogPosition
	for _, row := range rows {
		if row.ObjectName == "sqlite_sequence" {
			t.Fatal("sqlite_sequence was included in metadata schema definitions")
		}
		rank, ok := kindRank[row.ObjectKind]
		if !ok {
			t.Fatalf("unexpected schema object kind %q", row.ObjectKind)
		}
		current := catalogPosition{rank: rank, name: row.ObjectName}
		if previous != nil && (current.rank < previous.rank || (current.rank == previous.rank && current.name < previous.name)) {
			t.Fatalf(
				"schema definitions are not ordered by kind and name: %s/%s follows rank=%d name=%q",
				row.ObjectKind,
				row.ObjectName,
				previous.rank,
				previous.name,
			)
		}
		previous = &current
	}
}
