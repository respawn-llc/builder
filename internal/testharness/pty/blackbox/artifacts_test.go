package blackbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty/analyzer"
)

func TestPublishFailureArtifactsAtomicallyPublishesLatestBundle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	capture, err := analyzer.NewCapture(analyzer.MustDimensions(2, 8), []analyzer.Chunk{
		analyzer.NewChunk(0, 0, []byte("failure")),
	})
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	dir, err := publishFailureArtifacts(time.Now().Add(time.Second), root, capture, &analysis, errors.New("primary failure"), nil)
	if err != nil {
		t.Fatalf("publishFailureArtifacts: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "raw.bin")); err != nil {
		t.Fatalf("published raw artifact: %v", err)
	}
	latest, err := os.ReadFile(filepath.Join(root, "artifacts", "latest.json"))
	if err != nil {
		t.Fatalf("read latest pointer: %v", err)
	}
	if len(latest) == 0 {
		t.Fatal("latest pointer is empty")
	}
}

func TestPublishFailureArtifactsRejectsContendedPublicationLock(t *testing.T) {
	root := t.TempDir()
	artifactRoot := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	lock, err := os.OpenFile(filepath.Join(artifactRoot, "publish.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create publication lock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })

	capture, err := analyzer.NewCapture(analyzer.MustDimensions(1, 1), nil)
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	analysis, err := analyzer.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if _, err := publishFailureArtifacts(time.Now().Add(time.Second), root, capture, &analysis, errors.New("primary failure"), nil); err == nil {
		t.Fatal("publishFailureArtifacts succeeded while publication lock was held")
	}
}
