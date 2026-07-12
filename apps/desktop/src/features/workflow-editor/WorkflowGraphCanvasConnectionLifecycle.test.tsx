import { fireEvent, render, screen, within } from "@testing-library/react";
import type {
  ComponentProps,
  ComponentType,
  KeyboardEvent as ReactKeyboardEvent,
  MouseEvent as ReactMouseEvent,
  ReactElement,
  ReactNode,
} from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactFlow as ReactFlowComponent } from "@xyflow/react";
import { z } from "zod";

import { initializeI18n } from "../../i18n/setup";
import { workflowEditorEnglish } from "../../i18n/workflowEditorEn";
import type { WorkflowGraphEdge, WorkflowGraphNode } from "./workflowGraphLayout";

type ReactFlowProps = ComponentProps<typeof ReactFlowComponent>;
interface LifecycleNodeProps {
  data: unknown;
  dragging: boolean;
  selected: boolean;
}

type LifecycleHandleProps = Readonly<{
  "aria-label"?: string;
  className?: string;
  "data-testid"?: string;
  id?: string;
  onClick?: (event: ReactMouseEvent<HTMLButtonElement>) => void;
  onKeyDown?: (event: ReactKeyboardEvent<HTMLButtonElement>) => void;
  role?: string;
  tabIndex?: number;
}>;
const lifecycleNodeRendererSchema = z.custom<ComponentType<LifecycleNodeProps>>((value) =>
  z.function().safeParse(value).success,
);

const reactFlowHarness = vi.hoisted(
  (): {
    instance: {
      fitView: ReturnType<typeof vi.fn>;
      setViewport: ReturnType<typeof vi.fn>;
      zoomIn: ReturnType<typeof vi.fn>;
      zoomOut: ReturnType<typeof vi.fn>;
    };
    props: ReactFlowProps | null;
  } => ({
  instance: {
    fitView: vi.fn(async () => true),
    setViewport: vi.fn(async () => true),
    zoomIn: vi.fn(async () => true),
    zoomOut: vi.fn(async () => true),
  },
  props: null,
}),
);

vi.mock("@xyflow/react", async () => {
  const { createElement: renderElement, Fragment } = await import("react");

  function ReactFlow(props: ReactFlowProps): ReactNode {
    reactFlowHarness.props = props;
    return renderElement(
      "div",
      { "data-testid": "react-flow-lifecycle-harness" },
      props.nodes?.map((node) => renderLifecycleNode(props, node)),
      renderElement("button", {
        "data-testid": "connection-complete",
        onClick: () => {
          props.onConnectStart?.(new MouseEvent("pointerdown", { clientX: 10, clientY: 10 }), {
            handleId: "creation-source",
            handleType: "source",
            nodeId: "source",
          });
          props.onConnect?.({ source: "source", target: "target" });
          props.onConnectEnd?.(new MouseEvent("pointerup", { clientX: 20, clientY: 10 }));
        },
        type: "button",
      }),
      renderElement("button", {
        "data-testid": "connection-cancel",
        onClick: () => {
          props.onConnectStart?.(new MouseEvent("pointerdown", { clientX: 10, clientY: 10 }), {
            handleId: "creation-source",
            handleType: "source",
            nodeId: "source",
          });
          props.onConnectEnd?.(new MouseEvent("pointerup", { clientX: 20, clientY: 10 }));
        },
        type: "button",
      }),
      renderElement("button", {
        "data-testid": "connection-stationary",
        onClick: () => {
          props.onConnectStart?.(new MouseEvent("pointerdown", { clientX: 10, clientY: 10 }), {
            handleId: "creation-source",
            handleType: "source",
            nodeId: "source",
          });
          props.onConnectEnd?.(new MouseEvent("pointerup", { clientX: 10, clientY: 10 }));
        },
        type: "button",
      }),
      renderElement("button", {
        "data-testid": "canvas-pane-click",
        onClick: () => {
          props.onPaneClick?.(new MouseEvent("click"));
        },
        type: "button",
      }),
    );
  }

  return {
    Background: () => null,
    BackgroundVariant: { Dots: "dots" },
    Handle: ({ "aria-label": ariaLabel, className, "data-testid": testID, id, onClick, onKeyDown, role, tabIndex }: LifecycleHandleProps) =>
      renderElement("button", {
        "aria-label": ariaLabel,
        className,
        "data-testid": testID,
        id,
        onClick,
        onKeyDown,
        role,
        tabIndex,
        type: "button",
      }),
    Position: { Bottom: "bottom", Left: "left", Right: "right", Top: "top" },
    ReactFlow,
    ReactFlowProvider: ({ children }: Readonly<{ children?: ReactNode }>) => renderElement(Fragment, null, children),
    applyNodeChanges: <NodeType,>(_: unknown, nodes: readonly NodeType[]) => nodes,
    useReactFlow: () => reactFlowHarness.instance,
  };
});

import { WorkflowGraphCanvas } from "./WorkflowGraphCanvas";

void initializeI18n();

describe("WorkflowGraphCanvas connection lifecycle", () => {
  beforeEach(() => {
    reactFlowHarness.props = null;
    vi.clearAllMocks();
  });

  it("suppresses only completed and canceled drag activation, then recovers for the next click", async () => {
    const onAddConnectedNode = vi.fn();
    const onConnectNodes = vi.fn();
    render(
      <WorkflowGraphCanvas
        graph={{
          edges: [],
          nodes: [
            workflowGraphNode({ id: "source", kind: "agent", label: "Source", x: 0 }),
            workflowGraphNode({ id: "target", kind: "agent", label: "Target", x: 320 }),
          ],
        }}
        onAddConnectedNode={onAddConnectedNode}
        onConnectNodes={onConnectNodes}
        onEdgeInspect={() => undefined}
        onGroupInspect={() => undefined}
        onNodeInspect={() => undefined}
        onWorkflowInspect={() => undefined}
      />,
    );

    const sourceHandle = within(screen.getByTestId("workflow-graph-node-source")).getByTestId(
      "workflow-node-source-handle",
    );

    fireEvent.click(screen.getByTestId("connection-complete"));
    fireEvent.click(sourceHandle, { detail: 1 });
    expect(onConnectNodes).toHaveBeenCalledExactlyOnceWith("source", "target");
    expect(screen.queryByRole("button", { name: workflowEditorEnglish.addAgentNode })).toBeNull();

    fireEvent.click(screen.getByTestId("connection-cancel"));
    fireEvent.click(sourceHandle, { detail: 1 });
    expect(screen.queryByRole("button", { name: workflowEditorEnglish.addAgentNode })).toBeNull();

    fireEvent.click(sourceHandle, { detail: 1 });
    expect(await screen.findByRole("button", { name: workflowEditorEnglish.addAgentNode })).toBeInTheDocument();
    expect(onAddConnectedNode).not.toHaveBeenCalled();
  });

  it("does not suppress a stationary activation lifecycle", async () => {
    render(
      <WorkflowGraphCanvas
        graph={{ edges: [], nodes: [workflowGraphNode({ id: "source", kind: "agent", label: "Source", x: 0 })] }}
        onAddConnectedNode={() => undefined}
        onEdgeInspect={() => undefined}
        onGroupInspect={() => undefined}
        onNodeInspect={() => undefined}
        onWorkflowInspect={() => undefined}
      />,
    );

    fireEvent.click(screen.getByTestId("connection-stationary"));
    fireEvent.click(
      within(screen.getByTestId("workflow-graph-node-source")).getByTestId("workflow-node-source-handle"),
      { detail: 1 },
    );

    expect(await screen.findByRole("button", { name: workflowEditorEnglish.addAgentNode })).toBeInTheDocument();
  });

  it("clears a selected quick-add transition before Delete when the canvas pane is clicked", async () => {
    const onDeleteSelection = vi.fn();
    const onGraphSelectionConsumed = vi.fn();
    render(
      <WorkflowGraphCanvas
        graph={{ edges: [workflowGraphEdge("edge-created")], nodes: [] }}
        graphSelectionRequest={{ edgeID: "edge-created", requestID: "quick-add-created-edge" }}
        onDeleteSelection={onDeleteSelection}
        onEdgeInspect={() => undefined}
        onGraphSelectionConsumed={onGraphSelectionConsumed}
        onGroupInspect={() => undefined}
        onNodeInspect={() => undefined}
        onWorkflowInspect={() => undefined}
      />,
    );

    await screen.findByTestId("react-flow-lifecycle-harness");
    await vi.waitFor(() => {
      expect(onGraphSelectionConsumed).toHaveBeenCalledExactlyOnceWith("quick-add-created-edge");
    });
    fireEvent.click(screen.getByTestId("canvas-pane-click"));
    fireEvent.keyDown(window, { key: "Delete" });

    expect(onDeleteSelection).not.toHaveBeenCalled();
  });
});

function renderLifecycleNode(
  props: ReactFlowProps,
  node: NonNullable<ReactFlowProps["nodes"]>[number],
): ReactElement | null {
  if (node.type === undefined) {
    return null;
  }
  const nodeTypes = z.record(z.string(), lifecycleNodeRendererSchema).safeParse(props.nodeTypes);
  if (!nodeTypes.success) {
    return null;
  }
  const NodeRenderer = nodeTypes.data[node.type];
  if (NodeRenderer === undefined) {
    return null;
  }
  return <NodeRenderer data={node.data} dragging={false} key={node.id} selected={node.selected ?? false} />;
}

function workflowGraphNode({
  id,
  kind,
  label,
  x,
}: Readonly<{
  id: string;
  kind: "agent";
  label: string;
  x: number;
}>): WorkflowGraphNode {
  return {
    data: {
      endpointPorts: [],
      entityID: id,
      entityKind: "node",
      groupID: "",
      hasError: false,
      key: id,
      kind,
      label,
      role: "coder",
    },
    draggable: true,
    id,
    position: { x, y: 0 },
    style: { height: 92, width: 220 },
    type: "workflowNode",
  };
}

function workflowGraphEdge(id: string): WorkflowGraphEdge {
  return {
    data: {
      contextMode: "new_session",
      entityID: id,
      entityKind: "edge",
      hasError: false,
      label: "",
      routePoints: [],
      transitionGroupID: "transition-group",
    },
    id,
    source: "source",
    target: "target",
    type: "workflow",
  };
}
