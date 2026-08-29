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

export const parsePendingWorkItemID =
  createUUIDv4ValueParser<"pending_work_item">("Invalid Pending Work UUID v4.");
export const parseCompactionRequestID =
  createUUIDv4ValueParser<"compaction_request">("Invalid compaction UUID v4.");
export const parseWorktreeOperationID =
  createUUIDv4ValueParser<"worktree_operation">("Invalid Worktree operation UUID v4.");

export const pendingWorkItemIDSchema = uuidSchema(parsePendingWorkItemID);
const compactionRequestIDSchema = uuidSchema(parseCompactionRequestID);
const worktreeOperationIDSchema = uuidSchema(parseWorktreeOperationID);
const itemBase = {
  state: z.literal("pending"),
  canonical_input: nonBlankExact,
};

const worktreeTransitionSchema = z.discriminatedUnion("transition", [
  strict({ transition: z.literal("enter"), selector: normalizedArgument }),
  strict({ transition: z.literal("leave") }),
]);

export const pendingWorkItemSchema = z
  .discriminatedUnion("kind", [
    strict({
      ...itemBase,
      id: pendingWorkItemIDSchema,
      lane: z.enum(["queue", "steer"]),
      kind: z.literal("message"),
      message: strict({ text: nonBlankExact }),
    }),
    strict({
      ...itemBase,
      id: compactionRequestIDSchema,
      lane: z.literal("steer"),
      kind: z.literal("manual_compaction"),
      manual_compaction: strict({ guidance: normalizedArgument.optional() }),
    }),
    strict({
      ...itemBase,
      id: worktreeOperationIDSchema,
      lane: z.literal("steer"),
      kind: z.literal("worktree_transition"),
      worktree_transition: worktreeTransitionSchema,
    }),
  ])
  .superRefine((item, context) => {
    const expected =
      item.kind === "message"
        ? item.message.text
        : item.kind === "manual_compaction"
          ? item.manual_compaction.guidance === undefined
            ? "/compact"
            : `/compact ${item.manual_compaction.guidance}`
          : item.worktree_transition.transition === "enter"
            ? `/wt switch ${item.worktree_transition.selector}`
            : "/wt leave";
    if (item.canonical_input !== expected) {
      context.addIssue({ code: "custom", message: "Canonical input does not match its payload." });
    }
  })
  .transform((item) => {
    const { canonical_input: canonicalInput, ...value } = item;
    if (value.kind === "message") return { ...value, canonicalInput };
    if (value.kind === "manual_compaction") {
      const { manual_compaction, ...rest } = value;
      return {
        ...rest,
        canonicalInput,
        manualCompaction: { guidance: manual_compaction.guidance ?? null },
      };
    }
    const { worktree_transition, ...rest } = value;
    return {
      ...rest,
      canonicalInput,
      worktreeTransition:
        worktree_transition.transition === "enter"
          ? { kind: "enter" as const, selector: worktree_transition.selector }
          : { kind: "leave" as const, selector: null },
    };
  });
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
  });
export type PendingWork = Readonly<z.output<typeof pendingWorkSchema>>;

export type PendingWorkIdentity = PendingWorkItemID | CompactionRequestID | WorktreeOperationID;

const pendingWorkItemKindSchema = z.enum(["message", "manual_compaction", "worktree_transition"]);

export const pendingWorkRestorationSchema = strict({
  kind: pendingWorkItemKindSchema,
  canonical_input: nonBlankExact,
}).transform((value) => ({
  kind: value.kind,
  canonicalInput: value.canonical_input,
}));
export type PendingWorkRestoration = Readonly<z.output<typeof pendingWorkRestorationSchema>>;

const pendingWorkTechnicalRestorationSchema = strict({
  item_id: pendingWorkItemIDSchema,
  kind: pendingWorkItemKindSchema,
  canonical_input: nonBlankExact,
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
    } catch {
      context.addIssue({ code: "custom", message: "Expected UUID v4." });
      return z.NEVER;
    }
  });
}

export function normalizeWhitespace(value: string): string {
  let normalized = "";
  let separatorPending = false;
  for (const character of value) {
    const whitespace = character.trim().length === 0;
    if (!whitespace && separatorPending) normalized += " ";
    if (!whitespace) normalized += character;
    separatorPending = whitespace ? normalized.length > 0 : false;
  }
  return normalized;
}
