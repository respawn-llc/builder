import type { WorkflowDefinition, WorkflowNode, WorkflowTransitionGroup } from "@/api";

export function fallbackLabel(fallback: string, ...candidates: readonly (string | undefined)[]): string {
  return candidates.find((candidate) => candidate !== undefined && candidate.trim().length > 0) ?? fallback;
}

export function transitionGroupByID(
  definition: WorkflowDefinition,
  transitionGroupID: string,
): WorkflowTransitionGroup | undefined {
  return definition.transitionGroups.find((group) => group.id === transitionGroupID);
}

export function nodeByID(definition: WorkflowDefinition, nodeID: string): WorkflowNode | undefined {
  return definition.nodes.find((node) => node.id === nodeID);
}

export function transitionGroupIsFanOut(definition: WorkflowDefinition, transitionGroupID: string): boolean {
  return definition.edges.filter((edge) => edge.transitionGroupID === transitionGroupID).length > 1;
}
