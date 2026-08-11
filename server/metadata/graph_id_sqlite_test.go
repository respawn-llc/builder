package metadata

import (
	"bytes"
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

const sqliteTestGraphEntityID = "12345678-1234-4234-9234-123456789abc"

func TestGraphEntityIDSQLiteFunctionsRoundTripScalarValues(t *testing.T) {
	db := openGraphEntityIDSQLiteTestDatabase(t)

	var blob []byte
	if err := db.QueryRow(
		`SELECT `+graphEntityIDBlobFunction+`(?)`,
		sqliteTestGraphEntityID,
	).Scan(&blob); err != nil {
		t.Fatalf("convert graph entity ID text to BLOB: %v", err)
	}
	expected := uuid.MustParse(sqliteTestGraphEntityID)
	if !bytes.Equal(blob, expected[:]) {
		t.Fatalf("graph entity ID BLOB = %x, want %x", blob, expected[:])
	}

	var text string
	if err := db.QueryRow(
		`SELECT `+graphEntityIDTextFunction+`(?)`,
		blob,
	).Scan(&text); err != nil {
		t.Fatalf("convert graph entity ID BLOB to text: %v", err)
	}
	if text != sqliteTestGraphEntityID {
		t.Fatalf("graph entity ID text = %q, want %q", text, sqliteTestGraphEntityID)
	}
}

func TestGraphEntityIDSQLiteFunctionsRejectInvalidScalarValues(t *testing.T) {
	db := openGraphEntityIDSQLiteTestDatabase(t)

	for name, value := range map[string]any{
		"blank":         "",
		"padded":        " " + sqliteTestGraphEntityID,
		"noncanonical":  "12345678-1234-4234-9234-123456789ABC",
		"non-v4":        "12345678-1234-1234-9234-123456789abc",
		"wrong variant": "12345678-1234-4234-1234-123456789abc",
		"zero":          "00000000-0000-0000-0000-000000000000",
		"BLOB":          []byte(sqliteTestGraphEntityID),
	} {
		t.Run("text-to-BLOB/"+name, func(t *testing.T) {
			var output []byte
			if err := db.QueryRow(
				`SELECT `+graphEntityIDBlobFunction+`(?)`,
				value,
			).Scan(&output); err == nil {
				t.Fatalf("%s accepted %T(%v)", graphEntityIDBlobFunction, value, value)
			}
		})
	}

	for name, value := range map[string]any{
		"TEXT":         sqliteTestGraphEntityID,
		"short BLOB":   []byte{1, 2, 3},
		"text BLOB":    []byte(sqliteTestGraphEntityID),
		"non-v4 BLOB":  graphUUIDBytes("12345678-1234-1234-9234-123456789abc"),
		"variant BLOB": graphUUIDBytes("12345678-1234-4234-1234-123456789abc"),
		"zero BLOB":    make([]byte, 16),
	} {
		t.Run("BLOB-to-text/"+name, func(t *testing.T) {
			var output string
			if err := db.QueryRow(
				`SELECT `+graphEntityIDTextFunction+`(?)`,
				value,
			).Scan(&output); err == nil {
				t.Fatalf("%s accepted %T(%v)", graphEntityIDTextFunction, value, value)
			}
		})
	}
}

func openGraphEntityIDSQLiteTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	if err := registerMetadataSQLiteFunctions(); err != nil {
		t.Fatalf("register metadata SQLite functions: %v", err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open isolated SQLite database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func graphUUIDBytes(raw string) []byte {
	value := uuid.MustParse(raw)
	return value[:]
}
