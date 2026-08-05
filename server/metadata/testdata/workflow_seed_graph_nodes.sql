INSERT INTO workflow_nodes (id, workflow_id, node_key, kind, display_name)
VALUES (?, ?, 'backlog', 'start', 'Backlog'),
       (?, ?, 'agent', 'agent', 'Agent'),
       (?, ?, 'done', 'terminal', 'Done')
