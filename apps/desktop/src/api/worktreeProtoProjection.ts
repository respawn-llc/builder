import { ProjectAvailability } from "@app/server-api-contract/gen/kent/api/project/project_pb";
import {
  CreateTargetResolutionKind,
  DirtyStateKind,
  SwitchOperationKind,
  StatusProblemKind,
  type CreateTargetResolution,
  type DirtyState,
  type GitFacts,
  type KentFacts,
  type ListEntry,
  type RegisteredFacts,
  type RetainedPreviousWorktree as ProtoRetainedPreviousWorktree,
  type SessionExecutionTarget as ProtoSessionExecutionTarget,
  type StatusProblem,
  type StatusSuccess,
  type TopologyEntry,
} from "@app/server-api-contract/gen/kent/api/worktree/worktree_pb";

import { ContractError } from "./errors";
import {
  projectWorktreeCreateTargetResolution,
  projectWorktreeDeletePreview,
  projectWorktreeSwitch,
  type RegisteredWorktreeTopology,
  type RetainedPreviousWorktree,
  type SessionExecutionTarget,
  type WorktreeCleanliness,
  type WorktreeCreateTargetResolution,
  type WorktreeDeletePreview,
  type WorktreeListEntry,
  type WorktreeStatus,
  type WorktreeTopology,
} from "./schemas/worktree";

export function projectWorktreeStatus(value: StatusSuccess): WorktreeStatus {
  const target = required(value.target);
  const worktree = required(value.worktree);
  return {
    target: projectSessionExecutionTarget(target),
    worktree: {
      recordedRoot: worktree.recordedRoot,
      observedRoot: worktree.observedRoot ?? null,
      displayName: worktree.displayName ?? null,
      recordedBranchRef: worktree.recordedBranchRef ?? null,
      observedBranchRef: worktree.observedBranchRef ?? null,
    },
    problems: value.problems.map(projectStatusProblem),
  };
}

export function projectWorktreeTopology(value: TopologyEntry): WorktreeTopology {
  switch (value.topology.case) {
    case "registered":
      return projectRegisteredWorktree(value.topology.value);
    case "external":
      return { variant: "external", git: projectGit(value.topology.value.git) };
    case "missing":
      return { variant: "missing", kent: projectKent(value.topology.value.kent) };
    case undefined:
      throw contract("Worktree topology is missing.");
  }
}

export function projectRegisteredWorktree(value: RegisteredFacts): RegisteredWorktreeTopology {
  return { variant: "registered", git: projectGit(value.git), kent: projectKent(value.kent) };
}

export function projectRetainedPreviousWorktree(
  value: ProtoRetainedPreviousWorktree | undefined,
): RetainedPreviousWorktree | null {
  return value === undefined ? null : { worktree: projectRegisteredWorktree(required(value.worktree)) };
}

export function projectWorktreeListEntry(value: ListEntry): WorktreeListEntry {
  const projection = value.projection;
  if (value.topology === undefined || projection === undefined)
    throw contract("Worktree list entry is incomplete.");
  const switchOperation =
    projection.switch === undefined
      ? null
      : projectWorktreeSwitch(
          projection.switch.kind === SwitchOperationKind.WORKTREE_SWITCH_OPERATION_ENTER
            ? { kind: "enter", selector: required(projection.switch.selector) }
            : { kind: "leave", selector: null },
        );
  return {
    topology: projectWorktreeTopology(value.topology),
    selector: projection.selector,
    isCurrent: projection.isCurrent,
    switchOperation,
    deletePreviewOperation:
      projection.deletePreview === undefined ? null : { selector: projection.deletePreview.selector },
    fallbackIdentity: projection.fallbackIdentity ?? null,
  };
}

export function projectSessionExecutionTarget(value: ProtoSessionExecutionTarget): SessionExecutionTarget {
  return {
    workspaceID: value.workspaceId,
    workspaceName: value.workspaceName,
    workspaceRoot: value.workspaceRoot,
    workspaceAvailability: projectAvailability(value.workspaceAvailability),
    worktree:
      value.worktree === undefined
        ? null
        : {
            id: value.worktree.id,
            name: value.worktree.name,
            root: value.worktree.root,
            availability: projectAvailability(value.worktree.availability),
          },
    cwdRelpath: value.cwdRelpath,
    effectiveWorkdir: value.effectiveWorkdir,
  };
}

export function projectWorktreeDirtyState(value: DirtyState): WorktreeCleanliness {
  switch (value.kind) {
    case DirtyStateKind.DIRTY_STATE_CLEAN:
      return { kind: "clean" };
    case DirtyStateKind.DIRTY_STATE_DIRTY:
      return { kind: "dirty", dirtyFileCount: required(value.dirtyFileCount) };
    case DirtyStateKind.DIRTY_STATE_UNKNOWN:
      return { kind: "unknown", unknownCause: required(value.unknownCause) };
    case DirtyStateKind.DIRTY_STATE_UNSPECIFIED:
      throw contract("Worktree dirty state is unspecified.");
  }
}

export function projectDeletePreview(
  topology: TopologyEntry,
  deletionSelector: string,
  cleanliness: DirtyState,
): WorktreeDeletePreview {
  return projectWorktreeDeletePreview({
    topology: projectWorktreeTopology(topology),
    deletionSelector,
    cleanliness: projectWorktreeDirtyState(cleanliness),
  });
}

export function projectCreateTargetResolution(value: CreateTargetResolution): WorktreeCreateTargetResolution {
  switch (value.kind) {
    case CreateTargetResolutionKind.WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH:
      return projectWorktreeCreateTargetResolution({
        kind: "new_branch",
        input: value.input,
        resolvedRef: null,
      });
    case CreateTargetResolutionKind.WORKTREE_CREATE_TARGET_RESOLUTION_KIND_EXISTING_BRANCH:
      return projectWorktreeCreateTargetResolution({
        kind: "existing_branch",
        input: value.input,
        resolvedRef: required(value.resolvedRef),
      });
    case CreateTargetResolutionKind.WORKTREE_CREATE_TARGET_RESOLUTION_KIND_DETACHED_REF:
      return projectWorktreeCreateTargetResolution({
        kind: "detached_ref",
        input: value.input,
        resolvedRef: required(value.resolvedRef),
      });
    case CreateTargetResolutionKind.WORKTREE_CREATE_TARGET_RESOLUTION_KIND_UNSPECIFIED:
      throw contract("Worktree create target resolution is unspecified.");
  }
}

function projectGit(value: GitFacts | undefined) {
  if (value === undefined) throw contract("Worktree Git facts are missing.");
  return {
    canonicalRoot: value.canonicalRoot,
    headObject: value.headObject,
    branchRef: value.branchRef ?? null,
    branchName: value.branchName ?? null,
    detached: value.detached,
    bare: value.bare,
    lockedReason: value.lockedReason ?? null,
    prunableReason: value.prunableReason ?? null,
    isMain: value.isMain,
    pathAvailable: value.pathAvailable,
  };
}

function projectKent(value: KentFacts | undefined) {
  if (value === undefined) throw contract("Worktree Kent facts are missing.");
  return {
    worktreeID: value.worktreeId,
    canonicalRoot: value.canonicalRoot,
    displayName: value.displayName,
    managed: value.managed,
    createdBranch: value.createdBranch,
    originSessionID: value.originSessionId ?? null,
  };
}

function projectAvailability(value: ProjectAvailability): "available" | "missing" | "inaccessible" {
  switch (value) {
    case ProjectAvailability.AVAILABLE:
      return "available";
    case ProjectAvailability.MISSING:
      return "missing";
    case ProjectAvailability.INACCESSIBLE:
      return "inaccessible";
    case ProjectAvailability.UNSPECIFIED:
    case ProjectAvailability.UNLINKED:
      throw contract("Worktree availability is invalid.");
  }
}

function projectStatusProblem(value: StatusProblem): WorktreeStatus["problems"][number] {
  switch (value.kind) {
    case StatusProblemKind.WORKTREE_STATUS_PROBLEM_ROOT_MISSING:
      return { kind: "root_missing", root: required(value.root) };
    case StatusProblemKind.WORKTREE_STATUS_PROBLEM_ROOT_INACCESSIBLE:
      return { kind: "root_inaccessible", root: required(value.root) };
    case StatusProblemKind.WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISSING:
      return { kind: "git_binding_missing", root: required(value.root) };
    case StatusProblemKind.WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISMATCHED:
      return { kind: "git_binding_mismatched", root: required(value.root) };
    case StatusProblemKind.WORKTREE_STATUS_PROBLEM_RECORDED_REF_MISSING:
      return { kind: "recorded_ref_missing", ref: required(value.ref) };
    case StatusProblemKind.WORKTREE_STATUS_PROBLEM_UNSPECIFIED:
      throw contract("Worktree status problem is unspecified.");
  }
}

function required<Value>(value: Value | undefined): Value {
  if (value === undefined) throw contract("Required Worktree fact is missing.");
  return value;
}

function contract(message: string): ContractError {
  return new ContractError(message);
}
