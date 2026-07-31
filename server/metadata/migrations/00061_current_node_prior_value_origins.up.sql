-- +goose Up

UPDATE task_current_nodes
SET prior_node_values_json = json_object(
    'transition_parameters',
    json(
        CASE
            WHEN json_type(prior_node_values_json, '$.transition_parameters') = 'object'
                THEN json_extract(prior_node_values_json, '$.transition_parameters')
            ELSE '{}'
        END
    )
);

UPDATE task_pending_approval_branches
SET target_snapshot_json = json_set(
    json_remove(target_snapshot_json, '$.prior_node_values'),
    '$.prior_values',
    json_object(
        'transition_parameters',
        json(
            CASE
                WHEN json_type(target_snapshot_json, '$.prior_values.transition_parameters') = 'object'
                    THEN json_extract(target_snapshot_json, '$.prior_values.transition_parameters')
                ELSE '{}'
            END
        )
    )
);

-- +goose StatementBegin
CREATE TRIGGER task_current_nodes_prior_transition_parameters_insert
BEFORE INSERT ON task_current_nodes
FOR EACH ROW
WHEN json_type(NEW.prior_node_values_json, '$.transition_parameters') IS NOT 'object'
  OR json(json_remove(NEW.prior_node_values_json, '$.transition_parameters')) != '{}'
BEGIN
    SELECT RAISE(ABORT, 'current node prior values must contain exactly one Transition parameter object');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_current_nodes_prior_transition_parameters_update
BEFORE UPDATE OF prior_node_values_json ON task_current_nodes
FOR EACH ROW
WHEN json_type(NEW.prior_node_values_json, '$.transition_parameters') IS NOT 'object'
  OR json(json_remove(NEW.prior_node_values_json, '$.transition_parameters')) != '{}'
BEGIN
    SELECT RAISE(ABORT, 'current node prior values must contain exactly one Transition parameter object');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_pending_approval_branches_prior_transition_parameters_insert
BEFORE INSERT ON task_pending_approval_branches
FOR EACH ROW
WHEN json_type(NEW.target_snapshot_json, '$.prior_values') IS NOT 'object'
  OR json_type(NEW.target_snapshot_json, '$.prior_values.transition_parameters') IS NOT 'object'
  OR json(json_remove(json_extract(NEW.target_snapshot_json, '$.prior_values'), '$.transition_parameters')) != '{}'
  OR json_type(NEW.target_snapshot_json, '$.prior_node_values') IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'pending approval target prior values must contain exactly one Transition parameter object');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_pending_approval_branches_prior_transition_parameters_update
BEFORE UPDATE OF target_snapshot_json ON task_pending_approval_branches
FOR EACH ROW
WHEN json_type(NEW.target_snapshot_json, '$.prior_values') IS NOT 'object'
  OR json_type(NEW.target_snapshot_json, '$.prior_values.transition_parameters') IS NOT 'object'
  OR json(json_remove(json_extract(NEW.target_snapshot_json, '$.prior_values'), '$.transition_parameters')) != '{}'
  OR json_type(NEW.target_snapshot_json, '$.prior_node_values') IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'pending approval target prior values must contain exactly one Transition parameter object');
END;
-- +goose StatementEnd

-- +goose Down
SELECT kent_current_node_prior_transition_parameters_are_irreversible();
