-- +goose Up

ALTER TABLE task_current_nodes
    ADD COLUMN effective_assignee TEXT
        CHECK (effective_assignee IS NULL OR length(trim(effective_assignee)) > 0);

ALTER TABLE task_current_nodes
    ADD COLUMN effective_thinking TEXT
        CHECK (effective_thinking IS NULL OR length(trim(effective_thinking)) > 0);

ALTER TABLE task_current_nodes
    ADD COLUMN assignee_origin TEXT
        CHECK (assignee_origin IS NULL OR assignee_origin IN (
            'configured_fallback',
            'transition_selected',
            'retained_session'
        ));
