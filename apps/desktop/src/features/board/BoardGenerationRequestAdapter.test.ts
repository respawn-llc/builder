import { CancelledError, QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";

import type { BoardNodeCardsPage, WorkflowBoard } from "@/api";
import {
  createBoardFilterGenerationController,
  type BoardTransportAdmission,
} from "./BoardFilterGenerationController";
import { createBoardGenerationRequestAdapter } from "./BoardGenerationRequestAdapter";
import { createBoardGenerationQueryRegistry } from "./BoardGenerationQueryRegistry";

describe("BoardGenerationRequestAdapter", () => {
  it("silently cancels a closure-race query without invoking transport", async () => {
    const queryClient = new QueryClient({
      defaultOptions: {
        queries: {
          retry: 1,
          retryDelay: 0,
        },
      },
    });
    const controller = createBoardFilterGenerationController({ kind: "none" });
    let settleActiveTransport: ((board: WorkflowBoard) => void) | undefined;
    const activeTransport = admittedPromise(
      controller.admitBoardTransport(
        1,
        "board:active",
        async () =>
          new Promise<WorkflowBoard>((resolve) => {
            settleActiveTransport = resolve;
          }),
      ),
    );
    controller.setDesiredFilter({ kind: "unlabeled" });
    const adapter = createBoardGenerationRequestAdapter({
      controller,
      queryClient,
      queryRegistry: createBoardGenerationQueryRegistry(queryClient),
    });
    const queryKey = ["board", "project-1", "11111111-1111-4111-8111-111111111111", "none"] as const;
    const previous = testBoard("previous");
    queryClient.setQueryData(queryKey, previous);
    let transportCalls = 0;

    const result = queryClient.fetchQuery({
      queryKey,
      queryFn: async ({ signal }) =>
        adapter.requestBoard({
          generation: 1,
          queryKey,
          requestIdentity: "board:closure-race",
          signal,
          transport: async () => {
            transportCalls += 1;
            return testBoard("stale");
          },
        }),
    });

    await expect(result).rejects.toSatisfy((error: unknown) => error instanceof CancelledError);
    expect(transportCalls).toBe(0);
    expect(queryClient.getQueryData(queryKey)).toBe(previous);
    expect(queryClient.getQueryState(queryKey)?.error).toBeNull();

    settleActiveTransport?.(testBoard());
    await activeTransport;
    await Promise.resolve();
    expect(controller.getSnapshot().active).toMatchObject({
      generation: 2,
      filter: { labelFilter: { kind: "unlabeled" }, dependencyFilter: null },
    });
  });

  it("suppresses an issued old-generation failure and its configured retry after cancellation", async () => {
    const queryClient = new QueryClient({
      defaultOptions: {
        queries: {
          retry: 1,
          retryDelay: 0,
        },
      },
    });
    const queryKey = ["board", "project-1", "11111111-1111-4111-8111-111111111111", "none"] as const;
    const controller = createBoardFilterGenerationController(
      { kind: "none" },
      {
        onRetiring: async () => {
          await queryClient.cancelQueries({ queryKey, exact: true }, { revert: true, silent: true });
        },
      },
    );
    const adapter = createBoardGenerationRequestAdapter({
      controller,
      queryClient,
      queryRegistry: createBoardGenerationQueryRegistry(queryClient),
    });
    const previous = testBoard("previous");
    queryClient.setQueryData(queryKey, previous);
    let rejectTransport: ((error: Error) => void) | undefined;
    let transportCalls = 0;
    const fetching = queryClient.fetchQuery({
      queryKey,
      queryFn: async ({ signal }) =>
        adapter.requestBoard({
          generation: 1,
          queryKey,
          requestIdentity: "board:project-1:workflow-1",
          signal,
          transport: async () => {
            transportCalls += 1;
            return new Promise<WorkflowBoard>((_resolve, reject) => {
              rejectTransport = reject;
            });
          },
        }),
    });
    await waitUntil(() => transportCalls === 1);

    controller.setDesiredFilter({ kind: "unlabeled" });
    rejectTransport?.(new Error("old generation failed"));
    await expect(fetching).rejects.toSatisfy((error: unknown) => error instanceof CancelledError);
    await Promise.resolve();

    expect(transportCalls).toBe(1);
    expect(queryClient.getQueryData(queryKey)).toBe(previous);
    expect(queryClient.getQueryState(queryKey)?.error).toBeNull();
    expect(controller.getSnapshot().active).toMatchObject({
      generation: 2,
      filter: { labelFilter: { kind: "unlabeled" }, dependencyFilter: null },
    });
  });

  it("cancels a sequential infinite fetch before its second page is invoked", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const queryRegistry = createBoardGenerationQueryRegistry(queryClient);
    const controller = createBoardFilterGenerationController(
      { kind: "none" },
      {
        onRetiring: async (generation) => {
          await queryRegistry.cancelGeneration(generation.generation);
        },
      },
    );
    const adapter = createBoardGenerationRequestAdapter({
      controller,
      queryClient,
      queryRegistry,
    });
    const queryKey = [
      "board-node-cards",
      "project-1",
      "11111111-1111-4111-8111-111111111111",
      "none",
      "dependency",
      "dependency:null",
      "node-1",
    ] as const;
    const pageCalls: (string | null)[] = [];
    let settleFirstPage: ((page: BoardNodeCardsPage) => void) | undefined;

    const fetching = queryClient.fetchInfiniteQuery({
      queryKey,
      initialPageParam: firstPageParam(),
      pages: 2,
      getNextPageParam: (_lastPage, _pages, lastPageParam) => (lastPageParam === null ? "page-2" : undefined),
      queryFn: async ({ pageParam, signal }) =>
        adapter.requestCards({
          generation: 1,
          queryKey,
          requestIdentity: `cards:${pageParam ?? "first"}`,
          signal,
          transport: async () => {
            pageCalls.push(pageParam);
            if (pageParam !== null) {
              return testCardsPage(pageParam);
            }
            return new Promise<BoardNodeCardsPage>((resolve) => {
              settleFirstPage = resolve;
            });
          },
        }),
    });
    await waitUntil(() => pageCalls.length === 1);

    controller.setDesiredFilter({ kind: "unlabeled" });
    settleFirstPage?.(testCardsPage("page-1"));
    await expect(fetching).rejects.toSatisfy((error: unknown) => error instanceof CancelledError);
    await Promise.resolve();

    expect(pageCalls).toEqual([null]);
    await waitUntil(() => controller.getSnapshot().active.generation === 2);
    expect(controller.getSnapshot().active.generation).toBe(2);
  });
});

async function admittedPromise<T>(admission: BoardTransportAdmission<T>): Promise<T> {
  if (admission.kind === "denied") {
    throw new Error("Expected board transport admission.");
  }
  return admission.promise;
}

async function waitUntil(predicate: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    if (predicate()) {
      return;
    }
    await Promise.resolve();
  }
  throw new Error("Condition did not become true.");
}

function testBoard(projectName = "Project"): WorkflowBoard {
  return {
    attachedWorkspaceCount: 0,
    columns: [],
    defaultWorkspaceID: "workspace-1",
    generatedAt: 1,
    groups: [],
    projectID: "project-1",
    projectKey: "PRO",
    projectName,
    selectedWorkflow: null,
    workflows: [],
  };
}

function testCardsPage(nodeID: string): BoardNodeCardsPage {
  return {
    cards: [],
    generatedAt: 1,
    nextPageToken: null,
    nodeID,
    previousPageToken: null,
    projectID: "project-1",
    workflowID: "11111111-1111-4111-8111-111111111111",
  };
}

function firstPageParam(): string | null {
  return null;
}
