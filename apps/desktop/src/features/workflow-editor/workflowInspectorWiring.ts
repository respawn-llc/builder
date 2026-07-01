import { useQueryClient } from "@tanstack/react-query";
import { useSyncExternalStore } from "react";
import type { useTranslation } from "react-i18next";

import type {
  ServerReadiness,
  WorkflowContextSource,
  WorkflowDefinition,
  WorkflowEdge,
  WorkflowNode,
  WorkflowParameter,
  WorkflowTransitionGroup,
  WorkflowValidation,
} from "../../api";
import { queryKeys } from "../../app/queryKeys";
import { type SelectFieldOption } from "../../ui";
import { fallbackLabel, nodeByID, transitionGroupByID } from "./workflowInspectorModel";

export type Translate = ReturnType<typeof useTranslation>["t"];

export const emptyWorkflowValidation: WorkflowValidation = { errors: [], valid: true };

export const immediateContextSourceOption = "__immediate_context_source__";
export const missingContextSourceOption = "__missing_context_source__";
export const previousTargetContextSourceOption = "__previous_target_context_source__";
export const previousTargetOrNewContextSourceOption = "__previous_target_or_new_context_source__";
export const immediateContextSource: WorkflowContextSource = { kind: "immediate_source", nodeKey: "" };
export const previousTargetContextSource: WorkflowContextSource = { kind: "previous_target", nodeKey: "" };
export const previousTargetOrNewContextSource: WorkflowContextSource = {
  kind: "previous_target_or_new",
  nodeKey: "",
};

export function edgeDetails(
  definition: WorkflowDefinition,
  edge: WorkflowEdge,
  validation: WorkflowValidation,
) {
  const group = transitionGroupByID(definition, edge.transitionGroupID);
  const source = sourceNodeForTransition(definition, group);
  const target = nodeByID(definition, edge.targetNodeID);
  const errors = edgeValidationGroups(validation, edge);
  return {
    ...edgeEndpointDetails(source, target),
    ...edgeTransitionDetails(group),
    ...errors,
    hasErrors: errors.directErrors.length + errors.groupErrors.length > 0,
  };
}

export function sourceNodeForTransition(
  definition: WorkflowDefinition,
  group: WorkflowTransitionGroup | undefined,
): WorkflowNode | undefined {
  return group === undefined ? undefined : nodeByID(definition, group.sourceNodeID);
}

export function edgeEndpointDetails(source: WorkflowNode | undefined, target: WorkflowNode | undefined) {
  return {
    sourceKind: source?.kind ?? "",
    sourceLabel: fallbackLabel("", source?.name, source?.key),
    targetKind: target?.kind ?? "",
    targetLabel: fallbackLabel("", target?.name, target?.key),
  };
}

export function edgeTransitionDetails(group: WorkflowTransitionGroup | undefined) {
  return {
    transitionGroupLabel: fallbackLabel("", group?.name, group?.id),
    transitionDescription: group?.description ?? "",
    transitionID: group?.transitionID ?? "",
  };
}

export function edgeValidationGroups(validation: WorkflowValidation, edge: WorkflowEdge) {
  return {
    directErrors: validation.errors.filter((error) => error.edgeID === edge.id),
    groupErrors: validation.errors.filter(
      (error) => error.edgeID !== edge.id && error.transitionGroupID === edge.transitionGroupID,
    ),
  };
}

export function derivedNodeWiring(definition: WorkflowDefinition, nodeID: string) {
  return (
    definition.derivedWiring.nodes.find((wiring) => wiring.nodeID === nodeID) ?? {
      joinOutputFields: [],
      nodeID,
      possibleProvisionFields: [],
    }
  );
}

export function derivedEdgeWiring(definition: WorkflowDefinition, edgeID: string) {
  return (
    definition.derivedWiring.edges.find((wiring) => wiring.edgeID === edgeID) ?? {
      edgeID,
      inputBindings: [],
      requiredProviderFields: [],
      requiredProvisionFields: [],
    }
  );
}

export function edgePromptPlaceholderParameters(
  definition: WorkflowDefinition,
  edge: WorkflowEdge,
): readonly WorkflowParameter[] {
  const source = edgeSourceNode(definition, edge);
  if (source?.kind === "join") {
    return derivedNodeWiring(definition, source.id).joinOutputFields.map((field) => ({
      description: field.description,
      key: field.name,
    }));
  }
  return edge.parameters;
}

export function parameterSummaryFields(
  parameters: readonly WorkflowParameter[],
): readonly { name: string; description: string }[] {
  return parameters.map((parameter) => ({ description: parameter.description, name: parameter.key }));
}

export function edgeSourceNode(definition: WorkflowDefinition, edge: WorkflowEdge): WorkflowNode | undefined {
  const group = transitionGroupByID(definition, edge.transitionGroupID);
  return group === undefined ? undefined : nodeByID(definition, group.sourceNodeID);
}

export function joinProviderOptions(
  definition: WorkflowDefinition,
  joinNodeID: string,
  selectedEdgeID: string,
): readonly SelectFieldOption[] {
  const options = definition.edges
    .filter((edge) => edge.targetNodeID === joinNodeID)
    .map((edge) => ({
      label: providerEdgeLabel(definition, edge.id),
      textValue: providerEdgeLabel(definition, edge.id),
      value: edge.id,
    }));
  if (selectedEdgeID.length === 0 || options.some((option) => option.value === selectedEdgeID)) {
    return options;
  }
  return [
    ...options,
    {
      disabled: true,
      label: selectedEdgeID,
      textValue: selectedEdgeID,
      value: selectedEdgeID,
    },
  ];
}

export function providerEdgeLabel(definition: WorkflowDefinition, edgeID: string): string {
  const edge = definition.edges.find((item) => item.id === edgeID);
  if (edge === undefined) {
    return edgeID;
  }
  const group = transitionGroupByID(definition, edge.transitionGroupID);
  const source = group === undefined ? undefined : nodeByID(definition, group.sourceNodeID);
  const sourceLabel = fallbackLabel(edge.key, source?.name, source?.key);
  return `${sourceLabel} / ${edge.key}`;
}

export function formatContextModeLabel(mode: string, translate: Translate): string {
  if (mode === "new_session") {
    return translate("workflowEditor.contextModeNewSession");
  }
  if (mode === "continue_session") {
    return translate("workflowEditor.contextModeContinueSession");
  }
  if (mode === "compact_and_continue_session") {
    return translate("workflowEditor.contextModeCompactContinueSession");
  }
  return mode;
}

export function formatContextSourceLabel(edge: WorkflowEdge, translate: Translate): string {
  if (edge.contextSource.kind === "selected_node") {
    return edge.contextSource.nodeKey.length > 0
      ? translate("workflowEditor.contextSourceNode", { nodeKey: edge.contextSource.nodeKey })
      : translate("workflowEditor.contextSourceSelected");
  }
  if (edge.contextSource.kind === "previous_target") {
    return translate("workflowEditor.contextSourcePreviousTarget");
  }
  if (edge.contextSource.kind === "previous_target_or_new") {
    return translate("workflowEditor.contextSourcePreviousTargetOrNew");
  }
  return translate("workflowEditor.contextSourceImmediate");
}

export function contextModeOptions(
  translate: Translate,
  continuationDisabled = false,
): readonly SelectFieldOption[] {
  return [
    {
      label: translate("workflowEditor.contextModeNewSession"),
      textValue: translate("workflowEditor.contextModeNewSession"),
      value: "new_session",
    },
    {
      disabled: continuationDisabled,
      label: translate("workflowEditor.contextModeContinueSession"),
      textValue: translate("workflowEditor.contextModeContinueSession"),
      value: "continue_session",
    },
    {
      disabled: continuationDisabled,
      label: translate("workflowEditor.contextModeCompactContinueSession"),
      textValue: translate("workflowEditor.contextModeCompactContinueSession"),
      value: "compact_and_continue_session",
    },
  ];
}

export function contextSourceOptions(
  definition: WorkflowDefinition,
  edge: WorkflowEdge,
  translate: Translate,
): readonly SelectFieldOption[] {
  const disabledReason = translate("workflowEditor.contextSourceUnavailable");
  const nodeCandidates = contextSourceNodeCandidates(definition, edge);
  const nodeOptions = nodeCandidates.map((node) => {
    const label = fallbackLabel(node.key, node.name, node.key);
    return withContextSourceAvailability(
      {
        label,
        textValue: label,
        value: node.id,
      },
      selectedNodeContextSourceAvailable(definition, edge, node),
      disabledReason,
    );
  });
  const options: SelectFieldOption[] = [
    withContextSourceAvailability(
      {
        label: translate("workflowEditor.contextSourceImmediate"),
        textValue: translate("workflowEditor.contextSourceImmediate"),
        value: immediateContextSourceOption,
      },
      immediateContextSourceAvailable(definition, edge),
      disabledReason,
    ),
    withContextSourceAvailability(
      {
        label: translate("workflowEditor.contextSourcePreviousTarget"),
        textValue: translate("workflowEditor.contextSourcePreviousTarget"),
        value: previousTargetContextSourceOption,
      },
      previousTargetContextSourceAvailable(definition, edge),
      disabledReason,
    ),
    withContextSourceAvailability(
      {
        label: translate("workflowEditor.contextSourcePreviousTargetOrNew"),
        textValue: translate("workflowEditor.contextSourcePreviousTargetOrNew"),
        value: previousTargetOrNewContextSourceOption,
      },
      previousTargetOrNewContextSourceAvailable(definition, edge),
      disabledReason,
    ),
    ...nodeOptions,
  ];
  if (
    edge.contextSource.kind === "selected_node" &&
    !nodeCandidates.some((node) => node.key === edge.contextSource.nodeKey)
  ) {
    options.push({
      disabled: true,
      disabledReason,
      label:
        edge.contextSource.nodeKey.length > 0
          ? edge.contextSource.nodeKey
          : translate("workflowEditor.contextSourceSelected"),
      textValue: edge.contextSource.nodeKey,
      value: missingContextSourceOption,
    });
  }
  return options;
}

export function contextSourceSelectValue(definition: WorkflowDefinition, edge: WorkflowEdge): string {
  if (edge.contextSource.kind === "previous_target") {
    return previousTargetContextSourceOption;
  }
  if (edge.contextSource.kind === "previous_target_or_new") {
    return previousTargetOrNewContextSourceOption;
  }
  if (edge.contextSource.kind !== "selected_node") {
    return immediateContextSourceOption;
  }
  return (
    contextSourceNodeCandidates(definition, edge).find((node) => node.key === edge.contextSource.nodeKey)
      ?.id ?? missingContextSourceOption
  );
}

export function contextSourceFromSelectValue(
  definition: WorkflowDefinition,
  value: string,
): WorkflowContextSource {
  if (value === immediateContextSourceOption) {
    return immediateContextSource;
  }
  if (value === previousTargetContextSourceOption) {
    return previousTargetContextSource;
  }
  if (value === previousTargetOrNewContextSourceOption) {
    return previousTargetOrNewContextSource;
  }
  const node = definition.nodes.find((item) => item.id === value);
  return { kind: "selected_node", nodeKey: node?.key ?? "" };
}

export function previousTargetContextSourceAvailable(
  definition: WorkflowDefinition,
  edge: WorkflowEdge,
): boolean {
  if (!contextModeUsesSource(edge.contextMode)) {
    return false;
  }
  const target = nodeByID(definition, edge.targetNodeID);
  if (target?.kind !== "agent") {
    return false;
  }
  const sourceNodeID = transitionGroupByID(definition, edge.transitionGroupID)?.sourceNodeID;
  const startNodes = definition.nodes.filter((node) => node.kind === "start");
  if (sourceNodeID === undefined || startNodes.length !== 1) {
    return true;
  }
  return nodeDominates(definition, target.id, sourceNodeID);
}

export function immediateContextSourceAvailable(definition: WorkflowDefinition, edge: WorkflowEdge): boolean {
  if (!contextModeUsesSource(edge.contextMode)) {
    return true;
  }
  return edgeSourceNode(definition, edge)?.kind === "agent";
}

export function previousTargetOrNewContextSourceAvailable(
  definition: WorkflowDefinition,
  edge: WorkflowEdge,
): boolean {
  return (
    contextModeUsesSource(edge.contextMode) &&
    nodeByID(definition, edge.targetNodeID)?.kind === "agent" &&
    edgeSourceNode(definition, edge)?.kind !== "start"
  );
}

export function validContextSourceNodes(
  definition: WorkflowDefinition,
  edge: WorkflowEdge,
): WorkflowDefinition["nodes"] {
  return contextSourceNodeCandidates(definition, edge).filter((node) =>
    selectedNodeContextSourceAvailable(definition, edge, node),
  );
}

export function contextSourceNodeCandidates(
  definition: WorkflowDefinition,
  edge: WorkflowEdge,
): WorkflowDefinition["nodes"] {
  if (nodeByID(definition, edge.targetNodeID)?.kind !== "agent") {
    return [];
  }
  return definition.nodes.filter((node) => node.kind === "agent");
}

export function selectedNodeContextSourceAvailable(
  definition: WorkflowDefinition,
  edge: WorkflowEdge,
  node: WorkflowNode,
): boolean {
  return (
    contextModeUsesSource(edge.contextMode) &&
    node.kind === "agent" &&
    node.id !== edge.targetNodeID &&
    contextSourceNodeIsGuaranteedBeforeEdgeSource(definition, edge, node.id)
  );
}

export function contextSourceNodeIsGuaranteedBeforeEdgeSource(
  definition: WorkflowDefinition,
  edge: WorkflowEdge,
  nodeID: string,
): boolean {
  const sourceNodeID = transitionGroupByID(definition, edge.transitionGroupID)?.sourceNodeID;
  const startNodes = definition.nodes.filter((node) => node.kind === "start");
  if (sourceNodeID === undefined || startNodes.length !== 1) {
    return true;
  }
  return nodeDominates(definition, nodeID, sourceNodeID);
}

export function nodeDominates(
  definition: WorkflowDefinition,
  candidateID: string,
  targetID: string,
): boolean {
  if (candidateID === targetID) {
    return true;
  }
  const startNodeID = definition.nodes.find((node) => node.kind === "start")?.id;
  if (startNodeID === undefined) {
    return false;
  }
  return !reachableFromSkipping(definition, startNodeID, candidateID).has(targetID);
}

export function reachableFromSkipping(
  definition: WorkflowDefinition,
  startNodeID: string,
  skippedNodeID: string,
): ReadonlySet<string> {
  const visited = new Set<string>();
  const stack = startNodeID === skippedNodeID ? [] : [startNodeID];
  while (stack.length > 0) {
    const nodeID = stack.pop();
    if (nodeID === undefined || visited.has(nodeID) || nodeID === skippedNodeID) {
      continue;
    }
    visited.add(nodeID);
    for (const targetNodeID of outgoingTargetNodeIDs(definition, nodeID)) {
      if (!visited.has(targetNodeID) && targetNodeID !== skippedNodeID) {
        stack.push(targetNodeID);
      }
    }
  }
  return visited;
}

export function outgoingTargetNodeIDs(
  definition: WorkflowDefinition,
  sourceNodeID: string,
): readonly string[] {
  const outgoingTransitionGroupIDs = new Set(
    definition.transitionGroups
      .filter((group) => group.sourceNodeID === sourceNodeID)
      .map((group) => group.id),
  );
  return definition.edges
    .filter((edge) => outgoingTransitionGroupIDs.has(edge.transitionGroupID))
    .map((edge) => edge.targetNodeID);
}

function contextModeUsesSource(contextMode: string): boolean {
  return contextMode === "continue_session" || contextMode === "compact_and_continue_session";
}

function withContextSourceAvailability(
  option: SelectFieldOption,
  available: boolean,
  disabledReason: string,
): SelectFieldOption {
  if (available) {
    return option;
  }
  return { ...option, disabled: true, disabledReason };
}

export function workflowAssigneeOptions(
  definition: WorkflowDefinition,
  roles: ServerReadiness["subagentRoles"],
): readonly SelectFieldOption[] {
  const roleNames = new Set<string>();
  roleNames.add("default");
  for (const role of roles) {
    if (role.name.trim().length > 0) {
      roleNames.add(role.name);
    }
  }
  const workflowRoleNames = definition.nodes
    .filter((node) => node.kind === "agent")
    .map((node) => node.subagentRole.trim())
    .filter((role) => role.length > 0)
    .sort((left, right) => left.localeCompare(right));
  for (const role of workflowRoleNames) {
    roleNames.add(role);
  }
  return [...roleNames].map((role) => ({ label: role, textValue: role, value: role }));
}

export function useWorkflowAssigneeOptions(definition: WorkflowDefinition): readonly SelectFieldOption[] {
  const readiness = useCachedServerReadiness();
  return workflowAssigneeOptions(definition, readiness?.subagentRoles ?? []);
}

export function workflowCompletionModeOptions(t: (key: string) => string): readonly SelectFieldOption[] {
  return [
    { label: t("workflowEditor.completionModeInherit"), value: "" },
    { label: t("workflowEditor.completionModeAuto"), value: "auto" },
    { label: t("workflowEditor.completionModeStructuredOutput"), value: "structured_output" },
    { label: t("workflowEditor.completionModeTool"), value: "tool" },
    { label: t("workflowEditor.completionModeShellCommand"), value: "shell_command" },
    { label: t("workflowEditor.completionModeUnstructuredOutput"), value: "unstructured_output" },
  ];
}

export function useCachedServerReadiness(): ServerReadiness | undefined {
  const queryClient = useQueryClient();
  return useSyncExternalStore(
    (onStoreChange) => queryClient.getQueryCache().subscribe(onStoreChange),
    () => queryClient.getQueryData<ServerReadiness>(queryKeys.readiness),
    () => queryClient.getQueryData<ServerReadiness>(queryKeys.readiness),
  );
}

export function useCachedWorkflowDefinition(workflowID: string): WorkflowDefinition | undefined {
  const queryKey = queryKeys.workflowDefinition(workflowID);
  const queryClient = useQueryClient();
  return useSyncExternalStore(
    (onStoreChange) => queryClient.getQueryCache().subscribe(onStoreChange),
    () => queryClient.getQueryData<WorkflowDefinition>(queryKey),
    () => queryClient.getQueryData<WorkflowDefinition>(queryKey),
  );
}

export function useCachedWorkflowValidation(workflowID: string): WorkflowValidation | undefined {
  const queryKey = queryKeys.workflowValidation(workflowID, "execution");
  const queryClient = useQueryClient();
  return useSyncExternalStore(
    (onStoreChange) => queryClient.getQueryCache().subscribe(onStoreChange),
    () => queryClient.getQueryData<WorkflowValidation>(queryKey),
    () => queryClient.getQueryData<WorkflowValidation>(queryKey),
  );
}
