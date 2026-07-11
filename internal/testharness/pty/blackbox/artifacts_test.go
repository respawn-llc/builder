package blackbox

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty/analyzer"

	"github.com/google/uuid"
)

func TestPublishFailureArtifactsAtomicallyPublishesLatestBundle(t *testing.T) {
	store := newArtifactStore(t.TempDir())
	run := beginTestArtifactRun(t, store)
	dir, err := publishFailureArtifacts(time.Now().Add(time.Second), run, testArtifactEvidence(t), errors.New("primary failure"), nil)
	if err != nil {
		t.Fatalf("publishFailureArtifacts: %v", err)
	}
	t.Cleanup(func() { closeTestArtifactRun(t, run) })
	if _, err := os.Stat(filepath.Join(dir, "raw.bin")); err != nil {
		t.Fatalf("published raw artifact: %v", err)
	}
	latest, err := os.ReadFile(filepath.Join(store.root, "latest.json"))
	if err != nil {
		t.Fatalf("read latest pointer: %v", err)
	}
	var pointer artifactLatestPointer
	if err := json.Unmarshal(latest, &pointer); err != nil {
		t.Fatalf("decode latest pointer: %v", err)
	}
	if pointer.Run != filepath.ToSlash(filepath.Join("runs", run.id.String())) {
		t.Fatalf("latest pointer = %q, want %q", pointer.Run, filepath.ToSlash(filepath.Join("runs", run.id.String())))
	}
}

func TestPublishFailureArtifactsRetainsBundleWhenLatestLeaseIsContended(t *testing.T) {
	store := newArtifactStore(t.TempDir())
	run := beginTestArtifactRun(t, store)
	lock, err := acquireFileLease(store.latestLeasePath())
	if err != nil {
		t.Fatalf("acquire latest lease: %v", err)
	}
	t.Cleanup(func() {
		closeTestLease(t, lock)
	})
	_, err = publishFailureArtifacts(time.Now().Add(40*time.Millisecond), run, testArtifactEvidence(t), errors.New("primary failure"), nil)
	if err == nil {
		t.Fatal("publishFailureArtifacts succeeded while latest lease was held")
	}
	var incomplete *ArtifactPublicationIncomplete
	if !errors.As(err, &incomplete) {
		t.Fatalf("publishFailureArtifacts error = %T %v, want ArtifactPublicationIncomplete", err, err)
	}
	if _, err := os.Stat(run.final); err != nil {
		t.Fatalf("complete bundle was not retained after latest contention: %v", err)
	}
}

func TestPublishFailureArtifactsRetainsBundleWhenLatestLeaseIsContendedCrossProcess(t *testing.T) {
	store := newArtifactStore(t.TempDir())
	run := beginTestArtifactRun(t, store)
	t.Cleanup(func() { closeTestArtifactRun(t, run) })
	holder, err := startLeaseHolder(store.latestLeasePath())
	if err != nil {
		t.Fatalf("start cross-process lease holder: %v", err)
	}
	t.Cleanup(func() {
		if err := holder.stop(); err != nil {
			t.Errorf("stop cross-process lease holder: %v", err)
		}
	})

	_, err = publishFailureArtifacts(time.Now().Add(40*time.Millisecond), run, testArtifactEvidence(t), errors.New("primary failure"), nil)
	var incomplete *ArtifactPublicationIncomplete
	if !errors.As(err, &incomplete) {
		t.Fatalf("publish error = %T %v, want ArtifactPublicationIncomplete", err, err)
	}
	if _, err := os.Stat(run.final); err != nil {
		t.Fatalf("complete bundle was not retained after cross-process contention: %v", err)
	}
}

func TestArtifactLeaseHolder(t *testing.T) {
	path := os.Getenv("KENT_ARTIFACT_LEASE_PATH")
	if path == "" {
		return
	}
	lease, err := acquireFileLease(path)
	if err != nil {
		t.Fatalf("acquire child lease: %v", err)
	}
	if _, err := os.Stdout.WriteString("locked\n"); err != nil {
		t.Fatalf("signal child lease: %v", err)
	}
	_, _ = io.ReadAll(os.Stdin)
	if err := lease.release(); err != nil {
		t.Fatalf("release child lease: %v", err)
	}
}

type leaseHolder struct {
	stdin io.WriteCloser
	cmd   *exec.Cmd
}

func startLeaseHolder(path string) (*leaseHolder, error) {
	command := exec.Command(os.Args[0], "-test.run=TestArtifactLeaseHolder$")
	command.Env = append(os.Environ(), "KENT_ARTIFACT_LEASE_PATH="+path)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	cleanupChild := true
	defer func() {
		if cleanupChild {
			_ = stdin.Close()
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		return nil, err
	}
	if line != "locked\n" {
		return nil, errors.New("cross-process artifact lease holder did not lock")
	}
	cleanupChild = false
	return &leaseHolder{stdin: stdin, cmd: command}, nil
}

func (h *leaseHolder) stop() error {
	if h == nil {
		return nil
	}
	if err := h.stdin.Close(); err != nil {
		return err
	}
	return h.cmd.Wait()
}

func TestArtifactStoreDoesNotPruneLiveRunAndPrunesReleasedRun(t *testing.T) {
	store := newArtifactStore(t.TempDir())
	first := beginTestArtifactRun(t, store)
	if _, err := publishFailureArtifacts(time.Now().Add(time.Second), first, testArtifactEvidence(t), errors.New("first"), nil); err != nil {
		t.Fatalf("publish first: %v", err)
	}
	second := beginTestArtifactRun(t, store)
	if _, err := publishFailureArtifacts(time.Now().Add(time.Second), second, testArtifactEvidence(t), errors.New("second"), nil); err != nil {
		t.Fatalf("publish second: %v", err)
	}
	if _, err := os.Stat(first.final); err != nil {
		t.Fatalf("live first run was pruned: %v", err)
	}
	closeTestArtifactRun(t, first)
	third := beginTestArtifactRun(t, store)
	if _, err := publishFailureArtifacts(time.Now().Add(time.Second), third, testArtifactEvidence(t), errors.New("third"), nil); err != nil {
		t.Fatalf("publish third: %v", err)
	}
	if _, err := os.Stat(first.final); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released first run still exists, stat error=%v", err)
	}
	closeTestArtifactRun(t, second)
	closeTestArtifactRun(t, third)
}

func TestArtifactStoreRecoversStaleLeaseFileDuringPrune(t *testing.T) {
	store := newArtifactStore(t.TempDir())
	staleID := uuid.New()
	stale := filepath.Join(store.runsRoot(), staleID.String())
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatalf("create stale artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stale, artifactLeaseFileName), nil, 0o600); err != nil {
		t.Fatalf("create stale lease file: %v", err)
	}
	run := beginTestArtifactRun(t, store)
	if _, err := publishFailureArtifacts(time.Now().Add(time.Second), run, testArtifactEvidence(t), errors.New("failure"), nil); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale artifact was not recovered, stat error=%v", err)
	}
	closeTestArtifactRun(t, run)
}

func beginTestArtifactRun(t *testing.T, store artifactStore) *artifactRun {
	t.Helper()
	run, err := store.beginRun()
	if err != nil {
		t.Fatalf("begin artifact run: %v", err)
	}
	return run
}

func closeTestArtifactRun(t *testing.T, run *artifactRun) {
	t.Helper()
	if err := run.release(); err != nil {
		t.Fatalf("release artifact run: %v", err)
	}
}

func closeTestLease(t *testing.T, lease *fileLease) {
	t.Helper()
	if err := lease.release(); err != nil {
		t.Fatalf("release lease: %v", err)
	}
}

func testArtifactEvidence(t *testing.T) artifactEvidence {
	t.Helper()
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
	return artifactEvidence{capture: capture, analysis: &analysis}
}
