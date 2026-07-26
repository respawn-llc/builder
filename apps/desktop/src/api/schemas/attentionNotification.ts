import { z } from "zod";

import type {
  AttentionNotification,
  AttentionNotificationEvent,
  AttentionNotificationEventParams,
  AttentionNotificationTarget,
  AttentionNotificationTaskDetailFocus,
} from "../attentionNotifications";

const id = z.string().min(1);
const ids = z.array(id);
const nonEmptyIDs = ids.min(1);
const notificationKind = z.enum(["question", "approval", "interrupted_current_node"]);

const identifierSchema = z.object({ kind: notificationKind, uuid: id }).strict();
const questionFocusSchema = z.object({ kind: z.literal("question"), ask_ids: nonEmptyIDs }).strict();
const approvalFocusSchema = z.object({ kind: z.literal("approval"), approval_id: id }).strict();
const interruptedFocusSchema = z.object({ kind: z.literal("interrupted_current_node") }).strict();
const focusSchema = z.discriminatedUnion("kind", [questionFocusSchema, approvalFocusSchema, interruptedFocusSchema]);

function focus(value: z.infer<typeof focusSchema>): AttentionNotificationTaskDetailFocus {
  switch (value.kind) {
    case "question":
      return { kind: value.kind, askIDs: value.ask_ids };
    case "approval":
      return { kind: value.kind, approvalID: value.approval_id };
    case "interrupted_current_node":
      return { kind: value.kind };
  }
}

const targetSchema = z.discriminatedUnion("kind", [
  z
    .object({
      kind: z.literal("workflow_task"),
      project_id: id.optional(),
      workflow_id: id.optional(),
      task_id: id,
      task_short_id: id.optional(),
      task_title: z.string().optional(),
      session_id: id.optional(),
      current_node_id: id.optional(),
      current_node_branch_key: id.optional(),
      focus: focusSchema,
    })
    .strict()
    .transform(
      (value): AttentionNotificationTarget => ({
        kind: value.kind,
        projectID: value.project_id,
        workflowID: value.workflow_id,
        taskID: value.task_id,
        taskShortID: value.task_short_id,
        taskTitle: value.task_title,
        sessionID: value.session_id,
        currentNodeID: value.current_node_id,
        currentNodeBranchKey: value.current_node_branch_key,
        focus: focus(value.focus),
      }),
    ),
  z
    .object({ kind: z.literal("session_prompt"), session_id: id })
    .strict()
    .transform((value): AttentionNotificationTarget => ({ kind: value.kind, sessionID: value.session_id })),
]);

const questionStateSchema = z
  .object({
    prepared_ask_ids: ids,
    materialized_ask_ids: ids,
    current_unresolved_ask_ids: ids,
    skipped_ask_ids: ids,
    preview: z.string().optional(),
    display_count: z.number().int().nonnegative(),
    materialized_count: z.number().int().nonnegative(),
  })
  .strict()
  .transform((value) => ({
    preparedAskIDs: value.prepared_ask_ids,
    materializedAskIDs: value.materialized_ask_ids,
    currentUnresolvedAskIDs: value.current_unresolved_ask_ids,
    skippedAskIDs: value.skipped_ask_ids,
    preview: value.preview,
    displayCount: value.display_count,
    materializedCount: value.materialized_count,
  }));

const notificationPayloadSchema = z
  .object({
    id: identifierSchema,
    kind: notificationKind,
    occurred_at: id,
    revision: z.number().int().positive(),
    question: questionStateSchema.nullish(),
    approval: z.object({ approval_id: id, message: z.string().optional() }).strict().nullish(),
    interrupted_current_node: z
      .object({ message: z.string().optional(), reason: z.string().optional(), detail_json: z.string().optional() })
      .strict()
      .nullish(),
    target: targetSchema,
  })
  .strict();

type NotificationPayload = z.output<typeof notificationPayloadSchema>;

const notificationSchema: z.ZodType<AttentionNotification> = notificationPayloadSchema
  .superRefine(validateNotificationCoherence)
  .transform((value) => ({
    id: value.id,
    kind: value.kind,
    occurredAt: value.occurred_at,
    revision: value.revision,
    question: value.question ?? null,
    approval: value.approval == null ? null : { approvalID: value.approval.approval_id, message: value.approval.message },
    interruptedCurrentNode:
      value.interrupted_current_node == null
        ? null
        : {
            message: value.interrupted_current_node.message,
            reason: value.interrupted_current_node.reason,
            detailJSON: value.interrupted_current_node.detail_json,
          },
    target: value.target,
  }));

export const attentionNotificationEventSchema: z.ZodType<AttentionNotificationEvent> = z.discriminatedUnion("type", [
  z
    .object({ type: z.literal("pending"), sequence: z.number().int().positive(), source: z.enum(["live", "snapshot"]), pending: notificationSchema })
    .strict(),
  z
    .object({ type: z.literal("resolved"), sequence: z.number().int().positive(), source: z.enum(["live", "snapshot"]), id: identifierSchema, kind: notificationKind, occurred_at: id })
    .strict()
    .transform((value) => ({ ...value, occurredAt: value.occurred_at })),
  z
    .object({ type: z.literal("snapshot_complete"), sequence: z.number().int().positive(), source: z.literal("snapshot"), session_id: id })
    .strict()
    .transform((value) => ({ ...value, sessionID: value.session_id })),
]);

export const attentionNotificationEventParamsSchema: z.ZodType<AttentionNotificationEventParams> = z
  .object({ event: attentionNotificationEventSchema })
  .strict();

function sameIDs(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

function validateNotificationCoherence(value: NotificationPayload, context: z.RefinementCtx): void {
  if (value.id.kind !== value.kind) {
    context.addIssue({ code: "custom", message: "notification kind mismatch" });
    return;
  }
  switch (value.kind) {
    case "question":
      validateQuestionNotification(value, context);
      return;
    case "approval":
      validateApprovalNotification(value, context);
      return;
    case "interrupted_current_node":
      validateInterruptedCurrentNodeNotification(value, context);
  }
}

function validateQuestionNotification(value: NotificationPayload, context: z.RefinementCtx): void {
  if (value.question == null) {
    context.addIssue({ code: "custom", message: "question state required" });
  }
  if (value.approval != null || value.interrupted_current_node != null) {
    context.addIssue({ code: "custom", message: "question notification has unrelated payload" });
  }
  if (value.target.kind !== "workflow_task" || value.target.focus.kind !== "question") {
    context.addIssue({ code: "custom", message: "question notification target mismatch" });
    return;
  }
  if (value.question != null && !sameIDs(value.question.currentUnresolvedAskIDs, value.target.focus.askIDs)) {
    context.addIssue({ code: "custom", message: "question notification IDs do not match target focus" });
  }
}

function validateApprovalNotification(value: NotificationPayload, context: z.RefinementCtx): void {
  if (value.approval == null) {
    context.addIssue({ code: "custom", message: "approval state required" });
  }
  if (value.question != null || value.interrupted_current_node != null) {
    context.addIssue({ code: "custom", message: "approval notification has unrelated payload" });
  }
  if (value.target.kind !== "workflow_task" || value.target.focus.kind !== "approval") {
    context.addIssue({ code: "custom", message: "approval notification target mismatch" });
    return;
  }
  if (value.approval != null && value.approval.approval_id !== value.target.focus.approvalID) {
    context.addIssue({ code: "custom", message: "approval notification ID does not match target focus" });
  }
}

function validateInterruptedCurrentNodeNotification(
  value: NotificationPayload,
  context: z.RefinementCtx,
): void {
  if (value.interrupted_current_node == null) {
    context.addIssue({ code: "custom", message: "interrupted-current-node state required" });
  }
  if (value.question != null || value.approval != null) {
    context.addIssue({ code: "custom", message: "interrupted-current-node notification has unrelated payload" });
  }
  if (
    value.target.kind !== "workflow_task" ||
    value.target.focus.kind !== "interrupted_current_node" ||
    value.target.currentNodeID === undefined
  ) {
    context.addIssue({ code: "custom", message: "interrupted-current-node notification target mismatch" });
  }
}
