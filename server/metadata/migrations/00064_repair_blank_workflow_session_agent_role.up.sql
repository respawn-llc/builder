-- +goose Up

UPDATE sessions
SET continuation_json = json_remove(continuation_json, '$.agent_role'),
    locked_json = '{}',
    metadata_json = json_set(
        metadata_json,
        '$.prompt_cache_lineage_generation',
        coalesce(json_extract(metadata_json, '$.prompt_cache_lineage_generation'), 0) + 1
    )
WHERE json_type(continuation_json, '$.agent_role') = 'text'
  AND trim(json_extract(continuation_json, '$.agent_role')) = '';

-- +goose Down
SELECT kent_workflow_session_agent_role_backfill_is_irreversible();
