-- +goose Up

CREATE INDEX session_workflow_node_associations_session_recency_idx
    ON session_workflow_node_associations(
        session_id,
        associated_at_unix_ms DESC,
        node_id DESC
    );
