import { z } from "zod";

import type {
  ApprovalDecision,
  ApprovalQuestionPrompt,
  AttentionItem,
  AttentionQuestionPrompt,
  BoardCard,
  BoardColumn,
  BoardGroup,
  ProjectBinding,
  TaskActions,
  TaskComment,
  TaskRun,
  TaskStatus,
  TaskStatusKind,
  TaskTransition,
  TransitionEdge,
  WorkflowOutputField,
  WorkflowParameter,
  WorkflowPickerItem,
  WorkflowValidationError,
  WorkspaceSummary,
  OrdinaryQuestionPrompt,
} from "../models";

export const emptyString = z.string().optional().default("");
export const numberValue = z.number().default(0);
export const stringList = z
  .array(z.string())
  .nullish()
  .transform((value) => value ?? []);

export const approvalDecisionSchema: z.ZodType<ApprovalDecision> = z.enum([
  "allow_once",
  "allow_session",
  "deny",
]);

const ordinaryQuestionPromptSchema = z.object({
  kind: z.literal("ordinary"),
  suggestions: stringList,
  recommended_option_index: z.number().optional().default(0),
});

const approvalQuestionPromptSchema = z.object({
  kind: z.literal("approval"),
  approval_decisions: z.array(approvalDecisionSchema).min(1),
});

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

export const workspaceSummarySchema: z.ZodType<WorkspaceSummary> = z
  .object({
    workspace_id: z.string(),
    display_name: z.string(),
    root_path: z.string(),
    availability: z.string(),
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

export const validationErrorSchema: z.ZodType<WorkflowValidationError> = z
  .object({
    code: z.string(),
    message: z.string(),
    workflow_id: emptyString,
    node_id: emptyString,
    transition_group_id: emptyString,
    edge_id: emptyString,
    details: z
      .object({
        field_name: emptyString,
        input_name: emptyString,
        placeholder: emptyString,
        provider_edge_id: emptyString,
      })
      .nullish(),
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
    details: {
      fieldName: value.details?.field_name ?? "",
      inputName: value.details?.input_name ?? "",
      placeholder: value.details?.placeholder ?? "",
      providerEdgeID: value.details?.provider_edge_id ?? "",
    },
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
  })
  .transform((value) => ({ key: value.key, description: value.description }));

export const workflowPickerItemSchema: z.ZodType<WorkflowPickerItem> = z
  .object({
    workflow_id: z.string(),
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
      "canceled",
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
    run_ids: stringList,
    attention_types: stringList,
  })
  .strict()
  .transform((value) => ({
    kind: value.kind satisfies TaskStatusKind,
    nativeState: value.native_state,
    nodeIDs: value.node_ids,
    runIDs: value.run_ids,
    attentionTypes: value.attention_types,
  }));

export const taskActionsSchema: z.ZodType<TaskActions> = z
  .object({
    can_start: z.boolean(),
    can_interrupt: z.boolean(),
    can_resume: z.boolean(),
    can_cancel: z.boolean(),
    manual_move_target_node_ids: stringList,
  })
  .transform((value) => ({
    canStart: value.can_start,
    canInterrupt: value.can_interrupt,
    canResume: value.can_resume,
    canCancel: value.can_cancel,
    manualMoveTargetNodeIDs: value.manual_move_target_node_ids,
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
      transition_output_fields: z
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
    transitionOutputFields: value.node.transition_output_fields,
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
    body_preview: emptyString,
    workflow_id: z.string(),
    active_node_ids: stringList,
    source_workspace: workspaceSummarySchema,
    status: taskStatusSchema,
    actions: taskActionsSchema,
    updated_at_unix_ms: z.number(),
  })
  .transform((value) => ({
    id: value.task_id,
    shortID: value.short_id,
    title: value.title,
    bodyPreview: value.body_preview,
    workflowID: value.workflow_id,
    activeNodeIDs: value.active_node_ids,
    sourceWorkspace: value.source_workspace,
    status: value.status,
    actions: value.actions,
    updatedAt: value.updated_at_unix_ms,
  }));

export const attentionItemSchema: z.ZodType<AttentionItem> = z
  .object({
    id: z.string(),
    kind: z.string(),
    project_id: emptyString,
    workflow_id: emptyString,
    task_id: emptyString,
    task_short_id: emptyString,
    task_title: emptyString,
    run_id: emptyString,
    session_id: emptyString,
    ask_id: emptyString,
    task_transition_id: emptyString,
    message: z.string(),
    detail_json: emptyString,
    suggestions: stringList,
    recommended_option_index: z.number().optional().default(0),
    question: questionPromptSchema.nullish(),
    occurred_at_unix_ms: z.number(),
  })
  .transform((value) => ({
    id: value.id,
    kind: value.kind,
    projectID: value.project_id,
    workflowID: value.workflow_id,
    taskID: value.task_id,
    taskShortID: value.task_short_id,
    taskTitle: value.task_title,
    runID: value.run_id,
    sessionID: value.session_id,
    askID: value.ask_id,
    taskTransitionID: value.task_transition_id,
    message: value.message,
    detailJSON: value.detail_json,
    suggestions: value.suggestions,
    recommendedOptionIndex: value.recommended_option_index,
    question: value.question ?? null,
    occurredAt: value.occurred_at_unix_ms,
  }));

export const commentSchema: z.ZodType<TaskComment> = z
  .object({
    id: z.string(),
    task_id: z.string(),
    body: z.string(),
    author: z.string(),
    created_at_unix_ms: z.number(),
    updated_at_unix_ms: z.number(),
  })
  .transform((value) => ({
    id: value.id,
    taskID: value.task_id,
    body: value.body,
    author: value.author,
    createdAt: value.created_at_unix_ms,
    updatedAt: value.updated_at_unix_ms,
  }));

export const runSchema: z.ZodType<TaskRun> = z
  .object({
    id: z.string(),
    task_id: z.string(),
    placement_id: z.string(),
    node_id: z.string(),
    node_kind: emptyString,
    script_path: emptyString,
    session_id: emptyString,
    session_name: emptyString,
    role: emptyString,
    status: z.string(),
    generation: z.number(),
    waiting_ask_id: emptyString,
    started_at_unix_ms: numberValue,
    completed_at_unix_ms: numberValue,
    interrupted_at_unix_ms: numberValue,
    interruption_reason: emptyString,
    interruption_detail_json: emptyString,
  })
  .transform((value) => ({
    id: value.id,
    taskID: value.task_id,
    placementID: value.placement_id,
    nodeID: value.node_id,
    nodeKind: value.node_kind,
    scriptPath: value.script_path,
    sessionID: value.session_id,
    sessionName: value.session_name,
    role: value.role,
    status: value.status,
    generation: value.generation,
    waitingAskID: value.waiting_ask_id,
    startedAt: value.started_at_unix_ms,
    completedAt: value.completed_at_unix_ms,
    interruptedAt: value.interrupted_at_unix_ms,
    interruptionReason: value.interruption_reason,
    interruptionDetail: value.interruption_detail_json,
  }));

export const transitionEdgeSchema: z.ZodType<TransitionEdge> = z
  .object({
    id: z.string(),
    edge_key: z.string(),
    target_node_display_name: emptyString,
    state: z.string(),
    requires_approval: z.boolean(),
    output_requirements: z
      .array(z.object({ field_name: z.string() }))
      .nullish()
      .transform((value) => value ?? []),
  })
  .transform((value) => ({
    id: value.id,
    edgeKey: value.edge_key,
    targetNodeName: value.target_node_display_name,
    state: value.state,
    requiresApproval: value.requires_approval,
    outputRequirements: value.output_requirements.map((item) => item.field_name),
  }));

export const transitionSchema: z.ZodType<TaskTransition> = z
  .object({
    id: z.string(),
    transition_id: z.string(),
    transition_display_name: emptyString,
    source_node_display_name: emptyString,
    state: z.string(),
    commentary: emptyString,
    output_values: z
      .record(z.string(), z.string())
      .nullish()
      .transform((value) => value ?? {}),
    edges: z
      .array(transitionEdgeSchema)
      .nullish()
      .transform((value) => value ?? []),
    workflow_revision_seen: z.number().optional().default(0),
    created_at_unix_ms: z.number(),
    applied_at_unix_ms: numberValue,
  })
  .transform((value) => ({
    id: value.id,
    transitionID: value.transition_id,
    transitionName: value.transition_display_name,
    sourceNodeName: value.source_node_display_name,
    state: value.state,
    commentary: value.commentary,
    outputValues: value.output_values,
    edges: value.edges,
    version: value.workflow_revision_seen,
    createdAt: value.created_at_unix_ms,
    appliedAt: value.applied_at_unix_ms,
  }));
