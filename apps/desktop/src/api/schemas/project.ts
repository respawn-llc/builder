import { z } from "zod";

import type {
  BindingPlan,
  ProjectEdit,
  ProjectDeleteResponse,
  ProjectMutationResponse,
  ProjectPage,
  ProjectSummary,
  ProjectWorkspaceAttachResponse,
  ProjectWorkspaceResult,
  WorkspaceCatalogPage,
  WorkspaceCatalogRow,
  WorkspaceUnlinkResponse,
} from "../models";
import { projectBindingSchema, workflowIDSchema, workspaceSummarySchema } from "./common";
import { canonicalProjectIDSchema, workspaceOffsetSchema } from "./catalog";

export const workspaceCatalogRowSchema: z.ZodType<WorkspaceCatalogRow> = z
  .object({
    workspace_id: canonicalProjectIDSchema,
    display_name: z.string(),
    root_path: z.string().min(1),
    is_default: z.boolean(),
  })
  .strict()
  .transform((value) => ({
    id: value.workspace_id,
    name: value.display_name,
    rootPath: value.root_path,
    isDefault: value.is_default,
  }));

export const projectSummarySchema = z
  .object({
    project_id: z.string(),
    project_key: z.string(),
    display_name: z.string(),
    primary_workspace: workspaceSummarySchema,
    default_workflow_id: workflowIDSchema.nullable(),
    default_workflow_name: z.string().optional().default(""),
    default_workflow_valid: z.boolean(),
    updated_at_unix_ms: z.number(),
    task_count: z.number(),
    attention_count: z.number(),
    workflow_count: z.number(),
  })
  .transform((value): ProjectSummary => ({
    id: value.project_id,
    key: value.project_key,
    name: value.display_name,
    primaryWorkspace: value.primary_workspace,
    defaultWorkflowID: value.default_workflow_id,
    defaultWorkflowName: value.default_workflow_name,
    defaultWorkflowValid: value.default_workflow_valid,
    updatedAt: value.updated_at_unix_ms,
    taskCount: value.task_count,
    attentionCount: value.attention_count,
    workflowCount: value.workflow_count,
  }));

export const projectPageSchema: z.ZodType<ProjectPage> = z
  .object({
    projects: z.array(projectSummarySchema),
    next_page_token: z.string().optional().default(""),
    generated_at_unix_ms: z.number(),
  })
  .transform((value) => ({
    projects: value.projects,
    nextPageToken: value.next_page_token,
    generatedAt: value.generated_at_unix_ms,
  }));

export const workspaceCatalogPageSchema: z.ZodType<WorkspaceCatalogPage> = z
  .object({
    project_id: canonicalProjectIDSchema,
    offset: workspaceOffsetSchema,
    workspaces: z.array(workspaceCatalogRowSchema).max(100),
    next_offset: workspaceOffsetSchema.nullable(),
  })
  .strict()
  .transform((value) => ({
    projectID: value.project_id,
    offset: value.offset,
    workspaces: value.workspaces,
    nextOffset: value.next_offset,
  }));

export const projectEditSchema: z.ZodType<ProjectEdit> = z
  .object({
    project_id: canonicalProjectIDSchema,
    project_key: z.string(),
    display_name: z.string(),
  })
  .strict()
  .transform((value) => ({
    projectID: value.project_id,
    projectKey: value.project_key,
    displayName: value.display_name,
  }));

export const projectWorkspaceResultSchema: z.ZodType<
  Readonly<{ projectID: string; result: ProjectWorkspaceResult }>
> = z
  .discriminatedUnion("result", [
    z
      .object({
        project_id: canonicalProjectIDSchema,
        result: z.literal("attached"),
        workspace: workspaceCatalogRowSchema,
      })
      .strict(),
    z
      .object({
        project_id: canonicalProjectIDSchema,
        result: z.literal("not_attached"),
        workspace: z.null(),
      })
      .strict(),
  ])
  .transform((value) => ({
    projectID: value.project_id,
    result:
      value.result === "attached"
        ? { kind: "attached" as const, workspace: value.workspace }
        : { kind: "not_attached" as const },
  }));

export const projectWorkspaceAttachResponseSchema: z.ZodType<ProjectWorkspaceAttachResponse> = z
  .object({
    binding: projectBindingSchema,
    outcome: z.enum(["attached", "already_attached"]),
  })
  .strict()
  .transform((value) => ({ binding: value.binding, outcome: value.outcome }));

export const projectMutationResponseSchema: z.ZodType<ProjectMutationResponse> = z
  .object({
    project: projectSummarySchema,
  })
  .transform((value) => ({
    project: value.project,
  }));

export const workspaceUnlinkResponseSchema: z.ZodType<WorkspaceUnlinkResponse> = z
  .object({
    project_id: z.string(),
    workspace_id: z.string(),
    blockers: z
      .array(
        z.object({
          code: z.string(),
          message: z.string(),
          count: z.number().optional().default(0),
        }),
      )
      .nullish()
      .transform((value) => value ?? []),
    project: projectSummarySchema.nullish(),
  })
  .transform((value) => ({
    projectID: value.project_id,
    workspaceID: value.workspace_id,
    blockers: value.blockers,
    project: value.project ?? null,
  }));

export const projectDeleteResponseSchema: z.ZodType<ProjectDeleteResponse> = z
  .object({
    project_id: z.string(),
    deleted: z.boolean(),
    blockers: z
      .array(
        z.object({
          code: z.string(),
          message: z.string(),
          count: z.number().optional().default(0),
        }),
      )
      .nullish()
      .transform((value) => value ?? []),
  })
  .transform((value) => ({
    projectID: value.project_id,
    deleted: value.deleted,
    blockers: value.blockers,
  }));

export const bindingPlanSchema: z.ZodType<BindingPlan> = z
  .object({
    kind: z.string(),
    canonical_root: z.string().optional().default(""),
    binding: projectBindingSchema.nullish(),
  })
  .transform((value) => ({
    kind: value.kind,
    canonicalRoot: value.canonical_root,
    binding: value.binding ?? null,
  }));

export const projectCreateSchema = z
  .object({
    binding: projectBindingSchema,
  })
  .transform((value) => value.binding);
