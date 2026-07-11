import type { WorkflowNodeKind } from "../../api";
import type { CreatableWorkflowNodeKind } from "./workflowEditorGraphMutationTypes";

export type WorkflowNodeKindPickerChoice = Readonly<{
  kind: CreatableWorkflowNodeKind;
  labelKey: "workflowEditor.addAgentNode" | "workflowEditor.addScriptNode" | "workflowEditor.addTerminalNode";
}>;

export const creatableWorkflowNodeKinds: readonly WorkflowNodeKindPickerChoice[] = [
  { kind: "agent", labelKey: "workflowEditor.addAgentNode" },
  { kind: "script", labelKey: "workflowEditor.addScriptNode" },
  { kind: "terminal", labelKey: "workflowEditor.addTerminalNode" },
];

export function isInspectableWorkflowNodeKind(kind: string): kind is WorkflowNodeKind {
  return kind === "agent" || kind === "script" || kind === "join" || kind === "start" || kind === "terminal";
}

export function hasWorkflowNodeMetadataTooltip(kind: string): boolean {
  return kind === "join";
}
