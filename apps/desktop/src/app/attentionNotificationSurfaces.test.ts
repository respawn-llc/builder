import { describe, expect, it } from "vitest";

import {
  advancesAttentionNotificationRevision,
  attentionNotificationSurfaceKey,
  reconcileActiveSurfaces,
  taskDetailInitialFocus,
} from "./attentionNotificationSurfaces";
import { parseSetupOperationID } from "@/api";
import { createTestServices } from "@/test-support/app-services";

describe("attention notification surfaces", () => {
  it("applies only the first or a newer revision for one notification ID", () => {
    expect(advancesAttentionNotificationRevision(undefined, 1)).toBe(true);
    expect(advancesAttentionNotificationRevision(1, 1)).toBe(false);
    expect(advancesAttentionNotificationRevision(2, 1)).toBe(false);
    expect(advancesAttentionNotificationRevision(1, 2)).toBe(true);
  });
});

it("uses Setup Operation identity for setup-recovery surfaces", () => {
  const first = setupNotification(parseSetupOperationID("99999999-9999-4999-8999-999999999999"));
  const second = setupNotification(parseSetupOperationID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"));

  expect(attentionNotificationSurfaceKey(first)).not.toBe(attentionNotificationSurfaceKey(second));
  expect(attentionNotificationSurfaceKey(first)).toBe(attentionNotificationSurfaceKey({ ...first }));
});

it("reconciles setup recovery against the exact Setup Operation", async () => {
  const retainedSetupOperationID = parseSetupOperationID("99999999-9999-4999-8999-999999999999");
  const currentSetupOperationID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
  const notification = setupNotification(retainedSetupOperationID);
  const surfaceKey = attentionNotificationSurfaceKey(notification);
  const services = createTestServices([
    {
      method: "workflow.task.attention.list",
      result: {
        items: [
          {
            id: "canonical-attention",
            kind: "interrupted_current_node",
            project_id: "project-1",
            workflow_id: "11111111-1111-4111-8111-111111111111",
            task_id: "task-1",
            task_short_id: "T-1",
            task_title: "Recover setup",
            current_node: {
              node_id: "canonical-node",
              transition_branch_key: "branch-2",
              session_id: null,
            },
            session_id: null,
            session_name: null,
            setup_operation_id: currentSetupOperationID,
            detail_json: JSON.stringify({
              code: "workflow_setup_recovery",
              fields: {},
              setup_recovery: {
                setup_operation_id: currentSetupOperationID,
                cause: "target_preparation",
                diagnostic: "target preparation failed",
                script_path: null,
                execution_target: { mode: "default_branch" },
              },
            }),
            occurred_at_unix_ms: 1,
          },
        ],
        generated_at_unix_ms: 2,
      },
    },
  ]);

  await expect(
    reconcileActiveSurfaces([[surfaceKey, { notification, state: "toast" }]], services.api, services.logger),
  ).resolves.toEqual([surfaceKey]);
});

it("maps setup recovery to exact Task-detail focus", () => {
  const setupOperationID = parseSetupOperationID("44444444-4444-4444-8444-444444444444");
  expect(
    taskDetailInitialFocus({
      kind: "workflow_task",
      workflowID: "11111111-1111-4111-8111-111111111111",
      taskID: "task-1",
      currentNodeID: "canonical-node",
      currentNodeBranchKey: "branch-2",
      focus: { kind: "interrupted_current_node", setupOperationID },
    }),
  ).toEqual({
    kind: "interrupted_current_node",
    currentNodeID: "canonical-node",
    currentNodeBranchKey: "branch-2",
    setupOperationID,
  });
});

function setupNotification(setupOperationID: ReturnType<typeof parseSetupOperationID>) {
  return {
    id: { kind: "interrupted_current_node", uuid: "canonical-attention" },
    kind: "interrupted_current_node",
    occurredAt: "2026-08-08T12:00:00Z",
    revision: 1,
    question: null,
    approval: null,
    workflowApproval: null,
    interruptedCurrentNode: {
      message: "Setup needs attention",
      reason: "workflow_setup_recovery",
      detailJSON: undefined,
    },
    target: {
      kind: "workflow_task",
      workflowID: "11111111-1111-4111-8111-111111111111",
      taskID: "task-1",
      currentNodeID: "canonical-node",
      currentNodeBranchKey: "branch-2",
      focus: { kind: "interrupted_current_node", setupOperationID },
    },
  } as const;
}
