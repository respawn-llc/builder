import { compactJsonObject, type JsonObject } from "./json";
import type {
  WorkflowGraphDraft,
  WorkflowGraphMetadata,
  WorkflowGraphSaveConfirmation,
} from "./models";

export function workflowGraphDraftPayload(graph: WorkflowGraphDraft): JsonObject {
  return {
    node_groups: graph.nodeGroups.map((group) => ({
      id: group.id,
      key: group.key,
      display_name: group.name,
    })),
    nodes: graph.nodes.map((node) =>
      compactJsonObject({
        id: node.id,
        key: node.key,
        kind: node.kind,
        display_name: node.name,
        group_id: node.groupID.length > 0 ? node.groupID : undefined,
        group_key: node.groupKey.length > 0 ? node.groupKey : undefined,
        subagent_role: node.subagentRole.length > 0 ? node.subagentRole : undefined,
        prompt_template: node.promptTemplate.length > 0 ? node.promptTemplate : undefined,
        completion_mode:
          node.completionMode !== undefined && node.completionMode.length > 0
            ? node.completionMode
            : undefined,
        script_path:
          typeof node.scriptPath === "string" && node.scriptPath.trim().length > 0
            ? node.scriptPath
            : undefined,
        input_fields: node.inputFields.map((field) => ({
          name: field.name,
          description: field.description,
        })),
        join_input_providers: node.joinInputProviders.map((provider) => ({
          input_name: provider.inputName,
          provider_edge_id: provider.providerEdgeID,
        })),
      }),
    ),
    transition_groups: graph.transitionGroups.map((group) =>
      compactJsonObject({
        id: group.id,
        source_node_id: group.sourceNodeID,
        transition_id: group.transitionID,
        display_name: group.name,
        description: group.description.length > 0 ? group.description : undefined,
      }),
    ),
    edges: graph.edges.map((edge) =>
      compactJsonObject({
        id: edge.id,
        transition_group_id: edge.transitionGroupID,
        key: edge.key,
        target_node_id: edge.targetNodeID,
        requires_approval: edge.requiresApproval,
        context_mode: edge.contextMode,
        context_source: {
          kind: edge.contextSource.kind,
          node_key: edge.contextSource.nodeKey,
        },
        prompt_template: edge.promptTemplate.length > 0 ? edge.promptTemplate : undefined,
        parameters: edge.parameters.map((parameter) => ({
          key: parameter.key,
          description: parameter.description,
        })),
      }),
    ),
  };
}

export function workflowGraphMetadataPayload(
  metadata: WorkflowGraphMetadata | undefined,
): JsonObject | undefined {
  if (!metadata) {
    return undefined;
  }
  return {
    name: metadata.name,
    description: metadata.description,
  };
}

export function workflowGraphSaveConfirmationPayload(
  confirmation: WorkflowGraphSaveConfirmation | undefined,
): JsonObject | undefined {
  if (!confirmation) {
    return undefined;
  }
  return {
    expected_removed_node_count: confirmation.expectedRemovedNodeCount,
    expected_removed_transition_group_count: confirmation.expectedRemovedTransitionGroupCount,
    expected_removed_edge_count: confirmation.expectedRemovedEdgeCount,
    expected_node_task_reference_count: confirmation.expectedNodeTaskReferenceCount,
    expected_edge_task_reference_count: confirmation.expectedEdgeTaskReferenceCount,
  };
}
