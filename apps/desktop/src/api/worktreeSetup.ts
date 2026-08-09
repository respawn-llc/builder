import { z } from "zod";

import { ContractError, RpcError } from "./errors";
import { setupOperationIDSchema, type SetupOperationID } from "./setupOperationID";
import { workflowExecutionTargetSelectionSchema } from "./schemas/workflowExecutionTarget";
import type { WorkflowExecutionTargetSelection } from "./workflowExecutionTarget";
import {
  registeredWorktreeTopologySchema,
  retainedPreviousWorktreeSchema,
  type RegisteredWorktreeTopology,
  type RetainedPreviousWorktree,
} from "./worktreeTopology";
import type { RpcEventHandler } from "./transport";

export type WorktreeSetupPhase = "started" | "completed" | "not_required" | "failed";

export type WorktreeSetupFailureCause =
  | Readonly<{ kind: "process_exit"; exitCode: number; stdout: string | null; stderr: string | null }>
  | Readonly<{ kind: "timeout"; stdout: string | null; stderr: string | null }>
  | Readonly<{
      kind:
        | "target_preparation"
        | "interruption_persistence"
        | "canceled"
        | "controller_shutdown"
        | "operational";
    }>;

export type WorktreeSetupFailure = Readonly<{
  retryReadiness: "retry_ready" | "non_retryable";
  cause: WorktreeSetupFailureCause;
  diagnostic: string;
  scriptPath: string | null;
  executionTarget: WorkflowExecutionTargetSelection | null;
  retainedWorktree: RegisteredWorktreeTopology | null;
  retainedPreviousWorktree: RetainedPreviousWorktree | null;
}>;

const processExitCauseSchema = z
  .object({
    kind: z.literal("process_exit"),
    process_exit: z
      .object({
        exit_code: z
          .number()
          .int()
          .refine((value) => value !== 0),
        stdout: z.string().nullable(),
        stderr: z.string().nullable(),
      })
      .strict(),
  })
  .strict()
  .transform((value): WorktreeSetupFailureCause => ({
    kind: value.kind,
    exitCode: value.process_exit.exit_code,
    stdout: value.process_exit.stdout,
    stderr: value.process_exit.stderr,
  }));

const timeoutCauseSchema = z
  .object({
    kind: z.literal("timeout"),
    timeout: z
      .object({
        stdout: z.string().nullable(),
        stderr: z.string().nullable(),
      })
      .strict(),
  })
  .strict()
  .transform((value): WorktreeSetupFailureCause => ({
    kind: value.kind,
    stdout: value.timeout.stdout,
    stderr: value.timeout.stderr,
  }));

const markerCauseSchema = (
  kind:
    "target_preparation" | "interruption_persistence" | "canceled" | "controller_shutdown" | "operational",
) =>
  z
    .object({
      kind: z.literal(kind),
      [kind]: z.object({}).strict(),
    })
    .strict()
    .transform((): WorktreeSetupFailureCause => ({ kind }));

const failureCauseSchema = z.discriminatedUnion("kind", [
  processExitCauseSchema,
  timeoutCauseSchema,
  markerCauseSchema("target_preparation"),
  markerCauseSchema("interruption_persistence"),
  markerCauseSchema("canceled"),
  markerCauseSchema("controller_shutdown"),
  markerCauseSchema("operational"),
]);

const worktreeSetupFailureWireSchema = z
  .object({
    retry_readiness: z.enum(["retry_ready", "non_retryable"]),
    cause: failureCauseSchema,
    diagnostic: z.string().trim().min(1),
    script_path: z.string().trim().min(1).nullable(),
    execution_target: workflowExecutionTargetSelectionSchema.nullable(),
    retained_worktree: registeredWorktreeTopologySchema.nullable(),
    retained_previous_worktree: retainedPreviousWorktreeSchema.nullable(),
  })
  .strict()
  .superRefine((value, context) => {
    const retryable =
      value.cause.kind === "process_exit" ||
      value.cause.kind === "timeout" ||
      value.cause.kind === "target_preparation";
    const nonRetryable =
      value.cause.kind === "interruption_persistence" ||
      value.cause.kind === "canceled" ||
      value.cause.kind === "controller_shutdown";
    if (
      (retryable && value.retry_readiness !== "retry_ready") ||
      (nonRetryable && value.retry_readiness !== "non_retryable")
    ) {
      context.addIssue({
        code: "custom",
        message: "Failure retry readiness does not match its typed cause.",
        path: ["retry_readiness"],
      });
    }
    if (
      value.retry_readiness === "retry_ready" &&
      value.cause.kind !== "target_preparation" &&
      value.retained_worktree == null
    ) {
      context.addIssue({
        code: "custom",
        message: "Retry-ready setup-script failure requires a retained Worktree.",
        path: ["retained_worktree"],
      });
    }
  })
  .refine(
    (value) =>
      value.retry_readiness !== "retry_ready" ||
      value.cause.kind === "target_preparation" ||
      value.script_path !== null,
    {
      message: "Retry-ready setup-script failure requires its script path.",
      path: ["script_path"],
    },
  )
  .refine((value) => value.cause.kind !== "target_preparation" || value.script_path === null, {
    message: "Target-preparation failure cannot include a setup script.",
    path: ["script_path"],
  })
  .transform((value): WorktreeSetupFailure => ({
    retryReadiness: value.retry_readiness,
    cause: value.cause,
    diagnostic: value.diagnostic,
    scriptPath: value.script_path,
    executionTarget: value.execution_target,
    retainedWorktree: value.retained_worktree,
    retainedPreviousWorktree: value.retained_previous_worktree,
  }));

export class WorktreeSetupRetainedError extends RpcError {
  readonly worktree: RegisteredWorktreeTopology;
  readonly scriptPath: string;
  readonly diagnostic: string;
  readonly retainedPreviousWorktree: RetainedPreviousWorktree | null;

  constructor(
    rpcError: RpcError,
    facts: Readonly<{
      worktree: RegisteredWorktreeTopology;
      scriptPath: string;
      diagnostic: string;
      retainedPreviousWorktree: RetainedPreviousWorktree | null;
    }>,
  ) {
    super({
      code: rpcError.code,
      message: rpcError.message,
      method: rpcError.method,
      data: rpcError.data,
    });
    this.worktree = facts.worktree;
    this.scriptPath = facts.scriptPath;
    this.diagnostic = facts.diagnostic;
    this.retainedPreviousWorktree = facts.retainedPreviousWorktree;
  }
}

const worktreeSetupRetainedErrorDataSchema = z
  .object({
    type: z.literal("worktree_setup_retained"),
    worktree: registeredWorktreeTopologySchema,
    script_path: z.string().trim().min(1),
    diagnostic: z.string().trim().min(1),
    retained_previous_worktree: retainedPreviousWorktreeSchema.nullable(),
  })
  .strict();

export function decodeWorktreeSetupRetainedError(
  error: unknown,
): WorktreeSetupRetainedError | null {
  if (!(error instanceof RpcError) || error.code !== -32039) {
    return null;
  }
  const parsed = worktreeSetupRetainedErrorDataSchema.safeParse(error.data);
  if (!parsed.success) {
    return null;
  }
  return new WorktreeSetupRetainedError(error, {
    worktree: parsed.data.worktree,
    scriptPath: parsed.data.script_path,
    diagnostic: parsed.data.diagnostic,
    retainedPreviousWorktree: parsed.data.retained_previous_worktree,
  });
}

export type WorktreeSetupEvent =
  | Readonly<{
      setupOperationID: SetupOperationID;
      phase: "started";
      started: Readonly<{ sourceWorkspaceRoot: string; worktreeRoot: string; scriptPath: string }>;
    }>
  | Readonly<{
      setupOperationID: SetupOperationID;
      phase: "completed";
      completed: Readonly<{ retainedPreviousWorktree: RetainedPreviousWorktree | null }>;
    }>
  | Readonly<{
      setupOperationID: SetupOperationID;
      phase: "not_required";
      notRequired: Readonly<{
        reason: "no_target_preparation" | "no_configured_script";
        retainedPreviousWorktree: RetainedPreviousWorktree | null;
      }>;
    }>
  | Readonly<{
      setupOperationID: SetupOperationID;
      phase: "failed";
      failed: WorktreeSetupFailure;
    }>;

export type WorktreeSetupEventHandler = Readonly<{
  onOpen?(): void;
  onEvent(event: WorktreeSetupEvent): void;
  onComplete(code: number, message: string): void;
  onError(error: Error): void;
}>;

const setupEventWireSchema = z.discriminatedUnion("phase", [
  z
    .object({
      setup_operation_id: setupOperationIDSchema,
      phase: z.literal("started"),
      started: z
        .object({
          source_workspace_root: z.string().trim().min(1),
          worktree_root: z.string().trim().min(1),
          script_path: z.string().trim().min(1),
        })
        .strict(),
    })
    .strict()
    .transform((value): WorktreeSetupEvent => ({
      setupOperationID: value.setup_operation_id,
      phase: value.phase,
      started: {
        sourceWorkspaceRoot: value.started.source_workspace_root,
        worktreeRoot: value.started.worktree_root,
        scriptPath: value.started.script_path,
      },
    })),
  z
    .object({
      setup_operation_id: setupOperationIDSchema,
      phase: z.literal("completed"),
      completed: z
        .object({
          retained_previous_worktree: retainedPreviousWorktreeSchema.nullable(),
        })
        .strict(),
    })
    .strict()
    .transform((value): WorktreeSetupEvent => ({
      setupOperationID: value.setup_operation_id,
      phase: value.phase,
      completed: {
        retainedPreviousWorktree: value.completed.retained_previous_worktree,
      },
    })),
  z
    .object({
      setup_operation_id: setupOperationIDSchema,
      phase: z.literal("not_required"),
      not_required: z
        .object({
          reason: z.enum(["no_target_preparation", "no_configured_script"]),
          retained_previous_worktree: retainedPreviousWorktreeSchema.nullable(),
        })
        .strict(),
    })
    .strict()
    .transform((value): WorktreeSetupEvent => ({
      setupOperationID: value.setup_operation_id,
      phase: value.phase,
      notRequired: {
        reason: value.not_required.reason,
        retainedPreviousWorktree: value.not_required.retained_previous_worktree,
      },
    })),
  z
    .object({
      setup_operation_id: setupOperationIDSchema,
      phase: z.literal("failed"),
      failed: worktreeSetupFailureWireSchema,
    })
    .strict()
    .transform((value): WorktreeSetupEvent => ({
      setupOperationID: value.setup_operation_id,
      phase: value.phase,
      failed: value.failed,
    })),
]);

export const worktreeSetupEventParamsSchema = z
  .object({
    event: setupEventWireSchema,
  })
  .strict();

export function worktreeSetupRpcHandler(handler: WorktreeSetupEventHandler): RpcEventHandler {
  return {
    ...(handler.onOpen !== undefined ? { onOpen: handler.onOpen } : {}),
    onComplete: handler.onComplete,
    onError: handler.onError,
    onEvent(method, params) {
      if (method !== "worktree.setup") {
        return;
      }
      try {
        handler.onEvent(worktreeSetupEventParamsSchema.parse(params).event);
      } catch {
        handler.onError(new ContractError("worktree.setup event did not match GUI contract."));
      }
    },
  };
}
