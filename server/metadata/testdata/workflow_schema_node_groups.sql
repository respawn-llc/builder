INSERT INTO workflows (id, name, version, created_at_unix_ms, updated_at_unix_ms)
VALUES (?, 'Other', 1, ?, ?);
INSERT INTO workflow_node_groups (id, workflow_id, group_key, display_name)
VALUES ('group-workflow-1', ?, 'impl', 'Implementation'),
       ('group-other', ?, 'impl', 'Implementation');
