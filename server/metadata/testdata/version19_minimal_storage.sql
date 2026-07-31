INSERT INTO projects (id, display_name, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-minimal', 'Project', 1, 1, '{}');
INSERT INTO sessions (id, project_id, artifact_relpath, created_at_unix_ms, updated_at_unix_ms)
VALUES ('session-minimal', 'project-minimal', 'projects/project-minimal/sessions/session-minimal', 1, 1);
INSERT INTO runtime_leases (id, session_id, client_id, request_id, created_at_unix_ms, acquired_at_unix_ms, metadata_json)
VALUES ('lease-minimal', 'session-minimal', 'client', 'request', 1, 2, '{"trace":true}');
INSERT INTO workflows (id, name, description, graph_revision, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('workflow-550e8400-e29b-41d4-a716-446655440010', 'Workflow', '', 1, 1, 1, '{}');
INSERT INTO project_workflow_links (id, project_id, workflow_id, created_at_unix_ms, updated_at_unix_ms)
VALUES ('link-minimal', 'project-minimal', 'workflow-550e8400-e29b-41d4-a716-446655440010', 1, 1);
INSERT INTO tasks (id, project_workflow_link_id, workflow_revision_seen, task_seq, short_id, title, body, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-minimal', 'link-minimal', 1, 1, 'MIN-1', 'Task', '', 1, 1, '{}');
INSERT INTO task_comments (id, task_id, body, author_kind, author_id, source_run_id, created_at_unix_ms, updated_at_unix_ms, deleted_at_unix_ms, metadata_json)
VALUES ('comment-visible', 'task-minimal', 'visible', 'user', 'nek', NULL, 1, 3, 0, '{"keep":false}'),
       ('comment-deleted', 'task-minimal', 'deleted', 'user', 'nek', NULL, 1, 4, 4, '{"keep":false}');
