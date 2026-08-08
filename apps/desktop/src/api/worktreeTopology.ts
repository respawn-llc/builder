import { z } from "zod";

const optionalNonBlankString = z.string().trim().min(1).nullable().optional();

const gitFactsSchema = z
  .object({
    canonical_root: z.string().trim().min(1),
    head_object: z.string().trim().min(1),
    branch_ref: optionalNonBlankString,
    branch_name: optionalNonBlankString,
    detached: z.boolean(),
    bare: z.boolean(),
    locked_reason: optionalNonBlankString,
    prunable_reason: optionalNonBlankString,
    is_main: z.boolean(),
    path_available: z.boolean(),
  })
  .strict()
  .transform((value) => ({
    canonicalRoot: value.canonical_root,
    headObject: value.head_object,
    branchRef: value.branch_ref ?? null,
    branchName: value.branch_name ?? null,
    detached: value.detached,
    bare: value.bare,
    lockedReason: value.locked_reason ?? null,
    prunableReason: value.prunable_reason ?? null,
    isMain: value.is_main,
    pathAvailable: value.path_available,
  }));

const kentFactsSchema = z
  .object({
    worktree_id: z.string().trim().min(1),
    canonical_root: z.string().trim().min(1),
    display_name: z.string().trim().min(1),
    managed: z.boolean(),
    created_branch: z.boolean(),
    origin_session_id: optionalNonBlankString,
  })
  .strict()
  .transform((value) => ({
    worktreeID: value.worktree_id,
    canonicalRoot: value.canonical_root,
    displayName: value.display_name,
    managed: value.managed,
    createdBranch: value.created_branch,
    originSessionID: value.origin_session_id ?? null,
  }));

export type RegisteredWorktreeTopology = Readonly<{
  variant: "registered";
  registered: Readonly<{
    git: z.output<typeof gitFactsSchema>;
    kent: z.output<typeof kentFactsSchema>;
  }>;
}>;

export type RetainedPreviousWorktree = Readonly<{
  worktree: RegisteredWorktreeTopology;
}>;

export const registeredWorktreeTopologySchema: z.ZodType<RegisteredWorktreeTopology> = z
  .object({
    variant: z.literal("registered"),
    registered: z
      .object({
        git: gitFactsSchema,
        kent: kentFactsSchema,
      })
      .strict()
      .superRefine((value, context) => {
        if (value.git.canonicalRoot !== value.kent.canonicalRoot) {
          context.addIssue({ code: "custom", message: "Registered worktree roots must match." });
        }
      }),
  })
  .strict();

export const retainedPreviousWorktreeSchema: z.ZodType<RetainedPreviousWorktree> = z
  .object({
    worktree: registeredWorktreeTopologySchema,
  })
  .strict();
