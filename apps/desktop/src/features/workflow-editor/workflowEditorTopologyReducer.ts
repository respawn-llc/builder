import {
  addConnectedWorkflowNode,
  addWorkflowNode,
  addWorkflowNodeToGroup,
  connectWorkflowNodes,
  createWorkflowNodeGroupFromNode,
  deleteWorkflowEdge,
  deleteWorkflowNode,
  deleteWorkflowNodeGroup,
  editWorkflowEdgeRoute,
  extractWorkflowNodeFromGroup,
  reconnectWorkflowEdge,
  removeWorkflowNodeFromGroup,
  type AddConnectedWorkflowNodeInput,
  type AddWorkflowNodeInput,
  type AddWorkflowNodeToGroupInput,
  type ConnectWorkflowNodesInput,
  type CreateWorkflowNodeGroupInput,
  type EditWorkflowEdgeRouteInput,
  type ExtractWorkflowNodeFromGroupInput,
  type ReconnectWorkflowEdgeInput,
  type WorkflowEditorGraphMutationResult,
} from "./workflowEditorGraphMutations";
import type { DraftWorkflowDefinition } from "./workflowEditorDraft";

type TopologyAction = Extract<
  | Readonly<{ type: "addNode"; input: AddWorkflowNodeInput }>
  | Readonly<{ type: "addConnectedNode"; input: AddConnectedWorkflowNodeInput }>
  | Readonly<{ type: "deleteNode"; nodeID: string }>
  | Readonly<{ type: "connectNodes"; input: ConnectWorkflowNodesInput }>
  | Readonly<{ type: "reconnectEdge"; input: ReconnectWorkflowEdgeInput }>
  | Readonly<{ type: "deleteEdge"; edgeID: string }>
  | Readonly<{ type: "editEdgeRoute"; input: EditWorkflowEdgeRouteInput }>
  | Readonly<{ type: "createNodeGroupFromNode"; input: CreateWorkflowNodeGroupInput }>
  | Readonly<{ type: "addNodeToGroup"; input: AddWorkflowNodeToGroupInput }>
  | Readonly<{ type: "deleteNodeGroup"; groupID: string }>
  | Readonly<{ type: "extractNodeFromGroup"; input: ExtractWorkflowNodeFromGroupInput }>
  | Readonly<{ type: "removeNodeFromGroup"; nodeID: string }>,
  { type: string }
>;

export function workflowEditorTopologyMutation(
  draft: DraftWorkflowDefinition,
  action: TopologyAction,
): Readonly<{ mutation: WorkflowEditorGraphMutationResult; graphChanged: boolean }> {
  if (action.type === "addConnectedNode") {
    return { graphChanged: true, mutation: addConnectedWorkflowNode(draft, action.input) };
  }
  return nonConnectedWorkflowEditorTopologyMutation(draft, action);
}

type NonConnectedTopologyAction = Exclude<TopologyAction, { type: "addConnectedNode" }>;

function nonConnectedWorkflowEditorTopologyMutation(
  draft: DraftWorkflowDefinition,
  action: NonConnectedTopologyAction,
): Readonly<{ mutation: WorkflowEditorGraphMutationResult; graphChanged: boolean }> {
  switch (action.type) {
    case "addNode":
      return { graphChanged: true, mutation: addWorkflowNode(draft, action.input) };
    case "deleteNode":
      return { graphChanged: true, mutation: deleteWorkflowNode(draft, action.nodeID) };
    case "connectNodes":
      return { graphChanged: true, mutation: connectWorkflowNodes(draft, action.input) };
    case "reconnectEdge":
      return { graphChanged: true, mutation: reconnectWorkflowEdge(draft, action.input) };
    case "deleteEdge":
      return { graphChanged: true, mutation: deleteWorkflowEdge(draft, action.edgeID) };
    case "editEdgeRoute":
      return { graphChanged: false, mutation: editWorkflowEdgeRoute(draft, action.input) };
    case "createNodeGroupFromNode":
      return { graphChanged: true, mutation: createWorkflowNodeGroupFromNode(draft, action.input) };
    case "addNodeToGroup":
      return { graphChanged: true, mutation: addWorkflowNodeToGroup(draft, action.input) };
    case "deleteNodeGroup":
      return { graphChanged: true, mutation: deleteWorkflowNodeGroup(draft, action.groupID) };
    case "extractNodeFromGroup":
      return { graphChanged: true, mutation: extractWorkflowNodeFromGroup(draft, action.input) };
    case "removeNodeFromGroup":
      return { graphChanged: true, mutation: removeWorkflowNodeFromGroup(draft, action.nodeID) };
  }
}
