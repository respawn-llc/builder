import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { App } from "../startup/App";
import { appI18n, initializeI18n } from "@/i18n";
import { createTestServices, startupRoutes } from "@/test-support/app-services";
import { installTestStorage } from "@/test-support/storage";
import { showStatusToast } from "@/ui";
import type * as UiModule from "@/ui";

vi.mock("@/ui", async (importOriginal) => ({
  ...(await importOriginal<typeof UiModule>()),
  showStatusToast: vi.fn(),
}));

void initializeI18n();

describe("BoardRoute live refresh", () => {
  beforeEach(() => {
    installTestStorage("localStorage");
    vi.mocked(showStatusToast).mockClear();
  });

  it("preserves an explicitly empty route selector at the transport boundary", async () => {
    window.history.pushState(null, "", "/projects/project-1?workflowId=");
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.board.get",
        result: boardWithoutWorkflowResponse,
      },
    ]);

    render(<App services={services} />);

    await waitFor(() => {
      expect(boardCalls(services)).toContainEqual({
        method: "workflow.board.get",
        params: {
          project_id: "project-1",
          workflow_id: "",
          label_filter: { kind: "none" },
        },
      });
    });
  });

  it("refreshes a board without a selected workflow after a workflow-link event", async () => {
    window.history.pushState(null, "", "/projects/project-1");
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.board.get",
        result: boardWithoutWorkflowResponse,
      },
    ]);

    render(<App services={services} />);

    await waitFor(() => {
      expect(boardCalls(services).length).toBe(1);
      expect(services.transport.subscriptions).toContainEqual({
        method: "workflow.subscribeProject",
        params: { project_id: "project-1" },
      });
    });
    expect(
      await screen.findByRole("button", { name: appI18n.t("workflowLibrary.linkWorkflow") }),
    ).toBeEnabled();
    expect(screen.getByRole("button", { name: appI18n.t("workflowLibrary.createWorkflow") })).toBeEnabled();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: appI18n.t("board.newTask") })).not.toBeInTheDocument();

    act(() => {
      services.transport.emit("workflow.project", {
        event: {
          action: "linked",
          occurred_at_unix_ms: 1,
          primary_entity_id: "workflow-link-1",
          project_id: "project-1",
          resource: "workflow_link",
          workflow_id: "workflow-1",
        },
      });
    });

    await waitFor(() => {
      expect(boardCalls(services).length).toBe(2);
    });
    expect(
      services.transport.calls.filter((call) => call.method === "workflow.board.nodeCards.list"),
    ).toEqual([]);
  });

  it("closes a deleted task route while workflow selection is still loading", async () => {
    window.history.pushState(null, "", "/projects/project-1?workflowId=workflow-1&taskId=task-1");
    const pendingBoard = new Promise<unknown>(() => {
      // Keep selection pending until the project event exercises route cleanup.
    });
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.board.get",
        result: pendingBoard,
      },
    ]);

    render(<App services={services} />);

    await waitFor(() => {
      expect(services.transport.subscriptions).toContainEqual({
        method: "workflow.subscribeProject",
        params: { project_id: "project-1" },
      });
    });

    act(() => {
      services.transport.emit("workflow.project", {
        event: {
          action: "deleted",
          occurred_at_unix_ms: 1,
          primary_entity_id: "task-1",
          project_id: "project-1",
          resource: "task",
          workflow_id: "workflow-1",
        },
      });
    });

    await waitFor(() => {
      const search = new URLSearchParams(window.location.search);
      expect(search.get("workflowId")).toBe("workflow-1");
      expect(search.get("taskId")).not.toBe("task-1");
    });
  });

  it("mounts board content only when the server selects a workflow", async () => {
    window.history.pushState(null, "", "/projects/project-1");
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.board.get",
        result: boardWithWorkflowResponse,
      },
    ]);

    render(<App services={services} />);

    expect(await screen.findByRole("list")).toBeInTheDocument();
    expect(screen.getByRole("navigation")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: appI18n.t("labels.filter") })).toBeEnabled();
    expect(screen.getByRole("button", { name: appI18n.t("board.newTask") })).toBeEnabled();
    expect(
      screen.queryByRole("button", { name: appI18n.t("workflowLibrary.createWorkflow") }),
    ).not.toBeInTheDocument();
  });

  it("reactively requests the persisted unlabeled expression from the board filter", async () => {
    window.history.pushState(null, "", "/projects/project-1?workflowId=workflow-1");
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.board.get",
        result: boardWithWorkflowResponse,
      },
    ]);

    render(<App services={services} />);

    fireEvent.click(await screen.findByRole("button", { name: appI18n.t("labels.filter") }));
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("labels.unlabeled") }));

    await waitFor(() => {
      expect(boardCalls(services).at(-1)?.params).toEqual({
        project_id: "project-1",
        workflow_id: "workflow-1",
        label_filter: { kind: "unlabeled" },
      });
    });
    expect(
      screen.getByRole("button", {
        expanded: true,
        name: appI18n.t("labels.unlabeled"),
      }),
    ).toBeEnabled();
    expect(screen.getByRole("button", { name: appI18n.t("labels.clearFilter") })).toBeEnabled();
  });

  it("keeps the current board visible without a loader while a replacement filter is pending", async () => {
    window.history.pushState(null, "", "/projects/project-1?workflowId=workflow-1");
    let resolveFilteredBoard: ((value: typeof boardWithWorkflowResponse) => void) | undefined;
    const filteredBoard = new Promise<typeof boardWithWorkflowResponse>((resolve) => {
      resolveFilteredBoard = resolve;
    });
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.board.get",
        handler: async (_params, callIndex) => {
          if (callIndex === 0) {
            return boardWithWorkflowResponse;
          }
          return filteredBoard;
        },
      },
    ]);

    render(<App services={services} />);

    fireEvent.click(await screen.findByRole("button", { name: appI18n.t("labels.filter") }));
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("labels.unlabeled") }));
    await waitFor(() => {
      expect(boardCalls(services)).toHaveLength(2);
    });

    expect(screen.getByTestId("board-scrollport")).toBeInTheDocument();
    expect(screen.queryByTestId("loading-state")).not.toBeInTheDocument();
    act(() => {
      resolveFilteredBoard?.(boardWithWorkflowResponse);
    });
    await waitFor(() => {
      expect(screen.getByTestId("board-scrollport")).toBeInTheDocument();
    });
  });

  it("keeps the current board visible and offers Retry after the newest filter fails", async () => {
    window.history.pushState(null, "", "/projects/project-1?workflowId=workflow-1");
    let rejectFilteredBoard: ((error: Error) => void) | undefined;
    const filteredBoard = new Promise<never>((_resolve, reject) => {
      rejectFilteredBoard = reject;
    });
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.board.get",
        handler: async (_params, callIndex) => {
          if (callIndex === 0) {
            return boardWithWorkflowResponse;
          }
          return filteredBoard;
        },
      },
    ]);

    render(<App services={services} />);

    fireEvent.click(await screen.findByRole("button", { name: appI18n.t("labels.filter") }));
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("labels.unlabeled") }));
    fireEvent.click(
      screen.getByRole("button", {
        expanded: true,
        name: appI18n.t("labels.unlabeled"),
      }),
    );
    await waitFor(() => {
      expect(boardCalls(services)).toHaveLength(2);
    });
    act(() => {
      rejectFilteredBoard?.(new Error("filtered board failed"));
    });

    await waitFor(
      () => {
        expect(boardCalls(services)).toHaveLength(3);
      },
      { timeout: 2_500 },
    );
    await waitFor(() => {
      expect(screen.getByTestId("board-scrollport")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: appI18n.t("app.retry") })).toBeEnabled();
    });
  });

  it("restores one Project filter across relaunch and another linked workflow", async () => {
    window.history.pushState(null, "", "/projects/project-1?workflowId=workflow-1");
    const firstServices = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.board.get",
        result: boardWithWorkflowResponse,
      },
    ]);
    const view = render(<App services={firstServices} />);

    fireEvent.click(await screen.findByRole("button", { name: appI18n.t("labels.filter") }));
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("labels.unlabeled") }));
    await waitFor(() => {
      expect(boardCalls(firstServices).at(-1)?.params).toMatchObject({
        label_filter: { kind: "unlabeled" },
      });
      expect(globalThis.localStorage.length).toBeGreaterThan(0);
    });
    view.unmount();

    window.history.pushState(null, "", "/projects/project-1?workflowId=workflow-2");
    const secondServices = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.board.get",
        result: boardWithWorkflowResponse,
      },
    ]);
    render(<App services={secondServices} />);

    await waitFor(() => {
      expect(boardCalls(secondServices)).toHaveLength(1);
    });
    expect(boardCalls(secondServices)[0]?.params).toEqual({
      project_id: "project-1",
      workflow_id: "workflow-2",
      label_filter: { kind: "unlabeled" },
    });
  });

  it("replaces displayed column counts with the active server-filtered counts", async () => {
    window.history.pushState(null, "", "/projects/project-1?workflowId=workflow-1");
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.board.get",
        handler: (_params, callIndex) =>
          callIndex === 0 ? boardWithBacklogCount(7) : boardWithBacklogCount(2),
      },
      {
        method: "workflow.board.nodeCards.list",
        result: emptyBacklogCards,
      },
    ]);

    render(<App services={services} />);

    expect(await screen.findByTestId("kanban-column-task-count-backlog")).toHaveTextContent("7");
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("labels.filter") }));
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("labels.unlabeled") }));

    await waitFor(() => {
      expect(screen.getByTestId("kanban-column-task-count-backlog")).toHaveTextContent("2");
    });
  });

  it("keeps filtering in memory and visibly reports a persistence failure", async () => {
    window.history.pushState(null, "", "/projects/project-1?workflowId=workflow-1");
    const storage = globalThis.localStorage;
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.board.get",
        result: boardWithWorkflowResponse,
      },
    ]);

    render(<App services={services} />);

    const filterButton = await screen.findByRole("button", { name: appI18n.t("labels.filter") });
    storage.setItem = () => {
      throw new Error("storage write failed");
    };
    fireEvent.click(filterButton);
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("labels.unlabeled") }));

    await waitFor(() => {
      expect(boardCalls(services).at(-1)?.params).toMatchObject({
        label_filter: { kind: "unlabeled" },
      });
    });
    await waitFor(() => {
      expect(showStatusToast).toHaveBeenCalledWith(
        expect.objectContaining({
          id: "board-load-error",
          title: appI18n.t("board.loadFailed"),
          tone: "danger",
        }),
      );
    });
  });

  it("updates informational card labels from the catalog and drops deleted IDs", async () => {
    window.history.pushState(null, "", "/projects/project-1?workflowId=workflow-1");
    const labelID = "38bf0da7-a3f7-4c15-bc5f-c8fca538e667";
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.project.label.list",
        handler: (_params, callIndex) => ({
          catalog: {
            project_id: "project-1",
            labels:
              callIndex === 0
                ? [{ id: labelID, name: "Alpha" }]
                : callIndex === 1
                  ? [{ id: labelID, name: "Renamed" }]
                  : [],
          },
        }),
      },
      {
        method: "workflow.board.get",
        result: boardWithBacklogCount(1),
      },
      {
        method: "workflow.board.nodeCards.list",
        result: labeledBacklogCards(labelID),
      },
    ]);

    render(<App services={services} />);

    const card = await screen.findByRole("article", { name: "Labeled task" });
    expect(within(card).getByRole("group", { name: appI18n.t("labels.filter") })).toHaveTextContent("Alpha");
    expect(nodeCardCalls(services)).toHaveLength(1);

    act(() => {
      services.transport.emit("workflow.project", {
        event: {
          action: "renamed",
          occurred_at_unix_ms: 1,
          primary_entity_id: labelID,
          project_id: "project-1",
          resource: "label",
        },
      });
    });
    await waitFor(() => {
      expect(within(card).getByRole("group", { name: appI18n.t("labels.filter") })).toHaveTextContent(
        "Renamed",
      );
    });
    expect(nodeCardCalls(services)).toHaveLength(1);

    act(() => {
      services.transport.emit("workflow.project", {
        event: {
          action: "deleted",
          occurred_at_unix_ms: 2,
          primary_entity_id: labelID,
          project_id: "project-1",
          resource: "label",
        },
      });
    });
    await waitFor(() => {
      expect(within(card).queryByRole("group", { name: appI18n.t("labels.filter") })).not.toBeInTheDocument();
    });
  });
});

function boardCalls(services: ReturnType<typeof createTestServices>) {
  return services.transport.calls.filter((call) => call.method === "workflow.board.get");
}

function nodeCardCalls(services: ReturnType<typeof createTestServices>) {
  return services.transport.calls.filter((call) => call.method === "workflow.board.nodeCards.list");
}

const boardWithoutWorkflowResponse = {
  board: {
    project_id: "project-1",
    project: {
      project_key: "PRO",
      display_name: "Project",
      default_workspace_id: "workspace-1",
      attached_workspace_count: 1,
    },
    workflows: [],
    groups: [],
    columns: [],
    generated_at_unix_ms: 1,
  },
};

const selectedWorkflow = {
  workflow_id: "workflow-1",
  display_name: "Workflow",
  description: "",
  version: 1,
  is_project_default: true,
  valid_for_task_creation: true,
  validation_errors: [],
};

const boardWithWorkflowResponse = {
  board: {
    ...boardWithoutWorkflowResponse.board,
    selected_workflow: selectedWorkflow,
    workflows: [selectedWorkflow],
  },
};

function boardWithBacklogCount(taskCount: number) {
  return {
    board: {
      ...boardWithWorkflowResponse.board,
      groups: [
        {
          group_id: "group-backlog",
          key: "backlog",
          display_name: "Backlog",
          sort_order: 0,
          node_ids: ["backlog"],
        },
      ],
      columns: [
        {
          node: {
            node_id: "backlog",
            key: "backlog",
            kind: "backlog",
            display_name: "Backlog",
            assignee_role: "",
            output_fields: [],
            transition_output_fields: [],
          },
          group_id: "group-backlog",
          sort_order: 0,
          is_backlog: true,
          is_done: false,
          task_count: taskCount,
        },
      ],
    },
  };
}

const emptyBacklogCards = {
  project_id: "project-1",
  workflow_id: "workflow-1",
  node_id: "backlog",
  cards: [],
  previous_page_token: null,
  next_page_token: null,
  generated_at_unix_ms: 1,
};

function labeledBacklogCards(labelID: string) {
  return {
    ...emptyBacklogCards,
    cards: [
      {
        task_id: "task-labeled",
        short_id: "PRO-1",
        title: "Labeled task",
        preview: { markdown: "Body", truncated: false },
        workflow_id: "workflow-1",
        active_node_ids: ["backlog"],
        source_workspace: {
          workspace_id: "workspace-1",
          display_name: "Workspace",
          root_path: "/workspace",
          availability: "available",
          is_primary: true,
          updated_at_unix_ms: 1,
        },
        status: {
          kind: "backlog",
          native_state: "backlog",
          node_ids: ["backlog"],
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
        label_ids: [labelID],
        updated_at_unix_ms: 1,
      },
    ],
  };
}
