import type { WorkflowEdgeSelectionMode, WorkflowParameterPurpose } from "@/api";
import type { DraftWorkflowEdge } from "./workflowEditorDraftTypes";

export type WorkflowEdgeSelector = "assignee" | "thinking";

export function protectedParameterPurposeForSelector(
  selector: WorkflowEdgeSelector,
): Extract<WorkflowParameterPurpose, "target_assignee" | "target_thinking"> {
  return selector === "assignee" ? "target_assignee" : "target_thinking";
}

export function protectedParameterDefaultKey(selector: WorkflowEdgeSelector): string {
  return selector === "assignee" ? "agent_role" : "thinking_level";
}

export function edgeSelectorMode(
  edge: Pick<DraftWorkflowEdge, "assigneeSelection" | "thinkingSelection">,
  selector: WorkflowEdgeSelector,
): WorkflowEdgeSelectionMode {
  return selector === "assignee" ? edge.assigneeSelection : edge.thinkingSelection;
}

export function setWorkflowEdgeSelector(
  edge: DraftWorkflowEdge,
  selector: WorkflowEdgeSelector,
  selection: WorkflowEdgeSelectionMode,
): DraftWorkflowEdge {
  const purpose = protectedParameterPurposeForSelector(selector);
  const parameters = [...edge.parameters];
  const existingIndex = parameters.findIndex((parameter) => parameter.purpose === purpose);
  if (selection === "previous_node" && existingIndex < 0) {
    parameters.push({
      description: "",
      key: protectedParameterDefaultKey(selector),
      purpose,
      rowID: [edge.id, "parameter", purpose].join(":"),
    });
  }
  return selector === "assignee"
    ? { ...edge, assigneeSelection: selection, parameters }
    : { ...edge, parameters, thinkingSelection: selection };
}

export function isProtectedWorkflowParameter(parameter: { purpose: WorkflowParameterPurpose }): boolean {
  return parameter.purpose === "target_assignee" || parameter.purpose === "target_thinking";
}

export function visibleWorkflowEdgeParameters(
  edge: Pick<DraftWorkflowEdge, "parameters" | "assigneeSelection" | "thinkingSelection">,
  protectedParameterVisibility: Readonly<{
    target_assignee?: boolean;
    target_thinking?: boolean;
  }> = {},
): readonly DraftWorkflowEdge["parameters"][number][] {
  return edge.parameters.filter((parameter) => {
    if (parameter.purpose === "target_assignee") {
      return (
        edge.assigneeSelection === "previous_node" && protectedParameterVisibility.target_assignee !== false
      );
    }
    if (parameter.purpose === "target_thinking") {
      return (
        edge.thinkingSelection === "previous_node" && protectedParameterVisibility.target_thinking !== false
      );
    }
    return true;
  });
}
