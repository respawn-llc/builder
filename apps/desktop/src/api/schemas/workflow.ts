import { z } from "zod";

import type {
  WorkflowDerivedWiring,
  WorkflowDeleteImpact,
  WorkflowDeleteResponse,
  WorkflowDefinition,
  WorkflowGraphSaveImpact,
  WorkflowGraphSavePreview,
  WorkflowGraphSaveResult,
  WorkflowGraphValidateDraftResult,
  WorkflowGraphValidationResults,
  WorkflowPage,
  WorkflowRecord,
  WorkflowExecutionTargetPolicy,
  ProjectWorkflowLink,
  WorkflowValidation,
} from "../models";
import { emptyWorkflowDerivedWiring } from "../models";
import {
  emptyString,
  validationErrorSchema,
  workflowOutputFieldSchema,
  workflowParameterSchema,
} from "./common";
import { emptyArray } from "./workflowHelpers";

const workflowExecutionTargetPolicySchema: z.ZodType<WorkflowExecutionTargetPolicy> = z
  .object({
    mode: z.enum(["none", "head", "default_branch", "custom_ref", "ask_on_first_execution"]),
    custom_ref: z.string().trim().min(1).optional(),
  })
  .superRefine((value, context) => {
    if (value.mode !== "custom_ref" && value.custom_ref !== undefined) {
      context.addIssue({
        code: "custom",
        message: "custom_ref is only valid for custom_ref",
        path: ["custom_ref"],
      });
    }
  })
  .transform((value) => ({
    mode: value.mode,
    customRef: value.custom_ref ?? null,
  }));

const workflowRecordSchema: z.ZodType<WorkflowRecord> = z
  .object({
    id: z.string(),
    name: z.string(),
    description: emptyString,
    version: z.number(),
    execution_target_policy: workflowExecutionTargetPolicySchema,
  })
  .transform((value) => ({
    id: value.id,
    name: value.name,
    description: value.description,
    version: value.version,
    executionTargetPolicy: value.execution_target_policy,
  }));

export const workflowListSchema: z.ZodType<WorkflowPage> = z
  .object({
    workflows: z.array(workflowRecordSchema).nullish().transform(emptyArray),
    next_page_token: emptyString,
  })
  .transform((value) => ({
    workflows: value.workflows,
    nextPageToken: value.next_page_token,
  }));

export const workflowCreateSchema: z.ZodType<WorkflowRecord> = z
  .object({
    workflow: workflowRecordSchema,
  })
  .transform((value) => value.workflow);

const projectWorkflowLinkSchema: z.ZodType<ProjectWorkflowLink> = z
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
  }));

export const workflowLinkProjectSchema: z.ZodType<ProjectWorkflowLink> = z
  .object({
    link: projectWorkflowLinkSchema,
  })
  .transform((value) => value.link);

export const workflowCreateAndLinkSchema: z.ZodType<
  Readonly<{ workflow: WorkflowRecord; link: ProjectWorkflowLink }>
> = z
  .object({
    workflow: workflowRecordSchema,
    link: projectWorkflowLinkSchema,
  })
  .transform((value) => ({
    workflow: value.workflow,
    link: value.link,
  }));

const workflowNodeGroupsSchema = z
  .array(
    z
      .object({
        group_id: z.string(),
        workflow_id: z.string(),
        group_key: z.string(),
        display_name: z.string(),
        sort_order: z.number(),
        node_ids: z.array(z.string()).nullish().transform(emptyArray),
      })
      .transform((value) => ({
        id: value.group_id,
        workflowID: value.workflow_id,
        key: value.group_key,
        name: value.display_name,
        sortOrder: value.sort_order,
        nodeIDs: value.node_ids,
      })),
  )
  .nullish()
  .transform(emptyArray);

const workflowNodesSchema = z
  .array(
    z
      .object({
        id: z.string(),
        workflow_id: z.string(),
        key: z.string(),
        kind: z.string(),
        display_name: z.string(),
        group_id: emptyString,
        group_key: emptyString,
        subagent_role: emptyString,
        prompt_template: emptyString,
        completion_mode: emptyString,
        script_path: z.string().nullable().optional(),
        input_fields: z.array(workflowOutputFieldSchema).nullish().transform(emptyArray),
        join_input_providers: z
          .array(
            z
              .object({
                input_name: z.string(),
                provider_edge_id: z.string(),
              })
              .transform((provider) => ({
                inputName: provider.input_name,
                providerEdgeID: provider.provider_edge_id,
              })),
          )
          .nullish()
          .transform(emptyArray),
        output_fields: z.array(workflowOutputFieldSchema).nullish().transform(emptyArray),
      })
      .transform((value) => ({
        id: value.id,
        workflowID: value.workflow_id,
        key: value.key,
        kind: value.kind,
        name: value.display_name,
        groupID: value.group_id,
        groupKey: value.group_key,
        subagentRole: value.subagent_role,
        promptTemplate: value.prompt_template,
        completionMode: value.completion_mode,
        scriptPath: value.script_path ?? null,
        inputFields: value.input_fields,
        joinInputProviders: value.join_input_providers,
        outputFields: value.output_fields,
      })),
  )
  .nullish()
  .transform(emptyArray);

const workflowTransitionGroupsSchema = z
  .array(
    z
      .object({
        id: z.string(),
        workflow_id: z.string(),
        source_node_id: z.string(),
        transition_id: z.string(),
        display_name: z.string(),
        description: emptyString,
      })
      .transform((value) => ({
        id: value.id,
        workflowID: value.workflow_id,
        sourceNodeID: value.source_node_id,
        transitionID: value.transition_id,
        name: value.display_name,
        description: value.description,
      })),
  )
  .nullish()
  .transform(emptyArray);

const workflowInputBindingsSchema = z
  .array(
    z
      .object({
        name: z.string(),
        source: z.string(),
        field: z.string(),
      })
      .transform((value) => ({
        name: value.name,
        source: value.source,
        field: value.field,
      })),
  )
  .nullish()
  .transform(emptyArray);

const workflowOutputRequirementsSchema = z
  .array(
    z
      .object({
        field_name: z.string(),
      })
      .transform((value) => ({
        fieldName: value.field_name,
      })),
  )
  .nullish()
  .transform(emptyArray);

const workflowDerivedWiringSchema: z.ZodType<WorkflowDerivedWiring> = z
  .object({
    nodes: z
      .array(
        z
          .object({
            node_id: z.string(),
            possible_provision_fields: z.array(workflowOutputFieldSchema).nullish().transform(emptyArray),
            join_output_fields: z.array(workflowOutputFieldSchema).nullish().transform(emptyArray),
          })
          .transform((value) => ({
            nodeID: value.node_id,
            possibleProvisionFields: value.possible_provision_fields,
            joinOutputFields: value.join_output_fields,
          })),
      )
      .nullish()
      .transform(emptyArray),
    transition_groups: z
      .array(
        z
          .object({
            transition_group_id: z.string(),
            required_provision_fields: z.array(workflowOutputFieldSchema).nullish().transform(emptyArray),
          })
          .transform((value) => ({
            transitionGroupID: value.transition_group_id,
            requiredProvisionFields: value.required_provision_fields,
          })),
      )
      .nullish()
      .transform(emptyArray),
    edges: z
      .array(
        z
          .object({
            edge_id: z.string(),
            input_bindings: workflowInputBindingsSchema,
            required_provision_fields: z.array(workflowOutputFieldSchema).nullish().transform(emptyArray),
            required_provider_fields: z.array(workflowOutputFieldSchema).nullish().transform(emptyArray),
          })
          .transform((value) => ({
            edgeID: value.edge_id,
            inputBindings: value.input_bindings,
            requiredProvisionFields: value.required_provision_fields,
            requiredProviderFields: value.required_provider_fields,
          })),
      )
      .nullish()
      .transform(emptyArray),
    diagnostics: z.array(validationErrorSchema).nullish().transform(emptyArray),
  })
  .transform((value) => ({
    nodes: value.nodes,
    transitionGroups: value.transition_groups,
    edges: value.edges,
    diagnostics: value.diagnostics,
  }));

const workflowContextSourceSchema = z
  .object({
    kind: z
      .string()
      .nullish()
      .transform((value) => value ?? "immediate_source"),
    node_key: emptyString,
  })
  .nullish()
  .transform((value) => ({
    kind: value?.kind ?? "immediate_source",
    nodeKey: value?.node_key ?? "",
  }));

const workflowEdgesSchema = z
  .array(
    z
      .object({
        id: z.string(),
        workflow_id: z.string(),
        transition_group_id: z.string(),
        key: z.string(),
        target_node_id: z.string(),
        requires_approval: z.boolean(),
        context_mode: z.string(),
        context_source: workflowContextSourceSchema,
        prompt_template: emptyString,
        parameters: z.array(workflowParameterSchema).nullish().transform(emptyArray),
        input_bindings: workflowInputBindingsSchema,
        output_requirements: workflowOutputRequirementsSchema,
      })
      .transform((value) => ({
        id: value.id,
        workflowID: value.workflow_id,
        transitionGroupID: value.transition_group_id,
        key: value.key,
        targetNodeID: value.target_node_id,
        requiresApproval: value.requires_approval,
        contextMode: value.context_mode,
        contextSource: value.context_source,
        promptTemplate: value.prompt_template,
        parameters: value.parameters,
        inputBindings: value.input_bindings,
        outputRequirements: value.output_requirements,
      })),
  )
  .nullish()
  .transform(emptyArray);

const workflowDefinitionValueSchema: z.ZodType<WorkflowDefinition> = z
  .object({
    workflow: workflowRecordSchema,
    node_groups: workflowNodeGroupsSchema,
    nodes: workflowNodesSchema,
    transition_groups: workflowTransitionGroupsSchema,
    edges: workflowEdgesSchema,
    derived_wiring: workflowDerivedWiringSchema
      .nullish()
      .transform((value) => value ?? emptyWorkflowDerivedWiring),
  })
  .transform((value) => ({
    workflow: value.workflow,
    nodeGroups: value.node_groups,
    nodes: value.nodes,
    transitionGroups: value.transition_groups,
    edges: value.edges,
    derivedWiring: value.derived_wiring,
  }));

export const workflowDefinitionSchema: z.ZodType<WorkflowDefinition> = z
  .object({
    definition: workflowDefinitionValueSchema,
  })
  .transform((value) => value.definition);

export const workflowValidationSchema: z.ZodType<WorkflowValidation> = z
  .object({
    valid: z.boolean(),
    errors: z.array(validationErrorSchema).nullish().transform(emptyArray),
  })
  .transform((value) => ({
    valid: value.valid,
    errors: value.errors,
  }));

const workflowGraphValidationResultsSchema: z.ZodType<WorkflowGraphValidationResults> = z.record(
  z.string(),
  workflowValidationSchema,
);

export const workflowGraphValidateDraftSchema: z.ZodType<WorkflowGraphValidateDraftResult> = z
  .object({
    results: workflowGraphValidationResultsSchema,
    derived_wiring: workflowDerivedWiringSchema
      .nullish()
      .transform((value) => value ?? emptyWorkflowDerivedWiring),
  })
  .transform((value) => ({ ...value.results, derivedWiring: value.derived_wiring }));

export const workflowGraphDeriveWiringSchema: z.ZodType<WorkflowDerivedWiring> = z
  .object({
    derived_wiring: workflowDerivedWiringSchema
      .nullish()
      .transform((value) => value ?? emptyWorkflowDerivedWiring),
  })
  .transform((value) => value.derived_wiring);

const workflowGraphSaveImpactSchema: z.ZodType<WorkflowGraphSaveImpact> = z
  .object({
    removed_node_count: z.number(),
    removed_transition_group_count: z.number(),
    removed_edge_count: z.number(),
    node_task_reference_count: z.number(),
    edge_task_reference_count: z.number(),
    active_node_placement_count: z.number(),
    pending_approval_count: z.number(),
    active_run_count: z.number(),
    runnable_run_count: z.number(),
    start_node_change_count: z.number(),
    last_terminal_change_count: z.number(),
    task_referenced_node_kind_change_count: z.number(),
  })
  .transform((value) => ({
    removedNodeCount: value.removed_node_count,
    removedTransitionGroupCount: value.removed_transition_group_count,
    removedEdgeCount: value.removed_edge_count,
    nodeTaskReferenceCount: value.node_task_reference_count,
    edgeTaskReferenceCount: value.edge_task_reference_count,
    activeNodePlacementCount: value.active_node_placement_count,
    pendingApprovalCount: value.pending_approval_count,
    activeRunCount: value.active_run_count,
    runnableRunCount: value.runnable_run_count,
    startNodeChangeCount: value.start_node_change_count,
    lastTerminalChangeCount: value.last_terminal_change_count,
    taskReferencedNodeKindChangeCount: value.task_referenced_node_kind_change_count,
  }));

const workflowGraphSaveBlockersSchema = z
  .array(
    z.object({
      code: z.string(),
      message: z.string(),
      count: z.number(),
    }),
  )
  .nullish()
  .transform(emptyArray);

export const workflowGraphSavePreviewSchema: z.ZodType<WorkflowGraphSavePreview> = z
  .object({
    current_version: z.number(),
    validation_results: workflowGraphValidationResultsSchema,
    impact: workflowGraphSaveImpactSchema,
    blockers: workflowGraphSaveBlockersSchema,
    can_save: z.boolean(),
    confirmation_required: z.boolean(),
  })
  .transform((value) => ({
    currentVersion: value.current_version,
    validationResults: value.validation_results,
    impact: value.impact,
    blockers: value.blockers,
    canSave: value.can_save,
    confirmationRequired: value.confirmation_required,
  }));

export const workflowGraphSaveSchema: z.ZodType<WorkflowGraphSaveResult> = z
  .object({
    saved: z.boolean(),
    definition: workflowDefinitionValueSchema.nullish().transform((value) => value ?? null),
    current_version: z.number(),
    validation_results: workflowGraphValidationResultsSchema,
    impact: workflowGraphSaveImpactSchema,
    blockers: workflowGraphSaveBlockersSchema,
    can_save: z.boolean(),
    confirmation_required: z.boolean(),
  })
  .transform((value) => ({
    saved: value.saved,
    definition: value.definition,
    currentVersion: value.current_version,
    validationResults: value.validation_results,
    impact: value.impact,
    blockers: value.blockers,
    canSave: value.can_save,
    confirmationRequired: value.confirmation_required,
  }));

const workflowDeleteImpactSchema: z.ZodType<WorkflowDeleteImpact> = z
  .object({
    workflow_id: z.string(),
    version: z.number(),
    project_count: z.number(),
    link_count: z.number(),
    default_replacement_project_count: z.number(),
    task_count: z.number(),
    active_run_count: z.number(),
    runnable_run_count: z.number(),
    blocked_task_count: z.number(),
  })
  .transform((value) => ({
    workflowID: value.workflow_id,
    version: value.version,
    projectCount: value.project_count,
    linkCount: value.link_count,
    defaultReplacementProjectCount: value.default_replacement_project_count,
    taskCount: value.task_count,
    activeRunCount: value.active_run_count,
    runnableRunCount: value.runnable_run_count,
    blockedTaskCount: value.blocked_task_count,
  }));

export const workflowDeletePreviewSchema: z.ZodType<WorkflowDeleteImpact> = z
  .object({
    impact: workflowDeleteImpactSchema,
  })
  .transform((value) => value.impact);

export const workflowDeleteResponseSchema: z.ZodType<WorkflowDeleteResponse> = z
  .object({
    deleted: z.boolean(),
    impact: workflowDeleteImpactSchema,
    blockers: z
      .array(
        z.object({
          code: z.string(),
          message: z.string(),
          count: z.number(),
        }),
      )
      .nullish()
      .transform(emptyArray),
  })
  .transform((value) => ({
    deleted: value.deleted,
    impact: value.impact,
    blockers: value.blockers,
  }));
