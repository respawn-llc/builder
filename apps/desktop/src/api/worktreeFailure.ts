import { classifyResultFailure, operationName, type DescMethod } from "@app/server-api-contract";
import { ServerNotReadyReason } from "@app/server-api-contract/gen/kent/api/server/server_pb";
import {
  CreateErrorOwner,
  ImmediateTransitionErrorKind,
  SelectorErrorKind,
  TopologyVariant,
  type CreateError,
  type CreateTargetResolveError,
  type DeleteError,
  type DeletePreviewError,
  type EnterError,
  type LeaveError,
  type ListError,
  type SelectorResolveError,
  type SetupRetainedDetails,
  type SetupStartError,
  type StatusError,
  type WorkspaceListError,
} from "@app/server-api-contract/gen/kent/api/worktree/worktree_pb";
import { z } from "zod";

import { ContractError, RpcError } from "./errors";
import { protobufRpcError } from "./protobufRpc";
import { rpcErrorCodes } from "./rpcErrorCodes";
import { parseWorktreeOperationID } from "./worktreeOperationID";
import {
  registeredWorktreeTopologySchema,
  type RegisteredWorktreeTopology,
  type RetainedPreviousWorktree,
  type WorktreeCleanliness,
} from "./schemas/worktree";
import {
  projectRegisteredWorktree,
  projectRetainedPreviousWorktree,
  projectWorktreeDirtyState,
} from "./worktreeProtoProjection";

export type WorktreeFailure =
  | StatusError
  | ListError
  | WorkspaceListError
  | SelectorResolveError
  | DeletePreviewError
  | CreateTargetResolveError
  | CreateError
  | EnterError
  | LeaveError
  | DeleteError
  | SetupStartError;
type WorktreeFailureDetail = WorktreeFailure["detail"];
type GatewayFailureDetail = Extract<
  WorktreeFailureDetail,
  | Readonly<{ case: "authRequired" }>
  | Readonly<{ case: "workspaceNotRegistered" }>
  | Readonly<{ case: "serverNotReady" }>
  | Readonly<{ case: "internalFailure" }>
  | Readonly<{ case: undefined }>
>;
type DomainFailureDetail = Exclude<WorktreeFailureDetail, GatewayFailureDetail>;

export type WorktreeErrorDetail =
  | Readonly<{
      kind: "selector";
      reason: "not_found" | "ambiguous" | "unavailable";
      input: string;
      candidates: readonly Readonly<{
        variant: "registered" | "external" | "missing";
        selector: string;
        branchName: string | null;
        displayName: string | null;
        fallbackIdentity: string;
      }>[];
    }>
  | Readonly<{ kind: "create"; owner: "base_ref" | "form"; diagnostic: string }>
  | Readonly<{
      kind: "transition_pending";
      sessionID: string;
      pendingOperationID: ReturnType<typeof parseWorktreeOperationID>;
    }>
  | Readonly<{
      kind: "immediate_transition";
      reason: "origin_inactive" | "apply_failed";
    }>
  | Readonly<{ kind: "delete_precondition"; cleanliness: Exclude<WorktreeCleanliness, { kind: "clean" }> }>
  | Readonly<{ kind: "blocked" }>
  | Readonly<{
      kind: "setup_retained";
      worktree: RegisteredWorktreeTopology;
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

export class WorktreeSetupRetainedError extends RpcError {
  readonly worktree: RegisteredWorktreeTopology;
  readonly scriptPath: string;
  readonly diagnostic: string;
  readonly retainedPreviousWorktree: RetainedPreviousWorktree | null;

  constructor(
    rpcError: RpcError,
    facts: Readonly<{
      worktree: RegisteredWorktreeTopology;
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

const nonBlank = z.string().trim().min(1);
export const retainedPreviousWorktreeSchema: z.ZodType<RetainedPreviousWorktree> = z
  .object({ worktree: registeredWorktreeTopologySchema })
  .strict();
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
  if (!(error instanceof RpcError) || error.code !== rpcErrorCodes.worktreeSetupRetained) return null;
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

export function projectWorktreeFailure(method: DescMethod, failure: WorktreeFailure): RpcError {
  let classification;
  try {
    classification = classifyResultFailure(method.output, failure);
  } catch (cause) {
    throw new ContractError(
      `${operationName(method)} returned a malformed error outcome: ${cause instanceof Error ? cause.message : String(cause)}`,
    );
  }
  const generic = protobufRpcError(method, failure);
  if (classification.kind === "generic") return generic;
  const detail = projectKnownDetail(failure);
  const projected = new RpcError({
    code: generic.code,
    method: generic.method,
    data: generic.data,
    message: detail.message,
  });
  if (detail.setupRetained !== undefined) {
    return new WorktreeSetupRetainedError(projected, detail.setupRetained);
  }
  return detail.worktree === undefined ? projected : new WorktreeError(projected, detail.worktree);
}

function projectKnownDetail(failure: WorktreeFailure): Readonly<{
  message: string;
  worktree?: WorktreeErrorDetail;
  setupRetained?: Readonly<{
    worktree: RegisteredWorktreeTopology;
    scriptPath: string;
    diagnostic: string;
    retainedPreviousWorktree: RetainedPreviousWorktree | null;
  }>;
}> {
  const detail = failure.detail;
  return isGatewayFailureDetail(detail) ? projectGatewayDetail(detail) : projectDomainDetail(detail);
}

function projectDomainDetail(detail: DomainFailureDetail): Readonly<{
  message: string;
  worktree?: WorktreeErrorDetail;
  setupRetained?: Readonly<{
    worktree: RegisteredWorktreeTopology;
    scriptPath: string;
    diagnostic: string;
    retainedPreviousWorktree: RetainedPreviousWorktree | null;
  }>;
}> {
  switch (detail.case) {
    case "selectorError": {
      const value = detail.value;
      const reason = selectorReason(value.kind);
      return {
        message: `worktree selector error: ${reason}`,
        worktree: {
          kind: "selector",
          reason,
          input: value.input,
          candidates: value.candidates.map((candidate) => ({
            variant: topologyVariant(candidate.variant),
            selector: candidate.selector,
            branchName: candidate.branchName ?? null,
            displayName: candidate.displayName ?? null,
            fallbackIdentity: candidate.fallbackIdentity,
          })),
        },
      };
    }
    case "createFailed": {
      const value = detail.value;
      const owner = createOwner(value.owner);
      return {
        message: `worktree creation failed: ${value.diagnostic}`,
        worktree: { kind: "create", owner, diagnostic: value.diagnostic },
      };
    }
    case "transitionPending": {
      const value = detail.value;
      return {
        message: "a worktree transition is already pending for this session",
        worktree: {
          kind: "transition_pending",
          sessionID: value.sessionId,
          pendingOperationID: parseWorktreeOperationID(value.pendingOperationId),
        },
      };
    }
    case "immediateTransition": {
      const value = detail.value;
      return {
        message: value.diagnostic,
        worktree: { kind: "immediate_transition", reason: immediateReason(value.kind) },
      };
    }
    case "setupRetained": {
      const value = detail.value;
      const facts = setupRetained(value);
      return {
        message: `worktree setup failed after worktree creation: ${value.diagnostic}`,
        setupRetained: facts,
        worktree: { kind: "setup_retained", ...facts },
      };
    }
    case "deletePrecondition": {
      const value = detail.value;
      const cleanliness = projectWorktreeDirtyState(required(value.dirtyState));
      if (cleanliness.kind === "clean") throw new ContractError("Delete precondition cannot be clean.");
      return {
        message: deletePreconditionMessage(cleanliness),
        worktree: { kind: "delete_precondition", cleanliness },
      };
    }
    case "worktreeBlocked": {
      const value = detail.value;
      return { message: value.diagnostic, worktree: { kind: "blocked" } };
    }
  }
}

function isGatewayFailureDetail(detail: WorktreeFailureDetail): detail is GatewayFailureDetail {
  return (
    detail.case === "authRequired" ||
    detail.case === "workspaceNotRegistered" ||
    detail.case === "serverNotReady" ||
    detail.case === "internalFailure" ||
    detail.case === undefined
  );
}

function projectGatewayDetail(detail: GatewayFailureDetail): Readonly<{ message: string }> {
  switch (detail.case) {
    case "authRequired":
      return { message: "server auth is not configured" };
    case "workspaceNotRegistered":
      return { message: "workspace is not registered" };
    case "serverNotReady":
      return { message: `server not ready: ${serverNotReadyReason(detail.value.reason)}` };
    case "internalFailure":
      return { message: required(detail.value.cause) };
    case undefined:
      throw new ContractError("Known Worktree failure has no detail.");
  }
}

function setupRetained(value: SetupRetainedDetails) {
  return {
    worktree: projectRegisteredWorktree(required(value.worktree)),
    scriptPath: value.scriptPath,
    diagnostic: value.diagnostic,
    retainedPreviousWorktree: projectRetainedPreviousWorktree(value.retainedPreviousWorktree),
  };
}

function selectorReason(value: SelectorErrorKind): "not_found" | "ambiguous" | "unavailable" {
  switch (value) {
    case SelectorErrorKind.WORKTREE_SELECTOR_ERROR_KIND_NOT_FOUND:
      return "not_found";
    case SelectorErrorKind.WORKTREE_SELECTOR_ERROR_KIND_AMBIGUOUS:
      return "ambiguous";
    case SelectorErrorKind.WORKTREE_SELECTOR_ERROR_KIND_UNAVAILABLE:
      return "unavailable";
    case SelectorErrorKind.WORKTREE_SELECTOR_ERROR_KIND_UNSPECIFIED:
      throw new ContractError("Worktree selector error kind is unspecified.");
  }
}

function topologyVariant(value: TopologyVariant): "registered" | "external" | "missing" {
  switch (value) {
    case TopologyVariant.WORKTREE_TOPOLOGY_VARIANT_REGISTERED:
      return "registered";
    case TopologyVariant.WORKTREE_TOPOLOGY_VARIANT_EXTERNAL:
      return "external";
    case TopologyVariant.WORKTREE_TOPOLOGY_VARIANT_MISSING:
      return "missing";
    case TopologyVariant.WORKTREE_TOPOLOGY_VARIANT_UNSPECIFIED:
      throw new ContractError("Worktree topology variant is unspecified.");
  }
}

function createOwner(value: CreateErrorOwner): "base_ref" | "form" {
  switch (value) {
    case CreateErrorOwner.WORKTREE_CREATE_ERROR_OWNER_BASE_REF:
      return "base_ref";
    case CreateErrorOwner.WORKTREE_CREATE_ERROR_OWNER_FORM:
      return "form";
    case CreateErrorOwner.WORKTREE_CREATE_ERROR_OWNER_UNSPECIFIED:
      throw new ContractError("Worktree create error owner is unspecified.");
  }
}

function immediateReason(value: ImmediateTransitionErrorKind): "origin_inactive" | "apply_failed" {
  switch (value) {
    case ImmediateTransitionErrorKind.WORKTREE_IMMEDIATE_TRANSITION_ORIGIN_INACTIVE:
      return "origin_inactive";
    case ImmediateTransitionErrorKind.WORKTREE_IMMEDIATE_TRANSITION_APPLY_FAILED:
      return "apply_failed";
    case ImmediateTransitionErrorKind.WORKTREE_IMMEDIATE_TRANSITION_UNSPECIFIED:
      throw new ContractError("Worktree immediate transition kind is unspecified.");
  }
}

function serverNotReadyReason(value: ServerNotReadyReason): string {
  switch (value) {
    case ServerNotReadyReason.ONBOARDING_REQUIRED:
      return "onboarding_required";
    case ServerNotReadyReason.ACTIVATION_FAILED:
      return "activation_failed";
    case ServerNotReadyReason.UNSPECIFIED:
      return "unknown";
  }
}

function deletePreconditionMessage(cleanliness: Exclude<WorktreeCleanliness, { kind: "clean" }>): string {
  const base = "worktree deletion requires additional authorization";
  return cleanliness.kind === "dirty"
    ? `${base}: ${cleanliness.dirtyFileCount.toString()} modified or untracked file(s); force folder removal to continue`
    : `${base}: worktree cleanliness could not be determined: ${cleanliness.unknownCause}; force folder removal to continue`;
}

function required<Value>(value: Value | undefined): Value {
  if (value === undefined) throw new ContractError("Required Worktree failure fact is missing.");
  return value;
}
