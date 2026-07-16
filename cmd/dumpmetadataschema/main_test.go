package main

import (
	"bytes"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"core/server/metadata/sqlitegen"

	_ "modernc.org/sqlite"
)

func TestRunDumpsLatestMetadataSchemaWithoutTouchingConfiguredPersistence(t *testing.T) {
	tempBase := t.TempDir()
	t.Setenv("TMPDIR", tempBase)

	configuredRoot := t.TempDir()
	configuredDatabasePath := filepath.Join(configuredRoot, "db", "main.sqlite3")
	if err := os.MkdirAll(filepath.Dir(configuredDatabasePath), 0o755); err != nil {
		t.Fatalf("create configured database directory: %v", err)
	}
	configuredDatabase := []byte("configured metadata database sentinel")
	if err := os.WriteFile(configuredDatabasePath, configuredDatabase, 0o600); err != nil {
		t.Fatalf("write configured database sentinel: %v", err)
	}
	t.Setenv("KENT_PERSISTENCE_ROOT", configuredRoot)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run(nil, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("run exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if stdout.Len() == 0 {
		t.Fatal("stdout is empty")
	}

	gotConfiguredDatabase, err := os.ReadFile(configuredDatabasePath)
	if err != nil {
		t.Fatalf("read configured database sentinel: %v", err)
	}
	if !bytes.Equal(gotConfiguredDatabase, configuredDatabase) {
		t.Fatal("configured metadata database changed")
	}
	tempArtifacts, err := os.ReadDir(tempBase)
	if err != nil {
		t.Fatalf("read isolated temporary base: %v", err)
	}
	if len(tempArtifacts) != 0 {
		t.Fatalf("temporary artifacts remain: %v", tempArtifacts)
	}

	assertExecutableMetadataSchemaDump(t, stdout.Bytes())
}

func assertExecutableMetadataSchemaDump(t *testing.T, dump []byte) {
	t.Helper()

	reconstructed, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "reconstructed.sqlite3"))
	if err != nil {
		t.Fatalf("open reconstructed database: %v", err)
	}
	t.Cleanup(func() { _ = reconstructed.Close() })
	if _, err := reconstructed.Exec(string(dump)); err != nil {
		t.Fatalf("execute schema dump: %v", err)
	}

	rows, err := reconstructed.Query(`
		SELECT type, name
		FROM sqlite_schema
		WHERE sql IS NOT NULL
	`)
	if err != nil {
		t.Fatalf("query reconstructed catalog: %v", err)
	}
	defer func() { _ = rows.Close() }()

	foundKinds := map[string]bool{}
	foundGooseVersionTable := false
	for rows.Next() {
		var kind string
		var name string
		if err := rows.Scan(&kind, &name); err != nil {
			t.Fatalf("scan reconstructed catalog: %v", err)
		}
		foundKinds[kind] = true
		if kind == "table" && name == "goose_db_version" {
			foundGooseVersionTable = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate reconstructed catalog: %v", err)
	}
	for _, kind := range []string{"table", "view", "index", "trigger"} {
		if !foundKinds[kind] {
			t.Errorf("reconstructed catalog does not contain %s definitions", kind)
		}
	}
	if !foundGooseVersionTable {
		t.Error("reconstructed catalog does not contain goose_db_version")
	}
}

func TestRunProducesByteIdenticalIndependentDumps(t *testing.T) {
	tempBase := t.TempDir()
	t.Setenv("TMPDIR", tempBase)

	var firstStdout bytes.Buffer
	var firstStderr bytes.Buffer
	if exitCode := run(nil, &firstStdout, &firstStderr); exitCode != 0 {
		t.Fatalf("first run exit code = %d, stderr = %q", exitCode, firstStderr.String())
	}

	var secondStdout bytes.Buffer
	var secondStderr bytes.Buffer
	if exitCode := run(nil, &secondStdout, &secondStderr); exitCode != 0 {
		t.Fatalf("second run exit code = %d, stderr = %q", exitCode, secondStderr.String())
	}

	if !bytes.Equal(firstStdout.Bytes(), secondStdout.Bytes()) {
		t.Fatal("independent schema dumps differ")
	}
}

func TestRenderSchemaDefinitionsRejectsUnknownObjectKind(t *testing.T) {
	_, err := renderSchemaDefinitions([]sqlitegen.ListMetadataSchemaDefinitionsRow{{
		ObjectKind: "virtual-table",
		ObjectName: "unexpected",
		Ddl:        "CREATE TABLE unexpected (id INTEGER)",
	}})
	if err == nil {
		t.Fatal("expected unknown object kind error")
	}
}

func TestRenderSchemaDefinitionsRejectsEmptyDDL(t *testing.T) {
	_, err := renderSchemaDefinitions([]sqlitegen.ListMetadataSchemaDefinitionsRow{{
		ObjectKind: "table",
		ObjectName: "missing_definition",
		Ddl:        "",
	}})
	if err == nil {
		t.Fatal("expected empty DDL error")
	}
}

func TestRunHelpSucceedsWithoutCreatingTemporaryDatabase(t *testing.T) {
	tempBase := t.TempDir()
	t.Setenv("TMPDIR", tempBase)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run([]string{"--help"}, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("run help exit code = %d", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("help stdout is not schema-only: %q", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("help did not write usage")
	}
	tempArtifacts, err := os.ReadDir(tempBase)
	if err != nil {
		t.Fatalf("read isolated temporary base: %v", err)
	}
	if len(tempArtifacts) != 0 {
		t.Fatalf("help created temporary artifacts: %v", tempArtifacts)
	}
}

func TestRunRejectsArgumentsWithoutCreatingTemporaryDatabase(t *testing.T) {
	for _, args := range [][]string{
		{"database.sqlite3"},
		{"--output", "schema.sql"},
	} {
		t.Run(args[0], func(t *testing.T) {
			tempBase := t.TempDir()
			t.Setenv("TMPDIR", tempBase)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if exitCode := run(args, &stdout, &stderr); exitCode != 2 {
				t.Fatalf("run exit code = %d, want 2", exitCode)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout is not schema-only: %q", stdout.String())
			}
			if stderr.Len() == 0 {
				t.Fatal("argument rejection did not write usage or an error")
			}
			tempArtifacts, err := os.ReadDir(tempBase)
			if err != nil {
				t.Fatalf("read isolated temporary base: %v", err)
			}
			if len(tempArtifacts) != 0 {
				t.Fatalf("argument rejection created temporary artifacts: %v", tempArtifacts)
			}
		})
	}
}

func TestRunReportsStdoutFailureAfterCleaningTemporaryDatabase(t *testing.T) {
	tempBase := t.TempDir()
	t.Setenv("TMPDIR", tempBase)

	writeCalled := false
	var artifactsAtWrite []os.DirEntry
	var readAtWriteErr error
	stdout := writerFunc(func(_ []byte) (int, error) {
		writeCalled = true
		artifactsAtWrite, readAtWriteErr = os.ReadDir(tempBase)
		return 0, nil
	})
	var stderr bytes.Buffer
	if exitCode := run(nil, stdout, &stderr); exitCode == 0 {
		t.Fatal("run succeeded after a short stdout write")
	}
	if !writeCalled {
		t.Fatal("stdout writer was not called")
	}
	if readAtWriteErr != nil {
		t.Fatalf("read isolated temporary base during stdout write: %v", readAtWriteErr)
	}
	if len(artifactsAtWrite) != 0 {
		t.Fatalf("temporary artifacts existed during stdout write: %v", artifactsAtWrite)
	}
	if stderr.Len() == 0 {
		t.Fatal("stdout failure did not write an error")
	}
	tempArtifacts, err := os.ReadDir(tempBase)
	if err != nil {
		t.Fatalf("read isolated temporary base after stdout write: %v", err)
	}
	if len(tempArtifacts) != 0 {
		t.Fatalf("temporary artifacts remain after stdout failure: %v", tempArtifacts)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) {
	return f(p)
}

func TestMetadataSchemaDumpScriptWorksOutsideRepository(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	scriptPath := filepath.Join(repositoryRoot, "scripts", "dump-metadata-schema.sh")

	shadowBin := t.TempDir()
	shadowSQLite := filepath.Join(shadowBin, "sqlite3")
	if err := os.WriteFile(shadowSQLite, []byte("#!/bin/sh\nexit 97\n"), 0o755); err != nil {
		t.Fatalf("write sqlite3 shadow: %v", err)
	}

	command := exec.Command(scriptPath)
	command.Dir = t.TempDir()
	command.Env = append(os.Environ(), "PATH="+shadowBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("run metadata schema dump script: %v, stderr = %q", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("script stderr = %q, want empty", stderr.String())
	}
	if stdout.Len() == 0 {
		t.Fatal("script stdout is empty")
	}
	assertExecutableMetadataSchemaDump(t, stdout.Bytes())
}
