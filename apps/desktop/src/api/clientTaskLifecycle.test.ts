import { ApiClient } from "./client";
import {
  taskApproveResponseSchema,
  taskMovePreviewResponseSchema,
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
        method: "workflow.task.move.preview",
        result: {
          outcome: "transition",
          transition: {
            choices: [
              {
                transition_key: "next",
                label: "Next",
                source_node_display_name: "Plan",
                required_values: [
                  {
                    node_key: "plan",
                    output_name: "summary",
                    description: "Summary",
                    resolved_value: "prefilled",
                  },
                ],
              },
            ],
          },
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

    await expect(client.startTask({ taskID: "task-1" })).resolves.toMatchObject({ outcome: "applied", applied: { currentNodes: [{ nodeID: "node-1" }] } });
    await expect(client.moveTask({ taskID: "task-1", targetNodeID: "node-2" })).resolves.toMatchObject({ outcome: "applied", applied: { currentNodes: [{ nodeID: "node-2" }] } });
    await expect(client.previewMoveTask("task-1", "node-2")).resolves.toEqual({
      outcome: "transition",
      transition: {
        choices: [
          {
            transitionKey: "next",
            label: "Next",
            sourceNodeDisplayName: "Plan",
            requiredValues: [
              { nodeKey: "plan", outputName: "summary", description: "Summary", resolvedValue: "prefilled" },
            ],
          },
        ],
      },
    });
    await expect(client.approveApproval("approval-1")).resolves.toMatchObject({ outcome: "applied", applied: { taskID: "task-1", currentNodes: [{ nodeID: "node-3" }] } });

    expect(transport.calls).toContainEqual({
      method: "workflow.task.approve",
      options: { timeoutMs: null },
      params: { approval_id: "approval-1" },
    });
    expect(transport.calls.find((call) => call.method === "workflow.task.move")?.params).not.toHaveProperty("allow_missing_edge");
    expect(transport.calls.find((call) => call.method === "workflow.task.move")?.params).not.toHaveProperty("auto_approve");
    expect(transport.calls.find((call) => call.method === "workflow.task.move.preview")).toEqual({
      method: "workflow.task.move.preview",
      options: { timeoutMs: null },
      params: { task_id: "task-1", target_node_id: "node-2" },
    });
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
      taskMoveResponseSchema.parse({
        outcome: "dependency_confirmation_required",
        unsatisfied_dependency_count: 2,
      }),
    ).toEqual({
      outcome: "dependency_confirmation_required",
      unsatisfiedDependencyCount: 2,
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

  it("parses every Manual Move preview outcome and same-current no-op", () => {
    expect(
      taskMovePreviewResponseSchema.parse({
        outcome: "no_op",
        no_op: { current_nodes: [{ node_id: "node-1", transition_branch_key: null, session_id: null }] },
      }),
    ).toEqual({
      outcome: "no_op",
      noOp: { currentNodes: [{ nodeID: "node-1", transitionBranchKey: null, sessionID: null }] },
    });
    expect(taskMovePreviewResponseSchema.parse({ outcome: "direct", direct: {} })).toEqual({
      outcome: "direct",
      direct: {},
    });
    expect(
      taskMovePreviewResponseSchema.parse({
        outcome: "blocked",
        blocked: { reason: "waiting_question" },
      }),
    ).toEqual({ outcome: "blocked", blocked: { reason: "waiting_question" } });
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
