import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { appI18n } from "@/i18n";
import { createTestServices, TestAppProviders } from "@/test-support/app-services";
import { BoardTaskSearchChrome } from "./BoardTaskSearch";

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
    expect(services.transport.calls).toHaveLength(0);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });

    expect(services.transport.calls).toEqual([
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

  it("ignores stationary pointer events after keyboard selection", async () => {
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

    fireEvent.pointerMove(first, { pointerType: "mouse", clientX: 10, clientY: 10 });
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(second).toHaveAttribute("aria-selected", "true");

    fireEvent.pointerMove(first, { pointerType: "mouse", clientX: 10, clientY: 10 });
    expect(second).toHaveAttribute("aria-selected", "true");

    fireEvent.pointerMove(first, { pointerType: "mouse", clientX: 11, clientY: 10 });
    expect(first).toHaveAttribute("aria-selected", "true");
  });
});

function renderSearch(
  services: ReturnType<typeof createTestServices>,
  projectID: string,
  onOpenTask = vi.fn(),
): void {
  render(
    <TestAppProviders services={services}>
      <BoardTaskSearchChrome onOpenTask={onOpenTask} projectID={projectID} />
    </TestAppProviders>,
  );
}
