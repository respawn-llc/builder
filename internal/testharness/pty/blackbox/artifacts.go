package blackbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/analyzer"

	"github.com/google/uuid"
)

const artifactLeaseFileName = "lease.lock"

type artifactStore struct {
	root string
}

type artifactRun struct {
	store     artifactStore
	id        uuid.UUID
	staging   string
	final     string
	lease     *fileLease
	publisher *artifactPublisher
	published bool
}

type artifactEvidence struct {
	capture     analyzer.Capture
	analysis    *analyzer.Analysis
	attachments []pty.ArtifactAttachment
}

type artifactLatestPointer struct {
	Run string `json:"run"`
}

// ArtifactPublicationIncomplete reports that a complete failed-run bundle was
// retained but could not be made the shared latest pointer before cleanup's
// deadline. It is deliberately distinct from the scenario failure.
type ArtifactPublicationIncomplete struct {
	Run   string
	Cause error
}

type artifactPublisher struct {
	done    chan struct{}
	outcome artifactPublicationOutcome
}

type artifactPublicationOutcome struct {
	dir string
	err error
}

func startArtifactPublisher(publish func() artifactPublicationOutcome) *artifactPublisher {
	publisher := &artifactPublisher{done: make(chan struct{})}
	go func() {
		publisher.outcome = publish()
		close(publisher.done)
	}()
	return publisher
}

func (p *artifactPublisher) Done() <-chan struct{} {
	if p == nil {
		return nil
	}
	return p.done
}

func (p *artifactPublisher) Outcome() artifactPublicationOutcome {
	if p == nil {
		return artifactPublicationOutcome{err: errors.New("artifact publisher is required")}
	}
	select {
	case <-p.done:
		return p.outcome
	default:
		panic("artifact publisher outcome requested before completion")
	}
}

func (e *ArtifactPublicationIncomplete) Error() string {
	return fmt.Sprintf("artifact publication incomplete for %s: %v", e.Run, e.Cause)
}

func (e *ArtifactPublicationIncomplete) Unwrap() error {
	return e.Cause
}

// publishFailureArtifacts writes into the lease-owned staging directory,
// atomically makes it complete, then updates the shared latest pointer.
func publishFailureArtifacts(deadline time.Time, run *artifactRun, evidence artifactEvidence, runErr error, cleanup *IncompleteCleanup) (string, error) {
	if run == nil {
		return "", errors.New("artifact run is required")
	}
	if runErr == nil {
		return "", errors.New("artifact run error is required")
	}
	if err := ensureBefore(deadline, "artifact publication"); err != nil {
		return "", err
	}
	if evidence.analysis == nil {
		replayed, err := analyzer.Analyze(evidence.capture)
		if err != nil {
			return "", fmt.Errorf("analyze artifact capture: %w", err)
		}
		evidence.analysis = &replayed
	}
	if err := pty.WriteArtifactsWithAttachments(run.staging, evidence.capture, *evidence.analysis, runErr, evidence.attachments); err != nil {
		return "", err
	}
	if err := ensureBefore(deadline, "artifact evidence write"); err != nil {
		return "", err
	}
	metadata := struct {
		RunError string             `json:"run_error"`
		Cleanup  *IncompleteCleanup `json:"cleanup,omitempty"`
	}{
		RunError: runErr.Error(),
		Cleanup:  cleanup,
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("marshal artifact metadata: %w", err)
	}
	if err := os.WriteFile(filepath.Join(run.staging, "result.json"), encoded, 0o600); err != nil {
		return "", fmt.Errorf("write artifact metadata: %w", err)
	}
	if err := ensureBefore(deadline, "artifact metadata write"); err != nil {
		return "", err
	}
	if err := ensureBefore(deadline, "artifact bundle publication"); err != nil {
		return "", err
	}
	if err := os.Rename(run.staging, run.final); err != nil {
		return "", fmt.Errorf("publish artifact bundle: %w", err)
	}
	run.published = true

	latestLease, err := run.store.acquireLatestLeaseUntil(deadline)
	if err != nil {
		return run.final, &ArtifactPublicationIncomplete{Run: run.id.String(), Cause: err}
	}
	finishPublication := func(cause error) (string, error) {
		releaseErr := latestLease.release()
		if cause == nil && releaseErr == nil {
			return run.final, nil
		}
		if cause == nil {
			cause = releaseErr
		} else if releaseErr != nil {
			cause = fmt.Errorf("%w; release latest artifact lease: %v", cause, releaseErr)
		}
		return run.final, &ArtifactPublicationIncomplete{Run: run.id.String(), Cause: cause}
	}
	if err := ensureBefore(deadline, "latest artifact pointer publication"); err != nil {
		return finishPublication(err)
	}
	if err := run.store.writeLatest(run.id); err != nil {
		return finishPublication(err)
	}
	if err := run.store.pruneInactiveCompletedRuns(run.id, deadline); err != nil {
		return finishPublication(err)
	}
	return finishPublication(nil)
}

func newArtifactStore(root string) artifactStore {
	return artifactStore{root: root}
}

func artifactStoreRoot() string {
	return filepath.Join(os.TempDir(), "kent-pty-artifacts")
}

func (s artifactStore) runsRoot() string {
	return filepath.Join(s.root, "runs")
}

func (s artifactStore) latestLeasePath() string {
	return filepath.Join(s.root, "latest.lock")
}

func (s artifactStore) beginRun() (*artifactRun, error) {
	if err := os.MkdirAll(s.runsRoot(), 0o700); err != nil {
		return nil, fmt.Errorf("create artifact runs root: %w", err)
	}
	id := uuid.New()
	staging := filepath.Join(s.runsRoot(), id.String()+".staging")
	if err := os.Mkdir(staging, 0o700); err != nil {
		return nil, fmt.Errorf("create artifact staging: %w", err)
	}
	lease, err := acquireFileLease(filepath.Join(staging, artifactLeaseFileName))
	if err != nil {
		removeErr := removeTreeUntil(staging, time.Now().Add(fixedWait))
		if removeErr != nil {
			return nil, fmt.Errorf("acquire artifact run lease: %w; remove incomplete staging: %v", err, removeErr)
		}
		return nil, fmt.Errorf("acquire artifact run lease: %w", err)
	}
	return &artifactRun{
		store:   s,
		id:      id,
		staging: staging,
		final:   filepath.Join(s.runsRoot(), id.String()),
		lease:   lease,
	}, nil
}

func beginArtifactRun() (*artifactRun, error) {
	return newArtifactStore(artifactStoreRoot()).beginRun()
}

func (r *artifactRun) startFailurePublication(deadline time.Time, evidence artifactEvidence, runErr error, cleanup *IncompleteCleanup) *artifactPublisher {
	if r.publisher != nil {
		return r.publisher
	}
	r.publisher = startArtifactPublisher(func() artifactPublicationOutcome {
		dir, err := publishFailureArtifacts(deadline, r, evidence, runErr, cleanup)
		if releaseErr := r.release(); releaseErr != nil {
			if err == nil {
				err = releaseErr
			} else {
				err = fmt.Errorf("%w; release artifact lease: %v", err, releaseErr)
			}
		}
		return artifactPublicationOutcome{dir: dir, err: err}
	})
	return r.publisher
}

func (r *artifactRun) discard(deadline time.Time) error {
	if r == nil {
		return nil
	}
	if err := ensureBefore(deadline, "artifact staging cleanup"); err != nil {
		return err
	}
	if err := r.release(); err != nil {
		return err
	}
	if r.published {
		return nil
	}
	return removeTreeUntil(r.staging, deadline)
}

func (r *artifactRun) release() error {
	if r == nil || r.lease == nil {
		return nil
	}
	err := r.lease.release()
	r.lease = nil
	return err
}

func (s artifactStore) acquireLatestLeaseUntil(deadline time.Time) (*fileLease, error) {
	for {
		if err := ensureBefore(deadline, "artifact latest lease acquisition"); err != nil {
			return nil, err
		}
		lease, err := acquireFileLease(s.latestLeasePath())
		if err == nil {
			return lease, nil
		}
		if !errors.Is(err, errLeaseContended) {
			return nil, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, err
		}
		wait := 5 * time.Millisecond
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		<-timer.C
	}
}

func (s artifactStore) writeLatest(id uuid.UUID) error {
	pointer, err := json.Marshal(artifactLatestPointer{Run: filepath.ToSlash(filepath.Join("runs", id.String()))})
	if err != nil {
		return fmt.Errorf("marshal latest artifact pointer: %w", err)
	}
	staging, err := os.CreateTemp(s.root, "latest-*.json.staging")
	if err != nil {
		return fmt.Errorf("create latest artifact pointer staging: %w", err)
	}
	stagingPath := staging.Name()
	cleanupStaging := func(cause error) error {
		if removeErr := os.Remove(stagingPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("%w; remove latest artifact pointer staging: %v", cause, removeErr)
		}
		return cause
	}
	closeStaging := func(cause error) error {
		if closeErr := staging.Close(); closeErr != nil {
			return cleanupStaging(fmt.Errorf("%w; close latest artifact pointer staging: %v", cause, closeErr))
		}
		return cleanupStaging(cause)
	}
	if _, err := staging.Write(pointer); err != nil {
		return closeStaging(fmt.Errorf("write latest artifact pointer: %w", err))
	}
	if err := staging.Chmod(0o600); err != nil {
		return closeStaging(fmt.Errorf("chmod latest artifact pointer: %w", err))
	}
	if err := staging.Close(); err != nil {
		return cleanupStaging(fmt.Errorf("close latest artifact pointer: %w", err))
	}
	if err := os.Rename(stagingPath, filepath.Join(s.root, "latest.json")); err != nil {
		return cleanupStaging(fmt.Errorf("publish latest artifact pointer: %w", err))
	}
	return nil
}

func (s artifactStore) pruneInactiveCompletedRuns(current uuid.UUID, deadline time.Time) error {
	entries, err := os.ReadDir(s.runsRoot())
	if err != nil {
		return fmt.Errorf("read artifact runs: %w", err)
	}
	for _, entry := range entries {
		if err := ensureBefore(deadline, "artifact retention prune"); err != nil {
			return err
		}
		if !entry.IsDir() || entry.Name() == current.String() || filepath.Ext(entry.Name()) == ".staging" {
			continue
		}
		if _, err := uuid.Parse(entry.Name()); err != nil {
			continue
		}
		runPath := filepath.Join(s.runsRoot(), entry.Name())
		lease, err := acquireFileLease(filepath.Join(runPath, artifactLeaseFileName))
		if errors.Is(err, errLeaseContended) {
			continue
		}
		if err != nil {
			return fmt.Errorf("acquire artifact lease for prune %s: %w", entry.Name(), err)
		}
		if err := removeTreeUntil(runPath, deadline); err != nil {
			releaseErr := lease.release()
			if releaseErr != nil {
				return fmt.Errorf("prune inactive artifact run %s: %w; release artifact lease: %v", entry.Name(), err, releaseErr)
			}
			return fmt.Errorf("prune inactive artifact run %s: %w", entry.Name(), err)
		}
		if err := lease.release(); err != nil {
			return fmt.Errorf("release artifact lease for prune %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func ensureBefore(deadline time.Time, operation string) error {
	if !time.Now().Before(deadline) {
		return fmt.Errorf("%s deadline elapsed", operation)
	}
	return nil
}

var errLeaseContended = errors.New("artifact lease contended")

type fileLease struct {
	file *os.File
}

func acquireFileLease(path string) (*fileLease, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open artifact lease %s: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			if closeErr != nil {
				return nil, fmt.Errorf("%w; close contended artifact lease: %v", errLeaseContended, closeErr)
			}
			return nil, errLeaseContended
		}
		if closeErr != nil {
			return nil, fmt.Errorf("acquire artifact lease: %w; close lease: %v", err, closeErr)
		}
		return nil, fmt.Errorf("acquire artifact lease: %w", err)
	}
	return &fileLease{file: file}, nil
}

func (l *fileLease) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil && closeErr != nil {
		return fmt.Errorf("release artifact lease: %w; close lease: %v", unlockErr, closeErr)
	}
	if unlockErr != nil {
		return fmt.Errorf("release artifact lease: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close artifact lease: %w", closeErr)
	}
	return nil
}
