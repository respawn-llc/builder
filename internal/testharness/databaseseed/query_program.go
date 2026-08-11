package databaseseed

import (
	"database/sql"
	"testing"
)

type Opcode string

const (
	OpcodeOpenRead      Opcode = "OpenRead"
	OpcodeOpenEphemeral Opcode = "OpenEphemeral"
	OpcodeVFilter       Opcode = "VFilter"
	OpcodeRewind        Opcode = "Rewind"
	OpcodeNext          Opcode = "Next"
	OpcodePrev          Opcode = "Prev"
	OpcodeSorterOpen    Opcode = "SorterOpen"
	OpcodeSort          Opcode = "Sort"
	OpcodeSorterSort    Opcode = "SorterSort"
)

type Instruction struct {
	Opcode Opcode
	P1     int64
	P2     int64
}

func Program(t testing.TB, db *sql.DB, query string, args ...any) []Instruction {
	t.Helper()
	rows, err := db.Query("EXPLAIN "+query, args...)
	if err != nil {
		t.Fatalf("explain query program: %v", err)
	}
	defer CloseRows(t, rows)

	var instructions []Instruction
	for rows.Next() {
		var address, p1, p2, p3, p5 int64
		var opcode string
		var p4, comment sql.NullString
		if err := rows.Scan(&address, &opcode, &p1, &p2, &p3, &p4, &p5, &comment); err != nil {
			t.Fatalf("scan query program: %v", err)
		}
		instructions = append(instructions, Instruction{
			Opcode: Opcode(opcode),
			P1:     p1,
			P2:     p2,
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query program: %v", err)
	}
	return instructions
}

func RequireUsesIndexWithoutSort(
	t testing.TB,
	db *sql.DB,
	query string,
	indexName string,
	args ...any,
) {
	t.Helper()
	RequireUsesIndexWithoutOpcodes(
		t,
		db,
		query,
		indexName,
		[]Opcode{OpcodeOpenEphemeral, OpcodeSorterOpen, OpcodeSort, OpcodeSorterSort},
		args...,
	)
}

func RequireUsesIndexWithoutSorter(
	t testing.TB,
	db *sql.DB,
	query string,
	indexName string,
	args ...any,
) {
	t.Helper()
	RequireUsesIndexWithoutOpcodes(
		t,
		db,
		query,
		indexName,
		[]Opcode{OpcodeSorterOpen, OpcodeSort, OpcodeSorterSort},
		args...,
	)
}

func RequireUsesIndexWithoutOpcodes(
	t testing.TB,
	db *sql.DB,
	query string,
	indexName string,
	forbidden []Opcode,
	args ...any,
) {
	t.Helper()
	instructions := Program(t, db, query, args...)
	RequireProgramOpensIndex(t, db, instructions, indexName)
	RequireProgramExcludesOpcodes(t, instructions, forbidden...)
}

func RequireUsesIndex(t testing.TB, db *sql.DB, query string, indexName string, args ...any) {
	t.Helper()
	RequireProgramOpensIndex(t, db, Program(t, db, query, args...), indexName)
}

func RequireUsesAnyTableIndex(t testing.TB, db *sql.DB, query string, tableName string, args ...any) {
	t.Helper()
	instructions := Program(t, db, query, args...)
	rows, err := db.Query(
		`SELECT rootpage FROM sqlite_schema WHERE type = 'index' AND tbl_name = ?`,
		tableName,
	)
	if err != nil {
		t.Fatalf("resolve indexes for table %q: %v", tableName, err)
	}
	defer CloseRows(t, rows)

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
		if instruction.Opcode == OpcodeOpenRead && rootPages[instruction.P2] {
			return
		}
	}
	t.Fatalf("query program did not open an index for table %q at root pages %v: %+v", tableName, rootPages, instructions)
}

func RequireProgramOpensIndex(
	t testing.TB,
	db *sql.DB,
	instructions []Instruction,
	indexName string,
) {
	t.Helper()
	indexRootPage := indexRootPage(t, db, indexName)
	for _, instruction := range instructions {
		if instruction.Opcode == OpcodeOpenRead && instruction.P2 == indexRootPage {
			return
		}
	}
	t.Fatalf("query program did not open index %q at root page %d: %+v", indexName, indexRootPage, instructions)
}

func RequireProgramDoesNotOpenIndex(
	t testing.TB,
	db *sql.DB,
	instructions []Instruction,
	indexName string,
) {
	t.Helper()
	indexRootPage := indexRootPage(t, db, indexName)
	for _, instruction := range instructions {
		if instruction.Opcode == OpcodeOpenRead && instruction.P2 == indexRootPage {
			t.Fatalf("query program opened forbidden index %q at root page %d: %+v", indexName, indexRootPage, instructions)
		}
	}
}

func RequireProgramContainsOpcode(t testing.TB, instructions []Instruction, opcode Opcode) {
	t.Helper()
	for _, instruction := range instructions {
		if instruction.Opcode == opcode {
			return
		}
	}
	t.Fatalf("query program did not contain opcode %s: %+v", opcode, instructions)
}

func RequireProgramWithoutSorter(t testing.TB, instructions []Instruction) {
	t.Helper()
	RequireProgramExcludesOpcodes(t, instructions, OpcodeSorterOpen, OpcodeSort, OpcodeSorterSort)
}

func RequireProgramExcludesOpcodes(t testing.TB, instructions []Instruction, forbidden ...Opcode) {
	t.Helper()
	for _, instruction := range instructions {
		for _, opcode := range forbidden {
			if instruction.Opcode == opcode {
				t.Fatalf("query program used forbidden opcode %s: %+v", opcode, instructions)
			}
		}
	}
}

func CloseRows(t testing.TB, rows *sql.Rows) {
	t.Helper()
	if err := rows.Close(); err != nil {
		t.Fatalf("close query rows: %v", err)
	}
}

func indexRootPage(t testing.TB, db *sql.DB, indexName string) int64 {
	t.Helper()
	var rootPage int64
	if err := db.QueryRow(
		`SELECT rootpage FROM sqlite_schema WHERE type = 'index' AND name = ?`,
		indexName,
	).Scan(&rootPage); err != nil {
		t.Fatalf("resolve index root page: %v", err)
	}
	return rootPage
}
