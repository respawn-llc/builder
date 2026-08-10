import type { WorkflowContextSource, WorkflowJoinInputProvider, WorkflowParameter } from "./models";
import type { WorkflowEdgeSelectionMode } from "./workflowSelectionModels";

export type WorkflowGraphDraftNodeGroup = Readonly<{
  id: string;
  key: string;
  name: string;
}>;

export type WorkflowGraphDraftNode = Readonly<{
  id: string;
  key: string;
  kind: string;
  name: string;
  groupID: string | null;
  subagentRole?: string;
  completionMode?: string | undefined;
  scriptPath?: string | null | undefined;
  joinInputProviders: readonly WorkflowJoinInputProvider[];
}>;

export type WorkflowGraphDraftTransitionGroup = Readonly<{
  id: string;
  sourceNodeID: string;
  transitionID: string;
  name: string;
  description: string;
}>;

export type WorkflowGraphDraftEdge = Readonly<{
  id: string;
  transitionGroupID: string;
  key: string;
  targetNodeID: string;
  assigneeSelection: WorkflowEdgeSelectionMode;
  thinkingSelection: WorkflowEdgeSelectionMode;
  requiresApproval: boolean;
  contextMode: string;
  contextSource: WorkflowContextSource;
  promptTemplate: string;
  parameters: readonly WorkflowParameter[];
}>;

export type WorkflowGraphDraft = Readonly<{
  nodeGroups: readonly WorkflowGraphDraftNodeGroup[];
  nodes: readonly WorkflowGraphDraftNode[];
  transitionGroups: readonly WorkflowGraphDraftTransitionGroup[];
  edges: readonly WorkflowGraphDraftEdge[];
}>;
