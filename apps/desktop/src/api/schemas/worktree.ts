import { z } from "zod";

import type { SetupOperationID } from "../setupOperationID";
import { parseWorktreeOperationID } from "../worktreeOperationID";

const strict = z.strictObject;
type DeepReadonly<T> = T extends (...args: never[]) => unknown
  ? T
  : T extends readonly (infer Item)[]
    ? readonly DeepReadonly<Item>[]
    : T extends object
      ? { readonly [Key in keyof T]: DeepReadonly<T[Key]> }
      : T;
type Output<Schema extends z.ZodType> = DeepReadonly<z.output<Schema>>;
export const nonBlankString = z.string().refine((value) => value.trim().length > 0, {
  message: "Expected a non-blank string.",
});
const nullableNonBlankString = nonBlankString.nullable();
export const optionalNonBlankString = nonBlankString.optional().transform((value) => value ?? null);
export const worktreeOperationIDSchema = z.string().transform((value, context) => {
  try {
    return parseWorktreeOperationID(value);
  } catch {
    context.addIssue({ code: "custom", message: "Expected Worktree operation id UUID v4." });
    return z.NEVER;
  }
});

export const worktreeScheduledAcknowledgementSchema = strict({
  operation_id: worktreeOperationIDSchema,
}).transform((value) => ({ operationID: value.operation_id }));
export type WorktreeScheduledAcknowledgement = Output<typeof worktreeScheduledAcknowledgementSchema>;
const cleanup = z.discriminatedUnion("kind", [
  strict({ kind: z.literal("not_requested") }),
  strict({ kind: z.literal("not_applicable") }),
  strict({ kind: z.literal("deleted"), branch_name: nonBlankString }).transform((value) => ({
    kind: value.kind,
    branchName: value.branch_name,
  })),
  strict({
    kind: z.literal("retained"),
    branch_name: nonBlankString,
    diagnostic: optionalNonBlankString,
  }).transform((value) => ({
    kind: value.kind,
    branchName: value.branch_name,
    diagnostic: value.diagnostic,
  })),
]);
export const worktreeDeleteResultSchema = z.union([
  strict({
    kind: z.literal("completed"),
    completed: strict({ cleanup, leftover_root: optionalNonBlankString }),
  }).transform((value) => ({
    kind: value.kind,
    cleanup: value.completed.cleanup,
    leftoverRoot: value.completed.leftover_root,
  })),
  strict({
    kind: z.literal("scheduled"),
    scheduled: worktreeScheduledAcknowledgementSchema,
  }).transform((value) => ({ kind: value.kind, acknowledgement: value.scheduled })),
]);
export type WorktreeDeleteResult = Output<typeof worktreeDeleteResultSchema>;
export const worktreeCleanlinessSchema = z.discriminatedUnion("kind", [
  strict({ kind: z.literal("clean") }),
  strict({
    kind: z.literal("dirty"),
    dirty_file_count: z.number().int().positive(),
  }).transform((value) => ({ kind: value.kind, dirtyFileCount: value.dirty_file_count })),
  strict({ kind: z.literal("unknown"), unknown_cause: nonBlankString }).transform((value) => ({
    kind: value.kind,
    unknownCause: value.unknown_cause,
  })),
]);
export type WorktreeCleanliness = Output<typeof worktreeCleanlinessSchema>;
const executionWorktree = strict({
  ID: nonBlankString,
  Name: nonBlankString,
  Root: nonBlankString,
  Availability: z.enum(["available", "missing", "inaccessible"]),
}).transform((value) => ({
  id: value.ID,
  name: value.Name,
  root: value.Root,
  availability: value.Availability,
}));
export const sessionExecutionTargetSchema = strict({
  WorkspaceID: nonBlankString,
  WorkspaceName: nonBlankString,
  WorkspaceRoot: nonBlankString,
  WorkspaceAvailability: z.enum(["available", "missing", "inaccessible", "unlinked"]),
  Worktree: executionWorktree.nullable(),
  CwdRelpath: nonBlankString,
  EffectiveWorkdir: nonBlankString,
}).transform((value) => ({
  workspaceID: value.WorkspaceID,
  workspaceName: value.WorkspaceName,
  workspaceRoot: value.WorkspaceRoot,
  workspaceAvailability: value.WorkspaceAvailability,
  worktree: value.Worktree,
  cwdRelpath: value.CwdRelpath,
  effectiveWorkdir: value.EffectiveWorkdir,
}));
export type SessionExecutionTarget = Output<typeof sessionExecutionTargetSchema>;
const git = strict({
  canonical_root: nonBlankString,
  head_object: nonBlankString,
  branch_ref: nullableNonBlankString,
  branch_name: nullableNonBlankString,
  detached: z.boolean(),
  bare: z.boolean(),
  locked_reason: nullableNonBlankString,
  prunable_reason: nullableNonBlankString,
  is_main: z.boolean(),
  path_available: z.boolean(),
}).transform((value) => ({
  canonicalRoot: value.canonical_root,
  headObject: value.head_object,
  branchRef: value.branch_ref,
  branchName: value.branch_name,
  detached: value.detached,
  bare: value.bare,
  lockedReason: value.locked_reason,
  prunableReason: value.prunable_reason,
  isMain: value.is_main,
  pathAvailable: value.path_available,
}));
const kent = strict({
  worktree_id: nonBlankString,
  canonical_root: nonBlankString,
  display_name: nonBlankString,
  managed: z.boolean(),
  created_branch: z.boolean(),
  origin_session_id: nullableNonBlankString,
}).transform((value) => ({
  worktreeID: value.worktree_id,
  canonicalRoot: value.canonical_root,
  displayName: value.display_name,
  managed: value.managed,
  createdBranch: value.created_branch,
  originSessionID: value.origin_session_id,
}));
export const worktreeTopologySchema = z
  .discriminatedUnion("variant", [
    strict({ variant: z.literal("registered"), registered: strict({ git, kent }) }),
    strict({ variant: z.literal("external"), external: strict({ git }) }),
    strict({ variant: z.literal("missing"), missing: strict({ kent }) }),
  ])
  .refine(
    (value) =>
      value.variant !== "registered" ||
      value.registered.git.canonicalRoot === value.registered.kent.canonicalRoot,
    { message: "Registered Git and Kent roots must match." },
  )
  .transform((value) => {
    switch (value.variant) {
      case "registered":
        return { variant: value.variant, ...value.registered } as const;
      case "external":
        return { variant: value.variant, ...value.external } as const;
      case "missing":
        return { variant: value.variant, ...value.missing } as const;
    }
  });
export type WorktreeTopology = Output<typeof worktreeTopologySchema>;
export type RegisteredWorktreeTopology = Extract<WorktreeTopology, { variant: "registered" }>;
export const registeredWorktreeTopologySchema = worktreeTopologySchema.refine(
  (value): value is RegisteredWorktreeTopology => value.variant === "registered",
  { message: "Expected a registered Worktree topology." },
);
type AuthorityKind = "switch" | "delete" | "create";
const authorities = new WeakMap<object, AuthorityKind>();
class AuthorityValue {
  readonly [Symbol.toStringTag] = "WorktreeAuthority";
  static from<Facts extends object>(facts: Facts) {
    return Object.assign(new AuthorityValue(), facts);
  }
}
Object.defineProperty(AuthorityValue.prototype, "constructor", { value: undefined });
type Authority<Facts> = Readonly<Facts>;
function decoded<Facts extends object>(authority: AuthorityKind, facts: Facts): Authority<Facts> {
  const value = deepFreeze(AuthorityValue.from(facts));
  authorities.set(value, authority);
  return value;
}
function deepFreeze<Value extends object>(value: Value): Value {
  for (const fact of Object.values(value)) {
    if (fact instanceof Object) deepFreeze(fact);
  }
  return Object.freeze(value);
}

export type WorktreeSwitch = Authority<
  { kind: "enter"; selector: string } | { kind: "leave"; selector: null }
>;
const worktreeSwitch = z.union([
  strict({ kind: z.literal("enter"), selector: nonBlankString }).transform((value) =>
    decoded("switch", value),
  ),
  strict({ kind: z.literal("leave") }).transform((value) =>
    decoded("switch", { kind: value.kind, selector: null }),
  ),
]);
const projection = strict({
  selector: nonBlankString,
  is_current: z.boolean(),
  switch: worktreeSwitch.optional().transform((value) => value ?? null),
  delete_preview: strict({ selector: nonBlankString })
    .optional()
    .transform((value) => value ?? null),
  fallback_identity: optionalNonBlankString,
});
export const worktreeListEntrySchema = strict({
  topology: worktreeTopologySchema,
  projection,
}).transform((value) => ({
  topology: value.topology,
  selector: value.projection.selector,
  isCurrent: value.projection.is_current,
  switchOperation: value.projection.switch,
  deletePreviewOperation: value.projection.delete_preview,
  fallbackIdentity: value.projection.fallback_identity,
}));
export type WorktreeListEntry = Output<typeof worktreeListEntrySchema>;
export const worktreeListResponseSchema = strict({
  target: sessionExecutionTargetSchema,
  worktrees: z.array(worktreeListEntrySchema),
});
export type WorktreeList = Output<typeof worktreeListResponseSchema>;
export const worktreeCreateResponseSchema = strict({
  target: sessionExecutionTargetSchema,
  worktree: worktreeListEntrySchema,
});
export type WorktreeCreateResponse = Output<typeof worktreeCreateResponseSchema>;
export const worktreeSelectorResolutionSchema = strict({ worktree: worktreeListEntrySchema });
export type WorktreeSelectorResolution = Output<typeof worktreeSelectorResolutionSchema>;

export type WorktreeDeleteConfirmationChoice = "confirm" | "confirm_and_branch";
export type WorktreeDeletePreview = Authority<{
  topology: WorktreeTopology;
  deletionSelector: string;
  cleanliness: WorktreeCleanliness;
}>;
export const worktreeDeletePreviewResponseSchema = strict({
  worktree: worktreeTopologySchema,
  deletion_selector: nonBlankString,
  cleanliness: worktreeCleanlinessSchema,
}).transform((value) =>
  decoded("delete", {
    topology: value.worktree,
    deletionSelector: value.deletion_selector,
    cleanliness: value.cleanliness,
  }),
);

const statusTarget = strict({
  recorded_root: nonBlankString,
  observed_root: optionalNonBlankString,
  display_name: optionalNonBlankString,
  recorded_branch_ref: optionalNonBlankString,
  observed_branch_ref: optionalNonBlankString,
}).transform((value) => ({
  recordedRoot: value.recorded_root,
  observedRoot: value.observed_root,
  displayName: value.display_name,
  recordedBranchRef: value.recorded_branch_ref,
  observedBranchRef: value.observed_branch_ref,
}));
const rootProblem = strict({
  kind: z.enum(["root_missing", "root_inaccessible", "git_binding_missing", "git_binding_mismatched"]),
  root: nonBlankString,
});
const refProblem = strict({ kind: z.literal("recorded_ref_missing"), ref: nonBlankString });
export const worktreeStatusResponseSchema = strict({
  target: sessionExecutionTargetSchema,
  worktree: statusTarget,
  problems: z.array(z.union([rootProblem, refProblem])),
});
export type WorktreeStatus = Output<typeof worktreeStatusResponseSchema>;

type CreateResolution =
  | { kind: "new_branch"; input: string; resolvedRef: null }
  | { kind: "existing_branch" | "detached_ref"; input: string; resolvedRef: string };
export type WorktreeCreateTargetResolution = Authority<CreateResolution>;
const createResolution = z.discriminatedUnion("kind", [
  strict({ kind: z.literal("new_branch"), input: nonBlankString }).transform((value) =>
    decoded("create", { ...value, resolvedRef: null }),
  ),
  ...(["existing_branch", "detached_ref"] as const).map((kind) =>
    strict({ kind: z.literal(kind), input: nonBlankString, resolved_ref: nonBlankString }).transform(
      (value) => decoded("create", { kind: value.kind, input: value.input, resolvedRef: value.resolved_ref }),
    ),
  ),
]);
export const worktreeCreateTargetResolutionResponseSchema = strict({
  resolution: createResolution,
});
export type WorktreeCreateTargetResolutionResponse = Output<
  typeof worktreeCreateTargetResolutionResponseSchema
>;

export function requireWorktreeAuthority(value: unknown, authority: "switch"): WorktreeSwitch;
export function requireWorktreeAuthority(value: unknown, authority: "delete"): WorktreeDeletePreview;
export function requireWorktreeAuthority(value: unknown, authority: "create"): WorktreeCreateTargetResolution;
export function requireWorktreeAuthority(value: unknown, authority: AuthorityKind): unknown {
  if (!(value instanceof Object) || authorities.get(value) !== authority) {
    throw new TypeError(`Worktree ${authority} requires decoded authority.`);
  }
  return value;
}
export type WorktreeCreateInput = Readonly<{
  sessionID: string;
  setupOperationID: SetupOperationID;
  resolution: WorktreeCreateTargetResolution;
  baseRef: string | null;
}>;
