import { z } from "zod";

import type {
  TaskApproveResponse,
  TaskMoveResponse,
  TaskStartResponse,
  WorkflowTaskExecutionTargetMaterializationProgress,
  WorkflowTaskExecutionTargetNegotiationConflict,
  WorkflowTaskExecutionTargetSelectionRequired,
  WorkflowTaskInitiatingActionResult,
} from "../models";
import { emptyString, stringList } from "./common";
import { workflowExecutionPolicySchema } from "./workflow";

const taskStartResponseSchema: z.ZodType<TaskStartResponse> = z
  .object({
    transition_id: z.string(),
    placement_id: z.string(),
    run_id: z.string(),
  })
  .transform((value) => ({
    placementID: value.placement_id,
    runID: value.run_id,
    transitionID: value.transition_id,
  }));

const taskMoveResponseSchema: z.ZodType<TaskMoveResponse> = z
  .object({
    transition_id: emptyString,
    state: emptyString,
    placement_ids: stringList,
    run_ids: stringList,
    approval_error: emptyString,
  })
  .transform((value) => ({
    approvalError: value.approval_error,
    placementIDs: value.placement_ids,
    runIDs: value.run_ids,
    state: value.state,
    transitionID: value.transition_id,
  }));

const taskApproveResponseSchema: z.ZodType<TaskApproveResponse> = z
  .object({
    transition_id: z.string(),
    task_id: z.string(),
    state: z.string(),
    placement_ids: stringList,
    run_ids: stringList,
  })
  .transform((value) => ({
    placementIDs: value.placement_ids,
    runIDs: value.run_ids,
    state: value.state,
    taskID: value.task_id,
    transitionID: value.transition_id,
  }));

const executionTargetSourceSchema = z.discriminatedUnion("kind", [
  z
    .object({
      kind: z.literal("non_git"),
      named_ref: z.undefined().optional(),
      commit: z.undefined().optional(),
    })
    .strict()
    .transform(() => ({ commit: null, kind: "non_git", namedRef: null }) as const),
  z
    .object({
      kind: z.literal("named_ref"),
      named_ref: z.string().min(1),
      commit: z.string().min(1),
    })
    .strict()
    .transform((value) => ({ commit: value.commit, kind: "named_ref", namedRef: value.named_ref }) as const),
  z
    .object({
      kind: z.literal("detached_commit"),
      named_ref: z.undefined().optional(),
      commit: z.string().min(1),
    })
    .strict()
    .transform((value) => ({ commit: value.commit, kind: "detached_commit", namedRef: null }) as const),
  z
    .object({
      kind: z.literal("unavailable"),
      named_ref: z.undefined().optional(),
      commit: z.undefined().optional(),
    })
    .strict()
    .transform(() => ({ commit: null, kind: "unavailable", namedRef: null }) as const),
]);

const selectionRequiredSchema: z.ZodType<WorkflowTaskExecutionTargetSelectionRequired> = z
  .object({
    task_id: z.string(),
    generation: z.string(),
    source_workspace_id: z.string(),
    source: executionTargetSourceSchema,
    supported_selections: z.array(z.enum(["none", "head", "default_branch", "custom_ref"])).min(1),
    configured_policy: workflowExecutionPolicySchema,
    recovery_cause: z.string().min(1).nullable().optional(),
  })
  .strict()
  .transform((value) => ({
    configuredPolicy: value.configured_policy,
    generation: value.generation,
    recoveryCause: value.recovery_cause ?? null,
    source: value.source,
    sourceWorkspaceID: value.source_workspace_id,
    supportedSelections: value.supported_selections,
    taskID: value.task_id,
  }));

const materializationProgressSchema: z.ZodType<WorkflowTaskExecutionTargetMaterializationProgress> = z
  .object({
    task_id: z.string(),
    phase: z.enum(["materializing", "recovery_queued", "recovering"]),
  })
  .strict()
  .transform((value) => ({ phase: value.phase, taskID: value.task_id }));

const negotiationConflictSchema: z.ZodType<WorkflowTaskExecutionTargetNegotiationConflict> = z
  .object({ task_id: z.string() })
  .strict()
  .transform((value) => ({ taskID: value.task_id }));

export const workflowTaskInitiatingActionResultSchema: z.ZodType<WorkflowTaskInitiatingActionResult> =
  z.discriminatedUnion("outcome", [
    z
      .object({ outcome: z.literal("started"), started: taskStartResponseSchema })
      .strict()
      .transform((value) => ({ outcome: "started", started: value.started }) as const),
    z
      .object({ outcome: z.literal("moved"), moved: taskMoveResponseSchema })
      .strict()
      .transform((value) => ({ moved: value.moved, outcome: "moved" }) as const),
    z
      .object({ outcome: z.literal("approved"), approved: taskApproveResponseSchema })
      .strict()
      .transform((value) => ({ approved: value.approved, outcome: "approved" }) as const),
    z
      .object({ outcome: z.literal("selection_required"), selection_required: selectionRequiredSchema })
      .strict()
      .transform(
        (value) =>
          ({
            outcome: "selection_required",
            selectionRequired: value.selection_required,
          }) as const,
      ),
    z
      .object({ outcome: z.literal("in_progress"), in_progress: materializationProgressSchema })
      .strict()
      .transform((value) => ({ inProgress: value.in_progress, outcome: "in_progress" }) as const),
    z
      .object({ outcome: z.literal("conflict"), conflict: negotiationConflictSchema })
      .strict()
      .transform((value) => ({ conflict: value.conflict, outcome: "conflict" }) as const),
  ]);
