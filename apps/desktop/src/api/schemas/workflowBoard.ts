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
  TaskMoveResponse,
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
import { emptyArray, firstNonEmpty } from "./workflowHelpers";

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

export const taskMoveResponseSchema = z
  .object({
    transition_id: emptyString,
    state: emptyString,
    placement_ids: stringList,
    run_ids: stringList,
    approval_error: emptyString,
  })
  .transform((value) => ({
    transitionID: value.transition_id,
    state: value.state,
    placementIDs: value.placement_ids,
    runIDs: value.run_ids,
    approvalError: value.approval_error,
  })) satisfies z.ZodType<TaskMoveResponse>;

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
    board: z.object({
      project_id: z.string(),
      project: z.object({
        project_key: z.string(),
        display_name: z.string(),
      }),
      selected_workflow: workflowPickerItemSchema,
      workflows: workflowPickerSchema,
      groups: boardGroupsSchema,
      columns: boardColumnsSchema,
      generated_at_unix_ms: z.number(),
    }),
  })
  .transform((value) => {
    const columns = visibleBoardColumns(value.board.columns);
    return {
      projectID: value.board.project_id,
      projectKey: value.board.project.project_key,
      projectName: value.board.project.display_name,
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
    next_page_token: z.string().optional().default(""),
    generated_at_unix_ms: z.number(),
  })
  .transform((value) => ({
    projectID: value.project_id,
    workflowID: value.workflow_id,
    nodeID: value.node_id,
    cards: value.cards,
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
      managed_worktree: z
        .object({
          canonical_root: z.string().optional().default(""),
          root_path: z.string().optional().default(""),
        })
        .nullish(),
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
    worktreePath: firstNonEmpty(
      value.task.managed_worktree?.canonical_root,
      value.task.managed_worktree?.root_path,
      "",
    ),
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
          .transform(
            (value): PendingAsk => ({
              askID: value.AskID,
              sessionID: value.SessionID,
              question: value.Question,
              suggestions: value.Suggestions,
              recommendedOptionIndex: value.RecommendedOptionIndex,
              createdAt: value.CreatedAt,
            }),
          ),
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
