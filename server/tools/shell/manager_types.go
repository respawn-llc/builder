package shell

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"core/server/tools/shell/postprocess"
)

const (
	defaultMinimumExecToBgTime = 15 * time.Second
	defaultWriteYieldTime      = 250 * time.Millisecond
	closeGracePeriod           = 1 * time.Second
	closeWaitTimeout           = 5 * time.Second
	minWriteYieldTime          = 250 * time.Millisecond
	defaultOutputTokenCap      = 10_000
	maxPendingOutputBytes      = 1 << 20
	maxRecentPreviewBytes      = 4096
	shellOutputNotifyInterval  = 50 * time.Millisecond
	maxFullLogPostprocessBytes = 2 << 20
	backgroundLogDirPrefix     = "kent-bg-shells-"
	initialProcessID           = 1000
)

type EventType string

const (
	EventBackgrounded EventType = "backgrounded"
	EventCompleted    EventType = "completed"
	EventKilled       EventType = "killed"
)

type Event struct {
	Type             EventType
	Snapshot         Snapshot
	Preview          string
	PreviewProcessed bool
	Removed          int
	NoticeSuppressed bool
}

type Snapshot struct {
	ID                      string
	OwnerSessionID          string
	OwnerRunID              string
	OwnerStepID             string
	State                   string
	Command                 string
	Workdir                 string
	StartedAt               time.Time
	FinishedAt              time.Time
	ExitCode                *int
	LogPath                 string
	RecentOutput            string
	OutputAvailable         bool
	OutputRetainedFromBytes int64
	OutputRetainedToBytes   int64
	RawOutputRequested      bool
	RawOutput               bool
	Running                 bool
	StdinOpen               bool
	Backgrounded            bool
	KillRequested           bool
	LastUpdatedAt           time.Time
}

type ExecRequest struct {
	Command        []string
	DisplayCommand string
	OwnerSessionID string
	OwnerRunID     string
	OwnerStepID    string
	Workdir        string
	YieldTime      time.Duration
	MaxOutputChars int
	KeepStdinOpen  bool
	Raw            bool
}

type ExecResult struct {
	SessionID          string
	WallTime           time.Duration
	Warning            string
	ToolError          string
	Output             string
	OutputPath         string
	ExitCode           *int
	Running            bool
	Backgrounded       bool
	MovedToBackground  bool
	RawOutputRequested bool
	Truncated          bool
}

type BackgroundNoticeSummary struct {
	DetailText    string
	CondensedText string
	LineCount     int
	Truncated     bool
	LogPath       string
}

type OutputChunk struct {
	ProcessID       string
	OffsetBytes     int64
	NextOffsetBytes int64
	Text            string
}

type OutputSubscription interface {
	Next(ctx context.Context) (OutputChunk, error)
	Close() error
}

type BackgroundOutputMode string

const (
	BackgroundOutputDefault BackgroundOutputMode = "default"
	BackgroundOutputVerbose BackgroundOutputMode = "verbose"
	BackgroundOutputConcise BackgroundOutputMode = "concise"
)

type BackgroundNoticeOptions struct {
	MaxChars          int
	SuccessOutputMode BackgroundOutputMode
}

type WriteRequest struct {
	SessionID      string
	Input          string
	YieldTime      time.Duration
	MaxOutputChars int
}

type PollingCanceledError struct {
	SessionID string
	Active    bool
}

func (e *PollingCanceledError) Error() string {
	state := "process finished"
	if e.Active {
		state = "process active"
	}
	if strings.TrimSpace(e.SessionID) == "" {
		return fmt.Sprintf("Canceled polling by user, %s", state)
	}
	return fmt.Sprintf("Canceled polling by user, %s (session_id %s)", state, strings.TrimSpace(e.SessionID))
}

func (e *PollingCanceledError) Unwrap() error {
	return context.Canceled
}

type processEntry struct {
	id             string
	ownerSessionID string
	ownerRunID     string
	ownerStepID    string
	command        string
	workdir        string
	raw            bool
	preserveOutput bool
	startedAt      time.Time
	finishedAt     time.Time
	exitCode       *int
	state          string
	backgrounded   bool
	logPath        string
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	log            *asyncLogWriter
	running        bool
	stdinOpen      bool
	lastUpdatedAt  time.Time
	lastSignaledAt time.Time
	recentOutput   []byte
	pendingOutput  []byte
	outputBytes    int64
	notify         chan struct{}
	done           chan struct{}
	killRequested  bool
	noticeConsumed bool
	mu             sync.Mutex
	interactMu     sync.Mutex
}

func (p *processEntry) signal() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

func (p *processEntry) snapshotLocked() Snapshot {
	recentOutput := string(p.recentOutput)
	if !p.preserveOutput {
		recentOutput = postprocess.SanitizeOutput(recentOutput)
	}
	return Snapshot{
		ID:                      p.id,
		OwnerSessionID:          p.ownerSessionID,
		OwnerRunID:              p.ownerRunID,
		OwnerStepID:             p.ownerStepID,
		State:                   p.state,
		Command:                 p.command,
		Workdir:                 p.workdir,
		StartedAt:               p.startedAt,
		FinishedAt:              p.finishedAt,
		ExitCode:                postprocess.CloneIntPtr(p.exitCode),
		LogPath:                 p.logPath,
		RecentOutput:            recentOutput,
		OutputAvailable:         p.logPath != "",
		OutputRetainedFromBytes: 0,
		OutputRetainedToBytes:   p.outputBytes,
		RawOutputRequested:      p.raw,
		RawOutput:               p.preserveOutput,
		Running:                 p.running,
		StdinOpen:               p.stdinOpen,
		Backgrounded:            p.backgrounded,
		KillRequested:           p.killRequested,
		LastUpdatedAt:           p.lastUpdatedAt,
	}
}

func (p *processEntry) detachResourcesLocked() (io.Closer, *asyncLogWriter) {
	stdin := p.stdin
	log := p.log
	p.stdin = nil
	p.log = nil
	return stdin, log
}

func closeDetachedResources(stdin io.Closer, log *asyncLogWriter) {
	if stdin != nil {
		_ = stdin.Close()
	}
	if log != nil {
		_ = log.Close()
	}
}

func (p *processEntry) writeOutput(chunk []byte) error {
	if len(chunk) == 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.log != nil {
		if err := p.log.Write(chunk); err != nil {
			return err
		}
	}
	p.outputBytes += int64(len(chunk))
	p.pendingOutput = append(p.pendingOutput, chunk...)
	if len(p.pendingOutput) > maxPendingOutputBytes {
		p.pendingOutput = append([]byte(nil), p.pendingOutput[len(p.pendingOutput)-maxPendingOutputBytes:]...)
	}
	p.recentOutput = append(p.recentOutput, chunk...)
	if len(p.recentOutput) > maxRecentPreviewBytes {
		p.recentOutput = append([]byte(nil), p.recentOutput[len(p.recentOutput)-maxRecentPreviewBytes:]...)
	}
	p.lastUpdatedAt = time.Now().UTC()
	if p.lastSignaledAt.IsZero() || p.lastUpdatedAt.Sub(p.lastSignaledAt) >= shellOutputNotifyInterval {
		p.lastSignaledAt = p.lastUpdatedAt
		p.signal()
	}
	return nil
}

func (p *processEntry) setExited(exitCode int, state string) {
	p.mu.Lock()
	p.running = false
	p.finishedAt = time.Now().UTC()
	p.lastUpdatedAt = p.finishedAt
	p.exitCode = &exitCode
	p.state = state
	stdin, log := p.detachResourcesLocked()
	p.mu.Unlock()
	closeDetachedResources(stdin, log)
	p.signal()
}

func (p *processEntry) isBackgrounded() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.backgrounded
}

func (p *processEntry) closeOnExit(exitCode int, state string) Snapshot {
	p.mu.Lock()
	p.finishedAt = time.Now().UTC()
	p.lastUpdatedAt = p.finishedAt
	p.exitCode = &exitCode
	p.state = state
	stdin, log := p.detachResourcesLocked()
	p.mu.Unlock()
	closeDetachedResources(stdin, log)
	p.mu.Lock()
	p.running = false
	snapshot := p.snapshotLocked()
	p.mu.Unlock()
	p.signal()
	return snapshot
}

func (p *processEntry) finalizeClosedExit() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = false
	p.signal()
}

func (p *processEntry) markCompletionNoticeConsumed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.backgrounded || p.exitCode == nil {
		return
	}
	p.noticeConsumed = true
}

func (p *processEntry) completionNoticeConsumed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.noticeConsumed
}

func (p *processEntry) snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshotLocked()
}

func (p *processEntry) drainPending() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pendingOutput) == 0 {
		return nil
	}
	out := append([]byte(nil), p.pendingOutput...)
	p.pendingOutput = nil
	return out
}

func (p *processEntry) isRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *processEntry) transitionToBackground() (Snapshot, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return p.snapshotLocked(), false
	}
	p.backgrounded = true
	p.state = "running"
	return p.snapshotLocked(), true
}

type outputWriter struct {
	entry *processEntry
}

func (w *outputWriter) Write(p []byte) (int, error) {
	if err := w.entry.writeOutput(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func normalizeWriteYieldTime(value time.Duration, fallback time.Duration) time.Duration {
	if value <= 0 {
		value = fallback
	}
	if value < minWriteYieldTime {
		return minWriteYieldTime
	}
	return value
}

func waitForEntryDone(entry *processEntry, timeout time.Duration) bool {
	if entry == nil {
		return true
	}
	if timeout <= 0 {
		select {
		case <-entry.done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-entry.done:
		return true
	case <-timer.C:
		return false
	}
}

func countOutputLines(text string) int {
	trimmed := strings.TrimRight(text, "\n")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}
