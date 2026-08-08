import { z } from "zod";

import type { TaskStatusKind } from "../models";
import type {
  TaskSearchFTS5Hit,
  TaskSearchGroup,
  TaskSearchLiteralHit,
  TaskSearchResponse,
  TaskSearchSource,
} from "../taskSearch";
import { taskStatusSchema } from "./common";

const exactNonBlankString = z
  .string()
  .min(1)
  .refine((value) => value.trim() === value);

const nativeStateByKind = {
  done: "terminal",
  waiting_question: "waiting_ask",
  waiting_approval: "waiting_approval",
  interrupted: "interrupted",
  running: "running",
  queued: "queued",
  backlog: "active",
  active: "active",
} as const satisfies Readonly<Record<TaskStatusKind, string>>;

const taskSearchStatusSchema = taskStatusSchema.superRefine((value, context) => {
  if (value.nativeState !== nativeStateByKind[value.kind]) {
    context.addIssue({
      code: "custom",
      message: "native state does not match kind",
      path: ["nativeState"],
    });
  }
});

const titleOrBodySourceSchema = z
  .object({ kind: z.enum(["title", "body"]) })
  .strict()
  .transform((value): TaskSearchSource => value);

const shortIDSourceSchema = z
  .object({ kind: z.literal("short_id") })
  .strict()
  .transform((value): TaskSearchSource => value);

const commentSourceSchema = z
  .object({
    kind: z.literal("comment"),
    comment_id: exactNonBlankString,
  })
  .strict()
  .transform((value): TaskSearchSource => ({
    kind: value.kind,
    commentID: value.comment_id,
  }));

const literalSourceSchema = z.union([
  shortIDSourceSchema,
  titleOrBodySourceSchema,
  commentSourceSchema,
]);
const fts5SourceSchema = z.union([titleOrBodySourceSchema, commentSourceSchema]);

const literalHitSchema: z.ZodType<TaskSearchLiteralHit> = z
  .object({
    ordinal: z.number().int().positive(),
    source: literalSourceSchema,
    literal: z
      .object({
        before: z.string(),
        match: z.string().min(1),
        after: z.string(),
        left_truncated: z.boolean(),
        right_truncated: z.boolean(),
      })
      .strict(),
  })
  .strict()
  .transform((value) => ({
    ordinal: value.ordinal,
    source: value.source,
    literal: {
      before: value.literal.before,
      match: value.literal.match,
      after: value.literal.after,
      leftTruncated: value.literal.left_truncated,
      rightTruncated: value.literal.right_truncated,
    },
  }));

const fts5HitSchema: z.ZodType<TaskSearchFTS5Hit> = z
  .object({
    ordinal: z.number().int().positive(),
    source: fts5SourceSchema,
    fts5: z.object({ snippet: z.string().min(1) }).strict(),
  })
  .strict();

function groupSchema(
  hitSchema: z.ZodType<TaskSearchLiteralHit> | z.ZodType<TaskSearchFTS5Hit>,
): z.ZodType<TaskSearchGroup> {
  return z
    .object({
      project_id: exactNonBlankString,
      project_key: exactNonBlankString,
      task_id: exactNonBlankString,
      short_id: exactNonBlankString,
      workflow_id: exactNonBlankString,
      title: exactNonBlankString,
      status: taskSearchStatusSchema,
      total_hit_count: z.number().int().positive(),
      hits: z.array(hitSchema).min(1),
    })
    .strict()
    .superRefine((value, context) => {
      if (value.total_hit_count < value.hits.length) {
        context.addIssue({
          code: "custom",
          message: "total_hit_count is smaller than returned hits",
          path: ["total_hit_count"],
        });
      }
      let previousOrdinal = 0;
      for (const [index, hit] of value.hits.entries()) {
        if (hit.ordinal <= previousOrdinal || hit.ordinal > value.total_hit_count) {
          context.addIssue({
            code: "custom",
            message: "hit ordinals must be strictly ascending and in range",
            path: ["hits", index, "ordinal"],
          });
        }
        previousOrdinal = hit.ordinal;
      }
    })
    .transform((value) => ({
      projectID: value.project_id,
      projectKey: value.project_key,
      taskID: value.task_id,
      shortID: value.short_id,
      workflowID: value.workflow_id,
      title: value.title,
      status: value.status,
      totalHitCount: value.total_hit_count,
      hits: value.hits,
    }));
}

function responseSchema(
  mode: "literal" | "fts5",
  hitSchema: z.ZodType<TaskSearchLiteralHit> | z.ZodType<TaskSearchFTS5Hit>,
): z.ZodType<TaskSearchResponse> {
  return z
    .object({
      mode: z.literal(mode),
      groups: z.array(groupSchema(hitSchema)),
      next_offset: z.number().int().positive().nullish(),
    })
    .strict()
    .superRefine((value, context) => {
      const taskIDs = new Set<string>();
      for (const [index, group] of value.groups.entries()) {
        if (taskIDs.has(group.taskID)) {
          context.addIssue({
            code: "custom",
            message: "task groups must be unique",
            path: ["groups", index, "task_id"],
          });
        }
        taskIDs.add(group.taskID);
      }
    })
    .transform((value) => ({
      mode: value.mode,
      groups: value.groups,
      nextOffset: value.next_offset ?? null,
    }));
}

export const taskSearchResponseSchema: z.ZodType<TaskSearchResponse> = z.union([
  responseSchema("literal", literalHitSchema),
  responseSchema("fts5", fts5HitSchema),
]);
