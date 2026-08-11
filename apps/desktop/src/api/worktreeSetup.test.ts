import { z } from "zod";

import { ApiClient } from "./client";
import { ContractError } from "./errors";
import { FakeRpcTransport } from "@/test-support/api";
import { subscriptionCompleteMethod } from "./jsonRpcSocket";
import { newSetupOperationID, parseSetupOperationID, type SetupOperationID } from "./setupOperationID";
import type { WorktreeSetupEvent } from "./worktreeSetup";
import { parseTaskSetupRecoveryDetail, worktreeSetupEventParamsSchema } from "./worktreeSetup";

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
  it("decodes every setup failure cause and rejects contradictory payloads", () => {
    const causes = [
      [{ kind: "process_exit", process_exit: { exit_code: 7, stdout: "output", stderr: "failure" } }, "retry_ready"],
      [{ kind: "timeout", timeout: { stdout: null, stderr: null } }, "retry_ready"],
      [{ kind: "target_preparation", target_preparation: {} }, "retry_ready"],
      [{ kind: "interruption_persistence", interruption_persistence: {} }, "non_retryable"],
      [{ kind: "canceled", canceled: {} }, "non_retryable"],
      [{ kind: "controller_shutdown", controller_shutdown: {} }, "non_retryable"],
      [{ kind: "operational", operational: {} }, "non_retryable"],
    ] as const;
    const decoded = causes.map(([cause, readiness]) =>
      worktreeSetupEventParamsSchema.parse({ event: failedSetupEvent(cause, readiness) }).event);
    expect(decoded.map((event) => event.phase === "failed" ? event.failed.cause.kind : null))
      .toEqual(causes.map(([cause]) => cause.kind));
    expect(decoded[0]).toMatchObject({ failed: { cause: {
      kind: "process_exit", exitCode: 7, stdout: "output", stderr: "failure",
    } } });
    expect(decoded[1]).toMatchObject({ failed: { cause: { kind: "timeout", stdout: null, stderr: null } } });
    const retryable = failedSetupEvent(causes[0][0], "retry_ready");
    for (const event of [
      failedSetupEvent({ kind: "canceled", canceled: {} }, "retry_ready"),
      { ...retryable, failed: { ...retryable.failed, retained_worktree: null } },
      { ...failedSetupEvent(causes[2][0], "retry_ready"),
        failed: { ...failedSetupEvent(causes[2][0], "retry_ready").failed, script_path: "/setup.sh" } },
      { ...retryable, failed: { ...retryable.failed, diagnostic: "" } },
    ]) expect(() => worktreeSetupEventParamsSchema.parse({ event })).toThrow();
  });

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

  it("forwards one terminal outcome or error and closes once", () => {
    expect(subscriptionCompleteMethod("worktree.setup.subscribe")).toBe("worktree.setup.complete");
    for (const terminal of [completedSetupEvent, notRequiredSetupEvent,
      failedSetupEvent({ kind: "canceled", canceled: {} }, "non_retryable")]) {
      const transport = new FakeRpcTransport([]);
      const observed = observe(new ApiClient(transport));
      transport.emit("worktree.setup", { event: startedSetupEvent });
      transport.emit("worktree.setup", { event: terminal });
      transport.complete("worktree.setup.subscribe", 0, "");
      transport.fail("worktree.setup.subscribe", new Error("late"));
      expect(observed.events.map(({ phase }) => phase)).toEqual(["started", terminal.phase]);
      expect(observed.completions).toEqual([0]);
      expect(observed.errors).toEqual([]);
      expect(transport.subscriptions).toEqual([]);
    }
    for (const trigger of [
      (transport: FakeRpcTransport) => {
        transport.emit("worktree.setup",
          { event: { ...startedSetupEvent, started: { ...startedSetupEvent.started, script_path: "" } } });
      },
      (transport: FakeRpcTransport) => {
        transport.fail("worktree.setup.subscribe", new Error("connection"));
      },
      (transport: FakeRpcTransport) => {
        transport.complete("worktree.setup.subscribe", 409, "conflict");
        transport.fail("worktree.setup.subscribe", new Error("subscription failed"));
      },
    ]) {
      const transport = new FakeRpcTransport([]);
      const observed = observe(new ApiClient(transport));
      trigger(transport);
      transport.fail("worktree.setup.subscribe", new Error("late"));
      expect(observed.completions).toEqual([]);
      expect(observed.errors).toHaveLength(1);
      expect(transport.subscriptions).toEqual([]);
    }
  });
});

const setupIDWire = "123e4567-e89b-42d3-a456-426614174000";
const setupID = parseSetupOperationID(setupIDWire);
const retainedWorktree = { variant: "registered", registered: {
  git: { canonical_root: "/worktree", head_object: "abc", branch_ref: null, branch_name: null,
    detached: true, bare: false, locked_reason: null, prunable_reason: null, is_main: false, path_available: true },
  kent: { worktree_id: "worktree-1", canonical_root: "/worktree", display_name: "feature",
    managed: true, created_branch: false, origin_session_id: null },
} } as const;
const startedSetupEvent = { setup_operation_id: setupIDWire, phase: "started",
  started: { source_workspace_root: "/source", worktree_root: "/worktree", script_path: "/setup.sh" } } as const;
const completedSetupEvent = { setup_operation_id: setupIDWire, phase: "completed",
  completed: { retained_previous_worktree: null } } as const;
const notRequiredSetupEvent = { setup_operation_id: setupIDWire, phase: "not_required",
  not_required: { reason: "no_configured_script", retained_previous_worktree: null } } as const;
function failedSetupEvent(cause: Readonly<{ kind: string }> & Readonly<Record<string, unknown>>,
  retryReadiness: "retry_ready" | "non_retryable") {
  const scriptFailure = retryReadiness === "retry_ready" && cause.kind !== "target_preparation";
  return { setup_operation_id: setupIDWire, phase: "failed", failed: {
    retry_readiness: retryReadiness, cause, diagnostic: "setup failed",
    script_path: scriptFailure ? "/setup.sh" : null, execution_target: null,
    retained_worktree: scriptFailure ? retainedWorktree : null, retained_previous_worktree: null,
  } } as const;
}
function observe(client: ApiClient) {
  const events: WorktreeSetupEvent[] = [], completions: number[] = [], errors: Error[] = [];
  client.subscribeWorktreeSetup(setupID, {
    onEvent: (event) => { events.push(event); },
    onComplete: (code) => { completions.push(code); },
    onError: (error) => { errors.push(error); },
  });
  return { events, completions, errors };
}
