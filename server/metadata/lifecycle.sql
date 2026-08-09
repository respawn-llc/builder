-- name: DeferForeignKeys :exec
PRAGMA defer_foreign_keys = ON;

-- name: BeginImmediate :exec
BEGIN IMMEDIATE;

-- name: Commit :exec
COMMIT;

-- name: Rollback :exec
ROLLBACK;
