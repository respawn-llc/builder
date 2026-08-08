import { z } from "zod";

import { ContractError } from "./errors";
import { setupOperationIDSchema, type SetupOperationID } from "./setupOperationID";
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
      failed: Readonly<{
        retryReadiness: "retry_ready" | "non_retryable";
        cause: WorktreeSetupFailureCause;
        diagnostic: string;
        retainedWorktree: RegisteredWorktreeTopology | null;
        retainedPreviousWorktree: RetainedPreviousWorktree | null;
      }>;
    }>;

export type WorktreeSetupEventHandler = Readonly<{
  onOpen?(): void;
  onEvent(event: WorktreeSetupEvent): void;
  onComplete(code: number, message: string): void;
  onError(error: Error): void;
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
        stdout: z.string().nullable().optional(),
        stderr: z.string().nullable().optional(),
      })
      .strict(),
  })
  .strict()
  .transform((value): WorktreeSetupFailureCause => ({
    kind: value.kind,
    exitCode: value.process_exit.exit_code,
    stdout: value.process_exit.stdout ?? null,
    stderr: value.process_exit.stderr ?? null,
  }));

const timeoutCauseSchema = z
  .object({
    kind: z.literal("timeout"),
    timeout: z
      .object({
        stdout: z.string().nullable().optional(),
        stderr: z.string().nullable().optional(),
      })
      .strict(),
  })
  .strict()
  .transform((value): WorktreeSetupFailureCause => ({
    kind: value.kind,
    stdout: value.timeout.stdout ?? null,
    stderr: value.timeout.stderr ?? null,
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
          retained_previous_worktree: retainedPreviousWorktreeSchema.nullable().optional(),
        })
        .strict(),
    })
    .strict()
    .transform((value): WorktreeSetupEvent => ({
      setupOperationID: value.setup_operation_id,
      phase: value.phase,
      completed: {
        retainedPreviousWorktree: value.completed.retained_previous_worktree ?? null,
      },
    })),
  z
    .object({
      setup_operation_id: setupOperationIDSchema,
      phase: z.literal("not_required"),
      not_required: z
        .object({
          reason: z.enum(["no_target_preparation", "no_configured_script"]),
          retained_previous_worktree: retainedPreviousWorktreeSchema.nullable().optional(),
        })
        .strict(),
    })
    .strict()
    .transform((value): WorktreeSetupEvent => ({
      setupOperationID: value.setup_operation_id,
      phase: value.phase,
      notRequired: {
        reason: value.not_required.reason,
        retainedPreviousWorktree: value.not_required.retained_previous_worktree ?? null,
      },
    })),
  z
    .object({
      setup_operation_id: setupOperationIDSchema,
      phase: z.literal("failed"),
      failed: z
        .object({
          retry_readiness: z.enum(["retry_ready", "non_retryable"]),
          cause: failureCauseSchema,
          diagnostic: z.string().trim().min(1),
          retained_worktree: registeredWorktreeTopologySchema.nullable().optional(),
          retained_previous_worktree: retainedPreviousWorktreeSchema.nullable().optional(),
        })
        .strict(),
    })
    .strict()
    .superRefine((value, context) => {
      const retryable =
        value.failed.cause.kind === "process_exit" ||
        value.failed.cause.kind === "timeout" ||
        value.failed.cause.kind === "target_preparation";
      const nonRetryable =
        value.failed.cause.kind === "interruption_persistence" ||
        value.failed.cause.kind === "canceled" ||
        value.failed.cause.kind === "controller_shutdown";
      if (
        (retryable && value.failed.retry_readiness !== "retry_ready") ||
        (nonRetryable && value.failed.retry_readiness !== "non_retryable")
      ) {
        context.addIssue({
          code: "custom",
          message: "Failure retry readiness does not match its typed cause.",
          path: ["failed", "retry_readiness"],
        });
      }
    })
    .transform((value): WorktreeSetupEvent => ({
      setupOperationID: value.setup_operation_id,
      phase: value.phase,
      failed: {
        retryReadiness: value.failed.retry_readiness,
        cause: value.failed.cause,
        diagnostic: value.failed.diagnostic,
        retainedWorktree: value.failed.retained_worktree ?? null,
        retainedPreviousWorktree: value.failed.retained_previous_worktree ?? null,
      },
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
