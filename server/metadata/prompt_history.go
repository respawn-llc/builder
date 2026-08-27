package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

type PromptHistoryEntry struct {
	SessionID string
	Text      string
	CreatedAt time.Time
}

type PromptHistoryRecord struct {
	Sequence  int64
	SessionID string
	Text      string
	CreatedAt time.Time
}

func (s *Store) ReadPromptHistory(ctx context.Context, sessionID string) ([]string, error) {
	if s == nil || s.queries == nil {
		return nil, errors.New("metadata store is required")
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return nil, errors.New("session_id is required")
	}
	history, err := s.queries.ListSessionPromptHistoryText(ctx, sqlitegen.ListSessionPromptHistoryTextParams{
		SessionID:  trimmedSessionID,
		MaxEntries: serverapi.SessionPromptHistoryMaxEntries,
	})
	if err != nil {
		return nil, fmt.Errorf("list prompt history: %w", err)
	}
	return history, nil
}

func (s *Store) RecordPromptHistoryEntry(ctx context.Context, entry PromptHistoryEntry) (PromptHistoryRecord, error) {
	if s == nil || s.queries == nil {
		return PromptHistoryRecord{}, errors.New("metadata store is required")
	}
	normalized, err := normalizePromptHistoryEntry(entry)
	if err != nil {
		return PromptHistoryRecord{}, err
	}
	inserted, err := s.queries.InsertSessionPromptHistoryEntry(ctx, sqlitegen.InsertSessionPromptHistoryEntryParams{
		SessionID:       normalized.SessionID,
		Text:            normalized.Text,
		CreatedAtUnixMs: normalized.CreatedAt.UTC().UnixMilli(),
	})
	if err != nil {
		return PromptHistoryRecord{}, fmt.Errorf("insert prompt history: %w", err)
	}
	return promptHistoryRecordFromRow(inserted), nil
}

func normalizePromptHistoryEntry(entry PromptHistoryEntry) (PromptHistoryEntry, error) {
	normalized := PromptHistoryEntry{
		SessionID: strings.TrimSpace(entry.SessionID),
		Text:      entry.Text,
		CreatedAt: entry.CreatedAt,
	}
	if normalized.CreatedAt.IsZero() {
		normalized.CreatedAt = time.Now().UTC()
	}
	if normalized.SessionID == "" {
		return PromptHistoryEntry{}, errors.New("session_id is required")
	}
	if strings.TrimSpace(normalized.Text) == "" {
		return PromptHistoryEntry{}, errors.New("text is required")
	}
	return normalized, nil
}

func promptHistoryRecordFromRow(row sqlitegen.SessionPromptHistoryEntry) PromptHistoryRecord {
	return PromptHistoryRecord{
		Sequence:  row.Sequence,
		SessionID: row.SessionID,
		Text:      row.Text,
		CreatedAt: time.UnixMilli(row.CreatedAtUnixMs).UTC(),
	}
}
