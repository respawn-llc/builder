import { z } from "zod";

import type {
  ProjectTaskGroup,
  ProjectTaskGroupCounts,
  ProjectTaskGroupDefinition,
  TaskListPage,
} from "../workflowLabels";
import { projectLabelSchema } from "./workflowLabels";
import { taskStatusKindSchema, taskStatusSchema, workflowIDSchema } from "./common";

export const taskListPageSchema: z.ZodType<TaskListPage> = z
  .object({
    scope: z
      .object({
        project_id: z.string().min(1),
        workflow_id: workflowIDSchema.optional(),
      })
      .strict(),
    matching_workflow_cardinality: z.enum(["none", "one", "multiple"]),
    next_offset: z.number().int().positive().nullable().optional(),
    generated_at_unix_ms: z.number(),
    tasks: z.array(
      z
        .object({
          task_id: z.string().min(1),
          short_id: z.string().min(1),
          workflow_id: workflowIDSchema,
          workflow_name: z.string().min(1).optional(),
          title: z.string(),
          created_at_unix_ms: z.number(),
          updated_at_unix_ms: z.number(),
          column_keys: z.array(z.string()).optional(),
          status: taskStatusSchema,
          labels: z.array(projectLabelSchema),
          dependency_progress: z
            .object({
              satisfied_count: z.number().int().nonnegative(),
              total_count: z.number().int().positive(),
            })
            .strict()
            .refine((value) => value.satisfied_count <= value.total_count)
            .optional(),
        })
        .strict()
        .transform((value) => ({
          id: value.task_id,
          shortID: value.short_id,
          workflowID: value.workflow_id,
          workflowName: value.workflow_name ?? null,
          title: value.title,
          createdAt: value.created_at_unix_ms,
          updatedAt: value.updated_at_unix_ms,
          columnKeys: value.column_keys ?? null,
          status: value.status,
          labels: value.labels,
          dependencyProgress:
            value.dependency_progress === undefined
              ? null
              : {
                  satisfiedCount: value.dependency_progress.satisfied_count,
                  totalCount: value.dependency_progress.total_count,
                },
        })),
    ),
  })
  .strict()
  .transform((value) => ({
    scope: {
      projectID: value.scope.project_id,
      workflowID: value.scope.workflow_id ?? null,
    },
    matchingWorkflowCardinality: value.matching_workflow_cardinality,
    nextOffset: value.next_offset ?? null,
    generatedAt: value.generated_at_unix_ms,
    tasks: value.tasks,
  }));

const projectTaskGroupSchema: z.ZodType<ProjectTaskGroup> = z.enum(["active", "backlog", "done"]);

const projectTaskGroupDefinitionSchema: z.ZodType<ProjectTaskGroupDefinition> = z
  .object({
    group: projectTaskGroupSchema,
    status_kinds: z.array(taskStatusKindSchema).nonempty(),
  })
  .strict()
  .transform((value) => ({
    group: value.group,
    statusKinds: value.status_kinds,
  }));

export const projectTaskGroupCountsSchema: z.ZodType<ProjectTaskGroupCounts> = z
  .object({
    project_id: z.string().min(1),
    definitions: z.array(projectTaskGroupDefinitionSchema).length(3).refine((definitions) => new Set(definitions.map((definition) => definition.group)).size === 3),
    counts: z
      .object({
        active: z.number().int().nonnegative(),
        backlog: z.number().int().nonnegative(),
        done: z.number().int().nonnegative(),
      })
      .strict(),
    generated_at_unix_ms: z.number(),
  })
  .strict()
  .transform((value) => ({
    projectID: value.project_id,
    definitions: value.definitions,
    counts: value.counts,
    generatedAt: value.generated_at_unix_ms,
  }));
