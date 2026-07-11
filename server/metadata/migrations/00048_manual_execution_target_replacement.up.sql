-- +goose Up

DROP TRIGGER task_execution_target_negotiations_task_identity_insert;
DROP TRIGGER task_execution_target_negotiations_task_identity_update;

-- +goose StatementBegin
CREATE TRIGGER task_execution_target_negotiations_task_identity_insert
BEFORE INSERT ON task_execution_target_negotiations
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM tasks t
    JOIN project_workflow_links pwl ON pwl.id = t.project_workflow_link_id
    WHERE t.id = NEW.task_id
      AND pwl.workflow_id = NEW.workflow_id
      AND t.source_workspace_id = NEW.source_workspace_id
)
OR EXISTS (
    SELECT 1
    FROM task_execution_targets target
    WHERE target.task_id = NEW.task_id
      AND NOT (
          target.recovery_disposition = 'manual_recovery'
          AND target.active_claim_generation IS NULL
          AND target.active_claim_phase IS NULL
      )
)
BEGIN
    SELECT RAISE(ABORT, 'execution target negotiation must match an unlocked or manual-recovery task identity');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_execution_target_negotiations_task_identity_update
BEFORE UPDATE OF task_id, workflow_id, source_workspace_id ON task_execution_target_negotiations
FOR EACH ROW
WHEN NOT EXISTS (
    SELECT 1
    FROM tasks t
    JOIN project_workflow_links pwl ON pwl.id = t.project_workflow_link_id
    WHERE t.id = NEW.task_id
      AND pwl.workflow_id = NEW.workflow_id
      AND t.source_workspace_id = NEW.source_workspace_id
)
OR EXISTS (
    SELECT 1
    FROM task_execution_targets target
    WHERE target.task_id = NEW.task_id
      AND NOT (
          target.recovery_disposition = 'manual_recovery'
          AND target.active_claim_generation IS NULL
          AND target.active_claim_phase IS NULL
      )
)
BEGIN
    SELECT RAISE(ABORT, 'execution target negotiation must match an unlocked or manual-recovery task identity');
END;
-- +goose StatementEnd
