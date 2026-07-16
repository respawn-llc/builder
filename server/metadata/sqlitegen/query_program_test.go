package sqlitegen

import (
	"database/sql"
	"testing"
)

type sqliteOpcode string

const (
	sqliteOpcodeOpenRead      sqliteOpcode = "OpenRead"
	sqliteOpcodeOpenEphemeral sqliteOpcode = "OpenEphemeral"
	sqliteOpcodeSorterOpen    sqliteOpcode = "SorterOpen"
	sqliteOpcodeSort          sqliteOpcode = "Sort"
	sqliteOpcodeSorterSort    sqliteOpcode = "SorterSort"
)

type sqliteInstruction struct {
	Opcode sqliteOpcode
	P2     int64
}

func requireQueryUsesIndexWithoutSort(t *testing.T, db *sql.DB, query string, indexName string, args ...any) {
	t.Helper()
	rows, err := db.Query("EXPLAIN "+query, args...)
	if err != nil {
		t.Fatalf("explain query program: %v", err)
	}
	defer rows.Close()
	var instructions []sqliteInstruction
	for rows.Next() {
		var address, p1, p2, p3, p5 int64
		var opcode string
		var p4, comment sql.NullString
		if err := rows.Scan(&address, &opcode, &p1, &p2, &p3, &p4, &p5, &comment); err != nil {
			t.Fatalf("scan query program: %v", err)
		}
		instructions = append(instructions, sqliteInstruction{Opcode: sqliteOpcode(opcode), P2: p2})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query program: %v", err)
	}

	var indexRootPage int64
	if err := db.QueryRow(
		`SELECT rootpage FROM sqlite_schema WHERE type = 'index' AND name = ?`,
		indexName,
	).Scan(&indexRootPage); err != nil {
		t.Fatalf("resolve index root page: %v", err)
	}
	indexOpened := false
	for _, instruction := range instructions {
		if instruction.Opcode == sqliteOpcodeOpenRead && instruction.P2 == indexRootPage {
			indexOpened = true
		}
		switch instruction.Opcode {
		case sqliteOpcodeOpenEphemeral, sqliteOpcodeSorterOpen, sqliteOpcodeSort, sqliteOpcodeSorterSort:
			t.Fatalf("query program opened a temporary sort structure: %+v", instructions)
		}
	}
	if !indexOpened {
		t.Fatalf("query program did not open index %q at root page %d: %+v", indexName, indexRootPage, instructions)
	}
}
