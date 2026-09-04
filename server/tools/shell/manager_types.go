package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"core/server/tools/shell/postprocess"
	"core/shared/runtimeids"
	"core/shared/textutil"

	"github.com/google/uuid"
)

// ErrResultUnavailable means a shell ID is unknown or its completed result was evicted.
var ErrResultUnavailable = errors.New("shell result no longer available")

const (
	defaultMinimumExecToBgTime     = 15 * time.Second
	defaultWriteYieldTime          = 250 * time.Millisecond
	closeGracePeriod               = 1 * time.Second
	closeWaitTimeout               = 5 * time.Second
	minWriteYieldTime              = 250 * time.Millisecond
	defaultOutputTokenCap          = 10_000
	maxPendingOutputBytes          = 1 << 20
	maxRecentPreviewBytes          = 4096
	shellOutputNotifyInterval      = 50 * time.Millisecond
	maxFullLogPostprocessBytes     = 2 << 20
	completedProcessRetentionLimit = 1_000
	backgroundLogDirPrefix         = "kent-bg-shells-"
	initialProcessID               = 1000
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
	NoticeSuppressed bool
	completion       *completionOutput
}

type completionOutputSource uint8

const (
	completionOutputFinalized completionOutputSource = iota + 1
	completionOutputFallback
)

type completionOutput struct {
	source  completionOutputSource
	output  visibleShellOutput
	removed int
}

type terminalEventCache struct {
	eventType        EventType
	snapshot         Snapshot
	noticeSuppressed bool
	completion       *terminalCompletionCache
	err              error
}

type terminalCompletionCache struct {
	source     completionOutputSource
	outputPath *string
	warning    postprocess.Warning
	removed    int
}

func newBackgroundedEvent(snapshot Snapshot) Event {
	return Event{Type: EventBackgrounded, Snapshot: cloneSnapshot(snapshot)}
}

func newFinalizedBackgroundEvent(eventType EventType, snapshot Snapshot, output string, warning postprocess.Warning, noticeSuppressed bool) Event {
	return newTerminalBackgroundEvent(eventType, snapshot, completionOutput{
		source: completionOutputFinalized,
		output: newVisibleShellOutput(output, warning),
	}, noticeSuppressed)
}

func newFallbackBackgroundEvent(eventType EventType, snapshot Snapshot, output string, warning postprocess.Warning, removed int, noticeSuppressed bool) Event {
	if removed < 0 {
		panic("background fallback removal count must not be negative")
	}
	return newTerminalBackgroundEvent(eventType, snapshot, completionOutput{
		source:  completionOutputFallback,
		output:  newVisibleShellOutput(output, warning),
		removed: removed,
	}, noticeSuppressed)
}

func newTerminalBackgroundEvent(eventType EventType, snapshot Snapshot, output completionOutput, noticeSuppressed bool) Event {
	switch eventType {
	case EventCompleted, EventKilled:
	default:
		panic(fmt.Sprintf("terminal background event requires completed or killed type, got %q", eventType))
	}
	switch output.source {
	case completionOutputFinalized, completionOutputFallback:
	default:
		panic(fmt.Sprintf("terminal background event requires known output source, got %d", output.source))
	}
	return Event{
		Type:             eventType,
		Snapshot:         cloneSnapshot(snapshot),
		NoticeSuppressed: noticeSuppressed,
		completion:       &output,
	}
}

type Snapshot struct {
	ID                      string
	ActivityID              uuid.UUID
	OwnerSessionID          string
	OwnerRunID              string
	OwnerStepID             string
	ExecutionCorrelation    *runtimeids.ExecutionCorrelation
	State                   string
	Command                 string
	Workdir                 string
	StartedAt               time.Time
	FinishedAt              *time.Time
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
	Command              []string
	DisplayCommand       string
	OwnerSessionID       string
	OwnerRunID           string
	OwnerStepID          string
	ExecutionCorrelation *runtimeids.ExecutionCorrelation
	Workdir              string
	YieldTime            time.Duration
	MaxOutputChars       int
	KeepStdinOpen        bool
	Raw                  bool
	Postprocessor        *postprocess.Runner
}

type ExecResult struct {
	SessionID          string
	WallTime           time.Duration
	Warning            postprocess.Warning
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
	output        backgroundNoticeOutput
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
	id                   string
	activityID           uuid.UUID
	ownerSessionID       string
	ownerRunID           string
	ownerStepID          string
	executionCorrelation *runtimeids.ExecutionCorrelation
	command              string
	workdir              string
	raw                  bool
	postprocessor        *postprocess.Runner
	preserveOutput       bool
	startedAt            time.Time
	finishedAt           *time.Time
	exitCode             *int
	state                string
	backgrounded         bool
	logPath              string
	cmd                  *exec.Cmd
	stdin                io.WriteCloser
	log                  *asyncLogWriter
	running              bool
	stdinOpen            bool
	lastUpdatedAt        time.Time
	lastSignaledAt       time.Time
	recentOutput         []byte
	pendingOutput        []byte
	outputBytes          int64
	notify               chan struct{}
	done                 chan struct{}
	outputFinalized      chan struct{}
	killRequested        bool
	terminalEvent        *terminalEventCache
	terminalDelivered    bool
	mu                   sync.Mutex
	interactMu           sync.Mutex
	publishedSnapshot    atomic.Pointer[Snapshot]
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
		ActivityID:              p.activityID,
		OwnerSessionID:          p.ownerSessionID,
		OwnerRunID:              p.ownerRunID,
		OwnerStepID:             p.ownerStepID,
		ExecutionCorrelation:    cloneExecutionCorrelation(p.executionCorrelation),
		State:                   p.state,
		Command:                 p.command,
		Workdir:                 p.workdir,
		StartedAt:               p.startedAt,
		FinishedAt:              p.finishedAt,
		ExitCode:                textutil.Pointer(p.exitCode),
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

func cloneExecutionCorrelation(correlation *runtimeids.ExecutionCorrelation) *runtimeids.ExecutionCorrelation {
	if correlation == nil {
		return nil
	}
	copy := *correlation
	return &copy
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.ExecutionCorrelation = cloneExecutionCorrelation(snapshot.ExecutionCorrelation)
	snapshot.FinishedAt = textutil.Pointer(snapshot.FinishedAt)
	snapshot.ExitCode = textutil.Pointer(snapshot.ExitCode)
	return snapshot
}

func (p *processEntry) publishSnapshotLocked() Snapshot {
	snapshot := p.snapshotLocked()
	p.publishedSnapshot.Store(&snapshot)
	return snapshot
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

func (p *processEntry) finalizeOutput() {
	close(p.outputFinalized)
	p.signal()
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
		p.publishSnapshotLocked()
		p.signal()
	}
	return nil
}

func (p *processEntry) setExited(exitCode int, state string) {
	p.mu.Lock()
	p.running = false
	finishedAt := time.Now().UTC()
	p.finishedAt = &finishedAt
	p.lastUpdatedAt = finishedAt
	p.exitCode = &exitCode
	p.state = state
	stdin, log := p.detachResourcesLocked()
	p.publishSnapshotLocked()
	p.mu.Unlock()
	closeDetachedResources(stdin, log)
	p.finalizeOutput()
}

func (p *processEntry) isBackgrounded() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.backgrounded
}

func (p *processEntry) closeOnExit(exitCode int, state string) Snapshot {
	p.mu.Lock()
	p.running = false
	finishedAt := time.Now().UTC()
	p.finishedAt = &finishedAt
	p.lastUpdatedAt = finishedAt
	p.exitCode = &exitCode
	p.state = state
	stdin, log := p.detachResourcesLocked()
	p.mu.Unlock()
	closeDetachedResources(stdin, log)
	p.finalizeOutput()
	p.mu.Lock()
	p.running = false
	snapshot := p.publishSnapshotLocked()
	p.mu.Unlock()
	return snapshot
}

func (p *processEntry) snapshot() Snapshot {
	if p == nil {
		return Snapshot{}
	}
	snapshot := p.publishedSnapshot.Load()
	if snapshot == nil {
		panic("background shell process has no published snapshot")
	}
	return cloneSnapshot(*snapshot)
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
	return p.publishSnapshotLocked(), true
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

func waitForEntryExit(entry *processEntry, timeout time.Duration) bool {
	if entry == nil || !entry.isRunning() {
		return true
	}
	if timeout <= 0 {
		return false
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(min(5*time.Millisecond, time.Until(deadline)))
		if !entry.isRunning() {
			return true
		}
	}
	return !entry.isRunning()
}
