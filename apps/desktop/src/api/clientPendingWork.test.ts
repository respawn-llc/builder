import { FakeRpcTransport } from "@/test-support/api";

import { ApiClient } from "./client";
import { decodePendingWorkError } from "./clientPendingWork";
import { RpcError } from "./errors";
import {
  parseWorktreeOperationID,
  pendingWorkChangedEventSchema,
  pendingWorkSchema,
  pendingWorkTechnicalRestorationEventSchema,
  sessionSettingFeedbackSchema,
} from "./pendingWork";
import { rpcErrorCodes } from "./rpcErrorCodes";

const ids = [
  "123e4567-e89b-42d3-a456-426614174000",
  "223e4567-e89b-42d3-a456-426614174000",
  "323e4567-e89b-42d3-a456-426614174000",
  "423e4567-e89b-42d3-a456-426614174000",
] as const;
const base = (id: string, kind: string, canonical_input: string) => ({
  id,
  lane: "steer",
  kind,
  state: "pending",
  canonical_input,
});
const items = [
  { ...base(ids[0], "message", "queued"), lane: "queue", message: { text: "queued" } },
  {
    ...base(ids[1], "manual_compaction", "/compact keep decisions"),
    manual_compaction: { guidance: "keep decisions" },
  },
  {
    ...base(ids[2], "worktree_transition", "/wt switch feature"),
    worktree_transition: { transition: "enter", selector: "feature" },
  },
  { ...base(ids[3], "worktree_transition", "/wt leave"), worktree_transition: { transition: "leave" } },
] as const;
const [message, compact, enter] = items;
const rpcError = (method: string, code: number, data?: RpcError["data"]) =>
  new RpcError({ method, code, message: "failed", data });

describe("Desktop Pending Work client", () => {
  it("validates representative closed contracts and supplied order", () => {
    const decoded = pendingWorkSchema.parse({ items });
    expect(decoded.items.map((item) => item.canonicalInput)).toEqual(
      items.map((item) => item.canonical_input),
    );
    expect(decoded.items[2]?.id.toJSONValue()).toBe(parseWorktreeOperationID(ids[2]).toJSONValue());
    const invalid = [
      { items: [{ ...compact, canonical_input: "wrong" }] },
      { items: [{ ...compact, message: { text: "wrong family" } }] },
      { items: [compact, message] },
    ];
    for (const value of invalid) expect(pendingWorkSchema.safeParse(value).success).toBe(false);
    expect(pendingWorkChangedEventSchema.parse({})).toEqual({});
    const restoration = {
      Restoration: { item_id: ids[1], kind: "manual_compaction", canonical_input: "/compact" },
    };
    pendingWorkTechnicalRestorationEventSchema.parse(restoration);
    const feedback = {
      Kind: "fast_mode",
      Changed: true,
      SessionName: null,
      Thinking: null,
      FastMode: true,
      Supervisor: null,
      Questions: null,
      AutoCompaction: null,
    } as const;
    expect(sessionSettingFeedbackSchema.parse(feedback).value).toBe(true);
  });

  it("uses compact, list, and remove routes with typed identities", async () => {
    const transport = new FakeRpcTransport([
      { method: "runtime.compactContext", result: {} },
      { method: "runtime.pendingWork.list", result: { pending_work: { items: [enter] } } },
      {
        method: "runtime.pendingWork.remove",
        result: {
          restoration: { kind: "worktree_transition", canonical_input: "/wt switch feature" },
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
      kind: "worktree_transition",
      canonicalInput: "/wt switch feature",
    });
    expect(transport.dedicatedCalls[0]).toMatchObject({
      method: "runtime.compactContext",
      params: { session_id: "session-1", request_id: ids[1], admission: { guidance: "keep decisions" } },
    });
    expect(transport.calls[1]?.params).toEqual({ session_id: "session-1", item_id: ids[2] });
  });

  it("decodes direct and nested typed failures", () => {
    const failures = [
      [rpcErrorCodes.pendingWorkCapacity, { reason: "capacity" }, { kind: "capacity" }],
      [rpcErrorCodes.runtimeUnavailable, undefined, { kind: "runtime_unavailable" }],
      [
        rpcErrorCodes.manualCompactionTooSoon,
        { reason: "too_soon" },
        { kind: "manual_compaction", reason: "too_soon" },
      ],
    ] as const;
    for (const [code, data, detail] of failures) {
      expect(decodePendingWorkError(rpcError("runtime.compactContext", code, data))?.detail).toEqual(detail);
      if (data === undefined) continue;
      const nested = rpcError("runtime.compactContext", rpcErrorCodes.runtimeCommandNotAccepted, {
        cause: { code, message: "cause", data },
      });
      expect(decodePendingWorkError(nested)?.detail).toEqual({ kind: "not_accepted", cause: detail });
    }
    const notPending = decodePendingWorkError(
      rpcError("runtime.pendingWork.remove", rpcErrorCodes.pendingWorkNotPending, { item_id: ids[0] }),
    )?.detail;
    expect(notPending?.kind).toBe("not_pending");
    if (notPending?.kind === "not_pending") expect(notPending.itemID.toJSONValue()).toBe(ids[0]);
  });
});
