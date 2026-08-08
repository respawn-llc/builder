-- name: DeferForeignKeys :exec
PRAGMA defer_foreign_keys = ON;

-- name: CreateLifecycleQuestionIndex :exec
CREATE TABLE IF NOT EXISTS lifecycle_question_index (
    occurred_at_unix_ms INTEGER NOT NULL CHECK (occurred_at_unix_ms > 0),
    item_id TEXT NOT NULL CHECK (item_id <> '' AND item_id = trim(item_id)),
    task_id TEXT NOT NULL CHECK (task_id <> '' AND task_id = trim(task_id)),
    node_id TEXT NOT NULL CHECK (node_id <> '' AND node_id = trim(node_id)),
    transition_branch_key TEXT CHECK (
        transition_branch_key IS NULL
        OR (transition_branch_key <> '' AND transition_branch_key = trim(transition_branch_key))
    ),
    scope_id TEXT NOT NULL CHECK (scope_id <> '' AND scope_id = trim(scope_id)),
    prompt_id TEXT NOT NULL CHECK (prompt_id <> '' AND prompt_id = trim(prompt_id)),
    prompt_kind INTEGER NOT NULL CHECK (prompt_kind IN (1, 2)),
    question TEXT NOT NULL,
    suggestions_json TEXT NOT NULL CHECK (json_valid(suggestions_json) AND json_type(suggestions_json) = 'array'),
    recommended_option_index INTEGER,
    approval_decisions_json TEXT NOT NULL CHECK (
        json_valid(approval_decisions_json)
        AND json_type(approval_decisions_json) = 'array'
    ),
    PRIMARY KEY (occurred_at_unix_ms DESC, item_id DESC)
);

-- name: ClearLifecycleQuestionIndex :exec
DELETE FROM lifecycle_question_index;

-- name: InsertLifecycleQuestion :exec
INSERT INTO lifecycle_question_index (
    occurred_at_unix_ms,
    item_id,
    task_id,
    node_id,
    transition_branch_key,
    scope_id,
    prompt_id,
    prompt_kind,
    question,
    suggestions_json,
    recommended_option_index,
    approval_decisions_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: DeleteLifecycleQuestion :execrows
DELETE FROM lifecycle_question_index
WHERE occurred_at_unix_ms = ?
  AND item_id = ?
  AND task_id = ?
  AND node_id = ?
  AND transition_branch_key IS ?
  AND scope_id = ?
  AND prompt_id = ?;

-- name: AnchorLifecycleQuestionIndex :one
SELECT EXISTS(
    SELECT 1
    FROM lifecycle_question_index
) AS anchored;

-- name: ListLifecycleQuestions :many
SELECT
    occurred_at_unix_ms,
    item_id,
    task_id,
    node_id,
    transition_branch_key,
    scope_id,
    prompt_id,
    prompt_kind,
    question,
    suggestions_json,
    recommended_option_index,
    approval_decisions_json
FROM lifecycle_question_index
WHERE ? = 0
   OR occurred_at_unix_ms < ?
   OR (occurred_at_unix_ms = ? AND item_id < ?)
ORDER BY occurred_at_unix_ms DESC, item_id DESC
LIMIT ?;

-- name: ListLifecycleQuestionsForTask :many
SELECT
    occurred_at_unix_ms,
    item_id,
    task_id,
    node_id,
    transition_branch_key,
    scope_id,
    prompt_id,
    prompt_kind,
    question,
    suggestions_json,
    recommended_option_index,
    approval_decisions_json
FROM lifecycle_question_index
WHERE task_id = ?
ORDER BY occurred_at_unix_ms DESC, item_id DESC;
