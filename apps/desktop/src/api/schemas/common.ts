import { z } from "zod";

import type {
  ApprovalDecision,
  ApprovalQuestionPrompt,
  AttentionQuestionPrompt,
  BoardCard,
  BoardColumn,
  BoardGroup,
  ProjectBinding,
  TaskActions,
  TaskComment,
  TaskCurrentNode,
  TaskScriptCurrentNode,
  TaskStatus,
  TaskStatusKind,
  WorkflowOutputField,
  WorkflowParameter,
  WorkflowPickerItem,
  WorkflowValidationError,
  WorkspaceSummary,
  WorkspaceAvailability,
  OrdinaryQuestionPrompt,
} from "../models";
import type {
  ApprovalAttentionItem,
  AttentionItem,
  InterruptedCurrentNodeAttentionItem,
  QuestionAttentionItem,
} from "../attention";
import { labelIDListSchema } from "./workflowLabels";
import { workflowIDSchema } from "./workflowID";

export { workflowIDSchema } from "./workflowID";

export const emptyString = z.string().optional().default("");
export const nonBlankString = z.string().trim().min(1);
export const nullableWorkflowIDSchema = workflowIDSchema.nullish().transform((value) => value ?? null);
export const numberValue = z.number().default(0);
export const nullableString = z
  .string()
  .nullish()
  .transform((value) => value ?? null);
const nullableNonBlankString = z
  .string()
  .trim()
  .min(1)
  .nullish()
  .transform((value) => value ?? null);
const nullablePositiveInteger = z
  .number()
  .int()
  .positive()
  .nullish()
  .transform((value) => value ?? null);
export const stringList = z
  .array(z.string())
  .nullish()
  .transform((value) => value ?? []);

export const approvalDecisionSchema: z.ZodType<ApprovalDecision> = z.enum([
  "allow_once",
  "allow_session",
  "deny",
]);

const ordinaryQuestionPromptSchema = z
  .object({
    kind: z.literal("ordinary"),
    suggestions: stringList,
    recommended_option_index: nullablePositiveInteger,
  })
  .strict();

const approvalQuestionPromptSchema = z
  .object({
    kind: z.literal("approval"),
    approval_decisions: z.array(approvalDecisionSchema).min(1),
  })
  .strict();

export const questionPromptSchema: z.ZodType<AttentionQuestionPrompt> = z
  .discriminatedUnion("kind", [ordinaryQuestionPromptSchema, approvalQuestionPromptSchema])
  .transform((value): AttentionQuestionPrompt => {
    switch (value.kind) {
      case "ordinary":
        return {
          kind: "ordinary",
          suggestions: value.suggestions,
          recommendedOptionIndex: value.recommended_option_index,
        } satisfies OrdinaryQuestionPrompt;
      case "approval":
        return {
          kind: "approval",
          approvalDecisions: value.approval_decisions,
        } satisfies ApprovalQuestionPrompt;
    }
  });

export const workspaceAvailabilitySchema: z.ZodType<WorkspaceAvailability> = z.enum([
  "available",
  "missing",
  "inaccessible",
  "unlinked",
]);

export const workspaceSummarySchema: z.ZodType<WorkspaceSummary> = z
  .object({
    workspace_id: z.string(),
    display_name: z.string(),
    root_path: z.string(),
    availability: workspaceAvailabilitySchema,
    is_primary: z.boolean(),
    updated_at_unix_ms: z.number(),
  })
  .transform((value) => ({
    id: value.workspace_id,
    name: value.display_name,
    rootPath: value.root_path,
    availability: value.availability,
    isPrimary: value.is_primary,
    updatedAt: value.updated_at_unix_ms,
  }));

export const projectBindingSchema: z.ZodType<ProjectBinding> = z
  .object({
    project_id: z.string(),
    project_key: emptyString,
    project_name: emptyString,
    workspace_id: emptyString,
    canonical_root: emptyString,
    workspace_name: emptyString,
    workspace_status: emptyString,
  })
  .transform((value) => ({
    projectID: value.project_id,
    projectKey: value.project_key,
    projectName: value.project_name,
    workspaceID: value.workspace_id,
    canonicalRoot: value.canonical_root,
    workspaceName: value.workspace_name,
    workspaceStatus: value.workspace_status,
  }));

const validationErrorDetailsSchema = z
  .preprocess(
    (value) => value ?? {},
    z.object({
      field_name: emptyString,
      input_name: emptyString,
      placeholder: emptyString,
      provider_edge_id: emptyString,
      role: nullableNonBlankString,
      required_tool: nullableNonBlankString,
    }),
  )
  .transform((value) => ({
    fieldName: value.field_name,
    inputName: value.input_name,
    placeholder: value.placeholder,
    providerEdgeID: value.provider_edge_id,
    role: value.role,
    requiredTool: value.required_tool,
  }));

export const validationErrorSchema: z.ZodType<WorkflowValidationError> = z
  .object({
    code: z.string(),
    message: z.string(),
    workflow_id: nullableWorkflowIDSchema,
    node_id: emptyString,
    transition_group_id: emptyString,
    edge_id: emptyString,
    details: validationErrorDetailsSchema,
    related_ids: stringList,
    blocks_context: z.boolean().default(false),
  })
  .transform((value) => ({
    code: value.code,
    message: value.message,
    workflowID: value.workflow_id,
    nodeID: value.node_id,
    transitionGroupID: value.transition_group_id,
    edgeID: value.edge_id,
    details: value.details,
    relatedIDs: value.related_ids,
    blocksContext: value.blocks_context,
  }));

export const workflowOutputFieldSchema: z.ZodType<WorkflowOutputField> = z
  .object({
    name: z.string(),
    description: emptyString,
  })
  .transform((value) => ({ name: value.name, description: value.description }));

export const workflowParameterSchema: z.ZodType<WorkflowParameter> = z
  .object({
    key: z.string(),
    description: emptyString,
    purpose: z.enum(["ordinary", "target_assignee", "target_thinking"]),
  })
  .transform((value) => ({ key: value.key, description: value.description, purpose: value.purpose }));

export const workflowPickerItemSchema: z.ZodType<WorkflowPickerItem> = z
  .object({
    workflow_id: workflowIDSchema,
    display_name: z.string(),
    description: emptyString,
    version: z.number(),
    is_project_default: z.boolean(),
    valid_for_task_creation: z.boolean(),
    validation_errors: z
      .array(validationErrorSchema)
      .nullish()
      .transform((value) => value ?? []),
  })
  .transform((value) => ({
    id: value.workflow_id,
    name: value.display_name,
    description: value.description,
    version: value.version,
    isProjectDefault: value.is_project_default,
    validForTaskCreation: value.valid_for_task_creation,
    validationErrors: value.validation_errors,
  }));

export const taskStatusSchema: z.ZodType<TaskStatus> = z
  .object({
    kind: z.enum([
      "done",
      "waiting_question",
      "waiting_approval",
      "interrupted",
      "running",
      "queued",
      "backlog",
      "active",
    ]),
    native_state: z.string(),
    node_ids: stringList,
    attention_types: stringList,
  })
  .strict()
  .transform((value) => ({
    kind: value.kind satisfies TaskStatusKind,
    nativeState: value.native_state,
    nodeIDs: value.node_ids,
    attentionTypes: value.attention_types,
  }));

export const taskActionsSchema: z.ZodType<TaskActions> = z
  .object({
    can_start: z.boolean(),
    can_interrupt: z.boolean(),
    can_resume: z.boolean(),
    can_delete: z.boolean(),
  })
  .transform((value) => ({
    canStart: value.can_start,
    canInterrupt: value.can_interrupt,
    canResume: value.can_resume,
    canDelete: value.can_delete,
  }));

export const boardColumnSchema: z.ZodType<BoardColumn> = z
  .object({
    node: z.object({
      node_id: z.string(),
      key: z.string(),
      kind: emptyString,
      display_name: z.string(),
      assignee_role: emptyString,
      output_fields: z
        .array(workflowOutputFieldSchema)
        .nullish()
        .transform((value) => value ?? []),
    }),
    group_id: emptyString,
    sort_order: z.number(),
    is_backlog: z.boolean(),
    is_done: z.boolean(),
    task_count: z.number(),
  })
  .transform((value) => ({
    id: value.node.node_id,
    key: value.node.key,
    kind: value.node.kind,
    name: value.node.display_name,
    assigneeRole: value.node.assignee_role,
    outputFields: value.node.output_fields,
    groupID: value.group_id,
    sortOrder: value.sort_order,
    isBacklog: value.is_backlog,
    isDone: value.is_done,
    taskCount: value.task_count,
  }));

export const boardGroupSchema: z.ZodType<BoardGroup> = z
  .object({
    group_id: z.string(),
    key: z.string(),
    display_name: z.string(),
    sort_order: z.number(),
    node_ids: stringList,
  })
  .transform((value) => ({
    id: value.group_id,
    key: value.key,
    name: value.display_name,
    sortOrder: value.sort_order,
    nodeIDs: value.node_ids,
  }));

export const boardCardSchema: z.ZodType<BoardCard> = z
  .object({
    task_id: z.string(),
    short_id: z.string(),
    title: z.string(),
    preview: z
      .object({
        markdown: z.string(),
        truncated: z.boolean(),
      })
      .strict(),
    workflow_id: workflowIDSchema,
    active_node_ids: stringList,
    source_workspace: workspaceSummarySchema,
    status: taskStatusSchema,
    actions: taskActionsSchema,
    label_ids: labelIDListSchema,
    dependency_progress: z
      .object({
        satisfied_count: z.number().int().nonnegative(),
        total_count: z.number().int().positive(),
      })
      .strict()
      .refine((value) => value.satisfied_count <= value.total_count)
      .optional()
      .transform((value) =>
        value === undefined ? null : { satisfiedCount: value.satisfied_count, totalCount: value.total_count },
      ),
    updated_at_unix_ms: z.number(),
  })
  .strict()
  .transform((value) => ({
    id: value.task_id,
    shortID: value.short_id,
    title: value.title,
    preview: value.preview,
    workflowID: value.workflow_id,
    activeNodeIDs: value.active_node_ids,
    sourceWorkspace: value.source_workspace,
    status: value.status,
    actions: value.actions,
    labelIDs: value.label_ids,
    dependencyProgress: value.dependency_progress,
    updatedAt: value.updated_at_unix_ms,
  }));

const attentionItemBaseWireSchema = z.object({
  id: nonBlankString,
  project_id: nonBlankString,
  workflow_id: workflowIDSchema,
  task_id: nonBlankString,
  task_short_id: nonBlankString,
  task_title: nonBlankString,
  occurred_at_unix_ms: z.number(),
});

type AttentionItemBase = Pick<
  QuestionAttentionItem,
  "id" | "projectID" | "workflowID" | "taskID" | "taskShortID" | "taskTitle" | "occurredAt"
>;

function adaptAttentionItemBase(value: z.output<typeof attentionItemBaseWireSchema>): AttentionItemBase {
  return {
    id: value.id,
    projectID: value.project_id,
    workflowID: value.workflow_id,
    taskID: value.task_id,
    taskShortID: value.task_short_id,
    taskTitle: value.task_title,
    occurredAt: value.occurred_at_unix_ms,
  };
}

const approvalSnapshotSchema = z
  .object({
    source_node_display_name: nonBlankString,
    targets: z.array(z.object({ display_name: nonBlankString }).strict()),
    commentary: emptyString,
    output_values: z.record(z.string(), z.string()),
    workflow_revision_seen: z.number().int().nonnegative(),
  })
  .strict()
  .transform((value) => ({
    sourceNodeName: value.source_node_display_name,
    targets: value.targets.map((target) => ({ displayName: target.display_name })),
    commentary: value.commentary,
    outputValues: value.output_values,
    version: value.workflow_revision_seen,
  }));

export const currentNodeSchema: z.ZodType<TaskCurrentNode> = z
  .object({
    node_id: nonBlankString,
    transition_branch_key: nullableNonBlankString,
    session_id: nullableNonBlankString,
    effective_assignee: nullableNonBlankString,
    effective_thinking: nullableNonBlankString,
  })
  .strict()
  .transform((value) => ({
    nodeID: value.node_id,
    transitionBranchKey: value.transition_branch_key,
    sessionID: value.session_id,
    effectiveAssignee: value.effective_assignee,
    effectiveThinking: value.effective_thinking,
  }));

export const scriptCurrentNodeSchema: z.ZodType<TaskScriptCurrentNode> = z
  .object({
    node_id: nonBlankString,
    transition_branch_key: nullableNonBlankString,
    session_id: z.null(),
  })
  .strict()
  .transform((value) => ({
    nodeID: value.node_id,
    transitionBranchKey: value.transition_branch_key,
    sessionID: value.session_id,
  }));

export const attentionItemSchema: z.ZodType<AttentionItem> = z.discriminatedUnion("kind", [
  attentionItemBaseWireSchema
    .extend({
      kind: z.literal("question"),
      current_node: currentNodeSchema,
      session_id: nullableNonBlankString,
      session_name: nonBlankString.nullable(),
      question_id: nonBlankString,
      message: nonBlankString,
      suggestions: stringList,
      recommended_option_index: nullablePositiveInteger,
      question: questionPromptSchema.nullish(),
    })
    .strict()
    .transform((value): QuestionAttentionItem => ({
      ...adaptAttentionItemBase(value),
      kind: value.kind,
      currentNode: value.current_node,
      sessionID: value.session_id,
      sessionName: value.session_name,
      questionID: value.question_id,
      message: value.message,
      suggestions: value.suggestions,
      recommendedOptionIndex: value.recommended_option_index,
      question: value.question ?? null,
    })),
  attentionItemBaseWireSchema
    .extend({
      kind: z.literal("approval"),
      approval_id: nonBlankString,
      approval_snapshot: approvalSnapshotSchema,
      session_name: z.null(),
      message: nullableNonBlankString,
    })
    .strict()
    .transform((value): ApprovalAttentionItem => ({
      ...adaptAttentionItemBase(value),
      kind: value.kind,
      approvalID: value.approval_id,
      approvalSnapshot: value.approval_snapshot,
      message: value.message,
    })),
  attentionItemBaseWireSchema
    .extend({
      kind: z.literal("interrupted_current_node"),
      current_node: currentNodeSchema,
      session_id: nullableNonBlankString,
      session_name: z.null(),
      detail_json: nullableNonBlankString,
      message: nullableNonBlankString,
    })
    .strict()
    .transform((value): InterruptedCurrentNodeAttentionItem => ({
      ...adaptAttentionItemBase(value),
      kind: value.kind,
      currentNode: value.current_node,
      sessionID: value.session_id,
      detailJSON: value.detail_json,
      message: value.message,
    })),
]);

export const commentSchema: z.ZodType<TaskComment> = z
  .object({
    id: z.string(),
    task_id: z.string(),
    body: z.string(),
    author: z.enum(["agent", "user"]),
    author_id: z.string().min(1).optional(),
    created_at_unix_ms: z.number(),
    updated_at_unix_ms: z.number(),
  })
  .transform((value) => ({
    id: value.id,
    taskID: value.task_id,
    body: value.body,
    authorKind: value.author,
    authorID: value.author_id ?? null,
    createdAt: value.created_at_unix_ms,
    updatedAt: value.updated_at_unix_ms,
  }));
