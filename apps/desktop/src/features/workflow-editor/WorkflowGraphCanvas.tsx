import {
  applyNodeChanges,
  Background,
  BackgroundVariant,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type EdgeProps,
  type Node,
  type NodeProps,
  type NodeTypes,
} from "@xyflow/react";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { TooltipProvider } from "../../ui";
import {
  connectWorkflowGraphNodes,
  groupIDFromPoint,
  inspectEdge,
  inspectNode,
  isFormTarget,
  reconnectWorkflowGraphEdge,
  selectionFromEdge,
  selectionFromNode,
  type WorkflowGraphReconnectEdgeInput,
  type WorkflowGraphReconnectEndpoint,
  workflowGraphSelectionExists,
} from "./workflowGraphCanvasInteractions";
import { WorkflowGraphEdge as WorkflowGraphEdgeRenderer } from "./WorkflowGraphEdge";
import {
  WorkflowGroupNode,
  WorkflowJoinNode,
  WorkflowNode,
} from "./WorkflowGraphNodes";
import { WorkflowGroupDragPreview, type WorkflowGroupDragState } from "./WorkflowGroupDragPreview";
import type { CopyText } from "./WorkflowGraphNodeMetadata";
import { WorkflowGraphToolbar } from "./WorkflowGraphToolbar";
import { workflowGraphRenderEdges, workflowGraphRenderNodes } from "./workflowGraphRenderLayers";
import type { WorkflowGraphSelection } from "./workflowGraphSelection";
import type { CreatableWorkflowNodeKind } from "./workflowEditorGraphMutationTypes";
import type { WorkflowNodeKindSelectionModality } from "./WorkflowNodeKindPicker";
import type {
  WorkflowGraphEdge,
  WorkflowGraphGroupNode,
  WorkflowGraphLayout,
  WorkflowGraphNode,
  WorkflowGraphWorkflowNode,
} from "./workflowGraphLayout";
import "@xyflow/react/dist/style.css";
import "./workflow-editor.css";

export { WorkflowNodeInfoTooltipContent } from "./WorkflowGraphNodeMetadata";

export type WorkflowGraphCanvasProps = Readonly<{
  graph: WorkflowGraphLayout;
  keyboardScope?: "focused" | "global" | undefined;
  toolbarPositionStrategy?: "absolute" | "fixed" | undefined;
  onCopyText?: ((value: string) => Promise<void> | void) | undefined;
  onAddNode?: ((kind: CreatableWorkflowNodeKind) => void) | undefined;
  onAddConnectedNode?:
    | ((sourceNodeID: string, kind: CreatableWorkflowNodeKind, modality: WorkflowNodeKindSelectionModality) => void)
    | undefined;
  onAddNodeToGroup?: ((nodeID: string, groupID: string) => void) | undefined;
  onConnectNodes?: ((sourceNodeID: string, targetNodeID: string) => void) | undefined;
  onCreateNodeGroup?: ((nodeID: string) => void) | undefined;
  onDeleteSelection?: ((selection: WorkflowGraphSelection) => void) | undefined;
  onExtractNodeFromGroup?: ((nodeID: string) => void) | undefined;
  onReconnectEdge?: ((input: WorkflowGraphReconnectEdgeInput) => void) | undefined;
  onRemoveNodeFromGroup?: ((nodeID: string) => void) | undefined;
  onEdgeInspect: (edgeID: string) => void;
  onGroupInspect: (groupID: string) => void;
  onNodeInspect: (nodeID: string) => void;
  onWorkflowInspect: () => void;
  graphSelectionRequest?: Readonly<{ requestID: string; edgeID: string }> | null | undefined;
  onGraphSelectionConsumed?: ((requestID: string) => void) | undefined;
}>;

type RenderNodesState = Readonly<{
  nodes: Node[];
  sourceNodes: readonly WorkflowGraphNode[];
}>;

type WorkflowGraphReconnectEndpointState = Readonly<{
  edgeID: string;
  endpoint: WorkflowGraphReconnectEndpoint;
}>;

type ConnectionGesturePoint = Readonly<{ x: number; y: number }>;

const connectionDragThreshold = 6;

export function WorkflowGraphCanvas({
  graph,
  keyboardScope = "global",
  toolbarPositionStrategy = "fixed",
  onCopyText = copyTextWithNavigator,
  onAddNode,
  onAddConnectedNode,
  onAddNodeToGroup,
  onConnectNodes,
  onCreateNodeGroup,
  onDeleteSelection,
  onExtractNodeFromGroup,
  onReconnectEdge,
  onRemoveNodeFromGroup,
  onEdgeInspect,
  onGroupInspect,
  onNodeInspect,
  onWorkflowInspect,
  graphSelectionRequest = null,
  onGraphSelectionConsumed,
}: WorkflowGraphCanvasProps) {
  return (
    <TooltipProvider delayDuration={0}>
      <ReactFlowProvider>
        <WorkflowGraphCanvasInner
          edges={graph.edges}
          onAddNode={onAddNode}
          onAddConnectedNode={onAddConnectedNode}
          onAddNodeToGroup={onAddNodeToGroup}
          onConnectNodes={onConnectNodes}
          onCreateNodeGroup={onCreateNodeGroup}
          onDeleteSelection={onDeleteSelection}
          onExtractNodeFromGroup={onExtractNodeFromGroup}
          onReconnectEdge={onReconnectEdge}
          onRemoveNodeFromGroup={onRemoveNodeFromGroup}
          onEdgeInspect={onEdgeInspect}
          onGroupInspect={onGroupInspect}
          keyboardScope={keyboardScope}
          onNodeInspect={onNodeInspect}
          onWorkflowInspect={onWorkflowInspect}
          graphSelectionRequest={graphSelectionRequest}
          onGraphSelectionConsumed={onGraphSelectionConsumed}
          onCopyText={onCopyText}
          nodes={graph.nodes}
          toolbarPositionStrategy={toolbarPositionStrategy}
        />
      </ReactFlowProvider>
    </TooltipProvider>
  );
}

function WorkflowGraphCanvasInner({
  edges,
  keyboardScope,
  onAddNode,
  onAddConnectedNode,
  onAddNodeToGroup,
  onConnectNodes,
  onCopyText,
  onCreateNodeGroup,
  onDeleteSelection,
  onEdgeInspect,
  onExtractNodeFromGroup,
  onGroupInspect,
  onNodeInspect,
  onReconnectEdge,
  onRemoveNodeFromGroup,
  onWorkflowInspect,
  graphSelectionRequest,
  onGraphSelectionConsumed,
  nodes,
  toolbarPositionStrategy,
}: Readonly<{
  edges: readonly WorkflowGraphEdge[];
  keyboardScope: "focused" | "global";
  onAddNode: ((kind: CreatableWorkflowNodeKind) => void) | undefined;
  onAddConnectedNode:
    | ((sourceNodeID: string, kind: CreatableWorkflowNodeKind, modality: WorkflowNodeKindSelectionModality) => void)
    | undefined;
  onAddNodeToGroup: ((nodeID: string, groupID: string) => void) | undefined;
  onConnectNodes: ((sourceNodeID: string, targetNodeID: string) => void) | undefined;
  onCopyText: CopyText;
  onCreateNodeGroup: ((nodeID: string) => void) | undefined;
  onDeleteSelection: ((selection: WorkflowGraphSelection) => void) | undefined;
  onEdgeInspect: (edgeID: string) => void;
  onExtractNodeFromGroup: ((nodeID: string) => void) | undefined;
  onGroupInspect: (groupID: string) => void;
  onNodeInspect: (nodeID: string) => void;
  onReconnectEdge: ((input: WorkflowGraphReconnectEdgeInput) => void) | undefined;
  onRemoveNodeFromGroup: ((nodeID: string) => void) | undefined;
  onWorkflowInspect: () => void;
  graphSelectionRequest: Readonly<{ requestID: string; edgeID: string }> | null;
  onGraphSelectionConsumed: ((requestID: string) => void) | undefined;
  nodes: readonly WorkflowGraphNode[];
  toolbarPositionStrategy: "absolute" | "fixed";
}>) {
  const instance = useReactFlow();
  const rootRef = useRef<HTMLDivElement | null>(null);
  const reconnectEndpointRef = useRef<WorkflowGraphReconnectEndpointState | null>(null);
  const connectionGestureRef = useRef<Readonly<{ nodeID: string; start: ConnectionGesturePoint }> | null>(null);
  const suppressedCreationHandleRef = useRef<string | null>(null);
  const consumedGraphSelectionRequestIDRef = useRef<string | null>(null);
  const graphSelectionRequestRef = useRef(graphSelectionRequest);
  useLayoutEffect(() => {
    graphSelectionRequestRef.current = graphSelectionRequest;
  }, [graphSelectionRequest]);
  const [selection, setSelection] = useState<WorkflowGraphSelection | null>(null);
  // React Flow owns the drag gesture, but workflow layout stays ELK/server-authored.
  // This transient snapshot lets cards move during drag without persisting canvas positions.
  const [renderNodesState, setRenderNodesState] = useState<RenderNodesState>(() => ({
    nodes: workflowGraphRenderNodes(nodes),
    sourceNodes: nodes,
  }));
  const [groupDrag, setGroupDrag] = useState<WorkflowGroupDragState | null>(null);
  const renderNodes =
    renderNodesState.sourceNodes === nodes ? renderNodesState.nodes : workflowGraphRenderNodes(nodes);
  const dragAwareRenderNodes = useMemo(
    () => relaxActiveGroupedNodeClamp(renderNodes, groupDrag?.nodeID ?? null),
    [groupDrag?.nodeID, renderNodes],
  );
  const selectedRenderNodes = useMemo(
    () => dragAwareRenderNodes.map((node) => ({
      ...node,
      selected:
        selection?.kind === "node" && node.data.entityKind === "node"
          ? selection.nodeID === node.data.entityID
          : false,
    })),
    [dragAwareRenderNodes, selection],
  );
  const renderEdges = useMemo(() => workflowGraphRenderEdges(edges, selection), [edges, selection]);
  const consumeCreationHandleActivation = (nodeID: string): boolean => {
    if (suppressedCreationHandleRef.current !== nodeID) {
      return true;
    }
    suppressedCreationHandleRef.current = null;
    return false;
  };
  const suppressCreationHandleActivation = (nodeID: string | null): void => {
    if (nodeID === null) {
      return;
    }
    suppressedCreationHandleRef.current = nodeID;
    window.requestAnimationFrame(() => {
      if (suppressedCreationHandleRef.current === nodeID) {
        suppressedCreationHandleRef.current = null;
      }
    });
  };
  const edgeTypes = useMemo(
    () => ({
      workflow: (props: EdgeProps<WorkflowGraphEdge>) => (
        <WorkflowGraphEdgeRenderer
          {...props}
          onDeleteSelection={onDeleteSelection}
          onInspect={(edgeID) => {
            setSelection({ edgeID, kind: "edge" });
            onEdgeInspect(edgeID);
          }}
          onSelectContextMenu={(edgeID) => {
            setSelection({ edgeID, kind: "edge" });
          }}
        />
      ),
    }),
    [onDeleteSelection, onEdgeInspect],
  );
  const nodeTypes = useMemo(
    () => ({
      workflowGroup: (props: NodeProps<WorkflowGraphGroupNode>) => (
        <WorkflowGroupNode {...props} activeDropTarget={groupDrag?.targetGroupID === props.data.entityID} />
      ),
      workflowJoin: (props: NodeProps<WorkflowGraphWorkflowNode>) => (
        <WorkflowJoinNode
          {...props}
          onCopyText={onCopyText}
          onAddConnectedNode={onAddConnectedNode}
          onCreationHandleActivate={consumeCreationHandleActivation}
          onDeleteSelection={onDeleteSelection}
          onInspectNode={(nodeID) => {
            setSelection({ kind: "node", nodeID });
            onNodeInspect(nodeID);
          }}
          onSelectContextMenu={(nodeID) => {
            setSelection({ kind: "node", nodeID });
          }}
        />
      ),
      workflowNode: (props: NodeProps<WorkflowGraphWorkflowNode>) => (
        <WorkflowNode
          {...props}
          onCopyText={onCopyText}
          onAddConnectedNode={onAddConnectedNode}
          onCreationHandleActivate={consumeCreationHandleActivation}
          onCreateNodeGroup={onCreateNodeGroup}
          onDeleteSelection={onDeleteSelection}
          onInspectNode={(nodeID) => {
            setSelection({ kind: "node", nodeID });
            onNodeInspect(nodeID);
          }}
          onRemoveNodeFromGroup={onRemoveNodeFromGroup}
          onSelectContextMenu={(nodeID) => {
            setSelection({ kind: "node", nodeID });
          }}
        />
      ),
    }) satisfies NodeTypes,
    [
      groupDrag?.targetGroupID,
      onAddConnectedNode,
      onCopyText,
      onCreateNodeGroup,
      onDeleteSelection,
      onNodeInspect,
      onRemoveNodeFromGroup,
    ],
  );
  useEffect(() => {
    if (graphSelectionRequest === null) {
      consumedGraphSelectionRequestIDRef.current = null;
      return;
    }
    if (consumedGraphSelectionRequestIDRef.current === graphSelectionRequest.requestID) {
      return;
    }
    if (!edges.some((edge) => edge.data?.entityID === graphSelectionRequest.edgeID)) {
      return;
    }
    queueMicrotask(() => {
      const currentRequest = graphSelectionRequestRef.current;
      if (
        currentRequest?.requestID !== graphSelectionRequest.requestID ||
        currentRequest.edgeID !== graphSelectionRequest.edgeID
      ) {
        return;
      }
      consumedGraphSelectionRequestIDRef.current = currentRequest.requestID;
      setSelection({ edgeID: currentRequest.edgeID, kind: "edge" });
      onGraphSelectionConsumed?.(currentRequest.requestID);
    });
  }, [edges, graphSelectionRequest, onGraphSelectionConsumed]);
  useEffect(() => {
    if (selection !== null && !workflowGraphSelectionExists(selection, nodes, edges)) {
      queueMicrotask(() => {
        setSelection(null);
      });
    }
  }, [edges, nodes, selection]);
  const didFitInitialView = useRef(false);
  useEffect(() => {
    if (didFitInitialView.current) {
      return;
    }
    didFitInitialView.current = true;
    window.requestAnimationFrame(() => {
      void instance.fitView({ padding: 0.18 });
    });
  }, [instance]);
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent): void {
      const activeSelection =
        selection === null || !workflowGraphSelectionExists(selection, nodes, edges) ? null : selection;
      if (
        event.defaultPrevented ||
        isFormTarget(event.target) ||
        !shouldHandleWorkflowGraphShortcut(keyboardScope, rootRef.current)
      ) {
        return;
      }
      if (applyViewportShortcut(event.key, instance)) {
        event.preventDefault();
        return;
      }
      if ((event.key === "Delete" || event.key === "Backspace") && activeSelection !== null) {
        event.preventDefault();
        onDeleteSelection?.(activeSelection);
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [edges, instance, keyboardScope, nodes, onDeleteSelection, selection]);
  return (
    <div
      className="workflow-editor-canvas h-full min-h-0 w-full"
      data-testid="workflow-editor-canvas"
      onPointerDown={(event) => {
        if (!isFormTarget(event.target)) {
          event.currentTarget.focus({ preventScroll: true });
        }
      }}
      ref={rootRef}
      tabIndex={-1}
    >
      <ReactFlow
        colorMode="system"
        edges={renderEdges}
        edgeTypes={edgeTypes}
        fitView
        maxZoom={2}
        minZoom={0.15}
        nodeDragThreshold={6}
        connectionDragThreshold={connectionDragThreshold}
        connectOnClick={false}
        nodes={selectedRenderNodes}
        nodesConnectable={onConnectNodes !== undefined || onReconnectEdge !== undefined}
        nodesDraggable={false}
        nodeTypes={nodeTypes}
        onConnect={(connection) => {
          suppressCreationHandleActivation(connection.source);
          connectWorkflowGraphNodes(connection, onConnectNodes);
        }}
        onConnectEnd={(event) => {
          const gesture = connectionGestureRef.current;
          if (gesture !== null && connectionDragDistance(event, gesture) >= connectionDragThreshold) {
            suppressCreationHandleActivation(gesture.nodeID);
          }
          connectionGestureRef.current = null;
        }}
        onConnectStart={(event, params) => {
          const start = connectionGesturePoint(event);
          connectionGestureRef.current =
            params.handleType === "source" && params.nodeId !== null && start !== null
              ? { nodeID: params.nodeId, start }
              : null;
        }}
        onEdgeClick={(_event, edge) => {
          setSelection(selectionFromEdge(edge));
          inspectEdge(edge, onEdgeInspect);
        }}
        onNodeClick={(_event, node) => {
          setSelection(selectionFromNode(node));
          inspectNode(node, onGroupInspect, onNodeInspect);
        }}
        onPaneClick={() => {
          setSelection(null);
        }}
        onNodeDrag={(event, node) => {
          if (!isWorkflowAgentGraphNode(node)) {
            return;
          }
          setGroupDrag({
            label: node.data.label,
            nodeID: node.data.entityID,
            targetGroupID: groupIDFromPoint(event.clientX, event.clientY),
            x: event.clientX,
            y: event.clientY,
          });
        }}
        onNodeDragStart={(event, node) => {
          if (!isWorkflowAgentGraphNode(node)) {
            return;
          }
          setGroupDrag({
            label: node.data.label,
            nodeID: node.data.entityID,
            targetGroupID: null,
            x: event.clientX,
            y: event.clientY,
          });
        }}
        onNodeDragStop={(event, node) => {
          setGroupDrag(null);
          setRenderNodesState({ nodes: workflowGraphRenderNodes(nodes), sourceNodes: nodes });
          if (!isWorkflowAgentGraphNode(node)) {
            return;
          }
          const groupID = groupIDFromPoint(event.clientX, event.clientY);
          if (groupID !== null && groupID !== node.data.groupID) {
            onAddNodeToGroup?.(node.data.entityID, groupID);
            return;
          }
          if (groupID === null && node.data.groupID.length > 0) {
            onExtractNodeFromGroup?.(node.data.entityID);
          }
        }}
        onNodesChange={(changes) => {
          setRenderNodesState((current) => {
            const currentNodes =
              current.sourceNodes === nodes ? current.nodes : workflowGraphRenderNodes(nodes);
            return { nodes: applyNodeChanges(changes, currentNodes), sourceNodes: nodes };
          });
        }}
        onReconnect={(edge, connection) => {
          const reconnectEndpoint = reconnectEndpointRef.current;
          const endpoint =
            reconnectEndpoint !== null && reconnectEndpoint.edgeID === edge.id
              ? reconnectEndpoint.endpoint
              : null;
          reconnectWorkflowGraphEdge(
            edge,
            connection,
            endpoint,
            onReconnectEdge,
          );
        }}
        onReconnectEnd={() => {
          reconnectEndpointRef.current = null;
        }}
        onReconnectStart={(_event, edge, handleType) => {
          reconnectEndpointRef.current = { edgeID: edge.id, endpoint: handleType };
        }}
        panOnScroll
        proOptions={{ hideAttribution: true }}
        reconnectRadius={24}
        selectionOnDrag={false}
        edgesReconnectable={onReconnectEdge !== undefined}
        zoomOnDoubleClick={false}
      >
        <Background
          bgColor="transparent"
          color="var(--color-outline)"
          gap={24}
          size={1}
          variant={BackgroundVariant.Dots}
        />
        <WorkflowGraphToolbar
          onAddNode={onAddNode}
          onWorkflowInspect={onWorkflowInspect}
          positionStrategy={toolbarPositionStrategy}
        />
        {groupDrag === null ? null : <WorkflowGroupDragPreview drag={groupDrag} />}
      </ReactFlow>
    </div>
  );
}

function connectionDragDistance(event: unknown, gesture: Readonly<{ start: ConnectionGesturePoint }>): number {
  const end = connectionGesturePoint(event);
  if (end === null) {
    return 0;
  }
  return Math.hypot(end.x - gesture.start.x, end.y - gesture.start.y);
}

function connectionGesturePoint(event: unknown): ConnectionGesturePoint | null {
  if (event instanceof MouseEvent) {
    return { x: event.clientX, y: event.clientY };
  }
  if (event instanceof TouchEvent) {
    const touch = event.changedTouches.item(0) ?? event.touches.item(0);
    return touch === null ? null : { x: touch.clientX, y: touch.clientY };
  }
  return null;
}

function copyTextWithNavigator(value: string): void {
  void navigator.clipboard.writeText(value).catch(() => undefined);
}

function applyViewportShortcut(key: string, instance: ReturnType<typeof useReactFlow>): boolean {
  if (key === "+") {
    void instance.zoomIn();
    return true;
  }
  if (key === "-") {
    void instance.zoomOut();
    return true;
  }
  if (key === "0") {
    void instance.setViewport({ x: 0, y: 0, zoom: 1 });
    return true;
  }
  if (key.toLowerCase() === "f") {
    void instance.fitView({ padding: 0.18 });
    return true;
  }
  return false;
}

function shouldHandleWorkflowGraphShortcut(
  keyboardScope: "focused" | "global",
  root: HTMLElement | null,
): boolean {
  if (keyboardScope === "global") {
    return true;
  }
  return root?.contains(document.activeElement) === true;
}

function isWorkflowAgentGraphNode(node: Node): node is WorkflowGraphWorkflowNode {
  return node.data.entityKind === "node" && (node.data.kind === "agent" || node.data.kind === "script");
}

function relaxActiveGroupedNodeClamp(nodes: Node[], activeNodeID: string | null): Node[] {
  if (activeNodeID === null) {
    return nodes;
  }
  return nodes.map((node) => {
    if (node.id !== activeNodeID || !isWorkflowAgentGraphNode(node) || node.parentId === undefined) {
      return node;
    }
    const { extent, ...unclamped } = node;
    return extent === undefined ? node : unclamped;
  });
}
