import type { WorkflowDefinition, WorkflowEdge, WorkflowNode, WorkflowParameter } from "@/api";

export type DraftWorkflowParameter = WorkflowParameter &
  Readonly<{
    rowID?: string;
  }>;

export type DraftWorkflowNode = Omit<WorkflowNode, "completionMode"> &
  Readonly<{
    completionMode: string;
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
