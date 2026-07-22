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
	instructions := queryProgram(t, db, query, args...)
	requireQueryProgramOpensIndex(t, db, instructions, indexName)
	for _, instruction := range instructions {
		switch instruction.Opcode {
		case sqliteOpcodeOpenEphemeral, sqliteOpcodeSorterOpen, sqliteOpcodeSort, sqliteOpcodeSorterSort:
			t.Fatalf("query program opened a temporary sort structure: %+v", instructions)
		}
	}
}

func requireQueryUsesIndex(t *testing.T, db *sql.DB, query string, indexName string, args ...any) {
	t.Helper()
	requireQueryProgramOpensIndex(t, db, queryProgram(t, db, query, args...), indexName)
}

func requireQueryUsesAnyTableIndex(t *testing.T, db *sql.DB, query string, tableName string, args ...any) {
	t.Helper()
	instructions := queryProgram(t, db, query, args...)
	rows, err := db.Query(
		`SELECT rootpage FROM sqlite_schema WHERE type = 'index' AND tbl_name = ?`,
		tableName,
	)
	if err != nil {
		t.Fatalf("resolve indexes for table %q: %v", tableName, err)
	}
	defer closeQueryRows(t, rows)
	rootPages := map[int64]bool{}
	for rows.Next() {
		var rootPage int64
		if err := rows.Scan(&rootPage); err != nil {
			t.Fatalf("scan index root page for table %q: %v", tableName, err)
		}
		rootPages[rootPage] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate indexes for table %q: %v", tableName, err)
	}
	if len(rootPages) == 0 {
		t.Fatalf("table %q has no indexes", tableName)
	}
	for _, instruction := range instructions {
		if instruction.Opcode == sqliteOpcodeOpenRead && rootPages[instruction.P2] {
			return
		}
	}
	t.Fatalf("query program did not open an index for table %q at root pages %v: %+v", tableName, rootPages, instructions)
}

func queryProgram(t *testing.T, db *sql.DB, query string, args ...any) []sqliteInstruction {
	t.Helper()
	rows, err := db.Query("EXPLAIN "+query, args...)
	if err != nil {
		t.Fatalf("explain query program: %v", err)
	}
	defer closeQueryRows(t, rows)
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
	return instructions
}

func closeQueryRows(t *testing.T, rows *sql.Rows) {
	t.Helper()
	if err := rows.Close(); err != nil {
		t.Fatalf("close query rows: %v", err)
	}
}

func requireQueryProgramOpensIndex(t *testing.T, db *sql.DB, instructions []sqliteInstruction, indexName string) {
	t.Helper()
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
	}
	if !indexOpened {
		t.Fatalf("query program did not open index %q at root page %d: %+v", indexName, indexRootPage, instructions)
	}
}
