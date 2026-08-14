import { expect, it } from "vitest";

import { workflowGraphSavePreviewSchema } from "./workflow";

const impact = {
  active_current_node_count: 0,
  edge_task_reference_count: 0,
  last_terminal_change_count: 0,
  node_task_reference_count: 0,
  pending_approval_count: 0,
  removed_edge_count: 1,
  removed_entities: [{ entity_id: "edge-1", entity_type: "edge" }],
  removed_node_count: 0,
  removed_node_group_count: 0,
  removed_transition_group_count: 0,
  start_node_change_count: 0,
  task_referenced_node_kind_change_count: 0,
};

it("hard-cuts over graph impact and blocker identities", () => {
  const preview = {
    blockers: [
      {
        affected_entities: [{ entity_id: "edge-1", entity_type: "edge" }],
        code: "confirmation_required",
        count: 1,
        message: "Confirm removal.",
      },
    ],
    can_save: false,
    changed: true,
    confirmation_required: true,
    current_version: 12,
    impact,
    validation_results: {},
  };
  expect(workflowGraphSavePreviewSchema.parse(preview)).toMatchObject({
    changed: true,
    impact: { removedEntities: [{ entityID: "edge-1", entityType: "edge" }] },
    blockers: [{ affectedEntities: [{ entityID: "edge-1", entityType: "edge" }] }],
  });
  expect(
    workflowGraphSavePreviewSchema.safeParse({
      ...preview,
      blockers: [{ code: "confirmation_required", count: 1, message: "Confirm removal." }],
    }).success,
  ).toBe(false);
  expect(
    workflowGraphSavePreviewSchema.safeParse({
      ...preview,
      impact: { ...impact, removed_entities: undefined },
    }).success,
  ).toBe(false);
});
