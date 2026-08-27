import { create } from "@app/server-api-contract";
import {
  SetupExecutionTargetMode,
  SetupNotRequiredReason,
  SetupRetryReadiness,
  SetupService,
  SetupCompletionSchema,
  SetupEventSchema,
  type SetupEvent,
  type SetupFailed,
  type SetupStartResult,
} from "@app/server-api-contract/gen/kent/api/worktree/worktree_pb";

import { ContractError } from "./errors";
import type { SetupOperationID } from "./setupOperationID";
import type {
  WorktreeSetupEvent,
  WorktreeSetupEventHandler,
  WorktreeSetupFailure,
  WorktreeSetupFailureCause,
} from "./worktreeSetup";
import type { DescriptorRpcTransport, RpcSubscription } from "./transport";
import type { WorkflowExecutionTargetSelection } from "./workflowExecutionTarget";
import { projectWorktreeFailure } from "./worktreeFailure";
import { projectRegisteredWorktree, projectRetainedPreviousWorktree } from "./worktreeProtoProjection";

export type BinaryWorktreeSetupEventHandler = Omit<WorktreeSetupEventHandler, "onComplete"> &
  Readonly<{ onComplete(): void }>;

export function subscribeBinaryWorktreeSetup(
  transport: DescriptorRpcTransport,
  setupOperationID: SetupOperationID,
  handler: BinaryWorktreeSetupEventHandler,
): RpcSubscription {
  let subscription: RpcSubscription | null = null;
  let finished = false;
  const finish = (notify?: () => void) => {
    if (finished) return;
    finished = true;
    subscription?.close();
    notify?.();
  };
  const method = SetupService.method.subscribe;
  subscription = transport.subscribeDescriptor(
    method,
    create(method.input, { setupOperationId: setupOperationID.toJSONValue() }),
    {
      eventDescriptor: SetupEventSchema,
      completionDescriptor: SetupCompletionSchema,
      projectStart(result) {
        requireSetupStart(method, result);
      },
      projectEvent: projectSetupEvent,
      classifyCompletion(completion) {
        return completion.code === undefined
          ? { kind: "normal" }
          : {
              kind: "error",
              code: completion.code,
              diagnostic: required(completion.diagnostic),
            };
      },
    },
    {
      ...(handler.onOpen === undefined
        ? {}
        : {
            onOpen() {
              if (!finished) handler.onOpen?.();
            },
          }),
      onEvent(event) {
        if (finished) return;
        handler.onEvent(event);
        if (event.phase !== "started") finish(handler.onComplete);
      },
      onTerminal(outcome) {
        if (outcome.kind === "normal") finish(handler.onComplete);
      },
      onError(error) {
        finish(() => {
          handler.onError(error);
        });
      },
    },
  );
  return { close: finish };
}

function requireSetupStart(method: typeof SetupService.method.subscribe, result: SetupStartResult): void {
  switch (result.outcome.case) {
    case "success":
      return;
    case "error":
      throw projectWorktreeFailure(method, result.outcome.value);
    case undefined:
      throw new ContractError("Worktree setup subscription returned no outcome.");
  }
}

function projectSetupEvent(value: SetupEvent): WorktreeSetupEvent {
  const setupOperationID = parseSetupID(value.setupOperationId);
  switch (value.phase.case) {
    case "started":
      return {
        setupOperationID,
        phase: "started",
        started: {
          sourceWorkspaceRoot: value.phase.value.sourceWorkspaceRoot,
          worktreeRoot: value.phase.value.worktreeRoot,
          scriptPath: value.phase.value.scriptPath,
        },
      };
    case "completed":
      return {
        setupOperationID,
        phase: "completed",
        completed: {
          retainedPreviousWorktree: projectRetainedPreviousWorktree(
            value.phase.value.retainedPreviousWorktree,
          ),
        },
      };
    case "notRequired":
      return {
        setupOperationID,
        phase: "not_required",
        notRequired: {
          reason: projectNotRequiredReason(value.phase.value.reason),
          retainedPreviousWorktree: projectRetainedPreviousWorktree(
            value.phase.value.retainedPreviousWorktree,
          ),
        },
      };
    case "failed":
      return {
        setupOperationID,
        phase: "failed",
        failed: projectSetupFailure(value.phase.value),
      };
    case undefined:
      throw new ContractError("Worktree setup event has no phase.");
  }
}

function projectSetupFailure(value: SetupFailed): WorktreeSetupFailure {
  return {
    retryReadiness: projectRetryReadiness(value.retryReadiness),
    cause: projectFailureCause(required(value.cause)),
    diagnostic: value.diagnostic,
    scriptPath: value.scriptPath ?? null,
    executionTarget:
      value.executionTarget === undefined ? null : projectExecutionTarget(value.executionTarget),
    retainedWorktree:
      value.retainedWorktree === undefined ? null : projectRegisteredWorktree(value.retainedWorktree),
    retainedPreviousWorktree: projectRetainedPreviousWorktree(value.retainedPreviousWorktree),
  };
}

function projectFailureCause(value: NonNullable<SetupFailed["cause"]>): WorktreeSetupFailureCause {
  switch (value.cause.case) {
    case "processExit":
      return projectProcessExit(value.cause.value);
    case "timeout":
      return projectTimeout(value.cause.value);
    case "targetPreparation":
      return { kind: "target_preparation" };
    case "interruptionPersistence":
      return { kind: "interruption_persistence" };
    case "canceled":
      return { kind: "canceled" };
    case "controllerShutdown":
      return { kind: "controller_shutdown" };
    case "operational":
      return { kind: "operational" };
    case undefined:
      throw new ContractError("Worktree setup failure has no cause.");
  }
}

function projectProcessExit(
  value: Extract<NonNullable<SetupFailed["cause"]>["cause"], { case: "processExit" }>["value"],
): WorktreeSetupFailureCause {
  return {
    kind: "process_exit",
    exitCode: value.exitCode,
    stdout: value.stdout ?? null,
    stderr: value.stderr ?? null,
  };
}

function projectTimeout(
  value: Extract<NonNullable<SetupFailed["cause"]>["cause"], { case: "timeout" }>["value"],
): WorktreeSetupFailureCause {
  return {
    kind: "timeout",
    stdout: value.stdout ?? null,
    stderr: value.stderr ?? null,
  };
}

function projectNotRequiredReason(
  value: SetupNotRequiredReason,
): "no_target_preparation" | "no_configured_script" {
  switch (value) {
    case SetupNotRequiredReason.WORKTREE_SETUP_NOT_REQUIRED_REASON_NO_TARGET_PREPARATION:
      return "no_target_preparation";
    case SetupNotRequiredReason.WORKTREE_SETUP_NOT_REQUIRED_REASON_NO_CONFIGURED_SCRIPT:
      return "no_configured_script";
    case SetupNotRequiredReason.WORKTREE_SETUP_NOT_REQUIRED_REASON_UNSPECIFIED:
      throw new ContractError("Worktree setup not-required reason is unspecified.");
  }
}

function projectRetryReadiness(value: SetupRetryReadiness): "retry_ready" | "non_retryable" {
  switch (value) {
    case SetupRetryReadiness.WORKTREE_SETUP_RETRY_READY:
      return "retry_ready";
    case SetupRetryReadiness.WORKTREE_SETUP_NON_RETRYABLE:
      return "non_retryable";
    case SetupRetryReadiness.WORKTREE_SETUP_RETRY_READINESS_UNSPECIFIED:
      throw new ContractError("Worktree setup retry readiness is unspecified.");
  }
}

function projectExecutionTarget(
  value: NonNullable<SetupFailed["executionTarget"]>,
): WorkflowExecutionTargetSelection {
  switch (value.mode) {
    case SetupExecutionTargetMode.WORKTREE_SETUP_EXECUTION_TARGET_MODE_NONE:
      return { mode: "none", customRef: null };
    case SetupExecutionTargetMode.WORKTREE_SETUP_EXECUTION_TARGET_MODE_HEAD:
      return { mode: "head", customRef: null };
    case SetupExecutionTargetMode.WORKTREE_SETUP_EXECUTION_TARGET_MODE_DEFAULT_BRANCH:
      return { mode: "default_branch", customRef: null };
    case SetupExecutionTargetMode.WORKTREE_SETUP_EXECUTION_TARGET_MODE_CUSTOM_REF:
      return { mode: "custom_ref", customRef: required(value.customRef) };
    case SetupExecutionTargetMode.WORKTREE_SETUP_EXECUTION_TARGET_MODE_ASK_ON_FIRST_EXECUTION:
    case SetupExecutionTargetMode.WORKTREE_SETUP_EXECUTION_TARGET_MODE_UNSPECIFIED:
      throw new ContractError("Worktree setup execution target is invalid.");
  }
}

function parseSetupID(value: string): SetupOperationID {
  return importSetupOperationID(value);
}

import { parseSetupOperationID as importSetupOperationID } from "./setupOperationID";

function required<Value>(value: Value | undefined): Value {
  if (value === undefined) throw new ContractError("Required Worktree setup fact is missing.");
  return value;
}
