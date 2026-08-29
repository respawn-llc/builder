import { z } from "zod";

import { createUUIDv4ValueParser, type UUIDv4Value } from "./setupOperationID";

const strict = z.strictObject;
const nonBlankExact = z.string().refine((value) => value.trim().length > 0, {
  message: "Expected a non-blank string.",
});
const normalizedArgument = z
  .string()
  .refine((value) => value.length > 0 && normalizeWhitespace(value) === value, {
    message: "Expected normalized non-blank text.",
  });

export type PendingWorkItemID = UUIDv4Value<"pending_work_item">;
export type CompactionRequestID = UUIDv4Value<"compaction_request">;
export type WorktreeOperationID = UUIDv4Value<"worktree_operation">;

export const parsePendingWorkItemID = createUUIDv4ValueParser<"pending_work_item">(
  "Pending Work item id must be a UUID v4.",
);
export const parseCompactionRequestID = createUUIDv4ValueParser<"compaction_request">(
  "Compaction request id must be a UUID v4.",
);
export const parseWorktreeOperationID = createUUIDv4ValueParser<"worktree_operation">(
  "Worktree operation id must be a UUID v4.",
);

const pendingWorkItemIDSchema = uuidSchema(parsePendingWorkItemID);
const compactionRequestIDSchema = uuidSchema(parseCompactionRequestID);
const worktreeOperationIDSchema = uuidSchema(parseWorktreeOperationID);
const itemBase = {
  state: z.literal("pending"),
  canonical_input: nonBlankExact,
};

const messageItemSchema = strict({
  id: pendingWorkItemIDSchema,
  lane: z.enum(["queue", "steer"]),
  kind: z.literal("message"),
  ...itemBase,
  message: strict({ text: nonBlankExact }),
})
  .refine((value) => value.canonical_input === value.message.text, {
    message: "Message canonical input must match its text.",
  })
  .transform(({ canonical_input, ...value }) => ({ ...value, canonicalInput: canonical_input }));

const manualCompactionItemSchema = strict({
  id: compactionRequestIDSchema,
  lane: z.literal("steer"),
  kind: z.literal("manual_compaction"),
  ...itemBase,
  manual_compaction: strict({ guidance: normalizedArgument.optional() }),
})
  .refine(
    (value) =>
      value.canonical_input ===
      (value.manual_compaction.guidance === undefined
        ? "/compact"
        : `/compact ${value.manual_compaction.guidance}`),
    { message: "Manual compaction canonical input must match its guidance." },
  )
  .transform(({ canonical_input, manual_compaction, ...value }) => ({
    ...value,
    canonicalInput: canonical_input,
    manualCompaction: { guidance: manual_compaction.guidance ?? null },
  }));

const worktreeTransitionSchema = z.discriminatedUnion("transition", [
  strict({ transition: z.literal("enter"), selector: normalizedArgument }).transform((value) => ({
    kind: value.transition,
    selector: value.selector,
  })),
  strict({ transition: z.literal("leave") }).transform((value) => ({
    kind: value.transition,
    selector: null,
  })),
]);

const worktreeTransitionItemSchema = strict({
  id: worktreeOperationIDSchema,
  lane: z.literal("steer"),
  kind: z.literal("worktree_transition"),
  ...itemBase,
  worktree_transition: worktreeTransitionSchema,
})
  .refine(
    (value) =>
      value.canonical_input ===
      (value.worktree_transition.kind === "enter"
        ? `/wt switch ${value.worktree_transition.selector}`
        : "/wt leave"),
    { message: "Worktree canonical input must match its transition." },
  )
  .transform(({ canonical_input, worktree_transition, ...value }) => ({
    ...value,
    canonicalInput: canonical_input,
    worktreeTransition: worktree_transition,
  }));

export const pendingWorkItemSchema = z.union([
  messageItemSchema,
  manualCompactionItemSchema,
  worktreeTransitionItemSchema,
]);
export type PendingWorkItem = Readonly<z.output<typeof pendingWorkItemSchema>>;

export const pendingWorkSchema = strict({
  items: z.array(pendingWorkItemSchema),
})
  .superRefine((value, context) => {
    const identities = new Set<string>();
    let steerStarted = false;
    for (const [index, item] of value.items.entries()) {
      const identity = item.id.toJSONValue();
      if (identities.has(identity)) {
        context.addIssue({
          code: "custom",
          message: "Pending Work item ids must be unique.",
          path: ["items", index, "id"],
        });
      }
      identities.add(identity);
      if (item.lane === "steer") {
        steerStarted = true;
      } else if (steerStarted) {
        context.addIssue({
          code: "custom",
          message: "Pending Work Queue items must precede Steer items.",
          path: ["items", index, "lane"],
        });
      }
    }
  })
  .transform((value) => ({ items: value.items }));
export type PendingWork = Readonly<z.output<typeof pendingWorkSchema>>;

export type PendingWorkIdentity = PendingWorkItemID | CompactionRequestID | WorktreeOperationID;

const pendingWorkItemKindSchema = z.enum(["message", "manual_compaction", "worktree_transition"]);
const canonicalInputSchema = nonBlankExact;

export const pendingWorkRestorationSchema = strict({
  kind: pendingWorkItemKindSchema,
  canonical_input: canonicalInputSchema,
}).transform((value) => ({
  kind: value.kind,
  canonicalInput: value.canonical_input,
}));
export type PendingWorkRestoration = Readonly<z.output<typeof pendingWorkRestorationSchema>>;

const pendingWorkTechnicalRestorationSchema = strict({
  item_id: pendingWorkItemIDSchema,
  kind: pendingWorkItemKindSchema,
  canonical_input: canonicalInputSchema,
}).transform((value) => ({
  itemID: value.item_id,
  kind: value.kind,
  canonicalInput: value.canonical_input,
}));

export const pendingWorkTechnicalRestorationEventSchema = strict({
  Restoration: pendingWorkTechnicalRestorationSchema,
}).transform((value) => ({ restoration: value.Restoration }));
export type PendingWorkTechnicalRestorationEvent = Readonly<
  z.output<typeof pendingWorkTechnicalRestorationEventSchema>
>;

export const pendingWorkChangedEventSchema = strict({});
export type PendingWorkChangedEvent = Readonly<z.output<typeof pendingWorkChangedEventSchema>>;

const feedbackBase = {
  Changed: z.boolean(),
  SessionName: z.null(),
  Thinking: z.null(),
  FastMode: z.null(),
  Supervisor: z.null(),
  Questions: z.null(),
  AutoCompaction: z.null(),
};
const normalizedSessionName = z.string().refine((value) => value.trim() === value, {
  message: "Expected normalized Session Name.",
});
const normalizedNonBlank = z.string().refine((value) => value.trim().length > 0 && value.trim() === value, {
  message: "Expected normalized non-blank value.",
});

const sessionSettingFeedbackWireSchema = z.discriminatedUnion("Kind", [
  strict({ ...feedbackBase, Kind: z.literal("session_name"), SessionName: normalizedSessionName }),
  strict({ ...feedbackBase, Kind: z.literal("thinking"), Thinking: normalizedNonBlank }),
  strict({ ...feedbackBase, Kind: z.literal("fast_mode"), FastMode: z.boolean() }),
  strict({ ...feedbackBase, Kind: z.literal("supervisor"), Supervisor: z.enum(["off", "edits", "all"]) }),
  strict({ ...feedbackBase, Kind: z.literal("questions"), Questions: z.boolean() }),
  strict({ ...feedbackBase, Kind: z.literal("auto_compaction"), AutoCompaction: z.boolean() }),
]);
export const sessionSettingFeedbackSchema = sessionSettingFeedbackWireSchema.transform((value) => {
  const shared = { kind: value.Kind, changed: value.Changed } as const;
  switch (value.Kind) {
    case "session_name":
      return { ...shared, value: value.SessionName };
    case "thinking":
      return { ...shared, value: value.Thinking };
    case "fast_mode":
      return { ...shared, value: value.FastMode };
    case "supervisor":
      return { ...shared, value: value.Supervisor };
    case "questions":
      return { ...shared, value: value.Questions };
    case "auto_compaction":
      return { ...shared, value: value.AutoCompaction };
  }
});
export type SessionSettingFeedback = Readonly<z.output<typeof sessionSettingFeedbackSchema>>;

function uuidSchema<Domain extends string>(parse: (value: string) => UUIDv4Value<Domain>) {
  return z.string().transform((value, context) => {
    try {
      return parse(value);
    } catch (error) {
      context.addIssue({
        code: "custom",
        message: error instanceof Error ? error.message : "Expected UUID v4.",
      });
      return z.NEVER;
    }
  });
}

export function normalizeWhitespace(value: string): string {
  let normalized = "";
  let separatorPending = false;
  for (const character of value) {
    if (character.trim().length === 0) {
      separatorPending = normalized.length > 0;
      continue;
    }
    if (separatorPending) {
      normalized += " ";
      separatorPending = false;
    }
    normalized += character;
  }
  return normalized;
}
