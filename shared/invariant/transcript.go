package invariant

import (
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func ValidateTranscriptCommittedRow(row clientui.TranscriptCommittedRow) error {
	if !row.Integrity.Valid() {
		return fmt.Errorf("committed row has invalid integrity %d", row.Integrity)
	}
	switch row.Visibility {
	case clientui.EntryVisibilityOngoing,
		clientui.EntryVisibilityOngoingCollapsed,
		clientui.EntryVisibilityDetail,
		clientui.EntryVisibilityHidden:
	default:
		return fmt.Errorf("committed row has unresolved visibility %q", row.Visibility)
	}

	payloads := 0
	expectedKind := clientui.TranscriptRowKind("")
	if row.User != nil {
		payloads++
		expectedKind = clientui.TranscriptRowUser
	}
	if row.Assistant != nil {
		payloads++
		expectedKind = clientui.TranscriptRowAssistant
	}
	if row.Tool != nil {
		payloads++
		expectedKind = clientui.TranscriptRowTool
	}
	if row.Notice != nil {
		payloads++
		expectedKind = clientui.TranscriptRowNotice
	}
	if payloads != 1 {
		return fmt.Errorf("committed row kind %q has %d payloads, want exactly one", row.Kind, payloads)
	}
	if row.Kind == "" {
		return fmt.Errorf("committed row kind is required")
	}
	if row.Kind != expectedKind {
		return fmt.Errorf("committed row kind %q does not match payload kind %q", row.Kind, expectedKind)
	}
	if row.Integrity != transcript.RowIntegrityValid {
		return nil
	}
	if row.Assistant != nil && row.Assistant.StreamID != nil && *row.Assistant.StreamID == uuid.Nil {
		return fmt.Errorf("committed assistant row has zero stream_id")
	}
	if row.Tool != nil && strings.TrimSpace(row.Tool.ToolCallID) == "" {
		return fmt.Errorf("committed tool row has empty tool_call_id")
	}
	return nil
}

func ValidateTranscriptPage(page clientui.TranscriptPage) error {
	if _, err := runtimeids.ParseSessionID(page.SessionID); err != nil {
		return fmt.Errorf("transcript page session identity: %w", err)
	}
	if err := validateTranscriptPageCursor("older", page.HasMoreAbove, page.OlderCursor); err != nil {
		return err
	}
	if err := validateTranscriptPageCursor("newer", page.HasMoreBelow, page.NewerCursor); err != nil {
		return err
	}
	for index, row := range page.Entries {
		if err := ValidateTranscriptCommittedRow(row); err != nil {
			return fmt.Errorf("transcript page entry %d: %w", index, err)
		}
	}
	return nil
}

func validateTranscriptPageCursor(name string, hasMore bool, cursor *int64) error {
	if !hasMore {
		if cursor != nil {
			return fmt.Errorf("transcript page %s cursor is present without more entries", name)
		}
		return nil
	}
	if cursor == nil {
		return fmt.Errorf("transcript page %s cursor is required when more entries exist", name)
	}
	if *cursor <= 0 {
		return fmt.Errorf("transcript page %s cursor must be positive, got %d", name, *cursor)
	}
	return nil
}
