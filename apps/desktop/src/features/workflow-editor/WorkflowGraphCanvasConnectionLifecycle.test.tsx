import { fireEvent, render, screen, within } from "@testing-library/react";
import type { ComponentProps, ComponentType, ReactElement, ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactFlow as ReactFlowComponent } from "@xyflow/react";
import { z } from "zod";

import { initializeI18n } from "@/i18n";
import { workflowEditorEnglish } from "@/i18n";
import type { WorkflowGraphEdge, WorkflowGraphNode } from "./workflowGraphLayout";

type ReactFlowProps = ComponentProps<typeof ReactFlowComponent>;
interface LifecycleNodeProps {
  data: unknown;
  dragging: boolean;
  selected: boolean;
}

type LifecycleHandleProps = Readonly<
  Pick<
    ComponentProps<"button">,
    "aria-label" | "className" | "id" | "onClick" | "onKeyDown" | "role" | "tabIndex"
  > & { "data-testid"?: string }
>;
const lifecycleNodeRendererSchema = z.custom<ComponentType<LifecycleNodeProps>>(
  (value) => z.function().safeParse(value).success,
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

  function connectionLifecycleButton(
    props: ReactFlowProps,
    lifecycle: "cancel" | "complete" | "stationary",
  ): ReactElement {
    return renderElement("button", {
      "data-testid": `connection-${lifecycle}`,
      onClick: () => {
        props.onConnectStart?.(new MouseEvent("pointerdown", { clientX: 10, clientY: 10 }), {
          handleId: "creation-source",
          handleType: "source",
          nodeId: "source",
        });
        if (lifecycle === "complete") {
          props.onConnect?.({ source: "source", target: "target" });
        }
        props.onConnectEnd?.(
          new MouseEvent("pointerup", { clientX: lifecycle === "stationary" ? 10 : 20, clientY: 10 }),
        );
      },
      type: "button",
    });
  }

  function ReactFlow(props: ReactFlowProps): ReactNode {
    reactFlowHarness.props = props;
    return renderElement(
      "div",
      { "data-testid": "react-flow-lifecycle-harness" },
      props.nodes?.map((node) => renderLifecycleNode(props, node)),
      connectionLifecycleButton(props, "complete"),
      connectionLifecycleButton(props, "cancel"),
      connectionLifecycleButton(props, "stationary"),
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
    Handle: ({
      "aria-label": ariaLabel,
      className,
      "data-testid": testID,
      id,
      onClick,
      onKeyDown,
      role,
      tabIndex,
    }: LifecycleHandleProps) =>
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
    ReactFlowProvider: ({ children }: Readonly<{ children?: ReactNode }>) =>
      renderElement(Fragment, null, children),
    applyNodeChanges: <NodeType,>(_: unknown, nodes: readonly NodeType[]) => nodes,
    useReactFlow: () => reactFlowHarness.instance,
  };
});

import { WorkflowGraphCanvas, type WorkflowGraphCanvasProps } from "./WorkflowGraphCanvas";

void initializeI18n();

describe("WorkflowGraphCanvas connection lifecycle", () => {
  beforeEach(() => {
    reactFlowHarness.props = null;
    vi.clearAllMocks();
  });

  it("suppresses only completed and canceled drag activation, then recovers for the next click", async () => {
    const onAddConnectedNode = vi.fn();
    const onConnectNodes = vi.fn();
    renderCanvas({
      graph: testGraph(workflowGraphNode("source", "Source", 0), workflowGraphNode("target", "Target", 320)),
      onAddConnectedNode,
      onConnectNodes,
    });

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
    expect(
      await screen.findByRole("button", { name: workflowEditorEnglish.addAgentNode }),
    ).toBeInTheDocument();
    expect(onAddConnectedNode).not.toHaveBeenCalled();
  });

  it("does not suppress a stationary activation lifecycle", async () => {
    renderCanvas({
      graph: testGraph(workflowGraphNode("source", "Source", 0)),
      onAddConnectedNode: noop,
    });

    fireEvent.click(screen.getByTestId("connection-stationary"));
    fireEvent.click(
      within(screen.getByTestId("workflow-graph-node-source")).getByTestId("workflow-node-source-handle"),
      { detail: 1 },
    );

    expect(
      await screen.findByRole("button", { name: workflowEditorEnglish.addAgentNode }),
    ).toBeInTheDocument();
  });

  it("clears a selected quick-add transition before Delete when the canvas pane is clicked", async () => {
    const onDeleteSelection = vi.fn();
    const onGraphSelectionConsumed = vi.fn();
    renderCanvas({
      graph: { edges: [workflowGraphEdge("edge-created")], nodes: [] },
      graphSelectionRequest: { edgeID: "edge-created", requestID: "quick-add-created-edge" },
      onDeleteSelection,
      onGraphSelectionConsumed,
    });

    await screen.findByTestId("react-flow-lifecycle-harness");
    await vi.waitFor(() => {
      expect(onGraphSelectionConsumed).toHaveBeenCalledExactlyOnceWith("quick-add-created-edge");
    });
    fireEvent.click(screen.getByTestId("canvas-pane-click"));
    fireEvent.keyDown(window, { key: "Delete" });

    expect(onDeleteSelection).not.toHaveBeenCalled();
  });
});

const noop = () => undefined;
const defaultCanvasProps = {
  graph: testGraph(),
  onEdgeInspect: noop,
  onGroupInspect: noop,
  onNodeInspect: noop,
  onWorkflowInspect: noop,
} satisfies WorkflowGraphCanvasProps;

function testGraph(...nodes: readonly WorkflowGraphNode[]): WorkflowGraphCanvasProps["graph"] {
  return { edges: [], nodes };
}

function renderCanvas(overrides: Partial<WorkflowGraphCanvasProps> = {}) {
  return render(<WorkflowGraphCanvas {...defaultCanvasProps} {...overrides} />);
}

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

function workflowGraphNode(id: string, label: string, x: number): WorkflowGraphNode {
  return {
    data: {
      endpointPorts: [],
      entityID: id,
      entityKind: "node",
      groupID: "",
      hasError: false,
      key: id,
      kind: "agent",
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
