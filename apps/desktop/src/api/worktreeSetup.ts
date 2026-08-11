import { z } from "zod";

import { ContractError, RpcError } from "./errors";
import { parseSetupOperationID, type SetupOperationID } from "./setupOperationID";
import { workflowExecutionTargetSelectionSchema } from "./schemas/workflowExecutionTarget";
import type { RpcEventHandler } from "./transport";
import type { WorkflowExecutionTargetSelection } from "./workflowExecutionTarget";

const nonBlank = z.string().trim().min(1);
const nullableNonBlank = nonBlank.nullable();
const setupOperationIDSchema = z.string().transform((value, context): SetupOperationID => {
  try {
    return parseSetupOperationID(value);
  } catch {
    context.addIssue({ code: "custom", message: "Setup operation id must be a UUID v4." });
    return z.NEVER;
  }
});

const gitFactsSchema = z
  .object({
    canonical_root: nonBlank,
    head_object: nonBlank,
    branch_ref: nullableNonBlank,
    branch_name: nullableNonBlank,
    detached: z.boolean(),
    bare: z.boolean(),
    locked_reason: nullableNonBlank,
    prunable_reason: nullableNonBlank,
    is_main: z.boolean(),
    path_available: z.boolean(),
  })
  .strict()
  .transform((value) => ({ canonicalRoot: value.canonical_root }));
const kentFactsSchema = z
  .object({
    worktree_id: nonBlank,
    canonical_root: nonBlank,
    display_name: nonBlank,
    managed: z.boolean(),
    created_branch: z.boolean(),
    origin_session_id: nullableNonBlank,
  })
  .strict()
  .transform((value) => ({ worktreeID: value.worktree_id, canonicalRoot: value.canonical_root }));

export type RegisteredWorktreeTopology = Readonly<{
  variant: "registered";
  registered: Readonly<{ git: z.output<typeof gitFactsSchema>; kent: z.output<typeof kentFactsSchema> }>;
}>;
export type RetainedPreviousWorktree = Readonly<{ worktree: RegisteredWorktreeTopology }>;

const registeredWorktreeTopologySchema: z.ZodType<RegisteredWorktreeTopology> = z
  .object({
    variant: z.literal("registered"),
    registered: z
      .object({ git: gitFactsSchema, kent: kentFactsSchema })
      .strict()
      .refine((value) => value.git.canonicalRoot === value.kent.canonicalRoot),
  })
  .strict();
export const retainedPreviousWorktreeSchema: z.ZodType<RetainedPreviousWorktree> =
  z.object({ worktree: registeredWorktreeTopologySchema }).strict();

export type TaskSetupRecovery = Readonly<{
  setupOperationID: SetupOperationID; cause: "process_exit" | "timeout" | "target_preparation" | "operational"; diagnostic: string; scriptPath: string | null;
  executionTarget: WorkflowExecutionTargetSelection; retainedWorktree: Readonly<{ worktreeID: string; root: string }> | null; retainedPreviousWorktree: Readonly<{ worktreeID: string; root: string }> | null;
}>;

const recoveryWorktreeSchema = z.object({ worktree_id: nonBlank, root: nonBlank }).strict()
  .transform((value) => ({ worktreeID: value.worktree_id, root: value.root }));
const taskSetupRecoverySchema: z.ZodType<TaskSetupRecovery> = z
  .object({
    setup_operation_id: setupOperationIDSchema,
    cause: z.enum(["process_exit", "timeout", "target_preparation", "operational"]),
    diagnostic: nonBlank,
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
const taskSetupRecoveryEnvelopeSchema = z.object({ setup_recovery: taskSetupRecoverySchema.optional() }).loose();

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

export type WorktreeSetupFailureCause = Readonly<{ kind:
  "process_exit" | "timeout" | "target_preparation" | "interruption_persistence" |
  "canceled" | "controller_shutdown" | "operational" }>;
export type WorktreeSetupFailure = Readonly<{
  retryReadiness: "retry_ready" | "non_retryable"; cause: WorktreeSetupFailureCause; diagnostic: string; scriptPath: string | null;
  executionTarget: WorkflowExecutionTargetSelection | null; retainedWorktree: RegisteredWorktreeTopology | null; retainedPreviousWorktree: RetainedPreviousWorktree | null;
}>;

const outputSchema = z.object({ stdout: z.string().nullable(), stderr: z.string().nullable() }).strict();
const failureKindSchema = z.enum(["process_exit", "timeout", "target_preparation", "interruption_persistence", "canceled", "controller_shutdown", "operational"]);
const markerKinds = ["target_preparation", "interruption_persistence", "canceled", "controller_shutdown", "operational"] as const;
const failureCauseSchema = z
  .union([
    z.object({
      kind: z.literal("process_exit"),
      process_exit: outputSchema.extend({ exit_code: z.number().int().refine((value) => value !== 0) }),
    }).strict(),
    z.object({ kind: z.literal("timeout"), timeout: outputSchema }).strict(),
    ...markerKinds.map((kind) =>
      z.object({ kind: z.literal(kind), [kind]: z.object({}).strict() }).strict(),
    ),
  ])
  .transform((value): WorktreeSetupFailureCause => ({ kind: failureKindSchema.parse(value.kind) }));

const worktreeSetupFailureWireSchema: z.ZodType<WorktreeSetupFailure> = z
  .object({
    retry_readiness: z.enum(["retry_ready", "non_retryable"]),
    cause: failureCauseSchema,
    diagnostic: nonBlank,
    script_path: nullableNonBlank,
    execution_target: workflowExecutionTargetSelectionSchema.nullable(),
    retained_worktree: registeredWorktreeTopologySchema.nullable(),
    retained_previous_worktree: retainedPreviousWorktreeSchema.nullable(),
  })
  .strict()
  .superRefine((value, context) => {
    const retryable = ["process_exit", "timeout", "target_preparation"].includes(value.cause.kind);
    const scriptFailure = value.retry_readiness === "retry_ready" && value.cause.kind !== "target_preparation";
    if (value.cause.kind !== "operational" && retryable !== (value.retry_readiness === "retry_ready")) {
      context.addIssue({ code: "custom", message: "Retry readiness does not match failure cause." });
    }
    if (scriptFailure && (value.script_path === null || value.retained_worktree === null)) {
      context.addIssue({ code: "custom", message: "Setup failure requires script and Worktree facts." });
    }
    if (value.cause.kind === "target_preparation" && value.script_path !== null) {
      context.addIssue({ code: "custom", message: "Target preparation cannot include a setup script." });
    }
  })
  .transform((value) => ({
    retryReadiness: value.retry_readiness,
    cause: value.cause,
    diagnostic: value.diagnostic,
    scriptPath: value.script_path,
    executionTarget: value.execution_target,
    retainedWorktree: value.retained_worktree,
    retainedPreviousWorktree: value.retained_previous_worktree,
  }));

export class WorktreeSetupRetainedError extends RpcError {
  readonly worktree: RegisteredWorktreeTopology; readonly scriptPath: string; readonly diagnostic: string;
  readonly retainedPreviousWorktree: RetainedPreviousWorktree | null;

  constructor(rpcError: RpcError, facts: Readonly<{
    worktree: RegisteredWorktreeTopology; scriptPath: string; diagnostic: string;
    retainedPreviousWorktree: RetainedPreviousWorktree | null;
  }>) {
    super(rpcError);
    this.worktree = facts.worktree;
    this.scriptPath = facts.scriptPath;
    this.diagnostic = facts.diagnostic;
    this.retainedPreviousWorktree = facts.retainedPreviousWorktree;
  }
}

const retainedErrorSchema = z
  .object({
    type: z.literal("worktree_setup_retained"),
    worktree: registeredWorktreeTopologySchema,
    script_path: nonBlank,
    diagnostic: nonBlank,
    retained_previous_worktree: retainedPreviousWorktreeSchema.nullable(),
  })
  .strict();

export function decodeWorktreeSetupRetainedError(error: unknown): WorktreeSetupRetainedError | null {
  if (!(error instanceof RpcError) || error.code !== -32039) return null;
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

export type WorktreeSetupPhase = "started" | "completed" | "not_required" | "failed";
type SetupEvent<Phase extends WorktreeSetupPhase, Payload> =
  Readonly<{ setupOperationID: SetupOperationID; phase: Phase } & Payload>;
export type WorktreeSetupEvent =
  | SetupEvent<"started", { started: Readonly<{
      sourceWorkspaceRoot: string; worktreeRoot: string; scriptPath: string;
    }> }>
  | SetupEvent<"completed", { completed: Readonly<{ retainedPreviousWorktree: RetainedPreviousWorktree | null }> }>
  | SetupEvent<"not_required", { notRequired: Readonly<{
      reason: "no_target_preparation" | "no_configured_script";
      retainedPreviousWorktree: RetainedPreviousWorktree | null;
    }> }>
  | SetupEvent<"failed", { failed: WorktreeSetupFailure }>;

const setupEventWireSchema = z
  .discriminatedUnion("phase", [
    z.object({ setup_operation_id: setupOperationIDSchema, phase: z.literal("started"),
      started: z.object({ source_workspace_root: nonBlank, worktree_root: nonBlank, script_path: nonBlank }).strict() }).strict(),
    z.object({ setup_operation_id: setupOperationIDSchema, phase: z.literal("completed"),
      completed: z.object({ retained_previous_worktree: retainedPreviousWorktreeSchema.nullable() }).strict() }).strict(),
    z.object({ setup_operation_id: setupOperationIDSchema, phase: z.literal("not_required"),
      not_required: z.object({ reason: z.enum(["no_target_preparation", "no_configured_script"]),
        retained_previous_worktree: retainedPreviousWorktreeSchema.nullable() }).strict() }).strict(),
    z.object({ setup_operation_id: setupOperationIDSchema, phase: z.literal("failed"),
      failed: worktreeSetupFailureWireSchema }).strict(),
  ])
  .transform((value): WorktreeSetupEvent => {
    const setupOperationID = value.setup_operation_id;
    switch (value.phase) {
      case "started":
        return { setupOperationID, phase: value.phase, started: { sourceWorkspaceRoot: value.started.source_workspace_root,
          worktreeRoot: value.started.worktree_root, scriptPath: value.started.script_path } };
      case "completed":
        return { setupOperationID, phase: value.phase, completed: { retainedPreviousWorktree: value.completed.retained_previous_worktree } };
      case "not_required":
        return { setupOperationID, phase: value.phase, notRequired: { reason: value.not_required.reason,
          retainedPreviousWorktree: value.not_required.retained_previous_worktree } };
      case "failed":
        return { setupOperationID, phase: value.phase, failed: value.failed };
    }
  });

export type WorktreeSetupEventHandler = Readonly<{
  onOpen?(): void; onEvent(event: WorktreeSetupEvent): void;
  onComplete(code: number, message: string): void; onError(error: Error): void;
}>;
export const worktreeSetupEventParamsSchema = z.object({ event: setupEventWireSchema }).strict();
export function worktreeSetupRpcHandler(handler: WorktreeSetupEventHandler): RpcEventHandler {
  return {
    ...(handler.onOpen === undefined ? {} : { onOpen: handler.onOpen }),
    onComplete: handler.onComplete,
    onError: handler.onError,
    onEvent(method, params) {
      if (method !== "worktree.setup") return;
      const parsed = worktreeSetupEventParamsSchema.safeParse(params);
      if (parsed.success) handler.onEvent(parsed.data.event);
      else handler.onError(new ContractError("worktree.setup event did not match GUI contract."));
    },
  };
}
