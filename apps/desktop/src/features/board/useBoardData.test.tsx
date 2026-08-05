import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, vi } from "vitest";

import type { BoardNodeCardsPage, BoardNodeCardsSort } from "@/api";

const testState = vi.hoisted(() => ({
  requests: [] as Array<{ offset: number; sort: { field: string; direction: string } }>,
  sort: { field: "updated", direction: "desc" } as BoardNodeCardsSort,
}));

vi.mock("@/app-facade", () => ({
  useAppServices: () => ({
    api: {
      listBoardNodeCards: async (input: {
        offset?: number;
        sort?: { field: string; direction: string };
      }): Promise<BoardNodeCardsPage> => {
        const offset = input.offset ?? 0;
        const sort = input.sort ?? { field: "updated", direction: "desc" };
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

vi.mock("./BoardFilterGenerationRuntime", () => ({
  useBoardFilterGeneration: () => ({
    queriesEnabled: true,
    requestAdapter: {
      requestCards: ({ transport }: { transport: () => Promise<BoardNodeCardsPage> }) => transport(),
    },
    snapshot: {
      active: {
        generation: 1,
        filter: { labelFilter: { kind: "none" }, dependencyFilter: null },
        retiring: false,
      },
      desiredFilter: null,
    },
    sort: testState.sort,
  }),
}));

import { useBoardNodeCards } from "./useBoardData";

describe("useBoardNodeCards pagination", () => {
  afterEach(() => {
    testState.requests.length = 0;
    testState.sort = { field: "updated", direction: "desc" };
  });

  it("starts at offset zero, preserves server sort, and walks back to zero without duplicates", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { result } = renderHook(
      () => useBoardNodeCards("project-1", "workflow-1", "node-1", true),
      { wrapper: queryWrapper(queryClient) },
    );

    await waitFor(() => expect(testState.requests).toHaveLength(1));
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
    await waitFor(() => expect(result.current.data?.pageParams).toEqual([50, 75, 100]));
    expect(testState.requests.map((request) => request.offset)).toEqual([0, 25, 50, 75, 100]);
    expect(result.current.hasPreviousPage).toBe(true);

    await act(async () => {
      await result.current.fetchPreviousPage();
      await result.current.fetchPreviousPage();
    });
    await waitFor(() => expect(result.current.data?.pageParams).toEqual([0, 25, 50]));
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

    await waitFor(() => expect(testState.requests).toHaveLength(1));
    testState.sort = { field: "created", direction: "desc" };
    rerender();
    await waitFor(() => expect(testState.requests).toHaveLength(2));
    expect(testState.requests).toEqual([
      { offset: 0, sort: { field: "updated", direction: "desc" } },
      { offset: 0, sort: { field: "created", direction: "desc" } },
    ]);

    testState.sort = { field: "labels", direction: "asc" };
    rerender();
    await waitFor(() => expect(testState.requests).toHaveLength(3));
    expect(testState.requests[2]).toEqual({
      offset: 0,
      sort: { field: "labels", direction: "asc" },
    });
    expect(result.current.hasPreviousPage).toBe(false);
  });
});

function queryWrapper(queryClient: QueryClient) {
  return function QueryWrapper({ children }: Readonly<{ children: ReactNode }>) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  };
}
