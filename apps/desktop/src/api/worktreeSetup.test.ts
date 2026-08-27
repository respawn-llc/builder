import { FakeRpcTransport } from "@/test-support/api";
import { create } from "@app/server-api-contract";
import {
  SetupCompletionSchema,
  SetupEventSchema,
  SetupFailureCauseSchema,
  SetupNotRequiredReason,
  SetupRetryReadiness,
  SetupService,
  SetupStartResultSchema,
  type SetupEvent,
} from "@app/server-api-contract/gen/kent/api/worktree/worktree_pb";
import { z } from "zod";

import { ApiClient } from "./client";
import { ContractError } from "./errors";
import { parseTaskSetupRecoveryDetail } from "./schemas/workflowBoard";
import { newSetupOperationID, parseSetupOperationID, type SetupOperationID } from "./setupOperationID";

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
const setupIDWire = "123e4567-e89b-42d3-a456-426614174000";
const setupID = parseSetupOperationID(setupIDWire);
const startedSetupEvent = create(SetupEventSchema, {
  setupOperationId: setupIDWire,
  phase: {
    case: "started",
    value: {
      sourceWorkspaceRoot: "/source",
      worktreeRoot: "/worktree",
      scriptPath: "/setup.sh",
    },
  },
});
const completedSetupEvent = create(SetupEventSchema, {
  setupOperationId: setupIDWire,
  phase: { case: "completed", value: {} },
});
const notRequiredSetupEvent = create(SetupEventSchema, {
  setupOperationId: setupIDWire,
  phase: {
    case: "notRequired",
    value: {
      reason: SetupNotRequiredReason.WORKTREE_SETUP_NOT_REQUIRED_REASON_NO_CONFIGURED_SCRIPT,
    },
  },
});
const failedSetupEvent = create(SetupEventSchema, {
  setupOperationId: setupIDWire,
  phase: {
    case: "failed",
    value: {
      retryReadiness: SetupRetryReadiness.WORKTREE_SETUP_NON_RETRYABLE,
      cause: create(SetupFailureCauseSchema, {
        cause: { case: "canceled", value: {} },
      }),
      diagnostic: "setup failed",
    },
  },
});

describe("worktree setup API", () => {
  it("decodes canonical Task setup recovery without fabricating topology", () => {
    const recovery = parseTaskSetupRecoveryDetail(
      JSON.stringify({
        setup_recovery: {
          setup_operation_id: "55555555-5555-4555-8555-555555555555",
          cause: "target_preparation",
          diagnostic: "target failed",
          script_path: null,
          setup_requirement: "required",
          retained_worktree: null,
          retained_previous_worktree: null,
          execution_target: { mode: "head" },
        },
      }),
    );

    expect(recovery).toMatchObject({
      cause: "target_preparation",
      diagnostic: "target failed",
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
      setupSubscriptionRoute(),
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

    expect(transport.descriptorSubscriptionStarts).toContainEqual({
      descriptor: SetupService.method.subscribe,
      request: create(SetupService.method.subscribe.input, {
        setupOperationId: startSetupID.toJSONValue(),
      }),
    });
    const startCall = transport.calls.find((entry) => entry.method === "workflow.task.start");
    expect(startCall?.options).toEqual({ timeoutMs: null });
    expect(setupMutationParamsSchema.parse(startCall?.params).setup_operation_id.toJSONValue()).toBe(
      startSetupID.toJSONValue(),
    );
    const moveCall = transport.calls.find((entry) => entry.method === "workflow.task.move");
    expect(moveCall?.options).toEqual({ timeoutMs: null });
    expect(moveCall?.params).not.toHaveProperty("setup_operation_id");
  });

  it("subscribes to typed worktree setup events and rejects malformed setup ids", () => {
    const transport = successfulTransport();
    const client = new ApiClient(transport);
    const events: SetupEvent[] = [];
    const errors: Error[] = [];

    client.subscribeWorktreeSetup(setupID, {
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
    transport.openDescriptor(SetupService.method.subscribe);
    transport.emitDescriptor(SetupService.method.subscribe, SetupService.method.event, startedSetupEvent);

    expect(transport.descriptorSubscriptionStarts).toContainEqual({
      descriptor: SetupService.method.subscribe,
      request: create(SetupService.method.subscribe.input, {
        setupOperationId: setupID.toJSONValue(),
      }),
    });
    expect(events).toEqual([startedSetupEvent]);
    expect(errors).toEqual([]);
  });

  it("forwards one terminal outcome or error and closes once", () => {
    for (const terminal of [completedSetupEvent, notRequiredSetupEvent, failedSetupEvent]) {
      const transport = successfulTransport();
      const observed = observe(new ApiClient(transport));
      transport.openDescriptor(SetupService.method.subscribe);
      transport.emitDescriptor(SetupService.method.subscribe, SetupService.method.event, startedSetupEvent);
      transport.emitDescriptor(SetupService.method.subscribe, SetupService.method.event, terminal);
      transport.completeDescriptor(
        SetupService.method.subscribe,
        SetupService.method.complete,
        create(SetupCompletionSchema),
      );
      transport.failDescriptor(SetupService.method.subscribe, new Error("late"));
      expect(observed.events.map(({ phase }) => phase.case)).toEqual(["started", terminal.phase.case]);
      expect(observed.completions).toEqual(["complete"]);
      expect(observed.errors).toEqual([]);
      expect(transport.descriptorSubscriptions).toEqual([]);
    }
    for (const trigger of [
      (transport: FakeRpcTransport) => {
        transport.emitDescriptorBytes(SetupService.method.subscribe, new Uint8Array([0xff]));
      },
      (transport: FakeRpcTransport) => {
        transport.failDescriptor(SetupService.method.subscribe, new Error("connection"));
      },
      (transport: FakeRpcTransport) => {
        transport.completeDescriptor(
          SetupService.method.subscribe,
          SetupService.method.complete,
          create(SetupCompletionSchema, { code: 409, diagnostic: "conflict" }),
        );
        transport.failDescriptor(SetupService.method.subscribe, new Error("subscription failed"));
      },
    ]) {
      const transport = successfulTransport();
      const observed = observe(new ApiClient(transport));
      transport.openDescriptor(SetupService.method.subscribe);
      trigger(transport);
      transport.failDescriptor(SetupService.method.subscribe, new Error("late"));
      expect(observed.completions).toEqual([]);
      expect(observed.errors).toHaveLength(1);
      expect(transport.descriptorSubscriptions).toEqual([]);
    }
  });
});

function setupSubscriptionRoute() {
  return {
    subscriptionDescriptor: SetupService.method.subscribe,
    startResult: create(SetupStartResultSchema, {
      outcome: { case: "success", value: {} },
    }),
  } as const;
}

function successfulTransport(): FakeRpcTransport {
  return new FakeRpcTransport([setupSubscriptionRoute()]);
}

function observe(client: ApiClient) {
  const events: SetupEvent[] = [];
  const completions: string[] = [];
  const errors: Error[] = [];
  client.subscribeWorktreeSetup(setupID, {
    onEvent: (event) => {
      events.push(event);
    },
    onComplete: () => {
      completions.push("complete");
    },
    onError: (error) => {
      errors.push(error);
    },
  });
  return { events, completions, errors };
}
