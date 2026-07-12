import { fireEvent, render, screen, within } from "@testing-library/react";
import { act } from "react";
import { afterEach, beforeEach, vi } from "vitest";

import { initializeI18n } from "../../i18n/setup";
import type { WorkflowNodeKind } from "../../api";
import { workflowEditorEnglish } from "../../i18n/workflowEditorEn";
import { WorkflowGraphCanvas } from "./WorkflowGraphCanvas";
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
    render(
      <WorkflowGraphCanvas
        graph={{
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
        }}
        onEdgeInspect={onEdgeInspect}
        onAddConnectedNode={onAddConnectedNode}
        onGroupInspect={() => undefined}
        onNodeInspect={onNodeInspect}
        onWorkflowInspect={() => undefined}
      />,
    );

    const agent = screen.getByTestId("workflow-graph-node-agent");
    fireEvent.click(agent);
    expect(onNodeInspect).toHaveBeenCalledExactlyOnceWith("agent");
    expect(onEdgeInspect).not.toHaveBeenCalled();

    fireEvent.click(within(agent).getByTestId("workflow-node-source-handle"), { detail: 1 });
    fireEvent.click(await screen.findByRole("button", { name: workflowEditorEnglish.addAgentNode }), { detail: 1 });
    expect(onAddConnectedNode).toHaveBeenCalledWith("agent", "agent", "pointer");
    expect(onEdgeInspect).not.toHaveBeenCalled();
  });

  it("shows a visible creation handle while keeping routed endpoint handles node-side invisible", () => {
    render(
      <WorkflowGraphCanvas
        graph={{
          edges: [],
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
        }}
        onEdgeInspect={() => undefined}
        onAddConnectedNode={() => undefined}
        onGroupInspect={() => undefined}
        onNodeInspect={() => undefined}
        onWorkflowInspect={() => undefined}
      />,
    );

    const agent = screen.getByTestId("workflow-graph-node-agent");
    expect(within(agent).getByTestId("workflow-node-source-handle")).toBeInTheDocument();
    expect(within(agent).getByTestId("workflow-node-connection-target-handle")).toBeInTheDocument();
    expect(within(agent).queryAllByTestId("workflow-node-target-handle")).toHaveLength(0);
    expect(within(agent).getAllByTestId("workflow-node-endpoint-handle")).toHaveLength(2);
  });

  it("delivers an external edge selection only after its layout edge appears and never after cancellation", async () => {
    const onGraphSelectionConsumed = vi.fn();
    const request = { edgeID: "edge-delayed", requestID: "request-delayed" };
    const props = {
      onEdgeInspect: () => undefined,
      onGraphSelectionConsumed,
      onGroupInspect: () => undefined,
      onNodeInspect: () => undefined,
      onWorkflowInspect: () => undefined,
    };
    const { rerender } = render(
      <WorkflowGraphCanvas
        {...props}
        graph={{ edges: [], nodes: [] }}
        graphSelectionRequest={request}
      />,
    );

    expect(onGraphSelectionConsumed).not.toHaveBeenCalled();

    rerender(
      <WorkflowGraphCanvas
        {...props}
        graph={{
          edges: [workflowGraphEdge({ id: "edge-delayed", routePoints: [], source: "source", target: "target" })],
          nodes: [],
        }}
        graphSelectionRequest={request}
      />,
    );
    await act(async () => {
      await new Promise<void>((resolve) => {
        queueMicrotask(resolve);
      });
    });
    expect(onGraphSelectionConsumed).toHaveBeenCalledExactlyOnceWith("request-delayed");

    onGraphSelectionConsumed.mockClear();
    rerender(
      <WorkflowGraphCanvas
        {...props}
        graph={{ edges: [], nodes: [] }}
        graphSelectionRequest={null}
      />,
    );
    rerender(
      <WorkflowGraphCanvas
        {...props}
        graph={{
          edges: [workflowGraphEdge({ id: "edge-delayed", routePoints: [], source: "source", target: "target" })],
          nodes: [],
        }}
        graphSelectionRequest={null}
      />,
    );
    await act(async () => {
      await new Promise<void>((resolve) => {
        queueMicrotask(resolve);
      });
    });
    expect(onGraphSelectionConsumed).not.toHaveBeenCalled();

    const canceledAfterLayoutRequest = { edgeID: "edge-delayed", requestID: "request-canceled-after-layout" };
    rerender(
      <WorkflowGraphCanvas
        {...props}
        graph={{ edges: [], nodes: [] }}
        graphSelectionRequest={canceledAfterLayoutRequest}
      />,
    );
    rerender(
      <WorkflowGraphCanvas
        {...props}
        graph={{
          edges: [workflowGraphEdge({ id: "edge-delayed", routePoints: [], source: "source", target: "target" })],
          nodes: [],
        }}
        graphSelectionRequest={canceledAfterLayoutRequest}
      />,
    );
    rerender(
      <WorkflowGraphCanvas
        {...props}
        graph={{
          edges: [workflowGraphEdge({ id: "edge-delayed", routePoints: [], source: "source", target: "target" })],
          nodes: [],
        }}
        graphSelectionRequest={null}
      />,
    );
    await act(async () => {
      await new Promise<void>((resolve) => {
        queueMicrotask(resolve);
      });
    });
    expect(onGraphSelectionConsumed).not.toHaveBeenCalled();
  });

});

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
