import { z } from "zod";

import type {
  AttentionNotification,
  AttentionNotificationApprovalState,
  AttentionNotificationEvent,
  AttentionNotificationEventParams,
  AttentionNotificationID,
  AttentionNotificationInterruptedRunState,
  AttentionNotificationQuestionState,
  AttentionNotificationTarget,
  AttentionNotificationTaskDetailFocus,
} from "../attentionNotifications";

const idString = z.string().min(1);
const stringList = z.array(idString);
const nonEmptyStringList = stringList.refine((value) => value.length > 0, {
  message: "Expected at least one id",
});

const attentionIDWireSchema = z.object({
  kind: z.enum(["question", "approval", "interrupted_run"]),
  uuid: z.string().min(1),
});

function attentionID(value: z.infer<typeof attentionIDWireSchema>): AttentionNotificationID {
  return { kind: value.kind, uuid: value.uuid };
}

const questionFocusWireSchema = z.object({
  kind: z.literal("question"),
  ask_ids: nonEmptyStringList,
});

const approvalFocusWireSchema = z.object({
  kind: z.literal("approval"),
  task_transition_id: z.string().min(1),
});

const interruptedRunFocusWireSchema = z.object({
  kind: z.literal("interrupted_run"),
  run_id: z.string().min(1),
});

const workflowTaskFocusWireSchema = z.discriminatedUnion("kind", [
  questionFocusWireSchema,
  approvalFocusWireSchema,
  interruptedRunFocusWireSchema,
]);

function workflowTaskFocus(value: z.infer<typeof workflowTaskFocusWireSchema>): AttentionNotificationTaskDetailFocus {
  if (value.kind === "question") {
    return { kind: "question", askIDs: value.ask_ids };
  }
  if (value.kind === "approval") {
    return { kind: "approval", taskTransitionID: value.task_transition_id };
  }
  return { kind: "interrupted_run", runID: value.run_id };
}

const workflowTaskTargetWireSchema = z.object({
  kind: z.literal("workflow_task"),
  project_id: z.string().optional(),
  workflow_id: z.string().optional(),
  task_id: z.string().min(1),
  task_short_id: z.string().optional(),
  task_title: z.string().optional(),
  session_id: z.string().optional(),
  run_id: z.string().optional(),
  focus: workflowTaskFocusWireSchema,
});

const sessionPromptTargetWireSchema = z.object({
  kind: z.literal("session_prompt"),
  session_id: z.string().min(1),
});

const targetWireSchema = z.discriminatedUnion("kind", [
  workflowTaskTargetWireSchema,
  sessionPromptTargetWireSchema,
]);

function target(value: z.infer<typeof targetWireSchema>): AttentionNotificationTarget {
  if (value.kind === "session_prompt") {
    return { kind: "session_prompt", sessionID: value.session_id };
  }
  return {
    kind: "workflow_task",
    projectID: value.project_id,
    workflowID: value.workflow_id,
    taskID: value.task_id,
    taskShortID: value.task_short_id,
    taskTitle: value.task_title,
    sessionID: value.session_id,
    runID: value.run_id,
    focus: workflowTaskFocus(value.focus),
  };
}

const questionStateSchema: z.ZodType<AttentionNotificationQuestionState> = z
  .object({
    prepared_ask_ids: stringList,
    materialized_ask_ids: stringList,
    current_unresolved_ask_ids: stringList,
    skipped_ask_ids: stringList,
    preview: z.string().optional(),
    display_count: z.number(),
    materialized_count: z.number(),
  })
  .superRefine((value, context) => {
    if (value.prepared_ask_ids.length === 0) {
      context.addIssue({ code: "custom", message: "prepared_ask_ids is required" });
    }
    if (value.display_count !== value.prepared_ask_ids.length - value.skipped_ask_ids.length) {
      context.addIssue({ code: "custom", message: "display_count must match non-skipped prepared asks" });
    }
    if (value.materialized_count !== value.materialized_ask_ids.length) {
      context.addIssue({ code: "custom", message: "materialized_count must match materialized_ask_ids" });
    }
    if (
      !stringListSubset(value.materialized_ask_ids, value.prepared_ask_ids) ||
      !stringListSubset(value.current_unresolved_ask_ids, value.materialized_ask_ids) ||
      !stringListSubset(value.skipped_ask_ids, value.prepared_ask_ids)
    ) {
      context.addIssue({ code: "custom", message: "question ask id lists must be consistent" });
    }
  })
  .transform((value) => ({
    preparedAskIDs: value.prepared_ask_ids,
    materializedAskIDs: value.materialized_ask_ids,
    currentUnresolvedAskIDs: value.current_unresolved_ask_ids,
    skippedAskIDs: value.skipped_ask_ids,
    preview: value.preview,
    displayCount: value.display_count,
    materializedCount: value.materialized_count,
  }));

const approvalStateSchema: z.ZodType<AttentionNotificationApprovalState> = z
  .object({
    task_transition_id: z.string().optional(),
    message: z.string().optional(),
  })
  .transform((value) => ({
    taskTransitionID: value.task_transition_id,
    message: value.message,
  }));

const interruptedRunStateSchema: z.ZodType<AttentionNotificationInterruptedRunState> = z
  .object({
    run_id: z.string().min(1),
    message: z.string().optional(),
    reason: z.string().optional(),
    detail_json: z.string().optional(),
  })
  .transform((value) => ({
    runID: value.run_id,
    message: value.message,
    reason: value.reason,
    detailJSON: value.detail_json,
  }));

const notificationWireSchema = z.object({
  id: attentionIDWireSchema,
  kind: z.enum(["question", "approval", "interrupted_run"]),
  occurred_at: z.string().min(1),
  revision: z.number().min(1),
  question: questionStateSchema.nullish(),
  approval: approvalStateSchema.nullish(),
  interrupted_run: interruptedRunStateSchema.nullish(),
  target: targetWireSchema,
});

const notificationSchema: z.ZodType<AttentionNotification> = notificationWireSchema
  .superRefine((value, context) => {
    if (value.id.kind !== value.kind) {
      context.addIssue({ code: "custom", message: "id kind must match notification kind" });
    }
    if (value.kind === "question") {
      validateQuestionNotification(value, context);
      return;
    }
    if (value.kind === "approval") {
      validateApprovalNotification(value, context);
      return;
    }
    validateInterruptedRunNotification(value, context);
  })
  .transform((value) => ({
    id: attentionID(value.id),
    kind: value.kind,
    occurredAt: value.occurred_at,
    revision: value.revision,
    question: value.question ?? null,
    approval: value.approval ?? null,
    interruptedRun: value.interrupted_run ?? null,
    target: target(value.target),
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
    id: attentionIDWireSchema,
    kind: z.enum(["question", "approval", "interrupted_run"]),
    occurred_at: z.string().min(1),
  })
  .transform((value) => ({
    type: value.type,
    sequence: value.sequence,
    source: value.source,
    id: attentionID(value.id),
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

export function isUnsupportedAttentionNotificationEventParams(value: unknown): boolean {
  const parsed = unsupportedEventProbeSchema.safeParse(value);
  if (!parsed.success) {
    return false;
  }
  const { event } = parsed.data;
  if (event.type === undefined) {
    return false;
  }
  if (event.type === "resolved") {
    return event.kind !== undefined && !supportedAttentionKind(event.kind);
  }
  if (event.type === "pending") {
    return isUnsupportedPendingEvent(event);
  }
  return event.type !== "snapshot_complete";
}

const unsupportedEventProbeSchema = z.looseObject({
  event: z.looseObject({
    type: z.string().optional(),
    kind: z.string().optional(),
    pending: z.looseObject({
      kind: z.string().optional(),
      target: z.looseObject({
        kind: z.string().optional(),
        focus: z.looseObject({
          kind: z.string().optional(),
        }).optional(),
      }).optional(),
    }).optional(),
  }),
});

function isUnsupportedPendingEvent(
  event: z.infer<typeof unsupportedEventProbeSchema>["event"],
): boolean {
  const pending = event.pending;
  if (pending === undefined) {
    return false;
  }
  if (pending.kind !== undefined && !supportedAttentionKind(pending.kind)) {
    return true;
  }
  const target = pending.target;
  if (target === undefined) {
    return false;
  }
  if (target.kind !== undefined && !supportedTargetKind(target.kind)) {
    return true;
  }
  if (target.kind !== "workflow_task") {
    return false;
  }
  const focus = target.focus;
  if (focus === undefined) {
    return false;
  }
  return focus.kind !== undefined && !supportedFocusKind(focus.kind);
}

function supportedAttentionKind(kind: string): boolean {
  return kind === "question" || kind === "approval" || kind === "interrupted_run";
}

function supportedTargetKind(kind: string): boolean {
  return kind === "workflow_task" || kind === "session_prompt";
}

function supportedFocusKind(kind: string): boolean {
  return kind === "question" || kind === "approval" || kind === "interrupted_run";
}

function validateQuestionNotification(
  value: z.infer<typeof notificationWireSchema>,
  context: z.RefinementCtx,
): void {
  if (value.question === null || value.question === undefined) {
    context.addIssue({ code: "custom", message: "question payload is required" });
  }
  if (value.approval !== null && value.approval !== undefined) {
    context.addIssue({ code: "custom", message: "question notification must not carry approval payload" });
  }
  if (value.interrupted_run !== null && value.interrupted_run !== undefined) {
    context.addIssue({ code: "custom", message: "question notification must not carry interrupted_run payload" });
  }
  if (value.target.kind === "workflow_task") {
    const { focus } = value.target;
    if (focus.kind !== "question") {
      context.addIssue({ code: "custom", message: "question workflow-task focus kind must be question" });
      return;
    }
    if (
      value.question !== null &&
      value.question !== undefined &&
      !sameStringSet(focus.ask_ids, value.question.preparedAskIDs)
    ) {
      context.addIssue({ code: "custom", message: "question focus ask_ids must match prepared_ask_ids" });
    }
  }
}

function validateApprovalNotification(
  value: z.infer<typeof notificationWireSchema>,
  context: z.RefinementCtx,
): void {
  if (value.approval === null || value.approval === undefined) {
    context.addIssue({ code: "custom", message: "approval payload is required" });
  }
  if (value.question !== null && value.question !== undefined) {
    context.addIssue({ code: "custom", message: "approval notification must not carry question payload" });
  }
  if (value.interrupted_run !== null && value.interrupted_run !== undefined) {
    context.addIssue({ code: "custom", message: "approval notification must not carry interrupted_run payload" });
  }
  if (value.target.kind === "workflow_task") {
    const { focus } = value.target;
    if (focus.kind !== "approval") {
      context.addIssue({ code: "custom", message: "approval workflow-task focus kind must be approval" });
      return;
    }
    if (value.approval?.taskTransitionID !== focus.task_transition_id) {
      context.addIssue({ code: "custom", message: "approval focus task_transition_id must match payload" });
    }
  }
}

function validateInterruptedRunNotification(
  value: z.infer<typeof notificationWireSchema>,
  context: z.RefinementCtx,
): void {
  if (value.interrupted_run === null || value.interrupted_run === undefined) {
    context.addIssue({ code: "custom", message: "interrupted_run payload is required" });
  }
  if (value.question !== null && value.question !== undefined) {
    context.addIssue({ code: "custom", message: "interrupted_run notification must not carry question payload" });
  }
  if (value.approval !== null && value.approval !== undefined) {
    context.addIssue({ code: "custom", message: "interrupted_run notification must not carry approval payload" });
  }
  validateInterruptedRunTarget(value, context);
}

function validateInterruptedRunTarget(
  value: z.infer<typeof notificationWireSchema>,
  context: z.RefinementCtx,
): void {
  if (value.target.kind !== "workflow_task") {
    context.addIssue({ code: "custom", message: "interrupted_run target must be workflow_task" });
    return;
  }
  const { focus } = value.target;
  if (focus.kind !== "interrupted_run") {
    context.addIssue({ code: "custom", message: "interrupted_run workflow-task focus kind must be interrupted_run" });
    return;
  }
  if (
    value.interrupted_run !== null &&
    value.interrupted_run !== undefined &&
    (value.interrupted_run.runID !== value.target.run_id ||
      value.interrupted_run.runID !== focus.run_id)
  ) {
    context.addIssue({ code: "custom", message: "interrupted_run ids must match target and focus" });
  }
}

function stringListSubset(values: readonly string[], allowed: readonly string[]): boolean {
  return values.every((value) => allowed.includes(value));
}

function sameStringSet(left: readonly string[], right: readonly string[]): boolean {
  return stringListSubset(left, right) && stringListSubset(right, left);
}
