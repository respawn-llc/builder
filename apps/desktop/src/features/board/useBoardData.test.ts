import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { describe, expect, it } from "vitest";

import type { BoardNodeCardsPage, WorkflowProjectEvent } from "@/api";
import { queryKeys } from "@/app-facade";
import { AppServicesProvider } from "@/app-facade";
import { createTestServices } from "@/test-support/app-services";
import { shouldRefreshBoardFromProjectEvent } from "./useBoardData";
import { useBoardNodeCards } from "./useBoardData";

describe("shouldRefreshBoardFromProjectEvent", () => {
  it("refreshes active workflow task events", () => {
    expect(
      shouldRefreshBoardFromProjectEvent(
        workflowEvent({ resource: "task", workflowID: "workflow-active" }),
        "workflow-route",
        "workflow-active",
      ),
    ).toBe(true);
  });

  it("skips unrelated workflow task events", () => {
    expect(
      shouldRefreshBoardFromProjectEvent(
        workflowEvent({ resource: "task", workflowID: "workflow-other" }),
        "workflow-route",
        "workflow-active",
      ),
    ).toBe(false);
  });

  it("keeps workflow-link and unscoped workflow events conservative", () => {
    expect(
      shouldRefreshBoardFromProjectEvent(
        workflowEvent({ resource: "workflow_link", workflowID: "workflow-other" }),
        "workflow-route",
        "workflow-active",
      ),
    ).toBe(true);
    expect(
      shouldRefreshBoardFromProjectEvent(
        workflowEvent({ resource: "task", workflowID: null }),
        "workflow-route",
        "workflow-active",
      ),
    ).toBe(true);
  });
  it("refreshes workflow links without a selected workflow and skips unrelated task events", () => {
    expect(
      shouldRefreshBoardFromProjectEvent(
        workflowEvent({ resource: "workflow_link", workflowID: "workflow-new" }),
        undefined,
        undefined,
      ),
    ).toBe(true);
    expect(
      shouldRefreshBoardFromProjectEvent(
        workflowEvent({ resource: "task", workflowID: "workflow-other" }),
        undefined,
        undefined,
      ),
    ).toBe(false);
  });

  it("retains three bidirectional pages and releases them when the column owner unmounts", async () => {
    const pages = [
      boardCardsPage(0, null, "older-1"),
      boardCardsPage(1, "newer-0", "older-2"),
      boardCardsPage(2, "newer-1", "older-3"),
      boardCardsPage(3, "newer-2", null),
      boardCardsPage(0, null, "older-1"),
    ];
    const services = createTestServices([
      {
        method: "workflow.board.nodeCards.list",
        handler: (_params, callIndex) => pages[callIndex],
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const wrapper = ({ children }: Readonly<{ children: ReactNode }>) =>
      createElement(
        QueryClientProvider,
        { client: queryClient },
        createElement(AppServicesProvider, { services, children }),
      );
    const view = renderHook(() => useBoardNodeCards("project-1", "workflow-1", "node-1", true), { wrapper });

    await waitFor(() => {
      expect(view.result.current.isSuccess).toBe(true);
    });
    expect(pageTaskIDs(view.result.current.data?.pages)).toEqual(["task-0"]);
    expect(view.result.current.hasNextPage).toBe(true);
    await act(async () => {
      await view.result.current.fetchNextPage();
    });
    await waitFor(() => {
      expect(pageTaskIDs(view.result.current.data?.pages)).toEqual(["task-0", "task-1"]);
    });
    expect(services.transport.calls).toHaveLength(2);
    await act(async () => {
      await view.result.current.fetchNextPage();
    });
    await waitFor(() => {
      expect(pageTaskIDs(view.result.current.data?.pages)).toEqual(["task-0", "task-1", "task-2"]);
    });
    await act(async () => {
      await view.result.current.fetchNextPage();
    });
    await waitFor(() => {
      expect(pageTaskIDs(view.result.current.data?.pages)).toEqual(["task-1", "task-2", "task-3"]);
    });

    await act(async () => {
      await view.result.current.fetchPreviousPage();
    });
    await waitFor(() => {
      expect(pageTaskIDs(view.result.current.data?.pages)).toEqual(["task-0", "task-1", "task-2"]);
    });
    expect(
      services.transport.calls
        .filter((call) => call.method === "workflow.board.nodeCards.list")
        .map((call) => call.params),
    ).toEqual([
      boardCardsParams(null),
      boardCardsParams("older-1"),
      boardCardsParams("older-2"),
      boardCardsParams("older-3"),
      boardCardsParams("newer-0"),
    ]);

    view.unmount();
    await waitFor(() => {
      expect(
        queryClient.getQueryData(queryKeys.boardNodeCards("project-1", "workflow-1", "node-1")),
      ).toBeUndefined();
    });
  });
});

function workflowEvent(overrides: Partial<WorkflowProjectEvent>): WorkflowProjectEvent {
  return {
    action: "updated",
    changedIDs: [],
    occurredAtUnixMs: 1,
    projectID: "project-1",
    resource: "task",
    workflowID: "workflow-1",
    ...overrides,
  };
}

function boardCardsPage(index: number, previousPageToken: string | null, nextPageToken: string | null) {
  return {
    project_id: "project-1",
    workflow_id: "workflow-1",
    node_id: "node-1",
    cards: [
      {
        task_id: `task-${index.toString()}`,
        short_id: `KNT-${index.toString()}`,
        title: `Task ${index.toString()}`,
        preview: { markdown: `Preview ${index.toString()}`, truncated: false },
        workflow_id: "workflow-1",
        active_node_ids: ["node-1"],
        source_workspace: {
          workspace_id: "workspace-1",
          display_name: "Workspace",
          root_path: "/workspace",
          availability: "available",
          is_primary: true,
          updated_at_unix_ms: 1,
        },
        status: {
          kind: "active",
          native_state: "active",
          node_ids: ["node-1"],
          run_ids: [],
          attention_types: [],
        },
        actions: {
          can_start: false,
          can_interrupt: false,
          can_resume: false,
          can_cancel: true,
          manual_move_target_node_ids: [],
        },
        updated_at_unix_ms: index,
      },
    ],
    previous_page_token: previousPageToken,
    next_page_token: nextPageToken,
    generated_at_unix_ms: 1,
  };
}

function boardCardsParams(pageToken: string | null) {
  return {
    project_id: "project-1",
    workflow_id: "workflow-1",
    node_id: "node-1",
    page_size: 25,
    page_token: pageToken,
  };
}

function pageTaskID(page: Readonly<{ cards: readonly Readonly<{ id: string }>[] }>) {
  return page.cards[0]?.id;
}

function pageTaskIDs(pages: readonly BoardNodeCardsPage[] | undefined) {
  return pages?.map(pageTaskID) ?? [];
}
