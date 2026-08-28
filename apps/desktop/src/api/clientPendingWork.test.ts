import { FakeRpcTransport } from "@/test-support/api";

import { ApiClient } from "./client";
import { decodePendingWorkError, PendingWorkError } from "./clientPendingWork";
import { RpcError } from "./errors";
import type { JsonValue } from "./json";
import { rpcErrorCodes } from "./rpcErrorCodes";
import { parseWorktreeOperationID } from "./worktreeOperationID";
import {
  pendingWorkRestorationSchema,
  pendingWorkSchema,
  pendingWorkTechnicalRestorationEventSchema,
  sessionSettingFeedbackSchema,
} from "./pendingWork";

const ids = [
  "123e4567-e89b-42d3-a456-426614174000",
  "223e4567-e89b-42d3-a456-426614174000",
  "323e4567-e89b-42d3-a456-426614174000",
  "423e4567-e89b-42d3-a456-426614174000",
] as const;

describe("Desktop Pending Work client", () => {
  it("decodes the closed Pending Work variants in server order", () => {
    const pendingWork = pendingWorkSchema.parse({
      items: [
        {
          id: ids[0],
          lane: "queue",
          kind: "message",
          state: "pending",
          canonical_input: "follow up",
          message: { text: "follow up" },
        },
        {
          id: ids[1],
          lane: "steer",
          kind: "manual_compaction",
          state: "pending",
          canonical_input: "/compact keep the API decisions",
          manual_compaction: { guidance: "keep the API decisions" },
        },
        {
          id: ids[2],
          lane: "steer",
          kind: "worktree_transition",
          state: "pending",
          canonical_input: "/wt switch feature",
          worktree_transition: { transition: "enter", selector: "feature" },
        },
        {
          id: ids[3],
          lane: "steer",
          kind: "worktree_transition",
          state: "pending",
          canonical_input: "/wt leave",
          worktree_transition: { transition: "leave" },
        },
      ],
    });

    expect(pendingWork.items.map((item) => item.kind)).toEqual([
      "message",
      "manual_compaction",
      "worktree_transition",
      "worktree_transition",
    ]);
    const worktree = pendingWork.items[2];
    expect(worktree?.kind).toBe("worktree_transition");
    if (worktree?.kind !== "worktree_transition") throw new Error("fixture omitted Worktree item");
    expect(worktree.id.toJSONValue()).toBe(parseWorktreeOperationID(ids[2]).toJSONValue());
  });

  it("decodes typed restorations and every setting-feedback variant", () => {
    expect(
      pendingWorkRestorationSchema.parse({
        kind: "worktree_transition",
        canonical_input: "/wt leave",
      }),
    ).toEqual({ kind: "worktree_transition", canonicalInput: "/wt leave" });
    const technical = pendingWorkTechnicalRestorationEventSchema.parse({
      Restoration: {
        item_id: ids[1],
        kind: "manual_compaction",
        canonical_input: "/compact",
      },
    });
    expect(technical.restoration.itemID.toJSONValue()).toBe(ids[1]);

    const base = {
      Changed: true,
      SessionName: null,
      Thinking: null,
      FastMode: null,
      Supervisor: null,
      Questions: null,
      AutoCompaction: null,
    };
    const cases = [
      [{ ...base, Kind: "session_name", SessionName: "Kent API" }, "Kent API"],
      [{ ...base, Kind: "thinking", Thinking: "high" }, "high"],
      [{ ...base, Kind: "fast_mode", FastMode: true }, true],
      [{ ...base, Kind: "supervisor", Supervisor: "edits" }, "edits"],
      [{ ...base, Kind: "questions", Questions: false }, false],
      [{ ...base, Kind: "auto_compaction", AutoCompaction: true }, true],
    ] as const;
    for (const [wire, value] of cases) {
      expect(sessionSettingFeedbackSchema.parse(wire).value).toBe(value);
    }
  });

  it("preserves exact human input while rejecting incoherent payloads and ordering", () => {
    const exact = "  follow up exactly  ";
    const message = pendingWorkSchema.parse({
      items: [
        {
          id: ids[0],
          lane: "queue",
          kind: "message",
          state: "pending",
          canonical_input: exact,
          message: { text: exact },
        },
      ],
    });
    expect(message.items[0]?.canonicalInput).toBe(exact);

    const validMessage = {
      id: ids[0],
      lane: "queue",
      kind: "message",
      state: "pending",
      canonical_input: "message",
      message: { text: "message" },
    };
    const invalidCollections = [
      { items: [{ ...validMessage, id: "not-a-uuid" }] },
      { items: [{ ...validMessage, id: "123e4567-e89b-12d3-a456-426614174000" }] },
      { items: [{ ...validMessage, canonical_input: "different" }] },
      {
        items: [
          { ...validMessage, lane: "steer" },
          { ...validMessage, id: ids[1] },
        ],
      },
      {
        items: [
          {
            ...validMessage,
            manual_compaction: {},
          },
        ],
      },
    ];
    for (const invalid of invalidCollections) {
      expect(pendingWorkSchema.safeParse(invalid).success).toBe(false);
    }

    expect(
      sessionSettingFeedbackSchema.safeParse({
        Kind: "fast_mode",
        Changed: true,
        SessionName: null,
        Thinking: null,
        FastMode: true,
        Supervisor: "all",
        Questions: null,
        AutoCompaction: null,
      }).success,
    ).toBe(false);
  });

  it("submits compaction and lists and removes Pending Work with the same identities", async () => {
    const transport = new FakeRpcTransport([
      { method: "runtime.compactContext", result: {} },
      {
        method: "runtime.pendingWork.list",
        result: {
          pending_work: {
            items: [
              {
                id: ids[0],
                lane: "queue",
                kind: "message",
                state: "pending",
                canonical_input: "queued",
                message: { text: "queued" },
              },
            ],
          },
        },
      },
      {
        method: "runtime.pendingWork.remove",
        result: {
          restoration: { kind: "message", canonical_input: "queued" },
        },
      },
    ]);
    vi.spyOn(crypto, "randomUUID").mockReturnValue(ids[1]);
    const client = new ApiClient(transport);

    const requestID = await client.submitManualCompaction("session-1", "  keep   the API decisions ");
    const pendingWork = await client.listPendingWork("session-1");
    const item = pendingWork.items[0];
    if (item === undefined) throw new Error("fixture omitted Pending Work item");
    const restoration = await client.removePendingWork("session-1", item.id);

    expect(requestID.toJSONValue()).toBe(ids[1]);
    expect(transport.dedicatedCalls).toEqual([
      {
        method: "runtime.compactContext",
        params: {
          session_id: "session-1",
          request_id: ids[1],
          admission: { guidance: "keep the API decisions" },
        },
      },
    ]);
    expect(transport.calls).toEqual([
      { method: "runtime.pendingWork.list", params: { session_id: "session-1" } },
      {
        method: "runtime.pendingWork.remove",
        params: { session_id: "session-1", item_id: ids[0] },
      },
    ]);
    expect(restoration).toEqual({ kind: "message", canonicalInput: "queued" });
  });

  it("decodes only matching Pending Work errors, including nested admission failures", async () => {
    const rpcError = (method: string, code: number, data?: JsonValue) =>
      new RpcError({ method, code, message: "request failed", data });
    const cases = [
      [
        rpcError("runtime.compactContext", rpcErrorCodes.pendingWorkCapacity, {
          reason: "capacity",
        }),
        "capacity",
      ],
      [
        rpcError("runtime.pendingWork.remove", rpcErrorCodes.pendingWorkNotPending, {
          item_id: ids[0],
        }),
        "not_pending",
      ],
      [
        rpcError("worktree.enter", rpcErrorCodes.pendingWorkIdentityConflict, {
          item_id: ids[0],
        }),
        "identity_conflict",
      ],
      [
        rpcError("runtime.compactContext", rpcErrorCodes.manualCompactionDisabled, {
          reason: "disabled",
        }),
        "manual_compaction",
      ],
      [
        rpcError("runtime.compactContext", rpcErrorCodes.runtimeCommandNotAccepted, {
          cause: {
            code: rpcErrorCodes.pendingWorkCapacity,
            message: "Pending Work capacity reached",
            data: { reason: "capacity" },
          },
        }),
        "not_accepted",
      ],
      [
        rpcError("runtime.compactContext", rpcErrorCodes.runtimeCommandNotAccepted, {
          cause: {
            code: rpcErrorCodes.pendingWorkIdentityConflict,
            message: "Pending Work identity is already pending",
            data: { item_id: ids[0] },
          },
        }),
        "not_accepted",
      ],
    ] as const;
    for (const [error, kind] of cases) {
      const decoded = decodePendingWorkError(error);
      expect(decoded).toBeInstanceOf(PendingWorkError);
      expect(decoded?.detail.kind).toBe(kind);
    }

    const malformed = rpcError("runtime.compactContext", rpcErrorCodes.pendingWorkCapacity, {
      reason: "other",
    });
    expect(decodePendingWorkError(malformed)).toBeNull();
    expect(
      decodePendingWorkError(
        rpcError("worktree.enter", rpcErrorCodes.pendingWorkCapacity, { reason: "capacity" }),
      )?.detail.kind,
    ).toBe("capacity");

    const transport = new FakeRpcTransport([
      {
        method: "runtime.pendingWork.remove",
        error: cases[1][0],
      },
    ]);
    const pendingWork = pendingWorkSchema.parse({
      items: [
        {
          id: ids[0],
          lane: "queue",
          kind: "message",
          state: "pending",
          canonical_input: "queued",
          message: { text: "queued" },
        },
      ],
    });
    const item = pendingWork.items[0];
    if (item === undefined) throw new Error("fixture omitted Pending Work item");
    await expect(new ApiClient(transport).removePendingWork("session-1", item.id)).rejects.toMatchObject({
      detail: { kind: "not_pending" },
    });
  });
});
