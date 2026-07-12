import type {
  WorkflowDefinition,
  WorkflowEdge,
  WorkflowNode,
  WorkflowParameter,
} from "../../api";

export type DraftInputField = Readonly<{
  rowID: string;
  name: string;
  description: string;
}>;

export type DraftWorkflowParameter = WorkflowParameter &
  Readonly<{
    rowID?: string;
  }>;

export type DraftWorkflowNode = Omit<WorkflowNode, "completionMode" | "inputFields"> &
  Readonly<{
    completionMode: string;
    inputFields: readonly DraftInputField[];
  }>;

export type DraftWorkflowEdge = Omit<WorkflowEdge, "parameters"> &
  Readonly<{
    parameters: readonly DraftWorkflowParameter[];
  }>;

export type DraftWorkflowDefinition = Omit<WorkflowDefinition, "edges" | "nodes"> &
  Readonly<{
    edges: readonly DraftWorkflowEdge[];
    nodes: readonly DraftWorkflowNode[];
  }>;
