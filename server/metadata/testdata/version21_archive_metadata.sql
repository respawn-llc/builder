INSERT INTO workflows (id, name, description, graph_revision, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('workflow-550e8400-e29b-41d4-a716-446655440011', 'Workflow', '', 1, 1, 1, '{}');
INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, output_fields_json, metadata_json)
VALUES ('node-archive', 'workflow-550e8400-e29b-41d4-a716-446655440011', 'archived', 'terminal', 'Archived', '[]', '{"archived_at_unix_ms": 1, "other": true}');
