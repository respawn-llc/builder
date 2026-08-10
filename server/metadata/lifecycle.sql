-- name: DeferForeignKeys :exec
PRAGMA defer_foreign_keys = ON;

-- name: SetBusyTimeout15Seconds :exec
PRAGMA busy_timeout = 15000;

-- name: SetBusyTimeout5Seconds :exec
PRAGMA busy_timeout = 5000;

-- name: BeginImmediate :exec
BEGIN IMMEDIATE;

-- name: Commit :exec
COMMIT;

-- name: Rollback :exec
ROLLBACK;
