import { FakeRpcTransport } from "@/test-support/api";
import { create, validate } from "@app/server-api-contract";
import { ProjectAvailability } from "@app/server-api-contract/gen/kent/api/project/project_pb";
import {
  BranchCleanupMode,
  BranchCleanupOutcomeKind,
  CreateResultSchema,
  CreateService,
  CreateTargetResolutionKind,
  CreateTargetResolveResultSchema,
  CreateTargetService,
  DeletePreviewResultSchema,
  DeletePreviewService,
  DeleteResultSchema,
  DirtyStateKind,
  EnterResultSchema,
  LeaveResultSchema,
  ListEntrySchema,
  ListResultSchema,
  ListService,
  SelectorResolveResultSchema,
  SelectorService,
  StatusProblemKind,
  StatusResultSchema,
  StatusService,
  SwitchOperationKind,
  TopologyEntrySchema,
  TransitionService,
  type ListEntry,
  type TopologyEntry,
} from "@app/server-api-contract/gen/kent/api/worktree/worktree_pb";

import { ApiClient } from "./client";
import { requireWorktreeSuccess, WorktreeError } from "./clientWorktree";
import { newSetupOperationID } from "./index";

const ids = ["123e4567-e89b-42d3-a456-426614174000", "223e4567-e89b-42d3-a456-426614174000"] as const;

const target = {
  workspaceId: "workspace-1",
  workspaceName: "Workspace",
  workspaceRoot: "/repo",
  workspaceAvailability: ProjectAvailability.AVAILABLE,
  cwdRelpath: ".",
  effectiveWorkdir: "/repo",
} as const;
const git = {
  canonicalRoot: "/repo/feature",
  headObject: "abc123",
  branchRef: "refs/heads/feature",
  branchName: "feature",
  detached: false,
  bare: false,
  isMain: false,
  pathAvailable: true,
} as const;
const detachedGit = {
  ...git,
  branchRef: undefined,
  branchName: undefined,
  detached: true,
} as const;
const kent = {
  worktreeId: "worktree-1",
  canonicalRoot: "/repo/feature",
  displayName: "feature",
  managed: true,
  createdBranch: true,
} as const;
const topology = create(TopologyEntrySchema, {
  topology: { case: "registered", value: { git, kent } },
});
const detachedTopology = create(TopologyEntrySchema, {
  topology: { case: "registered", value: { git: detachedGit, kent } },
});
const unavailableTopology = create(TopologyEntrySchema, {
  topology: {
    case: "external",
    value: { git: { ...detachedGit, pathAvailable: false } },
  },
});
const entry = create(ListEntrySchema, {
  topology,
  projection: {
    selector: "feature",
    isCurrent: false,
    switch: {
      kind: SwitchOperationKind.WORKTREE_SWITCH_OPERATION_ENTER,
      selector: "feature",
    },
    deletePreview: { selector: "worktree-1" },
  },
});

afterEach(() => vi.restoreAllMocks());

describe("Desktop Worktree client", () => {
  it("decodes exact Session reads and every topology/status fact", async () => {
    const topologies = [
      topology,
      detachedTopology,
      create(TopologyEntrySchema, { topology: { case: "external", value: { git } } }),
      unavailableTopology,
      create(TopologyEntrySchema, { topology: { case: "missing", value: { kent } } }),
    ] as const;
    const problems = [
      { kind: StatusProblemKind.WORKTREE_STATUS_PROBLEM_ROOT_MISSING, root: "/repo" },
      { kind: StatusProblemKind.WORKTREE_STATUS_PROBLEM_ROOT_INACCESSIBLE, root: "/repo" },
      { kind: StatusProblemKind.WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISSING, root: "/repo" },
      { kind: StatusProblemKind.WORKTREE_STATUS_PROBLEM_GIT_BINDING_MISMATCHED, root: "/repo" },
      {
        kind: StatusProblemKind.WORKTREE_STATUS_PROBLEM_RECORDED_REF_MISSING,
        ref: "refs/heads/feature",
      },
    ] as const;
    const transport = new FakeRpcTransport([
      {
        descriptor: StatusService.method.get,
        result: create(StatusResultSchema, {
          outcome: {
            case: "success",
            value: {
              target,
              worktree: { recordedRoot: "/repo" },
              problems: [...problems],
            },
          },
        }),
      },
      {
        descriptor: ListService.method.list,
        result: create(ListResultSchema, {
          outcome: { case: "success", value: { target, worktrees: [entry] } },
        }),
      },
    ]);
    const client = new ApiClient(transport);

    expect(topologies.every(isValidTopology)).toBe(true);
    const detached = topologies[1];
    if (detached.topology.case !== "registered") throw new Error("fixture was not registered");
    expect(detached.topology.value.git).not.toHaveProperty("branchName");
    expect(topologies[3]).toMatchObject({
      topology: { value: { git: { pathAvailable: false } } },
    });
    await expect(client.getWorktreeStatus("session-1")).resolves.toMatchObject({
      worktree: { recordedRoot: "/repo" },
      problems,
    });
    await expect(client.listWorktrees("session-1")).resolves.toMatchObject({
      worktrees: [
        {
          projection: {
            selector: "feature",
            switch: { kind: SwitchOperationKind.WORKTREE_SWITCH_OPERATION_ENTER },
          },
        },
      ],
    });
    expect(transport.descriptorCalls.map(({ request }) => request)).toEqual([
      create(StatusService.method.get.input, { sessionId: "session-1" }),
      create(ListService.method.list.input, { sessionId: "session-1" }),
    ]);
  });

  it("maps every semantic mutation and rejects invalid authorities", async () => {
    const setupIDs = [newSetupOperationID(), newSetupOperationID(), newSetupOperationID()];
    const existing = await resolveTarget(
      CreateTargetResolutionKind.WORKTREE_CREATE_TARGET_RESOLUTION_KIND_EXISTING_BRANCH,
      "existing",
      "refs/heads/existing",
    );
    const newBranch = await resolveTarget(
      CreateTargetResolutionKind.WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH,
      "new",
    );
    const detached = await resolveTarget(
      CreateTargetResolutionKind.WORKTREE_CREATE_TARGET_RESOLUTION_KIND_DETACHED_REF,
      "abc123",
      "abc123",
    );
    const enter = await resolveSwitch(entry);
    const leave = await resolveSwitch(
      create(ListEntrySchema, {
        topology: {
          topology: {
            case: "registered",
            value: {
              git: {
                ...git,
                canonicalRoot: "/repo",
                branchRef: "refs/heads/main",
                branchName: "main",
                isMain: true,
              },
              kent: { ...kent, worktreeId: "main", canonicalRoot: "/repo" },
            },
          },
        },
        projection: {
          selector: "main",
          isCurrent: false,
          switch: { kind: SwitchOperationKind.WORKTREE_SWITCH_OPERATION_LEAVE_MAIN },
        },
      }),
    );
    const clean = await resolvePreview(DirtyStateKind.DIRTY_STATE_CLEAN, topology);
    const dirty = await resolvePreview(DirtyStateKind.DIRTY_STATE_DIRTY, topology, {
      dirtyFileCount: 2,
    });
    const unknown = await resolvePreview(DirtyStateKind.DIRTY_STATE_UNKNOWN, topology, {
      unknownCause: "inspection failed",
    });
    const branchless = await resolvePreview(DirtyStateKind.DIRTY_STATE_CLEAN, detachedTopology);
    const transport = new FakeRpcTransport([
      {
        descriptor: CreateService.method.create,
        result: create(CreateResultSchema, {
          outcome: { case: "success", value: { target, worktree: entry } },
        }),
      },
      {
        descriptor: TransitionService.method.enter,
        result: create(EnterResultSchema, {
          outcome: { case: "success", value: { operationId: ids[0] } },
        }),
      },
      {
        descriptor: TransitionService.method.leave,
        result: create(LeaveResultSchema, {
          outcome: { case: "success", value: { operationId: ids[1] } },
        }),
      },
      {
        descriptor: TransitionService.method.delete,
        result: create(DeleteResultSchema, {
          outcome: {
            case: "success",
            value: {
              cleanup: {
                kind: BranchCleanupOutcomeKind.WORKTREE_BRANCH_CLEANUP_OUTCOME_NOT_REQUESTED,
              },
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);
    vi.spyOn(crypto, "randomUUID").mockReturnValueOnce(ids[0]).mockReturnValueOnce(ids[1]);
    const invokeCreate = async (
      resolution: typeof existing,
      baseRef: string | null,
      setupOperationID = newSetupOperationID(),
    ) => client.createWorktree({ sessionID: "session-1", setupOperationID, resolution, baseRef });

    await invokeCreate(existing, null, setupIDs[0]);
    await invokeCreate(newBranch, "HEAD", setupIDs[1]);
    await invokeCreate(detached, null, setupIDs[2]);
    await client.switchWorktree("session-1", enter);
    await client.switchWorktree("session-1", leave);
    await client.deleteWorktree("session-1", dirty, "confirm_and_branch");
    await client.deleteWorktree("session-1", clean, "confirm");
    await client.deleteWorktree("session-1", unknown, "confirm");

    const mutations = transport.descriptorCalls;
    expect(mutations.slice(0, 3).map(({ request }) => request)).toMatchObject([
      { spec: { baseRef: "refs/heads/existing", createBranch: false } },
      { spec: { baseRef: "HEAD", createBranch: true, branchName: "new" } },
      { spec: { baseRef: "abc123", createBranch: false } },
    ]);
    expect(mutations.slice(0, 3).map(({ request }) => request)).toMatchObject(
      setupIDs.map((setupOperationID) => ({
        setupOperationId: setupOperationID.toJSONValue(),
      })),
    );
    for (const { request } of mutations.slice(0, 3)) {
      expect(request).not.toHaveProperty("clientRequestId");
    }
    expect(mutations.slice(0, 3).map(({ options }) => options)).toEqual([
      { timeoutMs: null },
      { timeoutMs: null },
      { timeoutMs: null },
    ]);
    expect(mutations[3]).toMatchObject({
      descriptor: TransitionService.method.enter,
      request: { selector: "feature" },
    });
    expect(mutations[4]?.descriptor).toBe(TransitionService.method.leave);
    expect(mutations.slice(5).map(({ request }) => request)).toMatchObject([
      {
        forceFolderRemoval: true,
        branchCleanupPolicy: BranchCleanupMode.WORKTREE_BRANCH_CLEANUP_MODE_DELETE_SAFE,
      },
      {
        forceFolderRemoval: false,
        branchCleanupPolicy: BranchCleanupMode.WORKTREE_BRANCH_CLEANUP_MODE_AUTO_IF_KENT_CREATED,
      },
      {
        forceFolderRemoval: true,
        branchCleanupPolicy: BranchCleanupMode.WORKTREE_BRANCH_CLEANUP_MODE_AUTO_IF_KENT_CREATED,
      },
    ]);
    for (const { request } of mutations.slice(5)) {
      expect(request).not.toHaveProperty("operationId");
    }
    if (branchless.worktree?.topology.case !== "registered") {
      throw new Error("fixture was not registered");
    }
    expect(Object.isFrozen(branchless.worktree.topology.value.git)).toBe(true);
    for (const [authority, baseRef] of [
      [newBranch, null],
      [existing, "HEAD"],
    ] as const) {
      await expect(invokeCreate(authority, baseRef)).rejects.toThrow(TypeError);
    }
    const callsBeforeForgery = transport.descriptorCalls.length;
    await expect(
      Reflect.apply(client.createWorktree, client, [
        {
          sessionID: "s",
          setupOperationID: newSetupOperationID(),
          resolution: { ...newBranch },
          baseRef: "HEAD",
        },
      ]),
    ).rejects.toThrow("decoded authority");
    await expect(client.deleteWorktree("s", branchless, "confirm_and_branch")).rejects.toThrow(TypeError);
    expect(transport.descriptorCalls).toHaveLength(callsBeforeForgery);
  });

  it("rejects acknowledgement mismatches and maps every typed error family", async () => {
    const operation = await resolveSwitch(entry);
    vi.spyOn(crypto, "randomUUID").mockReturnValue(ids[0]);

    await expect(
      new ApiClient(
        new FakeRpcTransport([
          {
            descriptor: TransitionService.method.enter,
            result: create(EnterResultSchema, {
              outcome: { case: "success", value: { operationId: ids[1] } },
            }),
          },
        ]),
      ).switchWorktree("session-1", operation),
    ).rejects.toThrow("different Worktree operation identity");

    for (const [method, result] of [
      [
        TransitionService.method.enter,
        create(EnterResultSchema, {
          outcome: {
            case: "error",
            value: {
              code: "pending_work_capacity",
              detail: { case: "pendingWorkCapacity", value: {} },
            },
          },
        }),
      ],
      [
        TransitionService.method.leave,
        create(LeaveResultSchema, {
          outcome: {
            case: "error",
            value: {
              code: "pending_work_capacity",
              detail: { case: "pendingWorkCapacity", value: {} },
            },
          },
        }),
      ],
    ] as const) {
      try {
        requireWorktreeSuccess(method, result);
        throw new Error("expected Pending Work capacity rejection");
      } catch (error) {
        expect(error).toBeInstanceOf(WorktreeError);
        if (!(error instanceof WorktreeError)) throw error;
        expect(error.detail).toEqual({ kind: "capacity" });
      }
    }
  });
});

function isValidTopology(value: TopologyEntry) {
  try {
    validate(TopologyEntrySchema, value);
    return true;
  } catch {
    return false;
  }
}

async function resolveTarget(kind: CreateTargetResolutionKind, input: string, resolvedRef?: string) {
  const client = new ApiClient(
    new FakeRpcTransport([
      {
        descriptor: CreateTargetService.method.resolve,
        result: create(CreateTargetResolveResultSchema, {
          outcome: {
            case: "success",
            value: {
              resolution: {
                kind,
                input,
                ...(resolvedRef === undefined ? {} : { resolvedRef }),
              },
            },
          },
        }),
      },
    ]),
  );
  const resolution = (await client.resolveWorktreeCreateTarget("session-1", input)).resolution;
  if (resolution === undefined) throw new Error("fixture omitted Create target resolution");
  return resolution;
}

async function resolveSwitch(value: ListEntry) {
  const client = new ApiClient(
    new FakeRpcTransport([
      {
        descriptor: SelectorService.method.resolve,
        result: create(SelectorResolveResultSchema, {
          outcome: { case: "success", value: { worktree: value } },
        }),
      },
    ]),
  );
  const operation = (await client.resolveWorktreeSelector("session-1", "feature")).worktree?.projection
    ?.switch;
  if (operation === undefined) throw new Error("fixture omitted Switch authority");
  return operation;
}

async function resolvePreview(
  kind: DirtyStateKind,
  worktree: TopologyEntry,
  details: Readonly<{ dirtyFileCount?: number; unknownCause?: string }> = {},
) {
  const client = new ApiClient(
    new FakeRpcTransport([
      {
        descriptor: DeletePreviewService.method.get,
        result: create(DeletePreviewResultSchema, {
          outcome: {
            case: "success",
            value: {
              worktree,
              deletionSelector: "worktree-1",
              cleanliness: { kind, ...details },
            },
          },
        }),
      },
    ]),
  );
  return client.previewWorktreeDelete("session-1", "feature");
}
