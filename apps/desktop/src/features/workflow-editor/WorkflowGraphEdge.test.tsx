import { fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { createPortal } from "react-dom";
import { describe, expect, it, vi } from "vitest";
import type * as XyflowReact from "@xyflow/react";

import { initializeI18n } from "@/i18n";
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
    render(
      <svg>
        <WorkflowGraphEdge
          data={{
            contextMode: "new_session",
            entityID: "edge-1",
            entityKind: "edge",
            hasError: false,
            label: "",
            routePoints: [
              { x: 0, y: 0 },
              { x: 50, y: 0 },
              { x: 50, y: 40 },
              { x: 100, y: 40 },
            ],
            transitionGroupID: "tg-1",
          }}
          id="edge-1"
          onInspect={() => undefined}
          source="join"
          sourceX={0}
          sourceY={0}
          target="done"
          targetX={100}
          targetY={40}
          type="workflow"
        />
      </svg>,
    );

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
    render(
      <svg onClick={onWrapperClick}>
        <WorkflowGraphEdge
          data={{
            contextMode: "compact_and_continue_session",
            entityID: "edge-1",
            entityKind: "edge",
            hasError: false,
            label: "",
            routePoints: [],
            transitionGroupID: "tg-1",
          }}
          id="edge-1"
          onInspect={onInspect}
          source="node-1"
          sourceX={0}
          sourceY={0}
          target="done"
          targetX={100}
          targetY={0}
          type="workflow"
        />
      </svg>,
    );

    fireEvent.click(screen.getByTestId("workflow-edge-path"));

    expect(onInspect).toHaveBeenCalledExactlyOnceWith("edge-1");
    expect(onWrapperClick).not.toHaveBeenCalled();
  });

  it("shows a delete-only context menu for the edge path", async () => {
    const onDeleteSelection = vi.fn();
    const onSelectContextMenu = vi.fn();
    render(
      <svg>
        <WorkflowGraphEdge
          data={{
            contextMode: "compact_and_continue_session",
            entityID: "edge-1",
            entityKind: "edge",
            hasError: false,
            label: "",
            routePoints: [],
            transitionGroupID: "tg-1",
          }}
          id="edge-1"
          onDeleteSelection={onDeleteSelection}
          onInspect={() => undefined}
          onSelectContextMenu={onSelectContextMenu}
          source="node-1"
          sourceX={0}
          sourceY={0}
          target="done"
          targetX={100}
          targetY={0}
          type="workflow"
        />
      </svg>,
    );

    fireEvent.contextMenu(screen.getByTestId("workflow-edge-hit-path"));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete branch" }));

    expect(onSelectContextMenu).toHaveBeenCalledExactlyOnceWith("edge-1");
    expect(onDeleteSelection).toHaveBeenCalledExactlyOnceWith({ edgeID: "edge-1", kind: "edge" });
    expect(screen.queryByRole("menuitem", { name: "Delete node" })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Create node group" })).not.toBeInTheDocument();
  });

  it("shows the same delete-only context menu for the edge label", async () => {
    const onDeleteSelection = vi.fn();
    const onSelectContextMenu = vi.fn();
    render(
      <svg>
        <WorkflowGraphEdge
          data={{
            contextMode: "compact_and_continue_session",
            entityID: "edge-1",
            entityKind: "edge",
            hasError: false,
            label: "Review",
            routePoints: [
              { x: 0, y: 0 },
              { x: 100, y: 0 },
            ],
            transitionGroupID: "tg-1",
          }}
          id="edge-1"
          onDeleteSelection={onDeleteSelection}
          onInspect={() => undefined}
          onSelectContextMenu={onSelectContextMenu}
          source="node-1"
          sourceX={0}
          sourceY={0}
          target="done"
          targetX={100}
          targetY={0}
          type="workflow"
        />
      </svg>,
    );

    fireEvent.contextMenu(screen.getByTestId("workflow-edge-label-edge-1"));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete branch" }));

    expect(onSelectContextMenu).toHaveBeenCalledExactlyOnceWith("edge-1");
    expect(onDeleteSelection).toHaveBeenCalledExactlyOnceWith({ edgeID: "edge-1", kind: "edge" });
    expect(screen.queryByRole("menuitem", { name: "Delete node" })).not.toBeInTheDocument();
  });
});
