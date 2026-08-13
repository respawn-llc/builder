-- +goose Up
CREATE TEMP TABLE workflow_legacy_current_node_source_candidates (
    task_id TEXT NOT NULL,
    target_node_id BLOB NOT NULL,
    transition_branch_key TEXT,
    source_session_id TEXT NOT NULL,
    associated_at_unix_ms INTEGER NOT NULL
);

INSERT INTO workflow_legacy_current_node_source_candidates (
    task_id,
    target_node_id,
    transition_branch_key,
    source_session_id,
    associated_at_unix_ms
)
SELECT
    current_node.task_id,
    current_node.node_id,
    current_node.transition_branch_key,
    source_association.session_id,
    source_association.associated_at_unix_ms
FROM task_current_nodes current_node
JOIN task_records task
  ON task.id = current_node.task_id
JOIN workflow_edges entering_edge
  ON entering_edge.id = current_node.entered_by_edge_id
 AND entering_edge.target_node_id = current_node.node_id
JOIN workflow_transition_groups entering_transition
  ON entering_transition.id = entering_edge.transition_group_id
JOIN workflow_nodes source_node
  ON source_node.id = entering_transition.source_node_id
 AND source_node.workflow_id = task.workflow_id
LEFT JOIN workflow_nodes selected_source_node
  ON selected_source_node.workflow_id = task.workflow_id
 AND selected_source_node.node_key = entering_edge.context_source_node_key
 AND entering_edge.context_source_kind = 'selected_node'
JOIN workflow_nodes target_node
  ON target_node.id = current_node.node_id
 AND target_node.workflow_id = task.workflow_id
JOIN session_workflow_node_associations source_association
  ON source_association.task_id = current_node.task_id
 AND source_association.node_id = CASE
        WHEN entering_edge.context_source_kind = 'selected_node'
        THEN selected_source_node.id
        ELSE source_node.id
     END
LEFT JOIN session_workflow_node_associations target_association
  ON target_association.task_id = current_node.task_id
 AND target_association.session_id = current_node.session_id
 AND target_association.node_id = current_node.node_id
 AND (
        target_association.transition_branch_key = current_node.transition_branch_key
        OR (
            target_association.transition_branch_key IS NULL
            AND current_node.transition_branch_key IS NULL
        )
 )
WHERE current_node.legacy_materialized = 1
  AND entering_edge.context_source_kind IN ('immediate_source', 'selected_node')
  AND entering_edge.context_mode != 'new_session'
  AND (
        (
            current_node.transition_branch_key IS NULL
            AND source_association.transition_branch_key IS NULL
        )
        OR (
            current_node.transition_branch_key IS NOT NULL
            AND (
                (
                    SELECT COUNT(*)
                    FROM workflow_edges sibling_edge
                    WHERE sibling_edge.transition_group_id = entering_transition.id
                ) > 1
                AND source_association.transition_branch_key IS NULL
            )
        )
        OR (
            current_node.transition_branch_key IS NOT NULL
            AND (
                SELECT COUNT(*)
                FROM workflow_edges sibling_edge
                WHERE sibling_edge.transition_group_id = entering_transition.id
            ) = 1
            AND source_association.transition_branch_key = current_node.transition_branch_key
        )
  )
  AND (
        target_association.associated_at_unix_ms IS NULL
        OR source_association.associated_at_unix_ms <= target_association.associated_at_unix_ms
  )
  AND NOT EXISTS (
      SELECT 1
      FROM session_workflow_node_associations conflicting_current
      WHERE conflicting_current.task_id = current_node.task_id
        AND conflicting_current.node_id = current_node.node_id
        AND conflicting_current.association_status = 'current'
        AND (
              conflicting_current.transition_branch_key = current_node.transition_branch_key
              OR (
                  conflicting_current.transition_branch_key IS NULL
                  AND current_node.transition_branch_key IS NULL
              )
        )
        AND (
              current_node.session_id IS NULL
              OR conflicting_current.session_id != current_node.session_id
        )
  );

CREATE TEMP TABLE workflow_legacy_current_node_source_winners AS
SELECT
    candidate.task_id,
    candidate.target_node_id,
    candidate.transition_branch_key,
    candidate.source_session_id
FROM workflow_legacy_current_node_source_candidates candidate
WHERE candidate.associated_at_unix_ms = (
    SELECT MAX(latest.associated_at_unix_ms)
    FROM workflow_legacy_current_node_source_candidates latest
    WHERE latest.task_id = candidate.task_id
      AND latest.target_node_id = candidate.target_node_id
      AND (
            latest.transition_branch_key = candidate.transition_branch_key
            OR (
                latest.transition_branch_key IS NULL
                AND candidate.transition_branch_key IS NULL
            )
      )
)
AND 1 = (
    SELECT COUNT(*)
    FROM workflow_legacy_current_node_source_candidates tied
    WHERE tied.task_id = candidate.task_id
      AND tied.target_node_id = candidate.target_node_id
      AND (
            tied.transition_branch_key = candidate.transition_branch_key
            OR (
                tied.transition_branch_key IS NULL
                AND candidate.transition_branch_key IS NULL
            )
      )
      AND tied.associated_at_unix_ms = candidate.associated_at_unix_ms
);

INSERT INTO workflow_legacy_current_node_source_winners (
    task_id,
    target_node_id,
    transition_branch_key,
    source_session_id
)
SELECT
    current_node.task_id,
    current_node.node_id,
    current_node.transition_branch_key,
    current_node.session_id
FROM task_current_nodes current_node
JOIN workflow_edges entering_edge
  ON entering_edge.id = current_node.entered_by_edge_id
 AND entering_edge.target_node_id = current_node.node_id
JOIN workflow_nodes current_node_definition
  ON current_node_definition.id = current_node.node_id
JOIN sessions current_session
  ON current_session.id = current_node.session_id
 AND current_session.task_id = current_node.task_id
WHERE current_node.legacy_materialized = 1
  AND current_node_definition.kind = 'agent'
  AND entering_edge.context_mode = 'new_session';

UPDATE task_current_nodes
SET
    continuation_source_kind = 'exact',
    continuation_source_session_id = (
        SELECT winner.source_session_id
        FROM workflow_legacy_current_node_source_winners winner
        WHERE winner.task_id = task_current_nodes.task_id
          AND winner.target_node_id = task_current_nodes.node_id
          AND (
                winner.transition_branch_key = task_current_nodes.transition_branch_key
                OR (
                    winner.transition_branch_key IS NULL
                    AND task_current_nodes.transition_branch_key IS NULL
                )
          )
    ),
    legacy_materialized = 0
WHERE legacy_materialized = 1
  AND EXISTS (
      SELECT 1
      FROM workflow_legacy_current_node_source_winners winner
      WHERE winner.task_id = task_current_nodes.task_id
        AND winner.target_node_id = task_current_nodes.node_id
        AND (
              winner.transition_branch_key = task_current_nodes.transition_branch_key
              OR (
                  winner.transition_branch_key IS NULL
                  AND task_current_nodes.transition_branch_key IS NULL
              )
        )
  );

UPDATE session_workflow_node_associations
SET
    association_status = 'current',
    source_session_id = (
        SELECT winner.source_session_id
        FROM workflow_legacy_current_node_source_winners winner
        WHERE winner.task_id = session_workflow_node_associations.task_id
          AND winner.target_node_id = session_workflow_node_associations.node_id
          AND (
                winner.transition_branch_key = session_workflow_node_associations.transition_branch_key
                OR (
                    winner.transition_branch_key IS NULL
                    AND session_workflow_node_associations.transition_branch_key IS NULL
                )
          )
    )
WHERE association_status = 'historical'
  AND EXISTS (
      SELECT 1
      FROM task_current_nodes current_node
      JOIN workflow_legacy_current_node_source_winners winner
        ON winner.task_id = current_node.task_id
       AND winner.target_node_id = current_node.node_id
       AND (
             winner.transition_branch_key = current_node.transition_branch_key
             OR (
                 winner.transition_branch_key IS NULL
                 AND current_node.transition_branch_key IS NULL
             )
       )
      WHERE current_node.task_id = session_workflow_node_associations.task_id
        AND current_node.node_id = session_workflow_node_associations.node_id
        AND current_node.session_id = session_workflow_node_associations.session_id
        AND (
              current_node.transition_branch_key = session_workflow_node_associations.transition_branch_key
              OR (
                  current_node.transition_branch_key IS NULL
                  AND session_workflow_node_associations.transition_branch_key IS NULL
              )
        )
  );

CREATE TEMP TABLE workflow_legacy_fanout_branch_source_candidates (
    task_id TEXT NOT NULL,
    transition_branch_key TEXT NOT NULL,
    source_session_id TEXT NOT NULL
);

UPDATE task_active_fanout_branches
SET
    continuation_source_kind = 'deferred_self',
    continuation_source_session_id = NULL,
    legacy_materialized = 0
WHERE legacy_materialized = 1
  AND EXISTS (
      SELECT 1
      FROM task_current_nodes current_node
      WHERE current_node.task_id = task_active_fanout_branches.task_id
        AND current_node.transition_branch_key = task_active_fanout_branches.transition_branch_key
        AND current_node.continuation_source_kind = 'deferred_self'
        AND current_node.continuation_source_session_id IS NULL
        AND current_node.legacy_materialized = 0
  );

INSERT INTO workflow_legacy_fanout_branch_source_candidates (
    task_id,
    transition_branch_key,
    source_session_id
)
SELECT
    current_node.task_id,
    current_node.transition_branch_key,
    current_node.continuation_source_session_id
FROM task_current_nodes current_node
WHERE current_node.transition_branch_key IS NOT NULL
  AND current_node.continuation_source_kind = 'exact'
  AND current_node.legacy_materialized = 0;

UPDATE task_active_fanout_branches
SET
    continuation_source_kind = 'exact',
    continuation_source_session_id = (
        SELECT MIN(candidate.source_session_id)
        FROM workflow_legacy_fanout_branch_source_candidates candidate
        WHERE candidate.task_id = task_active_fanout_branches.task_id
          AND candidate.transition_branch_key = task_active_fanout_branches.transition_branch_key
    ),
    legacy_materialized = 0
WHERE legacy_materialized = 1
  AND 1 = (
      SELECT COUNT(DISTINCT candidate.source_session_id)
      FROM workflow_legacy_fanout_branch_source_candidates candidate
      WHERE candidate.task_id = task_active_fanout_branches.task_id
        AND candidate.transition_branch_key = task_active_fanout_branches.transition_branch_key
  );

CREATE TEMP TABLE workflow_legacy_approval_source_candidates (
    approval_id TEXT NOT NULL,
    transition_branch_key TEXT NOT NULL,
    source_session_id TEXT NOT NULL,
    associated_at_unix_ms INTEGER NOT NULL
);

INSERT INTO workflow_legacy_approval_source_candidates (
    approval_id,
    transition_branch_key,
    source_session_id,
    associated_at_unix_ms
)
SELECT
    approval.id,
    branch.transition_branch_key,
    association.session_id,
    association.associated_at_unix_ms
FROM task_pending_approvals approval
JOIN task_pending_approval_branches branch
  ON branch.approval_id = approval.id
JOIN task_records task
  ON task.id = approval.source_task_id
JOIN workflow_nodes selected_node
  ON selected_node.workflow_id = task.workflow_id
 AND selected_node.node_key = json_extract(
     branch.effective_edge_configuration_json,
     '$.context_source.node_key'
 )
JOIN session_workflow_node_associations association
  ON association.task_id = approval.source_task_id
 AND association.node_id = selected_node.id
 AND association.associated_at_unix_ms <= approval.created_at_unix_ms
 AND (
       association.transition_branch_key = approval.source_transition_branch_key
       OR (
           association.transition_branch_key IS NULL
           AND approval.source_transition_branch_key IS NULL
       )
 )
WHERE json_extract(branch.context_source_resolution_json, '$.active_source.kind') = 'legacy'
  AND json_extract(
      branch.effective_edge_configuration_json,
      '$.context_source.kind'
  ) = 'selected_node';

CREATE TEMP TABLE workflow_legacy_approval_source_winners AS
SELECT
    candidate.approval_id,
    candidate.transition_branch_key,
    candidate.source_session_id
FROM workflow_legacy_approval_source_candidates candidate
WHERE candidate.associated_at_unix_ms = (
    SELECT MAX(latest.associated_at_unix_ms)
    FROM workflow_legacy_approval_source_candidates latest
    WHERE latest.approval_id = candidate.approval_id
      AND latest.transition_branch_key = candidate.transition_branch_key
)
AND 1 = (
    SELECT COUNT(*)
    FROM workflow_legacy_approval_source_candidates tied
    WHERE tied.approval_id = candidate.approval_id
      AND tied.transition_branch_key = candidate.transition_branch_key
      AND tied.associated_at_unix_ms = candidate.associated_at_unix_ms
);

INSERT INTO workflow_legacy_approval_source_winners (
    approval_id,
    transition_branch_key,
    source_session_id
)
SELECT
    approval.id,
    branch.transition_branch_key,
    approval.source_session_id
FROM task_pending_approvals approval
JOIN task_pending_approval_branches branch
  ON branch.approval_id = approval.id
JOIN sessions source_session
  ON source_session.id = approval.source_session_id
 AND source_session.task_id = approval.source_task_id
WHERE json_extract(branch.context_source_resolution_json, '$.active_source.kind') = 'legacy'
  AND COALESCE(
      json_extract(
          branch.effective_edge_configuration_json,
          '$.context_mode'
      ),
      'continue_session'
  ) != 'new_session'
  AND COALESCE(
      json_extract(
          branch.effective_edge_configuration_json,
          '$.context_source.kind'
      ),
      'immediate_source'
  ) = 'immediate_source';

INSERT INTO workflow_legacy_approval_source_winners (
    approval_id,
    transition_branch_key,
    source_session_id
)
SELECT
    approval.id,
    branch.transition_branch_key,
    source_current_node.continuation_source_session_id
FROM task_pending_approvals approval
JOIN task_pending_approval_branches branch
  ON branch.approval_id = approval.id
JOIN task_records task
  ON task.id = approval.source_task_id
JOIN workflow_nodes target_node
  ON target_node.workflow_id = task.workflow_id
 AND target_node.id = kent_graph_entity_id_blob_v1(
     json_extract(branch.target_snapshot_json, '$.node_id')
 )
JOIN task_current_nodes source_current_node
  ON source_current_node.task_id = approval.source_task_id
 AND source_current_node.node_id = approval.source_node_id
 AND (
       source_current_node.transition_branch_key = approval.source_transition_branch_key
       OR (
           source_current_node.transition_branch_key IS NULL
           AND approval.source_transition_branch_key IS NULL
       )
 )
WHERE json_extract(branch.context_source_resolution_json, '$.active_source.kind') = 'legacy'
  AND json_extract(
      branch.effective_edge_configuration_json,
      '$.context_mode'
  ) = 'new_session'
  AND target_node.kind IN ('script', 'join')
  AND source_current_node.continuation_source_kind = 'exact'
  AND source_current_node.continuation_source_session_id IS NOT NULL
  AND source_current_node.legacy_materialized = 0
  AND NOT EXISTS (
      SELECT 1
      FROM workflow_legacy_approval_source_winners existing
      WHERE existing.approval_id = approval.id
        AND existing.transition_branch_key = branch.transition_branch_key
  );

INSERT INTO workflow_legacy_approval_source_winners (
    approval_id,
    transition_branch_key,
    source_session_id
)
SELECT
    approval.id,
    branch.transition_branch_key,
    source_current_node.continuation_source_session_id
FROM task_pending_approvals approval
JOIN task_pending_approval_branches branch
  ON branch.approval_id = approval.id
JOIN task_current_nodes source_current_node
  ON source_current_node.task_id = approval.source_task_id
 AND source_current_node.node_id = approval.source_node_id
 AND (
       source_current_node.transition_branch_key = approval.source_transition_branch_key
       OR (
           source_current_node.transition_branch_key IS NULL
           AND approval.source_transition_branch_key IS NULL
       )
 )
WHERE json_extract(branch.context_source_resolution_json, '$.active_source.kind') = 'legacy'
  AND json_extract(
      branch.effective_edge_configuration_json,
      '$.context_source.kind'
  ) IN ('previous_target', 'previous_target_or_new')
  AND source_current_node.continuation_source_kind = 'exact'
  AND source_current_node.legacy_materialized = 0
  AND NOT EXISTS (
      SELECT 1
      FROM workflow_legacy_approval_source_winners existing
      WHERE existing.approval_id = approval.id
        AND existing.transition_branch_key = branch.transition_branch_key
  );

UPDATE task_pending_approval_branches
SET
    target_snapshot_json = json_remove(target_snapshot_json, '$.session_id'),
    context_source_resolution_json = json_set(
        context_source_resolution_json,
        '$.active_source',
        json_object(
            'kind',
            'exact',
            'session_id', (
                SELECT winner.source_session_id
                FROM workflow_legacy_approval_source_winners winner
                WHERE winner.approval_id = task_pending_approval_branches.approval_id
                  AND winner.transition_branch_key =
                      task_pending_approval_branches.transition_branch_key
            )
        )
    )
WHERE json_extract(context_source_resolution_json, '$.active_source.kind') = 'legacy'
  AND EXISTS (
      SELECT 1
      FROM workflow_legacy_approval_source_winners winner
      WHERE winner.approval_id = task_pending_approval_branches.approval_id
        AND winner.transition_branch_key =
            task_pending_approval_branches.transition_branch_key
  );

DROP TABLE workflow_legacy_approval_source_winners;
DROP TABLE workflow_legacy_approval_source_candidates;
DROP TABLE workflow_legacy_fanout_branch_source_candidates;
DROP TABLE workflow_legacy_current_node_source_winners;
DROP TABLE workflow_legacy_current_node_source_candidates;
