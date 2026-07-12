package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	for _, entry := range entries {
		entry.mu.Lock()
		process := entry.cmd.Process
		entry.mu.Unlock()
		if process != nil {
			_ = killManagedProcess(process)
		}
	}

	gracePeriod := m.closeGracePeriod
	if gracePeriod <= 0 {
		gracePeriod = closeGracePeriod
	}
	graceDeadline := time.Now().Add(gracePeriod)
	for _, entry := range entries {
		if waitForEntryDone(entry, time.Until(graceDeadline)) {
			continue
		}
		entry.mu.Lock()
		process := entry.cmd.Process
		entry.mu.Unlock()
		if process != nil {
			_ = forceKillManagedProcess(process)
		}
	}

	waitTimeout := m.closeWaitTimeout
	if waitTimeout <= 0 {
		waitTimeout = closeWaitTimeout
	}
	deadline := time.Now().Add(waitTimeout)
	pending := make([]string, 0)
	for _, entry := range entries {
		if waitForEntryDone(entry, time.Until(deadline)) {
			continue
		}
		pending = append(pending, entry.id)
	}
	if len(pending) > 0 {
		return fmt.Errorf("timed out waiting for background shells to exit: %s", strings.Join(pending, ", "))
	}
	return nil
}

func (m *Manager) entry(id string) (*processEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[id]
	if !ok {
		return nil, fmt.Errorf("unknown session_id %s", id)
	}
	return entry, nil
}

func (m *Manager) emitEvent(evt Event) {
	m.mu.Lock()
	handler := m.onEvent
	m.mu.Unlock()
	if handler != nil {
		handler(evt)
	}
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
	fullOutput, readErr := readOutputFileLimited(entry.logPath, maxFullLogPostprocessBytes)
	if readErr == nil {
		processed, postprocessErr := m.applyPostprocessing(context.Background(), entry, fullOutput, snapshot.ExitCode, true, defaultLimit)
		if postprocessErr == nil && processed.Processed && strings.TrimSpace(processed.UnrecoverableError) == "" {
			m.emitEvent(newFinalizedBackgroundEvent(eventType, snapshot, processed.Output, processed.Warning, completionNoticeSuppressed(entry)))
			entry.finalizeClosedExit()
			return
		}
		warning := processed.Warning
		if postprocessErr != nil {
			warning, postprocessErr = mergeOperationalWarning(warning, fmt.Sprintf("background postprocess failed: %v", postprocessErr))
			if postprocessErr != nil {
				m.emitEvent(Event{Type: eventType, Snapshot: snapshot, NoticeSuppressed: completionNoticeSuppressed(entry)})
				entry.finalizeClosedExit()
				return
			}
		} else if strings.TrimSpace(processed.UnrecoverableError) != "" {
			warning, postprocessErr = mergeOperationalWarning(warning, processed.UnrecoverableError)
			if postprocessErr != nil {
				m.emitEvent(Event{Type: eventType, Snapshot: snapshot, NoticeSuppressed: completionNoticeSuppressed(entry)})
				entry.finalizeClosedExit()
				return
			}
		}
		m.emitFallbackBackgroundEvent(entry, eventType, snapshot, warning)
		entry.finalizeClosedExit()
		return
	}
	warning, warningErr := mergeOperationalWarning(nil, fmt.Sprintf("full output log skipped: %v", readErr))
	if warningErr != nil {
		m.emitEvent(Event{Type: eventType, Snapshot: snapshot, NoticeSuppressed: completionNoticeSuppressed(entry)})
		entry.finalizeClosedExit()
		return
	}
	m.emitFallbackBackgroundEvent(entry, eventType, snapshot, warning)
	entry.finalizeClosedExit()
}

func (m *Manager) emitFallbackBackgroundEvent(entry *processEntry, eventType EventType, snapshot Snapshot, warning postprocess.Warning) {
	fallback, fallbackErr := m.fallbackBackgroundEvent(eventType, snapshot, warning)
	if fallbackErr != nil {
		m.emitEvent(Event{Type: eventType, Snapshot: snapshot, NoticeSuppressed: completionNoticeSuppressed(entry)})
	} else {
		fallback.NoticeSuppressed = completionNoticeSuppressed(entry)
		m.emitEvent(fallback)
	}
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
	removed := 0
	if truncated {
		removed = 1
	}
	return newFallbackBackgroundEvent(eventType, snapshot, preview, warning, removed, false), nil
}

func completionNoticeSuppressed(entry *processEntry) bool {
	entry.interactMu.Lock()
	defer entry.interactMu.Unlock()
	return entry.completionNoticeConsumed()
}

func (m *Manager) collectUntil(ctx context.Context, entry *processEntry, deadline time.Time) ([]byte, error) {
	var collected bytes.Buffer
	for {
		pending := entry.drainPending()
		if len(pending) > 0 {
			_, _ = collected.Write(pending)
		}
		if !entry.isRunning() {
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
