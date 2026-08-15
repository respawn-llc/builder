package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"core/server/tools/shell/postprocess"
)

func (m *Manager) List() []Snapshot {
	m.mu.Lock()
	entries := make([]*processEntry, 0, len(m.entries))
	for _, entry := range m.entries {
		entries = append(entries, entry)
	}
	m.mu.Unlock()
	out := make([]Snapshot, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.snapshot())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Running != out[j].Running {
			return out[i].Running
		}
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (m *Manager) Count() int {
	m.mu.Lock()
	entries := make([]*processEntry, 0, len(m.entries))
	for _, entry := range m.entries {
		entries = append(entries, entry)
	}
	m.mu.Unlock()
	count := 0
	for _, entry := range entries {
		if entry.isRunning() {
			count++
		}
	}
	return count
}

func (m *Manager) Snapshot(id string) (Snapshot, error) {
	entry, err := m.entry(strings.TrimSpace(id))
	if err != nil {
		return Snapshot{}, err
	}
	return entry.snapshot(), nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	entries := make([]*processEntry, 0, len(m.entries))
	for _, entry := range m.entries {
		entries = append(entries, entry)
	}
	m.mu.Unlock()

	for _, entry := range entries {
		entry.mu.Lock()
		if entry.stdin != nil {
			_ = entry.stdin.Close()
			entry.stdin = nil
			entry.stdinOpen = false
		}
		entry.mu.Unlock()
	}
	type closeTarget struct {
		entry       *processEntry
		process     *os.Process
		descendants []managedProcessIdentity
		rootExited  bool
	}
	targets := make([]closeTarget, 0, len(entries))
	var cleanupErrors []error
	for _, entry := range entries {
		entry.mu.Lock()
		process := entry.cmd.Process
		running := entry.running
		entry.mu.Unlock()
		if process != nil {
			var descendants []managedProcessIdentity
			var captureErr error
			if running {
				descendants, captureErr = captureManagedDescendants(process)
			}
			rootRunning := entry.isRunning()
			if !rootRunning {
				groupMembers, groupCaptureErr := captureCompletedManagedProcessGroup(process)
				descendants = mergeManagedProcessIdentities(descendants, groupMembers)
				captureErr = errors.Join(captureErr, groupCaptureErr)
			}
			if captureErr != nil {
				cleanupErrors = append(
					cleanupErrors,
					fmt.Errorf("capture descendants for background shell %s: %w", entry.id, captureErr),
				)
			}
			if !rootRunning && len(descendants) == 0 {
				continue
			}
			targets = append(targets, closeTarget{
				entry:       entry,
				process:     process,
				descendants: descendants,
				rootExited:  !rootRunning,
			})
			var terminateErr error
			if rootRunning {
				terminateErr = terminateManagedProcess(process, descendants)
			} else {
				terminateErr = terminateManagedDescendants(descendants)
			}
			if terminateErr != nil {
				cleanupErrors = append(
					cleanupErrors,
					fmt.Errorf("terminate background shell %s: %w", entry.id, terminateErr),
				)
			}
		}
	}

	gracePeriod := m.closeGracePeriod
	if gracePeriod <= 0 {
		gracePeriod = closeGracePeriod
	}
	graceDeadline := time.Now().Add(gracePeriod)
	for index := range targets {
		if targets[index].rootExited {
			continue
		}
		targets[index].rootExited = waitForEntryExit(targets[index].entry, time.Until(graceDeadline))
	}
	hasDescendants := false
	for _, target := range targets {
		if len(target.descendants) > 0 {
			hasDescendants = true
			break
		}
	}
	for time.Now().Before(graceDeadline) {
		if !hasDescendants {
			break
		}
		processes, snapshotErr := managedProcessSnapshot()
		if snapshotErr != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("probe background shell descendants: %w", snapshotErr))
			break
		}
		allDescendantsExited := true
		for _, target := range targets {
			if len(livingManagedDescendantPIDsIn(target.descendants, processes)) > 0 {
				allDescendantsExited = false
				break
			}
		}
		if allDescendantsExited {
			break
		}
		time.Sleep(min(10*time.Millisecond, time.Until(graceDeadline)))
	}
	var currentProcesses map[int]managedProcessSnapshotEntry
	var probeErr error
	if hasDescendants {
		currentProcesses, probeErr = managedProcessSnapshot()
		if probeErr != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("probe background shell descendants: %w", probeErr))
		}
	}
	for _, target := range targets {
		livingDescendants := livingManagedDescendantPIDsIn(target.descendants, currentProcesses)
		if len(livingDescendants) > 0 {
			if forceErr := forceKillManagedDescendants(livingDescendants); forceErr != nil {
				cleanupErrors = append(
					cleanupErrors,
					fmt.Errorf("kill descendants for background shell %s: %w", target.entry.id, forceErr),
				)
			}
		}
		rootExited := target.rootExited || waitForEntryExit(target.entry, 0)
		if !rootExited {
			if forceErr := forceKillManagedRoot(target.process); forceErr != nil {
				cleanupErrors = append(
					cleanupErrors,
					fmt.Errorf("kill background shell %s: %w", target.entry.id, forceErr),
				)
			}
		}
	}

	waitTimeout := m.closeWaitTimeout
	if waitTimeout <= 0 {
		waitTimeout = closeWaitTimeout
	}
	deadline := time.Now().Add(waitTimeout)
	pending := make([]string, 0)
	for _, entry := range entries {
		if waitForEntryExit(entry, time.Until(deadline)) {
			continue
		}
		pending = append(pending, entry.id)
	}
	if len(pending) > 0 {
		cleanupErrors = append(
			cleanupErrors,
			fmt.Errorf("timed out waiting for background shells to exit: %s", strings.Join(pending, ", ")),
		)
	}
	return errors.Join(cleanupErrors...)
}

func (m *Manager) entry(id string) (*processEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[id]
	if !ok {
		return nil, ErrResultUnavailable
	}
	m.touchCompletedLocked(id)
	return entry, nil
}

func (m *Manager) emitEvent(evt Event) bool {
	m.mu.Lock()
	handler := m.onEvent
	m.mu.Unlock()
	if handler == nil {
		return false
	}
	return handler(evt)
}

func (m *Manager) waitForExit(entry *processEntry) {
	defer close(entry.done)
	err := entry.cmd.Wait()
	exitCode, state := processExitState(err)
	if !entry.isBackgrounded() {
		entry.setExited(exitCode, state)
		m.releaseEntry(entry.id)
		return
	}
	snapshot := entry.closeOnExit(exitCode, state)
	eventType := EventCompleted
	if state == "killed" {
		eventType = EventKilled
	}
	m.retainCompletedEntry(entry.id)
	event := m.buildTerminalEvent(entry, eventType, snapshot)
	m.emitCompletionEvent(entry, event)
	entry.signal()
}

func (m *Manager) buildTerminalEvent(entry *processEntry, eventType EventType, snapshot Snapshot) Event {
	fullOutput, readErr := readOutputFileLimited(entry.logPath, maxFullLogPostprocessBytes)
	if readErr == nil {
		processed, postprocessErr := m.applyPostprocessing(context.Background(), entry, fullOutput, snapshot.ExitCode, true, defaultLimit)
		if postprocessErr == nil && processed.Processed && strings.TrimSpace(processed.UnrecoverableError) == "" {
			return newFinalizedBackgroundEvent(eventType, snapshot, processed.Output, processed.Warning, false)
		}
		warning := processed.Warning
		if postprocessErr != nil {
			warning, postprocessErr = mergeOperationalWarning(warning, fmt.Sprintf("background postprocess failed: %v", postprocessErr))
			if postprocessErr != nil {
				return Event{Type: eventType, Snapshot: snapshot}
			}
		} else if strings.TrimSpace(processed.UnrecoverableError) != "" {
			warning, postprocessErr = mergeOperationalWarning(warning, processed.UnrecoverableError)
			if postprocessErr != nil {
				return Event{Type: eventType, Snapshot: snapshot}
			}
		}
		return m.fallbackOrBareBackgroundEvent(eventType, snapshot, warning)
	}
	warning, warningErr := mergeOperationalWarning(nil, fmt.Sprintf("full output log skipped: %v", readErr))
	if warningErr != nil {
		return Event{Type: eventType, Snapshot: snapshot}
	}
	return m.fallbackOrBareBackgroundEvent(eventType, snapshot, warning)
}

func (m *Manager) fallbackOrBareBackgroundEvent(eventType EventType, snapshot Snapshot, warning postprocess.Warning) Event {
	fallback, fallbackErr := m.fallbackBackgroundEvent(eventType, snapshot, warning)
	if fallbackErr != nil {
		return Event{Type: eventType, Snapshot: snapshot}
	}
	return fallback
}

func (m *Manager) fallbackBackgroundEvent(eventType EventType, snapshot Snapshot, warning postprocess.Warning) (Event, error) {
	preview, _, truncated, err := readBackgroundSummaryFromFile(snapshot.LogPath, defaultLimit, BackgroundOutputDefault, !snapshot.RawOutput)
	if err != nil {
		warning, err = mergeOperationalWarning(warning, fmt.Sprintf("failed to read output preview: %v", err))
		if err != nil {
			return Event{}, err
		}
		preview = ""
	}
	preview = limitModelVisibleFallbackOutput(preview, snapshot.RawOutput)
	removed := 0
	if truncated {
		removed = 1
	}
	return newFallbackBackgroundEvent(eventType, snapshot, preview, warning, removed, false), nil
}

func (m *Manager) emitCompletionEvent(entry *processEntry, event Event) {
	entry.interactMu.Lock()
	defer entry.interactMu.Unlock()
	delivered := m.emitEvent(event)
	entry.mu.Lock()
	entry.terminalDelivered = entry.terminalDelivered || delivered
	entry.mu.Unlock()
	if delivered {
		return
	}
	cached := cacheTerminalEvent(entry, event)
	entry.mu.Lock()
	entry.terminalEvent = cached
	entry.mu.Unlock()
}

func (m *Manager) RetryTerminalEvents(ownerSessionID string) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return
	}
	m.mu.Lock()
	entries := make([]*processEntry, 0, len(m.entries))
	for _, entry := range m.entries {
		entries = append(entries, entry)
	}
	m.mu.Unlock()
	for _, entry := range entries {
		entry.mu.Lock()
		pending := entry.ownerSessionID == ownerSessionID &&
			entry.backgrounded &&
			!entry.running &&
			!entry.terminalDelivered
		entry.mu.Unlock()
		if pending {
			m.retryTerminalEvent(entry, ownerSessionID)
		}
	}
}

func (m *Manager) retryTerminalEvent(entry *processEntry, ownerSessionID string) {
	entry.interactMu.Lock()
	defer entry.interactMu.Unlock()
	entry.mu.Lock()
	if entry.ownerSessionID != ownerSessionID ||
		entry.terminalDelivered {
		entry.mu.Unlock()
		return
	}
	cached := entry.terminalEvent
	entry.mu.Unlock()
	if cached == nil {
		return
	}
	event := cached.event()
	delivered := m.emitEvent(event)
	entry.mu.Lock()
	entry.terminalDelivered = entry.terminalDelivered || delivered
	if delivered {
		entry.terminalEvent = nil
	}
	entry.mu.Unlock()
}

func cacheTerminalEvent(entry *processEntry, event Event) *terminalEventCache {
	cached := &terminalEventCache{
		eventType:        event.Type,
		snapshot:         snapshotWithExecutionCorrelationCopy(event.Snapshot),
		noticeSuppressed: event.NoticeSuppressed,
	}
	if event.completion == nil {
		return cached
	}
	completion := &terminalCompletionCache{
		source:  event.completion.source,
		warning: event.completion.output.warning,
		removed: event.completion.removed,
	}
	cached.completion = completion
	output := event.completion.output.command
	if output == "" {
		return cached
	}
	if len(output) > maxFullLogPostprocessBytes {
		cached.err = fmt.Errorf("postprocessed terminal output exceeds cache limit %d", maxFullLogPostprocessBytes)
		return cached
	}
	outputPath := entry.logPath + ".completion"
	if err := os.WriteFile(outputPath, []byte(output), 0o600); err != nil {
		cached.err = fmt.Errorf("cache postprocessed terminal output: %w", err)
		return cached
	}
	completion.outputPath = &outputPath
	return cached
}

func (c *terminalEventCache) event() Event {
	if c == nil {
		panic("terminal event cache is required")
	}
	if c.err != nil {
		return c.fallbackEvent(c.err)
	}
	event := Event{
		Type:             c.eventType,
		Snapshot:         snapshotWithExecutionCorrelationCopy(c.snapshot),
		NoticeSuppressed: c.noticeSuppressed,
	}
	if c.completion == nil {
		return event
	}
	output := ""
	if c.completion.outputPath != nil {
		cachedOutput, err := readOutputFileLimited(*c.completion.outputPath, maxFullLogPostprocessBytes)
		if err != nil {
			return c.fallbackEvent(fmt.Errorf("read cached postprocessed terminal output: %w", err))
		}
		output = cachedOutput
	}
	event.completion = &completionOutput{
		source:  c.completion.source,
		output:  newVisibleShellOutput(output, c.completion.warning),
		removed: c.completion.removed,
	}
	return event
}

func (c *terminalEventCache) fallbackEvent(cause error) Event {
	warning, err := postprocess.NewWarning(fmt.Sprintf("background terminal delivery cache failed: %v", cause))
	if err != nil {
		return Event{
			Type:             c.eventType,
			Snapshot:         snapshotWithExecutionCorrelationCopy(c.snapshot),
			NoticeSuppressed: c.noticeSuppressed,
		}
	}
	return newFallbackBackgroundEvent(
		c.eventType,
		c.snapshot,
		limitModelVisibleFallbackOutput(c.snapshot.RecentOutput, c.snapshot.RawOutput),
		warning,
		1,
		c.noticeSuppressed,
	)
}

func (m *Manager) collectUntil(ctx context.Context, entry *processEntry, deadline time.Time) ([]byte, error) {
	var collected bytes.Buffer
	for {
		pending := entry.drainPending()
		if len(pending) > 0 {
			_, _ = collected.Write(pending)
		}
		if !entry.isRunning() {
			select {
			case <-entry.outputFinalized:
			case <-ctx.Done():
				return collected.Bytes(), ctx.Err()
			}
			if pending := entry.drainPending(); len(pending) > 0 {
				_, _ = collected.Write(pending)
			}
			return collected.Bytes(), nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return collected.Bytes(), nil
		}
		select {
		case <-ctx.Done():
			return collected.Bytes(), ctx.Err()
		case <-entry.notify:
		case <-time.After(remaining):
			return collected.Bytes(), nil
		}
	}
}

func (m *Manager) allocateProcessSlot() (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", "", errors.New("background shell manager is closed")
	}
	id := strconv.Itoa(m.nextID)
	m.nextID++
	return id, filepath.Join(m.tempDir, id+".log"), nil
}

func (m *Manager) releaseEntry(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, id)
	m.removeCompletedLocked(id)
}

func (m *Manager) retainCompletedEntry(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, exists := m.entries[id]
	if !exists || entry.isRunning() {
		return
	}
	m.removeCompletedLocked(id)
	m.completedRecency = append(m.completedRecency, id)
	for len(m.completedRecency) > completedProcessRetentionLimit {
		evictedID := m.completedRecency[0]
		m.completedRecency[0] = ""
		m.completedRecency = m.completedRecency[1:]
		evicted, exists := m.entries[evictedID]
		if exists && !evicted.isRunning() {
			delete(m.entries, evictedID)
		}
	}
}

func (m *Manager) touchCompletedLocked(id string) {
	for index, completedID := range m.completedRecency {
		if completedID != id {
			continue
		}
		copy(m.completedRecency[index:], m.completedRecency[index+1:])
		m.completedRecency[len(m.completedRecency)-1] = id
		return
	}
}

func (m *Manager) removeCompletedLocked(id string) {
	for index, completedID := range m.completedRecency {
		if completedID != id {
			continue
		}
		copy(m.completedRecency[index:], m.completedRecency[index+1:])
		last := len(m.completedRecency) - 1
		m.completedRecency[last] = ""
		m.completedRecency = m.completedRecency[:last]
		return
	}
}

func (m *Manager) normalizeExecYieldTime(value time.Duration) time.Duration {
	minimum := m.minimumExecToBgTimeOrDefault()
	if value <= 0 {
		value = minimum
	}
	if value < minimum {
		return minimum
	}
	return value
}

func (m *Manager) minimumExecToBgTimeOrDefault() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.minimumExecToBgTime <= 0 {
		return defaultMinimumExecToBgTime
	}
	return m.minimumExecToBgTime
}
