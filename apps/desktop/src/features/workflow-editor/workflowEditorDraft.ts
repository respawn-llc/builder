import type {
  WorkflowDefinition,
  WorkflowEdge,
  WorkflowExecutionPolicyMode,
  WorkflowInputField,
  WorkflowNode,
  WorkflowParameter,
  WorkflowRecord,
} from "../../api";
import { z } from "zod";
import {
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
  type AddWorkflowNodeInput,
  type AddWorkflowNodeToGroupInput,
  type ConnectWorkflowNodesInput,
  type CreateWorkflowNodeGroupInput,
  type EditWorkflowEdgeRouteInput,
  type ExtractWorkflowNodeFromGroupInput,
  type ReconnectWorkflowEdgeInput,
  type WorkflowEditorCascadeSummary,
  type WorkflowEditorGraphMutationResult,
  type WorkflowEditorSelection,
} from "./workflowEditorGraphMutations";
import { workflowExecutionPolicy } from "./workflowExecutionPolicyDraft";

export {
  workflowDefinitionFromDraft,
  workflowEditorDirtyState,
  workflowEditorDraftGraph,
  workflowEditorDraftMetadata,
} from "./workflowEditorDraftProjection";

const workflowParameterRowIDSchema = z.string();

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

export type DraftWorkflowDefinition = Omit<WorkflowDefinition, "edges" | "nodes" | "workflow"> &
  Readonly<{
    edges: readonly DraftWorkflowEdge[];
    nodes: readonly DraftWorkflowNode[];
    executionPolicyCustomRef: string;
    workflow: WorkflowRecord;
  }>;

export type WorkflowEditorDraftState = Readonly<{
  acknowledgedConflictVersion: number;
  source: WorkflowDefinition;
  draft: DraftWorkflowDefinition;
  conflict: WorkflowDefinition | null;
  graphVersion: number;
  lastTopologyMutation: WorkflowEditorTopologyMutation | null;
  version: number;
}>;

export type WorkflowEditorTopologyMutation = Readonly<{
  summary: WorkflowEditorCascadeSummary;
  warnings: readonly string[];
  nextSelection: WorkflowEditorSelection;
}>;

export type WorkflowEditorDraftAction =
  | Readonly<{ type: "reset"; source: WorkflowDefinition }>
  | Readonly<{ type: "conflict"; source: WorkflowDefinition }>
  | Readonly<{ type: "keepEditing" }>
  | Readonly<{ type: "reloadConflict" }>
  | Readonly<{ type: "editWorkflowMetadata"; name: string; description: string }>
  | Readonly<{ type: "editWorkflowExecutionPolicyMode"; mode: WorkflowExecutionPolicyMode }>
  | Readonly<{ type: "editWorkflowExecutionPolicyCustomRef"; customRef: string }>
  | Readonly<{
      type: "editNodeIdentity";
      nodeID: string;
      patch: Partial<Pick<WorkflowNode, "key" | "name">>;
    }>
  | Readonly<{
      type: "editAgentNode";
      nodeID: string;
      patch: Partial<
        Pick<WorkflowNode, "key" | "name" | "subagentRole" | "promptTemplate" | "completionMode">
      >;
    }>
  | Readonly<{
      type: "editScriptNode";
      nodeID: string;
      patch: Partial<Pick<WorkflowNode, "key" | "name" | "scriptPath">>;
    }>
  | Readonly<{ type: "addInputField"; nodeID: string }>
  | Readonly<{
      type: "updateInputField";
      nodeID: string;
      rowID: string;
      patch: Partial<WorkflowInputField>;
    }>
  | Readonly<{ type: "deleteInputField"; nodeID: string; rowID: string }>
  | Readonly<{ type: "reorderInputField"; nodeID: string; activeRowID: string; overRowID: string }>
  | Readonly<{ type: "assignJoinInputProvider"; nodeID: string; inputName: string; providerEdgeID: string }>
  | Readonly<{ type: "editEdgePrompt"; edgeID: string; promptTemplate: string }>
  | Readonly<{ type: "addEdgeParameter"; edgeID: string }>
  | Readonly<{
      type: "updateEdgeParameter";
      edgeID: string;
      parameterRowID: string;
      patch: Partial<WorkflowParameter>;
    }>
  | Readonly<{ type: "deleteEdgeParameter"; edgeID: string; parameterRowID: string }>
  | Readonly<{ type: "reorderEdgeParameter"; edgeID: string; activeRowID: string; overRowID: string }>
  | Readonly<{ type: "addNode"; input: AddWorkflowNodeInput }>
  | Readonly<{ type: "deleteNode"; nodeID: string }>
  | Readonly<{ type: "connectNodes"; input: ConnectWorkflowNodesInput }>
  | Readonly<{ type: "reconnectEdge"; input: ReconnectWorkflowEdgeInput }>
  | Readonly<{ type: "deleteEdge"; edgeID: string }>
  | Readonly<{ type: "editEdgeRoute"; input: EditWorkflowEdgeRouteInput }>
  | Readonly<{ type: "createNodeGroupFromNode"; input: CreateWorkflowNodeGroupInput }>
  | Readonly<{ type: "addNodeToGroup"; input: AddWorkflowNodeToGroupInput }>
  | Readonly<{ type: "deleteNodeGroup"; groupID: string }>
  | Readonly<{ type: "extractNodeFromGroup"; input: ExtractWorkflowNodeFromGroupInput }>
  | Readonly<{ type: "removeNodeFromGroup"; nodeID: string }>;

export type WorkflowEditorDirtyState = Readonly<{
  dirty: boolean;
  graphDirty: boolean;
  metadataDirty: boolean;
}>;

export function initializeWorkflowEditorDraft(source: WorkflowDefinition): WorkflowEditorDraftState {
  return {
    acknowledgedConflictVersion: 0,
    conflict: null,
    draft: draftDefinitionFromSource(source),
    graphVersion: 0,
    lastTopologyMutation: null,
    source,
    version: 0,
  };
}

type LifecycleAction = Extract<
  WorkflowEditorDraftAction,
  {
    type:
      | "reset"
      | "conflict"
      | "keepEditing"
      | "reloadConflict"
      | "editWorkflowMetadata"
      | "editWorkflowExecutionPolicyMode"
      | "editWorkflowExecutionPolicyCustomRef";
  }
>;

type NodeFieldAction = Extract<
  WorkflowEditorDraftAction,
  {
    type:
      | "editNodeIdentity"
      | "editAgentNode"
      | "editScriptNode"
      | "addInputField"
      | "updateInputField"
      | "deleteInputField"
      | "reorderInputField"
      | "assignJoinInputProvider";
  }
>;

type EdgeFieldAction = Extract<
  WorkflowEditorDraftAction,
  {
    type:
      | "editEdgePrompt"
      | "addEdgeParameter"
      | "updateEdgeParameter"
      | "deleteEdgeParameter"
      | "reorderEdgeParameter";
  }
>;

type TopologyAction = Extract<
  WorkflowEditorDraftAction,
  {
    type:
      | "addNode"
      | "deleteNode"
      | "connectNodes"
      | "reconnectEdge"
      | "deleteEdge"
      | "editEdgeRoute"
      | "createNodeGroupFromNode"
      | "addNodeToGroup"
      | "deleteNodeGroup"
      | "extractNodeFromGroup"
      | "removeNodeFromGroup";
  }
>;

type DraftActionType = WorkflowEditorDraftAction["type"];

const lifecycleActionTypes: ReadonlySet<DraftActionType> = new Set<LifecycleAction["type"]>([
  "reset",
  "conflict",
  "keepEditing",
  "reloadConflict",
  "editWorkflowMetadata",
  "editWorkflowExecutionPolicyMode",
  "editWorkflowExecutionPolicyCustomRef",
]);

const nodeFieldActionTypes: ReadonlySet<DraftActionType> = new Set<NodeFieldAction["type"]>([
  "editNodeIdentity",
  "editAgentNode",
  "editScriptNode",
  "addInputField",
  "updateInputField",
  "deleteInputField",
  "reorderInputField",
  "assignJoinInputProvider",
]);

const edgeFieldActionTypes: ReadonlySet<DraftActionType> = new Set<EdgeFieldAction["type"]>([
  "editEdgePrompt",
  "addEdgeParameter",
  "updateEdgeParameter",
  "deleteEdgeParameter",
  "reorderEdgeParameter",
]);

function isLifecycleAction(action: WorkflowEditorDraftAction): action is LifecycleAction {
  return lifecycleActionTypes.has(action.type);
}

function isNodeFieldAction(action: WorkflowEditorDraftAction): action is NodeFieldAction {
  return nodeFieldActionTypes.has(action.type);
}

function isEdgeFieldAction(action: WorkflowEditorDraftAction): action is EdgeFieldAction {
  return edgeFieldActionTypes.has(action.type);
}

export function workflowEditorDraftReducer(
  state: WorkflowEditorDraftState,
  action: WorkflowEditorDraftAction,
): WorkflowEditorDraftState {
  if (isLifecycleAction(action)) {
    return reduceLifecycleAction(state, action);
  }
  if (isNodeFieldAction(action)) {
    return reduceNodeFieldAction(state, action);
  }
  if (isEdgeFieldAction(action)) {
    return reduceEdgeFieldAction(state, action);
  }
  return reduceTopologyAction(state, action);
}

function reduceLifecycleAction(
  state: WorkflowEditorDraftState,
  action: LifecycleAction,
): WorkflowEditorDraftState {
  switch (action.type) {
    case "reset":
      return initializeWorkflowEditorDraft(action.source);
    case "conflict":
      return { ...state, conflict: action.source };
    case "keepEditing":
      return {
        ...state,
        acknowledgedConflictVersion: state.conflict?.workflow.version ?? 0,
        conflict: null,
      };
    case "reloadConflict":
      return state.conflict === null ? state : initializeWorkflowEditorDraft(state.conflict);
    case "editWorkflowMetadata":
      return nextDraftState(
        state,
        {
          ...state.draft,
          workflow: { ...state.draft.workflow, name: action.name, description: action.description },
        },
        false,
      );
    case "editWorkflowExecutionPolicyMode":
      return nextDraftState(
        state,
        {
          ...state.draft,
          workflow: {
            ...state.draft.workflow,
            executionPolicy: workflowExecutionPolicy(action.mode, state.draft.executionPolicyCustomRef),
          },
        },
        false,
      );
    case "editWorkflowExecutionPolicyCustomRef":
      return nextDraftState(state, { ...state.draft, executionPolicyCustomRef: action.customRef }, false);
  }
}

function reduceNodeFieldAction(
  state: WorkflowEditorDraftState,
  action: NodeFieldAction,
): WorkflowEditorDraftState {
  switch (action.type) {
    case "editNodeIdentity":
      return editDraftNode(state, action.nodeID, false, (node) => {
        if (
          node.kind !== "start" &&
          node.kind !== "terminal" &&
          node.kind !== "agent" &&
          node.kind !== "script"
        ) {
          return node;
        }
        return { ...node, ...action.patch };
      });
    case "editAgentNode":
      return editDraftNode(state, action.nodeID, false, (node) => {
        if (node.kind !== "agent") {
          return node;
        }
        return {
          ...node,
          ...action.patch,
          completionMode: action.patch.completionMode ?? node.completionMode,
        };
      });
    case "editScriptNode":
      return editDraftNode(state, action.nodeID, false, (node) => {
        if (node.kind !== "script") {
          return node;
        }
        return {
          ...node,
          ...action.patch,
          scriptPath: action.patch.scriptPath === undefined ? node.scriptPath : action.patch.scriptPath,
        };
      });
    case "addInputField":
      return editDraftNode(state, action.nodeID, false, (node) => ({
        ...node,
        inputFields: [
          {
            description: "",
            name: "",
            rowID: [node.id, "input", state.version.toString(), node.inputFields.length.toString()].join(":"),
          },
          ...node.inputFields,
        ],
      }));
    case "updateInputField":
      return editDraftNode(state, action.nodeID, false, (node) => ({
        ...node,
        inputFields: node.inputFields.map((field) =>
          field.rowID === action.rowID ? { ...field, ...action.patch } : field,
        ),
      }));
    case "deleteInputField":
      return editDraftNode(state, action.nodeID, false, (node) => ({
        ...node,
        inputFields: node.inputFields.filter((field) => field.rowID !== action.rowID),
      }));
    case "reorderInputField":
      return editDraftNode(state, action.nodeID, false, (node) => ({
        ...node,
        inputFields: reorderRow(node.inputFields, action.activeRowID, action.overRowID),
      }));
    case "assignJoinInputProvider":
      return editDraftNode(state, action.nodeID, false, (node) => ({
        ...node,
        joinInputProviders: assignJoinInputProvider(
          node.joinInputProviders,
          action.inputName,
          action.providerEdgeID,
        ),
      }));
  }
}

function reduceEdgeFieldAction(
  state: WorkflowEditorDraftState,
  action: EdgeFieldAction,
): WorkflowEditorDraftState {
  switch (action.type) {
    case "editEdgePrompt":
      return editDraftEdge(state, action.edgeID, false, (edge) => ({
        ...edge,
        promptTemplate: action.promptTemplate,
      }));
    case "addEdgeParameter":
      return editDraftEdge(state, action.edgeID, false, (edge) => ({
        ...edge,
        parameters: [
          {
            description: "",
            key: "",
            rowID: [edge.id, "parameter", state.version.toString(), edge.parameters.length.toString()].join(
              ":",
            ),
          },
          ...edge.parameters,
        ],
      }));
    case "updateEdgeParameter":
      return editDraftEdge(state, action.edgeID, false, (edge) => ({
        ...edge,
        parameters: edge.parameters.map((parameter) =>
          parameter.rowID === action.parameterRowID ? { ...parameter, ...action.patch } : parameter,
        ),
      }));
    case "deleteEdgeParameter":
      return editDraftEdge(state, action.edgeID, false, (edge) => ({
        ...edge,
        parameters: edge.parameters.filter((parameter) => parameter.rowID !== action.parameterRowID),
      }));
    case "reorderEdgeParameter":
      return editDraftEdge(state, action.edgeID, false, (edge) => ({
        ...edge,
        parameters: reorderParameterRows(edge.parameters, action.activeRowID, action.overRowID),
      }));
  }
}

function reduceTopologyAction(
  state: WorkflowEditorDraftState,
  action: TopologyAction,
): WorkflowEditorDraftState {
  switch (action.type) {
    case "addNode":
      return applyTopologyMutation(state, addWorkflowNode(state.draft, action.input));
    case "deleteNode":
      return applyTopologyMutation(state, deleteWorkflowNode(state.draft, action.nodeID));
    case "connectNodes":
      return applyTopologyMutation(state, connectWorkflowNodes(state.draft, action.input));
    case "reconnectEdge":
      return applyTopologyMutation(state, reconnectWorkflowEdge(state.draft, action.input));
    case "deleteEdge":
      return applyTopologyMutation(state, deleteWorkflowEdge(state.draft, action.edgeID));
    case "editEdgeRoute":
      return applyTopologyMutation(state, editWorkflowEdgeRoute(state.draft, action.input), false);
    case "createNodeGroupFromNode":
      return applyTopologyMutation(state, createWorkflowNodeGroupFromNode(state.draft, action.input));
    case "addNodeToGroup":
      return applyTopologyMutation(state, addWorkflowNodeToGroup(state.draft, action.input));
    case "deleteNodeGroup":
      return applyTopologyMutation(state, deleteWorkflowNodeGroup(state.draft, action.groupID));
    case "extractNodeFromGroup":
      return applyTopologyMutation(state, extractWorkflowNodeFromGroup(state.draft, action.input));
    case "removeNodeFromGroup":
      return applyTopologyMutation(state, removeWorkflowNodeFromGroup(state.draft, action.nodeID));
  }
}

export function draftDefinitionFromSource(source: WorkflowDefinition): DraftWorkflowDefinition {
  const executionPolicy = source.workflow.executionPolicy;
  return {
    ...source,
    workflow: { ...source.workflow, executionPolicy },
    executionPolicyCustomRef: executionPolicy.customRef ?? "",
    edges: source.edges.map(draftEdgeWithParameterRowIDs),
    nodes: source.nodes.map((node) => ({
      ...node,
      completionMode: node.completionMode ?? "",
      inputFields: node.inputFields.map((field, index) => ({
        ...field,
        rowID: [node.id, "input", index.toString()].join(":"),
      })),
    })),
  };
}

function nextDraftState(
  state: WorkflowEditorDraftState,
  draft: DraftWorkflowDefinition,
  graphChanged = true,
  lastTopologyMutation: WorkflowEditorTopologyMutation | null = null,
): WorkflowEditorDraftState {
  return {
    ...state,
    draft,
    graphVersion: graphChanged ? state.graphVersion + 1 : state.graphVersion,
    lastTopologyMutation,
    version: state.version + 1,
  };
}

function applyTopologyMutation(
  state: WorkflowEditorDraftState,
  mutation: WorkflowEditorGraphMutationResult,
  graphChanged = true,
): WorkflowEditorDraftState {
  const lastTopologyMutation = {
    nextSelection: mutation.nextSelection,
    summary: mutation.summary,
    warnings: mutation.warnings,
  };
  if (mutation.draft === state.draft) {
    return { ...state, lastTopologyMutation };
  }
  return nextDraftState(state, draftDefinitionFromSource(mutation.draft), graphChanged, {
    ...lastTopologyMutation,
  });
}

function editDraftNode(
  state: WorkflowEditorDraftState,
  nodeID: string,
  graphChanged: boolean,
  edit: (node: DraftWorkflowNode, nodes: readonly DraftWorkflowNode[]) => DraftWorkflowNode,
): WorkflowEditorDraftState {
  let nextEdges = state.draft.edges;
  const nodes = state.draft.nodes.map((node) => {
    if (node.id !== nodeID) {
      return node;
    }
    const edited = edit(node, state.draft.nodes);
    if (edited.key !== node.key) {
      nextEdges = selectedNodeCascadeEdges({
        edges: nextEdges,
        newKey: edited.key,
        nodeID: node.id,
        nodes: state.draft.nodes,
        oldKey: node.key,
      });
    }
    return edited;
  });
  return nextDraftState(state, { ...state.draft, edges: nextEdges, nodes }, graphChanged);
}

function editDraftEdge(
  state: WorkflowEditorDraftState,
  edgeID: string,
  graphChanged: boolean,
  edit: (edge: DraftWorkflowEdge, edges: readonly DraftWorkflowEdge[]) => DraftWorkflowEdge,
): WorkflowEditorDraftState {
  const edgeIndex = state.draft.edges.findIndex((edge) => edge.id === edgeID);
  if (edgeIndex < 0) {
    return state;
  }
  const edges = state.draft.edges.map((edge, index) =>
    index === edgeIndex ? draftEdgeWithParameterRowIDs(edit(edge, state.draft.edges)) : edge,
  );
  return nextDraftState(state, { ...state.draft, edges }, graphChanged);
}

function draftEdgeWithParameterRowIDs(edge: WorkflowEdge): DraftWorkflowEdge {
  return {
    ...edge,
    parameters: edge.parameters.map((parameter, index) => ({
      ...parameter,
      rowID: draftParameterRowID(parameter) ?? [edge.id, "parameter", index.toString()].join(":"),
    })),
  };
}

function draftParameterRowID(parameter: WorkflowParameter): string | undefined {
  if (!("rowID" in parameter)) {
    return undefined;
  }
  const rowID = workflowParameterRowIDSchema.safeParse(parameter.rowID);
  return rowID.success ? rowID.data : undefined;
}

type SelectedNodeCascadeRequest = Readonly<{
  edges: readonly DraftWorkflowEdge[];
  nodeID: string;
  oldKey: string;
  newKey: string;
  nodes: readonly DraftWorkflowNode[];
}>;

function selectedNodeCascadeEdges(req: SelectedNodeCascadeRequest): readonly DraftWorkflowEdge[] {
  const { edges, nodeID, oldKey, newKey, nodes } = req;
  const oldKeyOwners = nodes.filter((item) => item.key === oldKey);
  const oldKeyOwner = oldKeyOwners.at(0);
  if (oldKeyOwners.length !== 1 || oldKeyOwner?.id !== nodeID) {
    return edges;
  }
  return edges.map((edge) =>
    edge.contextSource.kind === "selected_node" && edge.contextSource.nodeKey === oldKey
      ? { ...edge, contextSource: { ...edge.contextSource, nodeKey: newKey } }
      : edge,
  );
}

function reorderRow<T extends Readonly<{ rowID: string }>>(
  rows: readonly T[],
  activeRowID: string,
  overRowID: string,
): readonly T[] {
  const activeIndex = rows.findIndex((row) => row.rowID === activeRowID);
  const overIndex = rows.findIndex((row) => row.rowID === overRowID);
  if (activeIndex < 0 || overIndex < 0 || activeIndex === overIndex) {
    return rows;
  }
  const next = [...rows];
  const [item] = next.splice(activeIndex, 1);
  if (item === undefined) {
    return rows;
  }
  next.splice(overIndex, 0, item);
  return next;
}

function reorderParameterRows(
  rows: readonly DraftWorkflowParameter[],
  activeRowID: string,
  overRowID: string,
): readonly DraftWorkflowParameter[] {
  const activeIndex = rows.findIndex((row) => row.rowID === activeRowID);
  const overIndex = rows.findIndex((row) => row.rowID === overRowID);
  if (activeIndex < 0 || overIndex < 0 || activeIndex === overIndex) {
    return rows;
  }
  const next = [...rows];
  const [item] = next.splice(activeIndex, 1);
  if (item === undefined) {
    return rows;
  }
  next.splice(overIndex, 0, item);
  return next;
}

function assignJoinInputProvider(
  providers: WorkflowDefinition["nodes"][number]["joinInputProviders"],
  inputName: string,
  providerEdgeID: string,
): WorkflowDefinition["nodes"][number]["joinInputProviders"] {
  const updated = { inputName, providerEdgeID };
  const providerIndex = providers.findIndex((provider) => provider.inputName === inputName);
  if (providerIndex === -1) {
    return [...providers, updated];
  }
  return providers.map((provider, index) => (index === providerIndex ? updated : provider));
}
