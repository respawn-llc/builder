import { FakeRpcTransport } from "@/test-support/api";
import { create } from "@app/server-api-contract";
import { AuthRequiredDetailsSchema } from "@app/server-api-contract/gen/kent/api/auth/auth_pb";
import {
  SetupCompletionSchema,
  SetupEventSchema,
  SetupFailureCauseSchema,
  SetupRetryReadiness,
  SetupService,
  SetupStartResultSchema,
} from "@app/server-api-contract/gen/kent/api/worktree/worktree_pb";

import { ApiClient } from "./client";
import { ContractError, RpcError, errorMessage } from "./errors";
import { parseTaskSetupRecoveryDetail } from "./schemas/workflowWorktree";
import { parseSetupOperationID } from "./setupOperationID";
import type { WorktreeSetupEvent } from "./worktreeSetup";
import { subscribeBinaryWorktreeSetup } from "./worktreeSetupBinary";

const setupIDWire = "123e4567-e89b-42d3-a456-426614174000";
const setupID = parseSetupOperationID(setupIDWire);
const binaryRegisteredWorktree = {
  git: {
    canonicalRoot: "/worktree",
    headObject: "abc",
    detached: true,
    bare: false,
    isMain: false,
    pathAvailable: true,
  },
  kent: {
    worktreeId: "worktree-1",
    canonicalRoot: "/worktree",
    displayName: "worktree",
    managed: true,
    createdBranch: false,
  },
} as const;

function successfulTransport(): FakeRpcTransport {
  return new FakeRpcTransport([
    {
      subscriptionDescriptor: SetupService.method.subscribe,
      startResult: create(SetupStartResultSchema, {
        outcome: { case: "success", value: {} },
      }),
    },
  ]);
}

describe("worktree setup API", () => {
  it("composes the descriptor subscription and suppresses late callbacks", () => {
    const transport = successfulTransport();
    const client = new ApiClient(transport);
    const events: WorktreeSetupEvent[] = [];
    const completions: string[] = [];
    const errors: Error[] = [];

    client.subscribeWorktreeSetup(setupID, {
      onEvent: (event) => events.push(event),
      onComplete: () => completions.push("complete"),
      onError: (error) => errors.push(error),
    });
    transport.openDescriptor(SetupService.method.subscribe);
    transport.emitDescriptor(
      SetupService.method.subscribe,
      SetupService.method.event,
      create(SetupEventSchema, {
        setupOperationId: setupIDWire,
        phase: {
          case: "started",
          value: {
            sourceWorkspaceRoot: "/source",
            worktreeRoot: "/worktree",
            scriptPath: "/setup.sh",
          },
        },
      }),
    );
    transport.emitDescriptor(
      SetupService.method.subscribe,
      SetupService.method.event,
      create(SetupEventSchema, {
        setupOperationId: setupIDWire,
        phase: { case: "completed", value: {} },
      }),
    );
    transport.completeDescriptor(
      SetupService.method.subscribe,
      SetupService.method.complete,
      create(SetupCompletionSchema),
    );
    transport.failDescriptor(SetupService.method.subscribe, new Error("late"));

    expect(events.map(({ phase }) => phase)).toEqual(["started", "completed"]);
    expect(completions).toEqual(["complete"]);
    expect(errors).toEqual([]);
    expect(transport.subscriptions).toEqual([]);
    expect(transport.descriptorSubscriptionStarts).toMatchObject([
      {
        descriptor: SetupService.method.subscribe,
        request: { setupOperationId: setupIDWire },
      },
    ]);
  });

  it("preserves non-zero completion through the error path", () => {
    const transport = successfulTransport();
    const observed: string[] = [];
    subscribeBinaryWorktreeSetup(transport, setupID, {
      onEvent() {
        return;
      },
      onComplete() {
        observed.push("complete");
      },
      onError(error) {
        observed.push(error.message);
      },
    });
    transport.openDescriptor(SetupService.method.subscribe);
    transport.completeDescriptor(
      SetupService.method.subscribe,
      SetupService.method.complete,
      create(SetupCompletionSchema, { code: 409, diagnostic: "stream gap" }),
    );

    expect(observed).toHaveLength(1);
    expect(observed[0]).toContain("stream gap");
  });

  it("projects generated setup start failures", () => {
    const transport = new FakeRpcTransport([
      {
        subscriptionDescriptor: SetupService.method.subscribe,
        startResult: create(SetupStartResultSchema, {
          outcome: {
            case: "error",
            value: {
              code: "auth_required",
              detail: {
                case: "authRequired",
                value: create(AuthRequiredDetailsSchema),
              },
            },
          },
        }),
      },
    ]);
    const errors: Error[] = [];
    subscribeBinaryWorktreeSetup(transport, setupID, {
      onEvent() {
        return;
      },
      onComplete() {
        return;
      },
      onError(error) {
        errors.push(error);
      },
    });
    transport.openDescriptor(SetupService.method.subscribe);

    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(RpcError);
    expect(errorMessage(errors[0])).not.toContain("failed with code auth_required");
  });

  it("projects every generated setup failure cause", () => {
    const cases = [
      {
        expected: "process_exit",
        retryReadiness: SetupRetryReadiness.WORKTREE_SETUP_RETRY_READY,
        cause: create(SetupFailureCauseSchema, {
          cause: {
            case: "processExit" as const,
            value: { exitCode: 7, stdout: "output", stderr: "failure" },
          },
        }),
        scriptPath: "/setup.sh",
        retainedWorktree: binaryRegisteredWorktree,
      },
      {
        expected: "timeout",
        retryReadiness: SetupRetryReadiness.WORKTREE_SETUP_RETRY_READY,
        cause: create(SetupFailureCauseSchema, {
          cause: { case: "timeout" as const, value: {} },
        }),
        scriptPath: "/setup.sh",
        retainedWorktree: binaryRegisteredWorktree,
      },
      {
        expected: "target_preparation",
        retryReadiness: SetupRetryReadiness.WORKTREE_SETUP_RETRY_READY,
        cause: create(SetupFailureCauseSchema, {
          cause: { case: "targetPreparation" as const, value: {} },
        }),
      },
      {
        expected: "interruption_persistence",
        retryReadiness: SetupRetryReadiness.WORKTREE_SETUP_NON_RETRYABLE,
        cause: create(SetupFailureCauseSchema, {
          cause: { case: "interruptionPersistence" as const, value: {} },
        }),
      },
      {
        expected: "canceled",
        retryReadiness: SetupRetryReadiness.WORKTREE_SETUP_NON_RETRYABLE,
        cause: create(SetupFailureCauseSchema, {
          cause: { case: "canceled" as const, value: {} },
        }),
      },
      {
        expected: "controller_shutdown",
        retryReadiness: SetupRetryReadiness.WORKTREE_SETUP_NON_RETRYABLE,
        cause: create(SetupFailureCauseSchema, {
          cause: { case: "controllerShutdown" as const, value: {} },
        }),
      },
      {
        expected: "operational",
        retryReadiness: SetupRetryReadiness.WORKTREE_SETUP_NON_RETRYABLE,
        cause: create(SetupFailureCauseSchema, {
          cause: { case: "operational" as const, value: {} },
        }),
      },
    ];

    for (const setupFailure of cases) {
      const transport = successfulTransport();
      const observed: WorktreeSetupEvent[] = [];
      subscribeBinaryWorktreeSetup(transport, setupID, {
        onEvent: (value) => observed.push(value),
        onComplete() {
          return;
        },
        onError(error) {
          throw error;
        },
      });
      transport.openDescriptor(SetupService.method.subscribe);
      transport.emitDescriptor(
        SetupService.method.subscribe,
        SetupService.method.event,
        create(SetupEventSchema, {
          setupOperationId: setupIDWire,
          phase: {
            case: "failed",
            value: {
              retryReadiness: setupFailure.retryReadiness,
              cause: setupFailure.cause,
              diagnostic: "failed",
              ...(setupFailure.scriptPath === undefined ? {} : { scriptPath: setupFailure.scriptPath }),
              ...(setupFailure.retainedWorktree === undefined
                ? {}
                : { retainedWorktree: setupFailure.retainedWorktree }),
            },
          },
        }),
      );

      expect(observed).toMatchObject([
        { phase: "failed", failed: { cause: { kind: setupFailure.expected } } },
      ]);
    }
  });

  it("decodes Workflow-owned Task setup recovery", () => {
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
      executionTarget: { mode: "head", customRef: null },
      retainedWorktree: null,
    });
    expect(parseTaskSetupRecoveryDetail(JSON.stringify({ code: "user_interrupt" }))).toBeNull();
    expect(() => parseTaskSetupRecoveryDetail('{"setup_recovery":{}}')).toThrow(ContractError);
  });
});
