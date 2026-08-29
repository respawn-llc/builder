import { FakeRpcTransport } from "@/test-support/api";

import { ApiClient } from "./client";
import { decodePendingWorkError } from "./clientPendingWork";
import { RpcError } from "./errors";
import {
  pendingWorkChangedEventSchema,
  pendingWorkSchema,
  pendingWorkTechnicalRestorationEventSchema,
  sessionSettingFeedbackSchema,
} from "./pendingWork";
import { rpcErrorCodes } from "./rpcErrorCodes";

const ids = ["123e4567-e89b-42d3-a456-426614174000", "223e4567-e89b-42d3-a456-426614174000"] as const;
const message = {
  id: ids[0],
  lane: "queue",
  kind: "message",
  state: "pending",
  canonical_input: "queued",
  message: { text: "queued" },
} as const;

describe("Desktop Pending Work client", () => {
  it("validates the closed event and item contracts", () => {
    const parsed = pendingWorkSchema.parse({
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
          id: "323e4567-e89b-42d3-a456-426614174000",
          lane: "steer",
          kind: "worktree_transition",
          state: "pending",
          canonical_input: "/wt switch feature",
          worktree_transition: { transition: "enter", selector: "feature" },
        },
        {
          id: "423e4567-e89b-42d3-a456-426614174000",
          lane: "steer",
          kind: "worktree_transition",
          state: "pending",
          canonical_input: "/wt leave",
          worktree_transition: { transition: "leave" },
        },
      ],
    });
    expect(parsed.items.map((item) => item.canonicalInput)).toEqual([
      "queued",
      "/compact keep decisions",
      "/wt switch feature",
      "/wt leave",
    ]);
    expect(pendingWorkSchema.safeParse({ items: [{ ...message, manual_compaction: {} }] }).success).toBe(false);
    expect(pendingWorkChangedEventSchema.parse({})).toEqual({});
    expect(
      pendingWorkTechnicalRestorationEventSchema
        .parse({
          Restoration: { item_id: ids[1], kind: "manual_compaction", canonical_input: "/compact" },
        })
        .restoration.itemID.toJSONValue(),
    ).toBe(ids[1]);
    expect(
      sessionSettingFeedbackSchema.parse({
        Kind: "fast_mode",
        Changed: true,
        SessionName: null,
        Thinking: null,
        FastMode: true,
        Supervisor: null,
        Questions: null,
        AutoCompaction: null,
      }).value,
    ).toBe(true);
  });

  it("uses typed identities and decodes matching failures", async () => {
    const transport = new FakeRpcTransport([
      { method: "runtime.compactContext", result: {} },
      { method: "runtime.pendingWork.list", result: { pending_work: { items: [message] } } },
      {
        method: "runtime.pendingWork.remove",
        result: { restoration: { kind: "message", canonical_input: "queued" } },
      },
    ]);
    vi.spyOn(crypto, "randomUUID").mockReturnValue(ids[1]);
    const client = new ApiClient(transport);

    expect((await client.submitManualCompaction("session-1", " keep   decisions ")).toJSONValue()).toBe(ids[1]);
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
    expect(
      decodePendingWorkError(
        new RpcError({
          method: "runtime.pendingWork.remove",
          code: rpcErrorCodes.pendingWorkNotPending,
          message: "not pending",
          data: { item_id: ids[0] },
        }),
      )?.detail,
    ).toMatchObject({ kind: "not_pending" });
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
