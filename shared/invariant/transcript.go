package invariant

import (
	"fmt"

	"core/shared/clientui"
	"core/shared/rollbacktarget"
	"core/shared/runtimeids"
)

func ValidateTranscriptCommittedRow(row clientui.TranscriptCommittedRow) error {
	if err := row.Validate(); err != nil {
		return err
	}
	if row.User != nil && row.User.RollbackTargetID != nil {
		if _, err := rollbacktarget.DecodeUserMessageSeq(*row.User.RollbackTargetID); err != nil {
			return fmt.Errorf("committed user row has invalid rollback_target_id: %w", err)
		}
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
	if page.LatestRollbackCandidate != nil {
		if err := page.LatestRollbackCandidate.Validate(); err != nil {
			return fmt.Errorf("transcript page latest rollback candidate: %w", err)
		}
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
