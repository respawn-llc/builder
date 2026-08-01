package sqlitegen

import (
	"testing"

	"core/shared/labelcontract"
)

func TestLabelFoldFunctionUsesUnicodeCaseFolding(t *testing.T) {
	db := openSQLiteFixture(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })

	var got string
	if err := db.QueryRow(
		"SELECT "+LabelFoldFunctionName+"(?)",
		"Éclair",
	).Scan(&got); err != nil {
		t.Fatalf("query label fold function: %v", err)
	}
	if want := labelcontract.Fold("éclair"); got != want {
		t.Fatalf("label fold = %q, want %q", got, want)
	}
}
