import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { z } from "zod";

import { App } from "../../App";
import { createTestServices, startupRoutes } from "../../testSupport/appServices";

describe("BoardRoute", () => {
  it("retries a start with the server-issued execution-target selection", async () => {
    window.history.pushState(null, "", "/projects/project-1?workflowId=workflow-1");
    const services = createBoardServices((callIndex) =>
      callIndex === 0 ? selectionRequiredResult : startedResult,
    );

    render(<App services={services} />);

    const card = await screen.findByRole("article", { name: "Resolve blocker" });
    const implementation = screen.getByRole("listitem", { name: "Implement" });
    const dataTransfer = dragDataTransfer();
    fireEvent.dragStart(card, { dataTransfer });
    fireEvent.drop(implementation, { dataTransfer });

    const dialog = await screen.findByRole("dialog", { name: "Choose execution target" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Continue" }));

    await waitFor(() => {
      const startCalls = services.transport.calls.filter((call) => call.method === "workflow.task.start");
      expect(startCalls).toHaveLength(2);
      expect(startCalls[1]?.params).toMatchObject({
        task_id: "task-1",
        selection_generation: "generation-1",
        selection: { mode: "none" },
      });
    });
  });

  it("does not retry or report a move when target materialization is in progress", async () => {
    window.history.pushState(null, "", "/projects/project-1?workflowId=workflow-1");
    const services = createBoardServices(() => inProgressResult);

    render(<App services={services} />);

    const card = await screen.findByRole("article", { name: "Resolve blocker" });
    const implementation = screen.getByRole("listitem", { name: "Implement" });
    const dataTransfer = dragDataTransfer();
    fireEvent.dragStart(card, { dataTransfer });
    fireEvent.drop(implementation, { dataTransfer });

    await waitFor(() => {
      expect(services.transport.calls.filter((call) => call.method === "workflow.task.start")).toHaveLength(1);
    });
    expect(screen.queryByRole("dialog", { name: "Choose execution target" })).not.toBeInTheDocument();
    expect(screen.getByRole("article", { name: "Resolve blocker" })).toBeInTheDocument();
  });

  it("does not retry a start when execution-target selection is dismissed", async () => {
    window.history.pushState(null, "", "/projects/project-1?workflowId=workflow-1");
    const services = createBoardServices(() => selectionRequiredResult);

    render(<App services={services} />);

    const card = await screen.findByRole("article", { name: "Resolve blocker" });
    const implementation = screen.getByRole("listitem", { name: "Implement" });
    const dataTransfer = dragDataTransfer();
    fireEvent.dragStart(card, { dataTransfer });
    fireEvent.drop(implementation, { dataTransfer });

    const dialog = await screen.findByRole("dialog", { name: "Choose execution target" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

    await waitFor(() => {
      expect(services.transport.calls.filter((call) => call.method === "workflow.task.start")).toHaveLength(1);
    });
  });

  it("retries the original manual move with the server-issued execution-target selection", async () => {
    window.history.pushState(null, "", "/projects/project-1?workflowId=workflow-1");
    const services = createBoardServices((callIndex) =>
      callIndex === 0 ? selectionRequiredResult : movedResult,
    );

    render(<App services={services} />);

    const card = await screen.findByRole("article", { name: "Active work" });
    const review = screen.getByRole("listitem", { name: "Review" });
    const dataTransfer = dragDataTransfer();
    fireEvent.dragStart(card, { dataTransfer });
    fireEvent.drop(review, { dataTransfer });

    fireEvent.click(await screen.findByRole("button", { name: "Continue" }));

    await waitFor(() => {
      const moveCalls = services.transport.calls.filter((call) => call.method === "workflow.task.move");
      expect(moveCalls).toHaveLength(2);
      expect(moveCalls[1]?.params).toMatchObject({
        task_id: "task-2",
        target_node_id: "node-2",
        selection_generation: "generation-1",
        selection: { mode: "none" },
      });
    });
  });
});

function createBoardServices(taskStart: (callIndex: number) => unknown) {
  return createTestServices([
    ...startupRoutes,
    { method: "workflow.board.get", result: boardResult },
    {
      method: "workflow.board.nodeCards.list",
      handler(params) {
        const nodeID = nodeIDFromParams(params);
        return {
          project_id: "project-1",
          workflow_id: "workflow-1",
          node_id: nodeID,
          cards: nodeID === "backlog" ? [backlogCard] : nodeID === "node-1" ? [activeCard] : [],
          next_page_token: "",
          generated_at_unix_ms: 1,
        };
      },
    },
    { method: "workflow.task.start", handler(_params, callIndex) { return taskStart(callIndex); } },
    { method: "workflow.task.move", handler(_params, callIndex) { return taskStart(callIndex); } },
  ]);
}

function nodeIDFromParams(value: unknown): string {
  return z.object({ node_id: z.string() }).parse(value).node_id;
}

function dragDataTransfer() {
  const values = new Map<string, string>();
  return {
    dropEffect: "none",
    effectAllowed: "all",
    files: [],
    items: [],
    types: [],
    clearData(format?: string) {
      if (format === undefined) {
        values.clear();
        return;
      }
      values.delete(format);
    },
    getData(format: string) {
      return values.get(format) ?? "";
    },
    setData(format: string, value: string) {
      values.set(format, value);
    },
    setDragImage() {
      return undefined;
    },
  };
}

const workflow = {
  workflow_id: "workflow-1",
  display_name: "Delivery",
  description: "",
  version: 1,
  is_project_default: true,
  valid_for_task_creation: true,
  validation_errors: [],
};

const sourceWorkspace = {
  workspace_id: "workspace-1",
  display_name: "Main",
  root_path: "/tmp/project",
  availability: "available",
  is_primary: true,
  updated_at_unix_ms: 1,
};

const boardResult = {
  board: {
    project_id: "project-1",
    project: { project_key: "PROJ", display_name: "Project" },
    selected_workflow: workflow,
    workflows: [workflow],
    groups: [],
    columns: [
      boardColumn({ id: "backlog", name: "Backlog", kind: "start", isBacklog: true, isDone: false, taskCount: 1 }),
      boardColumn({ id: "node-1", name: "Implement", kind: "agent", isBacklog: false, isDone: false, taskCount: 1 }),
      boardColumn({ id: "node-2", name: "Review", kind: "agent", isBacklog: false, isDone: false, taskCount: 0 }),
      boardColumn({ id: "done", name: "Done", kind: "terminal", isBacklog: false, isDone: true, taskCount: 0 }),
    ],
    generated_at_unix_ms: 1,
  },
};

const backlogCard = {
  task_id: "task-1",
  short_id: "T-1",
  title: "Resolve blocker",
  body_preview: "",
  workflow_id: "workflow-1",
  active_node_ids: [],
  source_workspace: sourceWorkspace,
  status: {
    kind: "backlog",
    label: "Backlog",
    native_state: "backlog",
    node_ids: [],
    run_ids: [],
    attention_types: [],
  },
  actions: {
    can_start: true,
    can_interrupt: false,
    can_resume: false,
    can_cancel: true,
    manual_move_target_node_ids: [],
  },
  updated_at_unix_ms: 1,
};

const activeCard = {
  ...backlogCard,
  task_id: "task-2",
  short_id: "T-2",
  title: "Active work",
  active_node_ids: ["node-1"],
  status: {
    kind: "running",
    label: "Running",
    native_state: "running",
    node_ids: ["node-1"],
    run_ids: [],
    attention_types: [],
  },
  actions: {
    can_start: false,
    can_interrupt: false,
    can_resume: false,
    can_cancel: true,
    manual_move_target_node_ids: ["node-2"],
  },
};

function boardColumn({
  id,
  name,
  kind,
  isBacklog,
  isDone,
  taskCount,
}: Readonly<{
  id: string;
  name: string;
  kind: string;
  isBacklog: boolean;
  isDone: boolean;
  taskCount: number;
}>) {
  return {
    node: {
      node_id: id,
      key: id,
      kind,
      display_name: name,
      assignee_role: "",
      output_fields: [],
      transition_output_fields: [],
    },
    group_id: "",
    sort_order: isBacklog ? 0 : isDone ? 2 : 1,
    is_backlog: isBacklog,
    is_done: isDone,
    task_count: taskCount,
  };
}

const selectionRequiredResult = {
  outcome: "selection_required",
  selection_required: {
    task_id: "task-1",
    generation: "generation-1",
    source_workspace_id: "workspace-1",
    source: { kind: "named_ref", named_ref: "refs/heads/main", commit: "abc123" },
    supported_selections: ["none", "head"],
    configured_policy: { mode: "ask" },
  },
};

const startedResult = {
  outcome: "started",
  started: {
    transition_id: "transition-1",
    placement_id: "placement-1",
    run_id: "run-1",
  },
};

const movedResult = {
  outcome: "moved",
  moved: {
    transition_id: "transition-1",
    state: "moved",
    placement_ids: ["placement-1"],
    run_ids: [],
  },
};

const inProgressResult = {
  outcome: "in_progress",
  in_progress: { task_id: "task-1", phase: "materializing" },
};
