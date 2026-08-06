export const workflowParameterPurposes = ["ordinary", "target_assignee", "target_thinking"] as const;
export type WorkflowParameterPurpose = (typeof workflowParameterPurposes)[number];

export const workflowSelectorApplicabilityReasons = [
  "eligible",
  "topology",
  "context_source",
  "no_callable_roles",
  "no_thinking_support",
  "unavailable_configuration",
  "sole_callable_role",
  "no_thinking_levels",
  "sole_thinking_level",
] as const;
export type WorkflowSelectorApplicabilityReason = (typeof workflowSelectorApplicabilityReasons)[number];
export type WorkflowSelectorApplicability = Readonly<{
  available: boolean;
  parameterVisible: boolean;
  reason: WorkflowSelectorApplicabilityReason;
}>;

export const workflowEdgeSelectionModes = ["configured", "previous_node"] as const;
export type WorkflowEdgeSelectionMode = (typeof workflowEdgeSelectionModes)[number];
