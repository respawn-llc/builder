import { z } from "zod";

import type {
  AttentionNotification,
  AttentionNotificationEvent,
  AttentionNotificationEventParams,
  AttentionNotificationPresentation,
  AttentionNotificationQuestionState,
  AttentionNotificationTarget,
  AttentionNotificationTaskDetailFocus,
} from "../attentionNotifications";

const stringList = z.array(z.string());
const nonEmptyStringList = stringList.refine((value) => value.length > 0, {
  message: "Expected at least one id",
});

const questionFocusWireSchema = z.object({
  kind: z.literal("question"),
  ask_ids: nonEmptyStringList,
});

const approvalFocusWireSchema = z.object({
  kind: z.literal("approval"),
  task_transition_id: z.string().min(1),
});

const taskDetailFocusWireSchema = z.discriminatedUnion("kind", [
  questionFocusWireSchema,
  approvalFocusWireSchema,
]);

function taskDetailFocus(value: z.infer<typeof taskDetailFocusWireSchema>): AttentionNotificationTaskDetailFocus {
  if (value.kind === "question") {
    return { kind: "question", askIDs: value.ask_ids };
  }
  return { kind: "approval", taskTransitionID: value.task_transition_id };
}

const taskDetailTargetWireSchema = z.object({
  kind: z.literal("task_detail"),
  project_id: z.string().optional().default(""),
  workflow_id: z.string().optional().default(""),
  task_id: z.string().min(1),
  task_short_id: z.string().optional().default(""),
  task_title: z.string().optional().default(""),
  session_id: z.string().optional().default(""),
  run_id: z.string().optional().default(""),
  focus: taskDetailFocusWireSchema,
});

const sessionPromptTargetWireSchema = z.object({
  kind: z.literal("session_prompt"),
  session_id: z.string().min(1),
});

const targetWireSchema = z.discriminatedUnion("kind", [
  taskDetailTargetWireSchema,
  sessionPromptTargetWireSchema,
]);

function target(value: z.infer<typeof targetWireSchema>): AttentionNotificationTarget {
  if (value.kind === "session_prompt") {
    return { kind: "session_prompt", sessionID: value.session_id };
  }
  return {
    kind: "task_detail",
    projectID: value.project_id,
    workflowID: value.workflow_id,
    taskID: value.task_id,
    taskShortID: value.task_short_id,
    taskTitle: value.task_title,
    sessionID: value.session_id,
    runID: value.run_id,
    focus: taskDetailFocus(value.focus),
  };
}

const presentationSchema: z.ZodType<AttentionNotificationPresentation> = z
  .object({
    title: z.string(),
    body: z.string(),
    preview: z.string().optional().default(""),
    fallback_body: z.string().optional().default(""),
    count: z.number().optional().default(0),
    summary: z.string().optional().default(""),
  })
  .transform((value) => ({
    title: value.title,
    body: value.body,
    preview: value.preview,
    fallbackBody: value.fallback_body,
    count: value.count,
    summary: value.summary,
  }));

const questionStateSchema: z.ZodType<AttentionNotificationQuestionState> = z
  .object({
    prepared_ask_ids: stringList,
    materialized_ask_ids: stringList,
    current_unresolved_ask_ids: stringList,
    skipped_ask_ids: stringList,
    display_count: z.number(),
    materialized_count: z.number(),
  })
  .transform((value) => ({
    preparedAskIDs: value.prepared_ask_ids,
    materializedAskIDs: value.materialized_ask_ids,
    currentUnresolvedAskIDs: value.current_unresolved_ask_ids,
    skippedAskIDs: value.skipped_ask_ids,
    displayCount: value.display_count,
    materializedCount: value.materialized_count,
  }));

const notificationSchema: z.ZodType<AttentionNotification> = z
  .object({
    id: z.string().min(1),
    kind: z.enum(["question", "approval"]),
    occurred_at: z.string().min(1),
    revision: z.number().min(1),
    question: questionStateSchema.nullish(),
    target: targetWireSchema,
    presentation: presentationSchema,
  })
  .transform((value) => ({
    id: value.id,
    kind: value.kind,
    occurredAt: value.occurred_at,
    revision: value.revision,
    question: value.question ?? null,
    target: target(value.target),
    presentation: value.presentation,
  }));

const pendingEventSchema = z
  .object({
    type: z.literal("pending"),
    sequence: z.number().min(1),
    source: z.enum(["live", "snapshot"]),
    pending: notificationSchema,
  })
  .transform((value) => ({
    type: value.type,
    sequence: value.sequence,
    source: value.source,
    pending: value.pending,
  }));

const resolvedEventSchema = z
  .object({
    type: z.literal("resolved"),
    sequence: z.number().min(1),
    source: z.enum(["live", "snapshot"]),
    id: z.string().min(1),
    kind: z.enum(["question", "approval"]),
    occurred_at: z.string().min(1),
  })
  .transform((value) => ({
    type: value.type,
    sequence: value.sequence,
    source: value.source,
    id: value.id,
    kind: value.kind,
    occurredAt: value.occurred_at,
  }));

const snapshotCompleteEventSchema = z
  .object({
    type: z.literal("snapshot_complete"),
    sequence: z.number().min(1),
    source: z.literal("snapshot"),
    session_id: z.string().min(1),
  })
  .transform((value) => ({
    type: value.type,
    sequence: value.sequence,
    source: value.source,
    sessionID: value.session_id,
  }));

export const attentionNotificationEventSchema: z.ZodType<AttentionNotificationEvent> = z.union([
  pendingEventSchema,
  resolvedEventSchema,
  snapshotCompleteEventSchema,
]);

export const attentionNotificationEventParamsSchema: z.ZodType<AttentionNotificationEventParams> = z
  .object({
    event: attentionNotificationEventSchema,
  })
  .transform((value) => ({ event: value.event }));
