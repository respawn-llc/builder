import { z } from "zod";

import { ContractError, RpcError } from "../errors";
import { rpcErrorCodes } from "../rpcErrorCodes";
import { parseSetupOperationID, type SetupOperationID } from "../setupOperationID";
import { nonBlankString } from "./common";
import { workflowExecutionTargetSelectionSchema } from "./workflowExecutionTarget";

const nullableNonBlank = nonBlankString.nullable();
const workflowGitFactsSchema = z
  .object({
    canonical_root: nonBlankString,
    head_object: nonBlankString,
    branch_ref: nullableNonBlank,
    branch_name: nullableNonBlank,
    detached: z.boolean(),
    bare: z.boolean(),
    locked_reason: nullableNonBlank,
    prunable_reason: nullableNonBlank,
    is_main_worktree: z.boolean(),
    path_available: z.boolean(),
  })
  .strict();
const workflowKentFactsSchema = z
  .object({
    worktree_id: nonBlankString,
    canonical_root: nonBlankString,
    display_name: nonBlankString,
    managed: z.boolean(),
    created_branch: z.boolean(),
    origin_session_id: nullableNonBlank,
  })
  .strict();
const workflowRegisteredWorktreeSchema = z
  .object({
    variant: z.literal("registered"),
    registered: z.object({ git: workflowGitFactsSchema, kent: workflowKentFactsSchema }).strict(),
  })
  .strict()
  .superRefine((value, context) => {
    if (value.registered.git.canonical_root !== value.registered.kent.canonical_root) {
      context.addIssue({ code: "custom", message: "Registered Worktree roots must match." });
    }
  })
  .transform((value) => ({
    kent: {
      canonicalRoot: value.registered.kent.canonical_root,
      worktreeID: value.registered.kent.worktree_id,
    },
  }));
export type WorkflowRegisteredWorktree = z.output<typeof workflowRegisteredWorktreeSchema>;
export const retainedPreviousWorktreeSchema = z
  .object({ worktree: workflowRegisteredWorktreeSchema })
  .strict();
export type RetainedPreviousWorktree = z.output<typeof retainedPreviousWorktreeSchema>;

const recoveryWorktreeSchema = z
  .object({ worktree_id: nonBlankString, root: nonBlankString })
  .strict()
  .transform((value) => ({ worktreeID: value.worktree_id, root: value.root }));
const setupOperationIDSchema = z.string().transform((value, context): SetupOperationID => {
  try {
    return parseSetupOperationID(value);
  } catch {
    context.addIssue({ code: "custom", message: "Setup operation id must be a UUID v4." });
    return z.NEVER;
  }
});
const taskSetupRecoverySchema = z
  .object({
    setup_operation_id: setupOperationIDSchema,
    cause: z.enum(["process_exit", "timeout", "target_preparation", "operational"]),
    diagnostic: nonBlankString,
    script_path: nullableNonBlank,
    setup_requirement: z.enum(["required", "already_completed"]),
    execution_target: workflowExecutionTargetSelectionSchema,
    retained_worktree: recoveryWorktreeSchema.nullable(),
    retained_previous_worktree: recoveryWorktreeSchema.nullable(),
  })
  .strict()
  .superRefine((value, context) => {
    const scriptFailure = value.cause !== "target_preparation";
    if (scriptFailure && (value.script_path === null || value.retained_worktree === null)) {
      context.addIssue({ code: "custom", message: "Setup failure requires script and Worktree facts." });
    }
    if (!scriptFailure && value.script_path !== null) {
      context.addIssue({ code: "custom", message: "Target preparation cannot include a setup script." });
    }
  })
  .transform((value) => ({
    setupOperationID: value.setup_operation_id,
    cause: value.cause,
    diagnostic: value.diagnostic,
    scriptPath: value.script_path,
    executionTarget: value.execution_target,
    retainedWorktree: value.retained_worktree,
    retainedPreviousWorktree: value.retained_previous_worktree,
  }));
export type TaskSetupRecovery = z.output<typeof taskSetupRecoverySchema>;
const taskSetupRecoveryEnvelopeSchema = z
  .object({ setup_recovery: taskSetupRecoverySchema.optional() })
  .loose();

export function parseTaskSetupRecoveryDetail(detailJSON: string | null): TaskSetupRecovery | null {
  if (detailJSON === null) return null;
  let detail: unknown;
  try {
    detail = JSON.parse(detailJSON);
  } catch {
    throw new ContractError("Task setup recovery detail was not valid JSON.");
  }
  const parsed = taskSetupRecoveryEnvelopeSchema.safeParse(detail);
  if (!parsed.success) {
    throw new ContractError(
      "Task setup recovery detail did not match GUI contract.",
      parsed.error.issues.map((issue) => ({ code: issue.code, path: issue.path.map(String) })),
    );
  }
  return parsed.data.setup_recovery ?? null;
}

export class WorktreeSetupRetainedError extends RpcError {
  readonly worktree: WorkflowRegisteredWorktree;
  readonly scriptPath: string;
  readonly diagnostic: string;
  readonly retainedPreviousWorktree: RetainedPreviousWorktree | null;

  constructor(
    rpcError: RpcError,
    facts: Readonly<{
      worktree: WorkflowRegisteredWorktree;
      scriptPath: string;
      diagnostic: string;
      retainedPreviousWorktree: RetainedPreviousWorktree | null;
    }>,
  ) {
    super(rpcError);
    this.name = "WorktreeSetupRetainedError";
    this.worktree = facts.worktree;
    this.scriptPath = facts.scriptPath;
    this.diagnostic = facts.diagnostic;
    this.retainedPreviousWorktree = facts.retainedPreviousWorktree;
  }
}
const retainedErrorSchema = z
  .object({
    type: z.literal("worktree_setup_retained"),
    worktree: workflowRegisteredWorktreeSchema,
    script_path: nonBlankString,
    diagnostic: nonBlankString,
    retained_previous_worktree: retainedPreviousWorktreeSchema.nullable(),
  })
  .strict();

export function decodeWorktreeSetupRetainedError(error: unknown): WorktreeSetupRetainedError | null {
  if (!(error instanceof RpcError) || error.code !== rpcErrorCodes.workflowWorktreeSetupRetained) {
    return null;
  }
  const parsed = retainedErrorSchema.safeParse(error.data);
  return parsed.success
    ? new WorktreeSetupRetainedError(error, {
        worktree: parsed.data.worktree,
        scriptPath: parsed.data.script_path,
        diagnostic: parsed.data.diagnostic,
        retainedPreviousWorktree: parsed.data.retained_previous_worktree,
      })
    : null;
}
