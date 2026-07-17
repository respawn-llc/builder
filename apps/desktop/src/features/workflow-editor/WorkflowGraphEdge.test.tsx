import { fireEvent, render, screen } from "@testing-library/react";
import type { ComponentProps, ReactNode } from "react";
import { createPortal } from "react-dom";
import { describe, expect, it, vi } from "vitest";
import type * as XyflowReact from "@xyflow/react";

import { initializeI18n } from "../../i18n/setup";
import { WorkflowGraphEdge } from "./WorkflowGraphEdge";

void initializeI18n();

vi.mock("@xyflow/react", async (importOriginal) => {
  const actual = await importOriginal<typeof XyflowReact>();
  return {
    ...actual,
    EdgeLabelRenderer({ children }: Readonly<{ children?: ReactNode }>) {
      return createPortal(children, document.body);
    },
  };
});

describe("WorkflowGraphEdge", () => {
  it("renders routed edges as rounded orthogonal polylines", () => {
    renderEdge({
      data: edgeData({
        contextMode: "new_session",
        routePoints: [
          { x: 0, y: 0 },
          { x: 50, y: 0 },
          { x: 50, y: 40 },
          { x: 100, y: 40 },
        ],
      }),
      source: "join",
      targetY: 40,
    });

    expect(screen.getByTestId("workflow-edge-path")).toHaveAttribute(
      "d",
      "M 0 0 L 36 0 Q 50 0 50 14 L 50 26 Q 50 40 64 40 L 100 40",
    );
    expect(screen.getByTestId("workflow-edge-tail-marker")).toHaveAttribute("cx", "0");
    expect(screen.getByTestId("workflow-edge-tail-marker")).toHaveAttribute("cy", "0");
    expect(screen.getByTestId("workflow-edge-tail-marker")).toHaveAttribute("r", "4");
  });

  it("inspects an edge from the visible path without bubbling to the React Flow wrapper", () => {
    const onInspect = vi.fn();
    const onWrapperClick = vi.fn();
    renderEdge({ onInspect }, onWrapperClick);

    fireEvent.click(screen.getByTestId("workflow-edge-path"));

    expect(onInspect).toHaveBeenCalledExactlyOnceWith("edge-1");
    expect(onWrapperClick).not.toHaveBeenCalled();
  });

  it("shows a delete-only context menu for the edge path", async () => {
    const { onDeleteSelection, onSelectContextMenu } = mountContextMenuEdge();

    fireEvent.contextMenu(screen.getByTestId("workflow-edge-hit-path"));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete branch" }));

    expect(onSelectContextMenu).toHaveBeenCalledExactlyOnceWith("edge-1");
    expect(onDeleteSelection).toHaveBeenCalledExactlyOnceWith({ edgeID: "edge-1", kind: "edge" });
    expect(screen.queryByRole("menuitem", { name: "Delete node" })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Create node group" })).not.toBeInTheDocument();
  });

  it("shows the same delete-only context menu for the edge label", async () => {
    const { onDeleteSelection, onSelectContextMenu } = mountContextMenuEdge({
      label: "Review",
      routePoints: [
        { x: 0, y: 0 },
        { x: 100, y: 0 },
      ],
    });

    fireEvent.contextMenu(screen.getByTestId("workflow-edge-label-edge-1"));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete branch" }));

    expect(onSelectContextMenu).toHaveBeenCalledExactlyOnceWith("edge-1");
    expect(onDeleteSelection).toHaveBeenCalledExactlyOnceWith({ edgeID: "edge-1", kind: "edge" });
    expect(screen.queryByRole("menuitem", { name: "Delete node" })).not.toBeInTheDocument();
  });
});

type WorkflowGraphEdgeProps = ComponentProps<typeof WorkflowGraphEdge>;
type WorkflowGraphEdgeData = NonNullable<WorkflowGraphEdgeProps["data"]>;

const noop = () => undefined;
const defaultEdgeData = {
  contextMode: "compact_and_continue_session",
  entityID: "edge-1",
  entityKind: "edge",
  hasError: false,
  label: "",
  routePoints: [],
  transitionGroupID: "tg-1",
} satisfies WorkflowGraphEdgeData;
const defaultEdgeProps = {
  data: defaultEdgeData,
  id: "edge-1",
  onInspect: noop,
  source: "node-1",
  sourceX: 0,
  sourceY: 0,
  target: "done",
  targetX: 100,
  targetY: 0,
  type: "workflow",
} satisfies WorkflowGraphEdgeProps;

function edgeData(overrides: Partial<WorkflowGraphEdgeData> = {}): WorkflowGraphEdgeData {
  return { ...defaultEdgeData, ...overrides };
}

function renderEdge(
  overrides: Partial<WorkflowGraphEdgeProps> = {},
  onWrapperClick?: ComponentProps<"svg">["onClick"],
) {
  return render(
    <svg onClick={onWrapperClick}>
      <WorkflowGraphEdge {...defaultEdgeProps} {...overrides} />
    </svg>,
  );
}

function mountContextMenuEdge(dataOverrides: Partial<WorkflowGraphEdgeData> = {}) {
  const onDeleteSelection = vi.fn();
  const onSelectContextMenu = vi.fn();
  renderEdge({
    data: edgeData(dataOverrides),
    onDeleteSelection,
    onSelectContextMenu,
  });
  return { onDeleteSelection, onSelectContextMenu };
}
