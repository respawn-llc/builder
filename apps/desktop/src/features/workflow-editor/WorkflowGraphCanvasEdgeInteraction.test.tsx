import { fireEvent, render, screen, within } from "@testing-library/react";
import { act } from "react";
import { afterEach, beforeEach, vi } from "vitest";

import { initializeI18n } from "../../i18n/setup";
import type { WorkflowNodeKind } from "../../api";
import { workflowEditorEnglish } from "../../i18n/workflowEditorEn";
import { WorkflowGraphCanvas, type WorkflowGraphCanvasProps } from "./WorkflowGraphCanvas";
import type { WorkflowGraphEdge, WorkflowGraphNode, WorkflowGraphNodeData } from "./workflowGraphLayout";

void initializeI18n();

type WorkflowGraphEdgeData = NonNullable<WorkflowGraphEdge["data"]>;

describe("WorkflowGraphCanvas edge interactions", () => {
  const originalResizeObserver = globalThis.ResizeObserver;

  beforeEach(() => {
    globalThis.ResizeObserver = MockResizeObserver;
  });

  afterEach(() => {
    globalThis.ResizeObserver = originalResizeObserver;
    vi.restoreAllMocks();
  });

  it("keeps node and handle quick-add available with a crossing edge route in the canvas graph", async () => {
    const onEdgeInspect = vi.fn();
    const onNodeInspect = vi.fn();
    const onAddConnectedNode = vi.fn();
    renderCanvas({
      graph: testGraph({
        edges: [
          workflowGraphEdge({
            id: "edge-crossing-agent",
            routePoints: [
              { x: -40, y: 46 },
              { x: 260, y: 46 },
            ],
            source: "start",
            target: "terminal",
          }),
        ],
        nodes: [
          workflowGraphNode({ id: "start", kind: "start", label: "Backlog", x: -280 }),
          workflowGraphNode({ id: "agent", kind: "agent", label: "Agent", x: 0 }),
          workflowGraphNode({ id: "terminal", kind: "terminal", label: "Done", x: 320 }),
        ],
      }),
      onAddConnectedNode,
      onEdgeInspect,
      onNodeInspect,
    });

    const agent = screen.getByTestId("workflow-graph-node-agent");
    fireEvent.click(agent);
    expect(onNodeInspect).toHaveBeenCalledExactlyOnceWith("agent");
    expect(onEdgeInspect).not.toHaveBeenCalled();

    fireEvent.click(within(agent).getByTestId("workflow-node-source-handle"), { detail: 1 });
    fireEvent.click(await screen.findByRole("button", { name: workflowEditorEnglish.addAgentNode }), {
      detail: 1,
    });
    expect(onAddConnectedNode).toHaveBeenCalledWith("agent", "agent", "pointer");
    expect(onEdgeInspect).not.toHaveBeenCalled();
  });

  it("shows a visible creation handle while keeping routed endpoint handles node-side invisible", () => {
    renderCanvas({
      graph: testGraph({
        nodes: [
          workflowGraphNode({
            endpointPorts: [
              { id: "workflow-target-endpoint-edge-a", nodeID: "agent", side: "target", y: 23 },
              { id: "workflow-source-endpoint-edge-a", nodeID: "agent", side: "source", y: 69 },
            ],
            id: "agent",
            kind: "agent",
            label: "Agent",
            x: 0,
          }),
        ],
      }),
      onAddConnectedNode: noop,
    });

    const agent = screen.getByTestId("workflow-graph-node-agent");
    expect(within(agent).getByTestId("workflow-node-source-handle")).toBeInTheDocument();
    expect(within(agent).getByTestId("workflow-node-connection-target-handle")).toBeInTheDocument();
    expect(within(agent).queryAllByTestId("workflow-node-target-handle")).toHaveLength(0);
    expect(within(agent).getAllByTestId("workflow-node-endpoint-handle")).toHaveLength(2);
  });

  it("delivers an external edge selection only after its layout edge appears and never after cancellation", async () => {
    const onGraphSelectionConsumed = vi.fn();
    const request = { edgeID: "edge-delayed", requestID: "request-delayed" };
    const emptyGraph = testGraph();
    const edgeGraph = testGraph({
      edges: [workflowGraphEdge({ id: "edge-delayed", routePoints: [], source: "source", target: "target" })],
    });
    const { rerenderCanvas } = renderCanvas({
      graph: emptyGraph,
      graphSelectionRequest: request,
      onGraphSelectionConsumed,
    });

    expect(onGraphSelectionConsumed).not.toHaveBeenCalled();

    rerenderCanvas({
      graph: edgeGraph,
      graphSelectionRequest: request,
    });
    await settleGraphSelection();
    expect(onGraphSelectionConsumed).toHaveBeenCalledExactlyOnceWith("request-delayed");

    onGraphSelectionConsumed.mockClear();
    rerenderCanvas({ graph: emptyGraph, graphSelectionRequest: null });
    rerenderCanvas({ graph: edgeGraph, graphSelectionRequest: null });
    await settleGraphSelection();
    expect(onGraphSelectionConsumed).not.toHaveBeenCalled();

    const canceledAfterLayoutRequest = { edgeID: "edge-delayed", requestID: "request-canceled-after-layout" };
    rerenderCanvas({ graph: emptyGraph, graphSelectionRequest: canceledAfterLayoutRequest });
    rerenderCanvas({ graph: edgeGraph, graphSelectionRequest: canceledAfterLayoutRequest });
    rerenderCanvas({ graph: edgeGraph, graphSelectionRequest: null });
    await settleGraphSelection();
    expect(onGraphSelectionConsumed).not.toHaveBeenCalled();
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

function testGraph({
  edges = [],
  nodes = [],
}: Partial<WorkflowGraphCanvasProps["graph"]> = {}): WorkflowGraphCanvasProps["graph"] {
  return { edges, nodes };
}

function renderCanvas(overrides: Partial<WorkflowGraphCanvasProps> = {}) {
  const props = { ...defaultCanvasProps, ...overrides };
  const { rerender } = render(<WorkflowGraphCanvas {...props} />);
  return {
    rerenderCanvas: (nextOverrides: Partial<WorkflowGraphCanvasProps>) => {
      rerender(<WorkflowGraphCanvas {...props} {...nextOverrides} />);
    },
  };
}

async function settleGraphSelection(): Promise<void> {
  await act(async () => {
    await new Promise<void>((resolve) => {
      queueMicrotask(resolve);
    });
  });
}

class MockResizeObserver implements ResizeObserver {
  observe(): void {
    return;
  }

  unobserve(): void {
    return;
  }

  disconnect(): void {
    return;
  }
}

function workflowGraphNode({
  endpointPorts = [],
  id,
  kind,
  label,
  x,
}: Readonly<{
  endpointPorts?: WorkflowGraphNodeData["endpointPorts"];
  id: string;
  kind: WorkflowNodeKind;
  label: string;
  x: number;
}>): WorkflowGraphNode {
  return {
    data: {
      entityID: id,
      entityKind: "node",
      endpointPorts,
      groupID: "",
      hasError: false,
      key: id,
      kind,
      label,
      role: kind === "agent" ? "coder" : "",
    },
    draggable: kind === "agent",
    id,
    position: { x, y: 0 },
    style: { height: 92, width: 220 },
    type: "workflowNode",
  };
}

function workflowGraphEdge({
  id,
  routePoints,
  source,
  target,
}: Readonly<{
  id: string;
  routePoints: WorkflowGraphEdgeData["routePoints"];
  source: string;
  target: string;
}>): WorkflowGraphEdge {
  return {
    data: {
      contextMode: "new_session",
      entityID: id,
      entityKind: "edge",
      hasError: false,
      label: "",
      routePoints,
      transitionGroupID: `transition-group-${id}`,
    },
    id,
    source,
    target,
    type: "workflow",
  };
}
