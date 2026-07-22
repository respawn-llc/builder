import { CancelledError, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, waitFor } from "@testing-library/react";
import { useLayoutEffect } from "react";

import { noTaskLabelFilter, type WorkflowBoard } from "@/api";
import { AppServicesProvider, queryKeys } from "@/app-facade";
import { createTestServices } from "@/test-support/app-services";
import { BoardFilterGenerationProvider } from "./BoardFilterGenerationContext";
import type { BoardFilterGenerationRuntime } from "./BoardFilterGenerationRuntime";
import { useBoardFilterGeneration } from "./BoardFilterGenerationRuntime";
import { useBoard, useBoardNodeCards } from "./useBoardData";

describe("board filter generation visibility lifecycle", () => {
  it("retains an issued lease through deactivation and visibility churn, then enrolls only in the latest generation", async () => {
    let settleOldPage: ((page: ReturnType<typeof cardsPage>) => void) | undefined;
    const oldPage = new Promise<ReturnType<typeof cardsPage>>((resolve) => {
      settleOldPage = resolve;
    });
    const services = createTestServices([
      {
        method: "workflow.board.nodeCards.list",
        handler: async (_params, callIndex) => {
          if (callIndex === 0) {
            return oldPage;
          }
          return cardsPage();
        },
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    let runtime: BoardFilterGenerationRuntime | undefined;
    const view = render(
      <QueryClientProvider client={queryClient}>
        <AppServicesProvider services={services}>
          <BoardFilterGenerationProvider initialFilter={noTaskLabelFilter}>
            <RuntimeCapture
              onRuntime={(value) => {
                runtime = value;
              }}
            />
            <ColumnOwner active />
          </BoardFilterGenerationProvider>
        </AppServicesProvider>
      </QueryClientProvider>,
    );
    await waitFor(() => {
      expect(cardCalls(services)).toHaveLength(1);
      expect(runtime).toBeDefined();
    });

    view.rerender(
      <QueryClientProvider client={queryClient}>
        <AppServicesProvider services={services}>
          <BoardFilterGenerationProvider initialFilter={noTaskLabelFilter}>
            <RuntimeCapture
              onRuntime={(value) => {
                runtime = value;
              }}
            />
            <ColumnOwner active={false} />
          </BoardFilterGenerationProvider>
        </AppServicesProvider>
      </QueryClientProvider>,
    );
    act(() => {
      runtime?.controller.setDesiredFilter({ kind: "unlabeled" });
    });
    for (let index = 0; index < 20; index += 1) {
      view.rerender(
        <QueryClientProvider client={queryClient}>
          <AppServicesProvider services={services}>
            <BoardFilterGenerationProvider initialFilter={noTaskLabelFilter}>
              <RuntimeCapture
                onRuntime={(value) => {
                  runtime = value;
                }}
              />
              <ColumnOwner active={index % 2 === 0} />
            </BoardFilterGenerationProvider>
          </AppServicesProvider>
        </QueryClientProvider>,
      );
    }
    view.rerender(
      <QueryClientProvider client={queryClient}>
        <AppServicesProvider services={services}>
          <BoardFilterGenerationProvider initialFilter={noTaskLabelFilter}>
            <RuntimeCapture
              onRuntime={(value) => {
                runtime = value;
              }}
            />
            <ColumnOwner active />
          </BoardFilterGenerationProvider>
        </AppServicesProvider>
      </QueryClientProvider>,
    );

    expect(cardCalls(services)).toHaveLength(1);
    expect(runtime?.controller.getSnapshot().active).toMatchObject({
      generation: 1,
      retiring: true,
    });

    act(() => {
      settleOldPage?.(cardsPage());
    });
    await waitFor(() => {
      expect(runtime?.controller.getSnapshot().active).toMatchObject({
        generation: 2,
        filter: { kind: "unlabeled" },
      });
    });
    await waitFor(() => {
      expect(cardCalls(services)).toHaveLength(2);
    });
    expect(cardCalls(services)[0]?.params).toMatchObject({
      label_filter: { kind: "none" },
    });
    expect(cardCalls(services)[1]?.params).toMatchObject({
      label_filter: { kind: "unlabeled" },
    });
  });

  it("retains previous card pages when the newest filter fails", async () => {
    const services = createTestServices([
      {
        method: "workflow.board.nodeCards.list",
        handler: async (_params, callIndex) => {
          if (callIndex === 0) {
            return cardsPage();
          }
          throw new Error("filtered cards failed");
        },
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    let runtime: BoardFilterGenerationRuntime | undefined;
    let querySnapshot: CardQuerySnapshot | undefined;
    render(
      <QueryClientProvider client={queryClient}>
        <AppServicesProvider services={services}>
          <BoardFilterGenerationProvider initialFilter={noTaskLabelFilter}>
            <RuntimeCapture
              onRuntime={(value) => {
                runtime = value;
              }}
            />
            <CardQueryCapture
              onSnapshot={(value) => {
                querySnapshot = value;
              }}
            />
          </BoardFilterGenerationProvider>
        </AppServicesProvider>
      </QueryClientProvider>,
    );
    await waitFor(() => {
      expect(querySnapshot).toMatchObject({ hasData: true, isError: false });
    });

    act(() => {
      runtime?.controller.setDesiredFilter({ kind: "unlabeled" });
    });
    await waitFor(() => {
      expect(querySnapshot?.isError).toBe(true);
    });

    expect(querySnapshot?.hasData).toBe(true);
  });

  it("keeps the old observer closed through broad invalidations and barriers a forced race", async () => {
    let settleOldBoard: ((board: ReturnType<typeof boardResponse>) => void) | undefined;
    const oldBoard = new Promise<ReturnType<typeof boardResponse>>((resolve) => {
      settleOldBoard = resolve;
    });
    const services = createTestServices([
      {
        method: "workflow.board.get",
        handler: async (_params, callIndex) => {
          if (callIndex === 0) {
            return oldBoard;
          }
          return boardResponse();
        },
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    let runtime: BoardFilterGenerationRuntime | undefined;
    render(
      <QueryClientProvider client={queryClient}>
        <AppServicesProvider services={services}>
          <BoardFilterGenerationProvider initialFilter={noTaskLabelFilter}>
            <RuntimeCapture
              onRuntime={(value) => {
                runtime = value;
              }}
            />
            <BoardQueryOwner />
          </BoardFilterGenerationProvider>
        </AppServicesProvider>
      </QueryClientProvider>,
    );
    const oldKey = queryKeys.board("project-1", "workflow-1", noTaskLabelFilter);
    await waitFor(() => {
      expect(boardCalls(services)).toHaveLength(1);
      expect(runtime).toBeDefined();
    });

    act(() => {
      runtime?.controller.setDesiredFilter({ kind: "unlabeled" });
    });
    await waitFor(() => {
      expect(queryClient.getQueryState(oldKey)?.fetchStatus).toBe("idle");
      expect(runtime?.controller.getSnapshot().active).toMatchObject({
        generation: 1,
        retiring: true,
      });
    });
    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.allBoards });
      await queryClient.invalidateQueries({ queryKey: queryKeys.allBoards });
      await queryClient.invalidateQueries({ queryKey: queryKeys.allBoards });
      await queryClient.invalidateQueries({ queryKey: queryKeys.allBoards });
    });
    expect(boardCalls(services)).toHaveLength(1);

    let forcedTransportCalls = 0;
    const forced = queryClient.fetchQuery({
      queryKey: oldKey,
      queryFn: async ({ signal }) => {
        const current = runtime;
        if (current === undefined) {
          throw new Error("Board runtime did not initialize.");
        }
        return current.requestAdapter.requestBoard({
          generation: 1,
          queryKey: oldKey,
          requestIdentity: "board:forced-race",
          signal,
          transport: async () => {
            forcedTransportCalls += 1;
            return testBoard();
          },
        });
      },
    });
    await expect(forced).rejects.toSatisfy((error: unknown) => error instanceof CancelledError);
    expect(forcedTransportCalls).toBe(0);
    expect(queryClient.getQueryState(oldKey)?.error).toBeNull();

    act(() => {
      settleOldBoard?.(boardResponse());
    });
    await waitFor(() => {
      expect(runtime?.controller.getSnapshot().active).toMatchObject({
        generation: 2,
        filter: { kind: "unlabeled" },
      });
      expect(boardCalls(services)).toHaveLength(2);
    });
    expect(boardCalls(services)[1]?.params).toMatchObject({
      label_filter: { kind: "unlabeled" },
    });
  });

  it("surfaces a retirement cancellation failure through the Board background-error seam", async () => {
    let settleOldBoard: ((board: ReturnType<typeof boardResponse>) => void) | undefined;
    const oldBoard = new Promise<ReturnType<typeof boardResponse>>((resolve) => {
      settleOldBoard = resolve;
    });
    const cancellationError = new Error("generation cancellation failed");
    const backgroundErrors: unknown[] = [];
    const services = createTestServices([
      {
        method: "workflow.board.get",
        result: oldBoard,
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    vi.spyOn(queryClient, "cancelQueries").mockRejectedValueOnce(cancellationError);
    let runtime: BoardFilterGenerationRuntime | undefined;
    render(
      <QueryClientProvider client={queryClient}>
        <AppServicesProvider services={services}>
          <BoardFilterGenerationProvider
            initialFilter={noTaskLabelFilter}
            onBackgroundError={(error) => {
              backgroundErrors.push(error);
            }}
          >
            <RuntimeCapture
              onRuntime={(value) => {
                runtime = value;
              }}
            />
            <BoardQueryOwner />
          </BoardFilterGenerationProvider>
        </AppServicesProvider>
      </QueryClientProvider>,
    );
    await waitFor(() => {
      expect(boardCalls(services)).toHaveLength(1);
    });

    act(() => {
      runtime?.controller.setDesiredFilter({ kind: "unlabeled" });
    });
    await waitFor(() => {
      expect(backgroundErrors).toContain(cancellationError);
    });
    await act(async () => {
      settleOldBoard?.(boardResponse());
      await Promise.resolve();
    });
  });

  it("waits for an exact next-page lease and restarts the promoted generation without the obsolete token", async () => {
    let settleOldNextPage: ((page: ReturnType<typeof cardsPage>) => void) | undefined;
    const oldNextPage = new Promise<ReturnType<typeof cardsPage>>((resolve) => {
      settleOldNextPage = resolve;
    });
    const services = createTestServices([
      {
        method: "workflow.board.nodeCards.list",
        handler: async (_params, callIndex) => {
          if (callIndex === 0) {
            return cardsPage(null, "old-next");
          }
          if (callIndex === 1) {
            return oldNextPage;
          }
          return cardsPage();
        },
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    let runtime: BoardFilterGenerationRuntime | undefined;
    let querySnapshot: CardQueryActions | undefined;
    const view = render(
      <QueryClientProvider client={queryClient}>
        <AppServicesProvider services={services}>
          <BoardFilterGenerationProvider initialFilter={noTaskLabelFilter}>
            <RuntimeCapture
              onRuntime={(value) => {
                runtime = value;
              }}
            />
            <CardQueryActionsCapture
              onSnapshot={(value) => {
                querySnapshot = value;
              }}
            />
          </BoardFilterGenerationProvider>
        </AppServicesProvider>
      </QueryClientProvider>,
    );
    await waitFor(() => {
      expect(querySnapshot?.hasNextPage).toBe(true);
    });

    act(() => {
      void querySnapshot?.fetchNextPage();
    });
    await waitFor(() => {
      expect(cardCalls(services)).toHaveLength(2);
    });
    view.rerender(
      <QueryClientProvider client={queryClient}>
        <AppServicesProvider services={services}>
          <BoardFilterGenerationProvider initialFilter={noTaskLabelFilter}>
            <RuntimeCapture
              onRuntime={(value) => {
                runtime = value;
              }}
            />
          </BoardFilterGenerationProvider>
        </AppServicesProvider>
      </QueryClientProvider>,
    );
    act(() => {
      runtime?.controller.setDesiredFilter({ kind: "unlabeled" });
    });
    view.rerender(
      <QueryClientProvider client={queryClient}>
        <AppServicesProvider services={services}>
          <BoardFilterGenerationProvider initialFilter={noTaskLabelFilter}>
            <RuntimeCapture
              onRuntime={(value) => {
                runtime = value;
              }}
            />
            <CardQueryActionsCapture
              onSnapshot={(value) => {
                querySnapshot = value;
              }}
            />
          </BoardFilterGenerationProvider>
        </AppServicesProvider>
      </QueryClientProvider>,
    );

    expect(cardCalls(services)).toHaveLength(2);
    expect(runtime?.controller.getSnapshot().active.generation).toBe(1);
    act(() => {
      settleOldNextPage?.(cardsPage());
    });
    await waitFor(() => {
      expect(runtime?.controller.getSnapshot().active.generation).toBe(2);
      expect(cardCalls(services)).toHaveLength(3);
    });
    expect(cardCalls(services).map((call) => call.params)).toEqual([
      expect.objectContaining({ page_token: null }),
      expect.objectContaining({ page_token: "old-next" }),
      expect.objectContaining({ page_token: null }),
    ]);
    expect(cardCalls(services)[2]?.params).toMatchObject({
      label_filter: { kind: "unlabeled" },
    });
  });

  it("waits for an exact previous-page lease and restarts the promoted generation without the obsolete token", async () => {
    let settleOldPreviousPage: ((page: ReturnType<typeof cardsPage>) => void) | undefined;
    const oldPreviousPage = new Promise<ReturnType<typeof cardsPage>>((resolve) => {
      settleOldPreviousPage = resolve;
    });
    const services = createTestServices([
      {
        method: "workflow.board.nodeCards.list",
        handler: async (_params, callIndex) => {
          if (callIndex === 0) {
            return cardsPage("old-previous", null);
          }
          if (callIndex === 1) {
            return oldPreviousPage;
          }
          return cardsPage();
        },
      },
    ]);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    let runtime: BoardFilterGenerationRuntime | undefined;
    let querySnapshot: CardQueryActions | undefined;
    render(
      <QueryClientProvider client={queryClient}>
        <AppServicesProvider services={services}>
          <BoardFilterGenerationProvider initialFilter={noTaskLabelFilter}>
            <RuntimeCapture
              onRuntime={(value) => {
                runtime = value;
              }}
            />
            <CardQueryActionsCapture
              onSnapshot={(value) => {
                querySnapshot = value;
              }}
            />
          </BoardFilterGenerationProvider>
        </AppServicesProvider>
      </QueryClientProvider>,
    );
    await waitFor(() => {
      expect(querySnapshot?.hasPreviousPage).toBe(true);
    });

    act(() => {
      void querySnapshot?.fetchPreviousPage();
    });
    await waitFor(() => {
      expect(cardCalls(services)).toHaveLength(2);
    });
    act(() => {
      runtime?.controller.setDesiredFilter({ kind: "unlabeled" });
    });

    expect(runtime?.controller.getSnapshot().active.generation).toBe(1);
    act(() => {
      settleOldPreviousPage?.(cardsPage());
    });
    await waitFor(() => {
      expect(runtime?.controller.getSnapshot().active.generation).toBe(2);
      expect(cardCalls(services)).toHaveLength(3);
    });
    expect(cardCalls(services).map((call) => call.params)).toEqual([
      expect.objectContaining({ page_token: null }),
      expect.objectContaining({ page_token: "old-previous" }),
      expect.objectContaining({ page_token: null }),
    ]);
    expect(cardCalls(services)[2]?.params).toMatchObject({
      label_filter: { kind: "unlabeled" },
    });
  });
});

function RuntimeCapture({
  onRuntime,
}: Readonly<{
  onRuntime(runtime: BoardFilterGenerationRuntime): void;
}>) {
  const runtime = useBoardFilterGeneration();
  useLayoutEffect(() => {
    onRuntime(runtime);
  }, [onRuntime, runtime]);
  return null;
}

function ColumnOwner({ active }: Readonly<{ active: boolean }>) {
  return active ? <ActiveColumnOwner /> : null;
}

function ActiveColumnOwner() {
  useBoardNodeCards("project-1", "workflow-1", "node-1", true);
  return null;
}

function BoardQueryOwner() {
  useBoard("project-1", "workflow-1");
  return null;
}

type CardQuerySnapshot = Readonly<{
  hasData: boolean;
  isError: boolean;
}>;

type CardQueryActions = Readonly<{
  fetchNextPage(): Promise<unknown>;
  fetchPreviousPage(): Promise<unknown>;
  hasNextPage: boolean;
  hasPreviousPage: boolean;
}>;

function CardQueryCapture({
  onSnapshot,
}: Readonly<{
  onSnapshot(snapshot: CardQuerySnapshot): void;
}>) {
  const query = useBoardNodeCards("project-1", "workflow-1", "node-1", true);
  useLayoutEffect(() => {
    onSnapshot({
      hasData: query.data !== undefined,
      isError: query.isError,
    });
  }, [onSnapshot, query.data, query.isError]);
  return null;
}

function CardQueryActionsCapture({
  onSnapshot,
}: Readonly<{
  onSnapshot(snapshot: CardQueryActions): void;
}>) {
  const query = useBoardNodeCards("project-1", "workflow-1", "node-1", true);
  useLayoutEffect(() => {
    onSnapshot({
      fetchNextPage: query.fetchNextPage,
      fetchPreviousPage: query.fetchPreviousPage,
      hasNextPage: query.hasNextPage,
      hasPreviousPage: query.hasPreviousPage,
    });
  }, [onSnapshot, query.fetchNextPage, query.fetchPreviousPage, query.hasNextPage, query.hasPreviousPage]);
  return null;
}

function cardCalls(services: ReturnType<typeof createTestServices>) {
  return services.transport.calls.filter((call) => call.method === "workflow.board.nodeCards.list");
}

function boardCalls(services: ReturnType<typeof createTestServices>) {
  return services.transport.calls.filter((call) => call.method === "workflow.board.get");
}

function cardsPage(previousPageToken: string | null = null, nextPageToken: string | null = null) {
  return {
    project_id: "project-1",
    workflow_id: "workflow-1",
    node_id: "node-1",
    cards: [],
    previous_page_token: previousPageToken,
    next_page_token: nextPageToken,
    generated_at_unix_ms: 1,
  };
}

function boardResponse() {
  return {
    board: {
      project_id: "project-1",
      project: {
        project_key: "PRO",
        display_name: "Project",
        default_workspace_id: "workspace-1",
        attached_workspace_count: 1,
      },
      selected_workflow: {
        workflow_id: "workflow-1",
        display_name: "Workflow",
        description: "",
        version: 1,
        is_project_default: true,
        valid_for_task_creation: true,
        validation_errors: [],
      },
      workflows: [],
      groups: [],
      columns: [],
      generated_at_unix_ms: 1,
    },
  };
}

function testBoard(): WorkflowBoard {
  return {
    attachedWorkspaceCount: 1,
    columns: [],
    defaultWorkspaceID: "workspace-1",
    generatedAt: 1,
    groups: [],
    projectID: "project-1",
    projectKey: "PRO",
    projectName: "Project",
    selectedWorkflow: null,
    workflows: [],
  };
}
