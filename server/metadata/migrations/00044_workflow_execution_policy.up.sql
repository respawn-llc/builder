-- +goose Up

ALTER TABLE workflows
    ADD COLUMN execution_policy TEXT NOT NULL DEFAULT 'ask'
    CHECK (execution_policy IN ('none', 'head', 'default_branch', 'custom_ref', 'ask'));

ALTER TABLE workflows
    ADD COLUMN execution_custom_ref TEXT
    CHECK (
        (
            execution_policy = 'custom_ref'
            AND execution_custom_ref IS NOT NULL
            AND length(trim(execution_custom_ref)) > 0
        )
        OR (
            execution_policy != 'custom_ref'
            AND execution_custom_ref IS NULL
        )
    );
