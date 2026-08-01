import { z } from "zod";

import type {
  TaskDependencies,
  TaskDependencyDirection,
  TaskDependencyDirectionProjection,
  TaskDependencyItem,
  TaskDependencyListDirection,
  TaskDependencyListResponse,
  TaskDependencyMutationResponse,
} from "../models";
import { nonBlankString, taskStatusSchema } from "./common";

const dependencyItemShape = {
  task_id: nonBlankString,
  short_id: nonBlankString,
  title: nonBlankString,
  workflow_id: nonBlankString,
  status: taskStatusSchema,
};

const blockedByDependencyItemSchema: z.ZodType<TaskDependencyItem> = z
  .object({
    ...dependencyItemShape,
    satisfaction: z.enum(["satisfied", "unsatisfied"]),
  })
  .strict()
  .transform((value) => ({
    taskID: value.task_id,
    shortID: value.short_id,
    title: value.title,
    workflowID: value.workflow_id,
    status: value.status,
    satisfaction: value.satisfaction,
  }));

const blocksDependencyItemSchema: z.ZodType<TaskDependencyItem> = z
  .object(dependencyItemShape)
  .strict()
  .transform((value) => ({
    taskID: value.task_id,
    shortID: value.short_id,
    title: value.title,
    workflowID: value.workflow_id,
    status: value.status,
    satisfaction: null,
  }));

const addAvailabilitySchema = z.union([
  z
    .object({
      available: z.object({ remaining_capacity: z.number().int().positive() }).strict(),
    })
    .strict()
    .transform((value) => ({
      kind: "available" as const,
      remainingCapacity: value.available.remaining_capacity,
    })),
  z
    .object({
      limit_reached: z.object({}).strict(),
    })
    .strict()
    .transform(() => ({ kind: "limit_reached" as const })),
]);

const blockedByDirectionShape = {
  direction: z.literal("blocked-by"),
  total_count: z.number().int().nonnegative(),
  unsatisfied_count: z.number().int().nonnegative(),
  items: z.array(blockedByDependencyItemSchema),
};
const blocksDirectionShape = {
  direction: z.literal("blocks"),
  total_count: z.number().int().nonnegative(),
  items: z.array(blocksDependencyItemSchema),
};

function validateDependencyDirectionCounts(
  value: Readonly<{
    total_count: number;
    unsatisfied_count?: number | undefined;
    items: readonly unknown[];
  }>,
  context: z.RefinementCtx,
): void {
  if (value.total_count !== value.items.length) {
    context.addIssue({
      code: "custom",
      message: "total_count must match items",
      path: ["total_count"],
    });
  }
  if (value.unsatisfied_count !== undefined && value.unsatisfied_count > value.total_count) {
    context.addIssue({
      code: "custom",
      message: "unsatisfied_count exceeds total_count",
      path: ["unsatisfied_count"],
    });
  }
}

const blockedByListDirectionSchema = z
  .object(blockedByDirectionShape)
  .strict()
  .superRefine(validateDependencyDirectionCounts)
  .transform((value): TaskDependencyListDirection => ({
    direction: value.direction,
    totalCount: value.total_count,
    unsatisfiedCount: value.unsatisfied_count,
    items: value.items,
  }));
const blocksListDirectionSchema = z
  .object(blocksDirectionShape)
  .strict()
  .superRefine(validateDependencyDirectionCounts)
  .transform((value): TaskDependencyListDirection => ({
    direction: value.direction,
    totalCount: value.total_count,
    unsatisfiedCount: null,
    items: value.items,
  }));

const dependencyListDirectionSchema = z.union([blockedByListDirectionSchema, blocksListDirectionSchema]);

const blockedByDetailDirectionSchema = z
  .object({ ...blockedByDirectionShape, add_availability: addAvailabilitySchema })
  .strict()
  .superRefine(validateDependencyDirectionCounts)
  .transform((value): TaskDependencyDirectionProjection => ({
    direction: value.direction,
    totalCount: value.total_count,
    unsatisfiedCount: value.unsatisfied_count,
    items: value.items,
    addAvailability: value.add_availability,
  }));
const blocksDetailDirectionSchema = z
  .object({ ...blocksDirectionShape, add_availability: addAvailabilitySchema })
  .strict()
  .superRefine(validateDependencyDirectionCounts)
  .transform((value): TaskDependencyDirectionProjection => ({
    direction: value.direction,
    totalCount: value.total_count,
    unsatisfiedCount: null,
    items: value.items,
    addAvailability: value.add_availability,
  }));

const dependencyDetailDirectionSchema = z.union([
  blockedByDetailDirectionSchema,
  blocksDetailDirectionSchema,
]);

export const taskDependenciesSchema: z.ZodType<TaskDependencies> = z
  .object({
    blocker_count: z.number().int().nonnegative(),
    unsatisfied_blocker_count: z.number().int().nonnegative(),
    directly_blocked_task_count: z.number().int().nonnegative(),
    directions: z.array(dependencyDetailDirectionSchema).length(2),
  })
  .strict()
  .superRefine((value, context) => {
    const blockedBy = value.directions.find((direction) => direction.direction === "blocked-by");
    const blocks = value.directions.find((direction) => direction.direction === "blocks");
    if (blockedBy === undefined || blocks === undefined) {
      context.addIssue({
        code: "custom",
        message: "both directions are required",
        path: ["directions"],
      });
      return;
    }
    if (
      blockedBy.totalCount !== value.blocker_count ||
      blockedBy.unsatisfiedCount !== value.unsatisfied_blocker_count ||
      blocks.totalCount !== value.directly_blocked_task_count
    ) {
      context.addIssue({
        code: "custom",
        message: "dependency summary mismatch",
        path: ["directions"],
      });
    }
  })
  .transform((value) => ({
    blockerCount: value.blocker_count,
    unsatisfiedBlockerCount: value.unsatisfied_blocker_count,
    directlyBlockedTaskCount: value.directly_blocked_task_count,
    directions: value.directions,
  }));

const dependencyMutationResponseSchema = z
  .object({
    outcome: z.enum(["added", "already_present", "removed", "already_absent"]),
    blocker_task_id: nonBlankString,
    blocker_short_id: nonBlankString,
    blocked_task_id: nonBlankString,
    blocked_short_id: nonBlankString,
  })
  .strict()
  .transform((value): TaskDependencyMutationResponse => ({
    outcome: value.outcome,
    blockerTaskID: value.blocker_task_id,
    blockerShortID: value.blocker_short_id,
    blockedTaskID: value.blocked_task_id,
    blockedShortID: value.blocked_short_id,
  }));

export const taskDependencyAddResponseSchema = dependencyMutationResponseSchema.refine(
  (value) => value.outcome === "added" || value.outcome === "already_present",
);
export const taskDependencyRemoveResponseSchema = dependencyMutationResponseSchema.refine(
  (value) => value.outcome === "removed" || value.outcome === "already_absent",
);

export const taskDependencyListResponseSchema: z.ZodType<TaskDependencyListResponse> = z
  .object({
    task_id: nonBlankString,
    short_id: nonBlankString,
    directions: z.array(dependencyListDirectionSchema).max(2),
  })
  .strict()
  .superRefine((value, context) => {
    const seen = new Set<TaskDependencyDirection>();
    for (const [index, direction] of value.directions.entries()) {
      if (seen.has(direction.direction)) {
        context.addIssue({
          code: "custom",
          message: "duplicate direction",
          path: ["directions", index],
        });
      }
      seen.add(direction.direction);
    }
  })
  .transform((value) => ({
    taskID: value.task_id,
    shortID: value.short_id,
    directions: value.directions,
  }));
