package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"core/server/tools"
	"core/server/tools/shell/postprocess"
	"core/shared/config"
)

type Manager struct {
	mu                  sync.Mutex
	nextID              int
	entries             map[string]*processEntry
	tempDir             string
	onEvent             func(Event)
	minimumExecToBgTime time.Duration
	closeGracePeriod    time.Duration
	closeWaitTimeout    time.Duration
	postprocessor       *postprocess.Runner
	closed              bool
}

type ManagerOption func(*Manager)

func WithMinimumExecToBgTime(value time.Duration) ManagerOption {
	return func(m *Manager) {
		if value > 0 {
			m.minimumExecToBgTime = value
		}
	}
}

func WithCloseTimeouts(gracePeriod, waitTimeout time.Duration) ManagerOption {
	return func(m *Manager) {
		if gracePeriod > 0 {
			m.closeGracePeriod = gracePeriod
		}
		if waitTimeout > 0 {
			m.closeWaitTimeout = waitTimeout
		}
	}
}

func WithPostprocessor(runner *postprocess.Runner) ManagerOption {
	return func(m *Manager) {
		m.postprocessor = runner
	}
}

func NewManager(opts ...ManagerOption) (*Manager, error) {
	tempDir, err := os.MkdirTemp("", backgroundLogDirPrefix)
	if err != nil {
		return nil, fmt.Errorf("create background shell temp dir: %w", err)
	}
	mgr := &Manager{
		nextID:              initialProcessID,
		entries:             make(map[string]*processEntry),
		tempDir:             tempDir,
		minimumExecToBgTime: defaultMinimumExecToBgTime,
		closeGracePeriod:    closeGracePeriod,
		closeWaitTimeout:    closeWaitTimeout,
		postprocessor:       postprocess.NewRunner(postprocess.Settings{Mode: config.ShellPostprocessingModeBuiltin}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(mgr)
		}
	}
	if mgr.minimumExecToBgTime <= 0 {
		mgr.minimumExecToBgTime = defaultMinimumExecToBgTime
	}
	return mgr, nil
}

func (m *Manager) TempDir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tempDir
}

func (m *Manager) SetEventHandler(handler func(Event)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onEvent = handler
}

func (m *Manager) SetMinimumExecToBgTime(value time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if value <= 0 {
		m.minimumExecToBgTime = defaultMinimumExecToBgTime
		return
	}
	m.minimumExecToBgTime = value
}

func (m *Manager) Start(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if len(req.Command) == 0 {
		return ExecResult{}, errors.New("command is required")
	}
	workdir := strings.TrimSpace(req.Workdir)
	if workdir == "" {
		return ExecResult{}, errors.New("workdir is required")
	}
	yieldTime := m.normalizeExecYieldTime(req.YieldTime)
	maxOutputChars := req.MaxOutputChars
	if maxOutputChars <= 0 {
		maxOutputChars = defaultOutputTokenCap * 4
	}

	id, logPath, err := m.allocateProcessSlot()
	if err != nil {
		return ExecResult{}, err
	}
	cmd := exec.CommandContext(context.Background(), req.Command[0], req.Command[1:]...)
	cmd.Dir = workdir
	ownerSessionID := strings.TrimSpace(req.OwnerSessionID)
	ownerRunID := strings.TrimSpace(req.OwnerRunID)
	ownerStepID := strings.TrimSpace(req.OwnerStepID)
	cmd.Env = tools.EnrichShellEnvForSession(os.Environ(), ownerSessionID)
	prepareManagedExec(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ExecResult{}, fmt.Errorf("open stdin pipe: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return ExecResult{}, fmt.Errorf("open log file: %w", err)
	}
	entry := &processEntry{
		id:             id,
		ownerSessionID: ownerSessionID,
		ownerRunID:     ownerRunID,
		ownerStepID:    ownerStepID,
		command:        strings.TrimSpace(req.DisplayCommand),
		workdir:        workdir,
		raw:            req.Raw,
		preserveOutput: m.preserveRawOutput(req.Raw),
		startedAt:      time.Now().UTC(),
		lastUpdatedAt:  time.Now().UTC(),
		state:          "starting",
		logPath:        logPath,
		cmd:            cmd,
		stdin:          stdin,
		running:        true,
		stdinOpen:      req.KeepStdinOpen,
		notify:         make(chan struct{}, 1),
		done:           make(chan struct{}),
	}
	entry.log = newAsyncLogWriter(logFile, entry.signal)
	if entry.command == "" {
		entry.command = strings.Join(req.Command, " ")
	}
	writer := &outputWriter{entry: entry}
	cmd.Stdout = writer
	cmd.Stderr = writer
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		entry.mu.Lock()
		stdin, log := entry.detachResourcesLocked()
		entry.mu.Unlock()
		closeDetachedResources(stdin, log)
		return ExecResult{}, errors.New("background shell manager is closed")
	}
	m.mu.Unlock()

	if err := cmd.Start(); err != nil {
		entry.mu.Lock()
		stdin, log := entry.detachResourcesLocked()
		entry.mu.Unlock()
		closeDetachedResources(stdin, log)
		m.releaseEntry(id)
		return ExecResult{}, fmt.Errorf("start process: %w", err)
	}
	if !req.KeepStdinOpen {
		if err := stdin.Close(); err != nil {
			_ = killManagedProcess(cmd.Process)
			gracePeriod := m.closeGracePeriod
			if gracePeriod <= 0 {
				gracePeriod = closeGracePeriod
			}
			_, _ = m.collectUntil(context.Background(), entry, time.Now().Add(gracePeriod))
			entry.mu.Lock()
			stdin, log := entry.detachResourcesLocked()
			entry.mu.Unlock()
			closeDetachedResources(stdin, log)
			m.releaseEntry(id)
			return ExecResult{}, fmt.Errorf("close stdin: %w", err)
		}
		entry.stdin = nil
	}
	entry.state = "running"

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = killManagedProcess(cmd.Process)
		gracePeriod := m.closeGracePeriod
		if gracePeriod <= 0 {
			gracePeriod = closeGracePeriod
		}
		_, _ = m.collectUntil(context.Background(), entry, time.Now().Add(gracePeriod))
		entry.mu.Lock()
		stdin, log := entry.detachResourcesLocked()
		entry.mu.Unlock()
		closeDetachedResources(stdin, log)
		return ExecResult{}, errors.New("background shell manager is closed")
	}
	m.entries[id] = entry
	m.mu.Unlock()

	go m.waitForExit(entry)

	start := time.Now()
	output, err := m.collectUntil(ctx, entry, time.Now().Add(yieldTime))
	if err != nil {
		_ = killManagedProcess(cmd.Process)
		return ExecResult{}, err
	}
	result := ExecResult{
		SessionID:          id,
		WallTime:           time.Since(start),
		OutputPath:         logPath,
		RawOutputRequested: req.Raw,
	}
	snapshot, backgrounded := entry.transitionToBackground()
	if !backgrounded {
		if pending := entry.drainPending(); len(pending) > 0 {
			output = append(output, pending...)
		}
		processed, err := m.applyPostprocessing(ctx, entry, string(output), snapshot.ExitCode, false, maxOutputChars)
		if err != nil {
			return ExecResult{}, err
		}
		display, truncated, _ := truncateWithTemplate(processed.Output, maxOutputChars, truncationBannerTemplate)
		result.ExitCode = postprocess.CloneIntPtr(snapshot.ExitCode)
		result.Output = display
		result.Truncated = truncated
		result.Warning = processed.Warning
		result.ToolError = processed.UnrecoverableError
		m.releaseEntry(id)
		return result, nil
	}
	_ = deprioritizeManagedProcess(cmd.Process)
	processed, err := m.applyPostprocessing(ctx, entry, string(output), nil, true, maxOutputChars)
	if err != nil {
		return ExecResult{}, err
	}
	display, truncated, _ := truncateWithTemplate(processed.Output, maxOutputChars, backgroundTruncationBannerTemplate)
	result.Running = true
	result.Backgrounded = true
	result.MovedToBackground = true
	result.Output = display
	result.Truncated = truncated
	result.Warning = processed.Warning
	result.ToolError = processed.UnrecoverableError
	m.emitEvent(Event{Type: EventBackgrounded, Snapshot: snapshot})
	return result, nil
}

func (m *Manager) preserveRawOutput(raw bool) bool {
	if raw || m == nil || m.postprocessor == nil {
		return true
	}
	return m.postprocessor.PreservesRawOutput(false)
}

func (m *Manager) WriteStdin(ctx context.Context, req WriteRequest) (ExecResult, error) {
	id := strings.TrimSpace(req.SessionID)
	if id == "" {
		return ExecResult{}, errors.New("session_id is required")
	}
	entry, err := m.entry(id)
	if err != nil {
		return ExecResult{}, err
	}
	entry.interactMu.Lock()
	defer entry.interactMu.Unlock()

	yieldTime := normalizeWriteYieldTime(req.YieldTime, defaultWriteYieldTime)
	maxOutputChars := req.MaxOutputChars
	if maxOutputChars <= 0 {
		maxOutputChars = defaultOutputTokenCap * 4
	}
	if req.Input != "" {
		entry.mu.Lock()
		stdin := entry.stdin
		running := entry.running
		stdinOpen := entry.stdinOpen
		entry.mu.Unlock()
		if !running {
			return ExecResult{}, fmt.Errorf("unknown session_id %s", id)
		}
		if stdin == nil || !stdinOpen {
			return ExecResult{}, fmt.Errorf("stdin is closed for session %s", id)
		}
		if _, err := io.WriteString(stdin, req.Input); err != nil {
			return ExecResult{}, fmt.Errorf("write stdin: %w", err)
		}
	}

	start := time.Now()
	output, err := m.collectUntil(ctx, entry, time.Now().Add(yieldTime))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return ExecResult{}, &PollingCanceledError{SessionID: id, Active: entry.snapshot().Running}
		}
		return ExecResult{}, err
	}
	snapshot := entry.snapshot()
	consumedCompletion := false
	warning := ""
	sourceTruncated := false
	var processed postprocess.Result
	if snapshot.Backgrounded && snapshot.ExitCode != nil && !entry.completionNoticeConsumed() {
		fullOutput, readErr := readOutputFileLimited(snapshot.LogPath, maxFullLogPostprocessBytes)
		if readErr == nil {
			processed, err = m.applyPostprocessing(ctx, entry, fullOutput, snapshot.ExitCode, true, maxOutputChars)
			if err != nil {
				return ExecResult{}, err
			}
			consumedCompletion = true
		} else {
			var previewTruncated bool
			preview, _, previewTruncated, previewErr := readBackgroundSummaryFromFile(snapshot.LogPath, maxOutputChars, BackgroundOutputDefault, !snapshot.RawOutput)
			if previewErr == nil {
				processed = postprocess.Result{Output: preview}
				consumedCompletion = true
				sourceTruncated = previewTruncated
				warning = postprocess.JoinWarnings(warning, fmt.Sprintf("full output log skipped: %v", readErr))
			} else {
				warning = postprocess.JoinWarnings(warning, fmt.Sprintf("failed to read full output log: %v", readErr))
			}
		}
	}
	if !consumedCompletion {
		processed, err = m.applyPostprocessing(ctx, entry, string(output), snapshot.ExitCode, snapshot.Backgrounded, maxOutputChars)
		if err != nil {
			return ExecResult{}, err
		}
	}
	display, displayTruncated, _ := truncateWithTemplate(processed.Output, maxOutputChars, backgroundTruncationBannerTemplate)
	if snapshot.Backgrounded && snapshot.ExitCode != nil && consumedCompletion {
		entry.markCompletionNoticeConsumed()
	}
	return ExecResult{
		SessionID:          id,
		WallTime:           time.Since(start),
		Warning:            postprocess.JoinWarnings(warning, processed.Warning),
		ToolError:          processed.UnrecoverableError,
		Output:             display,
		OutputPath:         snapshot.LogPath,
		Running:            snapshot.Running,
		Backgrounded:       snapshot.Backgrounded,
		ExitCode:           postprocess.CloneIntPtr(snapshot.ExitCode),
		RawOutputRequested: snapshot.RawOutputRequested,
		Truncated:          sourceTruncated || displayTruncated,
	}, nil
}

func (m *Manager) Kill(id string) error {
	entry, err := m.entry(id)
	if err != nil {
		return err
	}
	entry.mu.Lock()
	entry.killRequested = true
	process := entry.cmd.Process
	entry.mu.Unlock()
	if process == nil {
		return fmt.Errorf("unknown session_id %s", id)
	}
	return killManagedProcess(process)
}

func (m *Manager) InlineOutput(id string, maxChars int) (string, string, error) {
	entry, err := m.entry(id)
	if err != nil {
		return "", "", err
	}
	maxOutputChars := maxChars
	if maxOutputChars <= 0 {
		maxOutputChars = defaultOutputTokenCap * 4
	}
	deadline := time.Now().Add(2 * logWriterFlushDelay)
	for {
		snapshot := entry.snapshot()
		preview, _, _, err := readBackgroundSummaryFromFile(snapshot.LogPath, maxOutputChars, BackgroundOutputDefault, !snapshot.RawOutput)
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(preview) != "" {
			return preview, snapshot.LogPath, nil
		}
		if strings.TrimSpace(snapshot.RecentOutput) != "" {
			recent, _, _ := truncateWithTemplate(snapshot.RecentOutput, maxOutputChars, backgroundTruncationBannerTemplate)
			return recent, snapshot.LogPath, nil
		}
		if !snapshot.Running || time.Now().After(deadline) {
			return preview, snapshot.LogPath, nil
		}
		time.Sleep(logWriterFlushDelay / 2)
	}
}
