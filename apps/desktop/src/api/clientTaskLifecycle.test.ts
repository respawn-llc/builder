import { ApiClient } from "./client";
import { ContractError } from "./errors";
import { FakeRpcTransport } from "./fakeTransport";

const appliedStartResponse = {
  outcome: "applied",
  applied: {
    transition_id: "transition-1",
    placement_id: "placement-1",
    run_id: "run-1",
  },
} as const;

describe("task lifecycle client", () => {
  it("rejects legacy top-level workflow move responses", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([{ method: "workflow.task.move", result: { approval_error: "approval failed" } }]),
    );

    await expect(
      client.moveTask({
        taskID: "task-1",
        targetNodeID: "node-1",
        allowMissingEdge: true,
        autoApprove: true,
      }),
    ).rejects.toBeInstanceOf(ContractError);
  });

  it("returns workflow move run ids from successful responses", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([
        {
          method: "workflow.task.move",
          result: {
            outcome: "applied",
            applied: {
              transition_id: "transition-1",
              state: "approved",
              placement_ids: ["placement-1"],
              run_ids: ["run-1"],
            },
          },
        },
      ]),
    );

    await expect(
      client.moveTask({
        taskID: "task-1",
        targetNodeID: "node-1",
        allowMissingEdge: true,
        autoApprove: true,
      }),
    ).resolves.toMatchObject({
      outcome: "applied",
      applied: {
        placementIDs: ["placement-1"],
        runIDs: ["run-1"],
        state: "approved",
        transitionID: "transition-1",
      },
    });
  });

  it("parses both execution-target selection reasons without accepting server-supplied choices", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([
        {
          method: "workflow.task.start",
          result: {
            outcome: "selection_required",
            selection_required: { reason: "policy_requires_selection" },
          },
        },
        {
          method: "workflow.task.move",
          result: {
            outcome: "selection_required",
            selection_required: {
              reason: "configured_target_unavailable",
              configured_target: { mode: "custom_ref", requested_ref: "release/v2" },
              unavailable_cause: "invalid_revision",
            },
          },
        },
      ]),
    );

    await expect(client.startTask("task-1")).resolves.toEqual({
      outcome: "selection_required",
      selectionRequired: { reason: "policy_requires_selection" },
    });
    await expect(client.moveTask({ taskID: "task-1", targetNodeID: "node-1" })).resolves.toEqual({
      outcome: "selection_required",
      selectionRequired: {
        reason: "configured_target_unavailable",
        configuredTarget: { mode: "custom_ref", requestedRef: "release/v2" },
        unavailableCause: "invalid_revision",
      },
    });
  });

  it("rejects malformed execution-target selection requirements and legacy allowed modes", async () => {
    for (const result of [
      {
        outcome: "selection_required",
        selection_required: {
          reason: "policy_requires_selection",
          allowed_modes: ["head"],
        },
      },
      {
        outcome: "selection_required",
        selection_required: {
          reason: "configured_target_unavailable",
          unavailable_cause: "invalid_revision",
        },
      },
    ]) {
      const client = new ApiClient(
        new FakeRpcTransport([{ method: "workflow.task.start", result }]),
      );
      await expect(client.startTask("task-1")).rejects.toBeInstanceOf(ContractError);
    }
  });

  it("sends a concrete task-local execution target for start, move, and approval continuations", async () => {
    const transport = new FakeRpcTransport([
      { method: "workflow.task.start", result: appliedStartResponse },
      {
        method: "workflow.task.move",
        result: {
          outcome: "applied",
          applied: { transition_id: "transition-2", state: "approved" },
        },
      },
      {
        method: "workflow.task.approve",
        result: {
          outcome: "applied",
          applied: {
            transition_id: "transition-3",
            task_id: "task-1",
            state: "approved",
          },
        },
      },
    ]);
    const client = new ApiClient(transport);

    await client.startTask("task-1", undefined, { mode: "default_branch", customRef: null });
    await client.moveTask({
      taskID: "task-1",
      targetNodeID: "node-1",
      executionTarget: { mode: "custom_ref", customRef: "release/v2" },
    });
    await client.approveTransition(
      "transition-3",
      undefined,
      { mode: "none", customRef: null },
    );

    expect(transport.calls.map((call) => call.params)).toMatchObject([
      { execution_target: { mode: "default_branch" } },
      { execution_target: { mode: "custom_ref", custom_ref: "release/v2" } },
      { execution_target: { mode: "none" } },
    ]);
  });
});
