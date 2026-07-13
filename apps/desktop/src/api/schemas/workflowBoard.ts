import { z } from "zod";

import type {
  ActivityPage,
  AttentionPage,
  BoardColumn,
  BoardGroup,
  BoardNodeCardsPage,
  CommentPage,
  PendingAsk,
  TaskDetail,
  TaskApproveResponse,
  TaskMoveResponse,
  TaskStartResponse,
  WorkflowExecutionTargetSelectionRequirement,
  WorkflowBoard,
  ProjectWorkflowLink,
} from "../models";
import {
  attentionItemSchema,
  boardCardSchema,
  boardColumnSchema,
  boardGroupSchema,
  commentSchema,
  emptyString,
  nullableString,
  runSchema,
  stringList,
  taskActionsSchema,
  taskStatusSchema,
  transitionSchema,
  workflowPickerItemSchema,
  workspaceSummarySchema,
} from "./common";
import { emptyArray } from "./workflowHelpers";
import { workflowExecutionTargetSchema } from "./workflowExecutionTarget";

const boardGroupsSchema = z
  .array(boardGroupSchema)
  .nullish()
  .transform((value) => value ?? []);
const boardColumnsSchema = z
  .array(boardColumnSchema)
  .nullish()
  .transform((value) => value ?? []);
const boardCardsSchema = z
  .array(boardCardSchema)
  .nullish()
  .transform((value) => value ?? []);
const workflowPickerSchema = z
  .array(workflowPickerItemSchema)
  .nullish()
  .transform((value) => value ?? []);

const unavailableCauseSchema = z.enum([
  "invalid_revision",
  "non_commit",
  "default_branch_missing",
  "default_branch_ambiguous",
  "git_failure",
]);

const configuredTargetSchema = z
  .discriminatedUnion("mode", [
    z.object({ mode: z.literal("head") }).strict(),
    z.object({ mode: z.literal("default_branch") }).strict(),
    z.object({ mode: z.literal("custom_ref"), requested_ref: z.string().trim().min(1) }).strict(),
  ])
  .transform((value) => ({
    mode: value.mode,
    requestedRef: value.mode === "custom_ref" ? value.requested_ref : null,
  }));

const selectionRequirementSchema: z.ZodType<WorkflowExecutionTargetSelectionRequirement> = z
  .discriminatedUnion("reason", [
    z.object({ reason: z.literal("policy_requires_selection") }).strict(),
    z
      .object({
        reason: z.literal("configured_target_unavailable"),
        configured_target: configuredTargetSchema,
        unavailable_cause: unavailableCauseSchema,
      })
      .strict(),
  ])
  .transform((value): WorkflowExecutionTargetSelectionRequirement => {
    if (value.reason === "policy_requires_selection") {
      return { reason: value.reason };
    }
    return {
      reason: value.reason,
      configuredTarget: value.configured_target,
      unavailableCause: value.unavailable_cause,
    };
  });

const selectionRequiredResponseSchema = z
  .object({
    outcome: z.literal("selection_required"),
    selection_required: selectionRequirementSchema,
  })
  .strict()
  .transform(
    (value) =>
      ({
        outcome: value.outcome,
        selectionRequired: value.selection_required,
      }) as const,
  );

export const taskStartResponseSchema: z.ZodType<TaskStartResponse> = z.discriminatedUnion("outcome", [
  z
    .object({
      outcome: z.literal("applied"),
      applied: z
        .object({
          transition_id: z.string().trim().min(1),
          placement_id: z.string().trim().min(1),
          run_id: z.string().trim().min(1),
        })
        .strict(),
    })
    .strict()
    .transform(
      (value) =>
        ({
          outcome: value.outcome,
          applied: {
            transitionID: value.applied.transition_id,
            placementID: value.applied.placement_id,
            runID: value.applied.run_id,
          },
        }) as const,
    ),
  selectionRequiredResponseSchema,
]);

export const taskMoveResponseSchema: z.ZodType<TaskMoveResponse> = z.discriminatedUnion("outcome", [
  z
    .object({
      outcome: z.literal("applied"),
      applied: z
        .object({
          transition_id: z.string().trim().min(1),
          state: z.string().trim().min(1),
          placement_ids: stringList,
          run_ids: stringList,
        })
        .strict(),
    })
    .strict()
    .transform(
      (value) =>
        ({
          outcome: value.outcome,
          applied: {
            transitionID: value.applied.transition_id,
            state: value.applied.state,
            placementIDs: value.applied.placement_ids,
            runIDs: value.applied.run_ids,
          },
        }) as const,
    ),
  selectionRequiredResponseSchema,
]);

export const taskApproveResponseSchema: z.ZodType<TaskApproveResponse> = z.discriminatedUnion("outcome", [
  z
    .object({
      outcome: z.literal("applied"),
      applied: z
        .object({
          transition_id: z.string().trim().min(1),
          task_id: z.string().trim().min(1),
          state: z.string().trim().min(1),
          placement_ids: stringList,
          run_ids: stringList,
        })
        .strict(),
    })
    .strict()
    .transform(
      (value) =>
        ({
          outcome: value.outcome,
          applied: {
            transitionID: value.applied.transition_id,
            taskID: value.applied.task_id,
            state: value.applied.state,
            placementIDs: value.applied.placement_ids,
            runIDs: value.applied.run_ids,
          },
        }) as const,
    ),
  selectionRequiredResponseSchema,
]);

export const projectWorkflowLinksSchema: z.ZodType<readonly ProjectWorkflowLink[]> = z
  .object({
    links: z
      .array(
        z
          .object({
            id: z.string(),
            project_id: z.string(),
            workflow_id: z.string(),
            default: z.boolean(),
          })
          .transform((value) => ({
            id: value.id,
            projectID: value.project_id,
            workflowID: value.workflow_id,
            isDefault: value.default,
          })),
      )
      .nullish()
      .transform(emptyArray),
  })
  .transform((value) => value.links);

export const workflowBoardSchema: z.ZodType<WorkflowBoard> = z
  .object({
    board: z
      .object({
        project_id: z.string(),
        project: z
          .object({
            project_key: z.string(),
            display_name: z.string(),
            default_workspace_id: z.string().min(1),
            attached_workspace_count: z.number().int().positive(),
          })
          .strict(),
        selected_workflow: workflowPickerItemSchema,
        workflows: workflowPickerSchema,
        groups: boardGroupsSchema,
        columns: boardColumnsSchema,
        generated_at_unix_ms: z.number(),
      })
      .strict(),
  })
  .strict()
  .transform((value) => {
    const columns = visibleBoardColumns(value.board.columns);
    return {
      projectID: value.board.project_id,
      projectKey: value.board.project.project_key,
      projectName: value.board.project.display_name,
      defaultWorkspaceID: value.board.project.default_workspace_id,
      attachedWorkspaceCount: value.board.project.attached_workspace_count,
      selectedWorkflow: value.board.selected_workflow,
      workflows: value.board.workflows,
      groups: visibleBoardGroups(value.board.groups, columns),
      columns,
      generatedAt: value.board.generated_at_unix_ms,
    };
  });

function visibleBoardColumns(columns: readonly BoardColumn[]): readonly BoardColumn[] {
  return columns.filter((column) => column.kind !== "join");
}

function visibleBoardGroups(
  groups: readonly BoardGroup[],
  columns: readonly BoardColumn[],
): readonly BoardGroup[] {
  const visibleNodeIDs = new Set(columns.map((column) => column.id));
  return groups
    .map((group) => ({
      ...group,
      nodeIDs: group.nodeIDs.filter((nodeID) => visibleNodeIDs.has(nodeID)),
    }))
    .filter((group) => group.nodeIDs.length > 0);
}

export const boardNodeCardsPageSchema: z.ZodType<BoardNodeCardsPage> = z
  .object({
    project_id: z.string(),
    workflow_id: z.string(),
    node_id: z.string(),
    cards: boardCardsSchema,
    previous_page_token: z.string().nullable(),
    next_page_token: z.string().nullable(),
    generated_at_unix_ms: z.number(),
  })
  .strict()
  .transform((value) => ({
    projectID: value.project_id,
    workflowID: value.workflow_id,
    nodeID: value.node_id,
    cards: value.cards,
    previousPageToken: value.previous_page_token,
    nextPageToken: value.next_page_token,
    generatedAt: value.generated_at_unix_ms,
  }));

export const attentionPageSchema: z.ZodType<AttentionPage> = z
  .object({
    items: z.array(attentionItemSchema),
    next_page_token: z.string().optional().default(""),
    generated_at_unix_ms: z.number(),
  })
  .transform((value) => ({
    items: value.items,
    nextPageToken: value.next_page_token,
    generatedAt: value.generated_at_unix_ms,
  }));

export const taskDetailSchema: z.ZodType<TaskDetail> = z
  .object({
    task: z.object({
      summary: z.object({
        id: z.string(),
        project_id: z.string(),
        workflow_id: z.string(),
        short_id: z.string(),
        title: z.string(),
        created_at_unix_ms: z.number(),
        updated_at_unix_ms: z.number(),
        done: z.boolean(),
        canceled_at_unix_ms: z.number().nullable().optional(),
        cancel_reason: nullableString,
      }),
      project: z.object({
        display_name: z.string(),
      }),
      workflow: workflowPickerItemSchema,
      body: emptyString,
      source_url: emptyString,
      source_workspace: workspaceSummarySchema,
      execution_target: workflowExecutionTargetSchema.optional().transform((value) => value ?? null),
      managed_worktree: z.never().optional(),
      status: taskStatusSchema,
      actions: taskActionsSchema,
      attention: z.array(attentionItemSchema).nullish().transform(emptyArray),
      runs: z.array(runSchema).nullish().transform(emptyArray),
      transitions: z.array(transitionSchema).nullish().transform(emptyArray),
      comments: z.array(commentSchema).nullish().transform(emptyArray),
    }),
  })
  .transform((value) => ({
    id: value.task.summary.id,
    shortID: value.task.summary.short_id,
    projectID: value.task.summary.project_id,
    projectName: value.task.project.display_name,
    workflowID: value.task.summary.workflow_id,
    workflowName: value.task.workflow.name,
    workflowVersion: value.task.workflow.version,
    title: value.task.summary.title,
    body: value.task.body,
    sourceURL: value.task.source_url,
    sourceWorkspace: value.task.source_workspace,
    status: value.task.status,
    actions: value.task.actions,
    attention: value.task.attention,
    comments: value.task.comments,
    runs: value.task.runs,
    transitions: value.task.transitions,
    executionTarget: value.task.execution_target,
    createdAt: value.task.summary.created_at_unix_ms,
    updatedAt: value.task.summary.updated_at_unix_ms,
    done: value.task.summary.done,
    canceledAt: value.task.summary.canceled_at_unix_ms ?? null,
    cancelReason: value.task.summary.cancel_reason,
  }));

export const activityPageSchema: z.ZodType<ActivityPage> = z
  .object({
    items: z.array(
      z
        .object({
          activity_id: z.string(),
          type: z.string(),
          task_id: z.string(),
          occurred_at_unix_ms: z.number(),
          updated_at_unix_ms: z.number(),
          actor: emptyString,
          summary: z.string(),
          comment: commentSchema.nullish(),
          transition: transitionSchema.nullish(),
          run: runSchema.nullish(),
          attention: attentionItemSchema.nullish(),
        })
        .transform((value) => ({
          id: value.activity_id,
          type: value.type,
          taskID: value.task_id,
          occurredAt: value.occurred_at_unix_ms,
          updatedAt: value.updated_at_unix_ms,
          actor: value.actor,
          summary: value.summary,
          comment: value.comment ?? null,
          transition: value.transition ?? null,
          run: value.run ?? null,
          attention: value.attention ?? null,
        })),
    ),
    next_page_token: z.string().optional().default(""),
    generated_at_unix_ms: z.number(),
  })
  .transform((value) => ({
    items: value.items,
    nextPageToken: value.next_page_token,
    generatedAt: value.generated_at_unix_ms,
  }));

export const pendingAskListSchema = z
  .object({
    Asks: z
      .array(
        z
          .object({
            AskID: z.string(),
            SessionID: z.string(),
            Question: z.string(),
            Suggestions: z.array(z.string()).optional().default([]),
            RecommendedOptionIndex: z.number().optional().default(0),
            CreatedAt: z.string().optional().default(""),
          })
          .transform((value): PendingAsk => ({
            askID: value.AskID,
            sessionID: value.SessionID,
            question: value.Question,
            suggestions: value.Suggestions,
            recommendedOptionIndex: value.RecommendedOptionIndex,
            createdAt: value.CreatedAt,
          })),
      )
      .optional()
      .default([]),
  })
  .transform((value) => value.Asks);

const taskSummaryResponseSchema = z.object({ task: z.object({ id: z.string() }) });

export const taskCreateResponseSchema = taskSummaryResponseSchema;
export const taskUpdateResponseSchema = taskSummaryResponseSchema;
export const commentAddResponseSchema = z.object({ comment: commentSchema });

export const commentPageSchema: z.ZodType<CommentPage> = z
  .object({
    comments: z.array(commentSchema).nullish().transform(emptyArray),
    next_page_token: z.string().optional().default(""),
  })
  .transform((value) => ({
    comments: value.comments,
    nextPageToken: value.next_page_token,
  }));
