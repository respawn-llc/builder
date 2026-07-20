import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";

import type { JsonValue } from "@/api";
import { createTestServices, startupRoutes } from "@/test-support/app-services";
import {
  dispatchBoardDrag,
  FakeAnimationFrames,
  setScrollportGeometry,
  TestDataTransfer,
} from "@/test-support/board-drag";
import { App } from "../startup/App";

let frames: FakeAnimationFrames;
let state = { sourcePresent: true, workflowValid: true };
const forgedDragPayload = {
  taskID: "task-1",
  canStart: true,
  activeNodeIDs: ["backlog"],
  statusKind: "backlog",
  manualMoveTargetNodeIDs: ["recon"],
};
const boardChange = {
  event: { action: "updated", occurred_at_unix_ms: 1, resource: "task" },
};
const taskMoveResult = {
  outcome: "applied",
  applied: { transition_id: "t", state: "applied", placement_ids: [], run_ids: [] },
};

describe("BoardRoute native drag lifecycle", () => {
  beforeEach(() => {
    frames = new FakeAnimationFrames();
    state = { sourcePresent: true, workflowValid: true };
    vi.stubGlobal("requestAnimationFrame", frames.request);
    vi.stubGlobal("cancelAnimationFrame", frames.cancel);
    window.history.pushState(null, "KENT-230", "/projects/project-1?workflowId=workflow-1");
  });

  afterEach(vi.unstubAllGlobals);

  it("rejects forged payloads and closes an active drag when the workflow becomes invalid", async () => {
    const view = await renderBoard();
    view.dataTransfer.setData("application/json", JSON.stringify(forgedDragPayload));
    fireEvent.drop(view.recon, transfer(view));

    startEdgeScroll(view, 196, 200);
    expect(view.recon).toHaveAttribute("data-drop-state", "allowed");

    state.workflowValid = false;
    emitBoardChange(view.services);
    await waitFor(() => {
      expect(view.source).toHaveAttribute("draggable", "false");
      expect(view.recon).toHaveAttribute("data-drop-state", "idle");
      expect(frames.pending).toBe(0);
    });

    dragOver(view, view.source, 196, 200);
    expect(frames.pending).toBe(0);
    fireEvent.drop(view.recon, { dataTransfer: view.dataTransfer });
    expect(taskActionCalls(view.services.transport.calls)).toEqual([]);
  });

  it("activates both scrollports and survives an in-board null-target dragleave", async () => {
    const view = await renderBoard();

    dragOver(view, view.source, 176, 396);
    expect(frames.pending).toBe(0);
    startEdgeScroll(view, 176, 396);
    dispatchBoardDrag(view.source, "dragleave", {
      clientX: 176,
      clientY: 396,
      dataTransfer: view.dataTransfer,
      relatedTarget: null,
    });
    frames.step(0);
    frames.step(16);
    expect(view.root.scrollLeft).toBeGreaterThan(0);
    expect(view.backlogScrollport.scrollTop).toBeGreaterThan(0);
  });

  it("pauses outside, resumes on re-entry, and submits one valid drop", async () => {
    const view = await renderBoard();
    startEdgeScroll(view, 176, 396);
    frames.step(0);
    frames.step(16);
    const pausedAt = view.root.scrollLeft;
    expect(pausedAt).toBeGreaterThan(0);

    dragOver(view, document.body, 600, 200);
    expect(frames.pending).toBe(0);
    dragOver(view, view.source, 176, 396);
    frames.step(32);
    frames.step(48);
    expect(view.root.scrollLeft).toBeGreaterThan(pausedAt);

    fireEvent.drop(view.recon, { dataTransfer: view.dataTransfer });
    expect(taskActionCalls(view.services.transport.calls)).toMatchObject([
      { method: "workflow.task.move", params: { target_node_id: "recon", task_id: "task-1" } },
    ]);
    fireEvent.drop(view.recon, { dataTransfer: view.dataTransfer });
    expect(taskActionCalls(view.services.transport.calls)).toHaveLength(1);
  });

  it("switches vertical targets and preserves horizontal motion outside columns", async () => {
    const view = await renderBoard();
    view.backlogScrollport.scrollTop = 300;
    startEdgeScroll(view, 100, 4);
    frames.step(0);
    frames.step(16);
    expect(view.backlogScrollport.scrollTop).toBeLessThan(300);

    dragOver(view, view.source, 300, 396);
    frames.step(32);
    const reconAt = view.reconScrollport.scrollTop;
    expect(reconAt).toBeGreaterThan(0);
    dragOver(view, view.root, 196, 200);
    frames.step(48);
    expect(view.root.scrollLeft).toBeGreaterThan(0);
    expect(view.reconScrollport.scrollTop).toBe(reconAt);
  });

  it.each([
    ["outside drop", (view: BoardView) => fireEvent.drop(document.body, transfer(view))],
    ["card dragend", (view: BoardView) => fireEvent.dragEnd(view.source, transfer(view))],
    ["document dragend", (view: BoardView) => fireEvent.dragEnd(document, transfer(view))],
    ["window blur", () => fireEvent(window, new Event("blur"))],
  ] as const)("clears drag state and evicts its source after %s", async (_reason, terminate) => {
    const view = await renderBoard();
    startEdgeScroll(view, 196, 200);

    terminate(view);
    terminate(view);
    expect(frames.pending).toBe(0);
    expect(taskActionCalls(view.services.transport.calls)).toEqual([]);

    state.sourcePresent = false;
    emitBoardChange(view.services);
    await waitFor(() => {
      expect(screen.queryAllByRole("article", { name: "Drag source" })).toHaveLength(0);
    });
  });
});

type BoardView = Awaited<ReturnType<typeof renderBoard>>;

async function renderBoard() {
  const services = boardTestServices();
  render(<App services={services} />);
  const root = await screen.findByTestId("board-scrollport");
  const source = await within(root).findByRole("article", { name: "Drag source" });
  const backlogScrollport = screen.getByTestId("kanban-column-scroll-backlog");
  const reconScrollport = screen.getByTestId("kanban-column-scroll-recon");
  setGeometry(root, 0, false);
  setGeometry(backlogScrollport, 0, true);
  setGeometry(reconScrollport, 200, true);
  frames.step(0);
  return {
    services,
    source,
    root,
    backlogScrollport,
    recon: screen.getByRole("listitem", { name: "Recon" }),
    reconScrollport,
    dataTransfer: new TestDataTransfer(),
  };
}

function boardTestServices() {
  return createTestServices([
    ...startupRoutes,
    { method: "workflow.board.get", handler: boardResponse },
    { method: "workflow.board.nodeCards.list", handler: nodeCardsResponse },
    { method: "workflow.task.move", result: taskMoveResult },
  ]);
}

function boardResponse() {
  const selectedWorkflow = {
    workflow_id: "workflow-1",
    display_name: "Workflow",
    version: 1,
    is_project_default: true,
    valid_for_task_creation: state.workflowValid,
  };
  return {
    board: {
      project_id: "project-1",
      project: {
        project_key: "KNT",
        display_name: "Kent",
        default_workspace_id: "workspace-1",
        attached_workspace_count: 1,
      },
      selected_workflow: selectedWorkflow,
      columns: [
        boardColumn("backlog", "backlog", true, state.sourcePresent ? 1 : 0),
        boardColumn("recon", "agent", false, 0),
      ],
      generated_at_unix_ms: 1,
    },
  };
}

function boardColumn(id: string, kind: string, isBacklog: boolean, taskCount: number) {
  return {
    node: {
      node_id: id,
      key: id,
      kind,
      display_name: isBacklog ? "Backlog" : "Recon",
    },
    group_id: "group-qa",
    sort_order: isBacklog ? 0 : 1,
    is_backlog: isBacklog,
    is_done: false,
    task_count: taskCount,
  };
}

function nodeCardsResponse(params: JsonValue) {
  const nodeID = nodeCardsRequestSchema.parse(params).node_id;
  return {
    project_id: "project-1",
    workflow_id: "workflow-1",
    node_id: nodeID,
    cards: state.sourcePresent && nodeID === "backlog" ? [sourceCard] : [],
    previous_page_token: null,
    next_page_token: null,
    generated_at_unix_ms: 1,
  };
}

const sourceCard = {
  task_id: "task-1",
  short_id: "KNT-1",
  title: "Drag source",
  preview: { markdown: "Source preview", truncated: false },
  workflow_id: "workflow-1",
  active_node_ids: ["backlog"],
  source_workspace: {
    workspace_id: "workspace-1",
    display_name: "Main",
    root_path: "/workspace/main",
    availability: "available",
    is_primary: true,
    updated_at_unix_ms: 1,
  },
  status: {
    kind: "backlog",
    native_state: "backlog",
  },
  actions: {
    can_start: false,
    can_interrupt: false,
    can_resume: false,
    can_cancel: true,
    manual_move_target_node_ids: ["recon"],
  },
  updated_at_unix_ms: 1,
};

function dragOver(view: BoardView, target: EventTarget, clientX: number, clientY: number) {
  dispatchBoardDrag(target, "dragover", { clientX, clientY, dataTransfer: view.dataTransfer });
}

function startEdgeScroll(view: BoardView, clientX: number, clientY: number) {
  fireEvent.dragStart(view.source, transfer(view));
  dragOver(view, view.source, clientX, clientY);
  expect(frames.pending).toBe(1);
}

const transfer = (view: BoardView) => ({ dataTransfer: view.dataTransfer });

function setGeometry(element: HTMLElement, left: number, vertical: boolean) {
  setScrollportGeometry(element, {
    left,
    top: 0,
    width: vertical ? 180 : 200,
    height: 400,
    scrollWidth: vertical ? 180 : 1_000,
    scrollHeight: vertical ? 1_000 : 400,
  });
}

function emitBoardChange(services: ReturnType<typeof boardTestServices>) {
  services.transport.emit("workflow.project", boardChange);
}

function taskActionCalls(calls: Readonly<{ method: string; params: JsonValue }>[]) {
  return calls.filter(({ method }) => method === "workflow.task.start" || method === "workflow.task.move");
}

const nodeCardsRequestSchema = z.object({ node_id: z.string() });
