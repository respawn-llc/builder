import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, vi } from "vitest";

import type { BoardNodeCardsPage, BoardNodeCardsSort } from "@/api";

type BoardRequest = Readonly<{ offset: number; sort: BoardNodeCardsSort }>;

const testState = vi.hoisted((): { requests: BoardRequest[]; sort: BoardNodeCardsSort } => ({
  requests: [],
  sort: { field: "updated", direction: "desc" },
}));

vi.mock("@/app-facade", () => ({
  useAppServices: () => ({
    api: {
      listBoardNodeCards: async (input: {
        offset?: number;
        sort?: BoardNodeCardsSort;
      }): Promise<BoardNodeCardsPage> => {
        const offset = input.offset ?? 0;
        const sort = input.sort ?? ({ field: "updated", direction: "desc" } satisfies BoardNodeCardsSort);
        testState.requests.push({ offset, sort });
        return {
          projectID: "project-1",
          workflowID: "workflow-1",
          nodeID: "node-1",
          cards: [],
          nextOffset: offset < 100 ? offset + 25 : null,
          generatedAt: 1,
        };
      },
    },
  }),
  queryKeys: {
    boardNodeCards: (...parts: unknown[]) => ["board-node-cards", ...parts],
  },
}));

vi.mock("./BoardQueryRuntime", () => ({
  useBoardQuery: () => ({
    filter: { labelFilter: { kind: "none" }, dependencyFilter: null },
    queriesEnabled: true,
    sort: testState.sort,
  }),
}));

import { shouldRefreshBoardFromProjectEvent, useBoardNodeCards } from "./useBoardData";

describe("useBoardNodeCards pagination", () => {
  afterEach(() => {
    testState.requests.length = 0;
    testState.sort = { field: "updated", direction: "desc" };
  });

  it("starts at offset zero, preserves server sort, and walks back to zero without duplicates", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { result } = renderHook(() => useBoardNodeCards("project-1", "workflow-1", "node-1", true), {
      wrapper: queryWrapper(queryClient),
    });

    await waitFor(() => {
      expect(testState.requests).toHaveLength(1);
    });
    expect(testState.requests[0]).toEqual({
      offset: 0,
      sort: { field: "updated", direction: "desc" },
    });
    expect(result.current.hasPreviousPage).toBe(false);

    await act(async () => {
      await result.current.fetchNextPage();
      await result.current.fetchNextPage();
      await result.current.fetchNextPage();
      await result.current.fetchNextPage();
    });
    await waitFor(() => {
      expect(result.current.data?.pageParams).toEqual([50, 75, 100]);
    });
    expect(testState.requests.map((request) => request.offset)).toEqual([0, 25, 50, 75, 100]);
    expect(result.current.hasPreviousPage).toBe(true);

    await act(async () => {
      await result.current.fetchPreviousPage();
      await result.current.fetchPreviousPage();
    });
    await waitFor(() => {
      expect(result.current.data?.pageParams).toEqual([0, 25, 50]);
    });
    expect(testState.requests.map((request) => request.offset)).toEqual([0, 25, 50, 75, 100, 25, 0]);
    expect(result.current.hasPreviousPage).toBe(false);

    await act(async () => {
      await result.current.fetchPreviousPage();
    });
    expect(testState.requests.map((request) => request.offset)).toEqual([0, 25, 50, 75, 100, 25, 0]);
  });

  it("restarts at offset zero when the route-local sort changes", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { result, rerender } = renderHook(
      () => useBoardNodeCards("project-1", "workflow-1", "node-1", true),
      { wrapper: queryWrapper(queryClient) },
    );

    await waitFor(() => {
      expect(testState.requests).toHaveLength(1);
    });
    testState.sort = { field: "created", direction: "desc" };
    rerender();
    await waitFor(() => {
      expect(testState.requests).toHaveLength(2);
    });
    expect(testState.requests).toEqual([
      { offset: 0, sort: { field: "updated", direction: "desc" } },
      { offset: 0, sort: { field: "created", direction: "desc" } },
    ]);

    testState.sort = { field: "labels", direction: "asc" };
    rerender();
    await waitFor(() => {
      expect(testState.requests).toHaveLength(3);
    });
    expect(testState.requests[2]).toEqual({
      offset: 0,
      sort: { field: "labels", direction: "asc" },
    });
    expect(result.current.hasPreviousPage).toBe(false);
  });
});

describe("Board project event refresh", () => {
  it("leaves label membership refresh to the Project Label owner", () => {
    expect(
      shouldRefreshBoardFromProjectEvent(
        {
          action: "labels_changed",
          occurredAtUnixMs: 1,
          primaryEntityID: "task-1",
          projectID: "project-1",
          relatedIDs: [],
          resource: "task",
          workflowID: "workflow-1",
        },
        "workflow-1",
        "workflow-1",
      ),
    ).toBe(false);
  });

  it("refreshes when a label event has no Workflow owner", () => {
    expect(
      shouldRefreshBoardFromProjectEvent(
        {
          action: "labels_changed",
          occurredAtUnixMs: 1,
          primaryEntityID: "task-1",
          projectID: "project-1",
          relatedIDs: [],
          resource: "task",
          workflowID: null,
        },
        "workflow-1",
        "workflow-1",
      ),
    ).toBe(true);
  });
});

function queryWrapper(queryClient: QueryClient) {
  return function QueryWrapper({ children }: Readonly<{ children: ReactNode }>) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  };
}
