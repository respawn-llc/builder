-- FTS5 exposes rank as a pseudo-column. sqlc's SQLite schema reader does not
-- retain virtual-table pseudo-columns, so this schema-only declaration keeps
-- the generated ranked search adapter typed.
ALTER TABLE task_search_fts ADD COLUMN rank REAL;
ALTER TABLE task_search_fts ADD COLUMN task_search_fts TEXT;
