import { useCallback, useEffect, useLayoutEffect, useMemo, useRef } from "react";
import { useTranslation } from "react-i18next";

import type { BoardColumn, SelectedWorkflowBoard } from "../../api";
import { errorMessage } from "../../api/errors";
import type { VirtualizedInfiniteListBoundaryState } from "../../ui";
import { cardBelongsToColumn } from "./BoardCardMotionModel";
import { toKanbanCardVM, type KanbanCardVM } from "./BoardColumnViewModel";
import { useBoardNodeCards } from "./useBoardData";
import { useObservedInterruptedRuns } from "./useObservedInterruptedRuns";

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
  nextBoundary: VirtualizedInfiniteListBoundaryState;
  onLoadMore: () => void;
  onLoadPrevious: () => void;
  previousBoundary: VirtualizedInfiniteListBoundaryState;
}>;

export function BoardColumnDataOwner({
  board,
  column,
  onCardsLoadError,
  onDataViewChange,
  onDataViewRelease,
  onInterruptedRunObserved,
  onReportColumnSnapshot,
}: Readonly<{
  board: SelectedWorkflowBoard;
  column: BoardColumn;
  onCardsLoadError: (error: unknown) => void;
  onDataViewChange: (view: BoardColumnDataView) => void;
  onDataViewRelease: () => void;
  onInterruptedRunObserved: (input: Readonly<{ runID: string; taskID: string }>) => void;
  onReportColumnSnapshot: (columnID: string, snapshot: BoardColumnQuerySnapshot) => void;
}>) {
  const { t } = useTranslation();
  const cardsQuery = useBoardNodeCards(board.projectID, board.selectedWorkflow.id, column.id, true);
  const generationRef = useRef(0);
  const hasHydratedRef = useRef(false);
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
        .map((card) => toKanbanCardVM(card, workspaceContext))
        .filter((card) => cardBelongsToColumn(column, card)),
    [column, queryCards, workspaceContext],
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
    isPending,
    refetch,
  } = cardsQuery;
  const retryInitial = useCallback(() => {
    void refetch();
  }, [refetch]);
  const loadNewer = useCallback(() => {
    if (hasPreviousPage && !isFetchingPreviousPage) {
      void fetchPreviousPage();
    }
  }, [fetchPreviousPage, hasPreviousPage, isFetchingPreviousPage]);
  const loadOlder = useCallback(() => {
    if (hasNextPage && !isFetchingNextPage) {
      void fetchNextPage();
    }
  }, [fetchNextPage, hasNextPage, isFetchingNextPage]);
  const initialBoundary = useMemo<VirtualizedInfiniteListBoundaryState | undefined>(
    () =>
      cardsQuery.data === undefined
        ? isError
          ? {
              state: "error",
              message: errorMessage(error),
              retryLabel: t("app.retry"),
              onRetry: retryInitial,
            }
          : {
              state: "loading",
              label: t("states.loading"),
            }
        : undefined,
    [cardsQuery.data, error, isError, retryInitial, t],
  );
  const previousBoundary = useMemo(
    () =>
      directionalBoundary({
        error,
        failed: isFetchPreviousPageError,
        loading: isFetchingPreviousPage,
        loadingLabel: t("app.loadingMore"),
        onRetry: loadNewer,
        retryLabel: t("app.retry"),
      }),
    [error, isFetchPreviousPageError, isFetchingPreviousPage, loadNewer, t],
  );
  const nextBoundary = useMemo(
    () =>
      directionalBoundary({
        error,
        failed: isFetchNextPageError,
        loading: isFetchingNextPage,
        loadingLabel: t("app.loadingMore"),
        onRetry: loadOlder,
        retryLabel: t("app.retry"),
      }),
    [error, isFetchNextPageError, isFetchingNextPage, loadOlder, t],
  );
  const dataView = useMemo<BoardColumnDataView>(
    () => ({
      cards: cardVMs,
      hasNextPage,
      hasPreviousPage,
      initialBoundary,
      isFetchingNextPage,
      isFetchingPreviousPage,
      nextBoundary,
      onLoadMore: loadOlder,
      onLoadPrevious: loadNewer,
      previousBoundary,
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
      previousBoundary,
    ],
  );

  useLayoutEffect(() => {
    onDataViewChange(dataView);
  }, [dataView, onDataViewChange]);

  useEffect(() => {
    if (isError) {
      onCardsLoadError(error);
    }
  }, [error, isError, onCardsLoadError]);

  useEffect(() => {
    if (isFetchingPreviousPage || isFetchingNextPage || isFetchPreviousPageError || isFetchNextPageError) {
      paginationInFlightRef.current = true;
    }
  }, [isFetchNextPageError, isFetchPreviousPageError, isFetchingNextPage, isFetchingPreviousPage]);

  useEffect(() => {
    const cause: BoardColumnUpdateCause = !hasHydratedRef.current
      ? "hydration"
      : paginationInFlightRef.current
        ? "pagination"
        : "domain";
    generationRef.current += 1;
    onReportColumnSnapshot(column.id, {
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
    if (cardsQuery.data !== undefined) {
      hasHydratedRef.current = true;
    }
    if (!isFetchingPreviousPage && !isFetchingNextPage) {
      paginationInFlightRef.current = false;
    }
  }, [
    cardVMs,
    cardsQuery.data,
    column.id,
    column.taskCount,
    isFetching,
    isFetchingNextPage,
    isFetchingPreviousPage,
    isPending,
    onReportColumnSnapshot,
  ]);

  useEffect(
    () => () => {
      onDataViewRelease();
      onReportColumnSnapshot(column.id, { cause: "deactivation" });
    },
    [column.id, onDataViewRelease, onReportColumnSnapshot],
  );

  useObservedInterruptedRuns(cardVMs, onInterruptedRunObserved);
  return null;
}

function directionalBoundary(
  input: Readonly<{
    error: unknown;
    failed: boolean;
    loading: boolean;
    loadingLabel: string;
    onRetry: () => void;
    retryLabel: string;
  }>,
): VirtualizedInfiniteListBoundaryState {
  if (input.failed) {
    return {
      state: "error",
      message: errorMessage(input.error),
      retryLabel: input.retryLabel,
      onRetry: input.onRetry,
    };
  }
  if (input.loading) {
    return { state: "loading", label: input.loadingLabel };
  }
  return { state: "idle" };
}
