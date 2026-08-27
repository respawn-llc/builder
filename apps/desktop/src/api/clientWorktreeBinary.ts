import { create, operationName, type DescMethod } from "@app/server-api-contract";
import {
  BranchCleanupMode,
  BranchCleanupOutcomeKind,
  CreateService,
  CreateTargetService,
  DeletePreviewService,
  ListService,
  SelectorService,
  StatusService,
  TransitionService,
  type BranchCleanupOutcome,
} from "@app/server-api-contract/gen/kent/api/worktree/worktree_pb";

import { ContractError } from "./errors";
import { parseWorktreeOperationID } from "./worktreeOperationID";
import {
  requireWorktreeAuthority,
  type WorktreeCreateInput,
  type WorktreeCreateResponse,
  type WorktreeDeleteConfirmationChoice,
  type WorktreeDeletePreview,
  type WorktreeDeleteResult,
  type WorktreeList,
  type WorktreeListEntry,
  type WorktreeSelectorResolution,
  type WorktreeStatus,
  type WorktreeSwitch,
} from "./schemas/worktree";
import type { DescriptorRpcTransport } from "./transport";
import { projectWorktreeFailure, type WorktreeFailure } from "./worktreeFailure";
import {
  projectCreateTargetResolution,
  projectDeletePreview,
  projectSessionExecutionTarget,
  projectWorktreeListEntry,
  projectWorktreeStatus,
} from "./worktreeProtoProjection";

export async function getBinaryWorktreeStatus(
  transport: DescriptorRpcTransport,
  sessionID: string,
): Promise<WorktreeStatus> {
  const method = StatusService.method.get;
  const success = requireWorktreeSuccess(
    method,
    (await transport.callDescriptor(method, create(method.input, { sessionId: sessionID }))).outcome,
  );
  return projectWorktreeStatus(success);
}

export async function listBinaryWorktrees(
  transport: DescriptorRpcTransport,
  sessionID: string,
): Promise<WorktreeList> {
  const method = ListService.method.list;
  const success = requireWorktreeSuccess(
    method,
    (await transport.callDescriptor(method, create(method.input, { sessionId: sessionID }))).outcome,
  );
  return {
    target: projectSessionExecutionTarget(required(success.target)),
    worktrees: success.worktrees.map(projectWorktreeListEntry),
  };
}

export async function listBinaryWorkspaceWorktrees(
  transport: DescriptorRpcTransport,
  projectID: string,
  workspaceID: string,
): Promise<Readonly<{ workspaceID: string; worktrees: readonly WorktreeListEntry[] }>> {
  const method = ListService.method.listWorkspace;
  const success = requireWorktreeSuccess(
    method,
    (
      await transport.callDescriptor(
        method,
        create(method.input, { projectId: projectID, workspaceId: workspaceID }),
      )
    ).outcome,
  );
  return {
    workspaceID: success.workspaceId,
    worktrees: success.worktrees.map(projectWorktreeListEntry),
  };
}

export async function resolveBinaryWorktreeSelector(
  transport: DescriptorRpcTransport,
  sessionID: string,
  selector: string,
): Promise<WorktreeSelectorResolution> {
  const method = SelectorService.method.resolve;
  const success = requireWorktreeSuccess(
    method,
    (await transport.callDescriptor(method, create(method.input, { sessionId: sessionID, selector })))
      .outcome,
  );
  return { worktree: projectWorktreeListEntry(required(success.worktree)) };
}

export async function resolveBinaryWorktreeCreateTarget(
  transport: DescriptorRpcTransport,
  sessionID: string,
  target: string,
) {
  const method = CreateTargetService.method.resolve;
  const success = requireWorktreeSuccess(
    method,
    (await transport.callDescriptor(method, create(method.input, { sessionId: sessionID, target }))).outcome,
  );
  return { resolution: projectCreateTargetResolution(required(success.resolution)) };
}

export async function previewBinaryWorktreeDelete(
  transport: DescriptorRpcTransport,
  sessionID: string,
  selector: string,
): Promise<WorktreeDeletePreview> {
  const method = DeletePreviewService.method.get;
  const success = requireWorktreeSuccess(
    method,
    (await transport.callDescriptor(method, create(method.input, { sessionId: sessionID, selector })))
      .outcome,
  );
  return projectDeletePreview(
    required(success.worktree),
    success.deletionSelector,
    required(success.cleanliness),
  );
}

export async function createBinaryWorktree(
  transport: DescriptorRpcTransport,
  input: WorktreeCreateInput,
): Promise<WorktreeCreateResponse> {
  const resolution = requireWorktreeAuthority(input.resolution, "create");
  if ((resolution.kind === "new_branch") !== (input.baseRef !== null)) {
    throw new TypeError("Worktree Create resolution and Base ref do not match.");
  }
  const method = CreateService.method.create;
  const success = requireWorktreeSuccess(
    method,
    (
      await transport.callDescriptor(
        method,
        create(method.input, {
          setupOperationId: input.setupOperationID.toJSONValue(),
          sessionId: input.sessionID,
          baseRef: resolution.kind === "new_branch" ? required(input.baseRef) : resolution.resolvedRef,
          createBranch: resolution.kind === "new_branch",
          ...(resolution.kind === "new_branch" ? { branchName: resolution.input } : {}),
        }),
        { timeoutMs: null },
      )
    ).outcome,
  );
  return {
    target: projectSessionExecutionTarget(required(success.target)),
    worktree: projectWorktreeListEntry(required(success.worktree)),
  };
}

export async function switchBinaryWorktree(
  transport: DescriptorRpcTransport,
  sessionID: string,
  operation: WorktreeSwitch,
) {
  const authority = requireWorktreeAuthority(operation, "switch");
  const operationID = parseWorktreeOperationID(crypto.randomUUID());
  const transition = { operationId: operationID.toJSONValue(), sessionId: sessionID };
  const method = authority.kind === "enter" ? TransitionService.method.enter : TransitionService.method.leave;
  const request =
    authority.kind === "enter"
      ? create(TransitionService.method.enter.input, { transition, selector: authority.selector })
      : create(TransitionService.method.leave.input, { transition });
  const result = await transport.callDescriptor(method, request);
  const acknowledgement = requireWorktreeSuccess(method, result.outcome);
  requireMatchingOperationID(operationID.toJSONValue(), acknowledgement.operationId);
  return { operationID };
}

export async function deleteBinaryWorktree(
  transport: DescriptorRpcTransport,
  sessionID: string,
  preview: WorktreeDeletePreview,
  confirmation: WorktreeDeleteConfirmationChoice,
): Promise<WorktreeDeleteResult> {
  const authority = requireWorktreeAuthority(preview, "delete");
  if (
    confirmation === "confirm_and_branch" &&
    (authority.topology.variant === "missing" || authority.topology.git.branchName === null)
  ) {
    throw new TypeError("Worktree Delete confirmation is invalid for this preview.");
  }
  const operationID = parseWorktreeOperationID(crypto.randomUUID());
  const method = TransitionService.method.delete;
  const success = requireWorktreeSuccess(
    method,
    (
      await transport.callDescriptor(
        method,
        create(method.input, {
          transition: { operationId: operationID.toJSONValue(), sessionId: sessionID },
          selector: authority.deletionSelector,
          forceFolderRemoval: authority.cleanliness.kind !== "clean",
          branchCleanupPolicy:
            confirmation === "confirm"
              ? BranchCleanupMode.WORKTREE_BRANCH_CLEANUP_MODE_AUTO_IF_KENT_CREATED
              : BranchCleanupMode.WORKTREE_BRANCH_CLEANUP_MODE_DELETE_SAFE,
        }),
      )
    ).outcome,
  );
  switch (success.result.case) {
    case "scheduled":
      requireMatchingOperationID(operationID.toJSONValue(), success.result.value.operationId);
      return {
        kind: "scheduled",
        acknowledgement: {
          operationID: parseWorktreeOperationID(success.result.value.operationId),
        },
      };
    case "completed": {
      const completed = success.result.value;
      return {
        kind: "completed",
        cleanup: projectCleanup(required(completed.cleanup)),
        leftoverRoot: completed.leftoverRoot ?? null,
      };
    }
    case undefined:
      throw new ContractError("Worktree Delete returned no result.");
  }
}

type WorktreeOutcome<Success> =
  | Readonly<{ case: "success"; value: Success | undefined }>
  | Readonly<{ case: "error"; value: WorktreeFailure }>
  | Readonly<{ case: undefined }>;

function requireWorktreeSuccess<Success>(method: DescMethod, outcome: WorktreeOutcome<Success>): Success {
  switch (outcome.case) {
    case "success":
      return required(outcome.value);
    case "error":
      throw projectWorktreeFailure(method, outcome.value);
    case undefined:
      throw new ContractError(`${operationName(method)} returned no outcome.`);
  }
}

function projectCleanup(
  value: BranchCleanupOutcome,
): Extract<WorktreeDeleteResult, { kind: "completed" }>["cleanup"] {
  switch (value.kind) {
    case BranchCleanupOutcomeKind.WORKTREE_BRANCH_CLEANUP_OUTCOME_NOT_REQUESTED:
      return { kind: "not_requested" };
    case BranchCleanupOutcomeKind.WORKTREE_BRANCH_CLEANUP_OUTCOME_NOT_APPLICABLE:
      return { kind: "not_applicable" };
    case BranchCleanupOutcomeKind.WORKTREE_BRANCH_CLEANUP_OUTCOME_DELETED:
      return { kind: "deleted", branchName: required(value.branchName) };
    case BranchCleanupOutcomeKind.WORKTREE_BRANCH_CLEANUP_OUTCOME_RETAINED:
      return {
        kind: "retained",
        branchName: required(value.branchName),
        diagnostic: value.diagnostic ?? null,
      };
    case BranchCleanupOutcomeKind.WORKTREE_BRANCH_CLEANUP_OUTCOME_UNSPECIFIED:
      throw new ContractError("Worktree branch cleanup outcome is unspecified.");
  }
}

function requireMatchingOperationID(expected: string, actual: string): void {
  if (actual !== expected) {
    throw new ContractError("Server returned a different Worktree operation identity.");
  }
}

function required<Value>(value: Value | null | undefined): Value {
  if (value === undefined || value === null) {
    throw new ContractError("Required Worktree response fact is missing.");
  }
  return value;
}
