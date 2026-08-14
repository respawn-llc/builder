-- +goose Up

ALTER TABLE sessions
ADD COLUMN completed_compaction_count INTEGER
CHECK (completed_compaction_count IS NULL OR completed_compaction_count >= 0);

ALTER TABLE sessions
ADD COLUMN manual_compact_eligible INTEGER
CHECK (manual_compact_eligible IS NULL OR manual_compact_eligible IN (0, 1));
