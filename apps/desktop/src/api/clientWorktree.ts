import { create, operationName, type DescMethod } from "@app/server-api-contract";
import {
  BranchCleanupMode,
  CreateErrorOwner,
  CreateService,
  CreateTargetResolutionKind,
  CreateTargetService,
  DeletePreviewService,
  DirtyStateKind,
  ListService,
  SelectorService,
  StatusService,
  SwitchOperationKind,
  TransitionService,
  type CreateError,
  type CreateSuccess,
  type CreateTargetResolveError,
  type CreateTargetResolveSuccess,
  type DeleteError,
  type DeletePreviewError,
  type DeletePreviewSuccess,
  type DeleteSuccess,
  type EnterError,
  type LeaveError,
  type ListError,
  type ListSuccess,
  type ScheduledAcknowledgement,
  type SelectorResolveError,
  type SelectorResolveSuccess,
  type SetupStartError,
  type StatusError,
  type StatusSuccess,
  type WorkspaceListError,
} from "@app/server-api-contract/gen/kent/api/worktree/worktree_pb";

import { ContractError, RpcError } from "./errors";
import { protobufRpcError } from "./protobufRpc";
import {
  authorizeWorktreeCreateTargetResolution,
  authorizeWorktreeDeletePreview,
  authorizeWorktreeListEntry,
  requireWorktreeAuthority,
  type WorktreeCreateInput,
  type WorktreeDeleteConfirmationChoice,
  type WorktreeDeletePreview,
  type WorktreeSwitch,
} from "./schemas/worktree";
import type { DescriptorRpcTransport } from "./transport";

export async function getWorktreeStatus(
  transport: DescriptorRpcTransport,
  sessionID: string,
): Promise<StatusSuccess> {
  const method = StatusService.method.get;
  return requireWorktreeSuccess(
    method,
    await transport.callDescriptor(method, create(method.input, { sessionId: sessionID })),
  );
}

export async function listWorktrees(
  transport: DescriptorRpcTransport,
  sessionID: string,
): Promise<ListSuccess> {
  const method = ListService.method.list;
  const success = requireWorktreeSuccess(
    method,
    await transport.callDescriptor(method, create(method.input, { sessionId: sessionID })),
  );
  success.worktrees.forEach(authorizeWorktreeListEntry);
  return success;
}

export async function resolveWorktreeSelector(
  transport: DescriptorRpcTransport,
  sessionID: string,
  selector: string,
): Promise<SelectorResolveSuccess> {
  const method = SelectorService.method.resolve;
  const success = requireWorktreeSuccess(
    method,
    await transport.callDescriptor(method, create(method.input, { sessionId: sessionID, selector })),
  );
  if (success.worktree !== undefined) authorizeWorktreeListEntry(success.worktree);
  return success;
}

export async function resolveWorktreeCreateTarget(
  transport: DescriptorRpcTransport,
  sessionID: string,
  target: string,
): Promise<CreateTargetResolveSuccess> {
  const method = CreateTargetService.method.resolve;
  const success = requireWorktreeSuccess(
    method,
    await transport.callDescriptor(method, create(method.input, { sessionId: sessionID, target })),
  );
  authorizeWorktreeCreateTargetResolution(required(success.resolution));
  return success;
}

export async function previewWorktreeDelete(
  transport: DescriptorRpcTransport,
  sessionID: string,
  selector: string,
): Promise<WorktreeDeletePreview> {
  const method = DeletePreviewService.method.get;
  return authorizeWorktreeDeletePreview(
    requireWorktreeSuccess(
      method,
      await transport.callDescriptor(method, create(method.input, { sessionId: sessionID, selector })),
    ),
  );
}

export async function createWorktree(
  transport: DescriptorRpcTransport,
  input: WorktreeCreateInput,
): Promise<CreateSuccess> {
  const resolution = requireWorktreeAuthority(input.resolution, "create");
  const createBranch =
    resolution.kind === CreateTargetResolutionKind.WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH;
  if (createBranch !== (input.baseRef !== null)) {
    throw new TypeError("Worktree Create resolution and Base ref do not match.");
  }
  const method = CreateService.method.create;
  const success = requireWorktreeSuccess(
    method,
    await transport.callDescriptor(
      method,
      create(method.input, {
        setupOperationId: input.setupOperationID.toJSONValue(),
        sessionId: input.sessionID,
        spec: {
          baseRef: createBranch ? required(input.baseRef) : required(resolution.resolvedRef),
          createBranch,
          ...(createBranch ? { branchName: resolution.input } : {}),
        },
      }),
      { timeoutMs: null },
    ),
  );
  if (success.worktree !== undefined) authorizeWorktreeListEntry(success.worktree);
  return success;
}

export async function switchWorktree(
  transport: DescriptorRpcTransport,
  sessionID: string,
  operation: WorktreeSwitch,
): Promise<ScheduledAcknowledgement> {
  const authority = requireWorktreeAuthority(operation, "switch");
  const operationID = crypto.randomUUID();
  const enter = authority.kind === SwitchOperationKind.WORKTREE_SWITCH_OPERATION_ENTER;
  const method = enter ? TransitionService.method.enter : TransitionService.method.leave;
  const result = enter
    ? await transport.callDescriptor(
        TransitionService.method.enter,
        create(TransitionService.method.enter.input, {
          operationId: operationID,
          sessionId: sessionID,
          selector: required(authority.selector),
        }),
      )
    : await transport.callDescriptor(
        TransitionService.method.leave,
        create(TransitionService.method.leave.input, { operationId: operationID, sessionId: sessionID }),
      );
  const acknowledgement = requireWorktreeSuccess(method, result);
  if (acknowledgement.operationId !== operationID) {
    throw new ContractError("Server returned a different Worktree operation identity.");
  }
  return acknowledgement;
}

export async function deleteWorktree(
  transport: DescriptorRpcTransport,
  sessionID: string,
  preview: WorktreeDeletePreview,
  confirmation: WorktreeDeleteConfirmationChoice,
): Promise<DeleteSuccess> {
  const authority = requireWorktreeAuthority(preview, "delete");
  if (confirmation === "confirm_and_branch" && !hasDeletableBranch(authority)) {
    throw new TypeError("Worktree Delete confirmation is invalid for this preview.");
  }
  const method = TransitionService.method.delete;
  return requireWorktreeSuccess(
    method,
    await transport.callDescriptor(
      method,
      create(method.input, {
        sessionId: sessionID,
        selector: authority.deletionSelector,
        forceFolderRemoval: required(authority.cleanliness).kind !== DirtyStateKind.DIRTY_STATE_CLEAN,
        branchCleanupPolicy:
          confirmation === "confirm"
            ? BranchCleanupMode.WORKTREE_BRANCH_CLEANUP_MODE_AUTO_IF_KENT_CREATED
            : BranchCleanupMode.WORKTREE_BRANCH_CLEANUP_MODE_DELETE_SAFE,
      }),
    ),
  );
}

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

export type WorktreeErrorDetail =
  | Readonly<{ kind: "selector"; details: Extract<WorktreeFailure["detail"], { case: "selectorError" }>["value"] }>
  | Readonly<{ kind: "create"; owner: "base_ref" | "form"; diagnostic: string }>
  | Readonly<{ kind: "setup_retained"; details: Extract<CreateError["detail"], { case: "setupRetained" }>["value"] }>
  | Readonly<{ kind: "delete_precondition"; details: Extract<DeleteError["detail"], { case: "deletePrecondition" }>["value"] }>
  | Readonly<{ kind: "blocked" }>;

export class WorktreeError extends RpcError {
  constructor(
    rpcError: RpcError,
    readonly detail: WorktreeErrorDetail,
  ) {
    super(rpcError);
    this.name = "WorktreeError";
  }
}

type WorktreeResult<Success, Failure extends WorktreeFailure> = Readonly<{
  outcome:
    | Readonly<{ case: "success"; value: Success | undefined }>
    | Readonly<{ case: "error"; value: Failure }>
    | Readonly<{ case: undefined; value?: undefined }>;
}>;

export function requireWorktreeSuccess<Success, Failure extends WorktreeFailure>(
  method: DescMethod,
  result: WorktreeResult<Success, Failure>,
): Success {
  switch (result.outcome.case) {
    case "success":
      return required(result.outcome.value);
    case "error":
      throw projectWorktreeFailure(method, result.outcome.value);
    case undefined:
      throw new ContractError(`${operationName(method)} returned no outcome.`);
  }
}

function projectWorktreeFailure(method: DescMethod, failure: WorktreeFailure): RpcError {
  const generic = protobufRpcError(method, failure);
  switch (failure.detail.case) {
    case "selectorError":
      return new WorktreeError(generic, { kind: "selector", details: failure.detail.value });
    case "createFailed":
      return new WorktreeError(generic, {
        kind: "create",
        owner:
          failure.detail.value.owner === CreateErrorOwner.WORKTREE_CREATE_ERROR_OWNER_BASE_REF
            ? "base_ref"
            : "form",
        diagnostic: failure.detail.value.diagnostic,
      });
    case "setupRetained":
      return new WorktreeError(generic, { kind: "setup_retained", details: failure.detail.value });
    case "deletePrecondition":
      return new WorktreeError(generic, { kind: "delete_precondition", details: failure.detail.value });
    case "worktreeBlocked":
      return new WorktreeError(generic, { kind: "blocked" });
    case "authRequired":
    case "workspaceNotRegistered":
    case "serverNotReady":
    case "internalFailure":
    case undefined:
      return generic;
  }
}

function hasDeletableBranch(preview: WorktreeDeletePreview): boolean {
  const topology = required(preview.worktree).topology;
  switch (topology.case) {
    case "registered":
    case "external":
      return topology.value.git?.branchName !== undefined;
    case "missing":
    case undefined:
      return false;
  }
}

function required<Value>(value: Value | null | undefined): Value {
  if (value === undefined || value === null) {
    throw new ContractError("Required Worktree fact is missing.");
  }
  return value;
}
