import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useEffect } from "react";

import { appI18n } from "@/i18n";
import { useSidebar } from "@/app-facade";
import { SidebarProvider } from "@/app/sidebarProvider";
import { createTestServices, TestAppProviders } from "@/test-support/app-services";
import {
  TaskSearchGlobalTrigger,
  TaskSearchHost,
  TaskSearchProjectTrigger,
  TaskSearchProvider,
} from "@/shared/task-search";

const searchResponse = {
  mode: "literal",
  groups: [
    {
      project_id: "project-1",
      project_key: "KNT",
      task_id: "task-1",
      short_id: "KNT-1",
      workflow_id: "workflow-1",
      title: "Search the board",
      status: {
        kind: "active",
        native_state: "active",
        node_ids: ["node-1"],
        attention_types: [],
      },
      total_hit_count: 4,
      hits: [
        {
          ordinal: 1,
          source: { kind: "title" },
          literal: {
            before: "",
            match: "Search",
            after: " the board",
            left_truncated: false,
            right_truncated: false,
          },
        },
        {
          ordinal: 2,
          source: { kind: "body" },
          literal: {
            before: "Build ",
            match: "search",
            after: " UI",
            left_truncated: false,
            right_truncated: false,
          },
        },
        {
          ordinal: 3,
          source: { kind: "comment", comment_id: "comment-1" },
          literal: {
            before: "Please ",
            match: "search",
            after: " comments",
            left_truncated: true,
            right_truncated: true,
          },
        },
      ],
    },
    {
      project_id: "project-1",
      project_key: "KNT",
      task_id: "task-2",
      short_id: "KNT-2",
      workflow_id: "workflow-2",
      title: "Second result",
      status: {
        kind: "done",
        native_state: "terminal",
        node_ids: [],
        attention_types: [],
      },
      total_hit_count: 1,
      hits: [
        {
          ordinal: 1,
          source: { kind: "body" },
          literal: {
            before: "Another ",
            match: "search",
            after: " result",
            left_truncated: false,
            right_truncated: false,
          },
        },
      ],
    },
  ],
} as const;

describe("Board Task Search", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("debounces a Project-scoped Comment-inclusive search", async () => {
    const pendingSearch = new Promise<never>(() => undefined);
    const services = createTestServices([
      {
        method: "workflow.task.search",
        handler: async () => pendingSearch,
      },
    ]);

    renderSearch(services, "project-1");
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("taskSearch.open") }));

    const input = screen.getByRole("searchbox", { name: appI18n.t("taskSearch.input") });
    expect(input).toHaveFocus();
    fireEvent.change(input, { target: { value: "search" } });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(299);
    });
    expect(services.transport.dedicatedCalls).toHaveLength(0);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });

    expect(services.transport.dedicatedCalls).toMatchObject([
      {
        method: "workflow.task.search",
        params: {
          mode: "literal",
          query: "search",
          context: 20,
          case_sensitive: false,
          include_comments: true,
          project_ids: ["project-1"],
          page_size: 40,
        },
      },
    ]);
    expect(services.transport.dedicatedCalls[0]?.options?.signal).toBeInstanceOf(AbortSignal);
  });

  it("keeps input focus while arrows choose a Task and Enter opens it", async () => {
    vi.useRealTimers();
    const services = createTestServices([{ method: "workflow.task.search", result: searchResponse }]);
    const onOpenTask = vi.fn();

    renderSearch(services, "project-keyboard", onOpenTask);
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("taskSearch.open") }));
    const input = screen.getByRole("searchbox", { name: appI18n.t("taskSearch.input") });
    fireEvent.change(input, { target: { value: "search" } });
    expect(await screen.findAllByRole("option")).toHaveLength(2);
    const listbox = screen.getByRole("listbox", { name: appI18n.t("taskSearch.results") });
    expect(input).toHaveAttribute("aria-controls", listbox.id);
    expect(within(listbox).queryByRole("listitem")).not.toBeInTheDocument();
    expect(screen.getAllByRole("option")[0]).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("img", { name: appI18n.t("taskSearch.commentHit") })).toBeInTheDocument();

    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(input).toHaveFocus();
    expect(screen.getAllByRole("option")[1]).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(input, { key: "Enter" });
    expect(onOpenTask).not.toHaveBeenCalled();
    await waitFor(() => {
      expect(onOpenTask).toHaveBeenCalledWith("task-2");
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("keeps the prior Task result actionable while a replacement query debounces", async () => {
    vi.useRealTimers();
    const services = createTestServices([{ method: "workflow.task.search", result: searchResponse }]);
    const onOpenTask = vi.fn();

    renderSearch(services, "project-retained", onOpenTask);
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("taskSearch.open") }));
    const input = screen.getByRole("searchbox", { name: appI18n.t("taskSearch.input") });
    fireEvent.change(input, { target: { value: "search" } });
    expect(await screen.findAllByRole("option")).toHaveLength(2);

    fireEvent.change(input, { target: { value: "replacement query" } });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => {
      expect(onOpenTask).toHaveBeenCalledWith("task-1");
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("falls back to the first result when a refresh removes the remembered selection", async () => {
    vi.useRealTimers();
    const refreshedResponse = { ...searchResponse, groups: searchResponse.groups.slice(0, 1) };
    const services = createTestServices([
      {
        method: "workflow.task.search",
        handler: (_params, callIndex) => (callIndex === 0 ? searchResponse : refreshedResponse),
      },
    ]);

    renderSearch(services, "project-refresh");
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("taskSearch.open") }));
    const input = screen.getByRole("searchbox", { name: appI18n.t("taskSearch.input") });
    fireEvent.change(input, { target: { value: "search" } });
    expect(await screen.findAllByRole("option")).toHaveLength(2);
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(screen.getAllByRole("option")[1]).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("taskSearch.open") }));

    await waitFor(() => {
      expect(services.transport.dedicatedCalls).toHaveLength(2);
      expect(screen.getAllByRole("option")).toHaveLength(1);
    });
    expect(screen.getByRole("option")).toHaveAttribute("aria-selected", "true");
  });

  it("cancels a pending Task activation when Search reopens during exit", async () => {
    vi.useRealTimers();
    const services = createTestServices([{ method: "workflow.task.search", result: searchResponse }]);
    const onOpenTask = vi.fn();

    renderSearch(services, "project-reopen", onOpenTask);
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("taskSearch.open") }));
    const input = screen.getByRole("searchbox", { name: appI18n.t("taskSearch.input") });
    fireEvent.change(input, { target: { value: "search" } });
    expect(await screen.findAllByRole("option")).toHaveLength(2);

    fireEvent.keyDown(input, { key: "Enter" });
    fireEvent.keyDown(window, { code: "KeyS", metaKey: true });
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    fireEvent.change(screen.getByRole("searchbox", { name: appI18n.t("taskSearch.input") }), {
      target: { value: "global" },
    });
    await waitFor(() => {
      expect(services.transport.dedicatedCalls.at(-1)?.params).not.toHaveProperty("project_ids");
    });

    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
    expect(onOpenTask).not.toHaveBeenCalled();
  });

  it("selects on pointer movement but ignores stationary pointer events after keyboard selection", async () => {
    vi.useRealTimers();
    const services = createTestServices([{ method: "workflow.task.search", result: searchResponse }]);

    renderSearch(services, "project-pointer-intent");
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("taskSearch.open") }));
    const input = screen.getByRole("searchbox", { name: appI18n.t("taskSearch.input") });
    fireEvent.change(input, { target: { value: "search" } });
    const options = await screen.findAllByRole("option");
    const first = options[0];
    const second = options[1];
    if (first === undefined || second === undefined) {
      throw new Error("Task Search pointer test requires two results.");
    }

    fireEvent.pointerMove(second, { pointerType: "mouse", clientX: 10, clientY: 10 });
    expect(second).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(input, { key: "ArrowUp" });
    expect(first).toHaveAttribute("aria-selected", "true");

    fireEvent.pointerMove(second, { pointerType: "mouse", clientX: 10, clientY: 10 });
    expect(first).toHaveAttribute("aria-selected", "true");

    fireEvent.pointerMove(second, { pointerType: "mouse", clientX: 11, clientY: 10 });
    expect(second).toHaveAttribute("aria-selected", "true");
  });

  it("closes the route-owned Task lifecycle after replacing it from global Search", async () => {
    vi.useRealTimers();
    const services = createTestServices([{ method: "workflow.task.search", result: searchResponse }]);
    const onRouteResult = vi.fn();

    render(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <TaskSearchProvider>
            <TaskSearchHost />
            <TaskSearchGlobalTrigger />
            <RouteTaskLifecycle onResult={onRouteResult} />
          </TaskSearchProvider>
        </SidebarProvider>
      </TestAppProviders>,
    );

    fireEvent.click(screen.getByRole("button", { name: appI18n.t("taskSearch.open") }));
    const input = screen.getByRole("searchbox", { name: appI18n.t("taskSearch.input") });
    fireEvent.change(input, { target: { value: "search" } });
    expect(await screen.findAllByRole("option")).toHaveLength(2);
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => {
      expect(screen.getByTestId("active-sidebar-task")).toHaveTextContent("task-2");
    });
    fireEvent.click(screen.getByRole("button", { name: "Close sidebar for test" }));

    await waitFor(() => {
      expect(onRouteResult).toHaveBeenCalledWith({ status: "canceled", reason: "closed" });
    });
  });

  it("invalidates Project Search when its BoardRoute owner changes", async () => {
    vi.useRealTimers();
    const services = createTestServices([
      { method: "workflow.task.search", handler: async () => new Promise<never>(() => undefined) },
    ]);
    const view = renderSearch(services, "project-1", vi.fn(), { ownerKey: "project-1:workflow-1" });
    fireEvent.click(screen.getByRole("button", { name: appI18n.t("taskSearch.open") }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    view.rerender(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <TaskSearchProvider>
            <TaskSearchHost />
            <TaskSearchProjectTrigger
              onOpenTask={vi.fn()}
              ownerKey="project-1:workflow-2"
              projectID="project-1"
            />
          </TaskSearchProvider>
        </SidebarProvider>
      </TestAppProviders>,
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    view.rerender(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <TaskSearchProvider>
            <TaskSearchHost />
            <TaskSearchProjectTrigger
              onOpenTask={vi.fn()}
              ownerKey="project-2:workflow-2"
              projectID="project-2"
            />
          </TaskSearchProvider>
        </SidebarProvider>
      </TestAppProviders>,
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("opens a global result while a Task Detail sidebar is closing", async () => {
    vi.useRealTimers();
    const services = createTestServices([{ method: "workflow.task.search", result: searchResponse }]);
    const onRouteResult = vi.fn();
    render(
      <TestAppProviders services={services}>
        <SidebarProvider>
          <TaskSearchProvider>
            <TaskSearchHost />
            <TaskSearchGlobalTrigger />
            <RouteTaskLifecycle onResult={onRouteResult} />
          </TaskSearchProvider>
        </SidebarProvider>
      </TestAppProviders>,
    );

    fireEvent.click(screen.getByRole("button", { name: appI18n.t("taskSearch.open") }));
    const input = screen.getByRole("searchbox", { name: appI18n.t("taskSearch.input") });
    fireEvent.change(input, { target: { value: "search" } });
    expect(await screen.findAllByRole("option")).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "Close sidebar for test" }));
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => {
      expect(screen.getByTestId("active-sidebar-task")).toHaveTextContent("task-2");
    });
  });

  it("shares the query while retaining selections independently by Search scope", async () => {
    vi.useRealTimers();
    const services = createTestServices([{ method: "workflow.task.search", result: searchResponse }]);
    const onOpenTask = vi.fn();

    renderSearch(services, "project-1", onOpenTask, { global: true });

    const [globalTrigger, projectTrigger] = screen.getAllByRole("button", {
      name: appI18n.t("taskSearch.open"),
    });
    if (globalTrigger === undefined || projectTrigger === undefined) {
      throw new Error("Task Search scope test requires both entry points.");
    }
    fireEvent.click(projectTrigger);
    const projectInput = screen.getByRole("searchbox", { name: appI18n.t("taskSearch.input") });
    fireEvent.change(projectInput, { target: { value: "search" } });
    expect(await screen.findAllByRole("option")).toHaveLength(2);
    fireEvent.keyDown(projectInput, { key: "ArrowDown" });

    fireEvent.click(globalTrigger);
    const globalInput = screen.getByRole("searchbox", { name: appI18n.t("taskSearch.input") });
    expect(globalInput).toHaveValue("search");
    await waitFor(() => {
      expect(services.transport.dedicatedCalls.at(-1)?.params).not.toHaveProperty("project_ids");
    });
    expect(await screen.findAllByRole("option")).toHaveLength(2);
    expect(screen.getAllByRole("option")[0]).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(globalInput, { key: "ArrowDown" });

    fireEvent.click(projectTrigger);
    await waitFor(() => {
      expect(screen.getAllByRole("option")[1]).toHaveAttribute("aria-selected", "true");
    });
    const reopenedProjectInput = screen.getByRole("searchbox", { name: appI18n.t("taskSearch.input") });
    expect(reopenedProjectInput).toHaveAttribute(
      "aria-activedescendant",
      screen.getAllByRole("option")[1]?.id,
    );
  });
});

function renderSearch(
  services: ReturnType<typeof createTestServices>,
  projectID: string,
  onOpenTask = vi.fn(),
  options: Readonly<{ global?: boolean; ownerKey?: string }> = {},
): ReturnType<typeof render> {
  return render(
    <TestAppProviders services={services}>
      <SidebarProvider>
        <TaskSearchProvider>
          <TaskSearchHost />
          {options.global ? <TaskSearchGlobalTrigger /> : null}
          <TaskSearchProjectTrigger
            onOpenTask={onOpenTask}
            ownerKey={options.ownerKey ?? projectID}
            projectID={projectID}
          />
        </TaskSearchProvider>
      </SidebarProvider>
    </TestAppProviders>,
  );
}

function RouteTaskLifecycle({ onResult }: Readonly<{ onResult(result: unknown): void }>) {
  const { activeDestination, closeSidebar, openSidebar } = useSidebar();
  useEffect(() => {
    void openSidebar({ kind: "taskDetail", taskID: "task-1" }).then(onResult);
  }, [onResult, openSidebar]);
  return (
    <>
      <output data-testid="active-sidebar-task">
        {activeDestination?.kind === "taskDetail" ? activeDestination.taskID : ""}
      </output>
      <button
        onClick={() => {
          closeSidebar();
        }}
        type="button"
      >
        Close sidebar for test
      </button>
    </>
  );
}
