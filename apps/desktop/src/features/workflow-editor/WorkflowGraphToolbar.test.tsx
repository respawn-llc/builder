import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, vi } from "vitest";

import { initializeI18n } from "@/i18n";
import { WorkflowGraphCanvas } from "./WorkflowGraphCanvas";

void initializeI18n();

describe("WorkflowGraphToolbar", () => {
  const originalResizeObserver = globalThis.ResizeObserver;

  beforeEach(() => {
    globalThis.ResizeObserver = MockResizeObserver;
  });

  afterEach(() => {
    globalThis.ResizeObserver = originalResizeObserver;
    vi.restoreAllMocks();
  });

  it("shows explicit hover tooltips for each toolbar action", async () => {
    const user = userEvent.setup();
    render(
      <WorkflowGraphCanvas
        graph={{ edges: [], nodes: [] }}
        onAddNode={() => undefined}
        onEdgeInspect={() => undefined}
        onGroupInspect={() => undefined}
        onNodeInspect={() => undefined}
        onWorkflowInspect={() => undefined}
      />,
    );

    const toolbar = within(screen.getByTestId("workflow-editor-tools"));
    expect(
      toolbar
        .getAllByRole("button")
        .map((button) => [button.getAttribute("aria-label"), button.getAttribute("title")]),
    ).toEqual([
      ["Add node", "Add node"],
      ["Inspect workflow", null],
      ["Actual size", null],
      ["Zoom in", null],
      ["Zoom out", null],
      ["Fit view", null],
    ]);

    const addNode = toolbar.getByRole("button", { name: "Add node" });
    await user.click(addNode);
    expect(await screen.findByRole("button", { name: "Agent node" })).toBeInTheDocument();
    await user.click(addNode);
    expect(screen.getByRole("button", { name: "Agent node" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Agent node" }));
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "Agent node" })).not.toBeInTheDocument();
    });

    await user.hover(toolbar.getByRole("button", { name: "Zoom in" }));
    await screen.findByRole("tooltip");
    expect(screen.queryByTestId("tooltip-arrow")).not.toBeInTheDocument();

    fireEvent.pointerEnter(addNode);
    expect(await screen.findByRole("button", { name: "Agent node" })).toBeInTheDocument();
    fireEvent.pointerLeave(addNode);
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "Agent node" })).not.toBeInTheDocument();
    });
    await new Promise((resolve) => setTimeout(resolve, 180));
    expect(screen.queryByRole("button", { name: "Agent node" })).not.toBeInTheDocument();

    fireEvent.pointerEnter(addNode);
    expect(await screen.findByRole("button", { name: "Agent node" })).toBeInTheDocument();
    const addNodeMenu = screen.getByRole("dialog");
    fireEvent.pointerLeave(addNode);
    fireEvent.pointerEnter(addNodeMenu);
    await new Promise((resolve) => setTimeout(resolve, 180));
    expect(screen.getByRole("button", { name: "Agent node" })).toBeInTheDocument();
    fireEvent.pointerLeave(addNodeMenu);
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "Agent node" })).not.toBeInTheDocument();
    });
    await new Promise((resolve) => setTimeout(resolve, 180));
    expect(screen.queryByRole("button", { name: "Agent node" })).not.toBeInTheDocument();

    fireEvent.blur(addNode, { relatedTarget: document.body });
    fireEvent.focus(addNode);
    expect(await screen.findByRole("button", { name: "Agent node" })).toBeInTheDocument();
    fireEvent.blur(addNode, { relatedTarget: document.body });
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "Agent node" })).not.toBeInTheDocument();
    });
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
