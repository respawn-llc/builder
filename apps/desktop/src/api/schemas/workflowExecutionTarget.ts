import { z } from "zod";

import type { WorkflowExecutionTarget, WorkflowExecutionTargetSelection } from "../workflowExecutionTarget";

const nonBlankString = z.string().trim().min(1);
const optionalNonBlankString = nonBlankString.optional().transform((value) => value ?? null);

const noManagedWorktreeTargetSchema = z
  .object({
    mode: z.literal("none"),
    provenance: z.literal("resolved"),
  })
  .strict()
  .transform((value): WorkflowExecutionTarget => ({
    mode: value.mode,
    requestedRef: null,
    resolvedRef: null,
    commitOID: null,
    provenance: value.provenance,
  }));

const managedTargetSchema = z
  .object({
    mode: z.enum(["head", "default_branch", "custom_ref"]),
    requested_ref: nonBlankString,
    resolved_ref: optionalNonBlankString,
    commit_oid: nonBlankString,
    provenance: z.enum(["resolved", "legacy_observed"]),
  })
  .strict()
  .transform((value): WorkflowExecutionTarget => ({
    mode: value.mode,
    requestedRef: value.requested_ref,
    resolvedRef: value.resolved_ref,
    commitOID: value.commit_oid,
    provenance: value.provenance,
  }));

export const workflowExecutionTargetSchema: z.ZodType<WorkflowExecutionTarget> = z.union([
  noManagedWorktreeTargetSchema,
  managedTargetSchema,
]);

export const workflowExecutionTargetSelectionSchema: z.ZodType<WorkflowExecutionTargetSelection> =
  z.discriminatedUnion("mode", [
    z
      .object({
        mode: z.enum(["none", "head", "default_branch"]),
      })
      .strict()
      .transform((value) => ({ mode: value.mode, customRef: null })),
    z
      .object({
        mode: z.literal("custom_ref"),
        custom_ref: nonBlankString,
      })
      .strict()
      .transform((value) => ({ mode: value.mode, customRef: value.custom_ref })),
  ]);
