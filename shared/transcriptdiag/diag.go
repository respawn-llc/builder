package transcriptdiag

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"core/shared/clientui"
)

func EnabledFromEnv(getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	value := strings.TrimSpace(getenv("KENT_TRANSCRIPT_DIAGNOSTICS"))
	if value == "" {
		return false
	}
	switch strings.ToLower(value) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

func Enabled(debug bool, getenv func(string) string) bool {
	if debug {
		return true
	}
	return EnabledFromEnv(getenv)
}

func EntriesDigest(entries []clientui.ChatEntry) string {
	if len(entries) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		toolName := ""
		if entry.ToolCall != nil {
			toolName = entry.ToolCall.ToolName
		}
		parts = append(parts, strings.Join([]string{
			entry.Role,
			entry.Phase,
			entry.ToolCallID,
			toolName,
			entry.Text,
			entry.CondensedText,
		}, "\x1f"))
	}
	return digest(parts)
}

func EventDigest(evt clientui.Event) string {
	parts := []string{
		string(evt.Kind),
		evt.StepID,
		evt.AssistantDelta,
		evt.UserMessage,
		strings.Join(evt.UserMessageBatch, "\x1e"),
		EntriesDigest(evt.TranscriptEntries),
	}
	if evt.ReasoningDelta != nil {
		parts = append(parts, evt.ReasoningDelta.Key, evt.ReasoningDelta.Role, evt.ReasoningDelta.Text)
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
		parts = append(parts, evt.Background.Type, evt.Background.ID, evt.Background.State, evt.Background.Command, evt.Background.Preview)
	}
	return digest(parts)
}

func AddEntriesFields(fields map[string]string, entries []clientui.ChatEntry) map[string]string {
	if fields == nil {
		fields = map[string]string{}
	}
	fields["entries_count"] = fmt.Sprint(len(entries))
	fields["entries_digest"] = EntriesDigest(entries)
	return fields
}

func FormatLine(name string, fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)+1)
	parts = append(parts, strings.TrimSpace(name))
	for _, key := range keys {
		value := strings.TrimSpace(fields[key])
		if value == "" {
			continue
		}
		if strings.ContainsAny(value, " \t\n\r\"") {
			parts = append(parts, fmt.Sprintf("%s=%q", key, value))
		} else {
			parts = append(parts, key+"="+value)
		}
	}
	return strings.Join(parts, " ")
}

func digest(parts []string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1d")))
	return hex.EncodeToString(sum[:8])
}
