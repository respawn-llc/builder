import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";

import type { JsonValue } from "../../api/json";
import { App } from "../../App";
import { createTestServices, startupRoutes } from "../../testSupport/appServices";
import {
  dispatchBoardDrag,
  FakeAnimationFrames,
  setScrollportGeometry,
  TestDataTransfer,
} from "../../testSupport/boardDrag";
import { boardCardDragPayloadType } from "./BoardDragTypes";

describe("BoardRoute native drag lifecycle", () => {
  let frames: FakeAnimationFrames;

  beforeEach(() => {
    frames = new FakeAnimationFrames();
    vi.stubGlobal("requestAnimationFrame", frames.request);
    vi.stubGlobal("cancelAnimationFrame", frames.cancel);
    window.history.pushState(null, "KENT-230", "/projects/project-1?workflowId=workflow-1");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("does not reconstruct an action payload from DataTransfer when no card drag is active", async () => {
    const services = boardTestServices();
    render(<App services={services} />);

    const destination = await screen.findByRole("listitem", { name: "Recon" });
    const dataTransfer = new TestDataTransfer();
    dataTransfer.setData(
      boardCardDragPayloadType,
      JSON.stringify({
        taskID: "task-1",
        canStart: false,
        activeNodeIDs: ["backlog"],
        statusKind: "backlog",
        manualMoveTargetNodeIDs: ["recon"],
      }),
    );

    fireEvent.drop(destination, { dataTransfer });

    await waitFor(() => {
      expect(taskActionCalls(services.transport.calls)).toEqual([]);
    });
  });

  it("activates from a real card and keeps both scrollports moving through an in-board null-target dragleave", async () => {
    const { source, root, backlogScrollport: column, dataTransfer } = await renderActiveBoard(frames);
    setScrollportGeometry(root, {
      left: 0,
      top: 0,
      width: 500,
      height: 400,
      scrollWidth: 1_000,
      scrollHeight: 400,
    });
    setScrollportGeometry(column, {
      left: 300,
      top: 0,
      width: 180,
      height: 400,
      scrollWidth: 180,
      scrollHeight: 1_000,
    });

    dispatchBoardDrag(source, "dragover", { clientX: 476, clientY: 396, dataTransfer });
    expect(frames.pending).toBe(0);

    fireEvent.dragStart(source, { dataTransfer });
    dispatchBoardDrag(source, "dragover", { clientX: 476, clientY: 396, dataTransfer });
    expect(frames.pending).toBe(1);

    dispatchBoardDrag(source, "dragleave", {
      clientX: 476,
      clientY: 396,
      dataTransfer,
      relatedTarget: null,
    });
    expect(frames.pending).toBe(1);

    frames.step(0);
    frames.step(16);

    expect(root.scrollLeft).toBeGreaterThan(0);
    expect(column.scrollTop).toBeGreaterThan(0);
  });

  it("pauses outside the board, resumes on re-entry, and then submits one valid drop", async () => {
    const { services, source, root, backlogScrollport, recon, dataTransfer } =
      await renderActiveBoard(frames);
    setScrollportGeometry(root, {
      left: 0,
      top: 0,
      width: 500,
      height: 400,
      scrollWidth: 1_000,
      scrollHeight: 400,
    });
    setScrollportGeometry(backlogScrollport, {
      left: 300,
      top: 0,
      width: 180,
      height: 400,
      scrollWidth: 180,
      scrollHeight: 1_000,
    });

    fireEvent.dragStart(source, { dataTransfer });
    dispatchBoardDrag(source, "dragover", { clientX: 476, clientY: 396, dataTransfer });
    frames.step(0);
    frames.step(16);
    const scrollLeftBeforePause = root.scrollLeft;
    expect(scrollLeftBeforePause).toBeGreaterThan(0);

    dispatchBoardDrag(document.body, "dragover", { clientX: 600, clientY: 200, dataTransfer });
    expect(frames.pending).toBe(0);

    dispatchBoardDrag(source, "dragover", { clientX: 476, clientY: 396, dataTransfer });
    expect(frames.pending).toBe(1);
    frames.step(32);
    frames.step(48);
    expect(root.scrollLeft).toBeGreaterThan(scrollLeftBeforePause);

    fireEvent.drop(recon, { dataTransfer });

    await waitFor(() => {
      const actionCalls = taskActionCalls(services.transport.calls);
      expect(actionCalls).toHaveLength(1);
      expect(actionCalls[0]).toMatchObject({
        method: "workflow.task.move",
        params: {
          target_node_id: "recon",
          task_id: "task-1",
        },
      });
    });

    fireEvent.drop(recon, { dataTransfer });
    await waitFor(() => {
      expect(taskActionCalls(services.transport.calls)).toHaveLength(1);
    });
  });

  it("switches vertical targets and preserves horizontal motion outside all columns", async () => {
    const { source, root, backlogScrollport, reconScrollport, dataTransfer } =
      await renderActiveBoard(frames);
    setScrollportGeometry(root, {
      left: 0,
      top: 0,
      width: 500,
      height: 400,
      scrollWidth: 1_000,
      scrollHeight: 400,
    });
    setScrollportGeometry(backlogScrollport, {
      left: 0,
      top: 0,
      width: 180,
      height: 400,
      scrollWidth: 180,
      scrollHeight: 1_000,
    });
    setScrollportGeometry(reconScrollport, {
      left: 200,
      top: 0,
      width: 180,
      height: 400,
      scrollWidth: 180,
      scrollHeight: 1_000,
    });

    backlogScrollport.scrollTop = 300;
    fireEvent.dragStart(source, { dataTransfer });
    dispatchBoardDrag(source, "dragover", { clientX: 100, clientY: 4, dataTransfer });
    frames.step(0);
    frames.step(16);
    expect(backlogScrollport.scrollTop).toBeLessThan(300);
    expect(reconScrollport.scrollTop).toBe(0);

    dispatchBoardDrag(source, "dragover", { clientX: 300, clientY: 396, dataTransfer });
    frames.step(32);
    expect(reconScrollport.scrollTop).toBeGreaterThan(0);
    const reconScrollTopOutside = reconScrollport.scrollTop;

    dispatchBoardDrag(root, "dragover", { clientX: 496, clientY: 200, dataTransfer });
    frames.step(48);
    expect(root.scrollLeft).toBeGreaterThan(0);
    expect(reconScrollport.scrollTop).toBe(reconScrollTopOutside);
  });

  it.each([
    [
      "outside drop",
      ({ dataTransfer }: ActiveBoardTestContext) => {
        fireEvent.drop(document.body, { dataTransfer });
      },
    ],
    [
      "card dragend",
      ({ source, dataTransfer }: ActiveBoardTestContext) => {
        fireEvent.dragEnd(source, { dataTransfer });
      },
    ],
    [
      "document dragend",
      ({ dataTransfer }: ActiveBoardTestContext) => {
        fireEvent.dragEnd(document, { dataTransfer });
      },
    ],
    [
      "duplicate cancellation notifications",
      ({ source, dataTransfer }: ActiveBoardTestContext) => {
        fireEvent.dragEnd(source, { dataTransfer });
        fireEvent.dragEnd(document, { dataTransfer });
      },
    ],
    [
      "window blur",
      () => {
        fireEvent(window, new Event("blur"));
      },
    ],
  ] as const)("clears active drag without a task action after %s", async (_reason, terminate) => {
    const view = await renderActiveBoard(frames);
    setScrollportGeometry(view.root, {
      left: 0,
      top: 0,
      width: 500,
      height: 400,
      scrollWidth: 1_000,
      scrollHeight: 400,
    });

    fireEvent.dragStart(view.source, { dataTransfer: view.dataTransfer });
    dispatchBoardDrag(view.source, "dragover", {
      clientX: 496,
      clientY: 200,
      dataTransfer: view.dataTransfer,
    });
    expect(frames.pending).toBe(1);

    terminate(view);
    expect(frames.pending).toBe(0);

    dispatchBoardDrag(view.source, "dragover", {
      clientX: 496,
      clientY: 200,
      dataTransfer: view.dataTransfer,
    });
    expect(frames.pending).toBe(0);
    expect(taskActionCalls(view.services.transport.calls)).toEqual([]);

    view.removeSource();
    await waitFor(() => {
      expect(screen.queryByRole("article", { name: "Drag source" })).not.toBeInTheDocument();
    });
  });
});

type ActiveBoardTestContext = Awaited<ReturnType<typeof renderActiveBoard>>;

async function renderActiveBoard(frames: FakeAnimationFrames) {
  let sourcePresent = true;
  const services = boardTestServices(() => sourcePresent);
  render(<App services={services} />);
  const root = await screen.findByTestId("board-scrollport");
  const source = await within(root).findByRole("article", { name: "Drag source" });
  frames.step(0);
  return {
    services,
    source,
    root,
    backlogScrollport: screen.getByTestId("kanban-column-scroll-backlog"),
    recon: screen.getByRole("listitem", { name: "Recon" }),
    reconScrollport: screen.getByTestId("kanban-column-scroll-recon"),
    dataTransfer: new TestDataTransfer(),
    removeSource: () => {
      sourcePresent = false;
      services.transport.emit("workflow.project", {
        event: {
          action: "updated",
          changed_ids: ["task-1"],
          project_id: "project-1",
          resource: "task",
          workflow_id: "workflow-1",
        },
      });
    },
  };
}

function boardTestServices(sourcePresent: () => boolean = () => true) {
  return createTestServices([
    ...startupRoutes,
    {
      method: "workflow.board.get",
      handler: () => boardResponse(sourcePresent() ? 1 : 0),
    },
    {
      method: "workflow.board.nodeCards.list",
      handler: (params: JsonValue) =>
        nodeCardsResponse(nodeID(params), sourcePresent() && nodeID(params) === "backlog"),
    },
    {
      method: "workflow.task.move",
      result: {
        outcome: "applied",
        applied: {
          transition_id: "transition-1",
          state: "approved",
          placement_ids: ["placement-1"],
          run_ids: [],
        },
      },
    },
  ]);
}

function taskActionCalls(
  calls: Readonly<{ method: string; params: JsonValue }>[],
): Readonly<{ method: string; params: JsonValue }>[] {
  return calls.filter(
    (call) => call.method === "workflow.task.start" || call.method === "workflow.task.move",
  );
}

function nodeID(params: JsonValue): string {
  return nodeCardsRequestSchema.parse(params).node_id;
}

function nodeCardsResponse(nodeIDValue: string, includeSource: boolean) {
  return {
    project_id: "project-1",
    workflow_id: "workflow-1",
    node_id: nodeIDValue,
    cards: includeSource ? [sourceCard] : [],
    previous_page_token: null,
    next_page_token: null,
    generated_at_unix_ms: 1,
  };
}

const boardGroupID = "group-qa";

function boardResponse(backlogTaskCount: number) {
  return {
    board: {
      project_id: "project-1",
      project: {
        project_key: "KNT",
        display_name: "Kent",
        default_workspace_id: "workspace-1",
        attached_workspace_count: 1,
      },
      selected_workflow: {
        workflow_id: "workflow-1",
        display_name: "Workflow",
        description: "Board drag QA workflow",
        version: 1,
        is_project_default: true,
        valid_for_task_creation: true,
        validation_errors: [],
      },
      workflows: [
        {
          workflow_id: "workflow-1",
          display_name: "Workflow",
          description: "Board drag QA workflow",
          version: 1,
          is_project_default: true,
          valid_for_task_creation: true,
          validation_errors: [],
        },
      ],
      groups: [
        {
          group_id: boardGroupID,
          key: "qa",
          display_name: "QA",
          sort_order: 0,
          node_ids: ["backlog", "recon"],
        },
      ],
      columns: [
        boardColumn({
          id: "backlog",
          kind: "backlog",
          name: "Backlog",
          sortOrder: 0,
          isBacklog: true,
          taskCount: backlogTaskCount,
        }),
        boardColumn({
          id: "recon",
          kind: "agent",
          name: "Recon",
          sortOrder: 1,
          isBacklog: false,
          taskCount: 0,
        }),
      ],
      generated_at_unix_ms: 1,
    },
  };
}

function boardColumn(
  input: Readonly<{
    id: string;
    kind: string;
    name: string;
    sortOrder: number;
    isBacklog: boolean;
    taskCount: number;
  }>,
) {
  return {
    node: {
      node_id: input.id,
      key: input.id,
      kind: input.kind,
      display_name: input.name,
      assignee_role: "qa",
      output_fields: [],
      transition_output_fields: [],
    },
    group_id: boardGroupID,
    sort_order: input.sortOrder,
    is_backlog: input.isBacklog,
    is_done: false,
    task_count: input.taskCount,
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
    node_ids: [],
    run_ids: [],
    attention_types: [],
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

const nodeCardsRequestSchema = z.object({ node_id: z.string() });
