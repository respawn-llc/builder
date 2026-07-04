import { z } from "zod";

import { ApiClient } from "./client";
import { ContractError } from "./errors";
import { FakeRpcTransport } from "./fakeTransport";
import { newSetupOperationID, parseSetupOperationID, type SetupOperationID } from "./setupOperationID";
import type { WorktreeSetupEvent } from "./worktreeSetup";

const setupOperationIDWireSchema = z.string().transform((value, ctx): SetupOperationID => {
  try {
    return parseSetupOperationID(value);
  } catch {
    ctx.addIssue({ code: "custom", message: "Expected setup operation id UUID v4." });
    return z.NEVER;
  }
});

const setupMutationParamsSchema = z.object({
  setup_operation_id: setupOperationIDWireSchema,
});

function parseSetupMutationParams(value: unknown): Readonly<{ setupOperationID: SetupOperationID }> {
  const parsed = setupMutationParamsSchema.parse(value);
  return { setupOperationID: parsed.setup_operation_id };
}

describe("worktree setup API", () => {
  it("rejects malformed setup operation ids before RPC submission can use them", () => {
    expect(() => parseSetupOperationID("11111111-1111-1111-1111-111111111111")).toThrow(
      "Setup operation id must be a UUID v4.",
    );
    expect(() => parseSetupOperationID("not-a-uuid")).toThrow("Setup operation id must be a UUID v4.");
  });

  it("uses caller-provided setup operation ids and disables generic timeouts for workflow lifecycle mutations", async () => {
    const transport = new FakeRpcTransport([
      { method: "workflow.task.start", result: {} },
      {
        method: "workflow.task.approve",
        result: { transition_id: "transition-1", task_id: "task-1", state: "approved" },
      },
      {
        method: "workflow.task.move",
        result: { transition_id: "transition-1", state: "approved", run_ids: [] },
      },
    ]);
    const client = new ApiClient(transport);
    const startSetupID = newSetupOperationID();
    const approveSetupID = newSetupOperationID();
    const moveSetupID = newSetupOperationID();

    client.subscribeWorktreeSetup(startSetupID, {
      onEvent() {
        return;
      },
      onComplete() {
        return;
      },
      onError(error) {
        throw error;
      },
    });
    await client.startTask("task-1", startSetupID);
    await client.approveTransition("transition-1", approveSetupID);
    await client.moveTask({
      taskID: "task-1",
      targetNodeID: "node-1",
      allowMissingEdge: true,
      autoApprove: true,
      setupOperationID: moveSetupID,
    });

    expect(transport.subscriptions).toContainEqual({
      method: "worktree.setup.subscribe",
      params: { setup_operation_id: startSetupID.toJSONValue() },
    });
    for (const [method, expectedSetupID] of [
      ["workflow.task.start", startSetupID],
      ["workflow.task.approve", approveSetupID],
      ["workflow.task.move", moveSetupID],
    ] as const) {
      const call = transport.calls.find((entry) => entry.method === method);
      expect(call?.options).toEqual({ timeoutMs: null });
      expect(parseSetupMutationParams(call?.params).setupOperationID.toJSONValue()).toBe(
        expectedSetupID.toJSONValue(),
      );
    }
  });

  it("subscribes to typed worktree setup events and rejects malformed setup ids", () => {
    const transport = new FakeRpcTransport([]);
    const client = new ApiClient(transport);
    const setupOperationID = parseSetupOperationID("123e4567-e89b-42d3-a456-426614174000");
    const events: WorktreeSetupEvent[] = [];
    const errors: Error[] = [];

    client.subscribeWorktreeSetup(setupOperationID, {
      onEvent(event) {
        events.push(event);
      },
      onComplete() {
        return;
      },
      onError(error) {
        errors.push(error);
      },
    });

    expect(transport.subscriptions).toContainEqual({
      method: "worktree.setup.subscribe",
      params: { setup_operation_id: setupOperationID.toJSONValue() },
    });
    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: setupOperationID.toJSONValue(),
        source_workspace_root: "/src",
        worktree_root: "/worktree",
        script_path: "/src/setup.sh",
        phase: "started",
      },
    });

    expect(events).toEqual([
      {
        setupOperationID,
        sourceWorkspaceRoot: "/src",
        worktreeRoot: "/worktree",
        scriptPath: "/src/setup.sh",
        phase: "started",
        timeout: false,
        canceled: false,
        stdout: "",
        stderr: "",
        error: "",
      },
    ]);

    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: "not-a-uuid",
        source_workspace_root: "/src",
        worktree_root: "/worktree",
        script_path: "/src/setup.sh",
        phase: "started",
      },
    });

    expect(errors[0]).toBeInstanceOf(ContractError);
  });
});
