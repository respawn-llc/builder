import {
  parseWorktreeOperationID,
  pendingWorkChangedEventSchema,
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
const [message, compact] = items;

describe("Desktop Pending Work domain", () => {
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
    pendingWorkTechnicalRestorationEventSchema.parse({
      Restoration: { item_id: ids[1], kind: "manual_compaction", canonical_input: "/compact" },
    });
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
});
