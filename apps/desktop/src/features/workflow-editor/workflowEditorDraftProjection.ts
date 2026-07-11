import type { WorkflowDefinition, WorkflowGraphDraft, WorkflowGraphMetadata } from "../../api";
import { workflowGraphsEqual } from "./workflowDraftEquality";
import type {
  DraftWorkflowDefinition,
  WorkflowEditorDirtyState,
  WorkflowEditorDraftState,
} from "./workflowEditorDraft";
import { workflowExecutionPolicy } from "./workflowExecutionPolicyDraft";

export function workflowDefinitionFromDraft(draft: DraftWorkflowDefinition): WorkflowDefinition {
  const { executionPolicyCustomRef, ...definition } = draft;
  return {
    ...definition,
    workflow: {
      ...definition.workflow,
      executionPolicy: workflowExecutionPolicy(
        definition.workflow.executionPolicy.mode,
        executionPolicyCustomRef,
      ),
    },
    edges: draft.edges.map((edge) => ({
      ...edge,
      parameters: edge.parameters.map(({ description, key }) => ({ description, key })),
    })),
    nodes: draft.nodes.map((node) => ({
      ...node,
      inputFields: node.inputFields.map(({ name, description }) => ({ name, description })),
      outputFields: node.outputFields,
    })),
  };
}

export function workflowEditorDirtyState(state: WorkflowEditorDraftState): WorkflowEditorDirtyState {
  const sourcePolicy = state.source.workflow.executionPolicy;
  const currentPolicy = workflowExecutionPolicy(
    state.draft.workflow.executionPolicy.mode,
    state.draft.executionPolicyCustomRef,
  );
  const metadataDirty =
    state.draft.workflow.name !== state.source.workflow.name ||
    state.draft.workflow.description !== state.source.workflow.description ||
    currentPolicy.mode !== sourcePolicy.mode ||
    currentPolicy.customRef !== sourcePolicy.customRef;
  const graphDirty = !workflowGraphsEqual(workflowDefinitionFromDraft(state.draft), state.source);
  return { dirty: metadataDirty || graphDirty, graphDirty, metadataDirty };
}

export function workflowEditorDraftGraph(state: WorkflowEditorDraftState): WorkflowGraphDraft {
  const definition = workflowDefinitionFromDraft(state.draft);
  return {
    edges: definition.edges.map((edge) => ({
      contextMode: edge.contextMode,
      contextSource: edge.contextSource,
      id: edge.id,
      key: edge.key,
      parameters: edge.parameters.map(({ description, key }) => ({ description, key })),
      promptTemplate: edge.promptTemplate,
      requiresApproval: edge.requiresApproval,
      targetNodeID: edge.targetNodeID,
      transitionGroupID: edge.transitionGroupID,
    })),
    nodeGroups: definition.nodeGroups.map((group) => ({ id: group.id, key: group.key, name: group.name })),
    nodes: definition.nodes.map((node) => ({
      groupID: node.groupID,
      groupKey: node.groupKey,
      id: node.id,
      key: node.key,
      kind: node.kind,
      name: node.name,
      completionMode: node.completionMode,
      scriptPath: node.scriptPath,
      inputFields: node.inputFields,
      joinInputProviders: node.joinInputProviders,
      promptTemplate: node.promptTemplate,
      subagentRole: node.subagentRole,
    })),
    transitionGroups: definition.transitionGroups.map((group) => ({
      description: group.description,
      id: group.id,
      name: group.name,
      sourceNodeID: group.sourceNodeID,
      transitionID: group.transitionID,
    })),
  };
}

export function workflowEditorDraftMetadata(state: WorkflowEditorDraftState): WorkflowGraphMetadata {
  return {
    description: state.draft.workflow.description,
    executionPolicy: workflowExecutionPolicy(
      state.draft.workflow.executionPolicy.mode,
      state.draft.executionPolicyCustomRef,
    ),
    name: state.draft.workflow.name,
  };
}
