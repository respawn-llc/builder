package runlog

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"core/server/runtime"
	"core/server/session"
	"core/shared/transcriptdiag"
)

const RunLogFileName = "steps.log"

type RunLogger struct {
	mu                   sync.Mutex
	fp                   writeStringCloser
	onDiagnostic         func(RunLoggerDiagnostic)
	reportedWriteFailure bool
}

type writeStringCloser interface {
	WriteString(string) (int, error)
	Close() error
}

type RunLoggerDiagnostic struct {
	Kind    string
	Message string
	Err     error
}

type DurabilityObserver struct {
	mu      sync.Mutex
	logger  *RunLogger
	pending []string
}

func NewDurabilityObserver() *DurabilityObserver {
	return &DurabilityObserver{}
}

func (o *DurabilityObserver) Attach(logger *RunLogger) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.logger = logger
	pending := append([]string(nil), o.pending...)
	o.pending = nil
	o.mu.Unlock()
	for _, line := range pending {
		logger.Logf("%s", line)
	}
}

func (o *DurabilityObserver) ObserveEventLogAppend(observation session.EventLogAppendObservation) {
	if o == nil {
		return
	}
	line := fmt.Sprintf(
		"durability.event_log.append records=%d latency=%s succeeded=%t",
		observation.RecordCount,
		observation.Latency,
		observation.Succeeded,
	)
	o.mu.Lock()
	logger := o.recordLineLocked(line)
	o.mu.Unlock()
	if logger != nil {
		logger.Logf("%s", line)
	}
}

func (o *DurabilityObserver) ObserveEventLogSync(observation session.EventLogSyncObservation) {
	if o == nil {
		return
	}
	line := fmt.Sprintf(
		"durability.event_log.sync latency=%s succeeded=%t",
		observation.Latency,
		observation.Succeeded,
	)
	o.mu.Lock()
	logger := o.recordLineLocked(line)
	o.mu.Unlock()
	if logger != nil {
		logger.Logf("%s", line)
	}
}

func (o *DurabilityObserver) ObserveResultGroupFlush(observation runtime.ResultGroupFlushObservation) {
	if o == nil {
		return
	}
	line := fmt.Sprintf(
		"durability.result_group.flush reason=%s results=%d records=%d latency=%s succeeded=%t",
		observation.Reason,
		observation.ResultCount,
		observation.RecordCount,
		observation.Latency,
		observation.Succeeded,
	)
	o.mu.Lock()
	logger := o.recordLineLocked(line)
	o.mu.Unlock()
	if logger != nil {
		logger.Logf("%s", line)
	}
}

func (o *DurabilityObserver) recordLineLocked(line string) *RunLogger {
	if o.logger == nil {
		o.pending = append(o.pending, line)
		return nil
	}
	return o.logger
}

func NewRunLogger(sessionDir string, onDiagnostic func(RunLoggerDiagnostic)) (*RunLogger, error) {
	fp, err := os.OpenFile(filepath.Join(sessionDir, RunLogFileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &RunLogger{onDiagnostic: onDiagnostic}, nil
		}
		return nil, fmt.Errorf("open run log: %w", err)
	}
	return &RunLogger{fp: fp, onDiagnostic: onDiagnostic}, nil
}

func (l *RunLogger) Close() error {
	if l == nil || l.fp == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fp.Close()
}

func (l *RunLogger) Logf(format string, args ...any) {
	if l == nil || l.fp == nil {
		return
	}
	line := fmt.Sprintf(format, args...)
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return
	}

	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.fp.WriteString(stamp + " " + line + "\n"); err != nil && !l.reportedWriteFailure {
		l.reportedWriteFailure = true
		if l.onDiagnostic != nil {
			l.onDiagnostic(RunLoggerDiagnostic{
				Kind:    "write_failed",
				Message: fmt.Sprintf("run log write failed; observability degraded: %v", err),
				Err:     err,
			})
		}
	}
}

func FormatConfigSourceLines(sources map[string]string) []string {
	keys := slices.Sorted(maps.Keys(sources))
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", key, strings.TrimSpace(sources[key])))
	}
	return lines
}

func FormatTranscriptRuntimeEventDiagnostic(sessionID string, evt runtime.Event) string {
	fields := map[string]string{
		"session_id":            strings.TrimSpace(sessionID),
		"path":                  "runtime_event",
		"kind":                  string(evt.Kind),
		"step_id":               strings.TrimSpace(evt.StepID),
		"event_digest":          runtimeEventDigest(evt),
		"assistant_delta_chars": fmt.Sprintf("%d", len(evt.AssistantDelta)),
	}
	if evt.ReasoningDelta != nil {
		fields["reasoning_role"] = strings.TrimSpace(evt.ReasoningDelta.Role)
		fields["reasoning_chars"] = fmt.Sprintf("%d", len(evt.ReasoningDelta.Text))
	}
	return transcriptdiag.FormatLine("transcript.diag.server.runtime_event", fields)
}

func runtimeEventDigest(evt runtime.Event) string {
	parts := []string{
		string(evt.Kind),
		evt.StepID,
		evt.AssistantDelta,
		evt.UserMessage,
		strings.Join(evt.UserMessageBatch, "\x1e"),
	}
	if evt.ReasoningDelta != nil {
		parts = append(parts, evt.ReasoningDelta.Role, evt.ReasoningDelta.Text)
	}
	if evt.RunState != nil {
		parts = append(
			parts,
			evt.RunState.RunID,
			string(evt.RunState.Status),
			string(evt.RunState.Lifecycle.Phase),
			string(evt.RunState.Lifecycle.Mode),
		)
	}
	if evt.Background != nil {
		parts = append(
			parts,
			string(evt.Background.Type),
			evt.Background.ID,
			evt.Background.State,
			evt.Background.Command,
			evt.Background.Preview,
		)
	}
	return transcriptdiag.Digest(parts...)
}

func FormatRuntimeEvent(evt runtime.Event) string {
	switch evt.Kind {
	case runtime.EventAssistantDelta:
		return fmt.Sprintf("runtime.event kind=%s step_id=%s delta_chars=%d", evt.Kind, evt.StepID, len(evt.AssistantDelta))
	case runtime.EventAssistantDeltaReset:
		return fmt.Sprintf("runtime.event kind=%s step_id=%s", evt.Kind, evt.StepID)
	case runtime.EventAssistantMessage:
		messageChars := 0
		if evt.Message.Content != nil {
			messageChars = len(*evt.Message.Content)
		}
		return fmt.Sprintf("runtime.event kind=%s step_id=%s message_chars=%d", evt.Kind, evt.StepID, messageChars)
	case runtime.EventModelResponse:
		if evt.ModelResponse != nil {
			return fmt.Sprintf(
				"runtime.event kind=%s step_id=%s phase=%s assistant_chars=%d tool_calls=%d output_items=%d output_types=%q",
				evt.Kind,
				evt.StepID,
				evt.ModelResponse.AssistantPhase,
				evt.ModelResponse.AssistantChars,
				evt.ModelResponse.ToolCallsCount,
				evt.ModelResponse.OutputItemsCount,
				strings.Join(evt.ModelResponse.OutputItemTypes, ","),
			)
		}
	case runtime.EventUserMessageFlushed:
		return fmt.Sprintf("runtime.event kind=%s step_id=%s user_chars=%d", evt.Kind, evt.StepID, len(evt.UserMessage))
	case runtime.EventToolCallStarted:
		if evt.ToolCall != nil {
			return fmt.Sprintf("runtime.event kind=%s step_id=%s call_id=%s name=%s", evt.Kind, evt.StepID, evt.ToolCall.ID, evt.ToolCall.Name)
		}
	case runtime.EventToolCallCompleted:
		if evt.ToolResult != nil {
			return fmt.Sprintf("runtime.event kind=%s step_id=%s call_id=%s name=%s is_error=%t", evt.Kind, evt.StepID, evt.ToolResult.CallID, evt.ToolResult.Name, evt.ToolResult.IsError)
		}
	case runtime.EventReviewerCompleted:
		if evt.Reviewer != nil {
			line := fmt.Sprintf(
				"runtime.event kind=%s step_id=%s outcome=%s suggestions=%d",
				evt.Kind,
				evt.StepID,
				evt.Reviewer.Outcome,
				evt.Reviewer.SuggestionsCount,
			)
			if strings.TrimSpace(evt.Reviewer.Error) != "" {
				line += fmt.Sprintf(" err=%q", evt.Reviewer.Error)
			}
			return line
		}
	case runtime.EventInFlightClearFailed, runtime.EventPromptHistoryPersistFailed:
		if strings.TrimSpace(evt.Error) != "" {
			return fmt.Sprintf("runtime.event kind=%s step_id=%s err=%q", evt.Kind, evt.StepID, evt.Error)
		}
	case runtime.EventCompactionStarted, runtime.EventCompactionCompleted, runtime.EventCompactionFailed:
		if evt.Compaction != nil {
			line := fmt.Sprintf(
				"runtime.event kind=%s step_id=%s mode=%s engine=%s provider=%s count=%d",
				evt.Kind,
				evt.StepID,
				evt.Compaction.Mode,
				evt.Compaction.Engine,
				evt.Compaction.Provider,
				evt.Compaction.Count,
			)
			if evt.Compaction.TrimmedItemsCount != nil {
				line += fmt.Sprintf(" trimmed=%d", *evt.Compaction.TrimmedItemsCount)
			}
			if strings.TrimSpace(evt.Compaction.Error) != "" {
				line += fmt.Sprintf(" err=%q", evt.Compaction.Error)
			}
			return line
		}
	case runtime.EventRunStateChanged:
		if evt.RunState != nil {
			return fmt.Sprintf("runtime.event kind=%s step_id=%s run_phase=%s", evt.Kind, evt.StepID, evt.RunState.Lifecycle.Phase)
		}
	case runtime.EventBackgroundUpdated:
		if evt.Background != nil {
			line := fmt.Sprintf("runtime.event kind=%s id=%s type=%s state=%s", evt.Kind, evt.Background.ID, evt.Background.Type, evt.Background.State)
			if evt.Background.ExitCode != nil {
				line += fmt.Sprintf(" exit_code=%d", *evt.Background.ExitCode)
			}
			return line
		}
	}
	return fmt.Sprintf("runtime.event kind=%s step_id=%s", evt.Kind, evt.StepID)
}
