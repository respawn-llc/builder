-- +goose Up

WITH ranked_agent_roles AS (
    SELECT
        association.session_id,
        lower(trim(node.subagent_role)) AS normalized_role,
        row_number() OVER (
            PARTITION BY association.session_id
            ORDER BY association.associated_at_unix_ms DESC, association.node_id DESC
        ) AS recency_rank
    FROM session_workflow_node_associations association
    JOIN workflow_nodes node ON node.id = association.node_id
    WHERE node.kind = 'agent'
)
UPDATE sessions
SET continuation_json = CASE (
        SELECT normalized_role
        FROM ranked_agent_roles
        WHERE ranked_agent_roles.session_id = sessions.id
          AND ranked_agent_roles.recency_rank = 1
    )
        WHEN 'default' THEN json_remove(continuation_json, '$.agent_role')
        ELSE json_set(
            continuation_json,
            '$.agent_role',
            (
                SELECT normalized_role
                FROM ranked_agent_roles
                WHERE ranked_agent_roles.session_id = sessions.id
                  AND ranked_agent_roles.recency_rank = 1
            )
        )
    END,
    locked_json = '{}',
    metadata_json = json_set(
        metadata_json,
        '$.prompt_cache_lineage_generation',
        coalesce(json_extract(metadata_json, '$.prompt_cache_lineage_generation'), 0) + 1
    )
WHERE json_type(continuation_json, '$.agent_role') IS NULL
  AND EXISTS (
      SELECT 1
      FROM ranked_agent_roles
      WHERE ranked_agent_roles.session_id = sessions.id
        AND ranked_agent_roles.recency_rank = 1
  );

-- +goose Down
SELECT kent_workflow_session_agent_role_backfill_is_irreversible();
