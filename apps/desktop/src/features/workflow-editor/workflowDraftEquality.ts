import type {
  WorkflowContextSource,
  WorkflowDefinition,
  WorkflowEdge,
  WorkflowNode,
  WorkflowNodeGroup,
  WorkflowTransitionGroup,
} from "../../api";

export function workflowGraphsEqual(left: WorkflowDefinition, right: WorkflowDefinition): boolean {
  return (
    nodeGroupsEqual(left.nodeGroups, right.nodeGroups) &&
    nodesEqual(left.nodes, right.nodes) &&
    transitionGroupsEqual(left.transitionGroups, right.transitionGroups) &&
    edgesEqual(left.edges, right.edges)
  );
}

function nodeGroupsEqual(left: readonly WorkflowNodeGroup[], right: readonly WorkflowNodeGroup[]): boolean {
  return sameLengthAndEvery(left, right, (a, b) => a.id === b.id && a.key === b.key && a.name === b.name);
}

function nodesEqual(left: readonly WorkflowNode[], right: readonly WorkflowNode[]): boolean {
  return sameLengthAndEvery(left, right, workflowNodesEqual);
}

function workflowNodesEqual(a: WorkflowNode, b: WorkflowNode): boolean {
  return (
    workflowNodeMetadataEqual(a, b) &&
    inputFieldsEqual(a.inputFields, b.inputFields) &&
    joinInputProvidersEqual(a.joinInputProviders, b.joinInputProviders)
  );
}

function workflowNodeMetadataEqual(a: WorkflowNode, b: WorkflowNode): boolean {
  return sameLengthAndEvery(workflowNodeMetadataValues(a), workflowNodeMetadataValues(b), (left, right) => left === right);
}

function workflowNodeMetadataValues(node: WorkflowNode): readonly string[] {
  return [
    node.id,
    node.key,
    node.kind,
    node.name,
    node.groupID,
    node.groupKey,
    node.subagentRole,
    node.promptTemplate,
    node.completionMode ?? "",
    node.scriptPath ?? "",
  ];
}

function transitionGroupsEqual(
  left: readonly WorkflowTransitionGroup[],
  right: readonly WorkflowTransitionGroup[],
): boolean {
  return sameLengthAndEvery(
    left,
    right,
    (a, b) =>
      a.id === b.id &&
      a.sourceNodeID === b.sourceNodeID &&
      a.transitionID === b.transitionID &&
      a.name === b.name &&
      a.description === b.description,
  );
}

function edgesEqual(left: readonly WorkflowEdge[], right: readonly WorkflowEdge[]): boolean {
  return sameLengthAndEvery(
    left,
    right,
    (a, b) =>
      a.id === b.id &&
      a.transitionGroupID === b.transitionGroupID &&
      a.key === b.key &&
      a.targetNodeID === b.targetNodeID &&
      a.requiresApproval === b.requiresApproval &&
      a.contextMode === b.contextMode &&
      contextSourceEqual(a.contextSource, b.contextSource) &&
      a.promptTemplate === b.promptTemplate &&
      parametersEqual(a.parameters, b.parameters),
  );
}

function inputFieldsEqual(
  left: readonly WorkflowDefinition["nodes"][number]["inputFields"][number][],
  right: readonly WorkflowDefinition["nodes"][number]["inputFields"][number][],
): boolean {
  return sameLengthAndEvery(left, right, (a, b) => a.name === b.name && a.description === b.description);
}

function parametersEqual(
  left: readonly WorkflowDefinition["edges"][number]["parameters"][number][],
  right: readonly WorkflowDefinition["edges"][number]["parameters"][number][],
): boolean {
  return sameLengthAndEvery(left, right, (a, b) => a.key === b.key && a.description === b.description);
}

function joinInputProvidersEqual(
  left: readonly WorkflowDefinition["nodes"][number]["joinInputProviders"][number][],
  right: readonly WorkflowDefinition["nodes"][number]["joinInputProviders"][number][],
): boolean {
  if (left.length !== right.length) {
    return false;
  }
  const rightByInputName = new Map(right.map((provider) => [provider.inputName, provider.providerEdgeID]));
  return left.every((provider) => rightByInputName.get(provider.inputName) === provider.providerEdgeID);
}

function contextSourceEqual(left: WorkflowContextSource, right: WorkflowContextSource): boolean {
  return left.kind === right.kind && left.nodeKey === right.nodeKey;
}

function sameLengthAndEvery<T>(
  left: readonly T[],
  right: readonly T[],
  equal: (left: T, right: T) => boolean,
): boolean {
  return (
    left.length === right.length &&
    left.every((item, index) => right[index] !== undefined && equal(item, right[index]))
  );
}
