import { FakeRpcTransport } from "@/test-support/api";
import { ApiClient } from "./client";
import {
  decodeWorktreeError,
  type JsonValue,
  newSetupOperationID,
  parseWorktreeOperationID,
  RpcError,
  rpcErrorCodes,
} from "./index";
import {
  worktreeCleanlinessSchema,
  worktreeCreateTargetResolutionResponseSchema,
  worktreeDeletePreviewResponseSchema,
  worktreeDeleteResultSchema,
  worktreeListEntrySchema,
  worktreeScheduledAcknowledgementSchema,
  worktreeStatusResponseSchema,
  worktreeTopologySchema,
} from "./schemas/worktree";
const ids = [
  "123e4567-e89b-42d3-a456-426614174000",
  "223e4567-e89b-42d3-a456-426614174000",
  "323e4567-e89b-42d3-a456-426614174000",
  "423e4567-e89b-42d3-a456-426614174000",
  "523e4567-e89b-42d3-a456-426614174000",
] as const;
const target = {
  WorkspaceID: "workspace-1",
  WorkspaceName: "Workspace",
  WorkspaceRoot: "/repo",
  WorkspaceAvailability: "available",
  Worktree: null,
  CwdRelpath: ".",
  EffectiveWorkdir: "/repo",
};
const git = {
  canonical_root: "/repo/feature",
  head_object: "abc123",
  branch_ref: "refs/heads/feature",
  branch_name: "feature",
  detached: false,
  bare: false,
  locked_reason: null,
  prunable_reason: null,
  is_main: false,
  path_available: true,
} as const;
const detachedGit = { ...git, branch_ref: null, branch_name: null, detached: true };
const kent = {
  worktree_id: "worktree-1",
  canonical_root: "/repo/feature",
  display_name: "feature",
  managed: true,
  created_branch: true,
  origin_session_id: null,
} as const;
const topology = { variant: "registered", registered: { git, kent } } as const;
const detachedTopology = {
  variant: "registered",
  registered: { git: detachedGit, kent },
} as const;
const unavailableTopology = {
  variant: "external",
  external: { git: { ...detachedGit, path_available: false } },
} as const;
const entry = {
  topology,
  projection: {
    selector: "feature",
    is_current: false,
    switch: { kind: "enter", selector: "feature" },
    delete_preview: { selector: "worktree-1" },
  },
} as const;
const resolution = (value: unknown) =>
  worktreeCreateTargetResolutionResponseSchema.parse({ resolution: value }).resolution;
const preview = (cleanliness: unknown, worktree: unknown = topology) =>
  worktreeDeletePreviewResponseSchema.parse({
    worktree,
    deletion_selector: "worktree-1",
    cleanliness,
  });
afterEach(() => vi.restoreAllMocks());
describe("Desktop Worktree client", () => {
  it("decodes exact Session reads and every topology/status fact", async () => {
    const topologies = [
      topology,
      detachedTopology,
      { variant: "external", external: { git } },
      unavailableTopology,
      { variant: "missing", missing: { kent } },
    ];
    const problems = [
      { kind: "root_missing", root: "/repo" },
      { kind: "root_inaccessible", root: "/repo" },
      { kind: "git_binding_missing", root: "/repo" },
      { kind: "git_binding_mismatched", root: "/repo" },
      { kind: "recorded_ref_missing", ref: "refs/heads/feature" },
    ];
    const transport = new FakeRpcTransport([
      {
        method: "worktree.status",
        result: { target, worktree: { recorded_root: "/repo" }, problems },
      },
      { method: "worktree.list", result: { target, worktrees: [entry] } },
    ]);
    const client = new ApiClient(transport);
    expect(topologies.every((value) => worktreeTopologySchema.safeParse(value).success)).toBe(true);
    expect(worktreeTopologySchema.parse(topologies[1])).toMatchObject({ git: { branchName: null } });
    expect(worktreeTopologySchema.parse(topologies[3])).toMatchObject({ git: { pathAvailable: false } });
    await expect(client.getWorktreeStatus("session-1")).resolves.toMatchObject({
      worktree: { recordedRoot: "/repo", observedRoot: null },
      problems,
    });
    await expect(client.listWorktrees("session-1")).resolves.toMatchObject({
      worktrees: [{ selector: "feature", switchOperation: { kind: "enter" } }],
    });
    expect(transport.calls.map(({ params }) => params)).toEqual([
      { session_id: "session-1" },
      { session_id: "session-1" },
    ]);
  });
  it("covers every mutation response, nullability, and malformed contract variant", () => {
    expect(newSetupOperationID().constructor).not.toBe(parseWorktreeOperationID(ids[0]).constructor);
    for (const cleanup of [
      { kind: "not_requested" },
      { kind: "not_applicable" },
      { kind: "deleted", branch_name: "feature" },
      { kind: "retained", branch_name: "feature" },
      { kind: "retained", branch_name: "feature", diagnostic: "still checked out" },
    ]) {
      const result = worktreeDeleteResultSchema.parse({
        kind: "completed",
        completed: { cleanup },
      });
      if (result.kind !== "completed") throw new Error("fixture was not completed");
      expect(result.leftoverRoot).toBeNull();
      if (result.cleanup.kind === "retained") {
        expect(result.cleanup.diagnostic).toBe(cleanup.diagnostic ?? null);
      }
    }
    const completed = worktreeDeleteResultSchema.parse({
      kind: "completed",
      completed: { cleanup: { kind: "not_requested" }, leftover_root: "/repo/feature" },
    });
    expect(completed.kind === "completed" ? completed.leftoverRoot : null).toBe("/repo/feature");
    expect(
      worktreeCleanlinessSchema.parse({ kind: "unknown", unknown_cause: " inspection failed " }),
    ).toEqual({ kind: "unknown", unknownCause: " inspection failed " });
    expectRejected(
      () => worktreeTopologySchema.parse({ ...topology, extra: true }),
      () =>
        worktreeTopologySchema.parse({
          variant: "external",
          external: { git: { ...git, branch_name: "" } },
        }),
      () =>
        worktreeStatusResponseSchema.parse({
          target,
          worktree: { recorded_root: "/repo", observed_root: null },
          problems: [],
        }),
      () => worktreeCleanlinessSchema.parse({ kind: "dirty", dirty_file_count: 0 }),
      () => worktreeCleanlinessSchema.parse({ kind: "unknown", unknown_cause: " " }),
      () =>
        worktreeCreateTargetResolutionResponseSchema.parse({
          resolution: { kind: "new_branch", input: "feature", resolved_ref: "x" },
        }),
      () =>
        worktreeCreateTargetResolutionResponseSchema.parse({
          resolution: { kind: "existing_branch", input: "feature" },
        }),
      () =>
        worktreeDeleteResultSchema.parse({
          kind: "completed",
          completed: { cleanup: { kind: "retained", branch_name: "feature", diagnostic: null } },
        }),
      () =>
        worktreeDeleteResultSchema.parse({
          kind: "completed",
          completed: { cleanup: { kind: "not_requested" }, leftover_root: "" },
        }),
      () => worktreeScheduledAcknowledgementSchema.parse({ operation_id: "invalid" }),
    );
  });
  it("maps every semantic mutation and rejects invalid authorities", async () => {
    const setupIDs = [newSetupOperationID(), newSetupOperationID(), newSetupOperationID()];
    const newBranch = resolution({ kind: "new_branch", input: "new" });
    const existing = resolution({
      kind: "existing_branch",
      input: "existing",
      resolved_ref: "refs/heads/existing",
    });
    const detached = resolution({ kind: "detached_ref", input: "abc123", resolved_ref: "abc123" });
    const enter = worktreeListEntrySchema.parse(entry).switchOperation;
    const leave = worktreeListEntrySchema.parse({
      ...entry,
      projection: { ...entry.projection, switch: { kind: "leave" } },
    }).switchOperation;
    const clean = preview({ kind: "clean" });
    const dirty = preview({ kind: "dirty", dirty_file_count: 2 });
    const unknown = preview({ kind: "unknown", unknown_cause: "inspection failed" });
    const branchless = preview({ kind: "clean" }, detachedTopology);
    const transport = new FakeRpcTransport([
      { method: "worktree.create", result: { target, worktree: entry } },
      { method: "worktree.enter", result: { operation_id: ids[0] } },
      { method: "worktree.leave", result: { operation_id: ids[1] } },
      {
        method: "worktree.delete",
        handler: (_params, index) => ({
          kind: "scheduled",
          scheduled: { operation_id: ids[index + 2] },
        }),
      },
    ]);
    const client = new ApiClient(transport);
    vi.spyOn(crypto, "randomUUID")
      .mockReturnValueOnce(ids[0])
      .mockReturnValueOnce(ids[1])
      .mockReturnValueOnce(ids[2])
      .mockReturnValueOnce(ids[3])
      .mockReturnValueOnce(ids[4]);
    const invokeCreate = async (
      authority: typeof existing,
      baseRef: string | null,
      setupOperationID = newSetupOperationID(),
    ) => client.createWorktree({ sessionID: "session-1", setupOperationID, resolution: authority, baseRef });
    await invokeCreate(existing, null, setupIDs[0]);
    await invokeCreate(newBranch, "HEAD", setupIDs[1]);
    await invokeCreate(detached, null, setupIDs[2]);
    if (enter === null) throw new Error("fixture omitted Enter authority");
    await client.switchWorktree("session-1", enter);
    if (leave === null) throw new Error("fixture omitted Leave authority");
    await client.switchWorktree("session-1", leave);
    await client.deleteWorktree("session-1", dirty, "confirm_and_branch");
    await client.deleteWorktree("session-1", clean, "confirm");
    await client.deleteWorktree("session-1", unknown, "confirm");

    const mutations = transport.calls;
    expect(mutations.slice(0, 3).map(({ params }) => params)).toMatchObject([
      { base_ref: "refs/heads/existing" },
      { base_ref: "HEAD", create_branch: true, branch_name: "new" },
      { base_ref: "abc123" },
    ]);
    expect(mutations.slice(0, 3).map(({ params }) => params)).toMatchObject(
      setupIDs.map((setupOperationID) => ({
        setup_operation_id: setupOperationID.toJSONValue(),
      })),
    );
    for (const { params } of mutations.slice(0, 3)) {
      expect(params).not.toHaveProperty("client_request_id");
    }
    expect(mutations.slice(0, 3).map(({ options }) => options)).toEqual([
      { timeoutMs: null },
      { timeoutMs: null },
      { timeoutMs: null },
    ]);
    expect(mutations[3]).toMatchObject({ method: "worktree.enter", params: { selector: "feature" } });
    expect(mutations[4]?.method).toBe("worktree.leave");
    expect(mutations.slice(5).map(({ params }) => params)).toMatchObject([
      { force_folder_removal: true, branch_cleanup_policy: "delete_safe" },
      { force_folder_removal: false, branch_cleanup_policy: "auto_if_kent_created" },
      { force_folder_removal: true, branch_cleanup_policy: "auto_if_kent_created" },
    ]);
    expect(Object.getPrototypeOf(newBranch)).not.toBe(Object.prototype);
    expect(Reflect.get(newBranch, "constructor")).toBeUndefined();
    if (branchless.topology.variant !== "registered") throw new Error("fixture was not registered");
    expect(Object.isFrozen(branchless.topology.git)).toBe(true);
    for (const [authority, baseRef] of [
      [newBranch, null],
      [existing, "HEAD"],
    ] as const) {
      await expect(invokeCreate(authority, baseRef)).rejects.toThrow(TypeError);
    }
    const callsBeforeForgery = transport.calls.length;
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
    await expect(
      Reflect.apply(client.deleteWorktree, client, ["s", branchless, "invalid"]),
    ).rejects.toThrow();
    expect(transport.calls).toHaveLength(callsBeforeForgery);
  });
  it("rejects acknowledgement mismatches and maps every typed error family", async () => {
    const operation = worktreeListEntrySchema.parse(entry).switchOperation;
    if (operation === null) throw new Error("fixture omitted Switch authority");
    vi.spyOn(crypto, "randomUUID").mockReturnValue(ids[0]);
    await expect(
      new ApiClient(
        new FakeRpcTransport([{ method: "worktree.enter", result: { operation_id: ids[1] } }]),
      ).switchWorktree("session-1", operation),
    ).rejects.toThrow("different Worktree operation identity");
    const selector = (kind: "not_found" | "ambiguous" | "unavailable") => ({
      type: "worktree_selector_error",
      kind,
      input: "feature",
      ...(kind === "ambiguous"
        ? { candidates: [{ variant: "external", selector: "feature", fallback_identity: "/repo/feature" }] }
        : {}),
    });
    const pending = {
      type: "worktree_transition_pending",
      session_id: "session-1",
      pending_operation_id: ids[0],
    };
    const retained = {
      type: "worktree_setup_retained",
      worktree: topology,
      script_path: "/source/setup.sh",
      diagnostic: "setup failed",
      retained_previous_worktree: null,
    };
    const immediate = (kind: string) => ({ type: "worktree_immediate_transition", kind });
    const precondition = (dirty_state: JsonValue) => ({
      type: "worktree_delete_precondition",
      dirty_state,
    });
    const cases = [
      [rpcErrorCodes.worktreeBlocked, undefined, "blocked"],
      [rpcErrorCodes.worktreeSelector, selector("not_found"), "selector"],
      [rpcErrorCodes.worktreeSelector, selector("ambiguous"), "selector"],
      [rpcErrorCodes.worktreeSelector, selector("unavailable"), "selector"],
      [rpcErrorCodes.worktreeCreate, { owner: "base_ref", diagnostic: "invalid" }, "create"],
      [rpcErrorCodes.worktreeCreate, { owner: "form", diagnostic: "invalid" }, "create"],
      [rpcErrorCodes.worktreeTransitionPending, pending, "transition_pending"],
      [rpcErrorCodes.worktreeImmediateTransition, immediate("origin_inactive"), "immediate_transition"],
      [rpcErrorCodes.worktreeImmediateTransition, immediate("apply_failed"), "immediate_transition"],
      [rpcErrorCodes.worktreeSetupRetained, retained, "setup_retained"],
      [
        rpcErrorCodes.worktreeDeletePrecondition,
        precondition({ kind: "dirty", dirty_file_count: 2 }),
        "delete_precondition",
      ],
      [
        rpcErrorCodes.worktreeDeletePrecondition,
        precondition({ kind: "unknown", unknown_cause: "failed" }),
        "delete_precondition",
      ],
    ] as const;
    for (const [code, data, kind] of cases) expect(errorDetail(code, data)?.kind).toBe(kind);
    for (const [code, data] of [
      [rpcErrorCodes.worktreeBlocked, {}],
      [rpcErrorCodes.worktreeSelector, { ...selector("ambiguous"), candidates: [] }],
      [rpcErrorCodes.worktreeCreate, { owner: "base_ref", diagnostic: "" }],
      [rpcErrorCodes.worktreeTransitionPending, { ...pending, pending_operation_id: "invalid" }],
      [rpcErrorCodes.worktreeImmediateTransition, immediate("invalid")],
      [
        rpcErrorCodes.worktreeSetupRetained,
        { ...retained, worktree: { variant: "external", external: { git } } },
      ],
      [rpcErrorCodes.worktreeDeletePrecondition, precondition({ kind: "clean" })],
    ] as const) {
      expect(errorDetail(code, data)).toBeNull();
    }
  });
});
function errorDetail(code: number, data?: JsonValue) {
  return (
    decodeWorktreeError(
      new RpcError({
        code,
        method: "worktree.test",
        message: "error",
        ...(data === undefined ? {} : { data }),
      }),
    )?.detail ?? null
  );
}
function expectRejected(...parsers: readonly (() => unknown)[]) {
  for (const parse of parsers) expect(parse).toThrow();
}
