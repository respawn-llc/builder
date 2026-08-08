INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name, subagent_role)
VALUES (?, ?, 'backlog', 'start', 'Backlog', ''),
       (?, ?, 'agent', 'agent', 'Agent', 'default'),
       (?, ?, 'done', 'terminal', 'Done', '')
