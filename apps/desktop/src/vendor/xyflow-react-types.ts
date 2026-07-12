import type { ComponentType, CSSProperties, ReactNode } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, MouseEvent } from "react";

// Version-locked lightweight declaration shim for @xyflow/react 12.10.x.
// On @xyflow/react upgrades, verify the node-drag API surface below against upstream before bumping.
export type XYPosition = Readonly<{ x: number; y: number }>;

export type PositionValue = "left" | "right" | "top" | "bottom";

export type Node<Data extends Record<string, unknown> = Record<string, unknown>> = Readonly<{
  id: string;
  type?: string;
  parentId?: string;
  extent?: "parent";
  position: XYPosition;
  sourcePosition?: PositionValue;
  targetPosition?: PositionValue;
  data: Data;
  draggable?: boolean;
  selectable?: boolean;
  selected?: boolean;
  style?: CSSProperties;
}>;

export type NodeChange<NodeType extends Node = Node> = Readonly<{
  dragging?: boolean;
  id: string;
  item?: NodeType;
  position?: XYPosition;
  selected?: boolean;
  type: string;
}>;

export type Edge<Data extends Record<string, unknown> = Record<string, unknown>> = Readonly<{
  id: string;
  source: string;
  target: string;
  sourceHandle?: string | null;
  targetHandle?: string | null;
  type?: string;
  data?: Data;
  markerEnd?: string | Readonly<{ color?: string; type: string }>;
  reconnectable?: boolean;
  selected?: boolean;
}>;

export type Connection = Readonly<{
  source: string | null;
  sourceHandle?: string | null;
  target: string | null;
  targetHandle?: string | null;
}>;

export type ConnectionStartParams = Readonly<{
  handleId: string | null;
  handleType: "source" | "target" | null;
  nodeId: string | null;
}>;

export type NodeProps<NodeType extends Node = Node> = Readonly<{
  data: NodeType["data"];
  dragging?: boolean;
  selected: boolean;
}>;

export type EdgeProps<EdgeType extends Edge = Edge> = EdgeType &
  Readonly<{
    sourceX: number;
    sourceY: number;
    targetX: number;
    targetY: number;
    sourcePosition?: string;
    targetPosition?: string;
  }>;

export type NodeTypes = unknown;
export type EdgeTypes = unknown;

export declare const ReactFlow: ComponentType<
  Readonly<{
    children?: ReactNode;
    colorMode?: string;
    defaultEdges?: readonly Edge[];
    defaultNodes?: readonly Node[];
    edges?: readonly Edge[];
    edgeTypes?: EdgeTypes;
    fitView?: boolean;
    maxZoom?: number;
    minZoom?: number;
    nodes?: readonly Node[];
    nodesConnectable?: boolean;
    nodesDraggable?: boolean;
    nodeDragThreshold?: number;
    connectionDragThreshold?: number;
    connectOnClick?: boolean;
    nodeTypes?: NodeTypes;
    onConnect?: (connection: Connection) => void;
    onConnectStart?: (event: unknown, params: ConnectionStartParams) => void;
    onConnectEnd?: (event: unknown) => void;
    onEdgeClick?: (event: unknown, edge: Edge) => void;
    onNodeClick?: (event: unknown, node: Node) => void;
    onPaneClick?: (event: unknown) => void;
    onNodeContextMenu?: (event: MouseEvent, node: Node) => void;
    onNodeDrag?: (event: MouseEvent, node: Node, nodes: readonly Node[]) => void;
    onNodeDragStart?: (event: MouseEvent, node: Node, nodes: readonly Node[]) => void;
    onNodeDragStop?: (event: MouseEvent, node: Node, nodes: readonly Node[]) => void;
    onNodesChange?: (changes: readonly NodeChange[]) => void;
    onReconnect?: (edge: Edge, connection: Connection) => void;
    onReconnectEnd?: () => void;
    onReconnectStart?: (event: unknown, edge: Edge, handleType: "source" | "target") => void;
    panOnScroll?: boolean;
    proOptions?: Readonly<{ hideAttribution?: boolean }>;
    edgesReconnectable?: boolean;
    reconnectRadius?: number;
    selectionOnDrag?: boolean;
    zoomOnDoubleClick?: boolean;
  }>
>;

export declare const ReactFlowProvider: ComponentType<Readonly<{ children?: ReactNode }>>;
export declare const Background: ComponentType<Readonly<{ bgColor?: string; color?: string; gap?: number; size?: number; variant?: string }>>;
export declare const BackgroundVariant: Readonly<{ Dots: string }>;
export declare const BaseEdge: ComponentType<Readonly<{ "data-testid"?: string; markerEnd?: string | Readonly<{ color?: string; type: string }>; path: string; style?: CSSProperties }>>;
export declare const EdgeLabelRenderer: ComponentType<Readonly<{ children?: ReactNode }>>;
export declare const Handle: ComponentType<
  Readonly<{
    "aria-hidden"?: string;
    "aria-label"?: string;
    className?: string;
    "data-testid"?: string;
    id?: string;
    onClick?: (event: MouseEvent) => void;
    onKeyDown?: (event: ReactKeyboardEvent<HTMLDivElement>) => void;
    position: PositionValue;
    role?: string;
    style?: CSSProperties;
    tabIndex?: number;
    type: "source" | "target";
  }>
>;
export declare const MarkerType: Readonly<{ ArrowClosed: string }>;
export declare const Position: Readonly<{ Bottom: "bottom"; Left: "left"; Right: "right"; Top: "top" }>;

export declare function getBezierPath(props: EdgeProps): [string, number, number];

export declare function applyNodeChanges<NodeType extends Node>(
  changes: readonly NodeChange<NodeType>[],
  nodes: readonly NodeType[],
): NodeType[];

export declare function useReactFlow(): Readonly<{
  fitView(options?: Readonly<{ padding?: number }>): Promise<boolean>;
  setViewport(viewport: Readonly<{ x: number; y: number; zoom: number }>): Promise<boolean>;
  zoomIn(): Promise<boolean>;
  zoomOut(): Promise<boolean>;
}>;
