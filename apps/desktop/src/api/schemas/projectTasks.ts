import { z } from "zod";

import type { ProjectTaskGroupCounts, TaskListPage } from "../workflowLabels";
import { projectLabelSchema } from "./workflowLabels";
import { coherentTaskStatusSchema, workflowIDSchema } from "./common";

const taskListPageWireSchema = z
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
          status: coherentTaskStatusSchema,
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
  .strict();

export function taskListPageSchemaForRequest(
  projectID: string,
  workflowID: string | undefined,
): z.ZodType<TaskListPage> {
  return taskListPageWireSchema
    .superRefine((value, context) => {
      if (value.scope.project_id !== projectID) {
        context.addIssue({
          code: "custom",
          message: "response Project scope does not match request",
          path: ["scope", "project_id"],
        });
      }
      if (value.scope.workflow_id !== workflowID) {
        context.addIssue({
          code: "custom",
          message: "response Workflow scope does not match request",
          path: ["scope", "workflow_id"],
        });
      }
      if (value.matching_workflow_cardinality === "none" && value.tasks.length > 0) {
        context.addIssue({
          code: "custom",
          message: "none cardinality cannot contain Tasks",
          path: ["matching_workflow_cardinality"],
        });
      }
      if (workflowID !== undefined && value.matching_workflow_cardinality === "multiple") {
        context.addIssue({
          code: "custom",
          message: "Workflow-narrowed response cannot match multiple Workflows",
          path: ["matching_workflow_cardinality"],
        });
      }

      const taskWorkflowIDs = new Set(value.tasks.map((task) => task.workflowID));
      if (value.matching_workflow_cardinality === "one" && taskWorkflowIDs.size > 1) {
        context.addIssue({
          code: "custom",
          message: "one cardinality cannot contain Tasks from multiple Workflows",
          path: ["tasks"],
        });
      }
      if (workflowID !== undefined && value.tasks.some((task) => task.workflowID !== workflowID)) {
        context.addIssue({
          code: "custom",
          message: "Task Workflow does not match response scope",
          path: ["tasks"],
        });
      }
    })
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
}

export const projectTaskGroupCountsSchema: z.ZodType<ProjectTaskGroupCounts> = z
  .object({
    project_id: z.string().min(1),
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
    counts: value.counts,
    generatedAt: value.generated_at_unix_ms,
  }));
