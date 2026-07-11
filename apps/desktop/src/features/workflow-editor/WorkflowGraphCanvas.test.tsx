import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, vi } from "vitest";

import { initializeI18n } from "../../i18n/setup";
import type { WorkflowNodeKind } from "../../api";
import { workflowEditorEnglish } from "../../i18n/workflowEditorEn";
import { WorkflowGraphCanvas, WorkflowNodeInfoTooltipContent } from "./WorkflowGraphCanvas";
import { groupIDFromPoint } from "./workflowGraphCanvasInteractions";
import type { WorkflowGraphNode } from "./workflowGraphLayout";

void initializeI18n();

describe("WorkflowGraphCanvas", () => {
  const originalResizeObserver = globalThis.ResizeObserver;

  beforeEach(() => {
    globalThis.ResizeObserver = MockResizeObserver;
  });

  afterEach(() => {
    globalThis.ResizeObserver = originalResizeObserver;
    vi.restoreAllMocks();
  });

  it("renders graph nodes and opens inspectors for editable nodes", async () => {
    const copied: string[] = [];
    const onNodeInspect = vi.fn();
    const onAddConnectedNode = vi.fn();
    const { unmount } = render(
      <WorkflowGraphCanvas
        graph={{
          edges: [],
          nodes: [
            workflowGroupGraphNode({ hasError: false, id: "group", label: "Group", x: -140 }),
            workflowGraphNode({
              hasError: false,
              id: "start",
              kind: "start",
              label: "Backlog",
              nodeKey: "backlog",
              x: 0,
            }),
            workflowGraphNode({ hasError: false, id: "agent", kind: "agent", label: "Agent", x: 120 }),
            workflowGraphNode({
              hasError: false,
              id: "terminal",
              kind: "terminal",
              label: "Done",
              nodeKey: "done",
              x: 240,
            }),
            workflowGraphNode({
              hasError: false,
              id: "join",
              kind: "join",
              label: "Join",
              type: "workflowJoin",
              x: 360,
            }),
            workflowGraphNode({ hasError: true, id: "error", kind: "agent", label: "Broken", x: 360 }),
          ],
        }}
        onCopyText={(value) => {
          copied.push(value);
        }}
        onAddConnectedNode={onAddConnectedNode}
        onEdgeInspect={() => undefined}
        onGroupInspect={() => undefined}
        onNodeInspect={onNodeInspect}
        onWorkflowInspect={() => undefined}
      />,
    );

    expect(screen.getByTestId("workflow-graph-node-start")).toHaveAttribute("data-kind", "start");
    expect(within(screen.getByTestId("workflow-graph-node-start")).queryAllByTestId("workflow-node-target-handle")).toHaveLength(0);
    expect(within(screen.getByTestId("workflow-graph-node-start")).queryAllByTestId("workflow-node-source-handle")).toHaveLength(1);
    expect(screen.getByTestId("workflow-graph-node-agent")).toHaveAttribute("data-kind", "agent");
    expect(screen.getByTestId("workflow-graph-node-agent")).not.toHaveAttribute("draggable", "true");
    expect(screen.getByTestId("workflow-graph-node-terminal")).toHaveAttribute("data-kind", "terminal");
    expect(screen.getByTestId("workflow-graph-node-join")).toHaveAttribute("data-kind", "join");
    expect(screen.getByTestId("workflow-graph-node-join")).not.toHaveAttribute("draggable", "true");
    fireEvent.click(screen.getByTestId("workflow-graph-node-start"));
    expect(onNodeInspect).toHaveBeenLastCalledWith("start");
    fireEvent.click(screen.getByTestId("workflow-graph-node-terminal"));
    expect(onNodeInspect).toHaveBeenLastCalledWith("terminal");
    fireEvent.click(screen.getByTestId("workflow-graph-node-join"));
    expect(onNodeInspect).toHaveBeenCalledWith("join");
    fireEvent.click(screen.getByTestId("workflow-graph-node-agent"));
    expect(onNodeInspect).toHaveBeenCalledWith("agent");
    fireEvent.click(within(screen.getByTestId("workflow-graph-node-agent")).getByTestId("workflow-node-source-handle"), {
      detail: 1,
    });
    fireEvent.click(await screen.findByRole("button", { name: workflowEditorEnglish.addAgentNode }), { detail: 1 });
    expect(onAddConnectedNode).toHaveBeenCalledWith("agent", "agent", "pointer");
    fireEvent.pointerMove(screen.getByTestId("workflow-graph-node-join"), { pointerType: "mouse" });
    await waitFor(() => {
      expect(screen.getByTestId("workflow-node-metadata-tooltip")).toBeInTheDocument();
    });
    expect(screen.queryByTestId("tooltip-arrow")).not.toBeInTheDocument();
    fireEvent.pointerMove(screen.getByTestId("workflow-graph-node-start"), { pointerType: "mouse" });
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "Copy Key backlog" })).not.toBeInTheDocument();
    });
    fireEvent.pointerMove(screen.getByTestId("workflow-graph-node-terminal"), { pointerType: "mouse" });
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "Copy Key done" })).not.toBeInTheDocument();
    });
    expect(screen.getAllByTitle("Drag node to group")).toHaveLength(2);

    unmount();
    const longNodeID = "node_0123456789abcdef0123456789abcdef";
    render(
      <WorkflowNodeInfoTooltipContent
        nodeID={longNodeID}
        nodeKey="backlog"
        onCopyText={(value) => {
          copied.push(value);
        }}
      />,
    );
    expect(screen.getByText(longNodeID)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Copy Key backlog" }));
    fireEvent.click(screen.getByRole("button", { name: `Copy ID ${longNodeID}` }));
    expect(copied).toEqual(["backlog", longNodeID]);
  });

  it("adds nodes from the canvas toolbar and reserves plain plus for add", async () => {
    const onAddNode = vi.fn();
    render(
      <WorkflowGraphCanvas
        graph={{ edges: [], nodes: [] }}
        onAddNode={onAddNode}
        onEdgeInspect={() => undefined}
        onGroupInspect={() => undefined}
        onNodeInspect={() => undefined}
        onWorkflowInspect={() => undefined}
      />,
    );

    fireEvent.pointerEnter(screen.getByRole("button", { name: "Add node" }));
    fireEvent.click(await screen.findByRole("button", { name: "Agent node" }));
    fireEvent.pointerEnter(screen.getByRole("button", { name: "Add node" }));
    fireEvent.click(await screen.findByRole("button", { name: "Terminal node" }));

    expect(onAddNode).toHaveBeenNthCalledWith(1, "agent");
    expect(onAddNode).toHaveBeenNthCalledWith(2, "terminal");
    expect(screen.getByRole("button", { name: "Zoom in" })).toBeEnabled();
  });

  it("selects graph entities for keyboard deletion and hides terminal source handles", () => {
    const onDeleteSelection = vi.fn();
    render(
      <WorkflowGraphCanvas
        graph={{
          edges: [],
          nodes: [
            workflowGraphNode({ hasError: false, id: "agent", kind: "agent", label: "Agent", x: 0 }),
            workflowGraphNode({ hasError: false, id: "terminal", kind: "terminal", label: "Done", x: 160 }),
          ],
        }}
        onDeleteSelection={onDeleteSelection}
        onAddConnectedNode={() => undefined}
        onEdgeInspect={() => undefined}
        onGroupInspect={() => undefined}
        onNodeInspect={() => undefined}
        onWorkflowInspect={() => undefined}
      />,
    );

    fireEvent.click(screen.getByTestId("workflow-graph-node-agent"));
    fireEvent.keyDown(window, { key: "Delete" });

    expect(onDeleteSelection).toHaveBeenCalledWith({ kind: "node", nodeID: "agent" });
    expect(within(screen.getByTestId("workflow-graph-node-agent")).queryAllByTestId("workflow-node-source-handle")).toHaveLength(1);
    expect(within(screen.getByTestId("workflow-graph-node-terminal")).queryAllByTestId("workflow-node-source-handle")).toHaveLength(0);
  });

  it("renders real plus icons for non-terminal creation handles on workflow and join nodes", () => {
    render(
      <WorkflowGraphCanvas
        graph={{
          edges: [],
          nodes: [
            workflowGraphNode({ hasError: false, id: "agent", kind: "agent", label: "Agent", x: 0 }),
            workflowGraphNode({
              hasError: false,
              id: "join",
              kind: "join",
              label: "Join",
              type: "workflowJoin",
              x: 160,
            }),
            workflowGraphNode({ hasError: false, id: "terminal", kind: "terminal", label: "Done", x: 320 }),
          ],
        }}
        onAddConnectedNode={() => undefined}
        onEdgeInspect={() => undefined}
        onGroupInspect={() => undefined}
        onNodeInspect={() => undefined}
        onWorkflowInspect={() => undefined}
      />,
    );

    const creationHandleIcons = screen.getAllByTestId("workflow-node-source-handle-icon");
    expect(creationHandleIcons).toHaveLength(2);
    expect(creationHandleIcons.map((icon) => icon.dataset.workflowNodeId)).toEqual(["agent", "join"]);
    expect(creationHandleIcons.every((icon) => icon.tagName.toLowerCase() === "svg")).toBe(true);
    expect(screen.getAllByTestId("workflow-node-source-handle")).toHaveLength(2);
  });

  it("creates node groups from context menu and drag-drops nodes onto groups", async () => {
    const onAddNodeToGroup = vi.fn();
    const onCreateNodeGroup = vi.fn();
    const onNodeInspect = vi.fn();
    render(
      <WorkflowGraphCanvas
        graph={{
          edges: [],
          nodes: [
            workflowGroupGraphNode({ hasError: false, id: "group", label: "Group", x: -140 }),
            workflowGraphNode({ hasError: false, id: "agent", kind: "agent", label: "Agent", x: 0 }),
          ],
        }}
        onAddNodeToGroup={onAddNodeToGroup}
        onAddConnectedNode={() => undefined}
        onCreateNodeGroup={onCreateNodeGroup}
        onEdgeInspect={() => undefined}
        onGroupInspect={() => undefined}
        onNodeInspect={onNodeInspect}
        onWorkflowInspect={() => undefined}
      />,
    );

    fireEvent.contextMenu(screen.getByTestId("workflow-graph-node-agent"), { clientX: 24, clientY: 32 });
    fireEvent.click(screen.getByRole("menuitem", { name: "Create node group" }));
    expect(onCreateNodeGroup).toHaveBeenCalledWith("agent");

    const card = screen.getByTestId("workflow-graph-node-agent");
    const eventView = card.ownerDocument.defaultView;
    if (eventView === null) {
      throw new Error("Expected test document to have a default window");
    }
    const elementFromPoint = vi.fn<typeof document.elementFromPoint>(() => card);
    Object.defineProperty(document, "elementFromPoint", {
      configurable: true,
      value: elementFromPoint,
    });
    dispatchMouseEvent(within(card).getByTestId("workflow-node-source-handle"), eventView, "mousedown", {
      button: 0,
      clientX: 12,
      clientY: 18,
    });
    dispatchMouseEvent(eventView, eventView, "mousemove", { buttons: 1, clientX: 30, clientY: 34 });
    dispatchMouseEvent(eventView, eventView, "mouseup", { clientX: 36, clientY: 40 });
    expect(screen.queryByTestId("workflow-group-drag-preview")).not.toBeInTheDocument();
    expect(onAddNodeToGroup).not.toHaveBeenCalled();

    dispatchMouseEvent(card, eventView, "mousedown", { button: 0, clientX: 12, clientY: 18 });
    dispatchMouseEvent(eventView, eventView, "mousemove", { buttons: 1, clientX: 24, clientY: 28 });
    dispatchMouseEvent(eventView, eventView, "mousemove", { buttons: 1, clientX: 30, clientY: 34 });
    await waitFor(() => {
      expect(screen.getByTestId("workflow-group-drag-preview")).toHaveTextContent("Agent");
    });
    Object.defineProperty(screen.getByTestId("workflow-graph-group-group"), "getBoundingClientRect", {
      configurable: true,
      value: () => new eventView.DOMRect(0, 0, 100, 100),
    });
    expect(groupIDFromPoint(36, 40)).toBe("group");
    dispatchMouseEvent(eventView, eventView, "mouseup", { clientX: 36, clientY: 40 });

    expect(elementFromPoint).toHaveBeenCalledWith(36, 40);
    expect(onAddNodeToGroup).toHaveBeenCalledWith("agent", "group");
    expect(screen.queryByTestId("workflow-group-drag-preview")).not.toBeInTheDocument();

    fireEvent.click(card);
    expect(onNodeInspect).toHaveBeenCalledWith("agent");
  });

  it("extracts grouped nodes only when dropped outside groups", async () => {
    const onAddNodeToGroup = vi.fn();
    const onExtractNodeFromGroup = vi.fn();
    render(
      <WorkflowGraphCanvas
        graph={{
          edges: [],
          nodes: [
            workflowGroupGraphNode({ hasError: false, id: "group", label: "Group", x: -140 }),
            workflowGroupGraphNode({ hasError: false, id: "other", label: "Other", x: 260 }),
            workflowGraphNode({
              groupID: "group",
              hasError: false,
              id: "agent",
              kind: "agent",
              label: "Agent",
              parentId: "group",
              x: 0,
            }),
          ],
        }}
        onAddNodeToGroup={onAddNodeToGroup}
        onEdgeInspect={() => undefined}
        onExtractNodeFromGroup={onExtractNodeFromGroup}
        onGroupInspect={() => undefined}
        onNodeInspect={() => undefined}
        onWorkflowInspect={() => undefined}
      />,
    );

    const card = screen.getByTestId("workflow-graph-node-agent");
    const eventView = card.ownerDocument.defaultView;
    if (eventView === null) {
      throw new Error("Expected test document to have a default window");
    }
    Object.defineProperty(document, "elementFromPoint", {
      configurable: true,
      value: vi.fn<typeof document.elementFromPoint>(() => card),
    });

    dragNode(card, eventView, { x: 500, y: 500 });
    await waitFor(() => {
      expect(onExtractNodeFromGroup).toHaveBeenCalledWith("agent");
    });
    expect(onAddNodeToGroup).not.toHaveBeenCalled();

    onAddNodeToGroup.mockClear();
    onExtractNodeFromGroup.mockClear();
    Object.defineProperty(screen.getByTestId("workflow-graph-group-group"), "getBoundingClientRect", {
      configurable: true,
      value: () => new eventView.DOMRect(0, 0, 100, 100),
    });
    dragNode(card, eventView, { x: 36, y: 40 });
    expect(onAddNodeToGroup).not.toHaveBeenCalled();
    expect(onExtractNodeFromGroup).not.toHaveBeenCalled();
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

function dispatchMouseEvent(
  target: Document | Element | Window,
  view: Window & typeof globalThis,
  type: "mousedown" | "mousemove" | "mouseup",
  options: MouseEventInit,
): void {
  const event = new view.MouseEvent(type, { bubbles: true, cancelable: true, ...options });
  Object.defineProperty(event, "view", { value: view });
  fireEvent(target, event);
}

function dragNode(
  element: HTMLElement,
  eventView: Window & typeof globalThis,
  end: Readonly<{ x: number; y: number }>,
): void {
  dispatchMouseEvent(element, eventView, "mousedown", { button: 0, clientX: 12, clientY: 18 });
  dispatchMouseEvent(eventView, eventView, "mousemove", { buttons: 1, clientX: 28, clientY: 34 });
  dispatchMouseEvent(eventView, eventView, "mousemove", { buttons: 1, clientX: end.x, clientY: end.y });
  dispatchMouseEvent(eventView, eventView, "mouseup", { clientX: end.x, clientY: end.y });
}

function workflowGraphNode({
  id,
  label,
  kind,
  hasError,
  groupID,
  nodeKey = id,
  parentId,
  x,
  type = "workflowNode",
}: Readonly<{
  hasError: boolean;
  id: string;
  kind: WorkflowNodeKind;
  label: string;
  nodeKey?: string;
  type?: string;
  x: number;
  groupID?: string | undefined;
  parentId?: string | undefined;
}>): WorkflowGraphNode {
  return {
    data: {
      entityID: id,
      entityKind: "node",
      groupID: groupID ?? "",
      hasError,
      key: nodeKey,
      kind,
      label,
      role: kind === "agent" ? "coder" : "",
    },
    draggable: kind === "agent",
    ...(parentId === undefined ? {} : { extent: "parent", parentId }),
    id,
    position: { x, y: 0 },
    style: { height: 92, width: 220 },
    type,
  };
}

function workflowGroupGraphNode({
  id,
  label,
  hasError,
  x,
}: Readonly<{
  hasError: boolean;
  id: string;
  label: string;
  x: number;
}>): WorkflowGraphNode {
  return {
    data: {
      empty: true,
      entityID: id,
      entityKind: "group",
      hasError,
      kind: "group",
      label,
    },
    draggable: false,
    id,
    position: { x, y: 140 },
    style: { height: 140, width: 260 },
    type: "workflowGroup",
  };
}
