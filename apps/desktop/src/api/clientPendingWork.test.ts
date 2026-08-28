import { FakeRpcTransport } from "@/test-support/api";

import { ApiClient } from "./client";
import { decodePendingWorkError } from "./clientPendingWork";
import { RpcError } from "./errors";
import { rpcErrorCodes } from "./rpcErrorCodes";
import {
  pendingWorkChangedEventSchema,
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
const message = {
  id: ids[0],
  lane: "queue",
  kind: "message",
  state: "pending",
  canonical_input: "queued",
  message: { text: "queued" },
} as const;

describe("Desktop Pending Work client", () => {
  it("decodes the closed collection in server order and rejects incoherent values", () => {
    const wire = {
      items: [
        message,
        {
          id: ids[1],
          lane: "steer",
          kind: "manual_compaction",
          state: "pending",
          canonical_input: "/compact keep decisions",
          manual_compaction: { guidance: "keep decisions" },
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
    };
    const parsed = pendingWorkSchema.parse(wire);
    expect(parsed.items.map((item) => item.canonicalInput)).toEqual([
      "queued",
      "/compact keep decisions",
      "/wt switch feature",
      "/wt leave",
    ]);

    for (const invalid of [
      { items: [{ ...message, id: "not-a-uuid" }] },
      { items: [{ ...message, canonical_input: "different" }] },
      {
        items: [
          { ...message, lane: "steer" },
          { ...message, id: ids[1] },
        ],
      },
      { items: [{ ...message, manual_compaction: {} }] },
    ]) {
      expect(pendingWorkSchema.safeParse(invalid).success).toBe(false);
    }
  });

  it("decodes payload-free changes, restorations, and typed setting feedback", () => {
    expect(pendingWorkChangedEventSchema.parse({})).toEqual({});
    expect(pendingWorkChangedEventSchema.safeParse({ PendingWork: { items: [] } }).success).toBe(false);
    expect(
      pendingWorkRestorationSchema.parse({
        kind: "worktree_transition",
        canonical_input: "/wt leave",
      }),
    ).toEqual({ kind: "worktree_transition", canonicalInput: "/wt leave" });
    expect(
      pendingWorkTechnicalRestorationEventSchema
        .parse({
          Restoration: {
            item_id: ids[1],
            kind: "manual_compaction",
            canonical_input: "/compact",
          },
        })
        .restoration.itemID.toJSONValue(),
    ).toBe(ids[1]);

    const absent = {
      SessionName: null,
      Thinking: null,
      FastMode: null,
      Supervisor: null,
      Questions: null,
      AutoCompaction: null,
    };
    expect(
      sessionSettingFeedbackSchema.parse({
        ...absent,
        Kind: "fast_mode",
        Changed: true,
        FastMode: true,
      }).value,
    ).toBe(true);
    expect(
      sessionSettingFeedbackSchema.safeParse({
        ...absent,
        Kind: "fast_mode",
        Changed: true,
        FastMode: true,
        Supervisor: "all",
      }).success,
    ).toBe(false);
  });

  it("uses the typed identities for submit, list, remove, and matching errors", async () => {
    const notPending = new RpcError({
      method: "runtime.pendingWork.remove",
      code: rpcErrorCodes.pendingWorkNotPending,
      message: "not pending",
      data: { item_id: ids[0] },
    });
    const transport = new FakeRpcTransport([
      { method: "runtime.compactContext", result: {} },
      { method: "runtime.pendingWork.list", result: { pending_work: { items: [message] } } },
      {
        method: "runtime.pendingWork.remove",
        result: {
          restoration: {
            kind: "message",
            canonical_input: "queued",
          },
        },
      },
    ]);
    vi.spyOn(crypto, "randomUUID").mockReturnValue(ids[1]);
    const client = new ApiClient(transport);

    expect((await client.submitManualCompaction("session-1", " keep   decisions ")).toJSONValue()).toBe(
      ids[1],
    );
    const item = (await client.listPendingWork("session-1")).items[0];
    if (item === undefined) throw new Error("fixture omitted Pending Work item");
    expect(await client.removePendingWork("session-1", item.id)).toEqual({
      kind: "message",
      canonicalInput: "queued",
    });
    expect(transport.dedicatedCalls[0]?.params).toEqual({
      session_id: "session-1",
      request_id: ids[1],
      admission: { guidance: "keep decisions" },
    });
    expect(decodePendingWorkError(notPending)?.detail).toMatchObject({ kind: "not_pending" });
    expect(
      decodePendingWorkError(
        new RpcError({
          method: "runtime.compactContext",
          code: rpcErrorCodes.runtimeCommandNotAccepted,
          message: "not accepted",
          data: {
            cause: {
              code: rpcErrorCodes.pendingWorkCapacity,
              message: "capacity",
              data: { reason: "capacity" },
            },
          },
        }),
      )?.detail,
    ).toEqual({ kind: "not_accepted", cause: { kind: "capacity" } });
  });
});
