import { ApiClient } from "./client";
import {
  taskApproveResponseSchema,
  taskMoveResponseSchema,
  taskStartResponseSchema,
} from "./schemas/workflowBoard";
import { FakeRpcTransport } from "@/test-support/api";

describe("task lifecycle client", () => {
  it("uses Current Node responses and does not emit board-only lifecycle flags", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.task.start",
        result: {
          outcome: "applied",
          applied: {
            current_nodes: [{ node_id: "node-1", transition_branch_key: null, session_id: "session-1" }],
          },
        },
      },
      {
        method: "workflow.task.move",
        result: {
          outcome: "applied",
          applied: { current_nodes: [{ node_id: "node-2", transition_branch_key: null, session_id: null }] },
        },
      },
      {
        method: "workflow.task.approve",
        result: {
          outcome: "applied",
          applied: {
            task_id: "task-1",
            current_nodes: [{ node_id: "node-3", transition_branch_key: "branch-a", session_id: null }],
          },
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.startTask({ taskID: "task-1" })).resolves.toMatchObject({
      outcome: "applied",
      applied: { currentNodes: [{ nodeID: "node-1" }] },
    });
    await expect(client.moveTask({ taskID: "task-1", targetNodeID: "node-2" })).resolves.toMatchObject({
      outcome: "applied",
      applied: { currentNodes: [{ nodeID: "node-2" }] },
    });
    await expect(client.approveApproval("approval-1")).resolves.toMatchObject({
      outcome: "applied",
      applied: { taskID: "task-1", currentNodes: [{ nodeID: "node-3" }] },
    });

    expect(transport.calls).toContainEqual({
      method: "workflow.task.approve",
      options: { timeoutMs: null },
      params: { approval_id: "approval-1" },
    });
    expect(transport.calls.find((call) => call.method === "workflow.task.move")?.params).not.toHaveProperty(
      "allow_missing_edge",
    );
    expect(transport.calls.find((call) => call.method === "workflow.task.move")?.params).not.toHaveProperty(
      "auto_approve",
    );
  });

  it("maps typed execution-target selection requirements", () => {
    expect(
      taskStartResponseSchema.parse({
        outcome: "selection_required",
        selection_required: { reason: "policy_requires_selection" },
      }),
    ).toEqual({
      outcome: "selection_required",
      selectionRequired: { reason: "policy_requires_selection" },
    });
    expect(
      taskMoveResponseSchema.parse({
        outcome: "selection_required",
        selection_required: {
          reason: "configured_target_unavailable",
          configured_target: { mode: "custom_ref", requested_ref: "release/v1" },
          unavailable_cause: "non_commit",
        },
      }),
    ).toEqual({
      outcome: "selection_required",
      selectionRequired: {
        reason: "configured_target_unavailable",
        configuredTarget: { mode: "custom_ref", requestedRef: "release/v1" },
        unavailableCause: "non_commit",
      },
    });
    expect(
      taskApproveResponseSchema.parse({
        outcome: "selection_required",
        selection_required: { reason: "policy_requires_selection" },
      }),
    ).toEqual({
      outcome: "selection_required",
      selectionRequired: { reason: "policy_requires_selection" },
    });
  });

  it("maps dependency confirmation and sends explicit proceed intent", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.task.start",
        result: {
          outcome: "dependency_confirmation_required",
          unsatisfied_dependency_count: 2,
        },
      },
      {
        method: "workflow.task.move",
        result: {
          outcome: "dependency_confirmation_required",
          unsatisfied_dependency_count: 3,
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.startTask({ taskID: "task-1", proceedDespiteDependencies: true })).resolves.toEqual({
      outcome: "dependency_confirmation_required",
      unsatisfiedDependencyCount: 2,
    });
    await expect(
      client.moveTask({
        taskID: "task-1",
        targetNodeID: "node-2",
        proceedDespiteDependencies: true,
      }),
    ).resolves.toEqual({
      outcome: "dependency_confirmation_required",
      unsatisfiedDependencyCount: 3,
    });

    expect(transport.calls[0]?.params).toMatchObject({ proceed_despite_dependencies: true });
    expect(transport.calls[1]?.params).toMatchObject({ proceed_despite_dependencies: true });
  });

  it("rejects malformed lifecycle responses and empty applied Current Nodes", () => {
    const schemas = [taskStartResponseSchema, taskMoveResponseSchema, taskApproveResponseSchema];
    for (const schema of schemas) {
      expect(() =>
        schema.parse({
          outcome: "applied",
          applied:
            schema === taskApproveResponseSchema
              ? { task_id: "task-1", current_nodes: [] }
              : { current_nodes: [] },
        }),
      ).toThrow();
      expect(() =>
        schema.parse({
          outcome: "selection_required",
          selection_required: { reason: "policy_requires_selection", extra: true },
        }),
      ).toThrow();
    }
  });
});
