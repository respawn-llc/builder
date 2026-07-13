import { z } from "zod";

import type {
  WorkflowExecutionTarget,
  WorkflowExecutionTargetWorktree,
} from "../workflowExecutionTarget";

const nonBlankString = z.string().trim().min(1);
const optionalNonBlankString = nonBlankString.optional().transform((value) => value ?? null);

const managedWorktreeSchema: z.ZodType<WorkflowExecutionTargetWorktree> = z
  .object({
    worktree_id: nonBlankString,
    display_name: nonBlankString,
    canonical_root: nonBlankString,
    availability: z.enum(["available", "missing", "inaccessible"]),
    branch_ref: nonBlankString.optional(),
    branch_name: nonBlankString.optional(),
    detached: z.boolean().optional(),
    locked_reason: nonBlankString.optional(),
    prunable_reason: nonBlankString.optional(),
    dirty_file_count: z.number().int().nonnegative().optional(),
    is_main: z.boolean().optional(),
    is_current: z.boolean().optional(),
    managed: z.boolean().optional(),
    created_branch: z.boolean().optional(),
    origin_session_id: nonBlankString.optional(),
  })
  .strict()
  .transform((value) => ({
    id: value.worktree_id,
    displayName: value.display_name,
    canonicalRoot: value.canonical_root,
    availability: value.availability,
  }));

const noManagedWorktreeTargetSchema = z
  .object({
    mode: z.literal("none"),
    effective_root: nonBlankString,
    provenance: z.literal("resolved"),
  })
  .strict()
  .transform(
    (value): WorkflowExecutionTarget => ({
      mode: value.mode,
      effectiveRoot: value.effective_root,
      requestedRef: null,
      resolvedRef: null,
      commitOID: null,
      provenance: value.provenance,
      currentBranch: null,
      managedWorktree: null,
    }),
  );

const managedTargetSchema = z
  .object({
    mode: z.enum(["head", "default_branch", "custom_ref"]),
    effective_root: optionalNonBlankString,
    requested_ref: nonBlankString,
    resolved_ref: optionalNonBlankString,
    commit_oid: nonBlankString,
    provenance: z.enum(["resolved", "legacy_observed"]),
    current_branch: optionalNonBlankString,
    managed_worktree: managedWorktreeSchema.optional().transform((value) => value ?? null),
  })
  .strict()
  .superRefine((value, context) => {
    if (value.effective_root !== null && value.managed_worktree === null) {
      context.addIssue({
        code: "custom",
        message: "Managed execution target root requires worktree facts.",
        path: ["managed_worktree"],
      });
    }
    if (
      value.effective_root !== null &&
      value.managed_worktree !== null &&
      value.effective_root !== value.managed_worktree.canonicalRoot
    ) {
      context.addIssue({
        code: "custom",
        message: "Managed execution root must match worktree root.",
        path: ["effective_root"],
      });
    }
    if (value.current_branch !== null && value.effective_root === null) {
      context.addIssue({
        code: "custom",
        message: "Current branch requires an available execution root.",
        path: ["current_branch"],
      });
    }
  })
  .transform(
    (value): WorkflowExecutionTarget => ({
      mode: value.mode,
      effectiveRoot: value.effective_root,
      requestedRef: value.requested_ref,
      resolvedRef: value.resolved_ref,
      commitOID: value.commit_oid,
      provenance: value.provenance,
      currentBranch: value.current_branch,
      managedWorktree: value.managed_worktree,
    }),
  );

export const workflowExecutionTargetSchema: z.ZodType<WorkflowExecutionTarget> = z.union([
  noManagedWorktreeTargetSchema,
  managedTargetSchema,
]);
