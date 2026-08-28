import { z } from "zod";

import { parseRpcResponse } from "./clientParse";
import { decodePendingWorkError } from "./clientPendingWork";
import { ContractError, RpcError } from "./errors";
import type { JsonValue } from "./json";
import { rpcErrorCodes } from "./rpcErrorCodes";
import * as worktree from "./schemas/worktree";
import type { RpcCallOptions, RpcTransport } from "./transport";
import { decodeWorktreeSetupRetainedError, type RetainedPreviousWorktree } from "./worktreeSetup";
import { parseWorktreeOperationID } from "./worktreeOperationID";

export const getWorktreeStatus = async (transport: RpcTransport, sessionID: string) =>
  call(transport, "worktree.status", session(sessionID), { schema: worktree.worktreeStatusResponseSchema });
export const listWorktrees = async (transport: RpcTransport, sessionID: string) =>
  call(transport, "worktree.list", session(sessionID), { schema: worktree.worktreeListResponseSchema });
export const resolveWorktreeSelector = factRead(
  "worktree.selector.resolve",
  "selector",
  worktree.worktreeSelectorResolutionSchema,
);
export const resolveWorktreeCreateTarget = factRead(
  "worktree.create_target.resolve",
  "target",
  worktree.worktreeCreateTargetResolutionResponseSchema,
);
export const previewWorktreeDelete = factRead(
  "worktree.deletePreview",
  "selector",
  worktree.worktreeDeletePreviewResponseSchema,
);

export async function createWorktree(transport: RpcTransport, input: worktree.WorktreeCreateInput) {
  const resolution = worktree.requireWorktreeAuthority(input.resolution, "create");
  if ((resolution.kind === "new_branch") !== (input.baseRef !== null)) {
    throw new TypeError("Worktree Create resolution and Base ref do not match.");
  }
  return call(
    transport,
    "worktree.create",
    {
      ...session(input.sessionID),
      setup_operation_id: input.setupOperationID.toJSONValue(),
      base_ref:
        resolution.kind === "new_branch"
          ? worktree.nonBlankString.parse(input.baseRef)
          : resolution.resolvedRef,
      ...(resolution.kind === "new_branch" ? { create_branch: true, branch_name: resolution.input } : {}),
    },
    { schema: worktree.worktreeCreateResponseSchema, options: { timeoutMs: null } },
  );
}

export async function switchWorktree(
  transport: RpcTransport,
  sessionID: string,
  operation: worktree.WorktreeSwitch,
) {
  const authority = worktree.requireWorktreeAuthority(operation, "switch");
  const method = authority.kind === "enter" ? "worktree.enter" : "worktree.leave";
  const id = parseWorktreeOperationID(crypto.randomUUID());
  const result = await call(
    transport,
    method,
    authority.kind === "enter"
      ? { ...session(sessionID), operation_id: id.toJSONValue(), selector: authority.selector }
      : { ...session(sessionID), operation_id: id.toJSONValue() },
    { schema: worktree.worktreeScheduledAcknowledgementSchema },
  );
  requireMatchingOperationID(id.toJSONValue(), result);
  return result;
}

export async function deleteWorktree(
  transport: RpcTransport,
  sessionID: string,
  preview: worktree.WorktreeDeletePreview,
  confirmation: worktree.WorktreeDeleteConfirmationChoice,
) {
  const authority = worktree.requireWorktreeAuthority(preview, "delete");
  const choice = z.enum(["confirm", "confirm_and_branch"]).parse(confirmation);
  if (
    choice === "confirm_and_branch" &&
    (authority.topology.variant === "missing" || authority.topology.git.branchName === null)
  ) {
    throw new TypeError("Worktree Delete confirmation is invalid for this preview.");
  }
  const result = await call(
    transport,
    "worktree.delete",
    {
      ...session(sessionID),
      selector: authority.deletionSelector,
      force_folder_removal: authority.cleanliness.kind !== "clean",
      branch_cleanup_policy: choice === "confirm" ? "auto_if_kent_created" : "delete_safe",
    },
    { schema: worktree.worktreeDeleteResultSchema },
  );
  return result;
}

const session = (sessionID: string) => ({ session_id: worktree.nonBlankString.parse(sessionID) });
function factRead<Output>(method: string, fact: "selector" | "target", schema: z.ZodType<Output>) {
  return async (transport: RpcTransport, sessionID: string, value: string) =>
    call(
      transport,
      method,
      { ...session(sessionID), [fact]: worktree.nonBlankString.parse(value) },
      { schema },
    );
}
function requireMatchingOperationID(
  expected: string,
  acknowledgement: worktree.WorktreeScheduledAcknowledgement,
) {
  if (acknowledgement.operationID.toJSONValue() !== expected) {
    throw new ContractError("Server returned a different Worktree operation identity.");
  }
}
type CallContract<T> = Readonly<{ schema: z.ZodType<T>; options?: RpcCallOptions }>;
async function call<T>(
  transport: RpcTransport,
  method: string,
  params: JsonValue,
  contract: CallContract<T>,
): Promise<T> {
  const { schema, options } = contract;
  try {
    return parseRpcResponse(method, schema, await transport.call(method, params, options));
  } catch (error) {
    throw decodeWorktreeError(error) ?? decodePendingWorkError(error) ?? error;
  }
}

const strict = z.strictObject;
const candidate = strict({
  variant: z.enum(["registered", "external", "missing"]),
  selector: worktree.nonBlankString,
  branch_name: worktree.optionalNonBlankString,
  display_name: worktree.optionalNonBlankString,
  fallback_identity: worktree.nonBlankString,
}).transform((value) => ({
  variant: value.variant,
  selector: value.selector,
  branchName: value.branch_name,
  displayName: value.display_name,
  fallbackIdentity: value.fallback_identity,
}));
const selectorError = strict({
  type: z.literal("worktree_selector_error"),
  kind: z.enum(["not_found", "ambiguous", "unavailable"]),
  input: worktree.nonBlankString,
  candidates: z.array(candidate).optional(),
})
  .refine((value) =>
    value.kind === "ambiguous" ? (value.candidates?.length ?? 0) > 0 : value.candidates === undefined,
  )
  .transform((value) => ({
    kind: "selector" as const,
    reason: value.kind,
    input: value.input,
    candidates: value.candidates ?? [],
  }));
const errorSchemas = {
  [rpcErrorCodes.worktreeSelector]: selectorError,
  [rpcErrorCodes.worktreeCreate]: strict({
    owner: z.enum(["base_ref", "form"]),
    diagnostic: worktree.nonBlankString,
  }).transform((value) => ({ kind: "create" as const, ...value })),
  [rpcErrorCodes.worktreeDeletePrecondition]: strict({
    type: z.literal("worktree_delete_precondition"),
    dirty_state: worktree.worktreeCleanlinessSchema.refine(
      (value): value is Exclude<worktree.WorktreeCleanliness, Readonly<{ kind: "clean" }>> =>
        value.kind !== "clean",
    ),
  }).transform((value) => ({
    kind: "delete_precondition" as const,
    cleanliness: value.dirty_state,
  })),
};
type ErrorSchema = (typeof errorSchemas)[keyof typeof errorSchemas];
export type WorktreeErrorDetail =
  | Readonly<z.output<ErrorSchema>>
  | Readonly<{ kind: "blocked" }>
  | Readonly<{
      kind: "setup_retained";
      worktree: worktree.RegisteredWorktreeTopology;
      scriptPath: string;
      diagnostic: string;
      retainedPreviousWorktree: RetainedPreviousWorktree | null;
    }>;
export class WorktreeError extends RpcError {
  constructor(
    rpcError: RpcError,
    readonly detail: WorktreeErrorDetail,
  ) {
    super(rpcError);
    this.name = "WorktreeError";
  }
}
function hasWorktreeErrorSchema(code: number): code is keyof typeof errorSchemas {
  return Object.hasOwn(errorSchemas, code);
}
export function decodeWorktreeError(error: unknown): WorktreeError | null {
  if (!(error instanceof RpcError)) return null;
  if (error.code === rpcErrorCodes.worktreeBlocked) {
    return error.data === undefined ? new WorktreeError(error, { kind: "blocked" }) : null;
  }
  const retained = decodeWorktreeSetupRetainedError(error);
  if (retained !== null) {
    return new WorktreeError(retained, {
      kind: "setup_retained",
      worktree: retained.worktree,
      scriptPath: retained.scriptPath,
      diagnostic: retained.diagnostic,
      retainedPreviousWorktree: retained.retainedPreviousWorktree,
    });
  }
  if (!hasWorktreeErrorSchema(error.code)) return null;
  const parsed = errorSchemas[error.code].safeParse(error.data);
  return parsed.success ? new WorktreeError(error, parsed.data) : null;
}
