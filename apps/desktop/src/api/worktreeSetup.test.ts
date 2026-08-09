import { z } from "zod";

import { ApiClient } from "./client";
import { ContractError, RpcError } from "./errors";
import { FakeRpcTransport, registeredWorktreeWire } from "@/test-support/api";
import { newSetupOperationID, parseSetupOperationID, type SetupOperationID } from "./setupOperationID";
import type { WorktreeSetupEvent } from "./worktreeSetup";
import {
  decodeWorktreeSetupRetainedError,
  WorktreeSetupRetainedError,
} from "./worktreeSetupFailure";

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
  it("decodes the retained setup error used by synchronous Move recovery", () => {
    const decoded = decodeWorktreeSetupRetainedError(
      new RpcError({
        code: -32039,
        method: "workflow.task.move",
        message: "human text is not the contract",
        data: {
          type: "worktree_setup_retained",
          worktree: registeredWorktreeWire("/repo/current", "worktree-current"),
          diagnostic: "setup failed after retry",
          script_path: "/repo/setup.sh",
          retained_previous_worktree: null,
        },
      }),
    );
    expect(decoded).toBeInstanceOf(WorktreeSetupRetainedError);
    expect(decoded).toMatchObject({
      diagnostic: "setup failed after retry",
      scriptPath: "/repo/setup.sh",
      worktree: { registered: { kent: { canonicalRoot: "/repo/current" } } },
    });
  });

  it("rejects malformed setup operation ids before RPC submission can use them", () => {
    expect(() => parseSetupOperationID("11111111-1111-1111-1111-111111111111")).toThrow(
      "Setup operation id must be a UUID v4.",
    );
    expect(() => parseSetupOperationID("not-a-uuid")).toThrow("Setup operation id must be a UUID v4.");
  });

  it("keeps Start correlation and sends no setup correlation for synchronous Move", async () => {
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
            retained_previous_worktree: null,
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

  it("decodes terminal phase payloads and rejects inapplicable sentinel fields", () => {
    const transport = new FakeRpcTransport([]);
    const client = new ApiClient(transport);
    const setupOperationID = newSetupOperationID();
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

    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: setupOperationID.toJSONValue(),
        phase: "not_required",
        not_required: {
          reason: "no_configured_script",
          retained_previous_worktree: {
            worktree: {
              variant: "registered",
              registered: {
                git: {
                  canonical_root: "/old-worktree",
                  head_object: "abc123",
                  branch_ref: "refs/heads/old-worktree",
                  branch_name: "old-worktree",
                  detached: false,
                  bare: false,
                  locked_reason: null,
                  prunable_reason: null,
                  is_main: false,
                  path_available: true,
                },
                kent: {
                  worktree_id: "worktree-old",
                  canonical_root: "/old-worktree",
                  display_name: "old-worktree",
                  managed: true,
                  created_branch: false,
                  origin_session_id: null,
                },
              },
            },
          },
        },
      },
    });
    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: setupOperationID.toJSONValue(),
        phase: "failed",
        failed: {
          retry_readiness: "retry_ready",
          cause: {
            kind: "process_exit",
            process_exit: { exit_code: 7, stdout: "", stderr: null },
          },
          diagnostic: "setup exited",
          script_path: "/src/setup.sh",
          execution_target: { mode: "custom_ref", custom_ref: "refs/heads/recovery" },
          retained_worktree: registeredWorktreeWire("/worktree", "worktree-current"),
          retained_previous_worktree: null,
        },
      },
    });
    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: setupOperationID.toJSONValue(),
        phase: "not_required",
        not_required: {
          reason: "no_configured_script",
          retained_previous_worktree: null,
        },
        script_path: "",
      },
    });
    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: setupOperationID.toJSONValue(),
        phase: "failed",
        failed: {
          retry_readiness: "retry_ready",
          cause: {
            kind: "canceled",
            canceled: {},
          },
          diagnostic: "preparation canceled",
          script_path: null,
          execution_target: null,
          retained_worktree: null,
          retained_previous_worktree: null,
        },
      },
    });

    expect(events).toMatchObject([
      {
        setupOperationID,
        phase: "not_required",
        notRequired: {
          reason: "no_configured_script",
          retainedPreviousWorktree: {
            worktree: {
              variant: "registered",
              registered: {
                git: {
                  canonicalRoot: "/old-worktree",
                  headObject: "abc123",
                  branchRef: "refs/heads/old-worktree",
                  branchName: "old-worktree",
                  detached: false,
                  bare: false,
                  lockedReason: null,
                  prunableReason: null,
                  isMain: false,
                  pathAvailable: true,
                },
                kent: {
                  worktreeID: "worktree-old",
                  canonicalRoot: "/old-worktree",
                  displayName: "old-worktree",
                  managed: true,
                  createdBranch: false,
                  originSessionID: null,
                },
              },
            },
          },
        },
      },
      {
        setupOperationID,
        phase: "failed",
        failed: {
          retryReadiness: "retry_ready",
          cause: { kind: "process_exit", exitCode: 7, stdout: "", stderr: null },
          diagnostic: "setup exited",
          scriptPath: "/src/setup.sh",
          executionTarget: { mode: "custom_ref", customRef: "refs/heads/recovery" },
          retainedWorktree: {
            registered: {
              git: { canonicalRoot: "/worktree" },
              kent: { worktreeID: "worktree-current", canonicalRoot: "/worktree" },
            },
          },
          retainedPreviousWorktree: null,
        },
      },
    ]);
    expect(errors).toHaveLength(2);
    expect(errors[0]).toBeInstanceOf(ContractError);
  });

  it("accepts explicit null and rejects omitted nullable retained Worktree facts", () => {
    const transport = new FakeRpcTransport([]);
    const client = new ApiClient(transport);
    const setupOperationID = newSetupOperationID();
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

    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: setupOperationID.toJSONValue(),
        phase: "completed",
        completed: { retained_previous_worktree: null },
      },
    });
    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: setupOperationID.toJSONValue(),
        phase: "not_required",
        not_required: {
          reason: "no_configured_script",
          retained_previous_worktree: null,
        },
      },
    });
    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: setupOperationID.toJSONValue(),
        phase: "completed",
        completed: {},
      },
    });
    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: setupOperationID.toJSONValue(),
        phase: "not_required",
        not_required: { reason: "no_configured_script" },
      },
    });

    expect(events).toMatchObject([
      {
        phase: "completed",
        completed: { retainedPreviousWorktree: null },
      },
      {
        phase: "not_required",
        notRequired: {
          reason: "no_configured_script",
          retainedPreviousWorktree: null,
        },
      },
    ]);
    expect(errors).toHaveLength(2);
    expect(errors[0]).toBeInstanceOf(ContractError);
    expect(errors[1]).toBeInstanceOf(ContractError);
  });

  it("rejects omitted nullable setup-failure facts", () => {
    const transport = new FakeRpcTransport([]);
    const client = new ApiClient(transport);
    const setupOperationID = newSetupOperationID();
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

    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: setupOperationID.toJSONValue(),
        phase: "failed",
        failed: {
          retry_readiness: "non_retryable",
          cause: { kind: "canceled", canceled: {} },
          diagnostic: "canceled",
          script_path: null,
        },
      },
    });

    expect(events).toHaveLength(0);
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(ContractError);
  });

  it("accepts explicit null and rejects omitted setup output streams", () => {
    const transport = new FakeRpcTransport([]);
    const client = new ApiClient(transport);
    const setupOperationID = newSetupOperationID();
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

    const failure = {
      retry_readiness: "retry_ready",
      diagnostic: "setup failed",
      script_path: "/src/setup.sh",
      execution_target: { mode: "head" },
      retained_worktree: registeredWorktreeWire("/worktree", "worktree-current"),
      retained_previous_worktree: null,
    } as const;
    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: setupOperationID.toJSONValue(),
        phase: "failed",
        failed: {
          ...failure,
          cause: {
            kind: "process_exit",
            process_exit: { exit_code: 1, stdout: null, stderr: null },
          },
        },
      },
    });
    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: setupOperationID.toJSONValue(),
        phase: "failed",
        failed: {
          ...failure,
          cause: {
            kind: "timeout",
            timeout: { stdout: null, stderr: null },
          },
        },
      },
    });
    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: setupOperationID.toJSONValue(),
        phase: "failed",
        failed: {
          ...failure,
          cause: {
            kind: "process_exit",
            process_exit: { exit_code: 1, stderr: null },
          },
        },
      },
    });
    transport.emit("worktree.setup", {
      event: {
        setup_operation_id: setupOperationID.toJSONValue(),
        phase: "failed",
        failed: {
          ...failure,
          cause: {
            kind: "timeout",
            timeout: { stdout: null },
          },
        },
      },
    });

    expect(events).toHaveLength(2);
    expect(events).toMatchObject([
      {
        phase: "failed",
        failed: {
          cause: {
            kind: "process_exit",
            stdout: null,
            stderr: null,
          },
        },
      },
      {
        phase: "failed",
        failed: {
          cause: {
            kind: "timeout",
            stdout: null,
            stderr: null,
          },
        },
      },
    ]);
    expect(errors).toHaveLength(2);
    expect(errors[0]).toBeInstanceOf(ContractError);
    expect(errors[1]).toBeInstanceOf(ContractError);
  });
});
