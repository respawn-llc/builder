package session

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"core/shared/runtimeids"
	"github.com/klauspost/compress/zstd"
)

type archivedEntry struct {
	typeflag byte
	linkname string
	body     string
}

func TestArchiveAndRemoveSessionArtifactsPreserveUnknownContent(t *testing.T) {
	parent := t.TempDir()
	sessionID := runtimeids.NewSessionID()
	realSessionDir := filepath.Join(parent, "real-session")
	if err := os.MkdirAll(filepath.Join(realSessionDir, eventLogMigrationWorkspaceDir), 0o755); err != nil {
		t.Fatalf("create Session directories: %v", err)
	}
	ownedFiles := map[string]string{
		eventsFile:                  "events",
		eventLogPersistenceLockFile: "lock",
		appendRecoveryFile:          "recovery",
		sessionRunLogFile:           "steps",
		filepath.Join(eventLogMigrationWorkspaceDir, eventLogMigrationWorkspaceMarkerFile): "migration",
		filepath.Join(eventLogMigrationWorkspaceDir, eventLogMigrationStagedLogFile):       "staged",
		filepath.Join(eventLogMigrationWorkspaceDir, eventLogMigrationReadyMarkerFile):     "ready",
	}
	for relativePath, body := range ownedFiles {
		writeArchiveFixtureFile(t, filepath.Join(realSessionDir, relativePath), body)
	}
	writeArchiveFixtureFile(t, filepath.Join(realSessionDir, "notes", "personal.txt"), "unknown")
	if err := os.Symlink("../notes/personal.txt", filepath.Join(realSessionDir, "nested-link")); err != nil {
		t.Skipf("nested symlinks unavailable: %v", err)
	}

	sessionAlias := filepath.Join(parent, "session-alias")
	if err := os.Symlink(realSessionDir, sessionAlias); err != nil {
		t.Skipf("root symlinks unavailable: %v", err)
	}
	outputPath := filepath.Join(sessionAlias, "analysis.tar.zst")
	if err := ArchiveSessionDirectory(context.Background(), sessionID, sessionAlias, outputPath); err != nil {
		t.Fatalf("ArchiveSessionDirectory: %v", err)
	}

	entries := decodeArchiveFixture(t, outputPath)
	root := sessionID.String()
	want := map[string]archivedEntry{
		root + "/": {
			typeflag: tar.TypeDir,
		},
		root + "/" + eventsFile: {
			typeflag: tar.TypeReg,
			body:     "events",
		},
		root + "/" + eventLogPersistenceLockFile: {
			typeflag: tar.TypeReg,
			body:     "lock",
		},
		root + "/" + appendRecoveryFile: {
			typeflag: tar.TypeReg,
			body:     "recovery",
		},
		root + "/" + sessionRunLogFile: {
			typeflag: tar.TypeReg,
			body:     "steps",
		},
		root + "/" + eventLogMigrationWorkspaceDir + "/": {
			typeflag: tar.TypeDir,
		},
		root + "/" + eventLogMigrationWorkspaceDir + "/" + eventLogMigrationWorkspaceMarkerFile: {
			typeflag: tar.TypeReg,
			body:     "migration",
		},
		root + "/" + eventLogMigrationWorkspaceDir + "/" + eventLogMigrationStagedLogFile: {
			typeflag: tar.TypeReg,
			body:     "staged",
		},
		root + "/" + eventLogMigrationWorkspaceDir + "/" + eventLogMigrationReadyMarkerFile: {
			typeflag: tar.TypeReg,
			body:     "ready",
		},
		root + "/notes/": {
			typeflag: tar.TypeDir,
		},
		root + "/notes/personal.txt": {
			typeflag: tar.TypeReg,
			body:     "unknown",
		},
		root + "/nested-link": {
			typeflag: tar.TypeSymlink,
			linkname: "../notes/personal.txt",
		},
	}
	if len(entries) != len(want) {
		t.Fatalf("archive entries = %#v, want %#v", entries, want)
	}
	for name, wantEntry := range want {
		if got, ok := entries[name]; !ok || got != wantEntry {
			t.Fatalf("archive entry %q = %#v, %t; want %#v", name, got, ok, wantEntry)
		}
	}
	schedule, err := PreflightSessionArtifactRemoval(sessionAlias)
	if err != nil {
		t.Fatalf("PreflightSessionArtifactRemoval: %v", err)
	}
	if err := os.Remove(filepath.Join(realSessionDir, appendRecoveryFile)); err != nil {
		t.Fatalf("remove scheduled recovery artifact: %v", err)
	}
	if err := RemovePreflightedSessionArtifacts(schedule); err != nil {
		t.Fatalf("RemovePreflightedSessionArtifacts: %v", err)
	}
	for relativePath := range ownedFiles {
		if _, err := os.Lstat(filepath.Join(realSessionDir, relativePath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned artifact %q remains: %v", relativePath, err)
		}
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("in-Session archive was not preserved: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(realSessionDir, "notes", "personal.txt")); err != nil || string(body) != "unknown" {
		t.Fatalf("unknown content = %q, %v", body, err)
	}
	if _, err := os.Lstat(filepath.Join(realSessionDir, "nested-link")); err != nil {
		t.Fatalf("unknown symlink was not preserved: %v", err)
	}
}

func TestPreflightSessionArtifactRemovalLeavesArtifactsOnFailure(t *testing.T) {
	sessionDir := t.TempDir()
	eventsPath := filepath.Join(sessionDir, eventsFile)
	writeArchiveFixtureFile(t, eventsPath, "events")
	if err := os.Chmod(sessionDir, 0o500); err != nil {
		t.Fatalf("make Session directory unwritable: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(sessionDir, 0o700)
	})

	_, err := PreflightSessionArtifactRemoval(sessionDir)
	var preflightErr *SessionArtifactPreflightError
	if !errors.As(err, &preflightErr) {
		t.Fatalf("PreflightSessionArtifactRemoval error = %v, want SessionArtifactPreflightError", err)
	}
	if preflightErr.Path != eventsPath {
		t.Fatalf("preflight path = %q, want %q", preflightErr.Path, eventsPath)
	}
	if body, readErr := os.ReadFile(eventsPath); readErr != nil || string(body) != "events" {
		t.Fatalf("events after failed preflight = %q, %v", body, readErr)
	}
}

func TestRemovePreflightedSessionArtifactsReportsExactRemainingPath(t *testing.T) {
	sessionDir := t.TempDir()
	eventsPath := filepath.Join(sessionDir, eventsFile)
	writeArchiveFixtureFile(t, eventsPath, "events")
	schedule, err := PreflightSessionArtifactRemoval(sessionDir)
	if err != nil {
		t.Fatalf("PreflightSessionArtifactRemoval: %v", err)
	}
	if err := os.Chmod(sessionDir, 0o500); err != nil {
		t.Fatalf("make Session directory unwritable: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(sessionDir, 0o700)
	})

	err = RemovePreflightedSessionArtifacts(schedule)
	var removalErr *SessionArtifactRemovalError
	if !errors.As(err, &removalErr) {
		t.Fatalf("RemovePreflightedSessionArtifacts error = %v, want SessionArtifactRemovalError", err)
	}
	if removalErr.RemainingPath != eventsPath {
		t.Fatalf("remaining path = %q, want %q", removalErr.RemainingPath, eventsPath)
	}
}

func TestArchiveSessionDirectoryRejectsUnwritableOutput(t *testing.T) {
	sessionDir := t.TempDir()
	writeArchiveFixtureFile(t, filepath.Join(sessionDir, eventsFile), "events")
	outputDir := filepath.Join(t.TempDir(), "output")
	if err := os.Mkdir(outputDir, 0o500); err != nil {
		t.Fatalf("create unwritable output directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(outputDir, 0o700)
	})
	outputPath := filepath.Join(outputDir, "session.tar.zst")

	err := ArchiveSessionDirectory(context.Background(), runtimeids.NewSessionID(), sessionDir, outputPath)
	var pathErr *ArchivePathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("ArchiveSessionDirectory error = %v, want ArchivePathError", err)
	}
	if pathErr.Path != outputPath || pathErr.Phase != ArchivePathPhaseTemp {
		t.Fatalf("archive path error = %+v, want path %q phase %q", pathErr, outputPath, ArchivePathPhaseTemp)
	}
	if _, statErr := os.Lstat(outputPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output exists after failure: %v", statErr)
	}
	if entries, readErr := os.ReadDir(outputDir); readErr != nil || len(entries) != 0 {
		t.Fatalf("output directory after failure = %v, %v; want empty", entries, readErr)
	}
}

func TestArchiveSessionDirectoryPreservesExistingOutput(t *testing.T) {
	sessionDir := t.TempDir()
	writeArchiveFixtureFile(t, filepath.Join(sessionDir, eventsFile), "events")
	outputPath := filepath.Join(t.TempDir(), "session.tar.zst")
	writeArchiveFixtureFile(t, outputPath, "existing")

	err := ArchiveSessionDirectory(context.Background(), runtimeids.NewSessionID(), sessionDir, outputPath)
	var existsErr *ArchiveOutputExistsError
	if !errors.As(err, &existsErr) {
		t.Fatalf("ArchiveSessionDirectory error = %v, want ArchiveOutputExistsError", err)
	}
	if body, readErr := os.ReadFile(outputPath); readErr != nil || string(body) != "existing" {
		t.Fatalf("existing output = %q, %v", body, readErr)
	}
	entries, readErr := os.ReadDir(filepath.Dir(outputPath))
	if readErr != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(outputPath) {
		t.Fatalf("output directory after rejection = %v, %v", entries, readErr)
	}
}

func writeArchiveFixtureFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func decodeArchiveFixture(t *testing.T, path string) map[string]archivedEntry {
	t.Helper()
	fp, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer fp.Close()
	decoder, err := zstd.NewReader(fp)
	if err != nil {
		t.Fatalf("create Zstandard decoder: %v", err)
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)
	entries := make(map[string]archivedEntry)
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			return entries
		}
		if nextErr != nil {
			t.Fatalf("read archive entry: %v", nextErr)
		}
		body, readErr := io.ReadAll(reader)
		if readErr != nil {
			t.Fatalf("read archive body %q: %v", header.Name, readErr)
		}
		entries[header.Name] = archivedEntry{
			typeflag: header.Typeflag,
			linkname: header.Linkname,
			body:     string(body),
		}
	}
}
