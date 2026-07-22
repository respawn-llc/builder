import { z } from "zod";

import type { ProjectLabel, ProjectLabelCatalog, TaskLabelAssignment } from "../workflowLabels";
import { workflowLabelMaxIDs } from "../workflowLabelContract";

export const labelIDSchema = z.uuidv4();
export const labelIDListSchema = z
  .array(labelIDSchema)
  .max(workflowLabelMaxIDs)
  .superRefine((labelIDs, context) => {
    const seen = new Set<string>();
    for (const [index, labelID] of labelIDs.entries()) {
      if (seen.has(labelID)) {
        context.addIssue({
          code: "custom",
          message: "label IDs must be unique",
          path: [index],
        });
      }
      seen.add(labelID);
    }
  });

export const projectLabelSchema: z.ZodType<ProjectLabel> = z
  .object({
    id: labelIDSchema,
    name: z.string().min(1),
  })
  .strict();

export const projectLabelCatalogSchema: z.ZodType<ProjectLabelCatalog> = z
  .object({
    catalog: z
      .object({
        project_id: z.string().min(1),
        labels: z.array(projectLabelSchema).max(workflowLabelMaxIDs),
      })
      .strict(),
  })
  .strict()
  .transform((value) => ({
    projectID: value.catalog.project_id,
    labels: value.catalog.labels,
  }));

export const projectLabelMutationSchema = z
  .object({ label: projectLabelSchema })
  .strict()
  .transform((value) => value.label);

export const projectLabelDeleteSchema = z
  .object({ label_id: labelIDSchema })
  .strict()
  .transform((value) => value.label_id);

export const taskLabelAssignmentSchema: z.ZodType<TaskLabelAssignment> = z
  .object({
    assignment: z
      .object({
        task_id: z.string().min(1),
        label_ids: labelIDListSchema,
      })
      .strict(),
  })
  .strict()
  .transform((value) => ({
    taskID: value.assignment.task_id,
    labelIDs: value.assignment.label_ids,
  }));
