import { z } from "zod";

import { ApiClient } from "./client";
import { ContractError } from "./errors";
import { FakeRpcTransport } from "@/test-support/api";
import { newSetupOperationID, parseSetupOperationID, type SetupOperationID } from "./setupOperationID";
import type { WorktreeSetupEvent } from "./worktreeSetup";
import { parseTaskSetupRecoveryDetail } from "./worktreeSetup";

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
  it("decodes canonical Task setup recovery without fabricating topology", () => {
    const recovery = parseTaskSetupRecoveryDetail(JSON.stringify({
      setup_recovery: {
        setup_operation_id: "55555555-5555-4555-8555-555555555555",
        cause: "target_preparation", diagnostic: "target failed", script_path: null,
        setup_requirement: "required", retained_worktree: null, retained_previous_worktree: null,
        execution_target: { mode: "head" },
      },
    }));
    expect(recovery).toMatchObject({
      cause: "target_preparation", diagnostic: "target failed",
      executionTarget: { mode: "head", customRef: null },
      retainedWorktree: null,
    });
    expect(parseTaskSetupRecoveryDetail(JSON.stringify({ code: "user_interrupt" }))).toBeNull();
    expect(() => parseTaskSetupRecoveryDetail('{"setup_recovery":{}}')).toThrow(ContractError);
  });

  it("rejects malformed setup operation ids before RPC submission can use them", () => {
    expect(() => parseSetupOperationID("11111111-1111-1111-1111-111111111111")).toThrow(
      "Setup operation id must be a UUID v4.",
    );
    expect(() => parseSetupOperationID("not-a-uuid")).toThrow("Setup operation id must be a UUID v4.");
  });

  it("uses caller-provided setup operation ids only for asynchronous workflow lifecycle mutations", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.task.start",
        result: {
          outcome: "applied",
          applied: {
            current_nodes: [{ node_id: "node-1", transition_branch_key: null, session_id: null }],
          },
        },
      },
      {
        method: "workflow.task.move",
        result: {
          outcome: "applied",
          applied: {
            current_nodes: [{ node_id: "node-1", transition_branch_key: null, session_id: null }],
          },
        },
      },
    ]);
    const client = new ApiClient(transport);
    const startSetupID = newSetupOperationID();

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
    await client.startTask({ taskID: "task-1", setupOperationID: startSetupID });
    await client.moveTask({
      taskID: "task-1",
      targetNodeID: "node-1",
    });

    expect(transport.subscriptions).toContainEqual({
      method: "worktree.setup.subscribe",
      params: { setup_operation_id: startSetupID.toJSONValue() },
    });
    const startCall = transport.calls.find((entry) => entry.method === "workflow.task.start");
    expect(startCall?.options).toEqual({ timeoutMs: null });
    expect(parseSetupMutationParams(startCall?.params).setupOperationID.toJSONValue()).toBe(
      startSetupID.toJSONValue(),
    );
    const moveCall = transport.calls.find((entry) => entry.method === "workflow.task.move");
    expect(moveCall?.options).toEqual({ timeoutMs: null });
    expect(moveCall?.params).not.toHaveProperty("setup_operation_id");
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
        phase: "started",
        started: {
          source_workspace_root: "/src",
          worktree_root: "/worktree",
          script_path: "/src/setup.sh",
        },
      },
    });

    expect(events).toEqual([
      {
        setupOperationID,
        phase: "started",
        started: {
          sourceWorkspaceRoot: "/src",
          worktreeRoot: "/worktree",
          scriptPath: "/src/setup.sh",
        },
      },
    ]);

    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: "not-a-uuid",
        phase: "started",
        started: {
          source_workspace_root: "/src",
          worktree_root: "/worktree",
          script_path: "/src/setup.sh",
        },
      },
    });

    expect(errors[0]).toBeInstanceOf(ContractError);
  });
});
