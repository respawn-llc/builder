//go:build darwin || linux

package session

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const migrationHardMemoryLimitBytes = 128 << 20

func TestMigrationValueLayerUnderHardMemoryLimit(t *testing.T) {
	if os.Getenv("KENT_MIGRATION_MEMORY_LIMIT_CHILD") == "1" {
		limited, err := applyMigrationHardMemoryLimit()
		if err != nil {
			t.Fatalf("apply migration hard memory limit: %v", err)
		}
		runMigrationBulkFixture(t, migrationLargeFixtureBytes)
		runLargeAbsentSnapshotFallbackFixture(t, 80<<20)
		runLargeStructuredAbsentSnapshotFallbackFixture(t, 80<<20)
		runMigrationDepthFixture(t, migrationMaxJSONNesting, nil)
		runMigrationDepthFixture(t, migrationMaxJSONNesting+1, errMigrationJSONComplex)
		if !limited {
			if err := assertMigrationResidentMemoryWithinLimit(); err != nil {
				t.Fatalf("migration resident memory limit: %v", err)
			}
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(
		ctx,
		os.Args[0],
		"-test.run=^TestMigrationValueLayerUnderHardMemoryLimit$",
		"-test.count=1",
	)
	command.Env = append(os.Environ(), "KENT_MIGRATION_MEMORY_LIMIT_CHILD=1")
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("memory-limited migration subprocess timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("memory-limited migration subprocess: %v\n%s", err, output)
	}
}

func runLargeStructuredAbsentSnapshotFallbackFixture(t *testing.T, fileDataBytes int64) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy-structured-fallback.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create structured absent-snapshot fixture: %v", err)
	}
	write := func(value string) {
		t.Helper()
		if _, writeErr := file.WriteString(value); writeErr != nil {
			_ = file.Close()
			t.Fatalf("write structured absent-snapshot fixture: %v", writeErr)
		}
	}
	write(`{"seq":1,"timestamp":"2026-07-20T00:00:00Z","kind":"message","payload":{"role":"assistant","tool_calls":[{"id":"call-1","name":"exec_command","input":{}}]}}` + "\n")
	write(`{"seq":2,"timestamp":"2026-07-20T00:00:01Z","kind":"tool_completed","payload":{"call_id":"call-1","name":"exec_command","is_error":false,"output":[{"type":"input_file","file_data":"`)
	if err := writeRepeatedFixtureBytes(file, sha256.New(), 'x', fileDataBytes); err != nil {
		_ = file.Close()
		t.Fatalf("write structured absent-snapshot output: %v", err)
	}
	write(`","filename":"fixture.txt"}],"summary":"done"}}` + "\n")
	if err := file.Close(); err != nil {
		t.Fatalf("close structured absent-snapshot fixture: %v", err)
	}

	source, err := openRegularSessionFile(path, "structured absent-snapshot fixture")
	if err != nil {
		t.Fatalf("open structured absent-snapshot fixture: %v", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		t.Fatalf("stat structured absent-snapshot fixture: %v", err)
	}
	ledger := newMigrationResourceLedger()
	result, err := transformLegacyEventLogV0(
		context.Background(),
		source,
		info.Size(),
		io.Discard,
		dir,
		ledger,
		osMigrationSpoolStorage{},
	)
	if err != nil {
		t.Fatalf("transform structured absent-snapshot fixture: %v", err)
	}
	if result.AbsentSnapshots != 1 {
		t.Fatalf("structured absent-snapshot transform result = %+v", result)
	}
	stats := ledger.snapshot()
	if stats.MaxEncoderMergeBytes != 6*migrationCopyBufferBytes ||
		stats.LiveInlineBytes != 0 ||
		stats.SourceDecoderBytes != 0 ||
		stats.EncoderMergeBytes != 0 ||
		stats.OpenSpoolFiles != 0 ||
		stats.CurrentSpoolBytes != 0 {
		t.Fatalf("structured absent-snapshot resources remain live: %+v", stats)
	}
}

func runLargeAbsentSnapshotFallbackFixture(t *testing.T, outputBytes int64) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy-fallback.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create absent-snapshot fixture: %v", err)
	}
	write := func(value string) {
		t.Helper()
		if _, writeErr := file.WriteString(value); writeErr != nil {
			_ = file.Close()
			t.Fatalf("write absent-snapshot fixture: %v", writeErr)
		}
	}
	write(`{"seq":1,"timestamp":"2026-07-20T00:00:00Z","kind":"message","payload":{"role":"assistant","tool_calls":[{"id":"call-1","name":"exec_command","input":{}}]}}` + "\n")
	write(`{"seq":2,"timestamp":"2026-07-20T00:00:01Z","kind":"tool_completed","payload":{"call_id":"call-1","name":"exec_command","is_error":false,"output":"`)
	if err := writeRepeatedFixtureBytes(file, sha256.New(), 'x', outputBytes); err != nil {
		_ = file.Close()
		t.Fatalf("write absent-snapshot output: %v", err)
	}
	write(`","summary":"done"}}` + "\n")
	if err := file.Close(); err != nil {
		t.Fatalf("close absent-snapshot fixture: %v", err)
	}

	source, err := openRegularSessionFile(path, "absent-snapshot fixture")
	if err != nil {
		t.Fatalf("open absent-snapshot fixture: %v", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		t.Fatalf("stat absent-snapshot fixture: %v", err)
	}
	ledger := newMigrationResourceLedger()
	result, err := transformLegacyEventLogV0(
		context.Background(),
		source,
		info.Size(),
		io.Discard,
		dir,
		ledger,
		osMigrationSpoolStorage{},
	)
	if err != nil {
		t.Fatalf("transform absent-snapshot fixture: %v", err)
	}
	if result.AbsentSnapshots != 1 {
		t.Fatalf("absent-snapshot transform result = %+v", result)
	}
	stats := ledger.snapshot()
	if stats.LiveInlineBytes != 0 ||
		stats.OpenSpoolFiles != 0 ||
		stats.CurrentSpoolBytes != 0 {
		t.Fatalf("absent-snapshot resources remain live: %+v", stats)
	}
}

func runMigrationDepthFixture(t *testing.T, depth int, wantErr error) {
	t.Helper()
	document := strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth)
	path := filepath.Join(t.TempDir(), "depth.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write depth fixture: %v", err)
	}
	source, err := openRegularSessionFile(path, "depth fixture")
	if err != nil {
		t.Fatalf("open depth fixture: %v", err)
	}
	defer source.Close()
	scanner, err := newMigrationJSONScanner(
		source,
		0,
		int64(len(document)),
		newMigrationResourceLedger(),
	)
	if err != nil {
		t.Fatalf("create depth scanner: %v", err)
	}
	defer scanner.Close()
	err = scanner.ScanArray(func(_ int, _ migrationJSONValueRange) error { return nil })
	if wantErr == nil {
		if err != nil {
			t.Fatalf("scan depth %d: %v", depth, err)
		}
		return
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("scan depth %d error = %T %v, want %v", depth, err, err, wantErr)
	}
}

func applyMigrationHardMemoryLimit() (bool, error) {
	resource := migrationMemoryLimitResource()
	if resource < 0 {
		return false, nil
	}
	var limit unix.Rlimit
	if err := unix.Getrlimit(resource, &limit); err != nil {
		return false, fmt.Errorf("read migration memory limit: %w", err)
	}
	limit.Cur = migrationHardMemoryLimitBytes
	if err := unix.Setrlimit(resource, &limit); err != nil {
		return false, fmt.Errorf("set 128 MiB migration limit: %w", err)
	}
	return true, nil
}
