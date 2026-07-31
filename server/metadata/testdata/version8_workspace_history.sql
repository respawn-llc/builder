INSERT INTO projects (id, display_name, project_key, next_task_seq, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('project-1', 'Project', 'PR', 1, 1, 1, '{}');
INSERT INTO workspaces (id, project_id, canonical_root_path, display_name, availability, is_primary, git_metadata_json, created_at_unix_ms, updated_at_unix_ms)
VALUES ('workspace-1', 'project-1', '/tmp/workspace-1', 'Workspace One', 'available', 1, '{}', 1, 1);
INSERT INTO sessions (id, project_id, workspace_id, artifact_relpath, name, first_prompt_preview, input_draft, parent_session_id, created_at_unix_ms, updated_at_unix_ms, last_sequence, model_request_count, in_flight_step, agents_injected, launch_visible, cwd_relpath, continuation_json, locked_json, usage_state_json, metadata_json)
VALUES ('session-1', 'project-1', 'workspace-1', 'projects/project-1/sessions/session-1', 'Session', '', '', '', 1, 1, 0, 1, 0, 0, 1, '.', '{}', '{}', '{}', '{invalid json');
INSERT INTO workflows (id, name, description, graph_revision, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('workflow-550e8400-e29b-41d4-a716-446655440001', 'Workflow', '', 1, 1, 1, '{}');
INSERT INTO project_workflow_links (id, project_id, workflow_id, is_default, created_at_unix_ms, updated_at_unix_ms)
VALUES ('link-1', 'project-1', 'workflow-550e8400-e29b-41d4-a716-446655440001', 1, 1, 1);
INSERT INTO tasks (id, project_id, project_workflow_link_id, workflow_id, workflow_revision_seen, task_seq, short_id, title, body, source_workspace_id, created_at_unix_ms, updated_at_unix_ms, metadata_json)
VALUES ('task-1', 'project-1', 'link-1', 'workflow-550e8400-e29b-41d4-a716-446655440001', 1, 1, 'PR-1', 'Task', '', 'workspace-1', 1, 1, '{}');
