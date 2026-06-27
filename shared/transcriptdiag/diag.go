package transcriptdiag

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
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

func PageDigest(page clientui.TranscriptPage) string {
	parts := []string{
		page.SessionID,
		strconv.FormatInt(page.Revision, 10),
		strconv.Itoa(page.StartEntryCount),
		strconv.Itoa(page.CommittedEntryCount),
		EntriesDigest(page.Entries),
		page.Streaming,
		page.StreamingError,
	}
	return digest(parts)
}

func RequestFields(req clientui.TranscriptPageRequest) map[string]string {
	fields := map[string]string{}
	if req.Cursor != 0 {
		fields["cursor"] = strconv.FormatInt(req.Cursor, 10)
	}
	return fields
}

func AddEntriesFields(fields map[string]string, entries []clientui.ChatEntry) map[string]string {
	if fields == nil {
		fields = map[string]string{}
	}
	fields["entries_count"] = strconv.Itoa(len(entries))
	fields["entries_digest"] = EntriesDigest(entries)
	return fields
}

func AddPageFields(fields map[string]string, page clientui.TranscriptPage) map[string]string {
	if fields == nil {
		fields = map[string]string{}
	}
	fields["revision"] = strconv.FormatInt(page.Revision, 10)
	fields["start_entry_count"] = strconv.Itoa(page.StartEntryCount)
	fields["committed_entry_count"] = strconv.Itoa(page.CommittedEntryCount)
	fields["streaming_chars"] = strconv.Itoa(len(page.Streaming))
	fields["page_digest"] = PageDigest(page)
	return AddEntriesFields(fields, page.Entries)
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
