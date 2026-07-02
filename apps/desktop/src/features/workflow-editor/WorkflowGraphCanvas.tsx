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
import { useEffect, useMemo, useRef, useState } from "react";

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
  onAddNode?: ((kind: "agent" | "script" | "terminal") => void) | undefined;
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
}>;

type RenderNodesState = Readonly<{
  nodes: Node[];
  sourceNodes: readonly WorkflowGraphNode[];
}>;

type WorkflowGraphReconnectEndpointState = Readonly<{
  edgeID: string;
  endpoint: WorkflowGraphReconnectEndpoint;
}>;

export function WorkflowGraphCanvas({
  graph,
  keyboardScope = "global",
  toolbarPositionStrategy = "fixed",
  onCopyText = copyTextWithNavigator,
  onAddNode,
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
}: WorkflowGraphCanvasProps) {
  return (
    <TooltipProvider delayDuration={0}>
      <ReactFlowProvider>
        <WorkflowGraphCanvasInner
          edges={graph.edges}
          onAddNode={onAddNode}
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
  nodes,
  toolbarPositionStrategy,
}: Readonly<{
  edges: readonly WorkflowGraphEdge[];
  keyboardScope: "focused" | "global";
  onAddNode: ((kind: "agent" | "script" | "terminal") => void) | undefined;
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
  nodes: readonly WorkflowGraphNode[];
  toolbarPositionStrategy: "absolute" | "fixed";
}>) {
  const instance = useReactFlow();
  const rootRef = useRef<HTMLDivElement | null>(null);
  const reconnectEndpointRef = useRef<WorkflowGraphReconnectEndpointState | null>(null);
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
  const renderEdges = useMemo(() => workflowGraphRenderEdges(edges), [edges]);
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
    [groupDrag?.targetGroupID, onCopyText, onCreateNodeGroup, onDeleteSelection, onNodeInspect, onRemoveNodeFromGroup],
  );
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
        nodes={dragAwareRenderNodes}
        nodesConnectable={onConnectNodes !== undefined || onReconnectEdge !== undefined}
        nodesDraggable={false}
        nodeTypes={nodeTypes}
        onConnect={(connection) => {
          connectWorkflowGraphNodes(connection, onConnectNodes);
        }}
        onEdgeClick={(_event, edge) => {
          setSelection(selectionFromEdge(edge));
          inspectEdge(edge, onEdgeInspect);
        }}
        onNodeClick={(_event, node) => {
          setSelection(selectionFromNode(node));
          inspectNode(node, onGroupInspect, onNodeInspect);
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
