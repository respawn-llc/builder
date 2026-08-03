import { useCallback, useEffect, useLayoutEffect, useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";

import { boardFiltersEqual, errorMessage, type BoardColumn, type SelectedWorkflowBoard } from "@/api";
import { useAppServices } from "@/app-facade";
import { useProjectLabelCatalog } from "@/shared/labels";
import { useStableCallback, type VirtualizedInfiniteListBoundaryState } from "@/ui";
import { cardBelongsToColumn } from "./BoardCardMotionModel";
import { toKanbanCardVM, type KanbanCardVM } from "./BoardColumnViewModel";
import { useBoardFilterGeneration } from "./BoardFilterGenerationRuntime";
import { useBoardNodeCards } from "./useBoardData";

export type BoardColumnUpdateCause = "hydration" | "pagination" | "domain";

export type BoardColumnQueryDataSnapshot = Readonly<{
  cards: readonly KanbanCardVM[];
  generation: number;
  hasData: boolean;
  isFetching: boolean;
  isSettled: boolean;
  taskCount: number;
}>;

export type BoardColumnQuerySnapshot =
  | Readonly<{ cause: "deactivation" }>
  | Readonly<{
      cause: BoardColumnUpdateCause;
      data: BoardColumnQueryDataSnapshot;
    }>;

export type BoardColumnDataView = Readonly<{
  cards: readonly KanbanCardVM[];
  hasNextPage: boolean;
  hasPreviousPage: boolean;
  initialBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  isFetchingNextPage: boolean;
  isFetchingPreviousPage: boolean;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  onLoadMore: () => void;
  onLoadPrevious: () => void;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  replacementBoundary: VirtualizedInfiniteListBoundaryState | undefined;
}>;

type BoardColumnDataOwnerBoard = Readonly<{
  attachedWorkspaceCount: SelectedWorkflowBoard["attachedWorkspaceCount"];
  defaultWorkspaceID: SelectedWorkflowBoard["defaultWorkspaceID"];
  projectID: SelectedWorkflowBoard["projectID"];
  selectedWorkflow: Pick<SelectedWorkflowBoard["selectedWorkflow"], "id">;
}>;

type BoardColumnDataOwnerColumn = Pick<
  BoardColumn,
  "id" | "isBacklog" | "isDone" | "taskCount"
>;

export function BoardColumnDataOwner({
  board,
  column,
  onDataViewChange,
  onDataViewRelease,
  onReportColumnSnapshot,
}: Readonly<{
  board: BoardColumnDataOwnerBoard;
  column: BoardColumnDataOwnerColumn;
  onDataViewChange: (view: BoardColumnDataView) => void;
  onDataViewRelease: () => void;
  onReportColumnSnapshot: (columnID: string, snapshot: BoardColumnQuerySnapshot) => void;
}>) {
  const { t } = useTranslation();
  const { logger } = useAppServices();
  const labelCatalog = useProjectLabelCatalog();
  const filterGeneration = useBoardFilterGeneration();
  const stableOnDataViewChange = useStableCallback(onDataViewChange);
  const stableOnDataViewRelease = useStableCallback(onDataViewRelease);
  const stableOnReportColumnSnapshot = useStableCallback(onReportColumnSnapshot);
  const activeFilterGeneration = filterGeneration.snapshot.active;
  const cardsQuery = useBoardNodeCards(board.projectID, board.selectedWorkflow.id, column.id, true);
  const generationRef = useRef(0);
  const paginationInFlightRef = useRef(false);
  const queryCards = useMemo(
    () => cardsQuery.data?.pages.flatMap((page) => page.cards) ?? [],
    [cardsQuery.data?.pages],
  );
  const workspaceContext = useMemo(
    () => ({
      attachedWorkspaceCount: board.attachedWorkspaceCount,
      defaultWorkspaceID: board.defaultWorkspaceID,
    }),
    [board.attachedWorkspaceCount, board.defaultWorkspaceID],
  );
  const cardVMs = useMemo(
    () =>
      queryCards
        .map((card) => toKanbanCardVM(card, workspaceContext, labelCatalog.data ?? null))
        .filter((card) => cardBelongsToColumn(column, card)),
    [column, labelCatalog.data, queryCards, workspaceContext],
  );
  const {
    error,
    fetchNextPage,
    fetchPreviousPage,
    hasNextPage,
    hasPreviousPage,
    isError,
    isFetchNextPageError,
    isFetchPreviousPageError,
    isFetching,
    isFetchingNextPage,
    isFetchingPreviousPage,
    isPlaceholderData,
    isPending,
    refetch,
  } = cardsQuery;
  const requestEnabled = !activeFilterGeneration.retiring && filterGeneration.snapshot.desiredFilter === null;
  const paginationEnabled = requestEnabled && !isPlaceholderData && cardsQuery.data !== undefined;
  const replacementDataRetained = cardsQuery.data !== undefined && isPlaceholderData;
  const retryCards = useCallback(() => {
    const current = filterGeneration.controller.getSnapshot();
    if (
      current.active.generation !== activeFilterGeneration.generation ||
      current.active.retiring ||
      current.desiredFilter !== null ||
      !boardFiltersEqual(current.active.filter, activeFilterGeneration.filter)
    ) {
      return;
    }
    void refetch();
  }, [
    activeFilterGeneration.filter,
    activeFilterGeneration.generation,
    filterGeneration.controller,
    refetch,
  ]);
  const loadNewer = useCallback(() => {
    if (paginationEnabled && hasPreviousPage && !isFetchingPreviousPage) {
      void fetchPreviousPage();
    }
  }, [fetchPreviousPage, hasPreviousPage, isFetchingPreviousPage, paginationEnabled]);
  const loadOlder = useCallback(() => {
    if (paginationEnabled && hasNextPage && !isFetchingNextPage) {
      void fetchNextPage();
    }
  }, [fetchNextPage, hasNextPage, isFetchingNextPage, paginationEnabled]);
  const initialBoundary = useMemo<VirtualizedInfiniteListBoundaryState | undefined>(
    () =>
      cardsQuery.data === undefined
        ? isError
          ? {
              state: "error",
              message: t("board.cardsLoadRetryBody"),
              retryLabel: t("app.retry"),
              onRetry: retryCards,
            }
          : {
              state: "loading",
              label: t("states.loading"),
            }
        : undefined,
    [cardsQuery.data, isError, retryCards, t],
  );
  const previousBoundary = useMemo(
    () =>
      directionalBoundary({
        failed: isFetchPreviousPageError,
        loading: isFetchingPreviousPage,
        message: t("board.cardsLoadRetryBody"),
        loadingLabel: t("app.loadingMore"),
        onRetry: loadNewer,
        retryLabel: t("app.retry"),
      }),
    [isFetchPreviousPageError, isFetchingPreviousPage, loadNewer, t],
  );
  const nextBoundary = useMemo(
    () =>
      directionalBoundary({
        failed: isFetchNextPageError,
        loading: isFetchingNextPage,
        message: t("board.cardsLoadRetryBody"),
        loadingLabel: t("app.loadingMore"),
        onRetry: loadOlder,
        retryLabel: t("app.retry"),
      }),
    [isFetchNextPageError, isFetchingNextPage, loadOlder, t],
  );
  const replacementBoundary = useMemo<VirtualizedInfiniteListBoundaryState | undefined>(
    () =>
      isError && replacementDataRetained && requestEnabled
        ? {
            state: "error",
            message: t("board.cardsLoadRetryBody"),
            retryLabel: t("app.retry"),
            onRetry: retryCards,
          }
        : undefined,
    [isError, replacementDataRetained, requestEnabled, retryCards, t],
  );
  const dataView = useMemo<BoardColumnDataView>(
    () => ({
      cards: cardVMs,
      hasNextPage: paginationEnabled && hasNextPage,
      hasPreviousPage: paginationEnabled && hasPreviousPage,
      initialBoundary,
      isFetchingNextPage: paginationEnabled && isFetchingNextPage,
      isFetchingPreviousPage: paginationEnabled && isFetchingPreviousPage,
      nextBoundary: paginationEnabled ? nextBoundary : undefined,
      onLoadMore: loadOlder,
      onLoadPrevious: loadNewer,
      previousBoundary: paginationEnabled ? previousBoundary : undefined,
      replacementBoundary,
    }),
    [
      cardVMs,
      hasNextPage,
      hasPreviousPage,
      initialBoundary,
      isFetchingNextPage,
      isFetchingPreviousPage,
      loadOlder,
      loadNewer,
      nextBoundary,
      paginationEnabled,
      previousBoundary,
      replacementBoundary,
    ],
  );

  useLayoutEffect(() => {
    stableOnDataViewChange(dataView);
  }, [dataView, stableOnDataViewChange]);

  useEffect(() => {
    if (replacementBoundary?.state !== "error") {
      return;
    }
    void logger.append("warn", "Board task-card replacement failed.", {
      columnID: column.id,
      error: errorMessage(error),
      filterGeneration: activeFilterGeneration.generation.toString(),
      projectID: board.projectID,
      workflowID: board.selectedWorkflow.id,
    });
  }, [
    activeFilterGeneration.generation,
    board.projectID,
    board.selectedWorkflow.id,
    column.id,
    error,
    logger,
    replacementBoundary,
  ]);

  useEffect(() => {
    if (isFetchingPreviousPage || isFetchingNextPage || isFetchPreviousPageError || isFetchNextPageError) {
      paginationInFlightRef.current = true;
    }
  }, [isFetchNextPageError, isFetchPreviousPageError, isFetchingNextPage, isFetchingPreviousPage]);

  useEffect(() => {
    const cause: BoardColumnUpdateCause =
      paginationInFlightRef.current ? "pagination" : isPlaceholderData ? "hydration" : "domain";
    generationRef.current += 1;
    stableOnReportColumnSnapshot(column.id, {
      cause,
      data: {
        cards: cardVMs,
        generation: generationRef.current,
        hasData: cardsQuery.data !== undefined,
        isFetching,
        isSettled: !isPending && !isFetching,
        taskCount: column.taskCount,
      },
    });
    if (!isFetchingPreviousPage && !isFetchingNextPage) {
      paginationInFlightRef.current = false;
    }
  }, [
    cardVMs,
    cardsQuery.data,
    column.id,
    column.taskCount,
    isError,
    isFetching,
    isFetchingNextPage,
    isFetchingPreviousPage,
    isPending,
    isPlaceholderData,
    stableOnReportColumnSnapshot,
  ]);

  useEffect(() => {
    return () => {
      stableOnDataViewRelease();
      stableOnReportColumnSnapshot(column.id, { cause: "deactivation" });
    };
  }, [column.id, stableOnDataViewRelease, stableOnReportColumnSnapshot]);

  return null;
}

function directionalBoundary(
  input: Readonly<{
    failed: boolean;
    loading: boolean;
    loadingLabel: string;
    message: string;
    onRetry: () => void;
    retryLabel: string;
  }>,
): VirtualizedInfiniteListBoundaryState | undefined {
  if (input.failed) {
    return {
      state: "error",
      message: input.message,
      retryLabel: input.retryLabel,
      onRetry: input.onRetry,
    };
  }
  if (input.loading) {
    return { state: "loading", label: input.loadingLabel };
  }
  return undefined;
}
