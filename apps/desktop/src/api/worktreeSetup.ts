import { z } from "zod";

import { ContractError } from "./errors";
import { setupOperationIDSchema, type SetupOperationID } from "./setupOperationID";
import {
  worktreeSetupFailureWireSchema,
  type WorktreeSetupFailure,
} from "./worktreeSetupFailure";
import {
  retainedPreviousWorktreeSchema,
  type RetainedPreviousWorktree,
} from "./worktreeTopology";
import type { RpcEventHandler } from "./transport";

export type WorktreeSetupPhase = "started" | "completed" | "not_required" | "failed";

export type { WorktreeSetupFailure, WorktreeSetupFailureCause } from "./worktreeSetupFailure";

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
