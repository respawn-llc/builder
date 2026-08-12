package sqlitegen

import (
	"database/sql"
	"testing"
)

type sqliteOpcode string

const (
	sqliteOpcodeOpenRead      sqliteOpcode = "OpenRead"
	sqliteOpcodeOpenEphemeral sqliteOpcode = "OpenEphemeral"
	sqliteOpcodeVFilter       sqliteOpcode = "VFilter"
	sqliteOpcodeSeekGE        sqliteOpcode = "SeekGE"
	sqliteOpcodeSeekGT        sqliteOpcode = "SeekGT"
	sqliteOpcodeSeekLE        sqliteOpcode = "SeekLE"
	sqliteOpcodeSeekLT        sqliteOpcode = "SeekLT"
	sqliteOpcodeDecrJumpZero  sqliteOpcode = "DecrJumpZero"
	sqliteOpcodeRewind        sqliteOpcode = "Rewind"
	sqliteOpcodeLast          sqliteOpcode = "Last"
	sqliteOpcodeNext          sqliteOpcode = "Next"
	sqliteOpcodePrev          sqliteOpcode = "Prev"
	sqliteOpcodeSorterOpen    sqliteOpcode = "SorterOpen"
	sqliteOpcodeSort          sqliteOpcode = "Sort"
	sqliteOpcodeSorterSort    sqliteOpcode = "SorterSort"
)

type sqliteInstruction struct {
	Address int64
	Opcode  sqliteOpcode
	P1      int64
	P2      int64
}

type QueryInstruction = sqliteInstruction

func requireQueryUsesIndexWithoutSort(t *testing.T, db *sql.DB, query string, indexName string, args ...any) {
	t.Helper()
	requireQueryUsesIndexWithoutOpcodes(
		t,
		db,
		query,
		indexName,
		[]sqliteOpcode{sqliteOpcodeOpenEphemeral, sqliteOpcodeSorterOpen, sqliteOpcodeSort, sqliteOpcodeSorterSort},
		args...,
	)
}

func requireQueryUsesIndexWithoutSorter(t *testing.T, db *sql.DB, query string, indexName string, args ...any) {
	t.Helper()
	requireQueryUsesIndexWithoutOpcodes(
		t,
		db,
		query,
		indexName,
		[]sqliteOpcode{sqliteOpcodeSorterOpen, sqliteOpcodeSort, sqliteOpcodeSorterSort},
		args...,
	)
}

func requireQueryUsesIndexWithoutOpcodes(
	t *testing.T,
	db *sql.DB,
	query string,
	indexName string,
	forbidden []sqliteOpcode,
	args ...any,
) {
	t.Helper()
	instructions := queryProgram(t, db, query, args...)
	requireQueryProgramOpensIndex(t, db, instructions, indexName)
	for _, instruction := range instructions {
		for _, opcode := range forbidden {
			if instruction.Opcode == opcode {
				t.Fatalf("query program used forbidden opcode %s: %+v", opcode, instructions)
			}
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
		instructions = append(instructions, sqliteInstruction{Address: address, Opcode: sqliteOpcode(opcode), P1: p1, P2: p2})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query program: %v", err)
	}
	return instructions
}

func QueryProgram(t *testing.T, db *sql.DB, query string, args ...any) []QueryInstruction {
	t.Helper()
	return queryProgram(t, db, query, args...)
}

func RequireProgramVirtualTableDrivesIndexPointSeeks(t *testing.T, db *sql.DB, instructions []QueryInstruction, indexName string) {
	t.Helper()
	cursor := queryProgramIndexCursor(t, db, instructions, indexName)
	vfilterAddress := int64(-1)
	pointSeeked := false
	for _, instruction := range instructions {
		if instruction.Opcode == sqliteOpcodeVFilter && vfilterAddress < 0 {
			vfilterAddress = instruction.Address
		}
		if instruction.P1 != cursor {
			continue
		}
		if instruction.Opcode == sqliteOpcodeRewind || instruction.Opcode == sqliteOpcodeLast {
			t.Fatalf("query program scans index %q cursor %d from an endpoint", indexName, cursor)
		}
		if vfilterAddress >= 0 && instruction.Address > vfilterAddress && isSeekOpcode(instruction.Opcode) {
			pointSeeked = true
		}
	}
	if !pointSeeked {
		t.Fatalf("query program does not drive index %q point seeks from a virtual table", indexName)
	}
}

func RequireProgramIndexSeekStopsAfterFirstRow(t *testing.T, db *sql.DB, instructions []QueryInstruction, indexName string) {
	t.Helper()
	cursor := queryProgramIndexCursor(t, db, instructions, indexName)
	seeked := false
	for index, instruction := range instructions {
		if instruction.P1 != cursor {
			continue
		}
		seeked = seeked || isSeekOpcode(instruction.Opcode)
		if instruction.Opcode != sqliteOpcodeNext && instruction.Opcode != sqliteOpcodePrev {
			continue
		}
		if index == 0 {
			t.Fatalf("query program loops over index %q without a one-row guard", indexName)
		}
		guard := instructions[index-1]
		if guard.Opcode != sqliteOpcodeDecrJumpZero || guard.P2 <= instruction.Address {
			t.Fatalf("query program loops over index %q without a one-row guard", indexName)
		}
	}
	if !seeked {
		t.Fatalf("query program does not seek index %q", indexName)
	}
}

func RequireProgramIndexSeekWithoutSorter(t *testing.T, db *sql.DB, instructions []QueryInstruction, indexName string) {
	t.Helper()
	cursor := queryProgramIndexCursor(t, db, instructions, indexName)
	seeked := false
	for _, instruction := range instructions {
		seeked = seeked || instruction.P1 == cursor && isSeekOpcode(instruction.Opcode)
		switch instruction.Opcode {
		case sqliteOpcodeSorterOpen, sqliteOpcodeSort, sqliteOpcodeSorterSort:
			t.Fatalf("query program uses sorter opcode %s", instruction.Opcode)
		}
	}
	if !seeked {
		t.Fatalf("query program does not seek index %q", indexName)
	}
}

func queryProgramIndexCursor(t *testing.T, db *sql.DB, instructions []QueryInstruction, indexName string) int64 {
	t.Helper()
	var rootPage int64
	if err := db.QueryRow(
		`SELECT rootpage FROM sqlite_schema WHERE type = 'index' AND name = ?`,
		indexName,
	).Scan(&rootPage); err != nil {
		t.Fatalf("resolve index %q root page: %v", indexName, err)
	}
	for _, instruction := range instructions {
		if instruction.Opcode == sqliteOpcodeOpenRead && instruction.P2 == rootPage {
			return instruction.P1
		}
	}
	t.Fatalf("query program does not open index %q", indexName)
	return 0
}

func isSeekOpcode(opcode sqliteOpcode) bool {
	switch opcode {
	case sqliteOpcodeSeekGE, sqliteOpcodeSeekGT, sqliteOpcodeSeekLE, sqliteOpcodeSeekLT:
		return true
	default:
		return false
	}
}

func closeQueryRows(t *testing.T, rows *sql.Rows) {
	t.Helper()
	if err := rows.Close(); err != nil {
		t.Fatalf("close query rows: %v", err)
	}
}

func requireQueryProgramOpensIndex(t *testing.T, db *sql.DB, instructions []sqliteInstruction, indexName string) {
	t.Helper()
	_ = queryProgramIndexCursor(t, db, instructions, indexName)
}
