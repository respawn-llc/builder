import { expect, it } from "vitest";

import { workflowGraphSavePreviewSchema } from "./workflow";

const impact = {
  active_current_node_count: 0,
  edge_task_reference_count: 0,
  last_terminal_change_count: 0,
  node_task_reference_count: 0,
  pending_approval_count: 0,
  removed_edge_count: 1,
  removed_entities: [
    { entity_id: "edge-1", entity_type: "edge" },
    { entity_id: "group-1", entity_type: "node_group" },
  ],
  removed_node_count: 0,
  removed_node_group_count: 1,
  removed_transition_group_count: 0,
  start_node_change_count: 0,
  task_referenced_node_kind_change_count: 0,
};

it("projects changed graph impact and exact entity identities", () => {
  expect(
    workflowGraphSavePreviewSchema.parse({
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
    }),
  ).toMatchObject({
    blockers: [
      {
        affectedEntities: [{ entityID: "edge-1", entityType: "edge" }],
      },
    ],
    changed: true,
    impact: {
      removedEntities: [
        { entityID: "edge-1", entityType: "edge" },
        { entityID: "group-1", entityType: "node_group" },
      ],
      removedNodeGroupCount: 1,
    },
  });
});

it("requires the hard-cutover graph impact and blocker fields", () => {
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
  expect(
    workflowGraphSavePreviewSchema.safeParse({
      ...preview,
      impact: { ...impact, removed_node_group_count: undefined },
    }).success,
  ).toBe(false);
  expect(
    workflowGraphSavePreviewSchema.safeParse({
      ...preview,
      blockers: [{ code: "confirmation_required", count: 1, message: "Confirm removal." }],
    }).success,
  ).toBe(false);
});
