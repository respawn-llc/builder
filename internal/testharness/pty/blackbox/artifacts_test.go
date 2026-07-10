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
