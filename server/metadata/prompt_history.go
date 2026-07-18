package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

var ErrPromptHistoryConflict = errors.New("prompt history conflict")

type PromptHistoryEntry struct {
	SessionID string
	SourceID  string
	Text      string
	CreatedAt time.Time
}

type PromptHistoryRecord struct {
	Sequence  int64
	SessionID string
	SourceID  string
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

func (s *Store) RecordPromptHistoryEntry(ctx context.Context, entry PromptHistoryEntry) (PromptHistoryRecord, bool, error) {
	if s == nil || s.queries == nil {
		return PromptHistoryRecord{}, false, errors.New("metadata store is required")
	}
	normalized, err := normalizePromptHistoryEntry(entry)
	if err != nil {
		return PromptHistoryRecord{}, false, err
	}
	inserted, err := s.queries.InsertSessionPromptHistoryEntry(ctx, sqlitegen.InsertSessionPromptHistoryEntryParams{
		SessionID:       normalized.SessionID,
		SourceID:        normalized.SourceID,
		Text:            normalized.Text,
		CreatedAtUnixMs: normalized.CreatedAt.UTC().UnixMilli(),
	})
	if err != nil {
		return PromptHistoryRecord{}, false, fmt.Errorf("insert prompt history: %w", err)
	}
	existing, err := s.promptHistoryRecordBySourceID(ctx, normalized.SessionID, normalized.SourceID)
	if err != nil {
		return PromptHistoryRecord{}, false, err
	}
	if inserted == 0 && !promptHistoryEquivalent(normalized, existing) {
		return PromptHistoryRecord{}, false, fmt.Errorf("%w: source_id=%q", ErrPromptHistoryConflict, normalized.SourceID)
	}
	return existing, inserted > 0, nil
}

func (s *Store) promptHistoryRecordBySourceID(ctx context.Context, sessionID string, sourceID string) (PromptHistoryRecord, error) {
	row, err := s.queries.GetSessionPromptHistoryEntryBySourceID(ctx, sqlitegen.GetSessionPromptHistoryEntryBySourceIDParams{
		SessionID: strings.TrimSpace(sessionID),
		SourceID:  strings.TrimSpace(sourceID),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PromptHistoryRecord{}, err
		}
		return PromptHistoryRecord{}, fmt.Errorf("get prompt history by source id: %w", err)
	}
	return promptHistoryRecordFromRow(row), nil
}

func normalizePromptHistoryEntry(entry PromptHistoryEntry) (PromptHistoryEntry, error) {
	normalized := PromptHistoryEntry{
		SessionID: strings.TrimSpace(entry.SessionID),
		SourceID:  strings.TrimSpace(entry.SourceID),
		Text:      entry.Text,
		CreatedAt: entry.CreatedAt,
	}
	if normalized.CreatedAt.IsZero() {
		normalized.CreatedAt = time.Now().UTC()
	}
	if normalized.SessionID == "" {
		return PromptHistoryEntry{}, errors.New("session_id is required")
	}
	if normalized.SourceID == "" {
		return PromptHistoryEntry{}, errors.New("source_id is required")
	}
	if strings.TrimSpace(normalized.Text) == "" {
		return PromptHistoryEntry{}, errors.New("text is required")
	}
	return normalized, nil
}

func promptHistoryEquivalent(entry PromptHistoryEntry, record PromptHistoryRecord) bool {
	return entry.SessionID == record.SessionID &&
		entry.SourceID == record.SourceID &&
		entry.Text == record.Text
}

func promptHistoryRecordFromRow(row sqlitegen.SessionPromptHistoryEntry) PromptHistoryRecord {
	return PromptHistoryRecord{
		Sequence:  row.Sequence,
		SessionID: row.SessionID,
		SourceID:  row.SourceID,
		Text:      row.Text,
		CreatedAt: time.UnixMilli(row.CreatedAtUnixMs).UTC(),
	}
}
