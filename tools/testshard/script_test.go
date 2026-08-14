package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDefaultScriptReusesPTYFixtureBinariesAcrossRuns(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	fakeGoDir := t.TempDir()
	cacheRoot := t.TempDir()
	fixturePathRecord := filepath.Join(t.TempDir(), "fixture-path")
	kentPathRecord := filepath.Join(t.TempDir(), "kent-path")
	ansiWriterPathRecord := filepath.Join(t.TempDir(), "ansi-writer-path")
	phaseInputWriterPathRecord := filepath.Join(t.TempDir(), "phase-input-writer-path")
	phaseWriterPathRecord := filepath.Join(t.TempDir(), "phase-writer-path")
	writeFakeGoCommand(t, filepath.Join(fakeGoDir, "go"))

	runScript := func() {
		t.Helper()
		command := exec.Command("bash", "scripts/test.sh", "server")
		command.Dir = repoRoot
		command.Env = append(
			os.Environ(),
			"PATH="+fakeGoDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"TEST_SCRIPT_FIXTURE_PATH="+fixturePathRecord,
			"TEST_SCRIPT_KENT_PATH="+kentPathRecord,
			"TEST_SCRIPT_ANSI_WRITER_PATH="+ansiWriterPathRecord,
			"TEST_SCRIPT_PHASE_INPUT_WRITER_PATH="+phaseInputWriterPathRecord,
			"TEST_SCRIPT_PHASE_WRITER_PATH="+phaseWriterPathRecord,
			"KENT_TEST_PTY_CACHE_ROOT="+cacheRoot,
			"KENT_SKIP_FRONTEND=1",
			"KENT_TEST_DISABLE_WALL_CLOCK_CAP=0",
			"KENT_TEST_TIMEOUT_SECONDS=1",
		)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("run capped default server script: %v\n%s", err, output)
		}
	}

	runScript()
	firstPaths := []string{
		assertPrebuiltExecutable(t, fixturePathRecord, "PTY fixture"),
		assertPrebuiltExecutable(t, kentPathRecord, "Kent"),
		assertPrebuiltExecutable(t, ansiWriterPathRecord, "ANSI writer"),
		assertPrebuiltExecutable(t, phaseInputWriterPathRecord, "phase input writer"),
		assertPrebuiltExecutable(t, phaseWriterPathRecord, "phase writer"),
	}

	runScript()
	secondRecords := []string{
		fixturePathRecord,
		kentPathRecord,
		ansiWriterPathRecord,
		phaseInputWriterPathRecord,
		phaseWriterPathRecord,
	}
	for index, recordPath := range secondRecords {
		if got := assertPrebuiltExecutable(t, recordPath, "cached fixture"); got != firstPaths[index] {
			t.Fatalf("cached fixture path changed: got %q, want %q", got, firstPaths[index])
		}
	}
}

func assertPrebuiltExecutable(t *testing.T, recordPath string, name string) string {
	t.Helper()
	recordedPath, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read prebuilt %s path: %v", name, err)
	}
	path := string(recordedPath)
	if !filepath.IsAbs(path) {
		t.Fatalf("prebuilt %s path = %q, want absolute path", name, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("prebuilt %s binary missing: %v", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		t.Fatalf("prebuilt %s path is not executable: %s", name, path)
	}
	return path
}

func writeFakeGoCommand(t *testing.T, path string) {
	t.Helper()

	const source = `#!/usr/bin/env bash
set -euo pipefail

case "$1" in
version)
    printf 'go version go-test darwin/arm64\n'
    ;;
env)
    printf 'darwin\narm64\n0\n'
    ;;
list)
    printf 'stable dependency build IDs\n'
    ;;
test)
    if [ "$2" != "-c" ]; then
        printf 'unexpected go test arguments: %s\n' "$*" >&2
        exit 1
    fi
    output=""
    shift 2
    while [ "$#" -gt 0 ]; do
        if [ "$1" = "-o" ]; then
            output="$2"
            shift 2
            continue
        fi
        shift
    done
    [ -n "$output" ]
    : >"$output"
    chmod +x "$output"
    ;;
run)
    [ "$2" = "./tools/testshard" ]
    : "${KENT_PTY_FIXTURE_BINARY:?missing PTY fixture binary}"
    [ -x "$KENT_PTY_FIXTURE_BINARY" ]
    : "${KENT_PTY_KENT_BINARY:?missing Kent binary}"
    [ -x "$KENT_PTY_KENT_BINARY" ]
    : "${KENT_PTY_ANSI_WRITER_BINARY:?missing ANSI writer binary}"
    [ -x "$KENT_PTY_ANSI_WRITER_BINARY" ]
    : "${KENT_PTY_PHASE_INPUT_WRITER_BINARY:?missing phase input writer binary}"
    [ -x "$KENT_PTY_PHASE_INPUT_WRITER_BINARY" ]
    : "${KENT_PTY_PHASE_WRITER_BINARY:?missing phase writer binary}"
    [ -x "$KENT_PTY_PHASE_WRITER_BINARY" ]
    printf '%s' "$KENT_PTY_FIXTURE_BINARY" >"$TEST_SCRIPT_FIXTURE_PATH"
    printf '%s' "$KENT_PTY_KENT_BINARY" >"$TEST_SCRIPT_KENT_PATH"
    printf '%s' "$KENT_PTY_ANSI_WRITER_BINARY" >"$TEST_SCRIPT_ANSI_WRITER_PATH"
    printf '%s' "$KENT_PTY_PHASE_INPUT_WRITER_BINARY" >"$TEST_SCRIPT_PHASE_INPUT_WRITER_PATH"
    printf '%s' "$KENT_PTY_PHASE_WRITER_BINARY" >"$TEST_SCRIPT_PHASE_WRITER_PATH"
    ;;
build)
    output=""
    shift
    while [ "$#" -gt 0 ]; do
        if [ "$1" = "-o" ]; then
            output="$2"
            shift 2
            continue
        fi
        shift
    done
    [ -n "$output" ]
    : >"$output"
    chmod +x "$output"
    ;;
*)
    printf 'unexpected go command: %s\n' "$*" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(path, []byte(source), 0o755); err != nil {
		t.Fatalf("write fake Go command: %v", err)
	}
}
