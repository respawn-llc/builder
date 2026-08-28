import { z } from "zod";

import { parseRpcResponse } from "./clientParse";
import { RpcError } from "./errors";
import { jsonValueSchema, type JsonValue } from "./json";
import {
  normalizeWhitespace,
  parseCompactionRequestID,
  parsePendingWorkItemID,
  pendingWorkRestorationSchema,
  pendingWorkSchema,
  type CompactionRequestID,
  type PendingWork,
  type PendingWorkIdentity,
  type PendingWorkItemID,
  type PendingWorkRestoration,
} from "./pendingWork";
import { rpcErrorCodes } from "./rpcErrorCodes";
import type { RpcTransport } from "./transport";

const strict = z.strictObject;
const emptyResponseSchema = strict({});
const listResponseSchema = strict({ pending_work: pendingWorkSchema }).transform(
  (value) => value.pending_work,
);
const removeResponseSchema = strict({ restoration: pendingWorkRestorationSchema }).transform(
  (value) => value.restoration,
);

export async function submitManualCompaction(
  transport: RpcTransport,
  sessionID: string,
  guidance: string | null,
): Promise<CompactionRequestID> {
  return withPendingWorkErrors(async () => {
    const requestID = parseCompactionRequestID(crypto.randomUUID());
    const normalizedGuidance = guidance === null ? null : normalizeWhitespace(guidance);
    if (normalizedGuidance === "") {
      throw new TypeError("Manual compaction guidance must be non-blank when provided.");
    }
    await callDedicated(transport, methods.compact, {
      session_id: requiredSessionID(sessionID),
      request_id: requestID.toJSONValue(),
      admission: normalizedGuidance === null ? {} : { guidance: normalizedGuidance },
    });
    return requestID;
  });
}

export async function listPendingWork(transport: RpcTransport, sessionID: string): Promise<PendingWork> {
  return withPendingWorkErrors(async () =>
    parseRpcResponse(
      methods.list,
      listResponseSchema,
      await transport.call(methods.list, { session_id: requiredSessionID(sessionID) }),
    ),
  );
}

export async function removePendingWork(
  transport: RpcTransport,
  sessionID: string,
  itemID: PendingWorkIdentity,
): Promise<PendingWorkRestoration> {
  return withPendingWorkErrors(async () =>
    parseRpcResponse(
      methods.remove,
      removeResponseSchema,
      await transport.call(methods.remove, {
        session_id: requiredSessionID(sessionID),
        item_id: itemID.toJSONValue(),
      }),
    ),
  );
}

async function callDedicated(transport: RpcTransport, method: string, params: JsonValue): Promise<void> {
  parseRpcResponse(method, emptyResponseSchema, await transport.callDedicated(method, params));
}

function requiredSessionID(sessionID: string): string {
  if (sessionID.trim().length === 0) {
    throw new TypeError("Session id is required.");
  }
  return sessionID;
}

const methods = {
  compact: "runtime.compactContext",
  list: "runtime.pendingWork.list",
  remove: "runtime.pendingWork.remove",
  worktreeEnter: "worktree.enter",
  worktreeLeave: "worktree.leave",
} as const;

export type ManualCompactionErrorReason = "too_soon" | "disabled" | "active";
export type PendingWorkFailure =
  | Readonly<{ kind: "capacity" }>
  | Readonly<{ kind: "not_pending"; itemID: PendingWorkItemID }>
  | Readonly<{ kind: "runtime_unavailable" }>
  | Readonly<{ kind: "manual_compaction"; reason: ManualCompactionErrorReason }>;
export type PendingWorkErrorDetail =
  PendingWorkFailure | Readonly<{ kind: "not_accepted"; cause: PendingWorkFailure }>;

export class PendingWorkError extends RpcError {
  constructor(
    rpcError: RpcError,
    readonly detail: PendingWorkErrorDetail,
  ) {
    super(rpcError);
    this.name = "PendingWorkError";
  }
}

const capacityErrorSchema = strict({ reason: z.literal("capacity") });
const notPendingErrorSchema = strict({
  item_id: z.string().transform((value, context) => {
    try {
      return parsePendingWorkItemID(value);
    } catch {
      context.addIssue({ code: "custom", message: "Expected Pending Work item UUID v4." });
      return z.NEVER;
    }
  }),
});
const nestedCauseSchema = strict({
  code: z.number().int(),
  message: z.string().trim().min(1),
  data: jsonValueSchema.optional(),
});

export function decodePendingWorkError(error: unknown): PendingWorkError | null {
  if (!(error instanceof RpcError)) return null;
  if (error.method === methods.compact && error.code === rpcErrorCodes.runtimeCommandNotAccepted) {
    const nested = strict({ cause: nestedCauseSchema }).safeParse(error.data);
    if (!nested.success || nested.data.cause.code === rpcErrorCodes.runtimeCommandNotAccepted) {
      return null;
    }
    const cause = decodeDirectPendingWorkFailure(
      new RpcError({
        method: error.method,
        code: nested.data.cause.code,
        message: nested.data.cause.message,
        data: nested.data.cause.data,
      }),
    );
    return cause === null ? null : new PendingWorkError(error, { kind: "not_accepted", cause });
  }
  const detail = decodeDirectPendingWorkFailure(error);
  return detail === null ? null : new PendingWorkError(error, detail);
}

function decodeDirectPendingWorkFailure(error: RpcError): PendingWorkFailure | null {
  for (const decode of directFailureDecoders) {
    const decoded = decode(error);
    if (decoded !== null) return decoded;
  }
  return null;
}

type FailureDecoder = (error: RpcError) => PendingWorkFailure | null;
const directFailureDecoders: readonly FailureDecoder[] = [
  decodeRuntimeUnavailable,
  decodeCapacity,
  decodeNotPending,
  decodeManualCompaction,
];

function decodeRuntimeUnavailable(error: RpcError): PendingWorkFailure | null {
  if (error.code === rpcErrorCodes.runtimeUnavailable && isPendingWorkMethod(error.method)) {
    return error.data === undefined ? { kind: "runtime_unavailable" } : null;
  }
  return null;
}

function decodeCapacity(error: RpcError): PendingWorkFailure | null {
  if (isPendingWorkAdmissionMethod(error.method) && error.code === rpcErrorCodes.pendingWorkCapacity) {
    return capacityErrorSchema.safeParse(error.data).success ? { kind: "capacity" } : null;
  }
  return null;
}

function decodeNotPending(error: RpcError): PendingWorkFailure | null {
  if (error.method === methods.remove && error.code === rpcErrorCodes.pendingWorkNotPending) {
    const parsed = notPendingErrorSchema.safeParse(error.data);
    return parsed.success ? { kind: "not_pending", itemID: parsed.data.item_id } : null;
  }
  return null;
}

function decodeManualCompaction(error: RpcError): PendingWorkFailure | null {
  if (error.method !== methods.compact) return null;
  const reason = manualCompactionReason(error.code);
  if (reason === null) return null;
  const parsed = strict({ reason: z.literal(reason) }).safeParse(error.data);
  return parsed.success ? { kind: "manual_compaction", reason } : null;
}

function manualCompactionReason(code: number): ManualCompactionErrorReason | null {
  switch (code) {
    case rpcErrorCodes.manualCompactionTooSoon:
      return "too_soon";
    case rpcErrorCodes.manualCompactionDisabled:
      return "disabled";
    case rpcErrorCodes.manualCompactionActive:
      return "active";
    default:
      return null;
  }
}

function isPendingWorkMethod(method: string): boolean {
  return isPendingWorkAdmissionMethod(method) || method === methods.list || method === methods.remove;
}

function isPendingWorkAdmissionMethod(method: string): boolean {
  return method === methods.compact || method === methods.worktreeEnter || method === methods.worktreeLeave;
}

async function withPendingWorkErrors<Value>(action: () => Promise<Value>): Promise<Value> {
  try {
    return await action();
  } catch (error) {
    throw decodePendingWorkError(error) ?? error;
  }
}
