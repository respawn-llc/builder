import { z } from "zod";

import { ContractError } from "./errors";
import { parseSetupOperationID, type SetupOperationID } from "./setupOperationID";
import type { RpcEventHandler } from "./transport";

export type WorktreeSetupPhase = "started" | "completed" | "failed";

export type WorktreeSetupEvent = Readonly<{
  setupOperationID: SetupOperationID;
  sourceWorkspaceRoot: string;
  worktreeRoot: string;
  scriptPath: string;
  phase: WorktreeSetupPhase;
  timeout: boolean;
  canceled: boolean;
  exitCode?: number;
  stdout: string;
  stderr: string;
  error: string;
}>;

export type WorktreeSetupEventHandler = Readonly<{
  onOpen?(): void;
  onEvent(event: WorktreeSetupEvent): void;
  onComplete(code: number, message: string): void;
  onError(error: Error): void;
}>;

function setupOperationID(value: string, ctx: z.RefinementCtx): SetupOperationID {
  try {
    return parseSetupOperationID(value);
  } catch {
    ctx.addIssue({ code: "custom", message: "Expected setup operation id UUID v4." });
    return z.NEVER;
  }
}

const setupEventWireSchema = z.object({
  setup_operation_id: z.string().transform(setupOperationID),
  source_workspace_root: z.string().min(1),
  worktree_root: z.string().min(1),
  script_path: z.string().min(1),
  phase: z.enum(["started", "completed", "failed"]),
  timeout: z.boolean().optional().default(false),
  canceled: z.boolean().optional().default(false),
  exit_code: z.number().int().optional(),
  stdout: z.string().optional().default(""),
  stderr: z.string().optional().default(""),
  error: z.string().optional().default(""),
});

export const worktreeSetupEventParamsSchema = z
  .object({
    event: setupEventWireSchema,
  })
  .transform(({ event }): { event: WorktreeSetupEvent } => {
    const parsedEvent: WorktreeSetupEvent = {
      setupOperationID: event.setup_operation_id,
      sourceWorkspaceRoot: event.source_workspace_root,
      worktreeRoot: event.worktree_root,
      scriptPath: event.script_path,
      phase: event.phase,
      timeout: event.timeout,
      canceled: event.canceled,
      stdout: event.stdout,
      stderr: event.stderr,
      error: event.error,
    };
    return {
      event: {
        ...parsedEvent,
        ...(event.exit_code !== undefined ? { exitCode: event.exit_code } : {}),
      },
    };
  });

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
