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
  TaskAttention,
  TaskApproveResponse,
  TaskCurrentNode,
  TaskMoveResponse,
  TaskMovePreviewResponse,
  TaskResumeResponse,
  TaskStartResponse,
  WorkflowExecutionTargetSelectionRequirement,
  WorkflowBoard,
  ProjectWorkflowLink,
} from "../models";
import type { TaskListPage } from "../workflowLabels";
import { retainedPreviousWorktreeSchema, type RetainedPreviousWorktree } from "../worktreeSetup";
import {
  attentionItemSchema,
  boardCardSchema,
  boardColumnSchema,
  boardGroupSchema,
  commentSchema,
  emptyString,
  nonBlankString,
  currentNodeSchema,
  scriptCurrentNodeSchema,
  taskActionsSchema,
  taskStatusSchema,
  workflowPickerItemSchema,
  workflowIDSchema,
  workspaceSummarySchema,
} from "./common";
import { emptyArray } from "./workflowHelpers";
import { workflowExecutionTargetSchema } from "./workflowExecutionTarget";
import { labelIDListSchema } from "./workflowLabels";
import { taskDependenciesSchema } from "./taskDependencies";
export {
  taskDependenciesSchema,
  taskDependencyAddResponseSchema,
  taskDependencyListResponseSchema,
  taskDependencyRemoveResponseSchema,
} from "./taskDependencies";

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
          label_ids: labelIDListSchema,
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
          labelIDs: value.label_ids,
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

const dependencyConfirmationRequiredResponseSchema = z
  .object({
    outcome: z.literal("dependency_confirmation_required"),
    unsatisfied_dependency_count: z.number().int().positive(),
  })
  .strict()
  .transform(
    (value) =>
      ({
        outcome: value.outcome,
        unsatisfiedDependencyCount: value.unsatisfied_dependency_count,
      }) as const,
  );

const appliedCurrentNodesResponseSchema = z
  .object({
    outcome: z.literal("applied"),
    applied: z
      .object({
        current_nodes: z.array(currentNodeSchema).min(1),
      })
      .strict(),
  })
  .strict()
  .transform(
    (value) =>
      ({
        outcome: value.outcome,
        applied: { currentNodes: value.applied.current_nodes },
      }) as const,
  );

export const taskStartResponseSchema: z.ZodType<TaskStartResponse> = z.discriminatedUnion("outcome", [
  appliedCurrentNodesResponseSchema,
  selectionRequiredResponseSchema,
  dependencyConfirmationRequiredResponseSchema,
]);

export const taskResumeResponseSchema: z.ZodType<TaskResumeResponse> = z.discriminatedUnion("outcome", [
  appliedCurrentNodesResponseSchema,
  selectionRequiredResponseSchema,
]);

type TaskMoveMutationResult = Readonly<{ currentNodes: readonly TaskCurrentNode[];
  retainedPreviousWorktree: RetainedPreviousWorktree | null }>;
const taskMoveResultSchema: z.ZodType<TaskMoveMutationResult> = z
  .object({ current_nodes: z.array(currentNodeSchema).min(1),
    retained_previous_worktree: retainedPreviousWorktreeSchema.nullable() })
  .strict()
  .transform((value) => ({ currentNodes: value.current_nodes,
    retainedPreviousWorktree: value.retained_previous_worktree }));

const taskMoveNoOpResponseSchema = z
  .object({ outcome: z.literal("no_op"), no_op: taskMoveResultSchema })
  .strict()
  .transform((value) => ({ outcome: value.outcome, noOp: value.no_op }));

const taskMovePreviewNoOpResponseSchema = z
  .object({ outcome: z.literal("no_op"), no_op:
    z.object({ current_nodes: z.array(currentNodeSchema).min(1) }).strict() })
  .strict()
  .transform((value) => ({ outcome: value.outcome, noOp: { currentNodes: value.no_op.current_nodes } }));

const taskMoveAppliedResponseSchema = z
  .object({ outcome: z.literal("applied"), applied: taskMoveResultSchema })
  .strict()
  .transform((value) => ({ outcome: value.outcome, applied: value.applied }));

export const taskMoveResponseSchema: z.ZodType<TaskMoveResponse> = z.discriminatedUnion("outcome", [
  taskMoveAppliedResponseSchema,
  selectionRequiredResponseSchema,
  taskMoveNoOpResponseSchema,
  dependencyConfirmationRequiredResponseSchema,
]);

const manualMoveBlockerSchema = z.enum([
  "invalid_workflow",
  "no_source_position",
  "unsupported_destination",
  "waiting_question",
  "lifecycle_conflict",
  "context_session_unavailable",
  "no_usable_transition",
  "parallel_branch_requires_fan_out",
]);

const nonBlankPreservingString = z.string().refine((value) => value.trim().length > 0);

const manualMoveRequiredValueSchema = z
  .object({
    node_key: z.string().trim().min(1),
    output_name: z.string().trim().min(1),
    description: z.string(),
    resolved_value: nonBlankPreservingString.nullable().optional(),
  })
  .strict()
  .transform((value) => ({
    nodeKey: value.node_key,
    outputName: value.output_name,
    description: value.description,
    resolvedValue: value.resolved_value ?? null,
  }));

export const taskMovePreviewResponseSchema: z.ZodType<TaskMovePreviewResponse> = z.discriminatedUnion(
  "outcome",
  [
    taskMovePreviewNoOpResponseSchema,
    z
      .object({ outcome: z.literal("direct"), direct: z.object({}).strict() })
      .strict()
      .transform((value) => ({ outcome: value.outcome, direct: {} })),
    z
      .object({
        outcome: z.literal("transition"),
        transition: z
          .object({
            choices: z.array(
              z
                .object({
                  transition_key: z.string().trim().min(1),
                  label: z.string().trim().min(1),
                  source_node_display_name: z.string().trim().min(1),
                  required_values: z.array(manualMoveRequiredValueSchema),
                })
                .strict()
                .transform((value) => ({
                  transitionKey: value.transition_key,
                  label: value.label,
                  sourceNodeDisplayName: value.source_node_display_name,
                  requiredValues: value.required_values,
                })),
            ).min(1),
          })
          .strict(),
      })
      .strict()
      .transform((value) => ({ outcome: value.outcome, transition: value.transition })),
    z
      .object({
        outcome: z.literal("blocked"),
        blocked: z.object({ reason: manualMoveBlockerSchema }).strict(),
      })
      .strict()
      .transform((value) => ({ outcome: value.outcome, blocked: value.blocked })),
  ],
);

export const taskApproveResponseSchema: z.ZodType<TaskApproveResponse> = z.discriminatedUnion("outcome", [
  z
    .object({
      outcome: z.literal("applied"),
      applied: z
        .object({
          task_id: z.string().trim().min(1),
          current_nodes: z.array(currentNodeSchema).min(1),
        })
        .strict(),
    })
    .strict()
    .transform(
      (value) =>
        ({
          outcome: value.outcome,
          applied: { taskID: value.applied.task_id, currentNodes: value.applied.current_nodes },
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
            workflow_id: workflowIDSchema,
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
        selected_workflow: workflowPickerItemSchema.nullish().transform((value) => value ?? null),
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
    workflow_id: workflowIDSchema,
    node_id: z.string(),
    cards: boardCardsSchema,
    next_offset: z.number().int().nonnegative().nullable().optional().default(null),
    generated_at_unix_ms: z.number(),
  })
  .strict()
  .transform((value) => ({
    projectID: value.project_id,
    workflowID: value.workflow_id,
    nodeID: value.node_id,
    cards: value.cards,
    nextOffset: value.next_offset,
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

export const taskAttentionSchema: z.ZodType<TaskAttention> = z
  .object({
    items: z.array(attentionItemSchema),
    generated_at_unix_ms: z.number(),
  })
  .transform((value) => ({
    items: value.items,
    generatedAt: value.generated_at_unix_ms,
  }));

export const taskDetailSchema: z.ZodType<TaskDetail> = z
  .object({
    task: z.object({
      summary: z.object({
        id: z.string(),
        project_id: z.string(),
        workflow_id: workflowIDSchema,
        short_id: z.string(),
        title: z.string(),
        created_at_unix_ms: z.number(),
        updated_at_unix_ms: z.number(),
        done: z.boolean(),
      }),
      project: z.object({
        display_name: z.string(),
      }),
      workflow: z.object({
        workflow_id: workflowIDSchema,
        display_name: z.string(),
        version: z.number(),
      }),
      body: emptyString,
      source_url: emptyString,
      source_workspace: workspaceSummarySchema,
      execution_target: workflowExecutionTargetSchema.optional().transform((value) => value ?? null),
      worktree_path: nonBlankString.nullable(),
      current_nodes: z.array(currentNodeSchema),
      live_sessions: z.array(
        z
          .object({
            session_id: nonBlankString,
            session_name: nonBlankString.optional(),
            node_display_name: nonBlankString,
          })
          .strict(),
      ),
      current_scripts: z.array(
        z
          .object({
            current_node: scriptCurrentNodeSchema,
            path: nonBlankString,
          })
          .strict(),
      ),
      retained_session_count: z.number().int().nonnegative(),
      status: taskStatusSchema,
      actions: taskActionsSchema,
      label_ids: labelIDListSchema,
      attention_count: z.number().int().nonnegative(),
      dependencies: taskDependenciesSchema,
    }),
  })
  .transform((value) => ({
    id: value.task.summary.id,
    shortID: value.task.summary.short_id,
    projectID: value.task.summary.project_id,
    projectName: value.task.project.display_name,
    workflowID: value.task.summary.workflow_id,
    workflowName: value.task.workflow.display_name,
    workflowVersion: value.task.workflow.version,
    title: value.task.summary.title,
    body: value.task.body,
    sourceURL: value.task.source_url,
    sourceWorkspace: value.task.source_workspace,
    status: value.task.status,
    actions: value.task.actions,
    labelIDs: value.task.label_ids,
    attentionCount: value.task.attention_count,
    dependencies: value.task.dependencies,
    executionTarget: value.task.execution_target,
    worktreePath: value.task.worktree_path,
    currentNodes: value.task.current_nodes,
    liveSessions: value.task.live_sessions.map((session) => ({
      sessionID: session.session_id,
      sessionName: session.session_name ?? null,
      nodeDisplayName: session.node_display_name,
    })),
    currentScripts: value.task.current_scripts.map((script) => ({
      currentNode: script.current_node,
      path: script.path,
    })),
    retainedSessionCount: value.task.retained_session_count,
    createdAt: value.task.summary.created_at_unix_ms,
    updatedAt: value.task.summary.updated_at_unix_ms,
    done: value.task.summary.done,
  }));

const activityItemSchema = z.discriminatedUnion("type", [
  z
    .object({
      activity_id: nonBlankString,
      type: z.literal("comment"),
      task_id: nonBlankString,
      occurred_at_unix_ms: z.number(),
      updated_at_unix_ms: z.number(),
      comment: commentSchema,
    })
    .strict()
    .transform((value) => ({
      id: value.activity_id,
      type: value.type,
      taskID: value.task_id,
      occurredAt: value.occurred_at_unix_ms,
      updatedAt: value.updated_at_unix_ms,
      comment: value.comment,
    })),
  z
    .object({
      activity_id: nonBlankString,
      type: z.literal("session_started"),
      task_id: nonBlankString,
      occurred_at_unix_ms: z.number(),
      updated_at_unix_ms: z.number(),
      session_started: z.object({ session_id: nonBlankString, name: nonBlankString }).strict(),
    })
    .strict()
    .transform((value) => ({
      id: value.activity_id,
      type: value.type,
      taskID: value.task_id,
      occurredAt: value.occurred_at_unix_ms,
      updatedAt: value.updated_at_unix_ms,
      sessionID: value.session_started.session_id,
      sessionName: value.session_started.name,
    })),
]);

export const activityPageSchema: z.ZodType<ActivityPage> = z
  .object({
    items: z.array(activityItemSchema),
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
            RecommendedOptionIndex: z.number().int().positive().nullable(),
            CreatedAt: z.string().optional().default(""),
          })
          .superRefine((value, context) => {
            if (
              value.RecommendedOptionIndex !== null &&
              value.RecommendedOptionIndex > value.Suggestions.length
            ) {
              context.addIssue({
                code: "custom",
                message: "recommended option index exceeds suggestions",
                path: ["RecommendedOptionIndex"],
              });
            }
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
    next_offset: z.number().int().positive().nullable().optional(),
  })
  .transform((value) => ({
    comments: value.comments,
    nextOffset: value.next_offset ?? null,
  }));
